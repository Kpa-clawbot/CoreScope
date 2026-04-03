package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDebugAffinityEndpoint(t *testing.T) {
	now := time.Now()

	edge1 := newEdge("aaaa1111", "bbbb2222", "bb", 50, now)
	edge2 := newEdge("aaaa1111", "", "cc", 10, now)
	edge2.Ambiguous = true
	edge2.Candidates = []string{"cccc3333", "cccc4444"}

	graph := makeTestGraph(edge1, edge2)
	srv := makeTestServer(graph)
	srv.cfg = &Config{APIKey: "test-key", DebugAffinity: true}

	r, _ := http.NewRequest("GET", "/api/debug/affinity", nil)
	r.Header.Set("X-API-Key", "test-key")
	w := httptest.NewRecorder()
	srv.handleDebugAffinity(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp DebugAffinityResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	if len(resp.Edges) != 2 {
		t.Errorf("expected 2 edges, got %d", len(resp.Edges))
	}

	// Check stats shape
	if resp.Stats.TotalEdges != 2 {
		t.Errorf("expected 2 total edges in stats, got %d", resp.Stats.TotalEdges)
	}
	if resp.Stats.LastRebuild == "" {
		t.Error("expected lastRebuild to be set")
	}
	if resp.Stats.CacheAge == "" {
		t.Error("expected cacheAge to be set")
	}
}

func TestDebugAffinityPrefixFilter(t *testing.T) {
	now := time.Now()
	edge1 := newEdge("aaaa1111", "bbbb2222", "bb", 50, now)
	edge2 := newEdge("aaaa1111", "dddd3333", "dd", 30, now)

	graph := makeTestGraph(edge1, edge2)
	srv := makeTestServer(graph)
	srv.cfg = &Config{APIKey: "test-key"}

	r, _ := http.NewRequest("GET", "/api/debug/affinity?prefix=bb", nil)
	r.Header.Set("X-API-Key", "test-key")
	w := httptest.NewRecorder()
	srv.handleDebugAffinity(w, r)

	var resp DebugAffinityResponse
	json.NewDecoder(w.Body).Decode(&resp)

	if len(resp.Edges) != 1 {
		t.Errorf("expected 1 edge with prefix filter, got %d", len(resp.Edges))
	}
}

func TestDebugAffinityNodeFilter(t *testing.T) {
	now := time.Now()
	edge1 := newEdge("aaaa1111", "bbbb2222", "bb", 50, now)
	edge2 := newEdge("cccc3333", "dddd4444", "dd", 30, now)

	graph := makeTestGraph(edge1, edge2)
	srv := makeTestServer(graph)
	srv.cfg = &Config{APIKey: "test-key"}

	r, _ := http.NewRequest("GET", "/api/debug/affinity?node=aaaa1111", nil)
	r.Header.Set("X-API-Key", "test-key")
	w := httptest.NewRecorder()
	srv.handleDebugAffinity(w, r)

	var resp DebugAffinityResponse
	json.NewDecoder(w.Body).Decode(&resp)

	if len(resp.Edges) != 1 {
		t.Errorf("expected 1 edge with node filter, got %d", len(resp.Edges))
	}
}

func TestDebugAffinityRequiresAuth(t *testing.T) {
	graph := makeTestGraph()
	srv := makeTestServer(graph)
	srv.cfg = &Config{APIKey: "secret"}

	r, _ := http.NewRequest("GET", "/api/debug/affinity", nil)
	r.Header.Set("X-API-Key", "wrong-key")
	w := httptest.NewRecorder()

	// Use the requireAPIKey middleware
	handler := srv.requireAPIKey(http.HandlerFunc(srv.handleDebugAffinity))
	handler.ServeHTTP(w, r)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestStructuredLogging(t *testing.T) {
	// Test that the logging function in the graph actually works
	var logMessages []string
	g := NewNeighborGraph()
	g.logFn = func(prefix, msg string) {
		logMessages = append(logMessages, "[affinity] resolve "+prefix+": "+msg)
	}

	// Add some edges that would trigger disambiguation
	now := time.Now()
	// Add resolved edges for neighbor sets
	g.mu.Lock()
	// Node aaaa has neighbors: xxxx, yyyy
	e1 := &NeighborEdge{NodeA: "aaaa", NodeB: "xxxx", Prefix: "xx", Count: 10, Observers: map[string]bool{}, FirstSeen: now, LastSeen: now}
	g.edges[makeEdgeKey("aaaa", "xxxx")] = e1
	g.byNode["aaaa"] = append(g.byNode["aaaa"], e1)
	g.byNode["xxxx"] = append(g.byNode["xxxx"], e1)

	e2 := &NeighborEdge{NodeA: "aaaa", NodeB: "yyyy", Prefix: "yy", Count: 10, Observers: map[string]bool{}, FirstSeen: now, LastSeen: now}
	g.edges[makeEdgeKey("aaaa", "yyyy")] = e2
	g.byNode["aaaa"] = append(g.byNode["aaaa"], e2)
	g.byNode["yyyy"] = append(g.byNode["yyyy"], e2)

	// Candidate cccc1 also has neighbor xxxx, yyyy (high Jaccard with aaaa)
	e3 := &NeighborEdge{NodeA: "cccc1", NodeB: "xxxx", Prefix: "xx", Count: 10, Observers: map[string]bool{}, FirstSeen: now, LastSeen: now}
	g.edges[makeEdgeKey("cccc1", "xxxx")] = e3
	g.byNode["cccc1"] = append(g.byNode["cccc1"], e3)

	e4 := &NeighborEdge{NodeA: "cccc1", NodeB: "yyyy", Prefix: "yy", Count: 10, Observers: map[string]bool{}, FirstSeen: now, LastSeen: now}
	g.edges[makeEdgeKey("cccc1", "yyyy")] = e4
	g.byNode["cccc1"] = append(g.byNode["cccc1"], e4)

	// Candidate cccc2 has no neighbors (low Jaccard)
	// Add ambiguous edge: aaaa ↔ prefix:cc with candidates [cccc1, cccc2]
	ambigEdge := &NeighborEdge{
		NodeA: "aaaa", NodeB: "", Prefix: "cc", Count: 5,
		Ambiguous: true, Candidates: []string{"cccc1", "cccc2"},
		Observers: map[string]bool{}, FirstSeen: now, LastSeen: now,
	}
	ambigKey := makeEdgeKey("aaaa", "prefix:cc")
	g.edges[ambigKey] = ambigEdge
	g.byNode["aaaa"] = append(g.byNode["aaaa"], ambigEdge)
	g.mu.Unlock()

	// Now run disambiguate — this should trigger logging
	g.disambiguate()

	if len(logMessages) == 0 {
		t.Error("expected at least one log message from disambiguation")
	}

	found := false
	for _, msg := range logMessages {
		if strings.Contains(msg, "[affinity] resolve cc:") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected log message about prefix 'cc', got: %v", logMessages)
	}
}

func TestColdStartCoverage(t *testing.T) {
	edges := []*NeighborEdge{
		{Prefix: "aa", Count: 5},
		{Prefix: "bb", Count: 3},
		{Prefix: "cc", Count: 1}, // below threshold
	}

	srv := &Server{cfg: &Config{}}
	coverage := srv.computeColdStartCoverage(edges)

	// 2 out of 3 prefixes have >=3 observations = 66.7%
	if coverage < 66.0 || coverage > 67.0 {
		t.Errorf("expected ~66.7%% coverage, got %.1f%%", coverage)
	}
}

func TestDebugResponseShape(t *testing.T) {
	edge := newEdge("aaaa1111", "bbbb2222", "bb", 50, time.Now())
	edge.Resolved = true

	graph := makeTestGraph(edge)
	srv := makeTestServer(graph)
	srv.cfg = &Config{APIKey: "test-key"}

	r, _ := http.NewRequest("GET", "/api/debug/affinity", nil)
	r.Header.Set("X-API-Key", "test-key")
	w := httptest.NewRecorder()
	srv.handleDebugAffinity(w, r)

	var resp map[string]interface{}
	json.NewDecoder(w.Body).Decode(&resp)

	// Verify top-level keys
	for _, key := range []string{"edges", "resolutions", "stats"} {
		if _, ok := resp[key]; !ok {
			t.Errorf("missing top-level key: %s", key)
		}
	}

	stats := resp["stats"].(map[string]interface{})
	for _, key := range []string{"totalEdges", "totalNodes", "resolvedCount", "ambiguousCount", "unresolvedCount", "avgConfidence", "coldStartCoverage", "cacheAge", "lastRebuild"} {
		if _, ok := stats[key]; !ok {
			t.Errorf("missing stats key: %s", key)
		}
	}
}
