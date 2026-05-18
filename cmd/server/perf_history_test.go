package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestPerfHistoryEndpoint_ReturnsValidJSON verifies GET /api/perf/history
// responds 200 with a JSON object carrying a "samples" array.
func TestPerfHistoryEndpoint_ReturnsValidJSON(t *testing.T) {
	_, router := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/perf/history", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body PerfHistoryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	// samples must be present and a (possibly empty, non-nil) array.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, ok := raw["samples"]; !ok {
		t.Fatalf("missing \"samples\" key in response: %s", w.Body.String())
	}
	if body.Samples == nil {
		t.Errorf("samples should be a non-nil array")
	}
}

// TestPerfHistory_RingBuffer verifies collect/store populate the buffer and
// that the endpoint serves the stored samples.
func TestPerfHistory_RingBuffer(t *testing.T) {
	srv, router := setupTestServer(t)

	sample := srv.collectPerfSample()
	if sample.Ts <= 0 {
		t.Fatalf("collectPerfSample produced invalid ts %d", sample.Ts)
	}
	if sample.CpuPercent < 0 {
		t.Errorf("cpuPercent must be non-negative, got %f", sample.CpuPercent)
	}
	srv.storePerfSample(sample)

	req := httptest.NewRequest("GET", "/api/perf/history", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var body PerfHistoryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(body.Samples) != 1 {
		t.Fatalf("expected 1 sample after one store, got %d", len(body.Samples))
	}
}

// TestPerfHistory_RingBufferCap verifies storePerfSample caps the buffer at
// perfHistoryCap and drops the oldest samples.
func TestPerfHistory_RingBufferCap(t *testing.T) {
	srv, _ := setupTestServer(t)

	for i := 0; i < perfHistoryCap+50; i++ {
		srv.storePerfSample(PerfSample{Ts: int64(i)})
	}
	srv.perfHistoryMu.Lock()
	n := len(srv.perfHistory)
	oldest := srv.perfHistory[0].Ts
	srv.perfHistoryMu.Unlock()

	if n != perfHistoryCap {
		t.Fatalf("expected buffer capped at %d, got %d", perfHistoryCap, n)
	}
	// The first 50 samples (ts 0..49) should have been dropped.
	if oldest != 50 {
		t.Errorf("expected oldest ts 50 after cap, got %d", oldest)
	}
}

// TestGetCPUPercent_NonNegative verifies getCPUPercent never returns a
// negative value (on non-Linux it returns 0; on Linux it returns a real
// percentage).
func TestGetCPUPercent_NonNegative(t *testing.T) {
	if v := getCPUPercent(); v < 0 {
		t.Fatalf("getCPUPercent returned negative %f", v)
	}
	// Burn a little CPU and sample again; the result must still be non-negative.
	deadline := time.Now().Add(20 * time.Millisecond)
	x := 0
	for time.Now().Before(deadline) {
		x++
	}
	_ = x
	if v := getCPUPercent(); v < 0 {
		t.Fatalf("getCPUPercent returned negative %f after busy loop", v)
	}
}

// TestPerfEndpoint_NewFields verifies the new /api/perf fields are present.
func TestPerfEndpoint_NewFields(t *testing.T) {
	_, router := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/perf", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	for _, field := range []string{"webSocketClients", "observerCounts"} {
		if _, ok := body[field]; !ok {
			t.Errorf("missing top-level field %q in /api/perf response", field)
		}
	}

	// goRuntime must carry cpuPercent and totalSysMB.
	grRaw, ok := body["goRuntime"]
	if !ok {
		t.Fatalf("missing goRuntime in /api/perf response")
	}
	var gr map[string]json.RawMessage
	if err := json.Unmarshal(grRaw, &gr); err != nil {
		t.Fatalf("goRuntime not an object: %v", err)
	}
	for _, field := range []string{"cpuPercent", "totalSysMB"} {
		if _, ok := gr[field]; !ok {
			t.Errorf("missing goRuntime field %q", field)
		}
	}

	// observerCounts must decode to the typed struct with the four keys.
	var full PerfResponse
	if err := json.Unmarshal(w.Body.Bytes(), &full); err != nil {
		t.Fatalf("PerfResponse decode failed: %v", err)
	}
	if full.ObserverCounts == nil {
		t.Fatalf("observerCounts should not be nil")
	}
	oc := full.ObserverCounts
	if oc.Total != oc.Online+oc.Stale+oc.Offline {
		t.Errorf("observer counts inconsistent: total=%d online=%d stale=%d offline=%d",
			oc.Total, oc.Online, oc.Stale, oc.Offline)
	}
}

// TestGetObserverCounts_Thresholds verifies the online/stale/offline split
// uses the same last_seen thresholds as the observers page.
func TestGetObserverCounts_Thresholds(t *testing.T) {
	db := setupTestDB(t)
	now := time.Now().UTC()

	// online: within 10 min
	db.conn.Exec(`INSERT INTO observers (id, name, last_seen, first_seen, packet_count)
		VALUES ('on1', 'Online', ?, '2026-01-01T00:00:00Z', 1)`,
		now.Add(-2*time.Minute).Format(time.RFC3339))
	// stale: between 10 min and 1 hour
	db.conn.Exec(`INSERT INTO observers (id, name, last_seen, first_seen, packet_count)
		VALUES ('st1', 'Stale', ?, '2026-01-01T00:00:00Z', 1)`,
		now.Add(-30*time.Minute).Format(time.RFC3339))
	// offline: older than 1 hour
	db.conn.Exec(`INSERT INTO observers (id, name, last_seen, first_seen, packet_count)
		VALUES ('off1', 'Offline', ?, '2026-01-01T00:00:00Z', 1)`,
		now.Add(-3*time.Hour).Format(time.RFC3339))
	// offline: null last_seen counts as offline
	db.conn.Exec(`INSERT INTO observers (id, name, last_seen, first_seen, packet_count)
		VALUES ('off2', 'NullSeen', NULL, '2026-01-01T00:00:00Z', 1)`)
	// soft-deleted: must be excluded entirely
	db.conn.Exec(`INSERT INTO observers (id, name, last_seen, first_seen, packet_count, inactive)
		VALUES ('del1', 'Deleted', ?, '2026-01-01T00:00:00Z', 1, 1)`,
		now.Add(-1*time.Minute).Format(time.RFC3339))

	oc := db.GetObserverCounts()
	if oc.Total != 4 {
		t.Errorf("expected total 4 (soft-deleted excluded), got %d", oc.Total)
	}
	if oc.Online != 1 {
		t.Errorf("expected online 1, got %d", oc.Online)
	}
	if oc.Stale != 1 {
		t.Errorf("expected stale 1, got %d", oc.Stale)
	}
	if oc.Offline != 2 {
		t.Errorf("expected offline 2 (one old, one null), got %d", oc.Offline)
	}
}
