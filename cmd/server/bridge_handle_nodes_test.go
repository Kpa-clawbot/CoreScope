package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// TestBridgeScore_HandleNodesSurface verifies that /api/nodes
// includes a `bridge_score` field on repeater rows after the bridge
// recomputer has run. Drives the line-graph A-B-C-D through the full
// pipeline: insert nodes, populate the neighbor graph, force a
// recompute, hit the handler, parse the response. Issue #672 axis 2.
func TestBridgeScore_HandleNodesSurface(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()
	// handleNodes/db.GetNodes selects a foreign_advert column not in
	// the minimal capability-test schema.
	if _, err := db.conn.Exec(`ALTER TABLE nodes ADD COLUMN foreign_advert INTEGER DEFAULT 0`); err != nil {
		t.Fatal(err)
	}

	// Four repeater nodes in a line.
	pks := []string{
		"aaaa000000000000000000000000000000000000000000000000000000000000",
		"bbbb000000000000000000000000000000000000000000000000000000000000",
		"cccc000000000000000000000000000000000000000000000000000000000000",
		"dddd000000000000000000000000000000000000000000000000000000000000",
	}
	recent := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	for _, pk := range pks {
		if _, err := db.conn.Exec(`INSERT INTO nodes
			(public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
			VALUES (?, ?, 'repeater', 37.5, -122.0, ?, ?, 10)`,
			pk, "node-"+pk[:4], recent, recent); err != nil {
			t.Fatal(err)
		}
	}

	store := NewPacketStore(db, nil)
	// Build neighbor graph with the line A-B-C-D. Add each edge
	// `count` times so its time-decayed Score saturates.
	g := NewNeighborGraph()
	now := time.Now()
	obs := "obs-test"
	snr := 5.0
	for i := 0; i < 10; i++ {
		g.upsertEdge(pks[0], pks[1], "aa", obs, &snr, now)
		g.upsertEdge(pks[1], pks[2], "bb", obs, &snr, now)
		g.upsertEdge(pks[2], pks[3], "cc", obs, &snr, now)
	}
	store.graph.Store(g)

	// Direct invocation of the recomputer's compute path — bypassing
	// StartBridgeScoreRecomputer's package-level once-flag (which is
	// problematic across tests).
	recomputeBridgeScoresSafe(store)

	snap := store.GetBridgeScoreMap()
	if len(snap) == 0 {
		t.Fatalf("expected non-empty bridge score snapshot, got empty")
	}
	// Sanity: middle nodes b/c must be positive, ends must be zero.
	if snap[pks[1]] <= 0 || snap[pks[2]] <= 0 {
		t.Errorf("middle nodes should have positive bridge: b=%v c=%v",
			snap[pks[1]], snap[pks[2]])
	}
	if snap[pks[0]] != 0 || snap[pks[3]] != 0 {
		t.Errorf("end nodes should have zero bridge: a=%v d=%v",
			snap[pks[0]], snap[pks[3]])
	}

	// Wire a Server, call handleNodes, parse the response.
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	srv.store = store

	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/nodes?limit=100", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("handleNodes status: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Nodes []map[string]interface{} `json:"nodes"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	gotBy := map[string]map[string]interface{}{}
	for _, n := range resp.Nodes {
		if pk, _ := n["public_key"].(string); pk != "" {
			gotBy[pk] = n
		}
	}
	for _, pk := range pks {
		n, ok := gotBy[pk]
		if !ok {
			t.Errorf("node %s missing from response", pk[:4])
			continue
		}
		if _, has := n["bridge_score"]; !has {
			t.Errorf("node %s: bridge_score field absent from response", pk[:4])
		}
	}
	// Middle node B must report a non-zero bridge_score; end node A
	// must report exactly zero. These two assertions together prevent
	// a "field present but always 0" regression.
	if v, _ := gotBy[pks[1]]["bridge_score"].(float64); v <= 0 {
		t.Errorf("middle node B bridge_score in API response should be > 0, got %v", v)
	}
	if v, _ := gotBy[pks[0]]["bridge_score"].(float64); v != 0 {
		t.Errorf("end node A bridge_score in API response should be 0, got %v", v)
	}
}
