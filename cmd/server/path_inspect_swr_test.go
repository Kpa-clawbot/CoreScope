package main

import (
	"bytes"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// TestHandlePathInspect_StaleWhileRevalidate (issue #1203 Pair B) asserts that
// when s.graph is non-nil but stale, handlePathInspect serves it immediately
// with stale:true and kicks off a background rebuild. On master the handler
// times out at 2s and returns 503 instead.
//
// Anti-tautology: revert the SWR branch and this test fails (sees 503).
func TestHandlePathInspect_StaleWhileRevalidate(t *testing.T) {
	srv := newTestServerForInspect(t)
	srv.store.graph.builtAt = time.Now().Add(-1 * time.Hour) // stale

	// Seed nodes so beamSearch returns a candidate (prefix "aa").
	pk := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	srv.store.nodeCache = []nodeInfo{{PublicKey: pk, Name: "N", Role: "repeater"}}
	srv.store.nodePM = buildPrefixMap(srv.store.nodeCache)
	srv.store.nodeCacheTime = time.Now()

	// Slow rebuild — far longer than the historical 2s gate.
	var built int32
	origBuild := buildGraphFn
	defer func() { buildGraphFn = origBuild }()
	buildGraphFn = func(s *PacketStore) *NeighborGraph {
		atomic.AddInt32(&built, 1)
		time.Sleep(3 * time.Second)
		g := NewNeighborGraph()
		g.builtAt = time.Now()
		return g
	}

	req := httptest.NewRequest("POST", "/api/paths/inspect", bytes.NewBufferString(`{"prefixes":["aa"]}`))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	start := time.Now()
	srv.handlePathInspect(rr, req)
	elapsed := time.Since(start)

	if rr.Code != 200 {
		t.Fatalf("expected 200 from stale-while-revalidate, got %d body=%s", rr.Code, rr.Body.String())
	}
	if elapsed > 1*time.Second {
		t.Fatalf("handler blocked for %v — should return near-instant from stale graph", elapsed)
	}
	if !bytes.Contains(rr.Body.Bytes(), []byte(`"stale":true`)) {
		t.Fatalf("expected stale:true in response, got: %s", rr.Body.String())
	}

	// Background rebuild must have been kicked off.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&built) >= 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt32(&built) < 1 {
		t.Fatal("expected background rebuild to be kicked off")
	}
}
