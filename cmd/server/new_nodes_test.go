package main

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ensureInactiveNodesTable creates a minimal inactive_nodes table for tests
// that need it -- the shared setupTestDB schema predates this feature (it
// doesn't create inactive_nodes at all), same reasoning as ping_scores_test.go's
// setupPingScoresFixture creating ping_triggers locally rather than touching
// the schema shared by every other test in the package.
func ensureInactiveNodesTable(t *testing.T, srv *Server) {
	t.Helper()
	if _, err := srv.db.conn.Exec(`CREATE TABLE IF NOT EXISTS inactive_nodes (
		public_key TEXT PRIMARY KEY,
		name TEXT,
		role TEXT,
		lat REAL,
		lon REAL,
		last_seen TEXT,
		first_seen TEXT
	)`); err != nil {
		t.Fatalf("create inactive_nodes: %v", err)
	}
}

func insertNewNodeRow(t *testing.T, srv *Server, pubkey, name, role string, lat, lon *float64, firstSeen string) {
	t.Helper()
	if _, err := srv.db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen) VALUES (?,?,?,?,?,?,?)`,
		pubkey, name, role, lat, lon, firstSeen, firstSeen); err != nil {
		t.Fatalf("insert node %s: %v", pubkey, err)
	}
}

// TestComputeNewNodes_OrderedNewestFirst confirms the feed is sorted by
// first_seen descending (most recently discovered first).
func TestComputeNewNodes_OrderedNewestFirst(t *testing.T) {
	srv, _ := setupTestServer(t)
	ensureInactiveNodesTable(t, srv)

	now := time.Now().UTC()
	insertNewNodeRow(t, srv, "newnode0000000001", "Oldest", "repeater", f64(56.0), f64(10.0), now.Add(-3*time.Hour).Format(time.RFC3339))
	insertNewNodeRow(t, srv, "newnode0000000002", "Newest", "repeater", f64(56.0), f64(10.0), now.Add(-1*time.Minute).Format(time.RFC3339))
	insertNewNodeRow(t, srv, "newnode0000000003", "Middle", "repeater", f64(56.0), f64(10.0), now.Add(-1*time.Hour).Format(time.RFC3339))

	entries, err := srv.computeNewNodes(50)
	if err != nil {
		t.Fatalf("computeNewNodes: %v", err)
	}
	byKey := map[string]int{}
	for i, e := range entries {
		byKey[e.PublicKey] = i
	}
	newest, okN := byKey["newnode0000000002"]
	middle, okM := byKey["newnode0000000003"]
	oldest, okO := byKey["newnode0000000001"]
	if !okN || !okM || !okO {
		t.Fatalf("expected all 3 test nodes present, got indices: newest=%v middle=%v oldest=%v (present: %v/%v/%v)", newest, middle, oldest, okN, okM, okO)
	}
	if !(newest < middle && middle < oldest) {
		t.Errorf("expected newest-first order (newest=%d middle=%d oldest=%d), got wrong order", newest, middle, oldest)
	}
}

// TestComputeNewNodes_ExcludesResurrectedNodes confirms a node that ALSO
// appears in inactive_nodes (previously pruned for inactivity, now
// advertising again with a freshly-INSERTed nodes.first_seen) is excluded
// -- it's a returning node, not a genuinely new one.
func TestComputeNewNodes_ExcludesResurrectedNodes(t *testing.T) {
	srv, _ := setupTestServer(t)
	ensureInactiveNodesTable(t, srv)

	now := time.Now().UTC()
	// Fresh nodes.first_seen (as if just re-INSERTed after being deleted
	// by MoveStaleNodes), but ALSO present in inactive_nodes with an old
	// first_seen -- proof it was seen long before.
	insertNewNodeRow(t, srv, "resurrected000001", "Returning", "repeater", f64(56.0), f64(10.0), now.Add(-1*time.Minute).Format(time.RFC3339))
	if _, err := srv.db.conn.Exec(`INSERT INTO inactive_nodes (public_key, name, role, lat, lon, last_seen, first_seen) VALUES (?,?,?,?,?,?,?)`,
		"resurrected000001", "Returning", "repeater", 56.0, 10.0, "2025-01-01T00:00:00Z", "2025-01-01T00:00:00Z"); err != nil {
		t.Fatalf("insert inactive_nodes row: %v", err)
	}
	// A genuinely new node, never pruned.
	insertNewNodeRow(t, srv, "genuinelynew000001", "BrandNew", "repeater", f64(56.0), f64(10.0), now.Add(-30*time.Second).Format(time.RFC3339))

	entries, err := srv.computeNewNodes(50)
	if err != nil {
		t.Fatalf("computeNewNodes: %v", err)
	}
	for _, e := range entries {
		if e.PublicKey == "resurrected000001" {
			t.Errorf("resurrected000001 should be excluded (it's in inactive_nodes -- a returning node, not new), got: %+v", e)
		}
	}
	var foundNew bool
	for _, e := range entries {
		if e.PublicKey == "genuinelynew000001" {
			foundNew = true
		}
	}
	if !foundNew {
		t.Errorf("genuinelynew000001 should be present, entries: %+v", entries)
	}
}

// TestComputeNewNodes_ExcludesBlacklisted confirms nodeBlacklist is honored.
func TestComputeNewNodes_ExcludesBlacklisted(t *testing.T) {
	srv, _ := setupTestServer(t)
	ensureInactiveNodesTable(t, srv)

	now := time.Now().UTC()
	insertNewNodeRow(t, srv, "blacklisted0000001", "Hidden", "repeater", f64(56.0), f64(10.0), now.Add(-1*time.Minute).Format(time.RFC3339))
	srv.cfg.SetNodeBlacklist([]string{"blacklisted0000001"})

	entries, err := srv.computeNewNodes(50)
	if err != nil {
		t.Fatalf("computeNewNodes: %v", err)
	}
	for _, e := range entries {
		if e.PublicKey == "blacklisted0000001" {
			t.Errorf("blacklisted node should be excluded, got: %+v", e)
		}
	}
}

// TestComputeNewNodes_ResolvesAreas confirms a node's position is matched
// against configured areas the same way Ping Scores' Area Activity
// leaderboard and View Path's touchedAreas do (AreaKeysForPoint).
func TestComputeNewNodes_ResolvesAreas(t *testing.T) {
	srv, _ := setupTestServer(t)
	ensureInactiveNodesTable(t, srv)

	srv.cfg.Areas = map[string]AreaEntry{
		"AREAA": {Label: "Area A", LatMin: f64(55.9), LatMax: f64(56.2), LonMin: f64(9.9), LonMax: f64(10.2)},
	}
	now := time.Now().UTC()
	insertNewNodeRow(t, srv, "inarea00000000001", "InArea", "repeater", f64(56.0), f64(10.0), now.Add(-1*time.Minute).Format(time.RFC3339))
	insertNewNodeRow(t, srv, "outsidearea0000001", "Outside", "repeater", f64(10.0), f64(50.0), now.Add(-2*time.Minute).Format(time.RFC3339))

	entries, err := srv.computeNewNodes(50)
	if err != nil {
		t.Fatalf("computeNewNodes: %v", err)
	}
	var inArea, outside *NewNodeEntry
	for i := range entries {
		if entries[i].PublicKey == "inarea00000000001" {
			inArea = &entries[i]
		}
		if entries[i].PublicKey == "outsidearea0000001" {
			outside = &entries[i]
		}
	}
	if inArea == nil || len(inArea.Areas) != 1 || inArea.Areas[0] != "Area A" {
		t.Errorf("inarea00000000001 Areas = %+v, want [\"Area A\"]", inArea)
	}
	if outside == nil || len(outside.Areas) != 0 {
		t.Errorf("outsidearea0000001 Areas = %+v, want empty (outside configured area)", outside)
	}
}

// TestComputeNewNodes_LimitRespected confirms the limit param truncates
// after blacklist filtering (not before, which could silently undercount).
func TestComputeNewNodes_LimitRespected(t *testing.T) {
	srv, _ := setupTestServer(t)
	ensureInactiveNodesTable(t, srv)

	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		insertNewNodeRow(t, srv, "limittest0000000"+string(rune('1'+i)), "Node"+string(rune('1'+i)), "repeater", f64(56.0), f64(10.0), now.Add(-time.Duration(i)*time.Minute).Format(time.RFC3339))
	}
	entries, err := srv.computeNewNodes(2)
	if err != nil {
		t.Fatalf("computeNewNodes: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("len(entries) = %d, want 2 (limit)", len(entries))
	}
}

// TestHandleNewNodes_ReturnsWrappedList confirms the HTTP handler wraps
// the slice under "newNodes" and responds 200.
func TestHandleNewNodes_ReturnsWrappedList(t *testing.T) {
	srv, router := setupTestServer(t)
	ensureInactiveNodesTable(t, srv)

	req := httptest.NewRequest("GET", "/api/analytics/new-nodes?limit=10", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"newNodes"`) {
		t.Errorf("response missing newNodes key, got: %s", w.Body.String())
	}
}
