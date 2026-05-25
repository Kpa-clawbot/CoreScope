package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// collisionScenario captures the shared fixture state used by every #1352
// sub-test: 3 nodes sharing the 2-char "c0" prefix, plus a wired-up
// server + router ready to serve /api/nodes/{pk}/paths.
type collisionScenario struct {
	srv    *Server
	db     *DB
	router *mux.Router

	nodeAPK string
	nodeBPK string
	nodeCPK string

	recent      string
	recentEpoch int64
}

// mustExec runs db.conn.Exec and fails the test on error. Used so INSERT
// failures (schema drift, NOT NULL violations) surface as test failures
// rather than silently producing an empty database that lets later
// assertions pass vacuously (#1352 round-1 adv #2).
func mustExec(t *testing.T, db *DB, query string, args ...any) {
	t.Helper()
	if _, err := db.conn.Exec(query, args...); err != nil {
		t.Fatalf("Exec failed: %v\n  query: %s\n  args:  %v", err, query, args)
	}
}

// setupCollisionScenario wires up the shared #1352 fixture: 3 "c0"-prefix
// nodes with configurable GPS, a Server + PacketStore + router. Caller
// inserts transmissions/observations and queries via s.query.
func setupCollisionScenario(t *testing.T, withGPS bool) *collisionScenario {
	t.Helper()
	db := setupTestDB(t)
	recent := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	recentEpoch := time.Now().Add(-1 * time.Hour).Unix()

	sc := &collisionScenario{
		db:          db,
		nodeAPK:     "c0dedad42222aaaa",
		nodeBPK:     "c0ffeec733333333",
		nodeCPK:     "c0efb77f44444444",
		recent:      recent,
		recentEpoch: recentEpoch,
	}

	// GPS placement: when withGPS=true, ALL three siblings have distinct
	// GPS points (worst-case for the biased resolver, see fallback test).
	// When withGPS=false, only B has GPS (canonical-branch test).
	aLat, aLon := 0.0, 0.0
	bLat, bLon := 37.79, -122.41
	cLat, cLon := 0.0, 0.0
	if withGPS {
		aLat, aLon = 37.78, -122.40
		cLat, cLon = 37.50, -122.00
	}

	mustExec(t, db, `INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, 'NodeA', 'repeater', ?, ?, ?, '2026-01-01', 1)`, sc.nodeAPK, aLat, aLon, recent)
	mustExec(t, db, `INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, 'NodeB', 'repeater', ?, ?, ?, '2026-01-01', 1)`, sc.nodeBPK, bLat, bLon, recent)
	mustExec(t, db, `INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, 'NodeC', 'repeater', ?, ?, ?, '2026-01-01', 1)`, sc.nodeCPK, cLat, cLon, recent)

	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	sc.srv = srv

	// store is wired after observations are inserted, by reloadStore().
	return sc
}

// reloadStore (re)builds the PacketStore from the current DB state. Must
// be called AFTER all transmissions/observations are inserted, otherwise
// the store snapshot is empty and queries return nothing.
func (sc *collisionScenario) reloadStore(t *testing.T) {
	t.Helper()
	store := NewPacketStore(sc.db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	sc.srv.store = store
	router := mux.NewRouter()
	sc.srv.RegisterRoutes(router)
	sc.router = router
}

// query issues GET /api/nodes/{pk}/paths and returns the decoded response.
func (sc *collisionScenario) query(t *testing.T, pk string) NodePathsResponse {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/nodes/"+pk+"/paths", nil)
	w := httptest.NewRecorder()
	sc.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /paths for %s: code=%d body=%s", pk, w.Code, w.Body.String())
	}
	var resp NodePathsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return resp
}

// TestHandleNodePaths_PrefixCollision_1352 reproduces issue #1352.
//
// Setup: 3 nodes share 2-char prefix "c0":
//
//	A = c0dedad4...  (no GPS)
//	B = c0ffeec7...  (HAS GPS @ SF)   — canonical relay per resolved_path
//	C = c0efb77f...  (no GPS)
//
// A packet observed with raw path ["c0"] has a CANONICAL resolved_path
// that names B (c0ffeec7…) — produced by the hop-disambiguator using
// observer context. The query for paths-through-X must use the canonical
// resolved_path to decide membership, NOT a naive prefix lookup.
//
// Only B is in the canonical resolved_path; only paths-through-B
// must include the tx. paths-through-A and paths-through-C must exclude it.
func TestHandleNodePaths_PrefixCollision_1352(t *testing.T) {
	sc := setupCollisionScenario(t, false /* only B has GPS */)
	mustExec(t, sc.db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (42, 'DEAD', 'hash_1352', ?)`, sc.recent)
	mustExec(t, sc.db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
		VALUES (42, NULL, '["c0"]', ?, ?)`, sc.recentEpoch, `["`+sc.nodeBPK+`"]`)
	sc.reloadStore(t)

	respA := sc.query(t, sc.nodeAPK)
	respB := sc.query(t, sc.nodeBPK)
	respC := sc.query(t, sc.nodeCPK)

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
}

// TestHandleNodePaths_PrefixCollision_1352_FallbackBranch covers the
// worse case: obs has NO persisted resolved_path. The OLD fallback branch
// invoked pm.resolveWithContext(hop, []string{lowerPK}, graph) — anchoring
// the resolver on the queried node. Tier-2 (geo_proximity) then picked
// the GPS candidate closest to the centroid of context (== the target
// itself when the target has GPS), causing every paths-through-X query
// that shared the prefix to return the tx with X attribution.
//
// Fix: with multiple "c0" candidates and no SQL/index pre-confirmation,
// the colliders must sum to AT MOST 1 (ideally 0). Old buggy code:
// all three = 3. Fixed: ≤1, and we tighten further to ≤1 explicitly.
func TestHandleNodePaths_PrefixCollision_1352_FallbackBranch(t *testing.T) {
	sc := setupCollisionScenario(t, true /* all three have GPS */)
	mustExec(t, sc.db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (43, 'BEEF', 'hash_1352_fb', ?)`, sc.recent)
	mustExec(t, sc.db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
		VALUES (43, NULL, '["c0"]', ?, NULL)`, sc.recentEpoch)
	sc.reloadStore(t)

	a := sc.query(t, sc.nodeAPK).TotalTransmissions
	b := sc.query(t, sc.nodeBPK).TotalTransmissions
	c := sc.query(t, sc.nodeCPK).TotalTransmissions

	sum := a + b + c
	// Old buggy code: a==1 && b==1 && c==1 → sum==3 (wrong-node attribution
	// on all). Fixed: sum ∈ {0, 1}. Asserting sum ≤ 1 catches the degenerate
	// "all zero" implementation as legitimate (it IS legitimate — ambiguous
	// hops with no SQL confirmation must be excluded) while still rejecting
	// the bug. The positive case (sum==1 when unambiguous) is covered by
	// the canonical sub-test above and by FallbackUniquePrefix below.
	if sum > 1 {
		t.Errorf("ambiguous-prefix tx with NULL resolved_path attributed to %d nodes total (A=%d B=%d C=%d); "+
			"expected sum ≤ 1 — paths-through must not return the same tx for multiple sibling prefix collisions (#1352)",
			sum, a, b, c)
	}
}

// TestHandleNodePaths_FallbackUniquePrefix_1352 is the POSITIVE companion
// to FallbackBranch: a hop prefix that has EXACTLY ONE candidate node MUST
// attribute the tx when that hop resolves to the queried target.
//
// Without this test, the "all zero" degenerate implementation passes the
// ≤1 fallback assertion vacuously. This locks in that the
// `len(pm.m[lowerHop]) <= 1` guard does NOT over-reject unique prefixes.
//
// Setup: only ONE node has the prefix "ab". NULL resolved_path so we take
// the fallback branch. paths-through-target MUST include exactly 1 tx.
func TestHandleNodePaths_FallbackUniquePrefix_1352(t *testing.T) {
	db := setupTestDB(t)
	recent := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	recentEpoch := time.Now().Add(-1 * time.Hour).Unix()
	pk := "abcdef0123456789"

	mustExec(t, db, `INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, 'UniqueNode', 'repeater', 37.78, -122.4, ?, '2026-01-01', 1)`, pk, recent)
	mustExec(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (44, 'CAFE', 'hash_1352_unique', ?)`, recent)
	mustExec(t, db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
		VALUES (44, NULL, '["ab"]', ?, NULL)`, recentEpoch)

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

	req := httptest.NewRequest("GET", "/api/nodes/"+pk+"/paths", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /paths: code=%d body=%s", w.Code, w.Body.String())
	}
	var resp NodePathsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.TotalTransmissions != 1 {
		t.Errorf("unique-prefix hop with NULL resolved_path: target attribution "+
			"MUST be exactly 1, got %d — `len(pm.m[lowerHop]) <= 1` guard is "+
			"over-rejecting unambiguous prefixes (#1352)", resp.TotalTransmissions)
	}
}

// TestHandleNodePaths_FallbackPreconfirmed_1352 exercises the
// pre-confirmation path: when a tx is in confirmedByFullKey OR
// confirmedBySQL for the queried target, attribution MUST survive
// regardless of any sibling-prefix ambiguity.
//
// Mutation note (pushback recorded in PR body): in the current
// code shape, containsTarget is initialized to
// `confirmedByFullKey[tx.ID] || confirmedBySQL[tx.ID]` BEFORE the
// per-hop loop runs, and the loop only ever flips false→true. So
// removing the `preconfirmed ||` clause alone does not break this
// test — the preconfirmed tx is already attributed via the
// initialization. The `preconfirmed` snapshot is kept as a
// structural invariant (see routes.go comment): it documents the
// contract that the SQL/index signal must NEVER be silently
// overridden by a biased-resolver false-negative in a future edit
// that flips containsTarget back to false inside the loop. This
// test guards the BEHAVIOR ("preconfirmed survives ambiguous
// prefix") even if it can't currently mutation-detect every
// formulation of the structural guard.
func TestHandleNodePaths_FallbackPreconfirmed_1352(t *testing.T) {
	sc := setupCollisionScenario(t, true /* all three have GPS so resolver bias is maximal */)

	// tx 50: best obs has NULL resolved_path (fallback branch). A SECOND
	// obs persists resolved_path = [B] which populates the byPathHop index
	// for B's full pubkey AND lets confirmedBySQL hit via INSTR.
	mustExec(t, sc.db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (50, 'F00D', 'hash_1352_pre', ?)`, sc.recent)
	mustExec(t, sc.db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
		VALUES (50, NULL, '["c0"]', ?, NULL)`, sc.recentEpoch)
	// Second observation (different observer) — same tx, persisted resolved_path = [B].
	// This populates byPathHop[B] during Load(), so confirmedByFullKey is true
	// when paths-through-B is queried.
	mustExec(t, sc.db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
		VALUES (50, 1, '["c0"]', ?, ?)`, sc.recentEpoch+1, `["`+sc.nodeBPK+`"]`)
	sc.reloadStore(t)

	respA := sc.query(t, sc.nodeAPK)
	respB := sc.query(t, sc.nodeBPK)
	respC := sc.query(t, sc.nodeCPK)

	// B is preconfirmed by SQL/index → tx survives the collision guard.
	if respB.TotalTransmissions != 1 {
		t.Errorf("nodeB preconfirmed via byPathHop/SQL: tx MUST attribute despite "+
			"multi-candidate `c0` prefix — got %d, expected 1. The SQL/index "+
			"pre-confirmation path is the documented contract for #1352. "+
			"If this fails, either the byPathHop full-pubkey index is not being "+
			"populated from persisted resolved_path, or containsTarget is being "+
			"reset inside the per-hop loop.", respB.TotalTransmissions)
	}
	// A and C are NOT preconfirmed and the prefix IS ambiguous → excluded.
	if respA.TotalTransmissions != 0 {
		t.Errorf("nodeA not preconfirmed, prefix ambiguous: expected 0, got %d", respA.TotalTransmissions)
	}
	if respC.TotalTransmissions != 0 {
		t.Errorf("nodeC not preconfirmed, prefix ambiguous: expected 0, got %d", respC.TotalTransmissions)
	}
}

// TestHandleNodePaths_FallbackUnresolvableHop_1352 documents the
// behavior of the unresolvable-hop arm under multi-candidate prefix:
// when resolveHop returns nil (prefix not indexed by pm) AND the hop
// IS a prefix of the queried target, attribution must NOT happen
// without SQL/index pre-confirmation.
//
// Implementation reality (pushback recorded in PR body): the
// unresolvable arm is only reached when pm.m[lowerHop] is empty —
// resolveWithContext returns non-nil whenever len(candidates) >= 1.
// So in practice the arm's `len(pm.m[lowerHop]) <= 1` guard is
// always-true and structurally cannot be mutation-detected by a
// multi-candidate setup. This test instead asserts the BEHAVIOR
// (no attribution under an ambiguous + unresolvable scenario)
// and serves as a regression seat-belt for future edits to
// resolveWithContext that might start returning nil for len>=1.
func TestHandleNodePaths_FallbackUnresolvableHop_1352(t *testing.T) {
	sc := setupCollisionScenario(t, false /* only B has GPS */)

	mustExec(t, sc.db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (60, 'FEED', 'hash_1352_unres', ?)`, sc.recent)
	mustExec(t, sc.db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
		VALUES (60, NULL, '["c0"]', ?, NULL)`, sc.recentEpoch)
	sc.reloadStore(t)

	// Query A (no GPS): biased resolver in the fallback branch picks B via
	// tier-3 GPS preference; B's pubkey != A's lowerPK so the resolvable
	// arm's pubkey-match condition fails. Either way: NOT attributed to A.
	respA := sc.query(t, sc.nodeAPK)
	if respA.TotalTransmissions != 0 {
		t.Errorf("nodeA (no GPS) with multi-candidate `c0` prefix + NULL resolved_path: "+
			"expected 0 attribution, got %d (#1352)", respA.TotalTransmissions)
	}
	respC := sc.query(t, sc.nodeCPK)
	if respC.TotalTransmissions != 0 {
		t.Errorf("nodeC (no GPS) with multi-candidate `c0` prefix + NULL resolved_path: "+
			"expected 0 attribution, got %d (#1352)", respC.TotalTransmissions)
	}
}
