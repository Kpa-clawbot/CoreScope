package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func mustExecRouteHistoryDB(t *testing.T, db *DB, q string, args ...interface{}) {
	t.Helper()
	if _, err := db.conn.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func TestRouteHistoryEdgeNormalize(t *testing.T) {
	cases := []struct{ a, b, wantA, wantB string }{
		{"aaaa", "bbbb", "aaaa", "bbbb"},
		{"bbbb", "aaaa", "aaaa", "bbbb"},
		{"zzzz", "aaaa", "aaaa", "zzzz"},
		{"xxxx", "xxxx", "xxxx", "xxxx"},
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
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
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
	if resp.TotalEdges != 1 || resp.MappedEdges != 1 {
		t.Fatalf("want 1 GPS edge from resolved_path, got total=%d mapped=%d (%+v)", resp.TotalEdges, resp.MappedEdges, resp)
	}
	e := resp.Edges[0]
	if e.NodeA != "aabbccdd11223344" || e.NodeB != "eeff00112233aabb" {
		t.Fatalf("edge used path_json prefixes instead of resolved pubkeys: %+v", e)
	}
	if e.NameA != "TestRepeater" || e.NameB != "TestCompanion" {
		t.Fatalf("edge names not populated from nodes: %+v", e)
	}
	if resp.CandidateEdges != 1 || resp.RawEdges != 1 || resp.MissingGPSEdges != 0 || resp.UnmappedEdges != 0 {
		t.Fatalf("unexpected diagnostics: candidates=%d raw=%d missingGPS=%d unmapped=%d",
			resp.CandidateEdges, resp.RawEdges, resp.MissingGPSEdges, resp.UnmappedEdges)
	}
}

func TestHandleRouteHistory_DiagnosticsForMissingGPS(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	now := time.Now().UTC().Format(time.RFC3339)
	mustExecRouteHistoryDB(t, db, `INSERT INTO nodes (public_key, name, role, lat, lon, last_seen)
		VALUES ('node-a', 'Node A', 'repeater', 50.0, 5.0, ?),
		       ('node-b', 'Node B', 'repeater', NULL, NULL, ?)`, now, now)
	mustExecRouteHistoryDB(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type)
		VALUES (100, 'AA', 'nogpshash', ?, 1, 9)`, now)
	mustExecRouteHistoryDB(t, db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
		VALUES (100, 1, '["aa","bb"]', ?, '["node-a","node-b"]')`, time.Now().Unix())

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
	if resp.TotalEdges != 0 || resp.CandidateEdges != 1 || resp.MissingGPSEdges != 1 ||
		resp.MappedEdges != 0 || resp.RawEdges != 1 || resp.UnmappedEdges != 1 {
		t.Fatalf("unexpected diagnostics: %+v", resp)
	}
}

func TestHandleRouteHistory_ModernSchemaIgnoresRawPathFallback(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)

	mustExecRouteHistoryDB(t, db, `INSERT INTO nodes (public_key, name, role, lat, lon, last_seen)
		VALUES ('raw-aa-node', 'Raw A', 'repeater', 50.0, 5.0, ?),
		       ('raw-bb-node', 'Raw B', 'repeater', 51.0, 5.1, ?)`, recent, recent)
	mustExecRouteHistoryDB(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type)
		VALUES (200, 'AA', 'rawpathonly', ?, 1, 9)`, recent)
	mustExecRouteHistoryDB(t, db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
		VALUES (200, 1, '["raw-aa-node","raw-bb-node"]', ?, NULL)`, now.Unix())

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
	if resp.RawEdges != 0 || resp.UnmappedEdges != 0 || resp.MissingGPSEdges != 0 || resp.MappedEdges != 0 {
		t.Fatalf("modern schema should ignore unresolved path_json rows, got %+v", resp)
	}
}

func TestBuildRouteHistoryEdgesFromSnap_ModernSchemaIgnoresRawPathFallback(t *testing.T) {
	nodeA := "aa11111111111111"
	nodeB := "bb22222222222222"
	store := &PacketStore{
		db: &DB{hasResolvedPath: true},
		packets: []*StoreTx{
			{
				ID:        1,
				Hash:      "rawonly",
				FirstSeen: "2026-01-01T01:00:00Z",
				PathJSON:  `["` + nodeA + `","` + nodeB + `"]`,
			},
		},
	}
	edgeMap := map[rhEdgeKey]*rhEdgeData{}
	buildRouteHistoryEdgesFromSnap(store, store.packets, "2026-01-01T00:00:00Z", edgeMap)
	if len(edgeMap) != 0 {
		t.Fatalf("modern in-memory path should not fall back to raw path_json without resolved_path, got %d edge(s)", len(edgeMap))
	}
}

func TestRouteHistoryCache_CoalescesInFlightByHourWindow(t *testing.T) {
	var cache routeHistoryCacheState
	if payload, call, started := cache.getOrStart(24); payload != nil || call == nil || !started {
		t.Fatalf("first caller should start compute, payload=%q call=%v started=%v", payload, call, started)
	}

	_, call, started := cache.getOrStart(24)
	if call == nil || started {
		t.Fatalf("second caller should wait on existing in-flight call, call=%v started=%v", call, started)
	}

	want := []byte(`{"ok":true}`)
	var wg sync.WaitGroup
	wg.Add(1)
	var got []byte
	var waitErr error
	go func() {
		defer wg.Done()
		got, waitErr = cache.wait(call)
	}()
	cache.finish(24, call, want, nil)
	wg.Wait()

	if waitErr != nil {
		t.Fatalf("wait returned error: %v", waitErr)
	}
	if string(got) != string(want) {
		t.Fatalf("wait payload = %s, want %s", got, want)
	}
	if cached := cache.get(24); string(cached) != string(want) {
		t.Fatalf("cached payload = %s, want %s", cached, want)
	}
}

func TestHandleRouteHistory_CachesByHourWindow(t *testing.T) {
	db := setupTestDB(t)
	s := &Server{cfg: &Config{}, db: db}
	now := time.Now().UTC()
	firstSeen := now.Add(-1 * time.Hour).Format(time.RFC3339)
	nodeA := "aa11111111111111"
	nodeB := "bb22222222222222"

	mustExecRouteHistoryDB(t, db, `INSERT INTO nodes (public_key, name, role, lat, lon) VALUES (?, 'Node A', 'repeater', 52.1, 5.1)`, nodeA)
	mustExecRouteHistoryDB(t, db, `INSERT INTO nodes (public_key, name, role, lat, lon) VALUES (?, 'Node B', 'repeater', 52.2, 5.2)`, nodeB)
	mustExecRouteHistoryDB(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (1, '00', 'hash1', ?)`, firstSeen)
	mustExecRouteHistoryDB(t, db, `INSERT INTO observations (transmission_id, path_json, resolved_path, timestamp) VALUES (1, '["aa","bb"]', ?, ?)`,
		`["`+nodeA+`","`+nodeB+`"]`, now.Unix())

	req := httptest.NewRequest("GET", "/api/route-history?hours=6", nil)
	rr := httptest.NewRecorder()
	s.handleRouteHistory(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("first request: want 200, got %d", rr.Code)
	}

	mustExecRouteHistoryDB(t, db, `DELETE FROM observations`)
	mustExecRouteHistoryDB(t, db, `DELETE FROM transmissions`)

	req = httptest.NewRequest("GET", "/api/route-history?hours=6", nil)
	rr = httptest.NewRecorder()
	s.handleRouteHistory(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("second request: want 200, got %d", rr.Code)
	}
	var resp routeHistoryResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if resp.MappedEdges != 1 {
		t.Fatalf("want cached mapped edge after DB rows deleted, got %d", resp.MappedEdges)
	}
}

func TestHandleRouteHistory_UsesMaterializedEdgesBeforeRawScan(t *testing.T) {
	db := setupTestDB(t)
	s := &Server{cfg: &Config{}, db: db}
	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)
	nodeA := "aa11111111111111"
	nodeB := "bb22222222222222"
	rawA := "cc33333333333333"
	rawB := "dd44444444444444"

	mustExecRouteHistoryDB(t, db, `CREATE TABLE route_history_edges (
		observation_id INTEGER NOT NULL,
		hop_index INTEGER NOT NULL,
		bucket_start INTEGER NOT NULL,
		node_a TEXT NOT NULL,
		node_b TEXT NOT NULL,
		packet_hash TEXT NOT NULL,
		last_seen TEXT NOT NULL,
		PRIMARY KEY (observation_id, hop_index)
	)`)
	mustExecRouteHistoryDB(t, db, `CREATE INDEX idx_route_history_edges_last_seen ON route_history_edges(last_seen)`)
	mustExecRouteHistoryDB(t, db, `INSERT INTO nodes (public_key, name, role, lat, lon) VALUES
		(?, 'Mat A', 'repeater', 52.1, 5.1),
		(?, 'Mat B', 'repeater', 52.2, 5.2),
		(?, 'Raw A', 'repeater', 53.1, 6.1),
		(?, 'Raw B', 'repeater', 53.2, 6.2)`, nodeA, nodeB, rawA, rawB)
	mustExecRouteHistoryDB(t, db, `INSERT INTO route_history_edges
		(observation_id, hop_index, bucket_start, node_a, node_b, packet_hash, last_seen)
		VALUES (1, 0, ?, ?, ?, 'mat-hash', ?)`, now.Truncate(time.Hour).Unix(), nodeA, nodeB, recent)

	mustExecRouteHistoryDB(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (10, '00', 'raw-hash', ?)`, recent)
	mustExecRouteHistoryDB(t, db, `INSERT INTO observations (transmission_id, path_json, resolved_path, timestamp) VALUES (10, ?, ?, ?)`,
		`["cc","dd"]`, `["`+rawA+`","`+rawB+`"]`, now.Unix())

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
	if resp.MappedEdges != 1 {
		t.Fatalf("mapped edges = %d, want 1: %+v", resp.MappedEdges, resp)
	}
	e := resp.Edges[0]
	if e.NodeA != nodeA || e.NodeB != nodeB || e.Samples[0] != "mat-hash" {
		t.Fatalf("expected materialized edge, got %+v", e)
	}
}
