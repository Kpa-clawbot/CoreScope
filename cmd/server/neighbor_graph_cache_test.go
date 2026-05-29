package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// #1481 P0-1: handler must serve from pre-marshaled cache when set.
func TestNeighborGraphCacheServesFromAtomicPointer(t *testing.T) {
	s := &Server{}
	resp := NeighborGraphResponse{
		Nodes: []GraphNode{{Pubkey: "deadbeef", Name: "cached-node"}},
		Edges: []GraphEdge{},
		Stats: GraphStats{TotalNodes: 1},
	}
	raw, _ := json.Marshal(resp)
	s.neighborGraphCache.ptr.Store(&neighborGraphCacheEntry{resp: resp, json: raw})

	req := httptest.NewRequest("GET", "/api/analytics/neighbor-graph", nil)
	w := httptest.NewRecorder()
	s.handleNeighborGraph(w, req)

	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "cached-node") {
		t.Fatalf("expected cached node in response, got: %s", w.Body.String())
	}
}

// #1481 P0-1: positive cache hit — default params with sentinel cache MUST
// return the sentinel verbatim (proves cache is wired and consulted).
func TestNeighborGraphCacheServesSentinelOnDefaultParams(t *testing.T) {
	s := &Server{}
	resp := NeighborGraphResponse{
		Nodes: []GraphNode{{Pubkey: "deadbeef", Name: "CACHED-SENTINEL"}},
	}
	raw, _ := json.Marshal(resp)
	s.neighborGraphCache.ptr.Store(&neighborGraphCacheEntry{resp: resp, json: raw})

	req := httptest.NewRequest("GET", "/api/analytics/neighbor-graph", nil)
	w := httptest.NewRecorder()
	s.handleNeighborGraph(w, req)

	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "CACHED-SENTINEL") {
		t.Fatalf("expected CACHED-SENTINEL in default-params body, got: %s", w.Body.String())
	}
}

// #1483 follow-up: the analytics UI fetches with min_count=1&min_score=0;
// that shape must ALSO be cache-served (from a separate atomic-pointer).
func TestNeighborGraphCacheServesUnfilteredShape(t *testing.T) {
	s := &Server{}
	resp := NeighborGraphResponse{
		Nodes: []GraphNode{{Pubkey: "abcd", Name: "UNFILTERED-SENTINEL"}},
	}
	raw, _ := json.Marshal(resp)
	s.neighborGraphCache.unfilteredPtr.Store(&neighborGraphCacheEntry{resp: resp, json: raw})

	req := httptest.NewRequest("GET", "/api/analytics/neighbor-graph?min_count=1&min_score=0", nil)
	w := httptest.NewRecorder()
	s.handleNeighborGraph(w, req)

	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "UNFILTERED-SENTINEL") {
		t.Fatalf("expected UNFILTERED-SENTINEL in analytics-shape body, got: %s", w.Body.String())
	}
	if h := w.Header().Get("X-Cache-Age-Seconds"); h == "" {
		t.Error("expected X-Cache-Age-Seconds header on cached response")
	}
}

// #1481 P0-1: non-default query (e.g. ?region=X) must bypass the cache
// and call the compute path (verified by injected counter). The bypass
// branch must NOT serve the sentinel — body must NOT contain it.
func TestNeighborGraphCacheBypassOnRegionFilter(t *testing.T) {
	var computeCalls atomic.Int32
	bypassResp := NeighborGraphResponse{
		Nodes: []GraphNode{{Pubkey: "abcd", Name: "BYPASS-COMPUTED"}},
		Stats: GraphStats{TotalNodes: 1},
	}
	s := &Server{
		computeNeighborGraphResponseFn: func(minCount int, minScore float64, region, role string) NeighborGraphResponse {
			computeCalls.Add(1)
			return bypassResp
		},
	}
	sentinel := NeighborGraphResponse{
		Nodes: []GraphNode{{Pubkey: "deadbeef", Name: "CACHED-SENTINEL"}},
	}
	rawSent, _ := json.Marshal(sentinel)
	s.neighborGraphCache.ptr.Store(&neighborGraphCacheEntry{resp: sentinel, json: rawSent})

	req := httptest.NewRequest("GET", "/api/analytics/neighbor-graph?region=USA", nil)
	w := httptest.NewRecorder()
	s.handleNeighborGraph(w, req)

	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, "CACHED-SENTINEL") {
		t.Fatalf("region=USA must bypass cache, but CACHED-SENTINEL was served: %s", body)
	}
	if !strings.Contains(body, "BYPASS-COMPUTED") {
		t.Fatalf("expected BYPASS-COMPUTED from compute fn, got: %s", body)
	}
	if got := computeCalls.Load(); got != 1 {
		t.Fatalf("expected compute fn called exactly once, got %d", got)
	}
	// Body must parse as non-empty JSON object with a nodes array.
	var parsed NeighborGraphResponse
	if err := json.Unmarshal(w.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("body is not valid JSON: %v body=%s", err, body)
	}
	if len(parsed.Nodes) == 0 {
		t.Fatalf("expected non-empty Nodes in response, got: %s", body)
	}
}
