package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// TestFirstSeen_1166_HandleNodesSurface pins issue #1166: the /api/nodes
// response carries a `first_seen` ISO timestamp per node so the frontend
// can show a sortable "First Seen" column.
func TestFirstSeen_1166_HandleNodesSurface(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()
	if _, err := db.conn.Exec(`ALTER TABLE nodes ADD COLUMN foreign_advert INTEGER DEFAULT 0`); err != nil {
		t.Fatal(err)
	}

	pk := "cccc000000000000000000000000000000000000000000000000000000000000"
	first := time.Now().Add(-72 * time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	last := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	if _, err := db.conn.Exec(`INSERT INTO nodes
		(public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, 'rpt', 'repeater', 37.5, -122.0, ?, ?, 5)`,
		pk, last, first); err != nil {
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
		t.Fatalf("node missing from /api/nodes response")
	}
	fs, hasFS := got["first_seen"]
	if !hasFS {
		t.Fatalf("first_seen absent from /api/nodes response (issue #1166)")
	}
	s, _ := fs.(string)
	if s == "" {
		t.Errorf("first_seen empty, want ISO timestamp, got %v", fs)
	}
	if s != first {
		t.Errorf("first_seen = %q, want %q", s, first)
	}
}
