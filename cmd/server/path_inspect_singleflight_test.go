package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestEnsureNeighborGraph_Singleflight (issue #1203 Pair A) asserts that N
// concurrent callers over a stale graph trigger at most ONE buildGraphFn
// invocation. On master the handler spawns one BuildFromStore goroutine per
// request — wasted CPU and the rebuild-storm that produces the 503 loop.
//
// Anti-tautology: revert singleflight and this test fails (it observes 10).
func TestEnsureNeighborGraph_Singleflight(t *testing.T) {
	store := &PacketStore{}
	stale := NewNeighborGraph()
	stale.builtAt = time.Now().Add(-1 * time.Hour) // stale by TTL
	store.graph.Store(stale)

	var count int32
	origBuild := buildGraphFn
	defer func() { buildGraphFn = origBuild }()
	buildGraphFn = func(s *PacketStore) *NeighborGraph {
		atomic.AddInt32(&count, 1)
		time.Sleep(50 * time.Millisecond) // ensure callers actually overlap
		g := NewNeighborGraph()
		g.builtAt = time.Now()
		return g
	}

	var wg sync.WaitGroup
	const N = 10
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			store.ensureNeighborGraph()
		}()
	}
	wg.Wait()

	got := atomic.LoadInt32(&count)
	if got > 1 {
		t.Fatalf("expected ≤1 buildGraphFn invocation under singleflight, got %d", got)
	}
}
