// Package main: issue #1659 — analytics warmup gating.
//
// Problem: after server restart, recompRF (and recompTopology /
// recompChannels) cache the FIRST computation, which immediately after
// boot is just the small in-RAM-observations slice (background
// chunk-loader has not yet backfilled history). The recomputer then
// serves that small slice from GetAnalyticsRFWithWindow's default
// shortcut for an entire recompute interval, while the client pins it
// via CLIENT_TTL.analyticsRF. UX: cards show a tiny "post-restart"
// window even when the user selects "All data".
//
// Fix: each analyticsRecomputer carries a firstPassDoneAt timestamp
// set ONLY after its first full-range compute completes. Handlers for
// the default-shape (region="" && area="" && window.IsZero()) request
// detect IsWarmingUp() and return 503 + Retry-After: 5 with
// {"error":"analytics warming up","retry_after_s":5} until the gate
// opens. Windowed / region-filtered requests bypass the recomputer
// already, so they are unaffected.
//
// Tests: analytics_warmup_1659_test.go.
package main

import (
	"net/http"
	"sync/atomic"
	"time"
)

// firstPassDoneNs is stored on the recomputer as a UnixNano timestamp
// (0 = not yet done). We avoid time.Time on the hot path because
// atomic.Value with time.Time pays a per-Load allocation under race;
// nanoseconds in an int64 are lock-free reads.
type firstPassClock struct {
	firstPassDoneNs atomic.Int64
}

// recomputerWarmup is a package-level side table keyed by recomputer
// pointer. We intentionally avoid editing the analyticsRecomputer
// struct directly so the change stays surgical (single-purpose file,
// one new field group, easy to revert / amend).
//
// Access pattern: each recomputer marks its firstPassDoneNs once in
// runOnce(); handlers read it via FirstPassDoneAt_1659() /
// IsWarmingUp_1659(). No locks: per-recomputer atomic Int64.
var warmupClocks = newRecomputerWarmupMap()

type recomputerWarmupMap struct {
	// We use a simple map under sync access — the recomputer set is
	// fixed at startup, so contention is nil after that.
	m  map[*analyticsRecomputer]*firstPassClock
	mu chanLock
}

type chanLock chan struct{}

func newChanLock() chanLock {
	l := make(chanLock, 1)
	return l
}
func (l chanLock) Lock()   { l <- struct{}{} }
func (l chanLock) Unlock() { <-l }

func newRecomputerWarmupMap() *recomputerWarmupMap {
	return &recomputerWarmupMap{
		m:  make(map[*analyticsRecomputer]*firstPassClock),
		mu: newChanLock(),
	}
}

func (w *recomputerWarmupMap) clockFor(r *analyticsRecomputer) *firstPassClock {
	w.mu.Lock()
	c, ok := w.m[r]
	if !ok {
		c = &firstPassClock{}
		w.m[r] = c
	}
	w.mu.Unlock()
	return c
}

// markFirstPassDone is called from analyticsRecomputer.runOnce() after
// every successful compute. The store is idempotent (only sets when
// still zero), so the FIRST successful compute wins the timestamp; all
// later passes leave it unchanged.
func (r *analyticsRecomputer) markFirstPassDone_1659() {
	c := warmupClocks.clockFor(r)
	if c.firstPassDoneNs.Load() == 0 {
		c.firstPassDoneNs.CompareAndSwap(0, time.Now().UnixNano())
	}
}

// FirstPassDoneAt_1659 reports the time the first full compute pass
// completed. Returns zero time if no pass has completed yet.
func (r *analyticsRecomputer) FirstPassDoneAt_1659() time.Time {
	if r == nil {
		return time.Time{}
	}
	ns := warmupClocks.clockFor(r).firstPassDoneNs.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// IsWarmingUp_1659 reports true when the recomputer has not yet
// completed its first full-range pass. Handlers for the default-shape
// request must return 503 + Retry-After: 5 while this is true.
func (r *analyticsRecomputer) IsWarmingUp_1659() bool {
	if r == nil {
		// No recomputer registered → treat as ready; the handler
		// falls through to the legacy compute path.
		return false
	}
	return warmupClocks.clockFor(r).firstPassDoneNs.Load() == 0
}

// writeAnalyticsWarmup503 emits the standard warmup response. The body
// shape is documented for clients: error string + retry_after_s int.
func writeAnalyticsWarmup503(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "5")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`{"error":"analytics warming up","retry_after_s":5}`))
}

// installWarmupBlocker_1659 is a test-only helper that registers the
// RF / topology / channels recomputers with a compute function that
// blocks on the supplied channel. firstPassDoneNs therefore stays
// zero, simulating the post-restart warmup window for the warmup test.
//
// We bypass StartAnalyticsRecomputers entirely and wire the
// recomputers manually so the background goroutines never fire. The
// test only needs the *analyticsRecomputer pointers to be non-nil and
// in the warmup state.
func (s *PacketStore) installWarmupBlocker_1659(block <-chan struct{}) {
	blockCompute := func() interface{} {
		<-block
		return nil
	}
	s.analyticsRecomputerMu.Lock()
	defer s.analyticsRecomputerMu.Unlock()
	s.recompRF = newAnalyticsRecomputer("rf-test-block", time.Hour, blockCompute)
	s.recompTopology = newAnalyticsRecomputer("topo-test-block", time.Hour, blockCompute)
	s.recompChannels = newAnalyticsRecomputer("chan-test-block", time.Hour, blockCompute)
	// Do NOT call Start() — leaving firstPassDoneNs at zero is exactly
	// the warmup state the test wants to exercise.
}
