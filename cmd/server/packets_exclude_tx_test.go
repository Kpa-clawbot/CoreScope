package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sort"
	"testing"
	"time"
)

// #667: reception comparisons must match observer and direction on the same
// observation, not on the transmission's globally selected display observation.
func TestPacketsExcludeTx(t *testing.T) {
	for _, mode := range []string{"memory", "sql-v2", "sql-v3", "fallback"} {
		t.Run(mode, func(t *testing.T) {
			db := setupTestDB(t)
			if mode == "sql-v2" {
				db.Close()
				db = setupTestDBV2(t)
			}
			t.Cleanup(func() { db.Close() })
			now := time.Now().UTC().Truncate(time.Second)
			seen := now.Add(-time.Hour)
			const node = "aabbccdd11223344"
			mustExec := func(query string, args ...any) {
				t.Helper()
				if _, err := db.conn.Exec(query, args...); err != nil {
					t.Fatal(err)
				}
			}
			mustExec("INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, 'Fixture node', 'repeater', ?)", node, seen.Format(time.RFC3339))
			mustExec("INSERT INTO observers (id, name, iata) VALUES ('a', 'Observer A', 'AAA'), ('b', 'Observer B', 'BBB')")
			for _, hash := range []string{"a-tx-b-rx", "a-rx-b-tx", "a-tx-and-rx", "legacy-empty", "legacy-null", "tx-only"} {
				mustExec(`INSERT INTO transmissions (hash, raw_hex, first_seen, route_type, payload_type, decoded_json, from_pubkey)
					VALUES (?, '1200', ?, 1, 4, '{"pubKey":"aabbccdd11223344","type":"ADVERT"}', ?)`, hash, seen.Format(time.RFC3339), node)
			}
			addObs := func(tx int, observer string, direction any, path string) {
				t.Helper()
				if db.isV3 {
					mustExec(`INSERT INTO observations (transmission_id, observer_idx, direction, path_json, timestamp)
						VALUES (?, (SELECT rowid FROM observers WHERE id = ?), ?, ?, ?)`, tx, observer, direction, path, seen.Unix())
				} else {
					mustExec(`INSERT INTO observations (transmission_id, observer_id, direction, path_json, timestamp)
						VALUES (?, ?, ?, ?, ?)`, tx, observer, direction, path, seen.Unix())
				}
			}
			addObs(1, "a", "tx", `[]`)
			addObs(1, "b", "rx", `["11","22"]`) // representative RX must not count as A's reception
			addObs(2, "a", "rx", `[]`)
			addObs(2, "b", "tx", `["11","22"]`) // representative TX must not hide A's reception
			addObs(3, "a", "tx", `["11"]`)
			addObs(3, "a", "rx", `[]`)
			addObs(4, "a", "", `[]`)
			addObs(5, "a", nil, `[]`)
			addObs(6, "a", "tx", `[]`)

			srv := NewServer(db, &Config{}, NewHub())
			suffix := ""
			if mode == "memory" || mode == "fallback" {
				store := NewPacketStore(db, nil)
				if err := store.Load(); err != nil {
					t.Fatal(err)
				}
				if !store.WaitIndexesReady(5 * time.Second) {
					t.Fatal("packet indexes did not become ready")
				}
				store.oldestLoaded = ""
				if mode == "fallback" {
					store.oldestLoaded = seen.Format(time.RFC3339)
					suffix = "&since=" + url.QueryEscape(now.Add(-2*time.Hour).Format(time.RFC3339))
				}
				srv.store = store
			}

			all := []string{"a-tx-b-rx", "a-rx-b-tx", "a-tx-and-rx", "legacy-empty", "legacy-null", "tx-only"}
			rxA := []string{"a-rx-b-tx", "a-tx-and-rx", "legacy-empty", "legacy-null"}
			for _, tc := range []struct {
				name, query string
				want        []string
				total       int
			}{
				{"default preserves TX", "observer=a", all, 6},
				{"false preserves TX", "observer=a&excludeTx=false", all, 6},
				{"observer reception", "observer=a&excludeTx=true", rxA, 4},
				{"other observer", "observer=b&excludeTx=true", []string{"a-tx-b-rx"}, 1},
				{"multiple observers", "observer=a,b&excludeTx=true", all[:5], 5},
				{"unscoped", "excludeTx=true", all[:5], 5},
				{"hash shortcut", "hash=tx-only&excludeTx=true", []string{}, 0},
				{"node shortcut", "node=" + node + "&excludeTx=true", all[:5], 5},
				{"pagination after exclusion", "observer=a&excludeTx=true&limit=2&offset=1", rxA[1:3], 4},
				{"grouped default warms cache", "observer=a&groupByHash=true", all, 6},
				{"grouped reception isolates cache", "observer=a&groupByHash=true&excludeTx=true", rxA, 4},
				{"grouped default unchanged", "observer=a&groupByHash=true", all, 6},
			} {
				t.Run(tc.name, func(t *testing.T) {
					w := httptest.NewRecorder()
					srv.handlePackets(w, httptest.NewRequest(http.MethodGet, "/api/packets?order=asc&"+tc.query+suffix, nil))
					if w.Code != http.StatusOK {
						t.Fatalf("status %d: %s", w.Code, w.Body.String())
					}
					var got struct {
						Packets []struct{ Hash string }
						Total   int
					}
					if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
						t.Fatal(err)
					}
					hashes := make([]string, 0, len(got.Packets))
					for _, p := range got.Packets {
						hashes = append(hashes, p.Hash)
					}
					want := append([]string{}, tc.want...)
					sort.Strings(hashes)
					sort.Strings(want)
					if !reflect.DeepEqual(hashes, want) || got.Total != tc.total {
						t.Errorf("packets = %v (total %d), want %v (total %d)", hashes, got.Total, tc.want, tc.total)
					}
				})
			}
		})
	}
}

func TestPacketsExcludeTxGroupedCacheCannotAliasRadio(t *testing.T) {
	srv, router := setupTestServer(t)
	t.Cleanup(func() { srv.db.Close() })
	for _, tc := range []struct {
		query string
		total int
	}{
		{"", 3},
		{"&radio=%7CexcludeTx", 0},
		{"&excludeTx=true", 3},
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/packets?groupByHash=true&observer=obs1"+tc.query, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		var got PacketResult
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Total != tc.total || len(got.Packets) != tc.total {
			t.Errorf("query %q: got %d packets (total %d), want %d; filters must have separate cache entries", tc.query, len(got.Packets), got.Total, tc.total)
		}
	}
}
