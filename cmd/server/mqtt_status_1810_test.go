package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// PR #1810 round-1: /api/mqtt/status must surface WatchdogLastTickUnix
// and WatchdogPanicCount so external monitoring can detect both a
// wedged watchdog goroutine and a panic-looping watchdog (the loop
// stamps WatchdogLastTickUnix BEFORE per-source work, so a panic on
// every source still advances the tick — the panic counter is the
// distinguishing signal).
func TestMqttStatus_ExposesWatchdogFields_1810(t *testing.T) {
	tmp := t.TempDir()
	statsPath := filepath.Join(tmp, "ingestor-stats.json")
	t.Setenv("CORESCOPE_INGESTOR_STATS", statsPath)

	stub := map[string]any{
		"sampledAt":            "2026-06-30T12:30:00Z",
		"source_statuses":      []map[string]any{},
		"watchdogLastTickUnix": int64(1751313000),
		"watchdogPanicCount":   int64(7),
	}
	data, err := json.Marshal(stub)
	if err != nil {
		t.Fatalf("marshal stub: %v", err)
	}
	if err := os.WriteFile(statsPath, data, 0o600); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	srv := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/mqtt/status", nil)
	rec := httptest.NewRecorder()
	srv.handleMqttStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp MqttStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if resp.WatchdogLastTickUnix != 1751313000 {
		t.Errorf("WatchdogLastTickUnix = %d, want 1751313000; body=%s", resp.WatchdogLastTickUnix, rec.Body.String())
	}
	if resp.WatchdogPanicCount != 7 {
		t.Errorf("WatchdogPanicCount = %d, want 7; body=%s", resp.WatchdogPanicCount, rec.Body.String())
	}
}
