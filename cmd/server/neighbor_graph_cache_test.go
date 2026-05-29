package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// #1481 P0-1: handler must serve from cache when set.
func TestNeighborGraphCacheServesFromAtomicPointer(t *testing.T) {
	s := &Server{}
	resp := NeighborGraphResponse{
		Nodes: []GraphNode{{Pubkey: "deadbeef", Name: "cached-node"}},
		Edges: []GraphEdge{},
		Stats: GraphStats{TotalNodes: 1},
	}
	s.neighborGraphCache.ptr.Store(&neighborGraphCacheEntry{resp: resp})

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
	s.neighborGraphCache.ptr.Store(&neighborGraphCacheEntry{resp: resp})

	req := httptest.NewRequest("GET", "/api/analytics/neighbor-graph?region=USA", nil)
	w := httptest.NewRecorder()
	// Will nil-deref on s.store etc. — that's fine; the assertion we want
	// is "we did NOT short-circuit on the cached entry". Recover the panic.
	defer func() {
		_ = recover()
		// If we reached here without writing "cached-only" we proved bypass.
		if strings.Contains(w.Body.String(), "cached-only") {
			t.Fatalf("region=X must bypass cache, but cached value was served")
		}
	}()
	s.handleNeighborGraph(w, req)
}
