package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestParseProcIO_CancelledWriteBytes verifies the parser populates
// cancelled_write_bytes from a synthetic /proc/self/io string. Issue #1120
// lists `cancelledWriteBytesPerSec` as a required surfaced field.
func TestParseProcIO_CancelledWriteBytes(t *testing.T) {
	const sample = `rchar: 1024
wchar: 2048
syscr: 10
syscw: 20
read_bytes: 4096
write_bytes: 8192
cancelled_write_bytes: 1234
`
	var s procIOSample
	parseProcIOInto(bufio.NewScanner(strings.NewReader(sample)), &s)
	if s.cancelledWrite != 1234 {
		t.Errorf("expected cancelledWrite=1234, got %d", s.cancelledWrite)
	}
	if s.readBytes != 4096 {
		t.Errorf("expected readBytes=4096, got %d", s.readBytes)
	}
}

// TestPerfIOEndpoint_ExposesCancelledWriteBytes asserts the JSON payload
// includes the cancelledWriteBytesPerSec field — this was the BLOCKER B1
// gap from PR #1123 review.
func TestPerfIOEndpoint_ExposesCancelledWriteBytes(t *testing.T) {
	_, router := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/perf/io", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, ok := body["cancelledWriteBytesPerSec"]; !ok {
		t.Errorf("missing field cancelledWriteBytesPerSec; got: %v", body)
	}
}

// TestPerfIOEndpoint_ExposesIngestorBlock writes a stub ingestor stats file
// containing a procIO block and asserts /api/perf/io surfaces it under
// `ingestor`. Issue #1120: "Both ingestor and server."
func TestPerfIOEndpoint_ExposesIngestorBlock(t *testing.T) {
	dir := t.TempDir()
	statsPath := filepath.Join(dir, "ingestor-stats.json")
	// Use a fresh sampledAt — the GREEN commit added a freshness guard
	// (#1167 must-fix #1) that drops snapshots older than ~5s. A fixed
	// date string would now incorrectly exercise the stale path.
	freshAt := time.Now().UTC().Format(time.RFC3339)
	stub := `{
		"sampledAt": "` + freshAt + `",
		"tx_inserted": 42,
		"obs_inserted": 1,
		"backfillUpdates": {},
		"procIO": {
			"readBytesPerSec": 100,
			"writeBytesPerSec": 200,
			"cancelledWriteBytesPerSec": 50,
			"syscallsRead": 5,
			"syscallsWrite": 6,
			"sampledAt": "` + freshAt + `"
		}
	}`
	if err := os.WriteFile(statsPath, []byte(stub), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORESCOPE_INGESTOR_STATS", statsPath)

	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/perf/io", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	ing, ok := body["ingestor"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected ingestor block in response, got: %v", body)
	}
	if v, ok := ing["writeBytesPerSec"].(float64); !ok || v != 200 {
		t.Errorf("expected ingestor.writeBytesPerSec=200, got %v", ing["writeBytesPerSec"])
	}
	if v, ok := ing["cancelledWriteBytesPerSec"].(float64); !ok || v != 50 {
		t.Errorf("expected ingestor.cancelledWriteBytesPerSec=50, got %v", ing["cancelledWriteBytesPerSec"])
	}
}
