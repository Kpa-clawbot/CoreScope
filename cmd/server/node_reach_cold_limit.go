package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// Bounded concurrency for cold-cache reach reports.
//
// /api/nodes/{pubkey}/reach collapses a thundering herd on one cache key with
// singleflight, but distinct (pubkey, days) keys are independent: N cold keys
// arriving together would run N windowed observation scans at once. The read
// pool is 4 connections (OpenDB), so a handful of concurrent cold scans could
// hold every connection and leave /api/nodes and /api/packets queueing behind
// them. At most reachColdBuildLimit cold scans run at once, which keeps half
// of the pool free for every other endpoint.
//
// At most reachColdWaiterLimit distinct cold build leaders may queue for up
// to reachColdBuildWait; excess leaders are shed immediately with 429 +
// Retry-After. The wait costs a parked goroutine and timer, not a database
// connection. The ceiling is two active scans plus eight queued leaders per
// Server, independent of request rate. Same-key singleflight followers share
// their leader's admission; this does not cap those pre-existing followers.
//
// Design and tests by dborup (dborup/CoreScope@195ec4c3), reviewing #2127.
//
// Vars (not consts) so tests can tighten the cap and the queue wait without
// standing up slow scans, the same pattern as reachScanRowLimit.
var (
	// reachColdBuildLimit is how many cold reach reports may compute at once.
	reachColdBuildLimit = 2
	// reachColdBuildWait is how long a cold request queues for a slot before
	// it is shed with 429.
	reachColdBuildWait = 3 * time.Second
)

// reachColdRetryAfterSeconds is the Retry-After sent with a shed request.
// Short on purpose: a slot frees as soon as one in-flight scan finishes.
const reachColdRetryAfterSeconds = 2

// reachColdWaiterLimit is a fixed internal default, not operator/customizer
// configuration. It bounds distinct leaders waiting for a build slot.
const reachColdWaiterLimit = 8

// errReachColdBusy is returned by reachColdLimiter.acquire when the limiter
// has a full queue or stayed saturated for the whole queue wait. The handler
// maps it to 429.
// singleflight does not keep errors, so the next request recomputes.
var errReachColdBusy = errors.New("reach: cold-scan concurrency limit reached")

// reachColdLimiter is a counting semaphore with a bounded queue count/wait, plus
// counters read by the tests. Usable as a zero value: the slot channel is
// built on first use, so every Server (including the ones tests assemble by
// hand) gets the limit without a constructor.
type reachColdLimiter struct {
	mu     sync.Mutex
	sem    chan struct{} // buffered to the limit; one slot per in-flight scan
	queued chan struct{} // one admission per distinct leader waiting for a slot
	lim    int           // <=0 → reachColdBuildLimit
	wait   time.Duration // <=0 → reachColdBuildWait
	hook   func()        // test injection, run just after a slot is taken

	inflight atomic.Int64  // slots currently held
	peak     atomic.Int64  // high-water mark of inflight
	acquired atomic.Uint64 // slots handed out
	rejected atomic.Uint64 // requests shed with errReachColdBusy
	canceled atomic.Uint64 // waiters whose context ended while queued
}

// configure replaces the limiter's capacity, queue wait and acquire hook.
// Test setup only: call it before any request can acquire, since holders
// release into the channel it installs. limit <= 0 and wait <= 0 fall back to
// the package defaults; hook is nil in production.
func (l *reachColdLimiter) configure(limit int, wait time.Duration, hook func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lim, l.wait, l.hook, l.sem, l.queued = limit, wait, hook, nil, nil
}

// slots returns the lazily built slot and queue channels, wait and hook.
func (l *reachColdLimiter) slots() (chan struct{}, chan struct{}, time.Duration, func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.sem == nil {
		n := l.lim
		if n <= 0 {
			n = reachColdBuildLimit
		}
		if n <= 0 {
			n = 1
		}
		l.sem = make(chan struct{}, n)
		l.queued = make(chan struct{}, reachColdWaiterLimit)
	}
	w := l.wait
	if w <= 0 {
		w = reachColdBuildWait
	}
	return l.sem, l.queued, w, l.hook
}

// acquire takes a slot for one cold reach build. It returns nil when a slot
// was taken (the caller MUST release it), ctx.Err() when the request went
// away first, or errReachColdBusy when the queue is full or the limiter stayed
// saturated for the whole queue wait.
func (l *reachColdLimiter) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sem, queued, wait, hook := l.slots()
	// Fast path: a free slot is taken without arming a timer.
	select {
	case sem <- struct{}{}:
		l.took(hook)
		return nil
	default:
	}
	// Bound waiting leaders before allocating a timer. Every exit below gives
	// back the admission, including promotion to an active scan.
	select {
	case queued <- struct{}{}:
		defer func() { <-queued }()
	default:
		l.rejected.Add(1)
		return errReachColdBusy
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case sem <- struct{}{}:
		l.took(hook)
		return nil
	case <-ctx.Done():
		l.canceled.Add(1)
		return ctx.Err()
	case <-t.C:
		l.rejected.Add(1)
		return errReachColdBusy
	}
}

// took records a handed-out slot and runs the test hook, if any, outside the
// limiter's mutex.
func (l *reachColdLimiter) took(hook func()) {
	l.acquired.Add(1)
	n := l.inflight.Add(1)
	for {
		p := l.peak.Load()
		if n <= p || l.peak.CompareAndSwap(p, n) {
			break
		}
	}
	if hook != nil {
		hook()
	}
}

// release hands a slot back. Call it exactly once per successful acquire (the
// handler defers it). Never blocks, so an unbalanced release cannot wedge a
// request.
func (l *reachColdLimiter) release() {
	l.inflight.Add(-1)
	sem, _, _, _ := l.slots()
	select {
	case <-sem:
	default:
	}
}

func (l *reachColdLimiter) peakInflight() int64   { return l.peak.Load() }
func (l *reachColdLimiter) acquiredCount() uint64 { return l.acquired.Load() }
func (l *reachColdLimiter) rejectedCount() uint64 { return l.rejected.Load() }
func (l *reachColdLimiter) canceledCount() uint64 { return l.canceled.Load() }

// writeReachColdBusy429 sheds a cold reach request that could not get a slot.
// 429 (not 503) because the server is healthy and the work is rate-limited;
// Retry-After tells a well-behaved client how long to back off.
func writeReachColdBusy429(w http.ResponseWriter) {
	ra := strconv.Itoa(reachColdRetryAfterSeconds)
	w.Header().Set("Retry-After", ra)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	_, _ = w.Write([]byte(`{"error":"reach is busy computing other reports","retryAfter":` + ra + `}`))
}
