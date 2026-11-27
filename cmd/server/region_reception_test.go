package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

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
			// These two reports really are direct. Older reports below still
			// exercise placement from their own path without an observation frame.
			db.hasObsRawHex = true
			mustExec(t, db, `UPDATE observations SET raw_hex=? WHERE transmission_id=1`, homeZeroHopDirect)
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

type regionReport struct {
	observer, direction, raw, path string
}

// Match the ingestor's content dedup: the first report supplies the
// transmission, but each distinct observer/path row keeps its own frame.
func TestNodeRegionPlacementUsesReceivedFrame(t *testing.T) {
	const floodNode, directNode = "aabbccdd11223344", "eeff00112233aabb"
	for _, order := range []string{"tx-first", "rx-first"} {
		for _, mode := range []string{"load", "background", "chunked", "new", "late"} {
			t.Run(order+"/"+mode, func(t *testing.T) {
				db := setupTestDB(t)
				defer db.Close()
				db.hasObsRawHex = true
				now := time.Now().UTC().Add(-time.Minute)
				mustExec(t, db, `INSERT INTO observers (id, iata) VALUES ('sender','SFO'),('near','AUS'),('far','PDX')`)
				type row struct {
					tx int
					regionReport
				}
				var later []row
				insert := func(r row) {
					t.Helper()
					mustExec(t, db, `INSERT INTO observations (transmission_id, observer_idx, direction, raw_hex, path_json, timestamp)
						SELECT ?, idx, ?, ?, ?, ? FROM observers WHERE id=?`, r.tx, r.direction, r.raw, r.path, now.Unix(), r.observer)
				}
				for i, pk := range []string{floodNode, directNode} {
					direct := regionReport{"sender", "tx", "130102030440" + pk, `[]`}
					flood := regionReport{"near", "rx", "1142aabbccdd" + pk, `["aabb","ccdd"]`}
					if pk == directNode {
						direct.observer, direct.direction = "near", "rx"
						flood.observer, flood.direction = "sender", "tx"
					}
					far := regionReport{"far", "rx", "1143aabbccddeeff" + pk, `["aabb","ccdd","eeff"]`}
					hash := ComputeContentHash(direct.raw)
					if hash != ComputeContentHash(flood.raw) || hash != ComputeContentHash(far.raw) {
						t.Fatal("fixture reports must identify the same content")
					}
					first, second := direct, flood
					if (first.direction == "tx") != (order == "tx-first") {
						first, second = second, first
					}
					mustExec(t, db, `INSERT INTO nodes (public_key, name, role) VALUES (?, ?, 'repeater')`, pk, pk)
					mustExec(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen, payload_type, decoded_json)
						VALUES (?, ?, ?, ?, 4, json_object('pubKey', ?))`, i+1, first.raw, hash, now.Format(time.RFC3339), pk)
					insert(row{i + 1, first})
					later = append(later, row{i + 1, second}, row{i + 1, far})
				}
				srv := NewServer(db, &Config{}, NewHub())
				s := NewPacketStore(db, nil)
				srv.store = s
				if mode == "late" {
					s.IngestNewFromDB(0, 10)
				}
				for _, r := range later {
					insert(r)
				}
				var err error
				switch mode {
				case "load":
					err = s.Load()
				case "background":
					err = s.loadChunk(now.Add(-time.Hour), now.Add(time.Hour))
				case "chunked":
					err = s.LoadChunked(1)
				case "new":
					s.IngestNewFromDB(0, 10)
				case "late":
					s.IngestNewObservations(2, 10)
				}
				if err != nil {
					t.Fatal(err)
				}
				s.recomputeNodeHomeRegionSets()
				router := mux.NewRouter()
				srv.RegisterRoutes(router)
				var body struct {
					Nodes []struct {
						Key       string              `json:"public_key"`
						Placement NodeRegionPlacement `json:"region_placement"`
					} `json:"nodes"`
				}
				getJSON(t, router, "/api/nodes", &body)
				got := make(map[string]NodeRegionPlacement)
				for _, n := range body.Nodes {
					got[n.Key] = n.Placement
				}
				want := map[string]NodeRegionPlacement{
					floodNode:  {Basis: RegionBasisFewestHops, Regions: []string{"AUS"}, Hops: intPtr(2)},
					directNode: {Basis: RegionBasisZeroHop, Regions: []string{"AUS"}},
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("placements %s, want %s", placementsString(got), placementsString(want))
				}
				// Both adverts reached PDX, but only directNode has a confirmed
				// home elsewhere. Hash-size analytics falls back to the receiving
				// observer's region for floodNode.
				var stats struct {
					Nodes []struct{ Pubkey string } `json:"multiByteNodes"`
				}
				getJSON(t, router, "/api/analytics/hash-sizes?region=PDX", &stats)
				if len(stats.Nodes) != 1 || stats.Nodes[0].Pubkey != floodNode {
					t.Errorf("PDX multi-byte nodes %v, want only %s", stats.Nodes, floodNode)
				}
			})
		}
	}
}

func TestNodeRegionPlacementIncompleteFrames(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	db.hasObsRawHex = true
	now := time.Now().UTC()
	mustExec(t, db, `INSERT INTO observers (id, iata) VALUES ('receiver','AUS'), ('sender','SFO')`)
	zero := NodeRegionPlacement{Basis: RegionBasisZeroHop, Regions: []string{"AUS"}}
	fewest := func(hops int) NodeRegionPlacement {
		return NodeRegionPlacement{Basis: RegionBasisFewestHops, Regions: []string{"AUS"}, Hops: intPtr(hops)}
	}
	scan := NodeRegionPlacement{Basis: RegionBasisObserverScan}
	const direct, flood, flood2 = "1240ab", "1140ab", "1142aabbccddab"
	cases := []struct {
		name, canonical string
		raw, path       any
		alsoTX          bool
		want            NodeRegionPlacement
	}{
		{"legacy-single", direct, nil, `[]`, false, zero},
		{"legacy-nonempty", direct, nil, `["aa","bb"]`, false, fewest(2)},
		{"grouped-legacy-empty", direct, nil, `[]`, true, fewest(0)},
		{"grouped-legacy-missing", direct, nil, nil, true, scan},
		{"received-flood-empty", direct, flood, `[]`, true, fewest(0)},
		{"received-direct-no-path-json", flood, direct, nil, true, zero},
		{"received-flood-no-path-json", direct, flood2, nil, true, fewest(2)},
		{"received-malformed", direct, "1240zz", `[]`, false, fewest(0)},
		{"received-truncated", direct, "1240", nil, false, scan},
		{"received-contradictory-path", direct, direct, `["aa","bb"]`, false, fewest(2)},
		{"malformed-path", flood, nil, "broken", true, scan},
	}
	for i, c := range cases {
		mustExec(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen, payload_type, decoded_json)
			VALUES (?, ?, ?, ?, 4, json_object('pubKey', ?))`, i+1, c.canonical, c.name, now.Format(time.RFC3339), c.name)
		if c.alsoTX {
			mustExec(t, db, `INSERT INTO observations (transmission_id, observer_idx, direction, raw_hex, path_json, timestamp)
				VALUES (?, 2, 'tx', ?, '[]', ?)`, i+1, c.canonical, now.Unix())
		}
		mustExec(t, db, `INSERT INTO observations (transmission_id, observer_idx, direction, raw_hex, path_json, timestamp)
			VALUES (?, 1, 'rx', ?, ?, ?)`, i+1, c.raw, c.path, now.Unix())
	}
	s := NewPacketStore(db, nil)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	s.recomputeNodeHomeRegionSets()
	got := s.GetNodeRegionPlacements()
	for _, c := range cases {
		if p := RegionPlacementFor(got, c.name); !reflect.DeepEqual(p, c.want) {
			t.Errorf("%s: %s, want %s", c.name, placementString(p), placementString(c.want))
		}
	}
}

// Older databases can hold multiple rows for an observer/path that the
// in-memory store folds together. Only a received direct frame can qualify.
func TestNodeRegionPlacementDuplicateReports(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	db.hasObsRawHex = true
	now := time.Now().UTC()
	mustExec(t, db, `INSERT INTO observers (id, iata) VALUES ('observer','AUS')`)
	const direct, flood = "1240ab", "1140ab"
	type report struct {
		direction string
		raw       any
	}
	directRX, directTX := report{"rx", direct}, report{"tx", direct}
	floodRX, floodTX := report{"rx", flood}, report{"tx", flood}
	cases := []struct {
		name          string
		first, second report
		basis         RegionBasis
	}{
		{"rx-flood-tx-direct", floodRX, directTX, RegionBasisFewestHops},
		{"tx-direct-rx-flood", directTX, floodRX, RegionBasisFewestHops},
		{"rx-direct-tx-flood", directRX, floodTX, RegionBasisZeroHop},
		{"tx-flood-rx-direct", floodTX, directRX, RegionBasisZeroHop},
		{"two-receptions", floodRX, directRX, RegionBasisZeroHop},
		{"two-transmissions", directTX, floodTX, RegionBasisObserverScan},
		{"tx-direct-rx-legacy", directTX, report{"rx", nil}, RegionBasisFewestHops},
	}
	for i, c := range cases {
		mustExec(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen, payload_type, decoded_json)
			VALUES (?, ?, ?, ?, 4, json_object('pubKey', ?))`, i+1, c.first.raw, c.name, now.Format(time.RFC3339), c.name)
		for j, r := range []report{c.first, c.second} {
			mustExec(t, db, `INSERT INTO observations (id, transmission_id, observer_idx, direction, raw_hex, path_json, timestamp)
				VALUES (?, ?, 1, ?, ?, '[]', ?)`, j*len(cases)+i+1, i+1, r.direction, r.raw, now.Unix())
		}
	}
	s := NewPacketStore(db, nil)
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	s.recomputeNodeHomeRegionSets()
	for _, c := range cases {
		p := RegionPlacementFor(s.GetNodeRegionPlacements(), c.name)
		if p.Basis != c.basis || (p.Basis == RegionBasisFewestHops && (p.Hops == nil || *p.Hops != 0)) {
			t.Errorf("%s: %s, want %s", c.name, placementString(p), c.basis)
		}
	}
}

// Four receptions per advert, 120-byte payloads and 1,024 advertisers. Most
// observations are ordinary floods; one in eight transmissions is direct.
func BenchmarkRegionAdvertReports(b *testing.B) {
	db := setupTestDB(b)
	defer db.Close()
	db.hasObsRawHex = true
	now := time.Now().UTC().Add(-time.Minute)
	exec := func(q string, args ...any) {
		b.Helper()
		if _, err := db.conn.Exec(q, args...); err != nil {
			b.Fatal(err)
		}
	}
	exec(`INSERT INTO observers (id, iata) VALUES ('a','AUS'),('b','PDX'),('c','SJC'),('d','SFO')`)
	exec(`WITH RECURSIVE ids(id) AS (VALUES(1) UNION ALL SELECT id+1 FROM ids WHERE id<4096)
		INSERT INTO transmissions (id, raw_hex, hash, first_seen, payload_type, decoded_json)
		SELECT id, (CASE WHEN id%8=0 THEN '1240' ELSE '1142aabbccdd' END) || ?, printf('%x',id), ?, 4,
		json_object('pubKey', printf('%016x',id%1024)) FROM ids`, strings.Repeat("ab", 120), now.Format(time.RFC3339))
	exec(`INSERT INTO observations (transmission_id, observer_idx, direction, raw_hex, path_json, timestamp)
		SELECT t.id, o.idx, 'rx', t.raw_hex, CASE WHEN t.id%8=0 THEN '[]' ELSE '["aabb","ccdd"]' END, ?
		FROM transmissions t CROSS JOIN observers o`, now.Unix())
	b.Run("load", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			s := NewPacketStore(db, nil)
			if err := s.LoadChunked(1024); err != nil {
				b.Fatal(err)
			}
		}
		b.ReportMetric(float64(unsafe.Sizeof(StoreObs{})), "bytes/obs")
	})
	s := NewPacketStore(db, nil)
	if err := s.LoadChunked(1024); err != nil {
		b.Fatal(err)
	}
	s.recomputeNodeHomeRegionSets()
	b.Run("recompute", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			s.recomputeNodeHomeRegionSets()
		}
	})
}
