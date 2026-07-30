package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

// TestComputeSuspiciousGPSPositions_FlagsNodeFarFromTightCluster covers the
// core case: a node's own reported GPS disagrees wildly with two neighbors
// that closely agree with each other.
func TestComputeSuspiciousGPSPositions_FlagsNodeFarFromTightCluster(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('badgps', 'BadGPSNode', 60.0, 20.0)`)
	db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('n1', 'Neighbor1', 55.00, 10.00)`)
	db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('n2', 'Neighbor2', 55.05, 10.05)`)
	db.conn.Exec(`INSERT INTO neighbor_edges (node_a, node_b, count) VALUES ('badgps', 'n1', 10)`)
	db.conn.Exec(`INSERT INTO neighbor_edges (node_a, node_b, count) VALUES ('badgps', 'n2', 5)`)

	positioned, _, err := db.GetNodesForAreaAnalytics()
	if err != nil {
		t.Fatalf("GetNodesForAreaAnalytics: %v", err)
	}
	resp, err := computeSuspiciousGPSPositions(db, positioned)
	if err != nil {
		t.Fatalf("computeSuspiciousGPSPositions: %v", err)
	}
	if resp.TotalRealGPS != 3 {
		t.Errorf("TotalRealGPS = %d, want 3", resp.TotalRealGPS)
	}
	if len(resp.Nodes) != 1 || resp.Nodes[0].PublicKey != "badgps" {
		t.Fatalf("expected exactly badgps flagged, got %+v", resp.Nodes)
	}
	flagged := resp.Nodes[0]
	if flagged.ClusterSize != 2 {
		t.Errorf("ClusterSize = %d, want 2", flagged.ClusterSize)
	}
	if flagged.DistanceKm < GPSSanitySuspectKm {
		t.Errorf("DistanceKm = %.1f, want > %.1f", flagged.DistanceKm, GPSSanitySuspectKm)
	}
	if flagged.ClusterSpreadKm > GPSSanityClusterTightKm {
		t.Errorf("ClusterSpreadKm = %.1f, want <= %.1f (n1/n2 are only a few km apart)", flagged.ClusterSpreadKm, GPSSanityClusterTightKm)
	}
}

// TestComputeSuspiciousGPSPositions_DoesNotFlagWithinRange confirms a node
// whose own position agrees with its trusted cluster is NOT flagged.
func TestComputeSuspiciousGPSPositions_DoesNotFlagWithinRange(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('goodgps', 'GoodGPSNode', 55.02, 10.02)`)
	db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('n1', 'Neighbor1', 55.00, 10.00)`)
	db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('n2', 'Neighbor2', 55.05, 10.05)`)
	db.conn.Exec(`INSERT INTO neighbor_edges (node_a, node_b, count) VALUES ('goodgps', 'n1', 10)`)
	db.conn.Exec(`INSERT INTO neighbor_edges (node_a, node_b, count) VALUES ('goodgps', 'n2', 5)`)

	positioned, _, err := db.GetNodesForAreaAnalytics()
	if err != nil {
		t.Fatalf("GetNodesForAreaAnalytics: %v", err)
	}
	resp, err := computeSuspiciousGPSPositions(db, positioned)
	if err != nil {
		t.Fatalf("computeSuspiciousGPSPositions: %v", err)
	}
	if len(resp.Nodes) != 0 {
		t.Fatalf("expected no flagged nodes, got %+v", resp.Nodes)
	}
	if resp.Evaluated != 1 {
		t.Errorf("Evaluated = %d, want 1 (goodgps had a trusted cluster)", resp.Evaluated)
	}
}

// TestComputeSuspiciousGPSPositions_SkipsScatteredNeighbors covers a node
// whose neighbors disagree with each other by more than the tight-cluster
// threshold -- the neighbor set itself isn't trustworthy (likely a bridge
// edge contaminating it), so the node is skipped entirely, not flagged,
// even though it's far from BOTH neighbors individually.
func TestComputeSuspiciousGPSPositions_SkipsScatteredNeighbors(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('mid', 'MidNode', 60.0, 20.0)`)
	db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('s1', 'Scattered1', 55.0, 10.0)`)
	db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('s2', 'Scattered2', 56.5, 12.5)`) // ~180km from s1, beyond the 50km tight threshold
	db.conn.Exec(`INSERT INTO neighbor_edges (node_a, node_b, count) VALUES ('mid', 's1', 10)`)
	db.conn.Exec(`INSERT INTO neighbor_edges (node_a, node_b, count) VALUES ('mid', 's2', 5)`)

	positioned, _, err := db.GetNodesForAreaAnalytics()
	if err != nil {
		t.Fatalf("GetNodesForAreaAnalytics: %v", err)
	}
	resp, err := computeSuspiciousGPSPositions(db, positioned)
	if err != nil {
		t.Fatalf("computeSuspiciousGPSPositions: %v", err)
	}
	if len(resp.Nodes) != 0 {
		t.Fatalf("expected mid to be skipped (scattered neighbors), got flagged: %+v", resp.Nodes)
	}
	if resp.Evaluated != 0 {
		t.Errorf("Evaluated = %d, want 0 (no node had a trusted cluster)", resp.Evaluated)
	}
}

// TestComputeSuspiciousGPSPositions_SkipsSingleNeighbor confirms one
// positioned neighbor alone is never enough to form a trusted cluster
// (GPSSanityMinClusterSize=2), regardless of how far away it agrees.
func TestComputeSuspiciousGPSPositions_SkipsSingleNeighbor(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('loner', 'LonerNode', 60.0, 20.0)`)
	db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('onlyneighbor', 'OnlyNeighbor', 55.0, 10.0)`)
	db.conn.Exec(`INSERT INTO neighbor_edges (node_a, node_b, count) VALUES ('loner', 'onlyneighbor', 10)`)

	positioned, _, err := db.GetNodesForAreaAnalytics()
	if err != nil {
		t.Fatalf("GetNodesForAreaAnalytics: %v", err)
	}
	resp, err := computeSuspiciousGPSPositions(db, positioned)
	if err != nil {
		t.Fatalf("computeSuspiciousGPSPositions: %v", err)
	}
	if len(resp.Nodes) != 0 {
		t.Fatalf("expected loner to be skipped (only 1 positioned neighbor), got flagged: %+v", resp.Nodes)
	}
}

// TestComputeSuspiciousGPSPositions_SkipsNoNeighbors confirms a real-GPS
// node with no neighbor_edges rows at all is skipped, not flagged.
func TestComputeSuspiciousGPSPositions_SkipsNoNeighbors(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('isolated', 'IsolatedNode', 55.0, 10.0)`)

	positioned, _, err := db.GetNodesForAreaAnalytics()
	if err != nil {
		t.Fatalf("GetNodesForAreaAnalytics: %v", err)
	}
	resp, err := computeSuspiciousGPSPositions(db, positioned)
	if err != nil {
		t.Fatalf("computeSuspiciousGPSPositions: %v", err)
	}
	if resp.TotalRealGPS != 1 {
		t.Errorf("TotalRealGPS = %d, want 1", resp.TotalRealGPS)
	}
	if len(resp.Nodes) != 0 || resp.Evaluated != 0 {
		t.Fatalf("expected isolated node to be skipped entirely, got nodes=%+v evaluated=%d", resp.Nodes, resp.Evaluated)
	}
}

// TestComputeSuspiciousGPSPositions_SortedWorstFirst confirms flagged nodes
// are sorted by DistanceKm descending.
func TestComputeSuspiciousGPSPositions_SortedWorstFirst(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('n1', 'Neighbor1', 55.00, 10.00)`)
	db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('n2', 'Neighbor2', 55.05, 10.05)`)

	// mild: ~150km off
	db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('mild', 'MildOffset', 56.3, 10.0)`)
	db.conn.Exec(`INSERT INTO neighbor_edges (node_a, node_b, count) VALUES ('mild', 'n1', 10)`)
	db.conn.Exec(`INSERT INTO neighbor_edges (node_a, node_b, count) VALUES ('mild', 'n2', 5)`)

	// extreme: ~1000+km off
	db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('extreme', 'ExtremeOffset', 65.0, 25.0)`)
	db.conn.Exec(`INSERT INTO neighbor_edges (node_a, node_b, count) VALUES ('extreme', 'n1', 10)`)
	db.conn.Exec(`INSERT INTO neighbor_edges (node_a, node_b, count) VALUES ('extreme', 'n2', 5)`)

	positioned, _, err := db.GetNodesForAreaAnalytics()
	if err != nil {
		t.Fatalf("GetNodesForAreaAnalytics: %v", err)
	}
	resp, err := computeSuspiciousGPSPositions(db, positioned)
	if err != nil {
		t.Fatalf("computeSuspiciousGPSPositions: %v", err)
	}
	if len(resp.Nodes) != 2 {
		t.Fatalf("expected both mild and extreme flagged, got %+v", resp.Nodes)
	}
	if resp.Nodes[0].PublicKey != "extreme" || resp.Nodes[1].PublicKey != "mild" {
		t.Fatalf("expected extreme before mild (worst first), got order: %s, %s", resp.Nodes[0].PublicKey, resp.Nodes[1].PublicKey)
	}
	if resp.Nodes[0].DistanceKm <= resp.Nodes[1].DistanceKm {
		t.Errorf("expected extreme's distance (%.1f) > mild's (%.1f)", resp.Nodes[0].DistanceKm, resp.Nodes[1].DistanceKm)
	}
}

// TestHandleGPSSanity_Populated drives the full HTTP path, confirming the
// JSON shape matches openapi.go and doesn't require Areas to be configured
// (unlike /api/analytics/areas).
func TestHandleGPSSanity_Populated(t *testing.T) {
	srv, router := setupTestServer(t)
	srv.db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('badgps', 'BadGPSNode', 60.0, 20.0)`)
	srv.db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('n1', 'Neighbor1', 55.00, 10.00)`)
	srv.db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('n2', 'Neighbor2', 55.05, 10.05)`)
	srv.db.conn.Exec(`INSERT INTO neighbor_edges (node_a, node_b, count) VALUES ('badgps', 'n1', 10)`)
	srv.db.conn.Exec(`INSERT INTO neighbor_edges (node_a, node_b, count) VALUES ('badgps', 'n2', 5)`)

	req := httptest.NewRequest("GET", "/api/analytics/gps-sanity", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp GPSSanityResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Nodes) != 1 || resp.Nodes[0].PublicKey != "badgps" {
		t.Fatalf("expected badgps flagged over HTTP, got %+v", resp.Nodes)
	}
}

// TestHandleGPSSanity_CachesResponse confirms the 30s TTL cache actually
// short-circuits a second request rather than recomputing (same pattern
// concern as areaAnalyticsCache -- a stale cache silently serving forever
// would be worse than no cache, but recomputing every request on a large
// network defeats the point of caching at all).
func TestHandleGPSSanity_CachesResponse(t *testing.T) {
	srv, router := setupTestServer(t)
	srv.db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('badgps', 'BadGPSNode', 60.0, 20.0)`)
	srv.db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('n1', 'Neighbor1', 55.00, 10.00)`)
	srv.db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('n2', 'Neighbor2', 55.05, 10.05)`)
	srv.db.conn.Exec(`INSERT INTO neighbor_edges (node_a, node_b, count) VALUES ('badgps', 'n1', 10)`)
	srv.db.conn.Exec(`INSERT INTO neighbor_edges (node_a, node_b, count) VALUES ('badgps', 'n2', 5)`)

	req1 := httptest.NewRequest("GET", "/api/analytics/gps-sanity", nil)
	w1 := httptest.NewRecorder()
	router.ServeHTTP(w1, req1)

	// A second real-GPS node added after the first request should NOT show
	// up in a second request within the TTL window -- proves the cache hit
	// path returned the same cached struct rather than recomputing.
	srv.db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('badgps2', 'BadGPSNode2', 61.0, 21.0)`)
	srv.db.conn.Exec(`INSERT INTO neighbor_edges (node_a, node_b, count) VALUES ('badgps2', 'n1', 10)`)
	srv.db.conn.Exec(`INSERT INTO neighbor_edges (node_a, node_b, count) VALUES ('badgps2', 'n2', 5)`)

	req2 := httptest.NewRequest("GET", "/api/analytics/gps-sanity", nil)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)

	var resp2 GPSSanityResponse
	if err := json.Unmarshal(w2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp2.Nodes) != 1 {
		t.Fatalf("expected the cached (stale) 1-flagged-node response on the second request within the TTL window, got %+v", resp2.Nodes)
	}
}
