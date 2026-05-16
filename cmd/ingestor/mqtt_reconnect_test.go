package main

import (
	"strings"
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
