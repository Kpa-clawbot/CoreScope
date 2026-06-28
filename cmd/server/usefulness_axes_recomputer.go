// Package main: usefulness-axes recomputer (issue #672 axes 3 & 4 of 4).
//
// Steady-state background loop that recomputes the per-pubkey Coverage
// (harmonic reach) and Redundancy (articulation criticality) scores over
// the in-memory NeighborGraph and stores the two resulting maps atomically.
// handleNodes reads each via a single atomic load — no lock contention with
// ingest or with the bridge recomputer (same discipline as #1240 / #1248).
//
// Both axes run over the SAME weighted edge snapshot the bridge axis uses
// (bridgeEdgesFromGraph), so the three structural axes always describe an
// identical graph. Cost is dominated by Coverage's all-sources Dijkstra,
// O(V·(E + V log V)) — the same budget as the bridge axis; Redundancy's
// Tarjan pass is O(V + E) and negligible. A 5-minute cadence (shared with
// the bridge / enrich recomputers) is well within the freshness budget for
// slow-moving structural metrics.
package main

import (
	"log"
	"sync"
	"time"
)

// usefulnessAxesRecomputerDefaultInterval mirrors the bridge recomputer:
// structural centrality is slow-moving and does not warrant a tighter
// cadence than the other derived-analytics loops.
const usefulnessAxesRecomputerDefaultInterval = 5 * time.Minute

// StartUsefulnessAxesRecomputer launches the coverage + redundancy
// recomputer (issue #672 axes 3 & 4). It performs an initial synchronous
// compute so the first /api/nodes after start hits populated snapshots
// rather than zeros, then reschedules every `interval` (default 5min if
// <= 0).
//
// Idempotent: subsequent calls are no-ops returning a no-op stop closure.
func (s *PacketStore) StartUsefulnessAxesRecomputer(interval time.Duration) func() {
	if interval <= 0 {
		interval = usefulnessAxesRecomputerDefaultInterval
	}

	s.usefulnessAxesRecompMu.Lock()
	if s.usefulnessAxesRecompStarted {
		s.usefulnessAxesRecompMu.Unlock()
		return func() {}
	}
	s.usefulnessAxesRecompStarted = true
	stop := make(chan struct{})
	done := make(chan struct{})
	s.usefulnessAxesRecompMu.Unlock()

	// Initial synchronous prewarm.
	s.recomputeUsefulnessAxes()

	var stopOnce sync.Once
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				s.recomputeUsefulnessAxes()
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

// recomputeUsefulnessAxes rebuilds both axis maps over the current neighbor
// graph and installs them. A panic is recovered AND logged (defensive) so the
// goroutine never dies silently; the previous snapshots remain valid.
func (s *PacketStore) recomputeUsefulnessAxes() {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[usefulness-axes-recompute] panic recovered, keeping previous snapshot: %v", r)
		}
	}()
	graph := s.graph.Load()
	if graph == nil {
		// No graph yet — install empty maps so readers get a defined zero.
		// Two independent map literals (not one aliased &empty) so a future
		// mutating caller of one snapshot can't accidentally affect the other.
		emptyCov := map[string]float64{}
		emptyRed := map[string]float64{}
		s.coverageScoreMap.Store(&emptyCov)
		s.redundancyScoreMap.Store(&emptyRed)
		return
	}
	now := time.Now()
	edges := bridgeEdgesFromGraph(graph, now)
	cov := ComputeCoverageScores(edges)
	red := ComputeRedundancyScores(edges)
	s.coverageScoreMap.Store(&cov)
	s.redundancyScoreMap.Store(&red)
}

// UsefulnessAxesComputed reports whether the structural-axis recomputer has
// installed at least one snapshot (the initial synchronous prewarm stores a
// map — possibly empty — on Start, and every tick thereafter). It lets the
// enrich path tell a genuinely-isolated repeater (recomputer ran, scored it
// zero → real "F") apart from cold start (no snapshot yet → grade withheld);
// see compositeUsefulness (#1762 MAJOR-4). Either axis snapshot existing is
// sufficient — they are stored together by recomputeUsefulnessAxes.
func (s *PacketStore) UsefulnessAxesComputed() bool {
	return s.coverageScoreMap.Load() != nil || s.redundancyScoreMap.Load() != nil
}

// GetCoverageScore returns the coverage score for a pubkey in [0, 1], or 0
// if the recomputer has not run yet or the pubkey is not in the graph.
// Case-insensitive (the score map keys are lowercase).
func (s *PacketStore) GetCoverageScore(pubkey string) float64 {
	if pubkey == "" {
		return 0
	}
	snap := s.coverageScoreMap.Load()
	if snap == nil {
		return 0
	}
	return lookupUsefulnessScore(*snap, pubkey)
}

// GetCoverageScoreMap returns the current coverage snapshot (read-only by
// convention — callers MUST NOT mutate). Nil-safe.
func (s *PacketStore) GetCoverageScoreMap() map[string]float64 {
	snap := s.coverageScoreMap.Load()
	if snap == nil {
		return map[string]float64{}
	}
	return *snap
}

// GetRedundancyScore returns the redundancy (criticality) score for a
// pubkey in [0, 1], or 0 if unavailable. Case-insensitive.
func (s *PacketStore) GetRedundancyScore(pubkey string) float64 {
	if pubkey == "" {
		return 0
	}
	snap := s.redundancyScoreMap.Load()
	if snap == nil {
		return 0
	}
	return lookupUsefulnessScore(*snap, pubkey)
}

// GetRedundancyScoreMap returns the current redundancy snapshot (read-only
// by convention — callers MUST NOT mutate). Nil-safe.
func (s *PacketStore) GetRedundancyScoreMap() map[string]float64 {
	snap := s.redundancyScoreMap.Load()
	if snap == nil {
		return map[string]float64{}
	}
	return *snap
}
