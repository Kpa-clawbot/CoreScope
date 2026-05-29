package main

import (
	"sync/atomic"
	"testing"
	"time"
)

// #1483 follow-up: assert the recompute interval is actually honored.
// Without this, changing 5min → 5hr in code would silently still tick
// every 5min and no test would catch it.
func TestNeighborGraphCacheRecomputerHonorsInterval(t *testing.T) {
	s := &Server{
		computeNeighborGraphResponseFn: func(minCount int, minScore float64, region, role string) NeighborGraphResponse {
			return NeighborGraphResponse{}
		},
	}
	// Count successful rebuilds via the at-timestamp swaps.
	var rebuilds atomic.Int32
	stop := make(chan struct{})
	// Wrap the recompute call by patching: easiest is to count from
	// the swapped entry pointer. Use a small interval and watch for
	// at least 3 ticks within a bounded wall-clock budget.
	go func() {
		var lastAt time.Time
		for {
			select {
			case <-stop:
				return
			default:
				if e := s.neighborGraphCache.ptr.Load(); e != nil && !e.at.Equal(lastAt) {
					rebuilds.Add(1)
					lastAt = e.at
				}
				time.Sleep(2 * time.Millisecond)
			}
		}
	}()
	// 10ms interval, run for ~120ms → expect ~12 rebuilds. Assert ≥ 3
	// to keep the test robust against scheduling jitter.
	s.startNeighborGraphRecomputer(10*time.Millisecond, stop)
	time.Sleep(120 * time.Millisecond)
	close(stop)
	got := rebuilds.Load()
	if got < 3 {
		t.Fatalf("expected ≥3 rebuilds with 10ms interval over 120ms, got %d", got)
	}
}
