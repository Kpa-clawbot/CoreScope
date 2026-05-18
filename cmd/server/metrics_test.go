package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// expectedMetricNames are the metric names handleMetrics must emit.
var expectedMetricNames = []string{
	"corescope_packets_total",
	"corescope_transmissions_total",
	"corescope_observations_total",
	"corescope_nodes_active",
	"corescope_nodes_total",
	"corescope_observers_total",
	"corescope_packets_last_hour",
	"corescope_uptime_seconds",
	"corescope_ingestor_lag_seconds",
}

func TestMetricsEndpoint(t *testing.T) {
	_, router := setupTestServer(t)

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("expected Content-Type to start with text/plain, got %q", ct)
	}

	body := w.Body.String()

	// Each expected metric must have a value line, a # HELP line and a # TYPE line.
	for _, name := range expectedMetricNames {
		if !strings.Contains(body, "\n"+name+" ") && !strings.HasPrefix(body, name+" ") {
			t.Errorf("body missing value line for metric %q", name)
		}
		if !strings.Contains(body, "# TYPE "+name+" ") {
			t.Errorf("body missing # TYPE line for metric %q", name)
		}
		if !strings.Contains(body, "# HELP "+name+" ") {
			t.Errorf("body missing # HELP line for metric %q", name)
		}
	}

	// Every non-comment, non-blank line must be `<name> <numeric value>`.
	for i, line := range strings.Split(body, "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			t.Errorf("line %d %q: expected exactly 2 fields, got %d", i+1, line, len(fields))
			continue
		}
		if _, err := strconv.ParseFloat(fields[1], 64); err != nil {
			t.Errorf("line %d %q: value %q is not numeric: %v", i+1, line, fields[1], err)
		}
	}
}

func TestMetricsTypeDeclarations(t *testing.T) {
	_, router := setupTestServer(t)

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	body := w.Body.String()
	for _, name := range expectedMetricNames {
		hasCounter := strings.Contains(body, "# TYPE "+name+" counter")
		hasGauge := strings.Contains(body, "# TYPE "+name+" gauge")
		if !hasCounter && !hasGauge {
			t.Errorf("metric %q must declare TYPE counter or gauge", name)
		}
	}
}

// TestMetricsNoAPIKeyRequired confirms /metrics is unauthenticated: a request
// with no X-API-Key header still returns 200 even when an API key is configured.
func TestMetricsNoAPIKeyRequired(t *testing.T) {
	_, router := setupTestServerWithAPIKey(t, "test-secret-key-strong-enough")

	req := httptest.NewRequest("GET", "/metrics", nil)
	// Deliberately no X-API-Key header.
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 without API key, got %d", w.Code)
	}
}
