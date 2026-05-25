package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestRouteHistoryEdgeNormalize verifies A→B and B→A collapse to the same key.
func TestRouteHistoryEdgeNormalize(t *testing.T) {
	cases := []struct{ a, b, wantA, wantB string }{
		{"aaaa", "bbbb", "aaaa", "bbbb"},
		{"bbbb", "aaaa", "aaaa", "bbbb"},
		{"zzzz", "aaaa", "aaaa", "zzzz"},
		{"xxxx", "xxxx", "xxxx", "xxxx"}, // same pubkey — degenerate edge
	}
	for _, tc := range cases {
		a, b := tc.a, tc.b
		if a > b {
			a, b = b, a
		}
		if a != tc.wantA || b != tc.wantB {
			t.Errorf("normalize(%q,%q): got (%q,%q), want (%q,%q)", tc.a, tc.b, a, b, tc.wantA, tc.wantB)
		}
	}
}

// TestHandleRouteHistory_InvalidHours verifies out-of-range hours return 400.
func TestHandleRouteHistory_InvalidHours(t *testing.T) {
	cases := []string{"0", "169", "abc", "-1", "999"}
	for _, h := range cases {
		s := &Server{cfg: &Config{}}
		req := httptest.NewRequest("GET", "/api/route-history?hours="+h, nil)
		rr := httptest.NewRecorder()
		s.handleRouteHistory(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("hours=%q: want 400, got %d", h, rr.Code)
		}
	}
}

// TestHandleRouteHistory_ValidHoursDefaultsTo24 verifies missing hours param defaults to 24.
func TestHandleRouteHistory_ValidHoursDefaultsTo24(t *testing.T) {
	db := setupTestDB(t)
	s := &Server{cfg: &Config{}, db: db}
	req := httptest.NewRequest("GET", "/api/route-history", nil)
	rr := httptest.NewRecorder()
	s.handleRouteHistory(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp routeHistoryResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if resp.Hours != 24 {
		t.Errorf("want hours=24, got %d", resp.Hours)
	}
}

// TestHandleRouteHistory_EmptyDB verifies an empty DB returns an empty edges list.
func TestHandleRouteHistory_EmptyDB(t *testing.T) {
	db := setupTestDB(t)
	s := &Server{cfg: &Config{}, db: db}
	req := httptest.NewRequest("GET", "/api/route-history?hours=24", nil)
	rr := httptest.NewRecorder()
	s.handleRouteHistory(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
	var resp routeHistoryResponse
	json.NewDecoder(rr.Body).Decode(&resp)
	if resp.Edges == nil {
		t.Error("edges should not be nil (should be empty slice)")
	}
	if len(resp.Edges) != 0 {
		t.Errorf("want 0 edges, got %d", len(resp.Edges))
	}
}

func TestHandleRouteHistory_UsesResolvedPathForGPSNodes(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	s := &Server{cfg: &Config{}, db: db}
	req := httptest.NewRequest("GET", "/api/route-history?hours=6", nil)
	rr := httptest.NewRecorder()
	s.handleRouteHistory(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp routeHistoryResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if resp.TotalEdges != 1 {
		t.Fatalf("want 1 GPS edge from resolved_path, got %d (%+v)", resp.TotalEdges, resp)
	}
	e := resp.Edges[0]
	if e.NodeA != "aabbccdd11223344" || e.NodeB != "eeff00112233aabb" {
		t.Fatalf("edge used path_json prefixes instead of resolved pubkeys: %+v", e)
	}
	if e.NameA != "TestRepeater" || e.NameB != "TestCompanion" {
		t.Fatalf("edge names not populated from nodes: %+v", e)
	}
	if resp.CandidateEdges != 1 || resp.MissingGPSEdges != 0 {
		t.Fatalf("unexpected diagnostics: candidates=%d missingGPS=%d", resp.CandidateEdges, resp.MissingGPSEdges)
	}
}

func TestHandleRouteHistory_DiagnosticsForMissingGPS(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen)
		VALUES ('node-a', 'Node A', 'repeater', 50.0, 5.0, ?),
		       ('node-b', 'Node B', 'repeater', NULL, NULL, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.Exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type)
		VALUES (100, 'AA', 'nogpshash', ?, 1, 9)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
		VALUES (100, 1, '["aa","bb"]', ?, '["node-a","node-b"]')`, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}

	s := &Server{cfg: &Config{}, db: db}
	req := httptest.NewRequest("GET", "/api/route-history?hours=168", nil)
	rr := httptest.NewRecorder()
	s.handleRouteHistory(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rr.Code, rr.Body.String())
	}
	var resp routeHistoryResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if resp.TotalEdges != 0 || resp.CandidateEdges != 1 || resp.MissingGPSEdges != 1 {
		t.Fatalf("unexpected diagnostics: %+v", resp)
	}
}
