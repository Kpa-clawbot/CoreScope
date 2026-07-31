package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

const nodeAnalyticsSummaryTestPK = "aabbccdd11223344" // seeded by seedTestData

// withFixedNodeAnalyticsNow pins nodeAnalyticsNow for the duration of the
// test, so GetNodeAnalytics/GetNodeAnalyticsSummary compute against the
// exact same instant instead of racing two independent time.Now() calls a
// few instructions apart — required for the full/summary parity assertion
// to be deterministic.
func withFixedNodeAnalyticsNow(t *testing.T, now time.Time) {
	t.Helper()
	orig := nodeAnalyticsNow
	nodeAnalyticsNow = func() time.Time { return now }
	t.Cleanup(func() { nodeAnalyticsNow = orig })
}

// TestNodeAnalyticsSummary_OnlyTimeRangeAndComputedStats asserts the wire
// JSON has exactly two top-level keys — no node, no clockSkew, none of the
// heavy display arrays the full /analytics endpoint carries.
func TestNodeAnalyticsSummary_OnlyTimeRangeAndComputedStats(t *testing.T) {
	_, router := setupTestServer(t)
	withFixedNodeAnalyticsNow(t, time.Now().UTC())

	req := httptest.NewRequest("GET", "/api/nodes/"+nodeAnalyticsSummaryTestPK+"/analytics/summary?days=7", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(raw) != 2 {
		gotKeys := make([]string, 0, len(raw))
		for k := range raw {
			gotKeys = append(gotKeys, k)
		}
		t.Fatalf("expected exactly 2 top-level keys, got %d: %v", len(raw), gotKeys)
	}
	if _, ok := raw["timeRange"]; !ok {
		t.Error("missing timeRange")
	}
	if _, ok := raw["computedStats"]; !ok {
		t.Error("missing computedStats")
	}
}

// TestNodeAnalyticsSummary_MatchesFullComputedStats asserts full and
// summary produce byte-for-byte identical ComputedNodeStats for the same
// pubkey/days/now — the whole point of sharing one accumulator.
func TestNodeAnalyticsSummary_MatchesFullComputedStats(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	if !store.WaitIndexesReady(5 * time.Second) {
		t.Fatal("background indexes never became ready")
	}

	withFixedNodeAnalyticsNow(t, time.Now().UTC())

	full, err := store.GetNodeAnalytics(nodeAnalyticsSummaryTestPK, 7)
	if err != nil {
		t.Fatalf("GetNodeAnalytics: %v", err)
	}
	summary, err := store.GetNodeAnalyticsSummary(nodeAnalyticsSummaryTestPK, 7)
	if err != nil {
		t.Fatalf("GetNodeAnalyticsSummary: %v", err)
	}

	if !reflect.DeepEqual(full.ComputedStats, summary.ComputedStats) {
		t.Fatalf("computedStats mismatch:\nfull:    %+v\nsummary: %+v", full.ComputedStats, summary.ComputedStats)
	}
	if full.TimeRange != summary.TimeRange {
		t.Fatalf("timeRange mismatch:\nfull:    %+v\nsummary: %+v", full.TimeRange, summary.TimeRange)
	}
	if full.ComputedStats.TotalPackets == 0 {
		t.Fatal("fixture produced zero packets — test isn't exercising real data")
	}
}

// TestHandleNodeAnalyticsSummary_DaysMissingDefaultsTo7 mirrors
// handleNodeAnalytics' own default.
func TestHandleNodeAnalyticsSummary_DaysMissingDefaultsTo7(t *testing.T) {
	_, router := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/nodes/"+nodeAnalyticsSummaryTestPK+"/analytics/summary", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp NodeAnalyticsSummaryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp.TimeRange.Days != 7 {
		t.Errorf("expected days=7, got %d", resp.TimeRange.Days)
	}
}

// TestHandleNodeAnalyticsSummary_DaysZeroClampsToOne.
func TestHandleNodeAnalyticsSummary_DaysZeroClampsToOne(t *testing.T) {
	_, router := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/nodes/"+nodeAnalyticsSummaryTestPK+"/analytics/summary?days=0", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp NodeAnalyticsSummaryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp.TimeRange.Days != 1 {
		t.Errorf("expected days=1, got %d", resp.TimeRange.Days)
	}
}

// TestHandleNodeAnalyticsSummary_DaysOver365ClampsTo365.
func TestHandleNodeAnalyticsSummary_DaysOver365ClampsTo365(t *testing.T) {
	_, router := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/nodes/"+nodeAnalyticsSummaryTestPK+"/analytics/summary?days=9999", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp NodeAnalyticsSummaryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp.TimeRange.Days != 365 {
		t.Errorf("expected days=365, got %d", resp.TimeRange.Days)
	}
}

// TestHandleNodeAnalyticsSummary_InvalidDaysDefaultsTo7 — a non-numeric
// days value falls back to the default via queryInt, same as /analytics.
func TestHandleNodeAnalyticsSummary_InvalidDaysDefaultsTo7(t *testing.T) {
	_, router := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/nodes/"+nodeAnalyticsSummaryTestPK+"/analytics/summary?days=notanumber", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp NodeAnalyticsSummaryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp.TimeRange.Days != 7 {
		t.Errorf("expected days=7, got %d", resp.TimeRange.Days)
	}
}

// TestHandleNodeAnalyticsSummary_UnknownNode404.
func TestHandleNodeAnalyticsSummary_UnknownNode404(t *testing.T) {
	_, router := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/nodes/does-not-exist-pk/analytics/summary", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

// TestHandleNodeAnalyticsSummary_BlacklistedNode404.
func TestHandleNodeAnalyticsSummary_BlacklistedNode404(t *testing.T) {
	srv, router := setupTestServer(t)
	srv.cfg.SetNodeBlacklist([]string{nodeAnalyticsSummaryTestPK})

	req := httptest.NewRequest("GET", "/api/nodes/"+nodeAnalyticsSummaryTestPK+"/analytics/summary", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for blacklisted node, got %d", w.Code)
	}
}

// TestHandleNodeAnalyticsSummary_HiddenNode404 mirrors the existing
// TestHiddenNamePrefix_1181_NodeHealth pattern (#1181).
func TestHandleNodeAnalyticsSummary_HiddenNode404(t *testing.T) {
	srv, router := setupTestServer(t)

	pk := "deadbeef00005202"
	if _, err := srv.db.conn.Exec(`INSERT INTO nodes
		(public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, ?, ?, 0, 0, '2026-06-01T00:00:00Z', '2026-06-01T00:00:00Z', 1)`,
		pk, "🚫 hide me", "companion"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	srv.cfg.SetHiddenNamePrefixes([]string{"🚫"})

	req := httptest.NewRequest("GET", "/api/nodes/"+pk+"/analytics/summary", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for hidden node, got %d, body=%s", w.Code, w.Body.String())
	}
}

// TestHandleNodeAnalyticsSummary_KnownNodeNoPackets200 asserts a real node
// with zero packets in the window returns 200 with the same zero values as
// the full endpoint would.
func TestHandleNodeAnalyticsSummary_KnownNodeNoPackets200(t *testing.T) {
	srv, router := setupTestServer(t)

	pk := "deadbeef00005203"
	if _, err := srv.db.conn.Exec(`INSERT INTO nodes
		(public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, ?, ?, 0, 0, '2026-06-01T00:00:00Z', '2026-06-01T00:00:00Z', 0)`,
		pk, "NoPacketsNode", "companion"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/nodes/"+pk+"/analytics/summary?days=7", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body=%s", w.Code, w.Body.String())
	}

	var resp NodeAnalyticsSummaryResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	want := ComputedNodeStats{SignalGrade: "D"}
	if !reflect.DeepEqual(resp.ComputedStats, want) {
		t.Fatalf("expected zero-value computedStats %+v, got %+v", want, resp.ComputedStats)
	}
}

// TestNodeAnalyticsFull_RetainsAllFields asserts the refactor didn't drop
// any field from the existing full /analytics response.
func TestNodeAnalyticsFull_RetainsAllFields(t *testing.T) {
	_, router := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/nodes/"+nodeAnalyticsSummaryTestPK+"/analytics?days=7", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &raw); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, key := range []string{
		"node", "timeRange", "activityTimeline", "snrTrend", "packetTypeBreakdown",
		"observerCoverage", "hopDistribution", "peerInteractions", "uptimeHeatmap", "computedStats",
	} {
		if _, ok := raw[key]; !ok {
			t.Errorf("full /analytics response missing expected field %q", key)
		}
	}
}
