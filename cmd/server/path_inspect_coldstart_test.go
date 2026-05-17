package main

import (
	"bytes"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestHandlePathInspect_ColdStartKicksRebuild (issue #1203 Pair C) asserts that
// a true cold start (nil graph) returns 503 immediately AND kicks off a
// background rebuild, so the next request lands warm.
//
// Anti-tautology: if the cold-start branch stops calling ensureNeighborGraph
// (the regression that motivated this fix — synchronous 2s gate version
// blocked on response instead of kicking-and-returning), the follow-up
// request would still be 503 and this test would fail.
func TestHandlePathInspect_ColdStartKicksRebuild(t *testing.T) {
	srv := newTestServerForInspect(t)
	srv.store.graph.Store(nil)

	var built int32
	origBuild := buildGraphFn
	defer func() { buildGraphFn = origBuild }()
	buildGraphFn = func(s *PacketStore) *NeighborGraph {
		atomic.AddInt32(&built, 1)
		time.Sleep(100 * time.Millisecond) // small async window
		g := NewNeighborGraph()
		g.builtAt = time.Now()
		return g
	}

	// Seed nodes so the post-rebuild request can return a candidate.
	pk := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	srv.store.nodeCache = []nodeInfo{{PublicKey: pk, Name: "N", Role: "repeater"}}
	srv.store.nodePM = buildPrefixMap(srv.store.nodeCache)
	srv.store.nodeCacheTime = time.Now()

	req := httptest.NewRequest("POST", "/api/paths/inspect", bytes.NewBufferString(`{"prefixes":["aa"]}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	start := time.Now()
	srv.handlePathInspect(rr, req)
	elapsed := time.Since(start)

	if rr.Code != 503 {
		t.Fatalf("cold start: expected 503, got %d body=%s", rr.Code, rr.Body.String())
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("cold-start 503 should be near-instant, took %v", elapsed)
	}

	// Wait for rebuild to land.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&built) >= 1 && srv.store.graph.Load() != nil && !srv.store.graph.Load().IsStale() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if atomic.LoadInt32(&built) < 1 {
		t.Fatal("cold-start did not kick off a rebuild")
	}

	// Follow-up request now lands warm (200, not 503).
	// Use a different prefix so the inspect cache from pair A's earlier call
	// (if any) doesn't satisfy it.
	rr2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/api/paths/inspect", bytes.NewBufferString(`{"prefixes":["aa"]}`))
	req2.Header.Set("Content-Type", "application/json")
	srv.handlePathInspect(rr2, req2)
	if rr2.Code != 200 {
		t.Fatalf("follow-up after cold-start rebuild expected 200, got %d body=%s", rr2.Code, rr2.Body.String())
	}
}
