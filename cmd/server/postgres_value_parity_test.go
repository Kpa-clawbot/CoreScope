package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

func openPostgresValueFixture(t *testing.T) (*DB, *sql.DB) {
	t.Helper()
	dsn := postgresTestDSN(t)
	writer, err := openFixtureSQL(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { writer.Close() })
	if err := applyTestSchema(t, writer); err != nil {
		t.Fatal(err)
	}
	reader, err := openFixtureReader(t, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reader.Close() })
	return reader, writer
}

// SQLite INTEGER affinity permits fractional values, and PostgreSQL float8
// returns large integral values in a form that NullInt64 cannot scan. Neither
// case may drop an observation or its joined transmission from the server.
func TestPostgresObservationScoreLoadPaths(t *testing.T) {
	for _, path := range []string{"full", "chunked", "older", "transmission_poll", "observation_poll"} {
		t.Run(path, func(t *testing.T) {
			db, writer := openPostgresValueFixture(t)
			stamp := time.Now().UTC().Truncate(time.Second).Add(-2 * time.Hour)
			if _, err := writer.Exec(`INSERT INTO observers(rowid,id,name,iata) VALUES(1,'score-observer','Score observer','AAA')`); err != nil {
				t.Fatal(err)
			}
			scores := []any{1000000, 3.5, nil}
			seedTx := func() {
				for i := range scores {
					if _, err := writer.Exec(`INSERT INTO transmissions(id,raw_hex,hash,first_seen,last_seen,payload_type) VALUES($1,'AA',$2,$3,$4,0)`, i+1, fmt.Sprintf("score-%d", i), stamp.Format(time.RFC3339), stamp.Unix()); err != nil {
						t.Fatal(err)
					}
				}
			}
			seedObs := func() {
				for i, score := range scores {
					if _, err := writer.Exec(`INSERT INTO observations(id,transmission_id,observer_idx,score,snr,rssi,path_json,timestamp) VALUES($1,$1,1,$2,3.5,-90.5,'[]',$3)`, i+1, score, stamp.Unix()); err != nil {
						t.Fatal(err)
					}
				}
			}
			if path != "transmission_poll" {
				seedTx()
				if path != "observation_poll" {
					seedObs()
				}
			}
			cfg := &PacketStoreConfig{RetentionHours: 24}
			if path == "older" {
				cfg.HotStartupHours = 1
			}
			store := NewPacketStore(db, cfg)
			if path == "chunked" {
				if err := store.LoadChunked(1); err != nil {
					t.Fatal(err)
				}
			} else if err := store.Load(); err != nil {
				t.Fatal(err)
			}
			switch path {
			case "older":
				if len(store.packets) != 0 {
					t.Fatal("older-history fixture unexpectedly loaded during hot startup")
				}
				if err := store.loadChunk(stamp.Add(-time.Hour), stamp.Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
			case "transmission_poll":
				seedTx()
				seedObs()
				packets, cursor := store.IngestNewFromDB(0, 10)
				if len(packets) != 3 || cursor != 3 {
					t.Errorf("poll returned %d transmissions, cursor %d; want 3 and 3", len(packets), cursor)
				}
			case "observation_poll":
				seedObs()
				if packets := store.IngestNewObservations(0, 10); len(packets) != 3 {
					t.Errorf("observation poll updated %d transmissions; want 3", len(packets))
				}
			}
			if len(store.packets) != 3 || store.totalObs != 3 {
				t.Errorf("loaded %d transmissions/%d observations; want 3/3", len(store.packets), store.totalObs)
			}
			want := map[string]string{"score-0": "1000000", "score-1": "3.5", "score-2": "null"}
			for hash, value := range want {
				observations := store.GetObservationsForHash(hash)
				if len(observations) != 1 {
					t.Errorf("%s loaded %d observations; want 1", hash, len(observations))
					continue
				}
				raw, err := json.Marshal(observations[0]["score"])
				if err != nil || string(raw) != value {
					t.Errorf("%s score JSON=%s, %v; want %s", hash, raw, err, value)
				}
			}
			// Exercise the existing HTTP field that exposes enriched scores.
			server := &Server{db: db, store: store}
			req := mux.SetURLVars(httptest.NewRequest("GET", "/api/observers/score-observer/analytics?days=1", nil), map[string]string{"id": "score-observer"})
			response := httptest.NewRecorder()
			server.handleObserverAnalytics(response, req)
			var body struct {
				RecentPackets []struct {
					Hash  string          `json:"hash"`
					Score json.RawMessage `json:"score"`
				} `json:"recentPackets"`
			}
			if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &body) != nil {
				t.Fatalf("observer analytics response: %d %s", response.Code, response.Body.String())
			}
			if len(body.RecentPackets) != len(want) {
				t.Errorf("HTTP API returned %d observations; want %d", len(body.RecentPackets), len(want))
			}
			for _, packet := range body.RecentPackets {
				if expected, ok := want[packet.Hash]; !ok || string(packet.Score) != expected {
					t.Errorf("HTTP score %s=%s; want JSON number/null %s", packet.Hash, packet.Score, expected)
				}
			}
		})
	}
}

func TestPostgresPacketRowScore(t *testing.T) {
	db, writer := openPostgresValueFixture(t)
	for i, score := range []any{1000000, 3.5, nil} {
		if _, err := writer.Exec(`INSERT INTO transmissions(id,raw_hex,hash,first_seen) VALUES($1,'AA',$2,'2026-01-01T00:00:00Z')`, i+1, fmt.Sprintf("score-%d", i)); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Exec(`INSERT INTO observations(id,transmission_id,score,timestamp) VALUES($1,$1,$2,1)`, i+1, score); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.conn.Query(`SELECT id,raw_hex,timestamp,observer_id,observer_name,direction,snr,rssi,score,hash,route_type,payload_type,payload_version,path_json,decoded_json,created_at FROM packets_v ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := []string{"1000000", "3.5", "null"}
	index := 0
	for rows.Next() {
		packet := scanPacketRow(rows)
		if packet == nil {
			t.Errorf("row %d silently dropped its score", index)
		} else if raw, err := json.Marshal(packet["score"]); err != nil || string(raw) != want[index] {
			t.Errorf("row %d score=%s, %v; want %s", index, raw, err, want[index])
		}
		index++
	}
	if rows.Err() != nil || index != len(want) {
		t.Fatalf("scanned %d rows, %v", index, rows.Err())
	}
}

func TestPostgresNodeSearchKeepsLiteralBackslashAndWildcards(t *testing.T) {
	db, writer := openPostgresValueFixture(t)
	for i, name := range []string{`node\path`, "nodepath", "nodeXpath"} {
		if _, err := writer.Exec(`INSERT INTO nodes(public_key,name) VALUES($1,$2)`, fmt.Sprintf("%064x", i+1), name); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		query string
		count int
	}{{`node\path`, 1}, {`NODE\PATH`, 1}, {`node\%`, 1}, {"node_path", 2}, {"node%path", 3}} {
		t.Run(tc.query, func(t *testing.T) {
			nodes, total, _, err := db.GetNodes(NodeQuery{Search: tc.query})
			if err != nil || len(nodes) != tc.count || total != tc.count {
				t.Errorf("GetNodes(%q): %d rows,total=%d,%v; want %d", tc.query, len(nodes), total, err, tc.count)
			}
			if tc.count == 1 && len(nodes) == 1 && nodes[0]["name"] != `node\path` {
				t.Errorf("GetNodes(%q) matched %v instead of the literal backslash name", tc.query, nodes[0]["name"])
			}
			nodes, err = db.SearchNodes(tc.query, 10)
			if err != nil || len(nodes) != tc.count {
				t.Errorf("SearchNodes(%q): %d rows,%v; want %d", tc.query, len(nodes), err, tc.count)
			}
			if tc.count == 1 && len(nodes) == 1 && nodes[0]["name"] != `node\path` {
				t.Errorf("SearchNodes(%q) matched %v instead of the literal backslash name", tc.query, nodes[0]["name"])
			}
		})
	}
}
