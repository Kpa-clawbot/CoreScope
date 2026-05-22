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

// TestHandleNodePaths_AnchorBiasInconsistency_Issue1278 reproduces #1278:
// /api/nodes/{pk}/paths returns a tx whose CANONICAL persisted resolved_path
// (the one the packets page reads via fetchResolvedPathForTxBest) does NOT
// contain the queried pubkey.
//
// Two nodes share the 1-byte prefix "c0":
//   - nodeNoGPS ("c0dedad…") — no GPS  (staging: Kpa Roof Solar)
//   - nodeGPS   ("c0ffeec…") — has GPS (staging: West SoMa Repeater)
//
// A transmission has TWO observations of the same raw path ["c0"]:
//   - obs1 (short path_json): persisted resolved_path = [nodeNoGPSPK]
//     (e.g. a region where context picked nodeNoGPS at ingest time)
//   - obs2 (longer path_json): persisted resolved_path = [nodeGPSPK]
//     (best-obs picks this one — it's the canonical answer the packets page shows)
//
// The membership index has BOTH pubkeys → /api/nodes/{nodeNoGPS}/paths
// passes the candidacy gate (obs1's resolved_path mentions nodeNoGPS), then
// re-resolves with the anchor-biased context and reports the tx — even
// though the CANONICAL ("best") resolved_path picked nodeGPS.
//
// Acceptance: /api/nodes/{nodeNoGPS}/paths MUST exclude this tx because
// the best-obs canonical resolved_path doesn't contain it. Conversely
// /api/nodes/{nodeGPS}/paths MUST include it.
func TestHandleNodePaths_AnchorBiasInconsistency_Issue1278(t *testing.T) {
	db := setupTestDB(t)
	recent := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	recentEpoch := time.Now().Add(-1 * time.Hour).Unix()

	nodeNoGPSPK := "c0dedad4208acb6cbe44b848943fc6d3c5d43cf38a21e48b43826a70862980e4"
	nodeGPSPK := "c0ffeec700000000000000000000000000000000000000000000000000000001"

	if _, err := db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, 'NodeNoGPS', 'repeater', 0, 0, ?, '2026-01-01', 1)`, nodeNoGPSPK, recent); err != nil {
		t.Fatalf("insert nodeNoGPS: %v", err)
	}
	if _, err := db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, 'NodeGPS', 'repeater', 37.5, -122.0, ?, '2026-01-01', 1)`, nodeGPSPK, recent); err != nil {
		t.Fatalf("insert nodeGPS: %v", err)
	}

	if _, err := db.conn.Exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen)
		VALUES (100, 'AA', 'hash_collision', ?)`, recent); err != nil {
		t.Fatalf("insert tx: %v", err)
	}
	// obs1: SHORTER path_json (single hop), resolved → nodeNoGPS.
	// (Without this row, the membership index wouldn't list nodeNoGPS at all
	// and the tx would be cleanly excluded — the bug needs the index hit.)
	if _, err := db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
		VALUES (100, NULL, '["c0"]', ?, ?)`, recentEpoch, `["`+nodeNoGPSPK+`"]`); err != nil {
		t.Fatalf("insert obs1: %v", err)
	}
	// obs2: LONGER path_json (two hops, first hop is what packets page shows
	// as resolved_path[0]). fetchResolvedPathForTxBest picks this obs as
	// canonical because it has the longer path_json. Its resolved_path
	// picks nodeGPS for "c0", NOT nodeNoGPS.
	if _, err := db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
		VALUES (100, NULL, '["c0","ee"]', ?, ?)`, recentEpoch, `["`+nodeGPSPK+`","ee00000000000000000000000000000000000000000000000000000000000000"]`); err != nil {
		t.Fatalf("insert obs2: %v", err)
	}

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

	doGet := func(pk string) NodePathsResponse {
		t.Helper()
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

	// Acceptance #1: nodeGPS (the canonical / best-obs pick) MUST include tx.
	respGPS := doGet(nodeGPSPK)
	if respGPS.TotalTransmissions != 1 {
		t.Errorf("nodeGPS /paths: expected 1 transmission (canonical owner), got %d", respGPS.TotalTransmissions)
	}

	// Acceptance #2: nodeNoGPS MUST NOT include the tx — its CANONICAL
	// (best-obs) resolved_path picked nodeGPS, so the packets page would
	// show nodeGPS. Consistency requires the same here.
	respNoGPS := doGet(nodeNoGPSPK)
	if respNoGPS.TotalTransmissions != 0 {
		var hashes []string
		for _, p := range respNoGPS.Paths {
			hashes = append(hashes, p.SampleHash)
		}
		t.Errorf("nodeNoGPS /paths: expected 0 transmissions (canonical/best-obs resolved_path picked NodeGPS, not NodeNoGPS) — anchor-bias inconsistency, got %d; sample hashes: %s",
			respNoGPS.TotalTransmissions, strings.Join(hashes, ","))
	}
}
