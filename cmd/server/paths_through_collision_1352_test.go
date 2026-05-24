package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// TestHandleNodePaths_PrefixCollision_1352 reproduces issue #1352.
//
// Setup: 3 nodes share 2-char prefix "c0":
//
//	A = c0dedad4...  (no GPS)         — "Kpa Roof Solar"-shaped
//	B = c0ffeec7...  (HAS GPS @ SF)   — actual relay per canonical resolved_path
//	C = c0efb77f...  (no GPS)
//
// A packet observed with raw path ["c0"] has a CANONICAL resolved_path that
// names B (c0ffeec7...) — produced by the hop-disambiguator using observer
// context. The query for paths-through-X must use the canonical
// resolved_path to decide membership, NOT a naive prefix lookup.
//
// Before the fix: handleNodePaths' fallback branch biased the resolver
// with hopContext=[targetPK], so geo-proximity / observation-count tiers
// promoted whichever target was queried. Every paths-through call for any
// "c0" node returned this tx — wrong-node attribution.
//
// After the fix: the canonical resolved_path (already disambiguated at
// ingest) is the sole source of truth for membership. Only paths-through-B
// includes the tx; paths-through-A and paths-through-C exclude it.
func TestHandleNodePaths_PrefixCollision_1352(t *testing.T) {
	db := setupTestDB(t)
	recent := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	recentEpoch := time.Now().Add(-1 * time.Hour).Unix()

	nodeAPK := "c0dedad42222aaaa" // Kpa Roof Solar shape — no GPS
	nodeBPK := "c0ffeec733333333" // C0ffee SF — has GPS (the real relay)
	nodeCPK := "c0efb77f44444444" // KiekR — no GPS

	// Insert nodes — B has GPS so the OLD biased resolver could have picked B
	// when querying paths-through-B. But the bug is that the OLD resolver
	// picked the *queried* target via the hopContext anchor — A when querying
	// A, C when querying C. So the bug manifests by A and C returning the tx.
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, 'NodeA', 'repeater', 0, 0, ?, '2026-01-01', 1)`, nodeAPK, recent)
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, 'NodeB', 'repeater', 37.78, -122.4, ?, '2026-01-01', 1)`, nodeBPK, recent)
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, 'NodeC', 'repeater', 0, 0, ?, '2026-01-01', 1)`, nodeCPK, recent)

	// Single tx with raw path ["c0"] and canonical resolved_path naming B.
	db.conn.Exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (42, 'DEAD', 'hash_1352', ?)`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
		VALUES (42, NULL, '["c0"]', ?, ?)`, recentEpoch, `["`+nodeBPK+`"]`)

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

	query := func(pk string) NodePathsResponse {
		req := httptest.NewRequest("GET", "/api/nodes/"+pk+"/paths", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /paths for %s: code=%d body=%s", pk, w.Code, w.Body.String())
		}
		var resp NodePathsResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return resp
	}

	respA := query(nodeAPK)
	respB := query(nodeBPK)
	respC := query(nodeCPK)

	// A and C are NOT in the canonical resolved_path → must be excluded.
	if respA.TotalTransmissions != 0 {
		t.Errorf("nodeA (c0dedad…) paths-through: canonical resolved_path names B, not A — "+
			"expected 0 transmissions, got %d (wrong-node attribution #1352)",
			respA.TotalTransmissions)
	}
	if respC.TotalTransmissions != 0 {
		t.Errorf("nodeC (c0efb77…) paths-through: canonical resolved_path names B, not C — "+
			"expected 0 transmissions, got %d (wrong-node attribution #1352)",
			respC.TotalTransmissions)
	}
	// B IS named by the canonical resolved_path → must be included.
	if respB.TotalTransmissions != 1 {
		t.Errorf("nodeB (c0ffeec…) paths-through: B is canonical relay — "+
			"expected 1 transmission, got %d", respB.TotalTransmissions)
	}

	_ = strings.ToLower // silence import if unused above
}

// TestHandleNodePaths_PrefixCollision_1352_FallbackBranch covers the WORSE
// case: obs has NO persisted resolved_path. The OLD fallback branch invoked
// pm.resolveWithContext(hop, []string{lowerPK}, graph) — anchoring the
// resolver on the queried node. Tier-2 (geo_proximity) then selected the
// GPS candidate closest to the centroid of context, which == the target
// itself when the target has GPS. Result: every paths-through-X query that
// shared the prefix returned the tx with X attribution.
//
// Setup: 3 nodes share prefix "c0", ALL have GPS at distinct sites:
//
//	A = c0dedad… @ (37.78, -122.4)  Kpa Roof Solar shape
//	B = c0ffeec… @ (37.79, -122.41) C0ffee SF — the *real* relay
//	C = c0efb77… @ (37.5,  -122.0)  far
//
// With NULL resolved_path the system cannot disambiguate. The CORRECT
// behavior under #1352 is: when no canonical resolved_path exists AND the
// hop prefix has multiple plausible candidates, do NOT attribute the tx to
// any one of them — exclude (or flag ambiguous). Specifically, the OLD
// "anchor the resolver on the queried node" hack must die.
//
// Assertion: at most ONE of {A, B, C} may include this tx in
// paths-through. The old code would include it for ALL THREE.
func TestHandleNodePaths_PrefixCollision_1352_FallbackBranch(t *testing.T) {
	db := setupTestDB(t)
	recent := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	recentEpoch := time.Now().Add(-1 * time.Hour).Unix()

	nodeAPK := "c0dedad42222aaaa"
	nodeBPK := "c0ffeec733333333"
	nodeCPK := "c0efb77f44444444"

	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, 'NodeA', 'repeater', 37.78, -122.40, ?, '2026-01-01', 1)`, nodeAPK, recent)
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, 'NodeB', 'repeater', 37.79, -122.41, ?, '2026-01-01', 1)`, nodeBPK, recent)
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, 'NodeC', 'repeater', 37.50, -122.00, ?, '2026-01-01', 1)`, nodeCPK, recent)

	// Single tx, raw path ["c0"], NULL resolved_path.
	db.conn.Exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (43, 'BEEF', 'hash_1352_fb', ?)`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
		VALUES (43, NULL, '["c0"]', ?, NULL)`, recentEpoch)

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

	query := func(pk string) int {
		req := httptest.NewRequest("GET", "/api/nodes/"+pk+"/paths", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /paths for %s: code=%d body=%s", pk, w.Code, w.Body.String())
		}
		var resp NodePathsResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		return resp.TotalTransmissions
	}

	a := query(nodeAPK)
	b := query(nodeBPK)
	c := query(nodeCPK)

	includes := 0
	for _, x := range []int{a, b, c} {
		if x > 0 {
			includes++
		}
	}
	// Old buggy code: a==1 && b==1 && c==1 (all three) — wrong-node attribution.
	// Fixed: at most one (or zero — ambiguous). Assert ≤1 to lock in the fix.
	if includes > 1 {
		t.Errorf("ambiguous-prefix tx with NULL resolved_path attributed to %d nodes (A=%d B=%d C=%d); "+
			"expected ≤1 — paths-through must not return the same tx for multiple sibling prefix collisions (#1352)",
			includes, a, b, c)
	}
}
