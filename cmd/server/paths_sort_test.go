package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// TestHandleNodePaths_SortByRecency_1145 is the regression test for issue #1145.
//
// Prior to the fix, paths were returned in map-iteration order (non-deterministic).
// After the fix, paths are sorted by LastSeen descending (newest first), with
// Count as a tiebreaker (higher first).
//
// Setup: target node "aa..." is reached via three distinct paths.
//
//	Path A (via relay "11..."): 3 transmissions, last seen 2026-01-03 (oldest)
//	Path B (via relay "22..."): 1 transmission,  last seen 2026-05-01 (newest)
//	Path C (direct — "aa..." only): 2 transmissions, last seen 2026-03-02 (middle)
//
// Expected sort: B (newest) → C (middle) → A (oldest)
// Also covers: when LastSeen is equal, Count descending is the tiebreaker.
func TestHandleNodePaths_SortByRecency_1145(t *testing.T) {
	db := setupTestDB(t)

	targetPK := "aabbccdd11111111"
	relay1PK := "1111111100000000"
	relay2PK := "2222222200000000"

	epoch := func(ts string) int64 {
		v, _ := time.Parse(time.RFC3339, ts)
		return v.Unix()
	}

	// Only the target node needs to be in the nodes table.
	// Relay pubkeys appear only in resolved_path; they don't need a nodes row.
	mustExec(t, db, `INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, 'Target', 'repeater', 0, 0, '2026-05-01T00:00:00Z', '2026-01-01T00:00:00Z', 1)`, targetPK)

	// -- Path A (via relay1): 3 txs, last seen 2026-01-03 → group sig "relay1PK→targetPK" --
	for txID, ts := range map[int]string{
		1: "2026-01-01T00:00:00Z",
		2: "2026-01-02T00:00:00Z",
		3: "2026-01-03T00:00:00Z",
	} {
		mustExec(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (?, 'AA', ?, ?)`,
			txID, "hashA"+string(rune('0'+txID)), ts)
		mustExec(t, db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
			VALUES (?, NULL, '["11", "aa"]', ?, ?)`,
			txID, epoch(ts), `["`+relay1PK+`", "`+targetPK+`"]`)
	}

	// -- Path B (via relay2): 1 tx, last seen 2026-05-01 → group sig "relay2PK→targetPK" --
	mustExec(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (4, 'BB', 'hashB1', '2026-05-01T00:00:00Z')`)
	mustExec(t, db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
		VALUES (4, NULL, '["22", "aa"]', ?, ?)`,
		epoch("2026-05-01T00:00:00Z"), `["`+relay2PK+`", "`+targetPK+`"]`)

	// -- Path C (direct — target is sole hop): 2 txs, last seen 2026-03-02 --
	for txID, ts := range map[int]string{
		5: "2026-03-01T00:00:00Z",
		6: "2026-03-02T00:00:00Z",
	} {
		mustExec(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (?, 'CC', ?, ?)`,
			txID, "hashC"+string(rune('0'+txID)), ts)
		mustExec(t, db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
			VALUES (?, NULL, '["aa"]', ?, ?)`,
			txID, epoch(ts), `["`+targetPK+`"]`)
	}

	// Wire up server + store
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

	req := httptest.NewRequest("GET", "/api/nodes/"+targetPK+"/paths", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /paths: code=%d body=%s", w.Code, w.Body.String())
	}
	var resp NodePathsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(resp.Paths) != 3 {
		t.Fatalf("expected 3 distinct paths, got %d: %+v", len(resp.Paths), resp.Paths)
	}
	if resp.TotalTransmissions != 6 {
		t.Errorf("expected TotalTransmissions=6, got %d", resp.TotalTransmissions)
	}

	// Sort order: B (newest, 2026-05-01) → C (middle, 2026-03-02) → A (oldest, 2026-01-03)
	wantCounts := []int{1, 2, 3}
	for i, want := range wantCounts {
		got := resp.Paths[i].Count
		if got != want {
			t.Errorf("Paths[%d].Count = %d, want %d (sort order wrong — paths must be newest-first)", i, got, want)
		}
	}
}

// TestHandleNodePaths_SortCountTiebreaker_1145 verifies that when two paths
// have identical LastSeen, the one with higher Count appears first.
func TestHandleNodePaths_SortCountTiebreaker_1145(t *testing.T) {
	db := setupTestDB(t)

	targetPK := "ccddeeFF11111111"
	relay1PK := "aaaa111100000000"
	relay2PK := "bbbb222200000000"
	sameTS := "2026-04-15T12:00:00Z"
	epoch := func(ts string) int64 {
		v, _ := time.Parse(time.RFC3339, ts)
		return v.Unix()
	}

	mustExec(t, db, `INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, 'Tgt', 'repeater', 0, 0, ?, '2026-01-01T00:00:00Z', 1)`, targetPK, sameTS)

	// Path X: 3 txs, all at sameTS → higher count
	for txID, ts := range map[int]string{
		10: "2026-04-15T11:00:00Z",
		11: "2026-04-15T11:30:00Z",
		12: sameTS,
	} {
		mustExec(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (?, 'XX', ?, ?)`,
			txID, "hashX"+string(rune('0'+txID)), ts)
		mustExec(t, db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
			VALUES (?, NULL, '["aa", "cc"]', ?, ?)`,
			txID, epoch(ts), `["`+relay1PK+`", "`+targetPK+`"]`)
	}

	// Path Y: 1 tx, at sameTS → lower count
	mustExec(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (20, 'YY', 'hashY1', ?)`, sameTS)
	mustExec(t, db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
		VALUES (20, NULL, '["bb", "cc"]', ?, ?)`,
		epoch(sameTS), `["`+relay2PK+`", "`+targetPK+`"]`)

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

	req := httptest.NewRequest("GET", "/api/nodes/"+targetPK+"/paths", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /paths: code=%d body=%s", w.Code, w.Body.String())
	}
	var resp NodePathsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Paths) != 2 {
		t.Fatalf("expected 2 paths, got %d", len(resp.Paths))
	}
	// Path X (count=3) must sort before Path Y (count=1) when LastSeen is equal.
	if resp.Paths[0].Count != 3 {
		t.Errorf("Paths[0].Count = %d, want 3 (higher-count path must sort first when LastSeen equal)", resp.Paths[0].Count)
	}
	if resp.Paths[1].Count != 1 {
		t.Errorf("Paths[1].Count = %d, want 1", resp.Paths[1].Count)
	}
}
