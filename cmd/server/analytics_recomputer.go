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

// StartAnalyticsRecomputers wires the registered analytics endpoints
// (topology, rf, distance, channels, hash-collisions, hash-sizes) to
// background recompute goroutines on the given default interval.
// Returns a stop function that signals all goroutines and waits for
// clean exit. Safe to call once per PacketStore.
//
// RED COMMIT STUB: returns a no-op stop closure without wiring any
// recompute. The latency test in analytics_recomputer_test.go must
// FAIL on this stub (proving the test gates the implementation). The
// GREEN commit replaces this with real wiring.
func (s *PacketStore) StartAnalyticsRecomputers(_ time.Duration) func() {
	return func() {}
}
