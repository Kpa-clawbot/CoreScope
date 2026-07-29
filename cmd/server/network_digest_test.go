package main

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestComputeNetworkDigest_CountsNewNodesWithinWindow confirms only nodes
// first_seen within the window are counted, and older ones are excluded.
func TestComputeNetworkDigest_CountsNewNodesWithinWindow(t *testing.T) {
	srv, _ := setupTestServer(t)
	ensureInactiveNodesTable(t, srv)
	ensureNodeChangesTable(t, srv)

	now := time.Now().UTC()
	insertNewNodeRow(t, srv, "digestnew0000001", "InWindow", "repeater", f64(56.0), f64(10.0), now.Add(-1*time.Hour).Format(time.RFC3339))
	insertNewNodeRow(t, srv, "digestold0000001", "OutOfWindow", "repeater", f64(56.0), f64(10.0), now.Add(-10*24*time.Hour).Format(time.RFC3339))

	digest, err := srv.computeNetworkDigest(now.Add(-7*24*time.Hour), "all")
	if err != nil {
		t.Fatalf("computeNetworkDigest: %v", err)
	}
	if digest.NewNodes != 1 {
		t.Errorf("NewNodes = %d, want 1 (only the in-window node)", digest.NewNodes)
	}
}

// TestComputeNetworkDigest_CountsChangeTypesWithinWindow confirms each
// change type is tallied into its own field, and only within the window.
func TestComputeNetworkDigest_CountsChangeTypesWithinWindow(t *testing.T) {
	srv, _ := setupTestServer(t)
	ensureInactiveNodesTable(t, srv)
	ensureNodeChangesTable(t, srv)

	now := time.Now().UTC()
	inWindow := now.Add(-1 * time.Hour).Format(time.RFC3339)
	outOfWindow := now.Add(-10 * 24 * time.Hour).Format(time.RFC3339)

	insertNodeChangeRow(t, srv, "digestrole0000001", "role", "companion", "repeater", inWindow)
	insertNodeChangeRow(t, srv, "digestname0000001", "name", "Old", "New", inWindow)
	insertNodeChangeRow(t, srv, "digestpos00000001", "position", "56.0,10.0", "56.1,10.0", inWindow)
	insertNodeChangeRow(t, srv, "digestres00000001", "resurrected", "2026-06-01T00:00:00Z", "", inWindow)
	// Outside the window -- must not be counted.
	insertNodeChangeRow(t, srv, "digestoldrole00001", "role", "sensor", "repeater", outOfWindow)

	digest, err := srv.computeNetworkDigest(now.Add(-7*24*time.Hour), "all")
	if err != nil {
		t.Fatalf("computeNetworkDigest: %v", err)
	}
	if digest.RoleChanges != 1 {
		t.Errorf("RoleChanges = %d, want 1", digest.RoleChanges)
	}
	if digest.NameChanges != 1 {
		t.Errorf("NameChanges = %d, want 1", digest.NameChanges)
	}
	if digest.PositionMoves != 1 {
		t.Errorf("PositionMoves = %d, want 1", digest.PositionMoves)
	}
	if digest.Resurrections != 1 {
		t.Errorf("Resurrections = %d, want 1", digest.Resurrections)
	}
}

// TestComputeNetworkDigest_TopArea confirms the area with the most
// new-node activity in the window wins, and ties break alphabetically.
func TestComputeNetworkDigest_TopArea(t *testing.T) {
	srv, _ := setupTestServer(t)
	ensureInactiveNodesTable(t, srv)
	ensureNodeChangesTable(t, srv)

	srv.cfg.Areas = map[string]AreaEntry{
		"AREAA": {Label: "Area A", LatMin: f64(55.9), LatMax: f64(56.2), LonMin: f64(9.9), LonMax: f64(10.2)},
		"AREAB": {Label: "Area B", LatMin: f64(10.0), LatMax: f64(10.2), LonMin: f64(50.0), LonMax: f64(50.2)},
	}
	now := time.Now().UTC()
	// Two new nodes in Area A, one in Area B -- Area A should win.
	insertNewNodeRow(t, srv, "topareaa0000001", "A1", "repeater", f64(56.0), f64(10.0), now.Add(-1*time.Hour).Format(time.RFC3339))
	insertNewNodeRow(t, srv, "topareaa0000002", "A2", "repeater", f64(56.0), f64(10.0), now.Add(-2*time.Hour).Format(time.RFC3339))
	insertNewNodeRow(t, srv, "topareab0000001", "B1", "repeater", f64(10.1), f64(50.1), now.Add(-3*time.Hour).Format(time.RFC3339))

	digest, err := srv.computeNetworkDigest(now.Add(-7*24*time.Hour), "all")
	if err != nil {
		t.Fatalf("computeNetworkDigest: %v", err)
	}
	if digest.TopArea == nil || digest.TopArea.Label != "Area A" || digest.TopArea.Count != 2 {
		t.Errorf("TopArea = %+v, want Area A with count 2", digest.TopArea)
	}
}

// TestComputeNetworkDigest_TopAreaNilWhenNoAreasConfigured confirms
// TopArea stays nil rather than a zero-valued struct when no areas are
// configured (matches New Nodes' own nil-Areas behavior in that case).
func TestComputeNetworkDigest_TopAreaNilWhenNoAreasConfigured(t *testing.T) {
	srv, _ := setupTestServer(t)
	ensureInactiveNodesTable(t, srv)
	ensureNodeChangesTable(t, srv)
	srv.cfg.Areas = nil

	now := time.Now().UTC()
	insertNewNodeRow(t, srv, "noareasnode000001", "NoArea", "repeater", f64(56.0), f64(10.0), now.Add(-1*time.Hour).Format(time.RFC3339))

	digest, err := srv.computeNetworkDigest(now.Add(-7*24*time.Hour), "all")
	if err != nil {
		t.Fatalf("computeNetworkDigest: %v", err)
	}
	if digest.TopArea != nil {
		t.Errorf("TopArea = %+v, want nil when no areas are configured", digest.TopArea)
	}
}

// TestHandleNetworkDigest_DefaultsTo7Days confirms the handler defaults
// to a 7-day window when none is given, and echoes it back.
func TestHandleNetworkDigest_DefaultsTo7Days(t *testing.T) {
	srv, router := setupTestServer(t)
	ensureInactiveNodesTable(t, srv)
	ensureNodeChangesTable(t, srv)

	req := httptest.NewRequest("GET", "/api/analytics/network-digest", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"window":"7d"`) {
		t.Errorf("response should echo window=7d, got: %s", w.Body.String())
	}
}

// TestComputeNetworkDigest_NewNodesCappedFlag confirms NewNodesCapped is
// set when the underlying fetch hits newNodesSQLFetchCap and the oldest
// fetched row is still inside the window (real count may exceed what
// was fetched) -- and stays false for an ordinary, uncapped window.
func TestComputeNetworkDigest_NewNodesCappedFlag(t *testing.T) {
	srv, _ := setupTestServer(t)
	ensureInactiveNodesTable(t, srv)
	ensureNodeChangesTable(t, srv)

	now := time.Now().UTC()
	for i := 0; i < newNodesSQLFetchCap; i++ {
		pubkey := fmt.Sprintf("capnode%010d0001", i)
		insertNewNodeRow(t, srv, pubkey, "Cap", "repeater", f64(56.0), f64(10.0), now.Add(-time.Duration(i)*time.Minute).Format(time.RFC3339))
	}

	digest, err := srv.computeNetworkDigest(now.Add(-7*24*time.Hour), "all")
	if err != nil {
		t.Fatalf("computeNetworkDigest: %v", err)
	}
	if !digest.NewNodesCapped {
		t.Errorf("NewNodesCapped = false, want true when the fetch cap is hit within the window")
	}
	if digest.NewNodes != newNodesSQLFetchCap {
		t.Errorf("NewNodes = %d, want %d (the full capped fetch)", digest.NewNodes, newNodesSQLFetchCap)
	}
}

// TestComputeNetworkDigest_NewNodesNotCappedWhenUnderLimit confirms the
// flag stays false on an ordinary window well under the fetch cap.
func TestComputeNetworkDigest_NewNodesNotCappedWhenUnderLimit(t *testing.T) {
	srv, _ := setupTestServer(t)
	ensureInactiveNodesTable(t, srv)
	ensureNodeChangesTable(t, srv)

	now := time.Now().UTC()
	insertNewNodeRow(t, srv, "notcapped0000001", "Solo", "repeater", f64(56.0), f64(10.0), now.Add(-1*time.Hour).Format(time.RFC3339))

	digest, err := srv.computeNetworkDigest(now.Add(-7*24*time.Hour), "all")
	if err != nil {
		t.Fatalf("computeNetworkDigest: %v", err)
	}
	if digest.NewNodesCapped {
		t.Errorf("NewNodesCapped = true, want false when nowhere near the fetch cap")
	}
}

// TestComputeNetworkDigest_ChangesCappedFlag mirrors the new-nodes cap
// test for the node_changes side of the digest.
func TestComputeNetworkDigest_ChangesCappedFlag(t *testing.T) {
	srv, _ := setupTestServer(t)
	ensureInactiveNodesTable(t, srv)
	ensureNodeChangesTable(t, srv)

	now := time.Now().UTC()
	for i := 0; i < nodeChangesSQLFetchCap; i++ {
		pubkey := fmt.Sprintf("capchange%09d01", i)
		insertNodeChangeRow(t, srv, pubkey, "role", "companion", "repeater", now.Add(-time.Duration(i)*time.Minute).Format(time.RFC3339))
	}

	digest, err := srv.computeNetworkDigest(now.Add(-7*24*time.Hour), "all")
	if err != nil {
		t.Fatalf("computeNetworkDigest: %v", err)
	}
	if !digest.ChangesCapped {
		t.Errorf("ChangesCapped = false, want true when the fetch cap is hit within the window")
	}
	if digest.RoleChanges != nodeChangesSQLFetchCap {
		t.Errorf("RoleChanges = %d, want %d (the full capped fetch)", digest.RoleChanges, nodeChangesSQLFetchCap)
	}
}

// TestComputeNetworkDigest_TopAreaPrefersMostSpecific confirms an
// umbrella area whose bounding box contains a smaller, more specific
// area does NOT dominate Most Growth just because it also matches every
// node -- reported https://github.com/dborup/CoreScope after live
// verification on stg always showed "Europa" as Most Growth regardless
// of where nodes actually were, since AreaKeysForPoint (used before this
// fix) tallies every overlapping area equally rather than picking the
// most specific one per node.
func TestComputeNetworkDigest_TopAreaPrefersMostSpecific(t *testing.T) {
	srv, _ := setupTestServer(t)
	ensureInactiveNodesTable(t, srv)
	ensureNodeChangesTable(t, srv)

	srv.cfg.Areas = map[string]AreaEntry{
		// A broad umbrella area whose box contains AREAB entirely.
		"UMBRELLA": {Label: "Umbrella", LatMin: f64(0), LatMax: f64(90), LonMin: f64(0), LonMax: f64(90)},
		"AREAB":    {Label: "Area B", LatMin: f64(55.9), LatMax: f64(56.2), LonMin: f64(9.9), LonMax: f64(10.2)},
	}
	now := time.Now().UTC()
	insertNewNodeRow(t, srv, "specific0000001", "S1", "repeater", f64(56.0), f64(10.0), now.Add(-1*time.Hour).Format(time.RFC3339))

	digest, err := srv.computeNetworkDigest(now.Add(-7*24*time.Hour), "all")
	if err != nil {
		t.Fatalf("computeNetworkDigest: %v", err)
	}
	if digest.TopArea == nil || digest.TopArea.Label != "Area B" {
		t.Errorf("TopArea = %+v, want the more specific \"Area B\", not the umbrella area that also happens to contain it", digest.TopArea)
	}
}

// TestComputeNetworkDigest_OriginFilterNewNodes confirms origin=domestic
// / origin=foreign narrow the New Nodes count (and Most Growth) using
// the same nodes.foreign_advert flag as Tools > New Nodes' toggle.
func TestComputeNetworkDigest_OriginFilterNewNodes(t *testing.T) {
	srv, _ := setupTestServer(t)
	ensureInactiveNodesTable(t, srv)
	ensureNodeChangesTable(t, srv)

	now := time.Now().UTC()
	insertNewNodeRow(t, srv, "domesticnew0000001", "Domestic", "repeater", f64(56.0), f64(10.0), now.Add(-1*time.Hour).Format(time.RFC3339))
	insertNewNodeRow(t, srv, "foreignnew00000001", "Foreign", "repeater", f64(52.5), f64(13.4), now.Add(-1*time.Hour).Format(time.RFC3339))
	if _, err := srv.db.conn.Exec(`UPDATE nodes SET foreign_advert = 1 WHERE public_key = ?`, "foreignnew00000001"); err != nil {
		t.Fatalf("set foreign_advert: %v", err)
	}

	since := now.Add(-7 * 24 * time.Hour)
	all, err := srv.computeNetworkDigest(since, "all")
	if err != nil {
		t.Fatalf("computeNetworkDigest(all): %v", err)
	}
	if all.NewNodes != 2 {
		t.Errorf("all.NewNodes = %d, want 2", all.NewNodes)
	}

	domestic, err := srv.computeNetworkDigest(since, "domestic")
	if err != nil {
		t.Fatalf("computeNetworkDigest(domestic): %v", err)
	}
	if domestic.NewNodes != 1 {
		t.Errorf("domestic.NewNodes = %d, want 1", domestic.NewNodes)
	}

	foreign, err := srv.computeNetworkDigest(since, "foreign")
	if err != nil {
		t.Fatalf("computeNetworkDigest(foreign): %v", err)
	}
	if foreign.NewNodes != 1 {
		t.Errorf("foreign.NewNodes = %d, want 1", foreign.NewNodes)
	}
}

// TestComputeNetworkDigest_OriginFilterNodeChanges confirms origin
// filtering also applies to the node_changes side, resolved live
// against the node's current foreign_advert (node_changes rows don't
// store their own snapshot of it).
func TestComputeNetworkDigest_OriginFilterNodeChanges(t *testing.T) {
	srv, _ := setupTestServer(t)
	ensureInactiveNodesTable(t, srv)
	ensureNodeChangesTable(t, srv)

	now := time.Now().UTC()
	inWindow := now.Add(-1 * time.Hour).Format(time.RFC3339)
	insertNewNodeRow(t, srv, "domesticchg0000001", "Domestic", "repeater", f64(56.0), f64(10.0), now.Add(-30*24*time.Hour).Format(time.RFC3339))
	insertNewNodeRow(t, srv, "foreignchg00000001", "Foreign", "repeater", f64(52.5), f64(13.4), now.Add(-30*24*time.Hour).Format(time.RFC3339))
	if _, err := srv.db.conn.Exec(`UPDATE nodes SET foreign_advert = 1 WHERE public_key = ?`, "foreignchg00000001"); err != nil {
		t.Fatalf("set foreign_advert: %v", err)
	}
	insertNodeChangeRow(t, srv, "domesticchg0000001", "role", "companion", "repeater", inWindow)
	insertNodeChangeRow(t, srv, "foreignchg00000001", "role", "companion", "repeater", inWindow)

	since := now.Add(-7 * 24 * time.Hour)
	domestic, err := srv.computeNetworkDigest(since, "domestic")
	if err != nil {
		t.Fatalf("computeNetworkDigest(domestic): %v", err)
	}
	if domestic.RoleChanges != 1 {
		t.Errorf("domestic.RoleChanges = %d, want 1", domestic.RoleChanges)
	}

	foreign, err := srv.computeNetworkDigest(since, "foreign")
	if err != nil {
		t.Fatalf("computeNetworkDigest(foreign): %v", err)
	}
	if foreign.RoleChanges != 1 {
		t.Errorf("foreign.RoleChanges = %d, want 1", foreign.RoleChanges)
	}
}

// TestHandleNetworkDigest_OriginEchoedAndDefaulted confirms the handler
// defaults origin to "all" and echoes back whatever was requested.
func TestHandleNetworkDigest_OriginEchoedAndDefaulted(t *testing.T) {
	srv, router := setupTestServer(t)
	ensureInactiveNodesTable(t, srv)
	ensureNodeChangesTable(t, srv)

	req := httptest.NewRequest("GET", "/api/analytics/network-digest", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), `"origin":"all"`) {
		t.Errorf("response should default+echo origin=all, got: %s", w.Body.String())
	}

	req2 := httptest.NewRequest("GET", "/api/analytics/network-digest?origin=foreign", nil)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	if !strings.Contains(w2.Body.String(), `"origin":"foreign"`) {
		t.Errorf("response should echo origin=foreign, got: %s", w2.Body.String())
	}
}

// TestHandleNetworkDigest_InvalidOriginRejected confirms a malformed
// origin query param is a 400, not a silent fallback to "all".
func TestHandleNetworkDigest_InvalidOriginRejected(t *testing.T) {
	srv, router := setupTestServer(t)
	ensureInactiveNodesTable(t, srv)
	ensureNodeChangesTable(t, srv)

	req := httptest.NewRequest("GET", "/api/analytics/network-digest?origin=extraterrestrial", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("status = %d, want 400 for an invalid origin, body: %s", w.Code, w.Body.String())
	}
}

// TestHandleNetworkDigest_InvalidWindowRejected confirms a malformed
// window query param is a 400, not a silent fallback.
func TestHandleNetworkDigest_InvalidWindowRejected(t *testing.T) {
	srv, router := setupTestServer(t)
	ensureInactiveNodesTable(t, srv)
	ensureNodeChangesTable(t, srv)

	req := httptest.NewRequest("GET", "/api/analytics/network-digest?window=notaduration", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("status = %d, want 400 for an invalid window, body: %s", w.Code, w.Body.String())
	}
}
