package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestPerfPostgresCacheIsNotPacketStoreCache(t *testing.T) {
	srv, _ := setupTestServer(t)
	get := func() map[string]any {
		w := httptest.NewRecorder()
		srv.handlePerfPostgres(w, httptest.NewRequest("GET", "/api/perf/postgres", nil))
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	srv.store.cacheMu.Lock()
	srv.store.cacheHits, srv.store.cacheMisses = 0, 1
	srv.store.cacheMu.Unlock()
	first := get()
	srv.store.cacheMu.Lock()
	srv.store.cacheHits, srv.store.cacheMisses = 1, 0
	srv.store.cacheMu.Unlock()
	second := get()
	if first["cacheHitRate"] != second["cacheHitRate"] {
		t.Fatalf("database cache metric changed with the PacketStore cache: %v -> %v", first["cacheHitRate"], second["cacheHitRate"])
	}
	if first["sampledAt"] == nil || first["sampleIntervalSeconds"] != float64(30) {
		t.Fatalf("diagnostic sample freshness is not disclosed: %v", first)
	}
}

func TestPerfPostgresUnavailableCacheIsNull(t *testing.T) {
	w := httptest.NewRecorder()
	(&Server{}).handlePerfPostgres(w, httptest.NewRequest("GET", "/api/perf/postgres", nil))
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if v, present := body["cacheHitRate"]; !present || v != nil {
		t.Fatalf("unavailable database cache metric must be null, got %v", v)
	}
	if v, present := body["databaseBytes"]; !present || v != nil {
		t.Fatalf("unavailable database size must be null, got %v", v)
	}
}

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

	// /proc/self/io only exists on Linux. When absent (e.g. some test
	// containers) we still expect well-formed JSON but skip the non-zero
	// delta assertion.
	if _, err := os.Stat("/proc/self/io"); err != nil {
		t.Skip("skip non-zero rate assertion: /proc/self/io unavailable")
	}

	// Drive a second request so the delta-tracker emits a non-zero rate.
	// Generate a small read-bytes signal between the two reads.
	req2 := httptest.NewRequest("GET", "/api/perf/io", nil)
	w2 := httptest.NewRecorder()
	router.ServeHTTP(w2, req2)
	var body2 map[string]interface{}
	json.Unmarshal(w2.Body.Bytes(), &body2)
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

func TestPerfPostgresEndpoint_ReturnsValidJSON(t *testing.T) {
	_, router := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/perf/postgres", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, field := range []string{"engine", "databaseBytes", "openConnections", "cacheHitRate"} {
		if _, ok := body[field]; !ok {
			t.Errorf("missing field %q", field)
		}
	}
	// A PostgreSQL database has a positive physical size.
	if v, ok := body["databaseBytes"].(float64); !ok || v <= 0 {
		t.Errorf("expected databaseBytes > 0, got %v", body["databaseBytes"])
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
