package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestReadIngestorIOSample_FileMissing — negative path: stats file absent
// must produce a nil sample (and the /api/perf/io endpoint must omit the
// ingestor block). Issue #1167 must-fix #4.
func TestReadIngestorIOSample_FileMissing(t *testing.T) {
	t.Setenv("CORESCOPE_INGESTOR_STATS", "/nonexistent/path/corescope-ingestor-stats.json")
	if got := readIngestorIOSample(); got != nil {
		t.Fatalf("expected nil for missing file, got %+v", got)
	}

	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/perf/io", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, ok := body["ingestor"]; ok {
		t.Errorf("expected NO ingestor block when stats file missing, got: %v", body["ingestor"])
	}
}

// TestReadIngestorIOSample_Unparseable — negative path: malformed JSON must
// produce nil. Issue #1167 must-fix #4.
func TestReadIngestorIOSample_Unparseable(t *testing.T) {
	dir := t.TempDir()
	statsPath := filepath.Join(dir, "ingestor-stats.json")
	if err := os.WriteFile(statsPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORESCOPE_INGESTOR_STATS", statsPath)

	if got := readIngestorIOSample(); got != nil {
		t.Fatalf("expected nil for unparseable JSON, got %+v", got)
	}
}

// TestReadIngestorIOSample_StaleBeyondThreshold — freshness guard: a snapshot
// whose sampledAt is older than the staleness threshold (5×default writer
// interval = 5s; we use 5 minutes here for clear margin) MUST be dropped, not
// served as live ingestor I/O. Issue #1167 must-fix #1.
func TestReadIngestorIOSample_StaleBeyondThreshold(t *testing.T) {
	dir := t.TempDir()
	statsPath := filepath.Join(dir, "ingestor-stats.json")
	staleAt := time.Now().UTC().Add(-5 * time.Minute).Format(time.RFC3339)
	stub := `{
		"sampledAt": "` + staleAt + `",
		"tx_inserted": 0,
		"backfillUpdates": {},
		"procIO": {
			"readBytesPerSec": 100,
			"writeBytesPerSec": 200,
			"cancelledWriteBytesPerSec": 0,
			"syscallsRead": 5,
			"syscallsWrite": 6,
			"sampledAt": "` + staleAt + `"
		}
	}`
	if err := os.WriteFile(statsPath, []byte(stub), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORESCOPE_INGESTOR_STATS", statsPath)

	if got := readIngestorIOSample(); got != nil {
		t.Fatalf("expected nil for stale snapshot (>threshold), got %+v", got)
	}

	// And the endpoint must omit `ingestor` entirely.
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/perf/io", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, ok := body["ingestor"]; ok {
		t.Errorf("stale ingestor must be dropped, got: %v", body["ingestor"])
	}
}

// TestReadIngestorIOSample_FreshIsServed — positive path: a snapshot with
// sampledAt <threshold old MUST still be served. Companion to the freshness
// guard test above. Issue #1167 must-fix #1.
func TestReadIngestorIOSample_FreshIsServed(t *testing.T) {
	dir := t.TempDir()
	statsPath := filepath.Join(dir, "ingestor-stats.json")
	freshAt := time.Now().UTC().Format(time.RFC3339)
	stub := `{
		"sampledAt": "` + freshAt + `",
		"tx_inserted": 0,
		"backfillUpdates": {},
		"procIO": {
			"readBytesPerSec": 100,
			"writeBytesPerSec": 200,
			"cancelledWriteBytesPerSec": 0,
			"syscallsRead": 5,
			"syscallsWrite": 6,
			"sampledAt": "` + freshAt + `"
		}
	}`
	if err := os.WriteFile(statsPath, []byte(stub), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORESCOPE_INGESTOR_STATS", statsPath)

	got := readIngestorIOSample()
	if got == nil {
		t.Fatalf("expected non-nil for fresh snapshot, got nil")
	}
	if got.WriteBytesPerSec != 200 {
		t.Errorf("expected writeBytesPerSec=200, got %v", got.WriteBytesPerSec)
	}
}
