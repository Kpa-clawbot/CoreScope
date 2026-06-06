package main

// Regression tests for the three MAJOR findings on PR #1589.
// These tests gate three semantic regressions that the rest of the PR's tests
// did not catch:
//
//   MAJOR-1: handleAnalyticsSubpaths default limit was silently halved 100→50
//            when migrated to queryLimit(r, 50, ...AnalyticsMax).
//   MAJOR-2: handleChannelMessages default limit was silently halved 100→50
//            when migrated to queryLimit(r, 50, ...ChannelMessagesMax).
//   MAJOR-3: handleBulkHealth was bundled into NodesMax (default 2000),
//            10× its previous ceiling of 200, despite being per-row heavier.
//
// For MAJOR-1/2 we assert on the literal call-site `def` value via source
// inspection because the rendered response does not expose the applied limit.
// For MAJOR-3 we assert both the config-defaults plumbing AND the runtime
// behavior: BulkHealthMax must exist as its own field with default 200, and
// handleBulkHealth must clamp through it (not NodesMax).

import (
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestPR1589_AnalyticsSubpathsDefaultIs100(t *testing.T) {
	// MAJOR-1: regression guard.
	src, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatalf("read routes.go: %v", err)
	}
	if !strings.Contains(string(src), "queryLimit(r, 100, s.cfg.ListLimits.AnalyticsMax)") {
		t.Error("handleAnalyticsSubpaths must use def=100 in queryLimit; " +
			"PR #1589 inadvertently halved the default to 50 (MAJOR-1)")
	}
}

func TestPR1589_ChannelMessagesDefaultIs100(t *testing.T) {
	// MAJOR-2: regression guard.
	src, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatalf("read routes.go: %v", err)
	}
	if !strings.Contains(string(src), "queryLimit(r, 100, s.cfg.ListLimits.ChannelMessagesMax)") {
		t.Error("handleChannelMessages must use def=100 in queryLimit; " +
			"PR #1589 inadvertently halved the default to 50 (MAJOR-2)")
	}
}

func TestPR1589_BulkHealthMaxDefaultsTo200(t *testing.T) {
	// MAJOR-3 (config plumbing): a dedicated BulkHealthMax must exist with
	// default 200 — bulk-health is per-row much heavier than /api/nodes,
	// so it cannot inherit NodesMax (default 2000).
	dir := t.TempDir()
	os.WriteFile(dir+"/config.json", []byte(`{"port":3000}`), 0644)
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.ListLimits.BulkHealthMax != 200 {
		t.Errorf("expected BulkHealthMax default 200, got %d", cfg.ListLimits.BulkHealthMax)
	}
}

func TestPR1589_BulkHealthClampsViaBulkHealthMax(t *testing.T) {
	// MAJOR-3 (runtime wiring): /api/nodes/bulk-health must clamp the limit
	// through BulkHealthMax — not NodesMax. We set BulkHealthMax=1 and
	// NodesMax=9999; if the handler still uses NodesMax the seed data (3
	// nodes) will all come back. If wired correctly it must clamp to 1.
	srv, router := setupTestServer(t)
	srv.cfg.ListLimits = &ListLimitsConfig{
		PacketsMax:         10000,
		NodesMax:           9999,
		AnalyticsMax:       200,
		ChannelMessagesMax: 500,
		BulkHealthMax:      1,
	}

	req := httptest.NewRequest("GET", "/api/nodes/bulk-health?limit=500", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	// Response is a top-level JSON array (filtered or unfiltered).
	body := strings.TrimSpace(w.Body.String())
	if !strings.HasPrefix(body, "[") {
		t.Fatalf("expected JSON array response, got: %s", body)
	}
	// Count top-level objects via "public_key" occurrences (each row has one).
	rowCount := strings.Count(body, `"public_key"`)
	if rowCount > 1 {
		t.Errorf("BulkHealthMax=1 should clamp to 1 row, got %d rows; "+
			"handler is likely still using NodesMax (MAJOR-3): %s", rowCount, body)
	}
}
