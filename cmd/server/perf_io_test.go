package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPerfIOEndpoint_ReturnsValidJSON(t *testing.T) {
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
	for _, field := range []string{"readBytesPerSec", "writeBytesPerSec", "syscallsRead", "syscallsWrite"} {
		if _, ok := body[field]; !ok {
			t.Errorf("missing field %q", field)
		}
	}

	// Drive a second request so the delta-tracker emits a non-zero rate.
	// Generate a small read-bytes signal between the two reads.
	req2 := httptest.NewRequest("GET", "/api/perf/io", nil)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	var body2 map[string]interface{}
	json.Unmarshal(w2.Body.Bytes(), &body2)
	// At least one of the syscalls counters must report > 0 after activity —
	// the second call definitely happened, so the implementation should track
	// non-zero IO between samples.
	any := false
	for _, k := range []string{"readBytesPerSec", "writeBytesPerSec", "syscallsRead", "syscallsWrite"} {
		if v, ok := body2[k].(float64); ok && v > 0 {
			any = true
			break
		}
	}
	if !any {
		t.Errorf("expected at least one non-zero rate after second sample, got %v", body2)
	}
}

func TestPerfSqliteEndpoint_ReturnsValidJSON(t *testing.T) {
	_, router := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/perf/sqlite", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, field := range []string{"walSize", "pageCount", "pageSize", "cacheHitRate"} {
		if _, ok := body[field]; !ok {
			t.Errorf("missing field %q", field)
		}
	}
	// pageSize must be > 0 for any open SQLite DB
	if v, ok := body["pageSize"].(float64); !ok || v <= 0 {
		t.Errorf("expected pageSize > 0, got %v", body["pageSize"])
	}
}

func TestPerfWriteSourcesEndpoint_ReturnsSources(t *testing.T) {
	_, router := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/perf/write-sources", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "sources") {
		t.Errorf("response missing 'sources' key: %s", body)
	}
}
