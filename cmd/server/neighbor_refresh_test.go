package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A warm request must not visit the packet store, even after the old TTL. The
// locked store makes a request-path rebuild fail deterministically (#2221).
func TestNeighborGraphStaleSnapshotDoesNotRebuildOnRequest(t *testing.T) {
	store := ngTestStore(nil, nil)
	graph := makeTestGraph()
	graph.builtAt = time.Now().Add(-2 * neighborGraphTTL)
	s := &Server{store: store, neighborGraph: graph}
	store.mu.Lock()
	result := make(chan *NeighborGraph, 1)
	go func() { result <- s.getNeighborGraph() }()
	select {
	case got := <-result:
		store.mu.Unlock()
		if got != graph {
			t.Fatal("request replaced an existing stale snapshot")
		}
	case <-time.After(500 * time.Millisecond):
		store.mu.Unlock()
		<-result
		t.Fatal("stale snapshot request waited for a packet-store rebuild")
	}
}

func TestNeighborGraphBackgroundRefreshRebuildsFreshSnapshot(t *testing.T) {
	store := ngTestStore(nil, nil)
	s := &Server{store: store}
	first := s.getNeighborGraph()
	if first.IsStale() {
		t.Fatal("fixture must start with a fresh snapshot")
	}
	observations := make([]*StoreObs, 20) // above the default min-count and score filters
	for i := range observations {
		observations[i] = ngMakeObs("bbbb2222", `[]`, time.Now().UTC().Format(time.RFC3339), nil)
	}
	store.mu.Lock()
	store.packets = []*StoreTx{ngMakeTx(1, PayloadADVERT, ngFromNodeJSON("aaaa1111"), observations)}
	store.mu.Unlock()

	s.recomputeNeighborGraphCache()
	second := s.getNeighborGraph()
	if second == first {
		t.Fatal("background refresh reused the almost-stale graph instead of rebuilding")
	}
	edges := second.Neighbors("aaaa1111")
	if len(edges) != 1 || edges[0].Count != len(observations) {
		t.Fatal("background refresh did not include newly received observations")
	}
	for _, entry := range []*neighborGraphCacheEntry{s.neighborGraphCache.ptr.Load(), s.neighborGraphCache.unfilteredPtr.Load()} {
		if entry == nil || len(entry.resp.Edges) != 1 || entry.resp.Edges[0].Weight != len(observations) {
			t.Fatal("background response cache did not use the rebuilt graph")
		}
	}
	s.recomputeNeighborGraphCache()
	if s.getNeighborGraph() == second {
		t.Fatal("a second background tick skipped a still-fresh graph")
	}
}

func TestNeighborGraphConcurrentColdRequestsShareOneSnapshot(t *testing.T) {
	store := ngTestStore(nil, nil)
	s := &Server{store: store}
	const callers = 32
	results := make(chan *NeighborGraph, callers)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			ready.Done()
			<-start
			results <- s.getNeighborGraph()
		}()
	}
	ready.Wait()
	close(start)
	first := <-results
	if first == nil || first.IsStale() {
		t.Fatal("cold initialization did not build a fresh snapshot")
	}
	for i := 1; i < callers; i++ {
		if got := <-results; got != first {
			t.Fatal("concurrent cold requests built different snapshots")
		}
	}
}

func TestNeighborGraphRefreshPanicPreservesSnapshot(t *testing.T) {
	graph := makeTestGraph()
	store := ngTestStore(nil, []*StoreTx{nil}) // builder failure, before publication
	s := &Server{store: store, neighborGraph: graph}
	s.recomputeNeighborGraphCache()
	if s.getNeighborGraph() != graph || atomic.LoadUint64(&s.neighborGraphCacheRebuildFailures) != 1 {
		t.Fatal("failed rebuild did not retain the last complete snapshot and count its failure")
	}
	store.mu.Lock()
	store.packets = nil
	store.mu.Unlock()
	done := make(chan struct{})
	go func() {
		s.recomputeNeighborGraphCache()
		close(done)
	}()
	select {
	case <-done:
		if s.getNeighborGraph() == graph {
			t.Fatal("a later refresh could not recover after a build panic")
		}
	case <-time.After(time.Second):
		t.Fatal("build panic stranded the refresh lock")
	}
}

func TestNeighborGraphBackgroundRefreshKeepsReadersAvailable(t *testing.T) {
	for _, tc := range []struct {
		name      string
		responses bool
	}{{"graph-only", false}, {"response-cache", true}} {
		t.Run(tc.name, func(t *testing.T) {
			graph := makeTestGraph()
			store := ngTestStore(nil, nil)
			s := &Server{store: store, neighborGraph: graph}
			store.mu.Lock() // keep the actual graph builder in flight
			done := make(chan struct{})
			go func() {
				s.recomputeNeighborGraph(tc.responses)
				close(done)
			}()
			defer func() {
				store.mu.Unlock()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Error("background refresh did not finish after releasing the store")
				}
			}()
			deadline := time.After(time.Second)
			ticker := time.NewTicker(time.Millisecond)
			defer ticker.Stop()
			for s.neighborBuildMu.TryLock() {
				s.neighborBuildMu.Unlock()
				select {
				case <-deadline:
					t.Fatal("background refresh did not start its builder")
				case <-ticker.C:
				}
			}
			if !s.neighborMu.TryLock() {
				t.Fatal("graph builder held the snapshot pointer mutex")
			}
			s.neighborMu.Unlock()
			result := make(chan *NeighborGraph, 1)
			go func() { result <- s.getNeighborGraph() }()
			select {
			case got := <-result:
				if got != graph {
					t.Fatal("request lost the last completed snapshot during refresh")
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatal("request waited for the background builder")
			}
		})
	}
}
