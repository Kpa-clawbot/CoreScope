package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Issue #1335 — staging's lincomatic source stalls: paho reports
// IsConnected==true but no messages arrive for 1h+. The PR #1216
// watchdog DETECTS this (LivenessStalled) but only LOGS — it never
// forces paho to drop the half-open TCP socket and reconnect, so the
// source stays silently broken until container restart.
//
// Fix: on transition INTO LivenessStalled, invoke a per-source
// ForceReconnectFn (wired in main.go to client.Disconnect(250) +
// client.Connect()). Throttled by forceReconnectThrottle so a
// stall→reconnect→re-stall loop self-recovers without hammering the
// broker.

// RED on master: ForceReconnectFn is never invoked because the
// transition engine does not call it. After the fix, the WARN edge on
// LivenessStalled MUST fire force-reconnect exactly once.
func TestMQTTStallWatchdog_ForceReconnectOnStallEdge(t *testing.T) {
	defer snapshotAndResetRegistry(t)()

	now := time.Now()
	var reconnectCount atomic.Int32
	s := &SourceLivenessState{
		Tag:              "stalled-half-open",
		Broker:           "tcp://halfopen.example:1883",
		IsConnectedFn:    func() bool { return true },
		ForceReconnectFn: func() { reconnectCount.Add(1) },
	}
	atomic.StoreInt64(&s.LastMessageUnix, now.Add(-10*time.Minute).Unix())
	atomic.StoreInt64(&s.StartedAt, now.Add(-20*time.Minute).Unix())
	if err := registerLivenessState(s); err != nil {
		t.Fatalf("setup: %v", err)
	}

	var mu sync.Mutex
	var emits []string
	emit := func(args ...any) {
		mu.Lock()
		defer mu.Unlock()
		if len(args) > 0 {
			if str, ok := args[0].(string); ok {
				emits = append(emits, str)
			}
		}
	}

	processLivenessTransition(s, LivenessStalled, "10m silent", now, emit)

	// ForceReconnectFn runs in a goroutine (the production code can't
	// block the watchdog tick on a slow Disconnect+Connect). Wait
	// briefly for it to land before asserting.
	waitForReconnect(t, &reconnectCount, 1, 2*time.Second)

	if got := reconnectCount.Load(); got != 1 {
		t.Fatalf("LivenessStalled transition MUST force-reconnect exactly once; got %d invocations (emits=%v)", got, emits)
	}
}

// Throttle: a second LivenessStalled transition within the throttle
// window MUST NOT fire a second reconnect (no broker hammering).
func TestMQTTStallWatchdog_ForceReconnectThrottled(t *testing.T) {
	defer snapshotAndResetRegistry(t)()

	now := time.Now()
	var reconnectCount atomic.Int32
	s := &SourceLivenessState{
		Tag:              "throttled",
		Broker:           "tcp://x:1883",
		IsConnectedFn:    func() bool { return true },
		ForceReconnectFn: func() { reconnectCount.Add(1) },
	}
	if err := registerLivenessState(s); err != nil {
		t.Fatalf("setup: %v", err)
	}

	emit := func(args ...any) {}

	// First stall edge → fires.
	processLivenessTransition(s, LivenessStalled, "stall 1", now, emit)
	waitForReconnect(t, &reconnectCount, 1, 2*time.Second)
	// Simulate paho reconnect cycle: MarkReconnected clears the alert
	// cooldown, then the source goes stalled again 5s later.
	s.MarkReconnected(now.Add(5 * time.Second))
	processLivenessTransition(s, LivenessStalled, "stall 2", now.Add(10*time.Second), emit)
	// Give a stray goroutine a chance to land (it shouldn't, due to throttle).
	time.Sleep(100 * time.Millisecond)

	if got := reconnectCount.Load(); got != 1 {
		t.Fatalf("force-reconnect MUST be throttled within %s; got %d invocations", forceReconnectThrottle, got)
	}

	// After the throttle window, a fresh stall edge MAY fire again.
	s.MarkReconnected(now.Add(30 * time.Second))
	processLivenessTransition(s, LivenessStalled, "stall 3", now.Add(forceReconnectThrottle+30*time.Second), emit)
	waitForReconnect(t, &reconnectCount, 2, 2*time.Second)
	if got := reconnectCount.Load(); got != 2 {
		t.Fatalf("after throttle window, force-reconnect must re-arm; got %d invocations", got)
	}
}

// NeverReceived (cold-start ACL-deny / never-flowed) MUST NOT
// force-reconnect. A SUBSCRIBE ACL deny is not fixed by a new TCP
// socket; reconnecting just churns the broker. Operators get the
// distinct "NEVER received" alarm so they can address the ACL.
func TestMQTTStallWatchdog_NoForceReconnectOnNeverReceived(t *testing.T) {
	defer snapshotAndResetRegistry(t)()

	now := time.Now()
	var reconnectCount atomic.Int32
	s := &SourceLivenessState{
		Tag:              "acl-denied",
		Broker:           "tcp://x:1883",
		IsConnectedFn:    func() bool { return true },
		ForceReconnectFn: func() { reconnectCount.Add(1) },
	}
	if err := registerLivenessState(s); err != nil {
		t.Fatalf("setup: %v", err)
	}

	emit := func(args ...any) {}
	processLivenessTransition(s, LivenessNeverReceived, "no msgs ever", now, emit)
	// Settle any (incorrect) goroutine before counting.
	time.Sleep(100 * time.Millisecond)

	if got := reconnectCount.Load(); got != 0 {
		t.Fatalf("LivenessNeverReceived must NOT force-reconnect (likely ACL deny — TCP churn won't help); got %d invocations", got)
	}
}

// Safety: a source with no ForceReconnectFn wired (e.g. tests, or a
// source registered before the wiring was added) MUST NOT panic when
// LivenessStalled fires.
func TestMQTTStallWatchdog_NilForceReconnectFnIsSafe(t *testing.T) {
	defer snapshotAndResetRegistry(t)()

	now := time.Now()
	s := &SourceLivenessState{
		Tag:           "no-reconnect-fn",
		Broker:        "tcp://x:1883",
		IsConnectedFn: func() bool { return true },
		// ForceReconnectFn deliberately nil.
	}
	if err := registerLivenessState(s); err != nil {
		t.Fatalf("setup: %v", err)
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil ForceReconnectFn must be a safe no-op; panicked: %v", r)
		}
	}()
	processLivenessTransition(s, LivenessStalled, "stalled", now, func(args ...any) {})
}

// waitForReconnect polls reconnectCount until it reaches `want` or the
// deadline elapses. ForceReconnectFn runs in a goroutine in production
// (Disconnect+Connect can block on broker IO), so tests can't read the
// counter synchronously.
func waitForReconnect(t *testing.T, count *atomic.Int32, want int32, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if count.Load() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}
