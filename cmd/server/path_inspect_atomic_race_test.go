package main

import (
	"sync"
	"testing"
	"time"
)

// TestPacketStoreGraph_ConcurrentReadWrite_NoRace (PR #1208 kent #1)
// asserts that concurrent readers of s.graph racing with a writer that
// replaces the pointer don't trip the Go race detector. The PR #1203
// migration from plain `*NeighborGraph` to `atomic.Pointer[NeighborGraph]`
// is what makes this safe — without it, the reader/writer race is a real
// data race the runtime will flag under `go test -race`.
//
// Anti-tautology / mutation verification: revert s.graph to a plain
// `*NeighborGraph` field (drop atomic.Pointer, use direct assignment
// `s.graph = g` in ensureNeighborGraph and direct read `s.graph` at the
// callsites) and this test FAILS under `-race` with a "WARNING: DATA
// RACE" report on s.graph. Confirmed on a manually-reverted local branch.
//
// This test must be run under `-race` to actually exercise the assertion;
// CI runs the full suite under -race so it gates the migration.
func TestPacketStoreGraph_ConcurrentReadWrite_NoRace(t *testing.T) {
	store := &PacketStore{}

	// Prime with an initial graph so readers don't all bail on nil.
	initial := NewNeighborGraph()
	initial.builtAt = time.Now()
	store.graph.Store(initial)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Spawn N readers that hot-loop Load() — these would race against the
	// writer's Store() if s.graph were a plain pointer.
	const readers = 16
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					g := store.graph.Load()
					// Touch a field so the optimizer doesn't elide the load.
					if g != nil {
						_ = g.builtAt
					}
				}
			}
		}()
	}

	// Writer: replace the pointer repeatedly for 200ms. With atomic.Pointer
	// each Store() is publication-safe; with a plain pointer this is a
	// classic data race.
	wg.Add(1)
	go func() {
		defer wg.Done()
		deadline := time.Now().Add(200 * time.Millisecond)
		for time.Now().Before(deadline) {
			g := NewNeighborGraph()
			g.builtAt = time.Now()
			store.graph.Store(g)
		}
	}()

	// Let it run, then stop readers.
	time.Sleep(220 * time.Millisecond)
	close(stop)
	wg.Wait()

	// Reaching here without -race firing IS the assertion. If -race
	// detected a write/read collision on s.graph the test process will
	// have already failed with exit code 66.
}
