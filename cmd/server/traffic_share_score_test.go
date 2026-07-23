package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// TestTrafficShareScore_HandleNodesSurface pins issue #1456 as amended by
// #672: the /api/nodes response carries `traffic_share_score` (the
// canonical Traffic-axis field) alongside `usefulness_score`. Since the
// #672 composite shipped, usefulness_score is the weighted 4-axis composite
// (no longer a mirror of traffic_share_score). Beyond presence/bounds this
// asserts a POSITIVE behavioral contract: a node that is a structural cut
// vertex but relays NO traffic (traffic_share_score == 0) must still earn a
// non-zero composite from its structural axes, and the composite must be
// >= the traffic axis — proving the composite isn't just the traffic share.
func TestTrafficShareScore_HandleNodesSurface(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()
	if _, err := db.conn.Exec(`ALTER TABLE nodes ADD COLUMN foreign_advert INTEGER DEFAULT 0`); err != nil {
		t.Fatal(err)
	}

	// Three repeaters on a line L-pk-R so the middle node `pk` is a cut
	// vertex: bridge/coverage/redundancy all > 0 while it relays no traffic.
	pk := "aaaa000000000000000000000000000000000000000000000000000000000000"
	left := "bbbb000000000000000000000000000000000000000000000000000000000000"
	right := "cccc000000000000000000000000000000000000000000000000000000000000"
	recent := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	for _, p := range []string{pk, left, right} {
		if _, err := db.conn.Exec(`INSERT INTO nodes
			(public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
			VALUES (?, 'rpt', 'repeater', 37.5, -122.0, ?, ?, 10)`,
			p, recent, recent); err != nil {
			t.Fatal(err)
		}
	}

	store := NewPacketStore(db, nil)
	// Wire a neighbor graph (left-pk-right) and compute the structural axes
	// so pk carries non-zero bridge/coverage/redundancy in the response.
	g := NewNeighborGraph()
	now := time.Now()
	snr := 5.0
	for i := 0; i < 10; i++ {
		g.upsertEdge(left, pk, "lp", "obs-test", &snr, now)
		g.upsertEdge(pk, right, "pr", "obs-test", &snr, now)
	}
	store.graph.Store(g)
	store.recomputeUsefulnessAxes()

	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	srv.store = store

	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/nodes?limit=10", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("/api/nodes status: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Nodes []map[string]interface{} `json:"nodes"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	var got map[string]interface{}
	for _, n := range resp.Nodes {
		if k, _ := n["public_key"].(string); k == pk {
			got = n
			break
		}
	}
	if got == nil {
		t.Fatalf("repeater node missing from /api/nodes response")
	}
	useful, hasU := got["usefulness_score"]
	share, hasS := got["traffic_share_score"]
	if !hasU {
		t.Fatalf("usefulness_score absent (must remain for API compat)")
	}
	if !hasS {
		t.Fatalf("traffic_share_score absent (new field per #1456)")
	}
	uf, _ := useful.(float64)
	sf, _ := share.(float64)
	if uf < 0 || uf > 1 {
		t.Errorf("usefulness_score (composite) out of [0,1]: %v", uf)
	}
	if sf < 0 || sf > 1 {
		t.Errorf("traffic_share_score out of [0,1]: %v", sf)
	}
	// Positive contract: pk relays nothing yet is a structural cut vertex.
	if sf != 0 {
		t.Errorf("traffic_share_score should be 0 (no relayed traffic), got %v", sf)
	}
	if uf <= 0 {
		t.Errorf("composite must be > 0 from structural axes despite zero traffic, got %v", uf)
	}
	// The composite reflects more than the traffic axis: it must be >= it.
	if uf < sf {
		t.Errorf("composite usefulness_score (%v) must be >= traffic_share_score (%v)", uf, sf)
	}
}

// TestTrafficShareScore_NodeDetail pins the same dual-field shape on
// the per-node detail endpoint /api/nodes/{pubkey}.
func TestTrafficShareScore_NodeDetail(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()
	if _, err := db.conn.Exec(`ALTER TABLE nodes ADD COLUMN foreign_advert INTEGER DEFAULT 0`); err != nil {
		t.Fatal(err)
	}

	pk := "bbbb000000000000000000000000000000000000000000000000000000000000"
	recent := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	if _, err := db.conn.Exec(`INSERT INTO nodes
		(public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, 'rpt', 'repeater', 37.5, -122.0, ?, ?, 10)`,
		pk, recent, recent); err != nil {
		t.Fatal(err)
	}

	store := NewPacketStore(db, nil)
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	srv.store = store

	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/nodes/"+pk, nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("/api/nodes/{pk} status: want 200, got %d body=%s", rr.Code, rr.Body.String())
	}

	var resp struct {
		Node map[string]interface{} `json:"node"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	if resp.Node == nil {
		t.Fatalf("node missing in response: %s", rr.Body.String())
	}
	if _, ok := resp.Node["usefulness_score"]; !ok {
		t.Errorf("usefulness_score absent on node detail (must remain for API compat)")
	}
	if _, ok := resp.Node["traffic_share_score"]; !ok {
		t.Errorf("traffic_share_score absent on node detail (new field per #1456)")
	}
	uf, _ := resp.Node["usefulness_score"].(float64)
	sf, _ := resp.Node["traffic_share_score"].(float64)
	// #672: usefulness_score is the composite (not a mirror of Traffic);
	// both must be valid scores in [0,1].
	if uf < 0 || uf > 1 {
		t.Errorf("usefulness_score (composite) out of [0,1]: %v", uf)
	}
	if sf < 0 || sf > 1 {
		t.Errorf("traffic_share_score out of [0,1]: %v", sf)
	}
}
