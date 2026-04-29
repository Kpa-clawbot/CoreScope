package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// TestHandleNodePaths_PrefixCollisionExclusion verifies that paths through a node
// sharing a 2-char prefix with another node are not returned as false positives
// when they have no resolved_path data (issue #929).
//
// Setup:
//   - nodeA (target): pubkey starts with "7a", no GPS
//   - nodeB (other):  pubkey starts with "7a", has GPS → "7a" resolves to nodeB
//   - tx1: path ["7a"], resolved_path NULL → false positive candidate, must be excluded
//   - tx2: path ["7a"], resolved_path contains nodeA pubkey → SQL-confirmed, must be included
func TestHandleNodePaths_PrefixCollisionExclusion(t *testing.T) {
	db := setupTestDB(t)
	recent := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	recentEpoch := time.Now().Add(-1 * time.Hour).Unix()

	nodeAPK := "7acb1111aaaabbbb"
	nodeBPK := "7aff2222ccccdddd" // same "7a" prefix, has GPS so resolveHop("7a") picks B

	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, 'NodeA', 'repeater', 0, 0, ?, '2026-01-01', 1)`, nodeAPK, recent)
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, 'NodeB', 'repeater', 37.5, -122.0, ?, '2026-01-01', 1)`, nodeBPK, recent)

	// tx1: no resolved_path — should be excluded by hop-level check
	db.conn.Exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (10, 'AA', 'hash_fp', ?)`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
		VALUES (10, NULL, '["7a"]', ?, NULL)`, recentEpoch)

	// tx2: resolved_path confirms nodeA — must be included
	db.conn.Exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (11, 'BB', 'hash_tp', ?)`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
		VALUES (11, NULL, '["7a"]', ?, ?)`, recentEpoch, `["`+nodeAPK+`"]`)

	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/nodes/"+nodeAPK+"/paths", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp NodePathsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// Only the SQL-confirmed path (tx2) should be present; tx1 (false positive) must be excluded.
	// tx1 and tx2 share the same raw path ["7a"] so they collapse into 1 unique path group.
	// If tx1 were included, TotalTransmissions would be 2.
	if resp.TotalPaths != 1 {
		t.Errorf("expected 1 path group, got %d", resp.TotalPaths)
	}
	if resp.TotalTransmissions != 1 {
		t.Errorf("expected 1 transmission (false positive tx1 excluded), got %d", resp.TotalTransmissions)
	}
}
