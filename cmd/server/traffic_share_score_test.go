package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// TestTrafficShareScore_HandleNodesSurface pins issue #1456: the
// /api/nodes response carries a new `traffic_share_score` field
// alongside the legacy `usefulness_score`, with the same numeric
// value. The legacy field is kept for API backwards-compat (existing
// consumers + stale frontends); the new field is the canonical name
// for the Traffic-axis score.
func TestTrafficShareScore_HandleNodesSurface(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()
	if _, err := db.conn.Exec(`ALTER TABLE nodes ADD COLUMN foreign_advert INTEGER DEFAULT 0`); err != nil {
		t.Fatal(err)
	}

	pk := "aaaa000000000000000000000000000000000000000000000000000000000000"
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
		t.Errorf("usefulness_score absent (must remain for API compat)")
	}
	if !hasS {
		t.Errorf("traffic_share_score absent (new field per #1456)")
	}
	if hasU && hasS {
		uf, _ := useful.(float64)
		sf, _ := share.(float64)
		if uf != sf {
			t.Errorf("traffic_share_score (%v) must equal usefulness_score (%v)", sf, uf)
		}
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
	if uf != sf {
		t.Errorf("traffic_share_score (%v) must equal usefulness_score (%v)", sf, uf)
	}
}
