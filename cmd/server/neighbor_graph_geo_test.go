package main

import (
	"sync/atomic"
	"testing"
)

// TestBuildNeighborGraph_RejectsGeoFarEdge — RED test for #1228.
//
// Synthetic advert produces an edge between A (Bay Area) and B (Berlin).
// Distance ≈ 9 100 km, well above any plausible terrestrial LoRa hop.
// The geo-sanity filter must reject the edge at build time so the
// affinity graph cannot self-reinforce a wrong disambiguation.
func TestBuildNeighborGraph_RejectsGeoFarEdge(t *testing.T) {
	nodes := []nodeInfo{
		// A: San Francisco
		{Role: "repeater", PublicKey: "aaaa1111", Name: "A_SF", Lat: 37.77, Lon: -122.41, HasGPS: true},
		// B: Berlin
		{Role: "repeater", PublicKey: "bbbb2222", Name: "B_BE", Lat: 52.52, Lon: 13.40, HasGPS: true},
		// Observer with GPS at SF (won't affect A↔B edge under test)
		{Role: "repeater", PublicKey: "obs00001", Name: "Observer", Lat: 37.77, Lon: -122.41, HasGPS: true},
	}
	// ADVERT originated by A, path=["bbbb"] → builder creates edge A↔B
	// (originator ↔ path[0]). With geo sanity ON this edge must be dropped.
	tx := ngMakeTx(1, 4, ngFromNodeJSON("aaaa1111"), []*StoreObs{
		ngMakeObs("obs00001", `["bbbb"]`, nowStr, ngFloatPtr(-10)),
	})
	store := ngTestStore(nodes, []*StoreTx{tx})
	g := BuildFromStore(store)

	for _, e := range g.AllEdges() {
		if (e.NodeA == "aaaa1111" && e.NodeB == "bbbb2222") ||
			(e.NodeA == "bbbb2222" && e.NodeB == "aaaa1111") {
			t.Fatalf("geo-implausible edge A(SF)↔B(Berlin) was not rejected: %+v", e)
		}
	}
}

// TestBuildNeighborGraph_AcceptsLocalEdge — A↔C within plausible LoRa range
// (both in CA, ~100 km apart) must remain in the graph.
func TestBuildNeighborGraph_AcceptsLocalEdge(t *testing.T) {
	nodes := []nodeInfo{
		{Role: "repeater", PublicKey: "aaaa1111", Name: "A_SF", Lat: 37.77, Lon: -122.41, HasGPS: true},
		{Role: "repeater", PublicKey: "cccc3333", Name: "C_SJ", Lat: 37.34, Lon: -121.89, HasGPS: true},
		{Role: "repeater", PublicKey: "obs00001", Name: "Observer", Lat: 37.77, Lon: -122.41, HasGPS: true},
	}
	tx := ngMakeTx(1, 4, ngFromNodeJSON("aaaa1111"), []*StoreObs{
		ngMakeObs("obs00001", `["cccc"]`, nowStr, ngFloatPtr(-10)),
	})
	store := ngTestStore(nodes, []*StoreTx{tx})
	g := BuildFromStore(store)

	found := false
	for _, e := range g.AllEdges() {
		if (e.NodeA == "aaaa1111" && e.NodeB == "cccc3333") ||
			(e.NodeA == "cccc3333" && e.NodeB == "aaaa1111") {
			found = true
		}
	}
	if !found {
		t.Fatalf("local A↔C edge (~50km) must be accepted")
	}
}

// TestBuildNeighborGraph_AcceptsEdgeWhenNoGPS — if either endpoint lacks GPS,
// we have no signal to reject; the edge is accepted.
func TestBuildNeighborGraph_AcceptsEdgeWhenNoGPS(t *testing.T) {
	nodes := []nodeInfo{
		// A has GPS (Berlin)
		{Role: "repeater", PublicKey: "aaaa1111", Name: "A", Lat: 52.52, Lon: 13.40, HasGPS: true},
		// D has no GPS
		{Role: "repeater", PublicKey: "dddd4444", Name: "D"}, // HasGPS = false
		{Role: "repeater", PublicKey: "obs00001", Name: "Observer"},
	}
	tx := ngMakeTx(1, 4, ngFromNodeJSON("aaaa1111"), []*StoreObs{
		ngMakeObs("obs00001", `["dddd"]`, nowStr, nil),
	})
	store := ngTestStore(nodes, []*StoreTx{tx})
	g := BuildFromStore(store)

	found := false
	for _, e := range g.AllEdges() {
		if (e.NodeA == "aaaa1111" && e.NodeB == "dddd4444") ||
			(e.NodeA == "dddd4444" && e.NodeB == "aaaa1111") {
			found = true
		}
	}
	if !found {
		t.Fatalf("A(GPS)↔D(no-GPS) edge must be accepted (no signal to reject)")
	}
}

// TestBuildNeighborGraph_RejectedCounterIncrements — every dropped edge bumps
// the atomic counter surfaced by /api/analytics/neighbor-graph stats.
func TestBuildNeighborGraph_RejectedCounterIncrements(t *testing.T) {
	nodes := []nodeInfo{
		{Role: "repeater", PublicKey: "aaaa1111", Name: "A_SF", Lat: 37.77, Lon: -122.41, HasGPS: true},
		{Role: "repeater", PublicKey: "bbbb2222", Name: "B_BE", Lat: 52.52, Lon: 13.40, HasGPS: true},
		{Role: "repeater", PublicKey: "obs00001", Name: "Observer", Lat: 37.77, Lon: -122.41, HasGPS: true},
	}
	// Two adverts each producing the far A↔B edge attempt → counter ≥ 2.
	txs := []*StoreTx{
		ngMakeTx(1, 4, ngFromNodeJSON("aaaa1111"), []*StoreObs{
			ngMakeObs("obs00001", `["bbbb"]`, nowStr, nil),
		}),
		ngMakeTx(2, 4, ngFromNodeJSON("aaaa1111"), []*StoreObs{
			ngMakeObs("obs00001", `["bbbb"]`, nowStr, nil),
		}),
	}
	store := ngTestStore(nodes, txs)
	g := BuildFromStore(store)
	got := atomic.LoadUint64(&g.RejectedEdgesGeoFar)
	if got < 2 {
		t.Fatalf("RejectedEdgesGeoFar = %d, want >= 2", got)
	}
}
