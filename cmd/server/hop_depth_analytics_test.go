package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// TestGetHopDepthAnalytics covers the two questions HopDepthAnalyticsResponse
// answers: (1) does scoped traffic actually travel fewer hops than unscoped
// traffic network-wide, and (2) which repeaters relay unscoped flood traffic
// that has already traveled far (high hops) vs merely locally (low hops).
func TestGetHopDepthAnalytics(t *testing.T) {
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	db := &DB{conn: conn}

	if _, err := conn.Exec(`CREATE TABLE nodes (public_key TEXT PRIMARY KEY, name TEXT, role TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`CREATE TABLE transmissions (
		id INTEGER PRIMARY KEY, hash TEXT, first_seen TEXT, route_type INTEGER, payload_type INTEGER
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`CREATE TABLE observations (
		id INTEGER PRIMARY KEY, transmission_id INTEGER, resolved_path TEXT
	)`); err != nil {
		t.Fatal(err)
	}

	recent := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)

	repeaterA := "aa001111aaaabbbb"
	repeaterB := "bb001111bbbbcccc"
	origin := "cc001111ccccdddd"

	conn.Exec(`INSERT INTO nodes (public_key, name, role) VALUES (?, 'RepeaterA', 'repeater')`, repeaterA)
	conn.Exec(`INSERT INTO nodes (public_key, name, role) VALUES (?, 'RepeaterB', 'repeater')`, repeaterB)
	conn.Exec(`INSERT INTO nodes (public_key, name, role) VALUES (?, 'CompanionC', 'companion')`, origin)

	// tx1: TRANSPORT_FLOOD (scoped), path len 1 -> scoped bucket gets one
	// hop=0 entry. Not unscoped, so doesn't feed unscopedByRepeater.
	conn.Exec(`INSERT INTO transmissions (id, hash, first_seen, route_type, payload_type) VALUES (1, 'h1', ?, 0, 5)`, recent)
	conn.Exec(`INSERT INTO observations (id, transmission_id, resolved_path) VALUES (1, 1, ?)`, `["`+repeaterA+`"]`)

	// tx2: plain FLOOD (unscoped), not advert, path len 2 -> unscoped
	// bucket gets hop=0 (repeaterA) and hop=1 (repeaterB). Both feed
	// unscopedByRepeater: repeaterA at hop 0, repeaterB at hop 1.
	conn.Exec(`INSERT INTO transmissions (id, hash, first_seen, route_type, payload_type) VALUES (2, 'h2', ?, 1, 5)`, recent)
	conn.Exec(`INSERT INTO observations (id, transmission_id, resolved_path) VALUES (2, 2, ?)`, `["`+repeaterA+`","`+repeaterB+`"]`)

	// tx3: another unscoped FLOOD, path len 3 -> repeaterB sees hop=2 this
	// time, giving it hops [1, 2] (median matters for the assertion below).
	conn.Exec(`INSERT INTO transmissions (id, hash, first_seen, route_type, payload_type) VALUES (3, 'h3', ?, 1, 5)`, recent)
	conn.Exec(`INSERT INTO observations (id, transmission_id, resolved_path) VALUES (3, 3, ?)`, `["`+repeaterA+`","`+origin+`","`+repeaterB+`"]`)

	// tx4: unscoped FLOOD ADVERT -- must be EXCLUDED from unscopedByRepeater
	// (adverts have their own separate flood.max.advert cap) but still
	// counts toward the network-wide unscoped hop-depth bucket.
	conn.Exec(`INSERT INTO transmissions (id, hash, first_seen, route_type, payload_type) VALUES (4, 'h4', ?, 1, 4)`, recent)
	conn.Exec(`INSERT INTO observations (id, transmission_id, resolved_path) VALUES (4, 4, ?)`, `["`+repeaterA+`"]`)

	// tx5: DIRECT -- excluded from both scoped and unscoped flood buckets
	// entirely (not a flood.max-relevant transport).
	conn.Exec(`INSERT INTO transmissions (id, hash, first_seen, route_type, payload_type) VALUES (5, 'h5', ?, 2, 5)`, recent)
	conn.Exec(`INSERT INTO observations (id, transmission_id, resolved_path) VALUES (5, 5, ?)`, `["`+repeaterA+`"]`)

	// tx6: companion (not repeater/room) relaying unscoped flood -- must
	// NOT appear in unscopedByRepeater despite matching the transport filter.
	conn.Exec(`INSERT INTO transmissions (id, hash, first_seen, route_type, payload_type) VALUES (6, 'h6', ?, 1, 5)`, recent)
	conn.Exec(`INSERT INTO observations (id, transmission_id, resolved_path) VALUES (6, 6, ?)`, `["`+origin+`"]`)

	resp, err := db.GetHopDepthAnalytics("24h")
	if err != nil {
		t.Fatalf("GetHopDepthAnalytics: %v", err)
	}

	if resp.Window != "24h" {
		t.Errorf("Window = %q, want 24h", resp.Window)
	}

	// Scoped bucket: only tx1 contributes -- one hop=0 entry.
	scopedByHop := map[int]int{}
	for _, b := range resp.ScopedHopDepth {
		scopedByHop[b.Hops] = b.Count
	}
	if scopedByHop[0] != 1 || len(resp.ScopedHopDepth) != 1 {
		t.Errorf("ScopedHopDepth = %+v, want just {hops:0 count:1}", resp.ScopedHopDepth)
	}

	// Unscoped bucket: tx2 (hop0,hop1) + tx3 (hop0,hop1,hop2) + tx4 (hop0)
	// + tx6 (hop0) -> hop0: 4 (tx2,tx3,tx4,tx6), hop1: 2 (tx2,tx3), hop2: 1 (tx3).
	unscopedByHop := map[int]int{}
	for _, b := range resp.UnscopedHopDepth {
		unscopedByHop[b.Hops] = b.Count
	}
	if unscopedByHop[0] != 4 {
		t.Errorf("UnscopedHopDepth[hop=0] = %d, want 4", unscopedByHop[0])
	}
	if unscopedByHop[1] != 2 {
		t.Errorf("UnscopedHopDepth[hop=1] = %d, want 2", unscopedByHop[1])
	}
	if unscopedByHop[2] != 1 {
		t.Errorf("UnscopedHopDepth[hop=2] = %d, want 1", unscopedByHop[2])
	}

	// unscopedByRepeater: only repeaterA and repeaterB should appear
	// (origin is a companion, tx4's advert excluded, tx1/tx5 not unscoped-flood).
	byPK := map[string]RepeaterUnscopedHopDepth{}
	for _, r := range resp.UnscopedByRepeater {
		byPK[r.PublicKey] = r
	}
	if len(byPK) != 2 {
		t.Fatalf("UnscopedByRepeater = %+v, want exactly repeaterA and repeaterB", resp.UnscopedByRepeater)
	}
	// repeaterA: hop=0 from tx2, hop=0 from tx3 -> [0,0], median 0.
	a := byPK[repeaterA]
	if a.Count != 2 || a.MinHops != 0 || a.MaxHops != 0 || a.MedianHops != 0 {
		t.Errorf("repeaterA = %+v, want count=2 min=0 max=0 median=0", a)
	}
	// repeaterB: hop=1 from tx2, hop=2 from tx3 -> [1,2], median 1.5.
	b := byPK[repeaterB]
	if b.Count != 2 || b.MinHops != 1 || b.MaxHops != 2 || b.MedianHops != 1.5 {
		t.Errorf("repeaterB = %+v, want count=2 min=1 max=2 median=1.5", b)
	}
	if _, ok := byPK[origin]; ok {
		t.Error("origin (companion role) must not appear in UnscopedByRepeater")
	}
}

func TestGetHopDepthAnalytics_EmptyWindow(t *testing.T) {
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	db := &DB{conn: conn}
	conn.Exec(`CREATE TABLE nodes (public_key TEXT PRIMARY KEY, name TEXT, role TEXT)`)
	conn.Exec(`CREATE TABLE transmissions (id INTEGER PRIMARY KEY, hash TEXT, first_seen TEXT, route_type INTEGER, payload_type INTEGER)`)
	conn.Exec(`CREATE TABLE observations (id INTEGER PRIMARY KEY, transmission_id INTEGER, resolved_path TEXT)`)

	resp, err := db.GetHopDepthAnalytics("1h")
	if err != nil {
		t.Fatalf("GetHopDepthAnalytics: %v", err)
	}
	if len(resp.ScopedHopDepth) != 0 || len(resp.UnscopedHopDepth) != 0 || len(resp.UnscopedByRepeater) != 0 {
		t.Errorf("expected all-empty response on an empty DB, got %+v", resp)
	}
}

// TestHandleHopDepthAnalytics_InvalidWindow mirrors handleScopeStats'
// window validation.
func TestHandleHopDepthAnalytics_InvalidWindow(t *testing.T) {
	db := setupTestDB(t)
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/analytics/hop-depth?window=30d", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleHopDepthAnalytics_DefaultWindow(t *testing.T) {
	db := setupTestDB(t)
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/analytics/hop-depth", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp HopDepthAnalyticsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Window != "24h" {
		t.Errorf("Window = %q, want default 24h", resp.Window)
	}
}
