package main

import (
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestNeighborGraphLongResponseIntervalStillRefreshesTopology(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := ngTestStore(nil, nil)
		s := &Server{store: store}
		var responseBuilds atomic.Int32
		s.computeNeighborGraphResponseFn = func(minCount int, minScore float64, region, role string) NeighborGraphResponse {
			responseBuilds.Add(1)
			return s.computeNeighborGraphResponse(minCount, minScore, region, role)
		}
		stop := make(chan struct{})
		defer close(stop)
		// Deliberately not a multiple of the graph TTL: refreshing graph data
		// must neither delay the configured JSON cadence nor repeat JSON work.
		interval := 3*neighborGraphTTL + 2*time.Minute
		s.startNeighborGraphRecomputer(interval, stop)
		synctest.Wait()
		cached := s.neighborGraphCache.ptr.Load()
		unfiltered := s.neighborGraphCache.unfilteredPtr.Load()
		graph := s.getNeighborGraph()
		if cached == nil || unfiltered == nil || responseBuilds.Load() != 2 {
			t.Fatal("initial graph and both response caches were not built")
		}

		observations := make([]*StoreObs, 20)
		for i := range observations {
			observations[i] = ngMakeObs("bbbb2222", `[]`, time.Now().UTC().Format(time.RFC3339), nil)
		}
		store.mu.Lock()
		store.packets = []*StoreTx{ngMakeTx(1, PayloadADVERT, ngFromNodeJSON("aaaa1111"), observations)}
		store.mu.Unlock()
		for i := 0; i < 3; i++ {
			time.Sleep(neighborGraphTTL)
			synctest.Wait()
			fresh := s.getNeighborGraph()
			if fresh == graph {
				t.Fatalf("topology did not refresh after TTL at tick %d with a long response interval", i+1)
			}
			if edges := fresh.Neighbors("aaaa1111"); len(edges) != 1 || edges[0].Count != len(observations) {
				t.Fatal("refreshed topology omitted new observations")
			}
			if s.neighborGraphCache.ptr.Load() != cached || s.neighborGraphCache.unfilteredPtr.Load() != unfiltered || responseBuilds.Load() != 2 {
				t.Fatal("graph-only refresh rebuilt the response caches before their configured interval")
			}
			graph = fresh
		}
		time.Sleep(2 * time.Minute)
		synctest.Wait()
		if s.getNeighborGraph() == graph || responseBuilds.Load() != 4 {
			t.Fatal("configured response tick did not force a fresh graph and rebuild both response caches")
		}
		for _, entry := range []*neighborGraphCacheEntry{s.neighborGraphCache.ptr.Load(), s.neighborGraphCache.unfilteredPtr.Load()} {
			if len(entry.resp.Edges) != 1 || entry.resp.Edges[0].Weight != len(observations) {
				t.Fatal("response cache did not include the refreshed topology")
			}
		}
	})
}

func TestNeighborGraphOnlyRefreshRecoversAfterPanic(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := ngTestStore(nil, nil)
		s := &Server{store: store}
		stop := make(chan struct{})
		defer func() {
			select {
			case <-stop:
			default:
				close(stop)
			}
		}()
		s.startNeighborGraphRecomputer(time.Hour, stop)
		synctest.Wait()
		graph := s.getNeighborGraph()
		cached := s.neighborGraphCache.ptr.Load()
		store.mu.Lock()
		store.packets = []*StoreTx{nil}
		store.mu.Unlock()
		time.Sleep(neighborGraphTTL)
		synctest.Wait()
		if s.getNeighborGraph() != graph || atomic.LoadUint64(&s.neighborGraphCacheRebuildFailures) != 1 {
			t.Fatal("failed graph-only refresh lost the old snapshot or failed to count the error")
		}
		store.mu.Lock()
		store.packets = nil
		store.mu.Unlock()
		time.Sleep(neighborGraphTTL)
		synctest.Wait()
		if s.getNeighborGraph() == graph || s.neighborGraphCache.ptr.Load() != cached {
			t.Fatal("graph-only refresh did not recover independently of the response cache")
		}
		close(stop)
		synctest.Wait()
		graph = s.getNeighborGraph()
		time.Sleep(time.Hour)
		synctest.Wait()
		if s.getNeighborGraph() != graph || s.neighborGraphCache.ptr.Load() != cached {
			t.Fatal("graph or response refreshed after the recomputer stopped")
		}
	})
}

func TestNeighborGraphDefaultAndShortResponseIntervals(t *testing.T) {
	for _, interval := range []time.Duration{0, -time.Second, neighborGraphCacheInterval, time.Minute} {
		t.Run(interval.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := &Server{store: ngTestStore(nil, nil)}
				stop := make(chan struct{})
				defer close(stop)
				s.startNeighborGraphRecomputer(interval, stop)
				synctest.Wait()
				graph := s.getNeighborGraph()
				cached := s.neighborGraphCache.ptr.Load()
				delay := interval
				if delay <= 0 {
					delay = neighborGraphCacheInterval
				}
				time.Sleep(delay - time.Nanosecond)
				synctest.Wait()
				if s.getNeighborGraph() != graph || s.neighborGraphCache.ptr.Load() != cached {
					t.Fatal("graph or response refreshed before the configured interval")
				}
				time.Sleep(time.Nanosecond)
				synctest.Wait()
				if s.getNeighborGraph() == graph || s.neighborGraphCache.ptr.Load() == cached {
					t.Fatal("configured interval did not refresh both the graph and response")
				}
			})
		})
	}
}
