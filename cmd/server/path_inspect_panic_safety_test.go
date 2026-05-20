package main

import (
	"sync"
	"testing"
	"time"
)

// TestEnsureNeighborGraph_PanicSafeCleanup (PR #1208 kent #2) asserts
// that a panic inside buildGraphFn does NOT leak the in-flight singleflight
// state. The leader's defer in ensureNeighborGraph must clear
// s.rebuildInFlt and close(done) even on panic — otherwise every future
// caller hangs forever on <-ch (the channel never closes) and the next
// would-be leader observes a non-nil s.rebuildInFlt indefinitely.
//
// Anti-tautology / mutation verification: remove the panic-safe defer
// (replace it with an inline post-build sequence that only runs on normal
// return), and this test FAILS — the second ensureNeighborGraph call
// deadlocks past the per-call deadline. Confirmed on a manually-reverted
// local branch.
func TestEnsureNeighborGraph_PanicSafeCleanup(t *testing.T) {
	store := &PacketStore{}
	// Force the cold-start path so ensureNeighborGraph proceeds to
	// buildGraphFn (no fast-return on fresh graph).
	// s.graph defaults to nil — that's already cold-start.

	origBuild := buildGraphFn
	defer func() { buildGraphFn = origBuild }()
	buildGraphFn = func(s *PacketStore) *NeighborGraph {
		panic("synthetic build failure")
	}

	// First call: should panic-and-recover the goroutine, leaving the
	// store's singleflight slot CLEAN.
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("expected buildGraphFn to panic (test setup); recover returned nil")
			}
		}()
		store.ensureNeighborGraph()
	}()

	// If the defer cleaned up correctly, rebuildInFlt is nil again and
	// the next call can proceed. Swap in a working build so we can verify
	// the next call completes (not just deadlocks).
	var built int32
	buildGraphFn = func(s *PacketStore) *NeighborGraph {
		built = 1
		g := NewNeighborGraph()
		g.builtAt = time.Now()
		return g
	}

	// Drive the second call with a hard timeout so a leaked in-flight
	// channel (the bug shape) produces a clean test failure instead of
	// hanging the suite.
	done := make(chan struct{})
	var once sync.Once
	go func() {
		store.ensureNeighborGraph()
		once.Do(func() { close(done) })
	}()

	select {
	case <-done:
		// Cleanup OK.
	case <-time.After(2 * time.Second):
		t.Fatal("ensureNeighborGraph deadlocked after a panicking build — " +
			"panic-safe defer must clear s.rebuildInFlt and close(done)")
	}

	if built != 1 {
		t.Fatal("expected second build to run after panic cleanup; it never ran")
	}
	if g := store.graph.Load(); g == nil {
		t.Fatal("expected store.graph to be populated by the successful second build")
	}
}
