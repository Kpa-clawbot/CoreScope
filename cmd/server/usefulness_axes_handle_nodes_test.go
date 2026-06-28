package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// TestUsefulnessAxes_HandleNodesSurface drives the line graph A-B-C-D
// through the full pipeline and verifies /api/nodes surfaces the #672
// axes 3 & 4 (coverage_score, redundancy_score) plus the composite
// (usefulness_score) and letter grade (usefulness_grade) on repeater rows.
// Mirrors TestBridgeScore_HandleNodesSurface.
func TestUsefulnessAxes_HandleNodesSurface(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()
	if _, err := db.conn.Exec(`ALTER TABLE nodes ADD COLUMN foreign_advert INTEGER DEFAULT 0`); err != nil {
		t.Fatal(err)
	}

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
	// A plain client node in the SAME response. enrichNodeUsefulness runs only
	// inside the repeater/room branch (#672), so this row must NOT carry any
	// usefulness fields — the assertion below guards against leakage if that
	// conditional is ever moved or widened (#1762 MAJOR-5).
	clientPK := "eeee000000000000000000000000000000000000000000000000000000000000"
	if _, err := db.conn.Exec(`INSERT INTO nodes
		(public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, 'client-eeee', 'client', 37.5, -122.0, ?, ?, 10)`,
		clientPK, recent, recent); err != nil {
		t.Fatal(err)
	}

	store := NewPacketStore(db, nil)
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

	// Call recomputeUsefulnessAxes directly rather than via
	// StartUsefulnessAxesRecomputer: Start latches this store's
	// usefulnessAxesRecompStarted flag (and spawns a ticker goroutine), so a
	// repeated Start on the same store would turn into a silent no-op. The
	// direct call deterministically populates the snapshots for this test
	// without spawning a background goroutine or arming that latch.
	store.recomputeUsefulnessAxes()

	cov := store.GetCoverageScoreMap()
	red := store.GetRedundancyScoreMap()
	if len(cov) == 0 || len(red) == 0 {
		t.Fatalf("expected non-empty coverage/redundancy snapshots, got cov=%d red=%d", len(cov), len(red))
	}
	// Middle nodes are cut vertices (redundancy > 0); leaves are not.
	if red[pks[1]] <= 0 || red[pks[2]] <= 0 {
		t.Errorf("middle redundancy should be > 0: b=%v c=%v", red[pks[1]], red[pks[2]])
	}
	if red[pks[0]] != 0 || red[pks[3]] != 0 {
		t.Errorf("leaf redundancy should be 0: a=%v d=%v", red[pks[0]], red[pks[3]])
	}
	// Every connected node has positive coverage (it reaches someone).
	if cov[pks[0]] <= 0 || cov[pks[1]] <= 0 {
		t.Errorf("coverage should be > 0 for connected nodes: a=%v b=%v", cov[pks[0]], cov[pks[1]])
	}

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
		for _, field := range []string{"coverage_score", "redundancy_score", "usefulness_score", "usefulness_grade"} {
			if _, has := n[field]; !has {
				t.Errorf("node %s: %s field absent from response", pk[:4], field)
			}
		}
	}
	// "Field present but always zero" regression guards.
	if v, _ := gotBy[pks[1]]["coverage_score"].(float64); v <= 0 {
		t.Errorf("middle B coverage_score should be > 0, got %v", v)
	}
	if v, _ := gotBy[pks[1]]["redundancy_score"].(float64); v <= 0 {
		t.Errorf("middle B redundancy_score should be > 0, got %v", v)
	}
	if v, _ := gotBy[pks[0]]["redundancy_score"].(float64); v != 0 {
		t.Errorf("leaf A redundancy_score should be 0, got %v", v)
	}
	if v, _ := gotBy[pks[1]]["usefulness_score"].(float64); v <= 0 {
		t.Errorf("middle B usefulness_score (composite) should be > 0, got %v", v)
	}
	// Contract: usefulness_score is the COMPOSITE, not a mirror of the
	// Traffic axis. B relays nothing in this fixture (traffic_share_score 0)
	// yet scores > 0 on the structural axes, so the two must diverge.
	if ts, _ := gotBy[pks[1]]["traffic_share_score"].(float64); ts != 0 {
		t.Errorf("middle B traffic_share_score should be 0 (no relayed traffic), got %v", ts)
	}
	if us, _ := gotBy[pks[1]]["usefulness_score"].(float64); us == 0 {
		t.Error("middle B composite must differ from its zero traffic_share_score")
	}
	// Grade is a valid A–F letter.
	if g, _ := gotBy[pks[1]]["usefulness_grade"].(string); g == "" || len(g) != 1 || g[0] < 'A' || g[0] > 'F' {
		t.Errorf("middle B usefulness_grade should be a single A–F letter, got %q", g)
	}

	// Non-repeater contract (#1762 MAJOR-5): the client row must NOT carry any
	// usefulness field — enrichNodeUsefulness runs only in the repeater/room
	// branch. This guards against leakage if that conditional ever moves.
	client, ok := gotBy[clientPK]
	if !ok {
		t.Fatalf("client node %s missing from response", clientPK[:4])
	}
	for _, field := range []string{"coverage_score", "redundancy_score", "bridge_score", "traffic_share_score", "usefulness_score", "usefulness_grade"} {
		if _, has := client[field]; has {
			t.Errorf("non-repeater node must not carry %q, got %v", field, client[field])
		}
	}
}
