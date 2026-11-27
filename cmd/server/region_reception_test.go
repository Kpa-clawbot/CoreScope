package main

import (
	"reflect"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// The sender reports each advert. Only the direct and relayed adverts also
// have receptions, including older reports with NULL or empty direction.
func TestNodeRegionsUseAdvertReceptions(t *testing.T) {
	const direct, relayed, txOnly = "aabbccdd11223344", "eeff00112233aabb", "1122334455667788"
	for _, mode := range []string{"sql-v2", "sql-v3", "store"} {
		t.Run(mode, func(t *testing.T) {
			var db *DB
			observerColumn, observerKey := "observer_idx", "idx"
			if mode == "sql-v2" {
				db = setupTestDBV2(t)
				observerColumn, observerKey = "observer_id", "id"
			} else {
				db = setupTestDB(t)
			}
			defer db.Close()
			now := time.Now().UTC()
			mustExec(t, db, `INSERT INTO observers (id, name, iata, first_seen, last_seen) VALUES
				('sender','Sender','SFO',?1,?1), ('receiver','Receiver','SJC',?1,?1), ('legacy','Legacy','PDX',?1,?1)`, now.Format(time.RFC3339))
			for i, pk := range []string{direct, relayed, txOnly} {
				raw := homeZeroHopDirect
				if pk == relayed {
					raw = homeRelayedFlood
				}
				mustExec(t, db, `INSERT INTO nodes (public_key, name, role) VALUES (?, ?, 'repeater')`, pk, pk)
				mustExec(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen, payload_type, decoded_json)
					VALUES (?, ?, ?, ?, 4, json_object('pubKey', ?))`, i+1, raw, pk, now.Format(time.RFC3339), pk)
			}
			report := func(tx int, observer string, direction any, path string) {
				t.Helper()
				mustExec(t, db, `INSERT INTO observations (transmission_id, `+observerColumn+`, direction, path_json, timestamp)
					SELECT ?, `+observerKey+`, ?, ?, ? FROM observers WHERE id = ?`, tx, direction, path, now.Unix(), observer)
			}
			report(1, "sender", "tx", `[]`)
			report(1, "receiver", "rx", `[]`)
			report(2, "sender", "tx", `[]`)
			report(2, "receiver", "", `["aa","bb","cc"]`)
			report(2, "legacy", nil, `["aa","bb"]`)
			report(3, "sender", "tx", `[]`)

			srv := NewServer(db, &Config{}, NewHub())
			router := mux.NewRouter()
			srv.RegisterRoutes(router)
			before := NodeRegionPlacement{} // SQL-only responses omit region_placement.
			if mode == "store" {
				srv.store = NewPacketStore(db, nil)
				if err := srv.store.Load(); err != nil {
					t.Fatal(err)
				}
				if !srv.store.WaitIndexesReady(5 * time.Second) {
					t.Fatal("store indexes did not finish loading")
				}
				before.Basis = RegionBasisObserverScan
			}
			checkNodes := func(url string, want map[string]NodeRegionPlacement) {
				t.Helper()
				var body struct {
					Nodes []struct {
						Key       string              `json:"public_key"`
						Placement NodeRegionPlacement `json:"region_placement"`
					} `json:"nodes"`
					Total int `json:"total"`
				}
				getJSON(t, router, url, &body)
				got := make(map[string]NodeRegionPlacement)
				for _, node := range body.Nodes {
					got[node.Key] = node.Placement
				}
				if body.Total != len(want) || !reflect.DeepEqual(got, want) {
					t.Errorf("%s: total %d, placements %v; want %v", url, body.Total, placementsString(got), placementsString(want))
				}
			}
			checkNodes("/api/nodes?region=SFO", map[string]NodeRegionPlacement{})
			checkNodes("/api/nodes?region=SJC", map[string]NodeRegionPlacement{direct: before, relayed: before})
			checkNodes("/api/nodes?region=PDX", map[string]NodeRegionPlacement{relayed: before})
			if mode != "store" {
				checkNodes("/api/nodes", map[string]NodeRegionPlacement{direct: {}, relayed: {}, txOnly: {}})
				return
			}

			srv.store.recomputeNodeHomeRegionSets()
			nearest := NodeRegionPlacement{Basis: RegionBasisFewestHops, Regions: []string{"PDX"}, Hops: intPtr(2)}
			checkNodes("/api/nodes", map[string]NodeRegionPlacement{
				direct:  {Basis: RegionBasisZeroHop, Regions: []string{"SJC"}},
				relayed: nearest,
				txOnly:  {Basis: RegionBasisObserverScan},
			})
			checkNodes("/api/nodes?region=PDX,SFO", map[string]NodeRegionPlacement{relayed: nearest})

			// Hash-size analytics uses a single home (the zero-hop plurality).
			// Counting the sender would give SFO the tie with SJC.
			for _, region := range []string{"SFO", "SJC"} {
				var stats struct {
					Nodes []struct{ Pubkey string } `json:"multiByteNodes"`
				}
				getJSON(t, router, "/api/analytics/hash-sizes?region="+region, &stats)
				found := false
				for _, node := range stats.Nodes {
					found = found || node.Pubkey == direct
				}
				if found != (region == "SJC") {
					t.Errorf("hash-size analytics in %s: direct advertiser present = %v", region, found)
				}
			}
		})
	}
}
