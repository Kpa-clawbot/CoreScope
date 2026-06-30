package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Issue #1749 — production CoreScope v3.9.1 experienced a complete MQTT
// ingest stall lasting 75+ minutes during which the watchdog never
// fired: no LivenessStalled, no LivenessNeverReceived, no
// LivenessDisconnected log lines, no force-reconnect attempts. Two
// distinct failure modes are addressed here:
//
//  1. Per-source paho machinery dies silently (recurrence with prod
//     wcmesh source 2026-06-30: connectCount=1, disconnectCount=1,
//     lastError="EOF", zero retries for ~18h while another source on
//     the same binary reconnected fine). The original watchdog returns
//     silently on LivenessDisconnected, trusting SetAutoReconnect(true)
//     to recover — when that trust is misplaced, there is no escalation
//     path.
//
//  2. The watchdog goroutine itself dies (3 sources going silent within
//     ~60s of each other strongly suggests a single shared dependency
//     failed, not 3 independent paho clients failing simultaneously).
//     A panic inside the emit callback (log pipe issues observed in
//     prior incidents) would kill the loop without leaving a trace.
//
// Fixes asserted here:
//   - Persistent LivenessDisconnected past disconnectedReconnectMultiplier
//     × threshold MUST trigger a forced reconnect with WARN telemetry.
//   - A panic inside emit MUST be recovered and the loop MUST continue
//     ticking.
//   - WatchdogLastTickUnix MUST advance with every tick so external
//     monitoring can detect a wedged watchdog goroutine.

// TestMQTTStallWatchdog_EscalateOnPersistentDisconnect_1749 (RED on
// master): a source that stays !IsConnected for longer than
// disconnectedReconnectMultiplier × threshold MUST be force-reconnected
// at least once. On master, processLivenessTransition returns silently
// on LivenessDisconnected — no escalation — so ForceReconnectFn is
// never invoked.
func TestMQTTStallWatchdog_EscalateOnPersistentDisconnect_1749(t *testing.T) {
	defer snapshotAndResetRegistry(t)()

	threshold := 60 * time.Second
	scanInterval := 5 * time.Millisecond

	var reconnectCount atomic.Int32
	s := &SourceLivenessState{
		Tag:              "silent-paho",
		Broker:           "ssl://mqtt2.example.com:8883",
		IsConnectedFn:    func() bool { return false }, // paho stuck disconnected
		ForceReconnectFn: func() { reconnectCount.Add(1) },
	}
	if err := registerLivenessState(s); err != nil {
		t.Fatalf("setup: %v", err)
	}

	tick := make(chan time.Time)
	done := make(chan struct{})
	defer close(done)

	exited := make(chan struct{})
	go func() {
		runLivenessWatchdogLoop(tick, done, threshold, func(args ...any) {})
		close(exited)
	}()

	// Feed ticks spanning > (multiplier × threshold) of wall clock so
	// the escalation path fires. We control the `now` parameter by
	// sending fabricated timestamps down the tick channel.
	base := time.Now()
	totalSpan := time.Duration(disconnectedReconnectMultiplier+2) * threshold
	for elapsed := time.Duration(0); elapsed <= totalSpan; elapsed += scanInterval * 200 {
		select {
		case tick <- base.Add(elapsed):
		case <-time.After(time.Second):
			t.Fatal("watchdog loop did not consume tick within 1s")
		}
	}

	// ForceReconnectFn runs in a goroutine in production; poll for the
	// counter to land.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && reconnectCount.Load() < 1 {
		time.Sleep(5 * time.Millisecond)
	}

	if got := reconnectCount.Load(); got < 1 {
		t.Fatalf("persistent LivenessDisconnected past %d×threshold MUST force-reconnect at least once (#1749); got %d invocations",
			disconnectedReconnectMultiplier, got)
	}
}

// TestMQTTStallWatchdog_DisconnectedEscalationThrottled_1749: once the
// watchdog has escalated, repeat escalations on the same source MUST be
// throttled by forceReconnectThrottle (no broker hammering during a
// prolonged outage).
func TestMQTTStallWatchdog_DisconnectedEscalationThrottled_1749(t *testing.T) {
	defer snapshotAndResetRegistry(t)()

	threshold := 60 * time.Second

	var reconnectCount atomic.Int32
	s := &SourceLivenessState{
		Tag:              "throttle-escalate",
		Broker:           "ssl://example.com:8883",
		IsConnectedFn:    func() bool { return false },
		ForceReconnectFn: func() { reconnectCount.Add(1) },
	}
	if err := registerLivenessState(s); err != nil {
		t.Fatalf("setup: %v", err)
	}

	tick := make(chan time.Time)
	done := make(chan struct{})
	defer close(done)
	go runLivenessWatchdogLoop(tick, done, threshold, func(args ...any) {})

	base := time.Now()
	// Pre-stamp DisconnectedSinceUnix so that the first tick is
	// already past the multiplier×threshold escalation boundary.
	// Without this we'd just observe the FIRST tick stamping the
	// timestamp and subsequent ticks would be only seconds past it.
	atomic.StoreInt64(&s.DisconnectedSinceUnix, base.Add(-time.Duration(disconnectedReconnectMultiplier+1)*threshold).Unix())
	// Cross the escalation boundary multiple times within a single
	// throttle window — expect ONE reconnect, not many.
	for i := 0; i < 10; i++ {
		select {
		case tick <- base.Add(time.Duration(i) * time.Second):
		case <-time.After(time.Second):
			t.Fatal("tick blocked")
		}
	}
	time.Sleep(200 * time.Millisecond)

	if got := reconnectCount.Load(); got != 1 {
		t.Fatalf("escalation must be throttled within %s; got %d invocations", forceReconnectThrottle, got)
	}
}

// TestMQTTStallWatchdog_LoopRecoversFromPanicInEmit_1749 (RED on
// master): a panic inside the emit callback MUST NOT kill the watchdog
// loop. On master there is no defer/recover around the per-source
// processLivenessTransition call, so the first panic kills the
// goroutine and no further ticks are processed.
func TestMQTTStallWatchdog_LoopRecoversFromPanicInEmit_1749(t *testing.T) {
	defer snapshotAndResetRegistry(t)()

	threshold := 1 * time.Minute

	s := &SourceLivenessState{
		Tag:           "panic-emit",
		Broker:        "tcp://x:1883",
		IsConnectedFn: func() bool { return true },
	}
	atomic.StoreInt64(&s.LastMessageUnix, time.Now().Add(-10*time.Minute).Unix())
	atomic.StoreInt64(&s.StartedAt, time.Now().Add(-20*time.Minute).Unix())
	if err := registerLivenessState(s); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var mu sync.Mutex
	var calls int
	emit := func(args ...any) {
		mu.Lock()
		calls++
		mu.Unlock()
		panic("synthetic emit panic — simulates blocked log pipe (#1749 hypothesis 2)")
	}

	tick := make(chan time.Time)
	done := make(chan struct{})

	// Wrap the loop spawn with our own recover so that an unrecovered
	// panic in the loop (the bug on master) does NOT crash the test
	// process. The bug-under-test is whether the LOOP recovers; if it
	// does not, the panic propagates up to OUR recover, this goroutine
	// exits, `exited` is closed, and the loop is dead — at which point
	// the second tick will block and we assert failure with a clear
	// message rather than tearing down the whole test binary.
	exited := make(chan struct{})
	go func() {
		defer func() {
			_ = recover() // RED-mode safety net; production loop is what we are asserting on
			close(exited)
		}()
		runLivenessWatchdogLoop(tick, done, threshold, emit)
	}()

	base := time.Now()
	// Tick 1: should hit the WARN edge, panic in emit, be recovered.
	select {
	case tick <- base:
	case <-time.After(time.Second):
		t.Fatal("first tick blocked")
	}
	// Give the goroutine a moment to recover & re-loop (or to die from
	// an unrecovered panic, which is the bug we are gating on).
	time.Sleep(100 * time.Millisecond)
	select {
	case <-exited:
		t.Fatal("watchdog loop died from panic in emit (#1749) — production loop lacks defer/recover")
	default:
	}

	// Reset the stalled state's alert so the second tick triggers
	// another emit (heartbeat suppression would otherwise mask the
	// second call). Use MarkReconnected then re-arm staleness.
	s.MarkReconnected(base.Add(50 * time.Millisecond))
	atomic.StoreInt64(&s.LastMessageUnix, base.Add(-10*time.Minute).Unix())
	atomic.StoreInt64(&s.StartedAt, base.Add(-20*time.Minute).Unix())

	// Tick 2: if the loop survived, we should get a second emit call.
	select {
	case tick <- base.Add(time.Second):
	case <-exited:
		t.Fatal("watchdog loop died from panic in emit (#1749); second tick cannot be delivered because the goroutine exited")
	case <-time.After(time.Second):
		t.Fatal("watchdog loop did not survive panic in emit (#1749); second tick blocked because the goroutine died")
	}
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	got := calls
	mu.Unlock()
	if got < 2 {
		t.Fatalf("watchdog loop must survive a panic in emit and continue ticking (#1749); emit calls=%d (expected ≥2)", got)
	}

	// Loop must still exit cleanly when signalled.
	close(done)
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("loop did not exit after done signal")
	}
}

// TestMQTTStallWatchdog_LastTickUnixExposed_1749 (RED on master):
// WatchdogLastTickUnix MUST advance with each tick so external monitoring
// can detect a wedged watchdog goroutine. On master no such clock is
// exposed.
func TestMQTTStallWatchdog_LastTickUnixExposed_1749(t *testing.T) {
	defer snapshotAndResetRegistry(t)()

	// Baseline: clock should be 0 (or stale) BEFORE the first tick of
	// this test. We can't assert exactly 0 because prior tests in the
	// same package may have ticked the loop, so just record the value
	// and assert it ADVANCES.
	before := WatchdogLastTickUnix()
	// #1810 round-1 (adv #7): this test stamps a 48h-future value into
	// the package-level watchdogLastTickUnix. Restore the prior value
	// so a downstream test that asserts "tick advanced past 'before'"
	// is not fooled by our leak.
	t.Cleanup(func() {
		watchdogLastTickUnix.Store(before)
	})

	tick := make(chan time.Time)
	done := make(chan struct{})
	defer close(done)
	go runLivenessWatchdogLoop(tick, done, time.Minute, func(args ...any) {})

	stamp := time.Now().Add(48 * time.Hour) // guaranteed > before
	select {
	case tick <- stamp:
	case <-time.After(time.Second):
		t.Fatal("tick blocked")
	}
	// The loop publishes the clock; poll for it to land.
	deadline := time.Now().Add(2 * time.Second)
	var got int64
	for time.Now().Before(deadline) {
		got = WatchdogLastTickUnix()
		if got >= stamp.Unix() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("WatchdogLastTickUnix() did not advance to tick timestamp (#1749); before=%d after=%d want≥%d",
		before, got, stamp.Unix())
}

// TestMQTTStallWatchdog_LoopRecoversFromPanicInEmit_EscalationPath_1749
// (RED on the prior commit): a panic inside emit on the ESCALATION
// path (maybeEscalateDisconnected → emit) must NOT kill the watchdog
// loop. The fix in b3c75ca3 moved maybeEscalateDisconnected INSIDE the
// per-source IIFE that has defer/recover, so a panic on this path is
// recovered alongside panics on the processLivenessTransition path.
func TestMQTTStallWatchdog_LoopRecoversFromPanicInEmit_EscalationPath_1749(t *testing.T) {
	defer snapshotAndResetRegistry(t)()

	threshold := 1 * time.Minute

	var reconnectCalls int32

	s := &SourceLivenessState{
		Tag:           "panic-escalation",
		Broker:        "tcp://x:1883",
		IsConnectedFn: func() bool { return false }, // disconnected
		ForceReconnectFn: func() {
			atomic.AddInt32(&reconnectCalls, 1)
		},
	}
	// Pre-stamp DisconnectedSinceUnix past the escalation threshold
	// (disconnectedReconnectMultiplier × threshold = 5 × 1min = 5min).
	// Set it 10 minutes in the past so escalation fires immediately.
	atomic.StoreInt64(&s.DisconnectedSinceUnix, time.Now().Add(-10*time.Minute).Unix())
	atomic.StoreInt64(&s.StartedAt, time.Now().Add(-20*time.Minute).Unix())
	if err := registerLivenessState(s); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Track which tick we're on to control panic timing.
	var tickNum int32
	var mu sync.Mutex
	var emitCalls int
	emit := func(args ...any) {
		mu.Lock()
		emitCalls++
		mu.Unlock()
		// Tick 1 (tickNum==1): all emits succeed → ForceReconnectFn fires.
		// Tick 2 (tickNum==2): panic on first emit in escalation path.
		// Tick 3: proves loop survived.
		if atomic.LoadInt32(&tickNum) == 2 {
			panic("synthetic emit panic on ESCALATION path (#1749 round-1 finding)")
		}
	}

	tick := make(chan time.Time)
	done := make(chan struct{})

	exited := make(chan struct{})
	go func() {
		defer func() {
			_ = recover()
			close(exited)
		}()
		runLivenessWatchdogLoop(tick, done, threshold, emit)
	}()

	base := time.Now()

	// Tick 1: escalation fires, ForceReconnectFn is invoked, no panic.
	atomic.StoreInt32(&tickNum, 1)
	select {
	case tick <- base:
	case <-time.After(time.Second):
		t.Fatal("tick 1 blocked")
	}
	time.Sleep(150 * time.Millisecond) // let goroutine with ForceReconnectFn run

	if rc := atomic.LoadInt32(&reconnectCalls); rc < 1 {
		t.Fatalf("ForceReconnectFn must be invoked ≥1 time after tick 1 (got %d)", rc)
	}

	// Reset state so tick 2 also triggers escalation.
	atomic.StoreInt64(&s.DisconnectedSinceUnix, base.Add(-10*time.Minute).Unix())
	atomic.StoreInt64(&s.LastForceReconnectUnix, 0)

	// Tick 2: emit panics on escalation path. On unfixed code this
	// kills the loop because maybeEscalateDisconnected is outside IIFE.
	atomic.StoreInt32(&tickNum, 2)
	select {
	case tick <- base.Add(time.Second):
	case <-time.After(time.Second):
		t.Fatal("tick 2 blocked")
	}
	time.Sleep(100 * time.Millisecond)
	select {
	case <-exited:
		t.Fatal("watchdog loop died from panic in emit on ESCALATION path (#1749 round-1) — maybeEscalateDisconnected is not panic-protected")
	default:
	}

	// Tick 3: proves loop survived the panic on tick 2.
	atomic.StoreInt32(&tickNum, 3)
	atomic.StoreInt64(&s.DisconnectedSinceUnix, base.Add(-10*time.Minute).Unix())
	atomic.StoreInt64(&s.LastForceReconnectUnix, 0)
	select {
	case tick <- base.Add(2 * time.Second):
	case <-exited:
		t.Fatal("watchdog loop died; tick 3 cannot be delivered")
	case <-time.After(time.Second):
		t.Fatal("tick 3 blocked — loop dead")
	}
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	got := emitCalls
	mu.Unlock()
	// Tick 1: 2 emits (escalation + "forcing reconnect") + 1 from goroutine = 3
	// Tick 2: 1 emit (panic) = partial
	// Tick 3: ≥1 emit
	// Total should be ≥4 if loop survived
	if got < 4 {
		t.Fatalf("emit must be called ≥4 times across 3 ticks (got %d) — loop did not survive escalation panic", got)
	}

	close(done)
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("loop did not exit after done signal")
	}
}
