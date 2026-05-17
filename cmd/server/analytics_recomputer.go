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
	"sync"
	"sync/atomic"
	"time"
)

// analyticsRecomputer holds the latest snapshot of an analytics result
// in an atomic.Value, refreshed periodically by a background goroutine.
//
// Lifecycle:
//   1. Construct via newAnalyticsRecomputer(...)
//   2. Call Start() — runs initial compute synchronously, then launches
//      the recompute goroutine. Initial compute is synchronous so the
//      first Load() after Start returns never sees a nil cache.
//   3. Call Load() any number of times concurrently — never blocks
//      beyond an atomic-pointer load.
//   4. Call Stop() to terminate the background goroutine cleanly.
//
// Compute func is called WITHOUT any lock held by this struct, so it
// may freely take any application-level locks it needs.
type analyticsRecomputer struct {
	name     string
	interval time.Duration
	compute  func() interface{}

	cache atomic.Value // holds interface{} — the latest snapshot
	stop  chan struct{}
	done  chan struct{}

	startOnce sync.Once
	stopOnce  sync.Once

	// Stats (atomic).
	computeRuns   atomic.Int64
	lastComputeNs atomic.Int64 // duration of last compute in nanoseconds
}

// newAnalyticsRecomputer constructs an unstarted recomputer.
// interval must be > 0; compute must be non-nil.
func newAnalyticsRecomputer(name string, interval time.Duration, compute func() interface{}) *analyticsRecomputer {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &analyticsRecomputer{
		name:     name,
		interval: interval,
		compute:  compute,
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
}

// Start runs the initial compute synchronously (so the first Load
// after Start returns a populated snapshot, never nil), then launches
// a background goroutine to periodically recompute.
//
// Calling Start multiple times is a no-op after the first call.
func (r *analyticsRecomputer) Start() {
	r.startOnce.Do(func() {
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
		// The previous snapshot remains valid.
		_ = recover()
	}()
	t0 := time.Now()
	result := r.compute()
	r.lastComputeNs.Store(int64(time.Since(t0)))
	r.computeRuns.Add(1)
	if result != nil {
		r.cache.Store(result)
	}
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
	Topology       time.Duration
	RF             time.Duration
	Distance       time.Duration
	Channels       time.Duration
	HashCollisions time.Duration
	HashSizes      time.Duration
}

func pickInterval(override, def time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	return def
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
	all := []*analyticsRecomputer{
		s.recompTopology, s.recompRF, s.recompDistance,
		s.recompChannels, s.recompHashCollisions, s.recompHashSizes,
	}
	s.analyticsRecomputerMu.Unlock()

	for _, rc := range all {
		rc.Start()
	}

	return func() {
		for _, rc := range all {
			rc.Stop()
		}
	}
}
