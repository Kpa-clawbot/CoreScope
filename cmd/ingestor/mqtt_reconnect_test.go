package main

import (
	"bytes"
	"crypto/tls"
	"log"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// PR #1216 r1 item 5 (kent #1 / adv MAJOR-2): the original assertion was
// tautological — it only checked OnConnectAttempt != nil, which passes
// even if the handler is a no-op. This version invokes the wired handler,
// captures log output, and asserts the OBSERVABLE behaviour operators
// rely on during a #1212-class outage:
//   - the configured source tag appears in the log line
//   - the broker URL appears in the log line
//   - the per-source AttemptCount increments on every invocation (proving
//     the handler is wired to the right state, not just a stub)
//   - the tlsCfg passed in is returned unchanged (no surprise TLS rewrite)
func TestBuildMQTTOpts_InstrumentsConnectionAttempt(t *testing.T) {
	defer snapshotAndResetRegistry(t)()

	source := MQTTSource{Broker: "tcp://localhost:1883", Name: "obs-tag"}
	opts := buildMQTTOpts(source)

	if opts.OnConnectAttempt == nil {
		t.Fatal("OnConnectAttempt must be wired in buildMQTTOpts (#1212 / PR #1216 r1)")
	}

	// Register the liveness state so the handler can find it and increment
	// the attempt counter (same wiring main.go does).
	liveness := &SourceLivenessState{Tag: "obs-tag", Broker: source.Broker}
	if err := registerLivenessState(liveness); err != nil {
		t.Fatalf("test setup: registerLivenessState: %v", err)
	}

	// Capture log output via log.SetOutput. Save/restore so other tests
	// running serially don't lose their writer.
	var buf bytes.Buffer
	origOut := log.Writer()
	origFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() {
		log.SetOutput(origOut)
		log.SetFlags(origFlags)
	}()

	brokerURL, err := url.Parse(source.Broker)
	if err != nil {
		t.Fatalf("test setup: parse broker url: %v", err)
	}
	tlsIn := &tls.Config{ServerName: "sentinel.test"}

	// Invoke the handler twice — operators need to see attempt # increment
	// per dial to gauge backoff progress.
	tlsOut1 := opts.OnConnectAttempt(brokerURL, tlsIn)
	tlsOut2 := opts.OnConnectAttempt(brokerURL, tlsIn)

	if tlsOut1 != tlsIn || tlsOut2 != tlsIn {
		t.Errorf("OnConnectAttempt must pass tlsCfg through unchanged (got %p, %p; want %p)", tlsOut1, tlsOut2, tlsIn)
	}

	logOut := buf.String()
	if !strings.Contains(logOut, "obs-tag") {
		t.Errorf("log output must include the source tag for operator grep; got %q", logOut)
	}
	if !strings.Contains(logOut, source.Broker) {
		t.Errorf("log output must include the broker URL so operators can correlate against config; got %q", logOut)
	}
	if !strings.Contains(logOut, "#1") || !strings.Contains(logOut, "#2") {
		t.Errorf("log output must show attempt #1 and #2 across the two invocations (per-source counter); got %q", logOut)
	}

	if got := atomic.LoadInt64(&liveness.AttemptCount); got != 2 {
		t.Errorf("AttemptCount must increment per dial (got %d after 2 invocations, want 2)", got)
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

	msg, kind := checkSourceLiveness(state, 5*time.Minute, time.Now())
	if kind != LivenessStalled {
		t.Fatalf("watchdog should flag stall when source connected but no message for 10m (threshold 5m); got kind=%v msg=%q", kind, msg)
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

	_, kind := checkSourceLiveness(state, 5*time.Minute, time.Now())
	if kind != LivenessOK {
		t.Fatal("watchdog should NOT flag stall when last message was 30s ago and threshold is 5m")
	}
}

func TestMQTTStallWatchdog_QuietWhenDisconnected(t *testing.T) {
	// When disconnected, paho's own reconnect logging covers it — the
	// watchdog should only fire for the silent-while-connected case.
	state := &SourceLivenessState{Tag: "test", Broker: "tcp://x:1883"}
	atomic.StoreInt64(&state.LastMessageUnix, time.Now().Add(-1*time.Hour).Unix())
	state.IsConnectedFn = func() bool { return false }

	_, kind := checkSourceLiveness(state, 5*time.Minute, time.Now())
	if kind != LivenessDisconnected {
		t.Fatalf("watchdog must classify a !IsConnected source as LivenessDisconnected (silent state), not LivenessOK — r2 item 1 prevents disconnect→recovery mis-classification; got kind=%v", kind)
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

// PR #1216 r1 item 6 (kent #2 / adv MAJOR-3): the original test had no
// assertions gating behaviour — it called stop() and trusted `-race` to
// catch leaks. `-race` does NOT detect goroutine leaks. This version
// captures runtime.NumGoroutine() before/after and asserts the watchdog's
// goroutine actually exited. Allows ±1 slack for unrelated runtime
// bookkeeping (gc, finalizer).
func TestMQTTStallWatchdog_RunStopsCleanly(t *testing.T) {
	defer snapshotAndResetRegistry(t)()

	// Settle: let any prior-test goroutines finish before sampling baseline.
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()

	stop := runLivenessWatchdog(10*time.Millisecond, 5*time.Minute)
	// Let the watchdog run a few ticks so we're sure it's truly spawned.
	time.Sleep(50 * time.Millisecond)
	if mid := runtime.NumGoroutine(); mid <= before {
		t.Fatalf("watchdog goroutine did not spawn: before=%d mid=%d", before, mid)
	}

	stop()

	// Poll for the goroutine count to return to baseline (±1 slack).
	deadline := time.Now().Add(2 * time.Second)
	var after int
	for time.Now().Before(deadline) {
		runtime.Gosched()
		after = runtime.NumGoroutine()
		if after <= before+1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("watchdog goroutine leaked: before=%d after=%d (delta %d) — stop() did not signal the loop to exit", before, after, after-before)
}
