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
	// Cross the escalation boundary multiple times within a single
	// throttle window — expect ONE reconnect, not many.
	for i := 0; i < 10; i++ {
		offset := time.Duration(disconnectedReconnectMultiplier+1)*threshold + time.Duration(i)*time.Second
		select {
		case tick <- base.Add(offset):
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
	defer close(done)

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
