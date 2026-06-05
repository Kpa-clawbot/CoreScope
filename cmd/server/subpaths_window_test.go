package main

// Regression test for issue #1217 — Route Patterns analytics must honor the
// `?window=` time-window filter (e.g. "1h", "24h", "7d"). Before the fix,
// computeAnalyticsSubpaths read the full s.spIndex / s.packets regardless of
// the window, so the chart counts were identical for every window selection.
//
// This test seeds two transmissions with distinct multi-hop paths at different
// `first_seen` ages (one recent, one ~30 days old) and asserts:
//   - the unbounded call returns BOTH paths
//   - a 24h windowed call returns ONLY the recent path
//
// If the handler/store ignores the window param, the windowed call returns
// the same totalPaths/subpaths as the unbounded call and the assertion below
// fails — that's the red-commit signal.

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

// setupSubpathWindowDB seeds a DB with one recent and one old multi-hop
// transmission, each with a distinct path so they produce distinct subpaths.
func setupSubpathWindowDB(t *testing.T) *DB {
	t.Helper()
	db := setupTestDB(t)

	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)
	old := now.Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	recentEpoch := now.Add(-1 * time.Hour).Unix()
	oldEpoch := now.Add(-30 * 24 * time.Hour).Unix()

	// Observer
	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obs1', 'Observer One', 'SJC', ?, '2025-01-01T00:00:00Z', 100)`, recent)

	// Recent transmission with path ["aa","bb"]
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('01', 'recent_hash_window_001', ?, 1, 4, '{}')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 10.0, -90, '["aa","bb"]', ?)`, recentEpoch)

	// Old transmission (30d ago) with disjoint path ["cc","dd"]
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('02', 'old_hash_window_002', ?, 1, 4, '{}')`, old)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (2, 1, 10.0, -90, '["cc","dd"]', ?)`, oldEpoch)

	return db
}

// TestSubpathsHonorsTimeWindow_StoreLevel asserts that the store-level
// API exposes a window-aware variant and that it filters by first_seen.
func TestSubpathsHonorsTimeWindow_StoreLevel(t *testing.T) {
	db := setupSubpathWindowDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}

	// Unbounded: should see both transmissions and their subpaths.
	all := store.GetAnalyticsSubpathsWithWindow("", 2, 8, 100, TimeWindow{})
	allTotal, _ := all["totalPaths"].(int)
	if allTotal != 2 {
		t.Fatalf("unbounded: expected totalPaths=2, got %d (subpaths=%v)", allTotal, all["subpaths"])
	}

	// 24h window: should exclude the 30d-old transmission.
	since := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	w := TimeWindow{Since: since, Label: "24h"}
	windowed := store.GetAnalyticsSubpathsWithWindow("", 2, 8, 100, w)
	winTotal, _ := windowed["totalPaths"].(int)
	if winTotal != 1 {
		t.Errorf("windowed (24h): expected totalPaths=1, got %d (subpaths=%v)", winTotal, windowed["subpaths"])
	}

	// And the old path "cc → dd" must NOT appear in the windowed response.
	if subs, ok := windowed["subpaths"].([]map[string]interface{}); ok {
		for _, s := range subs {
			if p, _ := s["path"].(string); p == "cc → dd" {
				t.Errorf("windowed (24h) leaked the old path %q", p)
			}
		}
	}
}

// TestSubpathsHandlerHonorsTimeWindow asserts that the HTTP handler reads
// `?window=` and forwards it to the store.
func TestSubpathsHandlerHonorsTimeWindow(t *testing.T) {
	db := setupSubpathWindowDB(t)
	defer db.Close()
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}
	srv.store = store

	mustGet := func(url string) map[string]interface{} {
		req := httptest.NewRequest("GET", url, nil)
		w := httptest.NewRecorder()
		switch {
		case containsPath(url, "/api/analytics/subpaths-bulk"):
			srv.handleAnalyticsSubpathsBulk(w, req)
		default:
			srv.handleAnalyticsSubpaths(w, req)
		}
		if w.Code != 200 {
			t.Fatalf("GET %s: status=%d body=%s", url, w.Code, w.Body.String())
		}
		var out map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatalf("json decode %s: %v body=%s", url, err, w.Body.String())
		}
		return out
	}

	all := mustGet("/api/analytics/subpaths?minLen=2&maxLen=8")
	allTotal, _ := all["totalPaths"].(float64)
	if int(allTotal) != 2 {
		t.Fatalf("unbounded: expected totalPaths=2, got %v", all["totalPaths"])
	}

	win := mustGet("/api/analytics/subpaths?minLen=2&maxLen=8&window=24h")
	winTotal, _ := win["totalPaths"].(float64)
	if int(winTotal) != 1 {
		t.Errorf("window=24h: expected totalPaths=1, got %v (resp=%+v)", win["totalPaths"], win)
	}

	// Bulk endpoint must also honor window.
	bulk := mustGet("/api/analytics/subpaths-bulk?groups=2-2:50&window=24h")
	results, _ := bulk["results"].([]interface{})
	if len(results) != 1 {
		t.Fatalf("bulk: expected 1 result group, got %d", len(results))
	}
	r0 := results[0].(map[string]interface{})
	bulkTotal, _ := r0["totalPaths"].(float64)
	if int(bulkTotal) != 1 {
		t.Errorf("bulk window=24h: expected totalPaths=1, got %v", r0["totalPaths"])
	}
}

func containsPath(url, want string) bool {
	for i := 0; i+len(want) <= len(url); i++ {
		if url[i:i+len(want)] == want {
			return true
		}
	}
	return false
}

// silence unused-import for fmt when iterating
var _ = fmt.Sprintf
