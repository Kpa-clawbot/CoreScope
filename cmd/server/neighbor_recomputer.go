// Package main: neighbor-graph snapshot recomputer (issue #1287).
//
// Per #1287 Option 4: the ingestor owns the neighbor_edges table —
// it computes the graph from observations it ingests and persists
// snapshots there. The server READS the snapshot and atomic-swaps
// it into s.graph; that swap is exactly what this recomputer does.
//
// Cadence: 60s default. Staleness budget matches the existing
// analytics recomputer (#1240) — operators already accept that
// derived analytics lag the wire by tens of seconds.
package main

import (
	"sync"
	"time"
)

// NeighborGraphRecomputerDefaultInterval is how often the server
// re-reads the neighbor_edges snapshot. 60s is the standard
// staleness budget for derived analytics (#1240 / #1262 / #672 axis 2).
const NeighborGraphRecomputerDefaultInterval = 60 * time.Second

var (
	neighborRecompStartedMu sync.Mutex
	neighborRecompStarted   bool
)

// StartNeighborGraphRecomputer launches the background goroutine that
// re-reads neighbor_edges every `interval` and atomic-swaps the
// resulting NeighborGraph into s.graph. Idempotent — subsequent calls
// are no-ops and return a no-op stop closure.
//
// Server NEVER writes to neighbor_edges; the ingestor owns those
// writes per #1287. This recomputer is the ONLY thing that updates
// s.graph at steady state (the initial startup load in main.go is the
// other writer to s.graph, only at boot).
func (s *PacketStore) StartNeighborGraphRecomputer(interval time.Duration) func() {
	if interval <= 0 {
		interval = NeighborGraphRecomputerDefaultInterval
	}

	neighborRecompStartedMu.Lock()
	if neighborRecompStarted {
		neighborRecompStartedMu.Unlock()
		return func() {}
	}
	neighborRecompStarted = true
	stop := make(chan struct{})
	done := make(chan struct{})
	neighborRecompStartedMu.Unlock()

	var stopOnce sync.Once
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				s.refreshNeighborGraphFromSnapshot()
			case <-stop:
				return
			}
		}
	}()

	return func() {
		stopOnce.Do(func() { close(stop) })
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
}

// refreshNeighborGraphFromSnapshot re-reads neighbor_edges through
// the read-only DB handle and atomic-swaps a freshly built graph.
// Panics are swallowed defensively — the previous snapshot remains
// valid if a read fails.
func (s *PacketStore) refreshNeighborGraphFromSnapshot() {
	defer func() { _ = recover() }()
	if s.db == nil || s.db.conn == nil {
		return
	}
	g := loadNeighborEdgesFromDB(s.db.conn)
	if g != nil {
		s.graph.Store(g)
	}
}

// resetNeighborRecomputerForTest is a test helper — production code
// MUST NOT call this.
func resetNeighborRecomputerForTest() {
	neighborRecompStartedMu.Lock()
	neighborRecompStarted = false
	neighborRecompStartedMu.Unlock()
}
