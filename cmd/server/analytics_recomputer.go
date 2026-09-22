// Package main: analytics recomputer (issue #1240).
//
// Steady-state background recompute loop for expensive analytics
// endpoints. Reads always hit an atomic-pointer cache; compute runs
// on a fixed ticker in a goroutine. This eliminates the on-request
// compute-then-cache pattern where the first reader after expiry pays
// the full compute cost and blocks under writer contention.
//
// See issue #1240 and AGENTS.md "Performance is a feature".
package main

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// analyticsRecomputer holds the latest snapshot of an analytics result
// in an atomic.Value, refreshed periodically by a background goroutine.
//
// Lifecycle:
//  1. Construct via newAnalyticsRecomputer(...)
//  2. Call Start() — runs initial compute synchronously, then launches
//     the recompute goroutine. Initial compute is synchronous so the
//     first Load() after Start returns never sees a nil cache.
//  3. Call Load() any number of times concurrently — never blocks
//     beyond an atomic-pointer load.
//  4. Call Stop() to terminate the background goroutine cleanly.
//
// Compute func is called WITHOUT any lock held by this struct, so it
// may freely take any application-level locks it needs.
type analyticsRecomputer struct {
	name     string
	interval time.Duration
	compute  func() interface{}

	cache        atomic.Value // holds interface{} — the latest snapshot
	stop         chan struct{}
	done         chan struct{}
	recomputeReq chan chan struct{} // RecomputeNow → loop; the loop closes the inner channel when done

	startOnce sync.Once
	stopOnce  sync.Once

	// Stats (atomic).
	computeRuns   atomic.Int64
	lastComputeNs atomic.Int64 // duration of last compute in nanoseconds

	// Issue #1659 (PR #1688 r1) — warmup gate state, inlined here so
	// hot-path readers (IsWarmingUp_1659) do lock-free atomic loads
	// only (replaces the r0 package-level map + chanLock). See
	// analytics_warmup_1659.go for full design notes.
	firstPassDoneNs atomic.Int64
	warmupStartedNs atomic.Int64
	warmupReadyGate atomic.Value // *func() bool — gate must return true for markFirstPassDone to take effect
}

// newAnalyticsRecomputer constructs an unstarted recomputer.
// interval must be > 0; compute must be non-nil.
func newAnalyticsRecomputer(name string, interval time.Duration, compute func() interface{}) *analyticsRecomputer {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &analyticsRecomputer{
		name:         name,
		interval:     interval,
		compute:      compute,
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
		recomputeReq: make(chan chan struct{}),
	}
}

// Start runs the initial compute synchronously (so the first Load
// after Start returns a populated snapshot, never nil), then launches
// a background goroutine to periodically recompute.
//
// Calling Start multiple times is a no-op after the first call.
func (r *analyticsRecomputer) Start() {
	r.startOnce.Do(func() {
		// Issue #1659 (#1688 munger #2): record warmup-start before
		// the first compute, so IsWarmingUp_1659's fallback timeout
		// is measured from "recomputer started" — not "first pass
		// returned", which never happens if compute() hangs.
		r.noteWarmupStart_1659()
		// Initial synchronous compute — first read must NOT see empty
		// or uninitialized data (acceptance criterion #1240).
		r.runOnce()
		go r.loop()
	})
}

func (r *analyticsRecomputer) loop() {
	defer close(r.done)
	t := time.NewTicker(r.interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			r.runOnce()
		case ack := <-r.recomputeReq:
			r.runOnce()
			t.Reset(r.interval)
			close(ack)
		case <-r.stop:
			return
		}
	}
}

func (r *analyticsRecomputer) runOnce() {
	if r.compute == nil {
		return
	}
	defer func() {
		// Don't let a compute panic kill the background goroutine.
		// The previous snapshot remains valid. Even on panic, we
		// still want IsWarmingUp_1659's fallback timeout to be the
		// safety net (a perpetually panicking compute would never
		// reach markFirstPassDone otherwise).
		_ = recover()
	}()
	// Sample the #1659 readiness gate BEFORE computing: a pass that
	// started on a partially loaded store must not end the warm-up,
	// even if the load finishes while it runs.
	ready := r.warmupReadyGateOpen_1659()
	t0 := time.Now()
	result := r.compute()
	r.lastComputeNs.Store(int64(time.Since(t0)))
	r.computeRuns.Add(1)
	if result != nil {
		r.cache.Store(result)
	}
	// Issue #1659: mark the first-pass clock so the warmup gate
	// in GetAnalyticsRFWithWindow / Topology / Channels handlers
	// can flip from 503-Retry-After to serving the cache.
	//
	// PR #1688 r1: called on EVERY successful pass (even nil
	// result) so a compute that returns nil but doesn't panic
	// still lifts the gate — banner-stuck-forever fix (munger #2).
	if ready {
		r.markFirstPassDone_1659()
	}
}

// RecomputeNow has the loop goroutine run a compute that starts after
// this call, and waits for it to finish. The periodic ticker restarts
// from that compute, so the next periodic pass is a full interval
// later. Returns early if the recomputer is stopped; blocks until Start
// if it has not started yet.
func (r *analyticsRecomputer) RecomputeNow() {
	ack := make(chan struct{})
	select {
	case r.recomputeReq <- ack:
	case <-r.stop:
		return
	}
	select {
	case <-ack:
	case <-r.stop:
	}
}

// recomputeWhenLoaded waits for loaded to close, then recomputes each
// recomputer once, one at a time and in slice order. Sequential so the
// post-load passes do not all hold the store read lock at once, and so
// a recomputer that reads another one's snapshot can be placed after
// it. Returns when done or when stop closes.
func recomputeWhenLoaded(loaded, stop <-chan struct{}, rcs []*analyticsRecomputer) {
	select {
	case <-loaded:
	case <-stop:
		return
	}
	t0 := time.Now()
	parts := make([]string, 0, len(rcs))
	for _, rc := range rcs {
		select {
		case <-stop:
			return
		default:
		}
		rc.RecomputeNow()
		parts = append(parts, fmt.Sprintf("%s=%s", rc.name, rc.LastComputeDuration().Round(time.Millisecond)))
	}
	log.Printf("[analytics-recompute] startup load done: recomputed %d snapshots in %s (%s)",
		len(rcs), time.Since(t0).Round(time.Millisecond), strings.Join(parts, " "))
}

// Load returns the most recently computed snapshot, or nil if Start
// has not been called (or the very first compute returned nil).
// Never blocks beyond a single atomic load.
func (r *analyticsRecomputer) Load() interface{} {
	v := r.cache.Load()
	if v == nil {
		return nil
	}
	return v
}

// Stop signals the background goroutine to exit and waits for it.
// Safe to call multiple times. Safe to call before Start (no-op).
func (r *analyticsRecomputer) Stop() {
	r.stopOnce.Do(func() {
		close(r.stop)
	})
	// Only wait if the goroutine was actually started.
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		// Defensive timeout: shouldn't happen in practice.
	}
}

// LastComputeDuration returns the duration of the most recent compute.
func (r *analyticsRecomputer) LastComputeDuration() time.Duration {
	return time.Duration(r.lastComputeNs.Load())
}

// ComputeRuns returns the total number of compute invocations.
func (r *analyticsRecomputer) ComputeRuns() int64 {
	return r.computeRuns.Load()
}

// AnalyticsRecomputeIntervals lets callers (main.go) override the
// per-endpoint recompute interval from config.json. Zero values fall
// back to the defaultInterval passed to StartAnalyticsRecomputers.
type AnalyticsRecomputeIntervals struct {
	Topology           time.Duration
	RF                 time.Duration
	Distance           time.Duration
	Channels           time.Duration
	HashCollisions     time.Duration
	HashSizes          time.Duration
	Roles              time.Duration
	ObserversClockSkew time.Duration
	NodesClockSkew     time.Duration
}

func pickInterval(override, def time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	return def
}

// analyticsRecomputersLocked lists the analytics recomputers in the
// order they start and recompute after the startup load: the three
// warm-up-gated ones first, and roles after nodes-clock-skew because
// computeAnalyticsRoles reads that recomputer's snapshot
// (GetFleetClockSkew). Caller holds analyticsRecomputerMu.
func (s *PacketStore) analyticsRecomputersLocked() []*analyticsRecomputer {
	return []*analyticsRecomputer{
		s.recompRF, s.recompTopology, s.recompChannels,
		s.recompDistance, s.recompHashCollisions, s.recompHashSizes,
		s.recompObserversClockSkew, s.recompNodesClockSkew,
		s.recompRoles,
		s.recompRetransmissions,
		s.recompDirectHeard,
	}
}

// StartAnalyticsRecomputers wires each analytics endpoint to a
// background recompute goroutine. Each runs an initial compute
// synchronously (so the first read after startup is a cache hit, never
// cold) and then refreshes on a ticker.
//
// All recomputers serve the DEFAULT query shape only: region="" and
// zero-window (no ?since= / ?until= params). Region-keyed or windowed
// queries continue to use the legacy on-request compute + TTL cache —
// the recomputer count would explode if we maintained one per
// (endpoint × region × window) combination, and region filtering is
// fast read-time work anyway.
//
// Returns a stop closure that signals all goroutines and blocks until
// they exit. Safe to call once per PacketStore. Idempotent if called
// multiple times (subsequent calls return the first stop closure).
func (s *PacketStore) StartAnalyticsRecomputers(defaultInterval time.Duration, overrides ...AnalyticsRecomputeIntervals) func() {
	if defaultInterval <= 0 {
		defaultInterval = 5 * time.Minute
	}
	var ov AnalyticsRecomputeIntervals
	if len(overrides) > 0 {
		ov = overrides[0]
	}

	s.analyticsRecomputerMu.Lock()
	if s.recompTopology != nil {
		// Already started; return a no-op so the caller's defer is harmless.
		s.analyticsRecomputerMu.Unlock()
		return func() {}
	}

	// Each recomputer wraps the underlying compute* function with the
	// default arguments. We use computeAnalytics* (not GetAnalytics*) to
	// bypass the legacy TTL cache layer — the recomputer IS the cache.
	s.recompTopology = newAnalyticsRecomputer(
		"topology", pickInterval(ov.Topology, defaultInterval),
		func() interface{} { return s.computeAnalyticsTopology("", "", TimeWindow{}) },
	)
	s.recompRF = newAnalyticsRecomputer(
		"rf", pickInterval(ov.RF, defaultInterval),
		func() interface{} { return s.computeAnalyticsRF("", "", TimeWindow{}) },
	)
	s.recompDistance = newAnalyticsRecomputer(
		"distance", pickInterval(ov.Distance, defaultInterval),
		func() interface{} { return s.computeAnalyticsDistance("", "") },
	)
	s.recompChannels = newAnalyticsRecomputer(
		"channels", pickInterval(ov.Channels, defaultInterval),
		func() interface{} { return s.computeAnalyticsChannels("", "", TimeWindow{}) },
	)
	s.recompHashCollisions = newAnalyticsRecomputer(
		"hash-collisions", pickInterval(ov.HashCollisions, defaultInterval),
		func() interface{} { return s.computeHashCollisions("", "") },
	)
	s.recompHashSizes = newAnalyticsRecomputer(
		"hash-sizes", pickInterval(ov.HashSizes, defaultInterval),
		func() interface{} { return s.computeAnalyticsHashSizesWithCapability("", "") },
	)
	s.recompRoles = newAnalyticsRecomputer(
		"roles", pickInterval(ov.Roles, defaultInterval),
		func() interface{} { return s.computeAnalyticsRoles() },
	)
	s.recompObserversClockSkew = newAnalyticsRecomputer(
		"observers-clock-skew", pickInterval(ov.ObserversClockSkew, defaultInterval),
		func() interface{} { return s.computeObserverCalibrations() },
	)
	s.recompNodesClockSkew = newAnalyticsRecomputer(
		"nodes-clock-skew", pickInterval(ov.NodesClockSkew, defaultInterval),
		func() interface{} { return s.computeFleetClockSkew() },
	)
	s.recompRetransmissions = newAnalyticsRecomputer(
		"retransmissions", defaultInterval,
		func() interface{} {
			return s.computeRetransmissionPressure("", TimeWindow{}, retransmissionDefaultBucket)
		},
	)
	// Feeds the node-health "Heard By" card. Not an analytics endpoint,
	// but it has the same shape: one full pass over the store that no
	// request can afford, served from an atomic snapshot. See
	// direct_heard.go.
	s.recompDirectHeard = newAnalyticsRecomputer(
		"direct-heard", defaultInterval,
		func() interface{} {
			idx := s.computeDirectHeard()
			s.publishDirectHeard(idx)
			return idx
		},
	)
	all := s.analyticsRecomputersLocked()
	s.analyticsRecomputerMu.Unlock()

	// Issue #1659 (PR #1688 r1, munger #5): wire the loader readiness
	// gate on the three warmup-gated recomputers (RF, Topology,
	// Channels). Only a pass that STARTS after the whole startup load
	// (hot window AND background fill) ends the warm-up. LoadComplete()
	// is not enough: it flips at the end of the hot window, so the gate
	// used to open on a snapshot that missed the background fill.
	loaded := s.StartupLoadDone()
	loadedGate := func() bool {
		select {
		case <-loaded:
			return true
		default:
			return false
		}
	}
	s.recompRF.setWarmupReadyGate_1659(loadedGate)
	s.recompTopology.setWarmupReadyGate_1659(loadedGate)
	s.recompChannels.setWarmupReadyGate_1659(loadedGate)
	s.recompRetransmissions.setWarmupReadyGate_1659(loadedGate)

	for _, rc := range all {
		rc.Start()
	}

	// main.go starts the recomputers at the first load chunk, so the
	// initial computes above only saw part of the data. Recompute as
	// soon as the load is done instead of a full interval later.
	stopPostLoad := make(chan struct{})
	postLoadDone := make(chan struct{})
	go func() {
		defer close(postLoadDone)
		recomputeWhenLoaded(loaded, stopPostLoad, all)
	}()

	var stopOnce sync.Once
	return func() {
		stopOnce.Do(func() {
			close(stopPostLoad)
			for _, rc := range all {
				rc.Stop()
			}
			<-postLoadDone
		})
	}
}
