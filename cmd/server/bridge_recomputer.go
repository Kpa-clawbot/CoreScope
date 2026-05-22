// Package main: bridge-axis recomputer (issue #672 axis 2 of 4).
//
// Steady-state background loop that recomputes the per-pubkey bridge
// centrality score over the in-memory NeighborGraph and stores the
// resulting map atomically. handleNodes reads via a single atomic
// load — no lock contention with ingest or with other recomputers
// (same pattern as #1240 / #1248).
//
// Interval default: 5 minutes. The graph itself rebuilds asynchronously
// on its own schedule (path_inspect.go); a 5-minute cadence here is
// well within the freshness budget for a structural metric (centrality
// changes slowly — a new edge or evicted node nudges scores by
// fractions of a percent).
//
// Cost (Brandes + Dijkstra): O(V · (E + V log V)). Staging-scale ~600
// nodes / ~2 000 edges ≈ ~4.8M ops, well under 100 ms in practice. On
// host-fleet scale (5 000 nodes / 30 000 edges) it is still seconds,
// running in a background goroutine off the request path.
package main

import (
	"sync"
	"time"
)

// bridgeRecomputerDefaultInterval is how often the bridge score map is
// rebuilt. 5 minutes mirrors analytics_recomputer (#1240) and
// repeater_enrich_recomputer (#1262); centrality is a slow-moving
// structural signal and does not warrant tighter cadence.
const bridgeRecomputerDefaultInterval = 5 * time.Minute

// bridgeRecompStartedMu serializes start of the bridge recomputer.
// We do not currently expose Stop publicly — the goroutine lives for
// the lifetime of the process — but keeping the started flag local
// (instead of on PacketStore) avoids further field churn in store.go.
var (
	bridgeRecompStartedMu sync.Mutex
	bridgeRecompStarted   bool
)

// StartBridgeScoreRecomputer launches the bridge-centrality recomputer
// (issue #672 axis 2). It performs an initial synchronous compute so
// that the very first /api/nodes after server start hits a populated
// snapshot instead of returning bridge_score=0 for every node, then
// reschedules every `interval` (default 5min if <= 0).
//
// Idempotent: subsequent calls are no-ops and return a no-op stop
// closure.
func (s *PacketStore) StartBridgeScoreRecomputer(interval time.Duration) func() {
	if interval <= 0 {
		interval = bridgeRecomputerDefaultInterval
	}

	bridgeRecompStartedMu.Lock()
	if bridgeRecompStarted {
		bridgeRecompStartedMu.Unlock()
		return func() {}
	}
	bridgeRecompStarted = true
	stop := make(chan struct{})
	done := make(chan struct{})
	bridgeRecompStartedMu.Unlock()

	// Initial synchronous prewarm — see comment above.
	recomputeBridgeScoresSafe(s)

	var stopOnce sync.Once
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				recomputeBridgeScoresSafe(s)
			case <-stop:
				return
			}
		}
	}()

	return func() {
		stopOnce.Do(func() {
			close(stop)
		})
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}
}

// recomputeBridgeScoresSafe runs ComputeBridgeScores over the current
// neighbor graph and installs the result. Panics in compute are
// swallowed (defensive) so the goroutine never dies; the previous
// snapshot remains valid.
func recomputeBridgeScoresSafe(s *PacketStore) {
	defer func() { _ = recover() }()
	graph := s.graph.Load()
	if graph == nil {
		// No graph yet — install an empty map so readers get a defined
		// zero rather than a nil sentinel (handleNodes treats both as
		// 0.0, but an explicit empty snapshot avoids "is this ready
		// yet?" confusion in operator-facing tooling).
		empty := map[string]float64{}
		s.bridgeScoreMap.Store(&empty)
		return
	}
	now := time.Now()
	edges := bridgeEdgesFromGraph(graph, now)
	scores := ComputeBridgeScores(edges)
	s.bridgeScoreMap.Store(&scores)
}

// bridgeEdgesFromGraph snapshots the NeighborGraph into a flat slice
// of BridgeEdge tuples with weight = Score(now) * Confidence(), per
// the convention established by #1235. Edges with unresolved B
// endpoints (no concrete pubkey yet — only a hop prefix) are skipped:
// they contribute no betweenness signal because the second endpoint
// is unknown.
func bridgeEdgesFromGraph(graph *NeighborGraph, now time.Time) []BridgeEdge {
	all := graph.AllEdges()
	out := make([]BridgeEdge, 0, len(all))
	for _, e := range all {
		if e == nil {
			continue
		}
		if e.NodeA == "" || e.NodeB == "" {
			// Unresolved (prefix-only) — no defined second endpoint.
			continue
		}
		w := e.Score(now) * e.Confidence()
		if w < bridgeMinWeightEpsilon {
			continue
		}
		out = append(out, BridgeEdge{A: e.NodeA, B: e.NodeB, Weight: w})
	}
	return out
}

// GetBridgeScore returns the bridge centrality score for a pubkey in
// [0, 1], or 0 if the recomputer has not run yet or the pubkey is not
// in the graph. Lookup is case-insensitive (the score map keys are
// lowercase, matching byPathHop convention).
func (s *PacketStore) GetBridgeScore(pubkey string) float64 {
	if pubkey == "" {
		return 0
	}
	snap := s.bridgeScoreMap.Load()
	if snap == nil {
		return 0
	}
	m := *snap
	if v, ok := m[pubkey]; ok {
		return v
	}
	// Try lowercase form.
	lc := pubkey
	for i := 0; i < len(lc); i++ {
		if lc[i] >= 'A' && lc[i] <= 'Z' {
			b := []byte(pubkey)
			for j := i; j < len(b); j++ {
				if b[j] >= 'A' && b[j] <= 'Z' {
					b[j] += 'a' - 'A'
				}
			}
			lc = string(b)
			break
		}
	}
	if v, ok := m[lc]; ok {
		return v
	}
	return 0
}

// GetBridgeScoreMap returns a defensive copy-by-reference of the
// current bridge score snapshot. Nil-safe: returns an empty map if
// no snapshot has been installed yet. Map is read-only by convention
// — callers MUST NOT mutate it (the snapshot is shared across all
// concurrent readers).
func (s *PacketStore) GetBridgeScoreMap() map[string]float64 {
	snap := s.bridgeScoreMap.Load()
	if snap == nil {
		return map[string]float64{}
	}
	return *snap
}

// resetBridgeRecomputerForTest is a test-only helper to allow the
// integration test to re-Start the recomputer in a fresh process
// (which would otherwise be blocked by the package-level
// bridgeRecompStarted flag). Production code must not call this.
func resetBridgeRecomputerForTest() {
	bridgeRecompStartedMu.Lock()
	bridgeRecompStarted = false
	bridgeRecompStartedMu.Unlock()
}
