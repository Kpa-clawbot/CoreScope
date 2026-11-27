package main

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

func TestObserverComparisonLegacyTimestamps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	createTestDBAt(t, path, 0) // observer_id and TEXT timestamps
	rw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	defer rw.Close()
	if _, err := rw.Exec(`CREATE INDEX idx_observations_timestamp ON observations(timestamp);
		INSERT INTO observers(id) VALUES ('ref'), ('peer')`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	offset := time.FixedZone("UTC-10", -10*60*60)
	samples := []any{
		now.Add(-time.Minute).Format("2006-01-02T15:04:05.000Z"),
		now.Add(-time.Hour).Format("2006-01-02 15:04:05"),
		now.Add(-24*time.Hour + time.Minute).In(offset).Format(time.RFC3339),
		now.Add(-2 * time.Hour).Unix(), // Unix seconds in a TEXT-affinity column
		now.Add(-24*time.Hour - time.Minute).Format(time.RFC3339),
		now.Add(time.Hour).In(offset).Format(time.RFC3339),
	}
	for i, ts := range samples {
		if _, err := rw.Exec(`INSERT INTO observations(transmission_id,observer_id,direction,snr,timestamp)
			VALUES (?,'ref','rx',-4,?), (?,'peer','rx',2,?)`, i+1, ts, i+1, ts); err != nil {
			t.Fatal(err)
		}
	}
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	router := mux.NewRouter()
	NewServer(db, &Config{}, NewHub()).RegisterRoutes(router)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/analytics/observer-comparison?ref=ref", nil))
	var response struct {
		ReferenceTotal int
		Peers          []struct {
			Total, Shared, SNRPairs int
			SNRDelta                *float64
		}
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.ReferenceTotal != 4 || len(response.Peers) != 1 {
		t.Fatalf("legacy window: want four shared transmissions, got HTTP %d %s", w.Code, w.Body.String())
	}
	peer := response.Peers[0]
	if peer.Total != 4 || peer.Shared != 4 || peer.SNRPairs != 4 || peer.SNRDelta == nil || *peer.SNRDelta != 6 {
		t.Fatalf("legacy receptions: want four pairs at +6 dB, got %s", w.Body.String())
	}
}

func TestObserverComparison(t *testing.T) {
	for _, version := range []string{"v2", "v3"} {
		t.Run(version, func(t *testing.T) {
			var db *DB
			receiver, obsKey := "observer_id", "id"
			if version == "v3" {
				db = setupTestDB(t)
				receiver, obsKey = "observer_idx", "idx"
			} else {
				db = setupTestDBV2(t)
			}
			defer db.Close()
			mustExec(t, db, `INSERT INTO observers (id, name, iata, inactive) VALUES
				('ref', 'Reference', 'AAA', 0), ('peer', 'Peer', 'BBB', NULL),
				('equal', 'Equal', 'AAA', 0), ('quiet', 'Quiet', 'BBB', 0),
				('hidden', 'Hidden', 'BBB', 0), ('inactive', 'Inactive', 'BBB', 1)`)
			mustExec(t, db, `WITH RECURSIVE n(id) AS (VALUES(1) UNION ALL SELECT id+1 FROM n WHERE id<9)
				INSERT INTO transmissions (id,raw_hex,hash,first_seen) SELECT id,'00',id,datetime('now') FROM n`)
			now := time.Now().Unix()
			add := func(tx int, id string, direction, snr any, at int64) {
				t.Helper()
				mustExec(t, db, `INSERT INTO observations (transmission_id, `+receiver+`, direction, snr, timestamp, path_json)
					SELECT ?, `+obsKey+`, ?, ?, ?, json_array(printf('%02x', ? % 256)) FROM observers WHERE id=?`, tx, direction, snr, at, at, id)
			}
			// Shared 1 averages to -6 at the reference and +1 at the peer.
			// Shared 2 gives -5 dB. Equal weight per transmission gives (+7 - 5)/2 = +1.
			add(1, "ref", nil, -8, now-6)
			add(1, "ref", "rx", -4, now-5)
			add(1, "peer", "rx", 0, now-4)
			add(1, "peer", "", 2, now-3)
			add(1, "peer", "tx", 70, now-2)
			add(1, "equal", "rx", -6, now-1)
			add(2, "ref", "rx", 4, now-1)
			add(2, "peer", nil, -1, now-1)
			add(3, "ref", "rx", 5, now-1)
			add(3, "peer", "rx", nil, now-1)
			add(4, "ref", "rx", nil, now-1)
			add(4, "peer", "rx", 6, now-1)
			add(5, "ref", "rx", 9, now-1)
			add(5, "peer", "tx", 7, now-1)
			add(6, "ref", "tx", 7, now-1)
			add(6, "peer", "rx", 2, now-1)
			add(7, "peer", "rx", 4, now-1)
			for _, id := range []string{"ref", "peer"} {
				add(8, id, "rx", 1, now-25*3600)
				add(9, id, "rx", 1, now+3600)
			}

			srv := NewServer(db, &Config{ObserverBlacklist: []string{"HIDDEN"}}, NewHub())
			router := mux.NewRouter()
			srv.RegisterRoutes(router)
			get := func(query string, status int) []byte {
				t.Helper()
				w := httptest.NewRecorder()
				router.ServeHTTP(w, httptest.NewRequest("GET", "/api/analytics/observer-comparison"+query, nil))
				if w.Code != status {
					t.Fatalf("comparison%s returned %d, want %d: %s", query, w.Code, status, w.Body.String())
				}
				return w.Body.Bytes()
			}
			check := func(query, expected string) {
				t.Helper()
				var got, want any
				body := get(query, 200)
				if err := json.Unmarshal(body, &got); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(expected), &want); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("%s: got %s, want %s", query, body, expected)
				}
			}
			const peer = `{"id":"peer","name":"Peer","total":6,"shared":4,"snrPairs":2,"snrDelta":1}`
			const equal = `{"id":"equal","name":"Equal","total":1,"shared":1,"snrPairs":1,"snrDelta":0}`
			const quiet = `{"id":"quiet","name":"Quiet","total":0,"shared":0,"snrPairs":0,"snrDelta":null}`
			check("?ref=ref", `{"referenceTotal":5,"peers":[`+peer+","+equal+","+quiet+`]}`)
			// Peer regions do not change the pinned reference or its denominator.
			check("?ref=ref&region=bbb", `{"referenceTotal":5,"peers":[`+peer+","+quiet+`]}`)
			check("?ref=quiet&region=BBB", `{"referenceTotal":0,"peers":[{"id":"peer","name":"Peer","total":6,"shared":0,"snrPairs":0,"snrDelta":null}]}`)
			get("", 400)
			for _, ref := range []string{"unknown", "hidden", "inactive"} {
				get("?ref="+ref, 404)
			}
		})
	}
}
