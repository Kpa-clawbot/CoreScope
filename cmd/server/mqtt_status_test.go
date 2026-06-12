package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMqttStatus_MasksBrokerPassword (#1043) asserts the /api/mqtt/status
// handler never leaks the broker password embedded in a mqtt:// URL.
// Operators viewing the API response (or the Observers panel that
// consumes it) must see `****` in place of the inline credential.
//
// Test shape: write a stub ingestor stats file with one source whose
// broker URL contains a plaintext password, invoke the handler, assert
// the JSON response (a) contains the username + host, (b) does NOT
// contain the password substring.
func TestMqttStatus_MasksBrokerPassword(t *testing.T) {
	const password = "hunter2supersecret"
	const rawBroker = "mqtt://obsuser:" + password + "@broker.example.com:1883"

	tmp := t.TempDir()
	statsPath := filepath.Join(tmp, "ingestor-stats.json")
	t.Setenv("CORESCOPE_INGESTOR_STATS", statsPath)

	// Stub stats file: one MQTT source with a credentialed broker URL.
	stub := map[string]any{
		"sampledAt": "2026-06-12T12:30:00Z",
		"source_statuses": []map[string]any{{
			"name":            "local",
			"broker":          rawBroker,
			"connected":       true,
			"lastPacketUnix":  1717977000,
			"connectCount":    1,
			"disconnectCount": 0,
			"packetsTotal":    42,
			"packetsLast5m":   7,
		}},
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
	body := rec.Body.String()
	t.Logf("response body: %s", body)

	if strings.Contains(body, password) {
		t.Errorf("response leaks broker password %q in body: %s", password, body)
	}
	// Sanity: the response still identifies the source by name + host.
	if !strings.Contains(body, "broker.example.com") {
		t.Errorf("response missing broker host: %s", body)
	}
	if !strings.Contains(body, "obsuser") {
		t.Errorf("response missing broker username: %s", body)
	}
	// Mask token must be present so operators can tell credentials were
	// redacted vs the broker URL never having a password to begin with.
	if !strings.Contains(body, "****") {
		t.Errorf("response missing redaction marker '****': %s", body)
	}
}

// TestMqttStatus_EmptyWhenNoStatsFile asserts the handler returns an empty
// list (200 OK) when the ingestor stats file is missing — the UI panel
// renders a "no data yet" state in that case.
func TestMqttStatus_EmptyWhenNoStatsFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("CORESCOPE_INGESTOR_STATS", filepath.Join(tmp, "does-not-exist.json"))

	srv := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/mqtt/status", nil)
	rec := httptest.NewRecorder()
	srv.handleMqttStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp MqttStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, rec.Body.String())
	}
	if len(resp.Sources) != 0 {
		t.Errorf("Sources len = %d, want 0", len(resp.Sources))
	}
}

// TestMaskBrokerURL_Patterns is a unit table-driven test for the masking
// helper. Kept separate from the handler test so a regression in the
// regex localizes immediately. NOTE: the helper does not yet exist in
// the red commit (handler uses inline passthrough); this test fails to
// compile until the green commit introduces maskBrokerURL.
// To keep the red commit assertion-fail (not compile-fail), this test is
// added in the GREEN commit alongside the helper.
