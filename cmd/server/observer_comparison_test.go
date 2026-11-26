package main

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

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
