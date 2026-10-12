package main

import (
	"testing"
	"time"
)

// #2146: the lock convoy. A request that waits on SQL while holding
// s.mu.RLock blocks the ingest writer's Lock, and a pending writer blocks
// every new reader, even ones that do no SQL (/api/healthz).
//
// Here the resolved_path query takes slowSQL. While /api/packets is in that
// query, the ingest poller asks for the write lock and then a reader asks
// for RLock. The reader must not wait for the slow query.
func TestSlowResolvedPathSQLDoesNotStallReaders(t *testing.T) {
	const slowSQL = 300 * time.Millisecond
	s := newResolvedPathStore(t)
	clearResolvedPathLRU(s)

	inSQL := make(chan struct{}, 8)
	s.cacheLoadHook = func(kind string) {
		if kind == "resolvedPath" {
			inSQL <- struct{}{}
			time.Sleep(slowSQL)
		}
	}

	go s.QueryPackets(PacketQuery{Limit: 10})
	<-inSQL

	go func() { // the ingest poller
		s.mu.Lock()
		s.mu.Unlock()
	}()
	time.Sleep(10 * time.Millisecond) // let the writer queue up

	start := time.Now()
	s.mu.RLock()
	waited := time.Since(start)
	s.mu.RUnlock()

	t.Logf("reader waited %v behind a %v resolved_path query", waited, slowSQL)
	if waited > slowSQL/3 {
		t.Fatalf("reader waited %v: the slow query held s.mu", waited)
	}
}
