package main

import (
	"sync"
	"time"
)

// repeaterEnrichmentRecomputerInterval is the default tick interval
// for the steady-state recompute of the repeater enrichment bulk
// caches. The on-request TTL fallback in repeater_enrich_bulk.go is
// kept as a safety net — the recomputer just makes sure the cache is
// populated before any request arrives.
//
// 5min mirrors the analytics_recomputer default from #1240 and is
// plenty fresh for an at-a-glance status column.
const repeaterEnrichmentRecomputerDefaultInterval = 5 * time.Minute

// repeaterEnrichmentPrewarmWait (#1008 review M1): unused stub in the
// red commit; the green commit wires it into the prewarm path so the
// recomputer waits for background indexes to be ready before snapshotting
// s.byPathHop. Declared here so the M1 red-commit test file compiles.
var repeaterEnrichmentPrewarmWait = 60 * time.Second

// StartRepeaterEnrichmentRecomputer is the steady-state background
// recompute loop for the repeater enrichment bulk caches consumed by
// handleNodes (GetRepeaterRelayInfoMap + GetRepeaterUsefulnessScoreMap).
//
// Why this exists (issue #1262): PR #1260 added a 15s-TTL bulk cache,
// but the rebuild itself runs on the request-serving goroutine on the
// first request after startup or after the TTL expires. On staging
// (75k tx, 600 nodes) that cold rebuild took 15.7s and was triggered
// by every cold SPA load via live.js's /api/nodes?limit=2000 call.
//
// On Start this does an initial synchronous compute (so the next
// request hits cache) and then ticks every `interval` to keep the
// snapshot fresh — same pattern as analytics_recomputer.go (#1240).
//
// Returns a stop closure that signals the goroutine and waits for it
// to exit (with a 5s defensive timeout).
//
// Safe to call multiple times: subsequent calls are no-ops and return
// a no-op stop closure (the original goroutine retains ownership).
func (s *PacketStore) StartRepeaterEnrichmentRecomputer(windowHours float64, interval time.Duration) func() {
	if interval <= 0 {
		interval = repeaterEnrichmentRecomputerDefaultInterval
	}

	s.repeaterEnrichRecompMu.Lock()
	if s.repeaterEnrichRecompStarted {
		s.repeaterEnrichRecompMu.Unlock()
		return func() {}
	}
	s.repeaterEnrichRecompStarted = true
	stop := make(chan struct{})
	done := make(chan struct{})
	s.repeaterEnrichRecompStop = stop
	s.repeaterEnrichRecompDone = done
	s.repeaterEnrichRecompMu.Unlock()

	// Initial synchronous prewarm — the entire point of this recomputer
	// is to make sure the very first /api/nodes?limit=2000 from
	// live.js's SPA bootstrap (issue #1262) hits a populated cache
	// instead of paying the on-thread rebuild cost.
	recomputeRepeaterEnrichmentSafe(s, windowHours)

	var stopOnce sync.Once
	go func() {
		defer close(done)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				recomputeRepeaterEnrichmentSafe(s, windowHours)
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

// recomputeRepeaterEnrichmentSafe runs both bulk-cache compute paths
// behind a panic recover — a panic in compute must not kill the
// background goroutine (the previous snapshot remains valid).
func recomputeRepeaterEnrichmentSafe(s *PacketStore, windowHours float64) {
	defer func() { _ = recover() }()
	// Write directly to the cache fields under mutex rather than going
	// through the public Get* helpers — those return the existing
	// non-nil cache immediately, so calling them here would be a no-op.
	relay := s.computeRepeaterRelayInfoMap(windowHours)
	useful := s.computeRepeaterUsefulnessScoreMap()
	now := time.Now()
	s.repeaterEnrichMu.Lock()
	s.repeaterRelayCache = relay
	s.repeaterRelayCacheWin = windowHours
	s.repeaterRelayAt = now
	s.repeaterUsefulCache = useful
	s.repeaterUsefulAt = now
	s.repeaterEnrichMu.Unlock()
}
