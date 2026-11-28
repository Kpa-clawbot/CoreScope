package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// nodeCoordFixture is a server over a DB holding the given nodes (all
// repeaters, lat/lon NULL when 0,0), with fuzz installed as main does
// (Config and DB) or nil for the feature off.
func nodeCoordFixture(t *testing.T, fuzz *locationFuzzer, nodes []visNode) (*Server, http.Handler) {
	t.Helper()
	db := setupTestDB(t)
	cfg := &Config{Port: 3000}
	cfg.setLocationFuzzer(fuzz)
	srv := NewServer(db, cfg, NewHub())
	base := time.Now().UTC().Add(-time.Minute)
	for i, n := range nodes {
		var lat, lon interface{}
		if n.lat != 0 || n.lon != 0 {
			lat, lon = n.lat, n.lon
		}
		seen := base.Add(-time.Duration(i) * time.Second).Format(time.RFC3339)
		if _, err := db.conn.Exec(`INSERT INTO nodes
			(public_key, name, role, lat, lon, last_seen, first_seen, advert_count, foreign_advert)
			VALUES (?, ?, 'repeater', ?, ?, ?, ?, 1, 0)`, n.pubkey, n.name, lat, lon, seen, seen); err != nil {
			t.Fatal(err)
		}
	}
	store := NewPacketStore(db, nil)
	store.config = cfg
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if !store.WaitIndexesReady(5 * time.Second) {
		t.Fatal("background indexes never became ready")
	}
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)
	return srv, router
}

// nodeCoordRow reads lat/lon of one row of an API answer.
func nodeCoordRow(t *testing.T, row map[string]interface{}) (lat, lon interface{}) {
	t.Helper()
	return row["lat"], row["lon"]
}

// edgeNodes finds, deterministically, a node 55 m inside the north edge of
// visTestPolygon (lat 31) whose approximate position is outside it, and one
// 55 m outside whose approximate position is inside.
func edgeNodes(t *testing.T, f *locationFuzzer) (in, out visNode) {
	t.Helper()
	const edge, d = 31.0, 0.0005
	var haveIn, haveOut bool
	// Every key has its own grid and offsets: try key after key, walking the
	// position along the edge (inside visTestPolygon's lon -98..-97).
	for i := 0; i < 1200 && !(haveIn && haveOut); i++ {
		pk := fmt.Sprintf("ed%062x", i)
		lon := -97.9 + float64(i)*0.0008
		if !haveIn {
			if la, _, _ := f.Fuzz(pk, edge-d, lon); la > edge {
				in, haveIn = visNode{pubkey: pk, name: "Edge inside", lat: edge - d, lon: lon, visible: true}, true
				continue
			}
		}
		if !haveOut {
			if la, _, _ := f.Fuzz(pk, edge+d, lon); la < edge {
				out, haveOut = visNode{pubkey: pk, name: "Edge outside", lat: edge + d, lon: lon}, true
			}
		}
	}
	if !haveIn || !haveOut {
		t.Fatal("no edge nodes found")
	}
	return in, out
}

// The geo_filter classifies by the exact position even with location
// privacy on: a node just inside the polygon whose approximate position is
// outside stays visible, one just outside whose approximate position is
// inside stays hidden — on /api/nodes, in the planner and in route LOS.
// The served position is still the approximate one.
func TestNodeCoordGeoFilterUsesExactPosition(t *testing.T) {
	f := newTestLocFuzzer(t, 1000, testLocSecret)
	in, out := edgeNodes(t, f)
	srv, router := nodeCoordFixture(t, f, []visNode{in, out})
	srv.setGeoFilter(visTestPolygon())

	// /api/nodes
	req := httptest.NewRequest("GET", "/api/nodes?limit=50", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/api/nodes: %d %s", w.Code, w.Body.String())
	}
	var page struct {
		Nodes []map[string]interface{} `json:"nodes"`
		Total int                      `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Nodes) != 1 || page.Nodes[0]["public_key"] != in.pubkey || page.Total != 1 {
		t.Fatalf("/api/nodes = %v (total %d), want only %s", page.Nodes, page.Total, in.pubkey)
	}
	wantLat, wantLon, _ := f.Fuzz(in.pubkey, in.lat, in.lon)
	if lat, lon := nodeCoordRow(t, page.Nodes[0]); lat != wantLat || lon != wantLon {
		t.Fatalf("/api/nodes serves %v,%v, want the approximate %v,%v", lat, lon, wantLat, wantLon)
	}
	if wantLat <= 31 {
		t.Fatalf("fixture: approximate lat %v is inside the polygon", wantLat)
	}

	// Node cache rule (planner, route LOS).
	all, _ := srv.store.getCachedNodesAndPM()
	rule := srv.visibleNodeInfo()
	var visible []string
	for i := range all {
		if all[i].Lat == in.lat || all[i].Lat == out.lat {
			t.Fatalf("node cache holds the exact position of %s", all[i].PublicKey)
		}
		if rule(&all[i]) {
			visible = append(visible, all[i].PublicKey)
		}
	}
	if !slices.Equal(visible, []string{in.pubkey}) {
		t.Fatalf("node-cache rule keeps %v, want only %s", visible, in.pubkey)
	}
	d := serverPlannerData{srv}
	var known []string
	for _, k := range buildPlannerKnown(all, d.filter(), d.filterGen(), time.Now()).infra {
		known = append(known, k.key)
	}
	if !slices.Equal(known, []string{in.pubkey}) {
		t.Fatalf("planner known set %v, want only %s", known, in.pubkey)
	}
	svc := newElevationService(nil)
	svc.nodes = func() []nodeInfo { ns, _ := srv.store.getCachedNodesAndPM(); return ns }
	svc.visibility = srv
	if !svc.known(elevKey(geoPoint{wantLat, wantLon})) {
		t.Fatal("route LOS does not know the visible node's approximate position")
	}
	if svc.known(elevKey(geoPoint{in.lat, in.lon})) {
		t.Fatal("route LOS knows the visible node's exact position")
	}
	oLat, oLon, _ := f.Fuzz(out.pubkey, out.lat, out.lon)
	if svc.known(elevKey(geoPoint{oLat, oLon})) {
		t.Fatal("route LOS knows the hidden node")
	}
}

// Every reader of node coordinates serves the approximate position with the
// feature on and the stored one with it off; the geo-filter readers always
// read the stored one.
func TestNodeCoordReadersServeApproximatePositions(t *testing.T) {
	nodes := []visNode{
		{pubkey: fmt.Sprintf("aa%062x", 1), name: "One", lat: 30.123456, lon: -97.654321},
		{pubkey: fmt.Sprintf("aa%062x", 2), name: "Two", lat: 30.654321, lon: -97.123456},
		{pubkey: fmt.Sprintf("aa%062x", 3), name: "No fix"},
	}
	for _, on := range []bool{false, true} {
		t.Run(fmt.Sprintf("privacy=%v", on), func(t *testing.T) {
			var f *locationFuzzer
			if on {
				f = newTestLocFuzzer(t, 1000, testLocSecret)
			}
			srv, _ := nodeCoordFixture(t, f, nodes)
			db := srv.db
			want := func(n visNode) (interface{}, interface{}) {
				if n.lat == 0 && n.lon == 0 {
					return nil, nil
				}
				la, lo, _ := f.Fuzz(n.pubkey, n.lat, n.lon)
				if on && (la == n.lat || lo == n.lon) {
					t.Fatalf("fuzzer left %s in place", n.pubkey)
				}
				return la, lo
			}
			check := func(what string, pk string, lat, lon interface{}) {
				t.Helper()
				for _, n := range nodes {
					if n.pubkey != pk {
						continue
					}
					wl, wo := want(n)
					if lat != wl || lon != wo {
						t.Errorf("%s %s: %v,%v want %v,%v", what, pk, lat, lon, wl, wo)
					}
					return
				}
				t.Errorf("%s: unknown node %s", what, pk)
			}

			list, _, _, err := db.GetNodes(NodeQuery{Limit: 50})
			if err != nil || len(list) != len(nodes) {
				t.Fatalf("GetNodes: %d rows, %v", len(list), err)
			}
			for _, m := range list {
				check("GetNodes", m["public_key"].(string), m["lat"], m["lon"])
			}
			found, err := db.SearchNodes("aa0000", 10)
			if err != nil || len(found) != len(nodes) {
				t.Fatalf("SearchNodes: %d rows, %v", len(found), err)
			}
			for _, m := range found {
				check("SearchNodes", m["public_key"].(string), m["lat"], m["lon"])
			}
			for _, n := range nodes {
				m, err := db.GetNodeByPubkey(n.pubkey)
				if err != nil || m == nil {
					t.Fatalf("GetNodeByPubkey: %v", err)
				}
				check("GetNodeByPubkey", n.pubkey, m["lat"], m["lon"])
			}
			m, _, err := db.GetNodeByPrefix("aa000000000000")
			if err != nil {
				t.Fatal(err)
			}
			if m != nil {
				t.Fatal("GetNodeByPrefix: ambiguous prefix resolved")
			}
			if m, _, err = db.GetNodeByPrefix(nodes[0].pubkey); err != nil || m == nil {
				t.Fatalf("GetNodeByPrefix: %v %v", m, err)
			}
			check("GetNodeByPrefix", m["public_key"].(string), m["lat"], m["lon"])
			for pk, loc := range db.GetNodeLocationsByKeys([]string{nodes[0].pubkey, nodes[2].pubkey}) {
				check("GetNodeLocationsByKeys", pk, loc.Lat, loc.Lon)
			}
			bh, err := srv.store.bulkHealthNodes(10, false, nil)
			if err != nil || len(bh) != len(nodes) {
				t.Fatalf("bulkHealthNodes: %d, %v", len(bh), err)
			}
			for _, n := range bh {
				check("bulkHealthNodes", n.pk, n.lat, n.lon)
			}
			for _, n := range srv.store.getAllNodes() {
				var lat, lon interface{}
				if n.HasGPS {
					lat, lon = n.Lat, n.Lon
				}
				check("getAllNodes", n.PublicKey, lat, lon)
				if n.hasExact != (on && n.HasGPS) {
					t.Errorf("getAllNodes %s: hasExact=%v", n.PublicKey, n.hasExact)
				}
			}

			// The geo-filter readers read the stored position.
			exact, err := db.exactNodePositions([]string{nodes[0].pubkey, nodes[2].pubkey, "ffff"})
			if err != nil {
				t.Fatal(err)
			}
			if p := exact[nodes[0].pubkey]; !p.ok || p.lat != nodes[0].lat || p.lon != nodes[0].lon {
				t.Errorf("exactNodePositions %+v", p)
			}
			if p, ok := exact[nodes[2].pubkey]; !ok || p.ok {
				t.Errorf("exactNodePositions no-fix row %+v %v", p, ok)
			}
			if _, ok := exact["ffff"]; ok || len(exact) != 2 {
				t.Errorf("exactNodePositions %v", exact)
			}
			prune, err := db.GetNodesForGeoPrune()
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range prune {
				if p.PubKey == nodes[1].pubkey && (*p.Lat != nodes[1].lat || *p.Lon != nodes[1].lon) {
					t.Errorf("GetNodesForGeoPrune %v,%v", *p.Lat, *p.Lon)
				}
			}
			lat0, lat1 := 30.0, 30.4
			lon0, lon1 := -98.0, -97.0
			area, err := db.GetNodePubkeysInArea(AreaEntry{LatMin: &lat0, LatMax: &lat1, LonMin: &lon0, LonMax: &lon1})
			if err != nil || !slices.Equal(area, []string{nodes[0].pubkey}) {
				t.Errorf("GetNodePubkeysInArea = %v, %v", area, err)
			}
		})
	}
}

// With the feature on, a half-NULL position is no position: neither
// coordinate is served exactly. Off, it passes through as before.
func TestNodeCoordHalfNullPosition(t *testing.T) {
	lat := sql.NullFloat64{Float64: 30.5, Valid: true}
	var lon sql.NullFloat64
	var off DB
	if la, lo := off.servedNodePos("aa", lat, lon); la != lat || lo != lon {
		t.Fatalf("off: %v %v", la, lo)
	}
	var on DB
	on.setLocationFuzzer(newTestLocFuzzer(t, 1000, testLocSecret))
	if la, lo := on.servedNodePos("aa", lat, lon); la.Valid || lo.Valid {
		t.Fatalf("on: %v %v", la, lo)
	}
	if la, lo := on.servedNodePos("aa", sql.NullFloat64{Valid: true}, sql.NullFloat64{Valid: true}); la.Float64 != 0 || lo.Float64 != 0 || !la.Valid || !lo.Valid {
		t.Fatalf("on, 0,0 (no fix) must pass through: %v %v", la, lo)
	}
	var nilDB *DB
	if nilDB.locationFuzzer() != nil {
		t.Fatal("nil DB has a fuzzer")
	}
	nilDB.setLocationFuzzer(nil) // no panic
}

// Concurrent node-cache refreshes and per-request reads with the feature on
// (run under -race): the fuzzer's anchor cache is shared.
func TestNodeCoordConcurrentReads(t *testing.T) {
	f := newTestLocFuzzer(t, 1000, testLocSecret)
	var nodes []visNode
	for i := 0; i < 50; i++ {
		nodes = append(nodes, visNode{pubkey: fmt.Sprintf("cc%062x", i), name: fmt.Sprintf("N%d", i),
			lat: 30 + float64(i)*0.01, lon: -97 - float64(i)*0.01})
	}
	srv, _ := nodeCoordFixture(t, f, nodes)
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				srv.store.InvalidateNodeCache()
				all, _ := srv.store.getCachedNodesAndPM()
				for _, n := range all {
					if la, lo, _ := f.Fuzz(n.PublicKey, n.exactLat, n.exactLon); n.Lat != la || n.Lon != lo {
						t.Errorf("%s: %v,%v not the approximate position", n.PublicKey, n.Lat, n.Lon)
						return
					}
				}
				if _, _, _, err := srv.db.GetNodes(NodeQuery{Limit: 50}); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// getAllNodes runs on every node-cache refresh (30 s TTL) over all nodes.
// Fuzzing adds the anchor-cache lookup per row; compare privacy=off/on.
func BenchmarkNodeCoordGetAllNodes(b *testing.B) {
	for _, on := range []bool{false, true} {
		b.Run(fmt.Sprintf("privacy=%v", on), func(b *testing.B) {
			db := setupTestDB(b)
			tx, _ := db.conn.Begin()
			for i := 0; i < 10000; i++ {
				tx.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen) VALUES (?, ?, 'repeater', ?, ?, '2026-01-01T00:00:00Z')`,
					fmt.Sprintf("bb%062x", i), fmt.Sprintf("N%d", i), 30+float64(i%100)*0.01, -97-float64(i/100)*0.01)
			}
			if err := tx.Commit(); err != nil {
				b.Fatal(err)
			}
			if on {
				db.setLocationFuzzer(newTestLocFuzzer(b, 1000, testLocSecret))
			}
			s := NewPacketStore(db, nil)
			s.getAllNodes() // warm the anchor cache, as every refresh after the first
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if n := len(s.getAllNodes()); n != 10000 {
					b.Fatalf("%d nodes", n)
				}
			}
		})
	}
}

// The ducting overlay computes its cells around node positions: with
// location privacy on those are the approximate ones, while the visibility
// rule still classifies by the stored position.
func TestDuctingNodeCoordsFuzzed(t *testing.T) {
	f := newTestLocFuzzer(t, 1000, testLocSecret)
	in, out := edgeNodes(t, f)
	plain := visNode{pubkey: fmt.Sprintf("ab%062x", 1), name: "Plain", lat: 30.2671, lon: -97.7431}
	all := []visNode{in, out, plain}
	db := setupTestDB(t)
	now := time.Now().UTC()
	seen := now.Add(-time.Hour).Format(time.RFC3339)
	for _, n := range all {
		if _, err := db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon, last_seen) VALUES (?, ?, ?, ?, ?)`,
			n.pubkey, n.name, n.lat, n.lon, seen); err != nil {
			t.Fatal(err)
		}
	}
	// Half a position is no position, with privacy off or on.
	if _, err := db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon, last_seen) VALUES (?, 'No lat', NULL, -97.5, ?), (?, 'No lon', 29.5, NULL, ?)`,
		fmt.Sprintf("cd%062x", 1), seen, fmt.Sprintf("cd%062x", 2), seen); err != nil {
		t.Fatal(err)
	}
	// A rule like the geo_filter: south of the edge (lat 31) only.
	stored := map[string][2]float64{}
	visible := func(n *nodeInfo) bool {
		lat, lon := n.geoFilterPos()
		stored[n.PublicKey] = [2]float64{lat, lon}
		return lat < 31
	}
	check := func(name string, rule func(n *nodeInfo) bool, wantNodes ...visNode) {
		t.Helper()
		want := map[[2]float64]bool{}
		for _, n := range wantNodes {
			la, lo := n.lat, n.lon
			if fz := db.locationFuzzer(); fz != nil {
				var ok bool
				if la, lo, ok = fz.Fuzz(n.pubkey, n.lat, n.lon); !ok {
					t.Fatalf("%s: no fuzzed position for %s", name, n.name)
				}
				if la == n.lat && lo == n.lon {
					t.Fatalf("%s: %s is not moved by the fuzzer", name, n.name)
				}
			}
			want[[2]float64{la, lo}] = true
		}
		coords, err := db.ductingNodeCoords(rule, now)
		if err != nil {
			t.Fatal(err)
		}
		if len(coords) != len(want) {
			t.Fatalf("%s: coords %v, want %v", name, coords, want)
		}
		for _, c := range coords {
			if !want[c] {
				t.Fatalf("%s: coords %v, want %v", name, coords, want)
			}
		}
		if rule == nil {
			return
		}
		for _, n := range all {
			if stored[n.pubkey] != [2]float64{n.lat, n.lon} {
				t.Fatalf("%s: rule saw %v for %s, want the stored position", name, stored[n.pubkey], n.name)
			}
		}
		clear(stored)
	}

	check("privacy off", nil, all...)
	check("privacy off, rule", visible, in, plain)
	db.setLocationFuzzer(f)
	// Every point is the served position, none the stored one.
	check("privacy on", nil, all...)
	// in's approximate position is north of the edge and out's south of it:
	// the rule must not follow them.
	check("privacy on, rule", visible, in, plain)

	// A node whose key is not 64 hex digits has no served position: it is
	// left out, never placed at (0, 0).
	if _, err := db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon, last_seen) VALUES ('abcd', 'Short', 29.5, -97.5, ?)`, seen); err != nil {
		t.Fatal(err)
	}
	check("privacy on, malformed key", nil, all...)
	db.setLocationFuzzer(nil)
	check("privacy off, malformed key", nil, append([]visNode{{pubkey: "abcd", name: "Short", lat: 29.5, lon: -97.5}}, all...)...)
}
