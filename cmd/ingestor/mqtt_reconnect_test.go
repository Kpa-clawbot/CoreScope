package main

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// RED: per-attempt observability is required. Issue #1212 reported a prod
// outage where the broker disconnect was logged but NO subsequent reconnect
// activity was ever logged. paho's SetReconnectingHandler only fires inside
// the reconnect loop; if the loop never executes (status race, internal
// abort), operators have zero visibility. ConnectionAttemptHandler fires on
// EVERY TCP/TLS dial — both the initial Connect() and every reconnect — and
// gives an attempt counter for operators to gauge backoff progress.
func TestBuildMQTTOpts_InstrumentsConnectionAttempt(t *testing.T) {
	source := MQTTSource{Broker: "tcp://localhost:1883", Name: "test"}
	opts := buildMQTTOpts(source)

	if opts.OnConnectAttempt == nil {
		t.Fatal("OnConnectAttempt must be wired in buildMQTTOpts so every TCP/TLS dial is logged with attempt #, independent of paho's internal reconnect-loop state (#1212)")
	}
}

// RED: the watchdog acceptance criterion from #1212 — even when the client
// reports connected, if NO packets have flowed for >threshold, log a warning.
// This is a separate detection layer that catches "silently dead" sockets
// (broker accepted TCP but stopped forwarding, half-open TCP, etc.).
func TestMQTTStallWatchdog_FiresOnSilentSource(t *testing.T) {
	state := &SourceLivenessState{Tag: "test", Broker: "tcp://x:1883"}
	atomic.StoreInt64(&state.LastMessageUnix, time.Now().Add(-10*time.Minute).Unix())
	state.IsConnectedFn = func() bool { return true }

	msg, stalled := checkSourceLiveness(state, 5*time.Minute, time.Now())
	if !stalled {
		t.Fatalf("watchdog should flag stall when source connected but no message for 10m (threshold 5m); got msg=%q", msg)
	}
	if !strings.Contains(msg, "no messages") {
		t.Errorf("stall message should mention 'no messages'; got %q", msg)
	}
	if !strings.Contains(msg, "test") {
		t.Errorf("stall message should include the source tag; got %q", msg)
	}
}

func TestMQTTStallWatchdog_QuietWhenRecent(t *testing.T) {
	state := &SourceLivenessState{Tag: "test", Broker: "tcp://x:1883"}
	atomic.StoreInt64(&state.LastMessageUnix, time.Now().Add(-30*time.Second).Unix())
	state.IsConnectedFn = func() bool { return true }

	_, stalled := checkSourceLiveness(state, 5*time.Minute, time.Now())
	if stalled {
		t.Fatal("watchdog should NOT flag stall when last message was 30s ago and threshold is 5m")
	}
}

func TestMQTTStallWatchdog_QuietWhenDisconnected(t *testing.T) {
	// When disconnected, paho's own reconnect logging covers it — the
	// watchdog should only fire for the silent-while-connected case.
	state := &SourceLivenessState{Tag: "test", Broker: "tcp://x:1883"}
	atomic.StoreInt64(&state.LastMessageUnix, time.Now().Add(-1*time.Hour).Unix())
	state.IsConnectedFn = func() bool { return false }

	_, stalled := checkSourceLiveness(state, 5*time.Minute, time.Now())
	if stalled {
		t.Fatal("watchdog should NOT flag stall when client is disconnected — paho's reconnect logging covers that case")
	}
}

// snapshotAndResetRegistry isolates the package-level livenessRegistry for a
// single test. Returns a restore func to defer. Without this, parallel or
// previously-registered sources leak into the watchdog goroutine under test.
func snapshotAndResetRegistry(t *testing.T) func() {
	t.Helper()
	livenessRegistryMu.Lock()
	saved := livenessRegistry
	livenessRegistry = map[string]*SourceLivenessState{}
	livenessRegistryMu.Unlock()
	return func() {
		livenessRegistryMu.Lock()
		livenessRegistry = saved
		livenessRegistryMu.Unlock()
	}
}

// RED-then-GREEN: the watchdog GOROUTINE (not just checkSourceLiveness) must
// fan out emits across the registry on each tick, AND must exit cleanly when
// the stop signal fires. Originally runLivenessWatchdog used `for range
// t.C` — ticker.Stop() does not close the channel, so the goroutine
// leaked past shutdown. This test asserts both:
//   - tick → emit for every stalled source in the registry
//   - stop → goroutine returns within a short bound
func TestMQTTStallWatchdog_LoopEmitsAndStopsCleanly(t *testing.T) {
	defer snapshotAndResetRegistry(t)()

	s1 := &SourceLivenessState{Tag: "alpha", Broker: "tcp://a:1883", IsConnectedFn: func() bool { return true }}
	s2 := &SourceLivenessState{Tag: "beta", Broker: "tcp://b:1883", IsConnectedFn: func() bool { return true }}
	atomic.StoreInt64(&s1.LastMessageUnix, time.Now().Add(-10*time.Minute).Unix())
	atomic.StoreInt64(&s2.LastMessageUnix, time.Now().Add(-10*time.Minute).Unix())
	registerLivenessState(s1)
	registerLivenessState(s2)

	tick := make(chan time.Time, 1)
	done := make(chan struct{})

	var mu sync.Mutex
	var emits []string
	emit := func(args ...any) {
		mu.Lock()
		defer mu.Unlock()
		if len(args) > 0 {
			if s, ok := args[0].(string); ok {
				emits = append(emits, s)
			}
		}
	}

	exited := make(chan struct{})
	go func() {
		runLivenessWatchdogLoop(tick, done, 5*time.Minute, emit)
		close(exited)
	}()

	tick <- time.Now()
	// Drain: wait briefly for the emits to land. Polling instead of sleeping
	// keeps the test fast on a healthy machine.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(emits)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	got := append([]string(nil), emits...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("expected 2 stall emits (alpha+beta), got %d: %v", len(got), got)
	}

	close(done)
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog goroutine did not exit within 2s of stop — ticker leak regression")
	}
}

func TestMQTTStallWatchdog_RunStopsCleanly(t *testing.T) {
	// runLivenessWatchdog returns a stop func; calling it must halt the
	// goroutine. We can't easily observe the goroutine directly here, but
	// the very fact that we can call stop() without panic AND the package
	// test suite finishes without `-race` complaints is the contract.
	defer snapshotAndResetRegistry(t)()
	stop := runLivenessWatchdog(10*time.Millisecond, 5*time.Minute)
	time.Sleep(30 * time.Millisecond)
	stop()
	// A second stop on a stopped ticker would panic — confirm the closure
	// is single-shot in practice by NOT calling it twice. We just assert
	// stop returned.
}
