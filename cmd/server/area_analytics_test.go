package main

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

// TestComputeAreaDensity covers multi-membership (a node in a narrower
// sub-area also counts toward a broader containing area, same convention
// as computeScopeAdoptionByArea) and the active/degraded/silent breakdown
// using GetNetworkStatus's own thresholds.
func TestComputeAreaDensity(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	areas := map[string]AreaEntry{
		"ODE": {Label: "Odense by", LatMin: f(55.32), LatMax: f(55.45), LonMin: f(10.3), LonMax: f(10.5)},
		"DK":  {Label: "Danmark (alle)", LatMin: f(54.5), LatMax: f(57.8), LonMin: f(8.0), LonMax: f(15.2)}, // contains ODE
	}
	thresholds := HealthThresholds{NodeDegradedHours: 1, NodeSilentHours: 24, InfraDegradedHours: 2, InfraSilentHours: 48}
	now := time.Now().UTC()

	nodes := []areaAnalyticsNode{
		{PublicKey: "active1", Role: "repeater", LastSeen: validStr(now.Add(-30 * time.Minute).Format(time.RFC3339)), Lat: 55.40, Lon: 10.40}, // Odense, infra-active
		{PublicKey: "degraded1", Role: "client", LastSeen: validStr(now.Add(-3 * time.Hour).Format(time.RFC3339)), Lat: 55.41, Lon: 10.41},    // Odense, node-degraded
		{PublicKey: "silent1", Role: "client", LastSeen: validStr(now.Add(-72 * time.Hour).Format(time.RFC3339)), Lat: 55.42, Lon: 10.42},     // Odense, silent
		{PublicKey: "outside1", Role: "client", LastSeen: validStr(now.Format(time.RFC3339)), Lat: 51.0, Lon: 4.0},                            // outside every area
	}

	got := computeAreaDensity(nodes, areas, thresholds)
	if len(got) != 2 {
		t.Fatalf("got %d areas, want 2 (ODE + DK) -- result: %+v", len(got), got)
	}
	byKey := map[string]AreaDensity{}
	for _, d := range got {
		byKey[d.AreaKey] = d
	}

	ode := byKey["ODE"]
	if ode.Total != 3 || ode.Active != 1 || ode.Degraded != 1 || ode.Silent != 1 {
		t.Errorf("ODE = %+v, want Total=3 Active=1 Degraded=1 Silent=1", ode)
	}
	if ode.RoleCounts["repeater"] != 1 || ode.RoleCounts["client"] != 2 {
		t.Errorf("ODE.RoleCounts = %v, want repeater=1 client=2", ode.RoleCounts)
	}

	dk := byKey["DK"]
	if dk.Total != 3 {
		t.Errorf("DK.Total = %d, want 3 (multi-membership: every Odense node also counts toward DK)", dk.Total)
	}
}

// TestComputeAreaBridgeNodes covers the core cross-area signal: an edge
// between two nodes in DIFFERENT areas credits both endpoints, an edge
// within the SAME area is ignored, and an edge with an unresolved
// endpoint (NodeB == "") is skipped per the bridge_recomputer.go
// convention.
func TestComputeAreaBridgeNodes(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	areas := map[string]AreaEntry{
		"A": {Label: "Area A", LatMin: f(55.0), LatMax: f(55.1), LonMin: f(10.0), LonMax: f(10.1)},
		"B": {Label: "Area B", LatMin: f(56.0), LatMax: f(56.1), LonMin: f(11.0), LonMax: f(11.1)},
	}
	nodes := []areaAnalyticsNode{
		{PublicKey: "bridgea", Name: "BridgeA", Lat: 55.05, Lon: 10.05},
		{PublicKey: "bridgeb", Name: "BridgeB", Lat: 56.05, Lon: 11.05},
		{PublicKey: "sameareaa1", Name: "SameA1", Lat: 55.06, Lon: 10.06},
		{PublicKey: "sameareaa2", Name: "SameA2", Lat: 55.07, Lon: 10.07},
	}

	g := NewNeighborGraph()
	now := time.Now()
	snr := 5.0
	g.upsertEdge("bridgea", "bridgeb", "aa", "obs", &snr, now)       // cross-area A<->B
	g.upsertEdge("sameareaa1", "sameareaa2", "bb", "obs", &snr, now) // same-area, must be excluded
	g.upsertEdge("bridgea", "unknownnode0000000000000000000000000000000000000000000000000000", "cc", "obs", &snr, now)

	// An edge with an unresolved (empty) NodeB -- the ambiguous-prefix
	// case upsertEdge can't itself produce -- must be skipped, same
	// convention as bridgeEdgesFromGraph in bridge_recomputer.go.
	g.mu.Lock()
	unresolvedKey := makeEdgeKey("bridgea", "")
	g.edges[unresolvedKey] = &NeighborEdge{NodeA: "bridgea", NodeB: "", Count: 5}
	g.mu.Unlock()

	got := computeAreaBridgeNodes(nodes, areas, g)
	byKey := map[string]AreaBridgeNode{}
	for _, b := range got {
		byKey[b.PublicKey] = b
	}

	a, ok := byKey["bridgea"]
	if !ok {
		t.Fatalf("bridgea missing from result: %+v", got)
	}
	if a.OtherAreaCount != 1 || len(a.OtherAreas) != 1 || a.OtherAreas[0] != "Area B" {
		t.Errorf("bridgea = %+v, want OtherAreaCount=1 OtherAreas=[Area B]", a)
	}

	b, ok := byKey["bridgeb"]
	if !ok || b.OtherAreaCount != 1 || b.OtherAreas[0] != "Area A" {
		t.Errorf("bridgeb = %+v, want OtherAreaCount=1 OtherAreas=[Area A]", b)
	}

	if _, ok := byKey["sameareaa1"]; ok {
		t.Errorf("sameareaa1 should not appear -- its only edge stays within Area A")
	}
}

// TestComputeAreaBridgeNodes_NilGraph confirms a nil graph (no neighbor
// data loaded yet) degrades to an empty result rather than panicking.
func TestComputeAreaBridgeNodes_NilGraph(t *testing.T) {
	areas := map[string]AreaEntry{"A": {Label: "Area A"}}
	got := computeAreaBridgeNodes(nil, areas, nil)
	if got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

// TestComputeAreaPositionGaps exercises the real DB path: one node with
// an actual GPS fix, one unpositioned node with a neighbor_edges row
// pointing at a positioned neighbor inside an area (so it should be
// "approximated" into that area), and one unpositioned node with no
// neighbor_edges at all (so it must land in unpositionedNoNeighborFix,
// not any area's Approximated count).
func TestComputeAreaPositionGaps(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	areas := map[string]AreaEntry{
		"ODE": {Label: "Odense by", LatMin: f(55.32), LatMax: f(55.45), LonMin: f(10.3), LonMax: f(10.5)},
	}
	db := setupTestDB(t)
	defer db.conn.Close()

	if _, err := db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('realfix01', 'RealFix', 55.40, 10.40)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.Exec(`INSERT INTO neighbor_edges (node_a, node_b, count) VALUES ('estimateme01', 'realfix01', 5)`); err != nil {
		t.Fatal(err)
	}

	positioned := []areaAnalyticsNode{{PublicKey: "realfix01", Lat: 55.40, Lon: 10.40}}
	unpositioned := []RepeaterRef{
		{PublicKey: "estimateme01", Name: "EstimateMe"},
		{PublicKey: "noneighborfix01", Name: "NoNeighborFix"},
	}

	gaps, noNeighborFix, estimatedNodes := computeAreaPositionGaps(db, positioned, unpositioned, areas, DefaultMaxEdgeKm)
	if len(gaps) != 1 {
		t.Fatalf("got %d area gaps, want 1: %+v", len(gaps), gaps)
	}
	ode := gaps[0]
	if ode.RealFix != 1 {
		t.Errorf("ODE.RealFix = %d, want 1", ode.RealFix)
	}
	if ode.Approximated != 1 {
		t.Errorf("ODE.Approximated = %d, want 1 (estimateme01 via its neighbor realfix01)", ode.Approximated)
	}
	if noNeighborFix != 1 {
		t.Errorf("unpositionedNoNeighborFix = %d, want 1 (noneighborfix01 has no neighbor_edges row)", noNeighborFix)
	}
	if len(estimatedNodes) != 1 {
		t.Fatalf("got %d estimatedNodes, want 1: %+v", len(estimatedNodes), estimatedNodes)
	}
	en := estimatedNodes[0]
	if en.PublicKey != "estimateme01" || en.Name != "EstimateMe" {
		t.Errorf("estimatedNodes[0] = %+v, want PublicKey=estimateme01 Name=EstimateMe", en)
	}
	if en.AreaKey != "ODE" || en.Label != "Odense by" {
		t.Errorf("estimatedNodes[0] area = %+v, want ODE/Odense by", en)
	}
	if en.Lat != 55.40 || en.Lon != 10.40 {
		t.Errorf("estimatedNodes[0] Lat/Lon = %v/%v, want the estimate centered on its only neighbor realfix01 (55.40/10.40)", en.Lat, en.Lon)
	}
}

// TestHandleAreaAnalytics_NoAreasConfigured confirms the endpoint returns
// an empty (not error) response when the server has no Areas configured,
// matching the openapi.go doc's "Returns an empty response if no Areas
// are configured."
func TestHandleAreaAnalytics_NoAreasConfigured(t *testing.T) {
	_, router := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/analytics/areas", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp AreaAnalyticsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Density) != 0 || len(resp.BridgeNodes) != 0 || len(resp.PositionGaps) != 0 {
		t.Errorf("resp = %+v, want all-empty with no Areas configured", resp)
	}
}

// TestHandleAreaAnalytics_Populated drives the full HTTP path with a
// configured area and a positioned node, confirming the JSON shape
// matches openapi.go and the density section actually reflects the seed
// data (seedTestData's TestRepeater/TestCompanion/TestRoom nodes sit
// around lat 37.4-37.6, lon -121.9..-122.1).
func TestHandleAreaAnalytics_Populated(t *testing.T) {
	srv, router := setupTestServer(t)
	f := func(v float64) *float64 { return &v }
	srv.cfg.Areas = map[string]AreaEntry{
		"BAY": {Label: "Bay Area", LatMin: f(37.0), LatMax: f(38.0), LonMin: f(-123.0), LonMax: f(-121.0)},
	}

	req := httptest.NewRequest("GET", "/api/analytics/areas", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp AreaAnalyticsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Density) != 1 || resp.Density[0].AreaKey != "BAY" {
		t.Fatalf("resp.Density = %+v, want one BAY entry", resp.Density)
	}
	if resp.Density[0].Total != 3 {
		t.Errorf("BAY.Total = %d, want 3 (seedTestData's three positioned nodes)", resp.Density[0].Total)
	}
}

func validStr(s string) sql.NullString {
	return sql.NullString{String: s, Valid: true}
}
