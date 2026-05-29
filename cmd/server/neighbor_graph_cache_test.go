package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
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

// #1481 P0-1: non-default query (e.g. ?region=X) must bypass the cache.
func TestNeighborGraphCacheBypassOnRegionFilter(t *testing.T) {
	s := &Server{}
	resp := NeighborGraphResponse{
		Nodes: []GraphNode{{Pubkey: "deadbeef", Name: "cached-only"}},
	}
	raw, _ := json.Marshal(resp)
	s.neighborGraphCache.ptr.Store(&neighborGraphCacheEntry{resp: resp, json: raw})

	req := httptest.NewRequest("GET", "/api/analytics/neighbor-graph?region=USA", nil)
	w := httptest.NewRecorder()
	defer func() {
		_ = recover()
		if strings.Contains(w.Body.String(), "cached-only") {
			t.Fatalf("region=X must bypass cache, but cached value was served")
		}
	}()
	s.handleNeighborGraph(w, req)
}
