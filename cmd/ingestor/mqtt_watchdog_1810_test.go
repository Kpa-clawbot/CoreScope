package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// PR #1810 round-1 follow-ups. Tests for the must-fix findings from
// adversarial / Taleb / Kent Beck reviews on the #1749 fix:
//
//   - C5: panic-recover writes to os.Stderr, NOT log.Printf (Taleb #2).
//   - C6: WatchdogPanicCount increments on recovered panic and is
//     surfaced via the snapshot path (Taleb #3).
//   - C7: per-source jitter on escalation prevents thundering-herd
//     (Taleb #4) — two sources escalating on the same tick yield
//     different escalation gaps.
//   - B5/C3: MarkReconnected clears DisconnectedSinceUnix so the next
//     disconnect starts its escalation timer from scratch (Taleb #5).
//   - C4 rewrite: throttled escalation WARN — the escalation log line
//     is bounded by forceReconnectThrottle even across many ticks
//     past the boundary (adv #2 + Taleb #1).

// captureStderr runs fn while redirecting os.Stderr to a pipe; returns
// whatever fn wrote to stderr. log.Printf's default writer is also
// pointed at the pipe so the test can prove a string did NOT go through
// log.Printf vs DID go to os.Stderr.
func captureStderrAndLog(t *testing.T, fn func()) (stderrBytes string, logBytes string) {
	t.Helper()
	rStd, wStd, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	rLog, wLog, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origStderr := os.Stderr
	origLogOut := log.Writer()
	os.Stderr = wStd
	log.SetOutput(wLog)
	t.Cleanup(func() {
		os.Stderr = origStderr
		log.SetOutput(origLogOut)
	})

	var stdBuf, logBuf bytes.Buffer
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(&stdBuf, rStd) }()
	go func() { defer wg.Done(); _, _ = io.Copy(&logBuf, rLog) }()

	fn()

	_ = wStd.Close()
	_ = wLog.Close()
	wg.Wait()
	return stdBuf.String(), logBuf.String()
}

// C5: panic recovery message MUST go to os.Stderr directly, NOT through
// log.Printf — log's writer is the suspected cause of the panic
// (blocked log pipe / full JSON-file driver) and using it for the
// recovery message risks re-panicking on the same broken sink.
func TestWatchdog_PanicRecoverWritesToStderrNotLog_1810(t *testing.T) {
	defer snapshotAndResetRegistry(t)()

	threshold := 1 * time.Minute
	s := &SourceLivenessState{
		Tag:           "panic-sink-1810",
		Broker:        "tcp://x:1883",
		IsConnectedFn: func() bool { return true },
	}
	atomic.StoreInt64(&s.LastMessageUnix, time.Now().Add(-10*time.Minute).Unix())
	atomic.StoreInt64(&s.StartedAt, time.Now().Add(-20*time.Minute).Unix())
	if err := registerLivenessState(s); err != nil {
		t.Fatalf("setup: %v", err)
	}

	emit := func(args ...any) {
		panic("synthetic panic to drive recover")
	}

	stderrOut, logOut := captureStderrAndLog(t, func() {
		tick := make(chan time.Time)
		done := make(chan struct{})
		exited := make(chan struct{})
		go func() {
			defer func() { _ = recover(); close(exited) }()
			runLivenessWatchdogLoop(tick, done, threshold, emit)
		}()
		tick <- time.Now()
		time.Sleep(150 * time.Millisecond)
		close(done)
		<-exited
	})

	if !strings.Contains(stderrOut, "WATCHDOG RECOVERED") {
		t.Fatalf("expected WATCHDOG RECOVERED line on os.Stderr; stderr=%q log=%q", stderrOut, logOut)
	}
	if strings.Contains(logOut, "WATCHDOG RECOVERED") {
		t.Fatalf("recovery message MUST NOT go through log.Printf (#1810 Taleb #2); log=%q", logOut)
	}
}

// C6: WatchdogPanicCount must increment on a recovered panic and the
// value must be reachable via the package-level accessor (which the
// stats snapshot reads).
func TestWatchdog_PanicCountIncrementsOnRecover_1810(t *testing.T) {
	defer snapshotAndResetRegistry(t)()

	before := WatchdogPanicCount()

	threshold := 1 * time.Minute
	s := &SourceLivenessState{
		Tag:           "panic-count-1810",
		Broker:        "tcp://x:1883",
		IsConnectedFn: func() bool { return true },
	}
	atomic.StoreInt64(&s.LastMessageUnix, time.Now().Add(-10*time.Minute).Unix())
	atomic.StoreInt64(&s.StartedAt, time.Now().Add(-20*time.Minute).Unix())
	if err := registerLivenessState(s); err != nil {
		t.Fatalf("setup: %v", err)
	}
	emit := func(args ...any) { panic("boom") }

	// Suppress stderr output during the recover so test output stays clean.
	_, _ = captureStderrAndLog(t, func() {
		tick := make(chan time.Time)
		done := make(chan struct{})
		exited := make(chan struct{})
		go func() {
			defer func() { _ = recover(); close(exited) }()
			runLivenessWatchdogLoop(tick, done, threshold, emit)
		}()
		tick <- time.Now()
		time.Sleep(150 * time.Millisecond)
		close(done)
		<-exited
	})

	after := WatchdogPanicCount()
	if after <= before {
		t.Fatalf("WatchdogPanicCount must advance on recovered panic (#1810 Taleb #3); before=%d after=%d", before, after)
	}
}

// C7: per-source jitter — two sources crossing the escalation boundary
// on the same tick must NOT escalate at the same threshold gap value.
// This proves the hash-based jitter offset spreads escalations and
// avoids a thundering-herd reconnect when N sources share an upstream
// broker outage.
func TestWatchdog_EscalationJitterPerSource_1810(t *testing.T) {
	threshold := 60 * time.Second
	gapA := disconnectedEscalationGap("source-a", threshold)
	gapB := disconnectedEscalationGap("source-b", threshold)
	if gapA == gapB {
		t.Fatalf("expected per-source jitter on escalation gap (#1810 Taleb #4); gapA=%s gapB=%s", gapA, gapB)
	}
	// Both must be at least the unjittered base.
	base := time.Duration(disconnectedReconnectMultiplier) * threshold
	if gapA < base || gapB < base {
		t.Fatalf("escalation gap must be ≥ base (%s); gapA=%s gapB=%s", base, gapA, gapB)
	}
	// Jitter must be bounded (≤ base + 30s sanity).
	maxJitter := base + 30*time.Second
	if gapA > maxJitter || gapB > maxJitter {
		t.Fatalf("escalation jitter exceeded bound; gapA=%s gapB=%s max=%s", gapA, gapB, maxJitter)
	}
}

// C3 / B5: MarkReconnected must clear DisconnectedSinceUnix. On unfixed
// code, a post-recovery LivenessDisconnected immediately satisfies
// (now - DisconnectedSinceUnix > multiplier×threshold) and force-
// reconnects on the FIRST tick — making escalation a flap-trigger
// instead of a persistent-failure-trigger.
func TestMarkReconnected_ClearsDisconnectedSinceUnix_1810(t *testing.T) {
	s := &SourceLivenessState{Tag: "recovery-clear-1810"}
	atomic.StoreInt64(&s.DisconnectedSinceUnix, time.Now().Add(-1*time.Hour).Unix())
	s.MarkReconnected(time.Now())
	if got := atomic.LoadInt64(&s.DisconnectedSinceUnix); got != 0 {
		t.Fatalf("MarkReconnected must clear DisconnectedSinceUnix (#1810 Taleb #5); got %d", got)
	}
}

// C4 (rewrite): the ESCALATION WARN log line must be throttled. On
// unfixed code (round 0) every tick past the boundary emitted the WARN;
// for a 1m scan interval and a 1h outage that's 55+ duplicates. This
// test does NOT pre-stamp DisconnectedSinceUnix — it starts from the
// first-observation branch and drives ticks past the boundary,
// asserting both that reconnects are throttled AND that the escalation
// WARN log count stays bounded.
func TestWatchdog_EscalationWarnThrottled_1810(t *testing.T) {
	defer snapshotAndResetRegistry(t)()

	threshold := 1 * time.Second
	var reconnectCount atomic.Int32
	s := &SourceLivenessState{
		Tag:              "warn-throttle-1810",
		Broker:           "tcp://x:1883",
		IsConnectedFn:    func() bool { return false },
		ForceReconnectFn: func() { reconnectCount.Add(1) },
	}
	if err := registerLivenessState(s); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var mu sync.Mutex
	var warnCount int
	emit := func(args ...any) {
		mu.Lock()
		defer mu.Unlock()
		for _, a := range args {
			if str, ok := a.(string); ok && strings.Contains(str, "WATCHDOG ESCALATION") {
				warnCount++
			}
		}
	}

	tick := make(chan time.Time)
	done := make(chan struct{})
	defer close(done)
	go runLivenessWatchdogLoop(tick, done, threshold, emit)

	base := time.Now()
	// First tick: stamps DisconnectedSinceUnix (no escalation yet).
	tick <- base
	// Subsequent ticks: drive 60 ticks at 1s each, far past the
	// escalation boundary (5×threshold = 5s + ≤30s jitter). On
	// unfixed code this would emit 50+ WARN lines.
	for i := 1; i < 60; i++ {
		tick <- base.Add(time.Duration(i) * time.Second)
	}
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	got := warnCount
	mu.Unlock()
	// One escalation per throttle window. With throttle=60s and the
	// test spanning ~59s of fabricated wall clock, at most ~2 WARNs
	// should fire (boundary cross + possibly one more if jitter is
	// minimal). 5 is a generous upper bound; the original bug
	// produced 50+.
	if got > 5 {
		t.Fatalf("escalation WARN must be throttled (#1810 adv #2 / Taleb #1); got %d emits across 60 ticks past the boundary", got)
	}
	if got < 1 {
		t.Fatalf("escalation WARN must fire at least once when paho stays disconnected past boundary; got 0")
	}
	if rc := reconnectCount.Load(); rc < 1 {
		t.Fatalf("ForceReconnectFn must be invoked at least once; got %d", rc)
	}
}

// C2: round-trip — IngestorStatsSnapshot.WatchdogLastTickUnix and
// WatchdogPanicCount must serialize through JSON and deserialize via
// the server's envelope shape.
func TestIngestorStatsSnapshot_WatchdogFieldsRoundTrip_1810(t *testing.T) {
	snap := IngestorStatsSnapshot{
		SampledAt:            time.Now().UTC().Format(time.RFC3339),
		WatchdogLastTickUnix: 1700000000,
		WatchdogPanicCount:   42,
	}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !bytes.Contains(b, []byte(`"watchdogLastTickUnix":1700000000`)) {
		t.Fatalf("watchdogLastTickUnix missing from JSON: %s", string(b))
	}
	if !bytes.Contains(b, []byte(`"watchdogPanicCount":42`)) {
		t.Fatalf("watchdogPanicCount missing from JSON: %s", string(b))
	}
	var back IngestorStatsSnapshot
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.WatchdogLastTickUnix != 1700000000 || back.WatchdogPanicCount != 42 {
		t.Fatalf("round-trip mismatch: %+v", back)
	}
}
