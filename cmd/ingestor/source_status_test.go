package main

import (
	"errors"
	"testing"
	"time"
)

// TestSourceStatus_BasicLifecycle exercises the counter wiring used by
// the /api/mqtt/status server-side endpoint (#1043).
func TestSourceStatus_BasicLifecycle(t *testing.T) {
	resetSourceStatusRegistry()
	defer resetSourceStatusRegistry()

	s := RegisterSourceStatus("local", "mqtt://broker.example.com:1883")
	if s == nil {
		t.Fatal("RegisterSourceStatus returned nil")
	}
	// Re-registration is idempotent.
	if s2 := RegisterSourceStatus("local", "mqtt://other"); s2 != s {
		t.Fatal("RegisterSourceStatus not idempotent")
	}

	now := time.Unix(1_700_000_000, 0)
	s.MarkConnect(now)
	s.MarkPacket(now)
	s.MarkPacket(now.Add(1 * time.Second))
	s.MarkPacket(now.Add(2 * time.Second))

	snap := s.snapshot(now.Add(3 * time.Second))
	if !snap.Connected {
		t.Error("snapshot.Connected = false, want true after MarkConnect")
	}
	if snap.PacketsTotal != 3 {
		t.Errorf("PacketsTotal = %d, want 3", snap.PacketsTotal)
	}
	if snap.PacketsLast5m != 3 {
		t.Errorf("PacketsLast5m = %d, want 3", snap.PacketsLast5m)
	}
	if snap.ConnectCount != 1 {
		t.Errorf("ConnectCount = %d, want 1", snap.ConnectCount)
	}
	if snap.LastConnectUnix != now.Unix() {
		t.Errorf("LastConnectUnix = %d, want %d", snap.LastConnectUnix, now.Unix())
	}
	if snap.Broker != "mqtt://broker.example.com:1883" {
		t.Errorf("Broker = %q, want raw URL passthrough (server masks)", snap.Broker)
	}

	// After 5 minutes idle, sliding window must be empty.
	snap2 := s.snapshot(now.Add(6 * time.Minute))
	if snap2.PacketsLast5m != 0 {
		t.Errorf("PacketsLast5m after 6m idle = %d, want 0", snap2.PacketsLast5m)
	}
	if snap2.PacketsTotal != 3 {
		t.Errorf("PacketsTotal must be lifetime-cumulative, got %d", snap2.PacketsTotal)
	}
}

func TestSourceStatus_Disconnect(t *testing.T) {
	resetSourceStatusRegistry()
	defer resetSourceStatusRegistry()

	s := RegisterSourceStatus("disco", "mqtt://x:1883")
	now := time.Unix(1_700_000_100, 0)
	s.MarkConnect(now)
	s.MarkDisconnect(now.Add(time.Minute), nil)

	snap := s.snapshot(now.Add(2 * time.Minute))
	if snap.Connected {
		t.Error("snapshot.Connected = true after MarkDisconnect, want false")
	}
	if snap.DisconnectCount != 1 {
		t.Errorf("DisconnectCount = %d, want 1", snap.DisconnectCount)
	}
}

func TestSnapshotSourceStatuses_ReturnsAll(t *testing.T) {
	resetSourceStatusRegistry()
	defer resetSourceStatusRegistry()

	RegisterSourceStatus("a", "mqtt://a")
	RegisterSourceStatus("b", "mqtt://b")
	snaps := SnapshotSourceStatuses(time.Now())
	if len(snaps) != 2 {
		t.Errorf("len(snaps) = %d, want 2", len(snaps))
	}
}

// TestSourceStatus_MarkConnectClearsLastError asserts MarkConnect wipes
// any prior sticky error (#1682 munger r1 review). Otherwise the UI sees
// connected=true alongside a stale "connection refused" string.
func TestSourceStatus_MarkConnectClearsLastError(t *testing.T) {
	resetSourceStatusRegistry()
	defer resetSourceStatusRegistry()

	s := RegisterSourceStatus("sticky", "mqtt://x:1883")
	now := time.Unix(1_700_000_200, 0)
	s.MarkConnect(now)
	s.MarkDisconnect(now.Add(time.Second), errors.New("connection refused"))

	snap := s.snapshot(now.Add(2 * time.Second))
	if snap.LastError == "" {
		t.Fatalf("precondition: expected lastError after MarkDisconnect, got empty")
	}

	// Reconnect — lastError must clear.
	s.MarkConnect(now.Add(3 * time.Second))
	snap = s.snapshot(now.Add(4 * time.Second))
	if snap.LastError != "" {
		t.Errorf("snapshot.LastError = %q after MarkConnect, want empty (sticky-error regression)", snap.LastError)
	}
	if !snap.Connected {
		t.Errorf("snapshot.Connected = false after MarkConnect, want true")
	}
}
