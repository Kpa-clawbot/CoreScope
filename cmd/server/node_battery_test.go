package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// TestGetNodeBatteryHistory_FromObserverMetrics validates that the DB layer
// can pull a node's battery_mv time-series from observer_metrics, joining
// observers.id (uppercase hex pubkey) to nodes.public_key (lowercase hex).
func TestGetNodeBatteryHistory_FromObserverMetrics(t *testing.T) {
	db := setupTestDB(t)
	now := time.Now().UTC()

	// node + observer with matching pubkey (cases differ on purpose)
	pkLower := "deadbeefcafef00d11223344"
	idUpper := strings.ToUpper(pkLower)
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, last_seen, first_seen) VALUES (?, 'BatNode', 'repeater', ?, ?)`,
		pkLower, now.Format(time.RFC3339), now.Add(-72*time.Hour).Format(time.RFC3339))
	db.conn.Exec(`INSERT INTO observers (id, name, last_seen, first_seen) VALUES (?, 'BatNode', ?, ?)`,
		idUpper, now.Format(time.RFC3339), now.Add(-72*time.Hour).Format(time.RFC3339))

	// 3 metrics samples: 3700, 3500, 3200 mV
	for i, mv := range []int{3700, 3500, 3200} {
		ts := now.Add(time.Duration(-2+i) * time.Hour).Format(time.RFC3339)
		db.conn.Exec(`INSERT INTO observer_metrics (observer_id, timestamp, battery_mv) VALUES (?, ?, ?)`,
			idUpper, ts, mv)
	}
	// One sample with NULL battery should be skipped
	db.conn.Exec(`INSERT INTO observer_metrics (observer_id, timestamp) VALUES (?, ?)`,
		idUpper, now.Add(-3*time.Hour).Format(time.RFC3339))

	since := now.Add(-24 * time.Hour).Format(time.RFC3339)
	samples, err := db.GetNodeBatteryHistory(pkLower, since)
	if err != nil {
		t.Fatalf("GetNodeBatteryHistory: %v", err)
	}
	if len(samples) != 3 {
		t.Fatalf("expected 3 samples, got %d", len(samples))
	}
	if samples[0].BatteryMv != 3700 || samples[2].BatteryMv != 3200 {
		t.Errorf("samples=%+v", samples)
	}
}

// TestNodeBatteryEndpoint validates the /api/nodes/{pubkey}/battery endpoint
// returns time-series data plus configured thresholds and a status flag.
func TestNodeBatteryEndpoint(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)

	now := time.Now().UTC()
	pkLower := "aabbccdd11223344"
	idUpper := strings.ToUpper(pkLower)
	db.conn.Exec(`INSERT INTO observers (id, name, last_seen, first_seen) VALUES (?, 'TestRepeater', ?, ?)`,
		idUpper, now.Format(time.RFC3339), now.Add(-72*time.Hour).Format(time.RFC3339))
	for i, mv := range []int{3800, 3600, 3200} {
		ts := now.Add(time.Duration(-2+i) * time.Hour).Format(time.RFC3339)
		db.conn.Exec(`INSERT INTO observer_metrics (observer_id, timestamp, battery_mv) VALUES (?, ?, ?)`,
			idUpper, ts, mv)
	}

	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/nodes/"+pkLower+"/battery?days=7", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	samples, ok := body["samples"].([]interface{})
	if !ok {
		t.Fatalf("samples missing: %+v", body)
	}
	if len(samples) != 3 {
		t.Errorf("expected 3 samples, got %d", len(samples))
	}
	thr, ok := body["thresholds"].(map[string]interface{})
	if !ok {
		t.Fatalf("thresholds missing: %+v", body)
	}
	if int(thr["low_mv"].(float64)) != 3300 {
		t.Errorf("default low_mv expected 3300, got %v", thr["low_mv"])
	}
	if int(thr["critical_mv"].(float64)) != 3000 {
		t.Errorf("default critical_mv expected 3000, got %v", thr["critical_mv"])
	}
	// latest 3200 -> "low" (below 3300, above 3000)
	if body["status"] != "low" {
		t.Errorf("expected status=low, got %v", body["status"])
	}
	if int(body["latest_mv"].(float64)) != 3200 {
		t.Errorf("latest_mv expected 3200, got %v", body["latest_mv"])
	}
}

// TestNodeBatteryEndpoint_NoData returns 200 with empty samples and status="unknown".
func TestNodeBatteryEndpoint_NoData(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/nodes/aabbccdd11223344/battery", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["status"] != "unknown" {
		t.Errorf("expected unknown when no samples, got %v", body["status"])
	}
}

// TestNodeBatteryEndpoint_404 returns 404 for unknown node.
func TestNodeBatteryEndpoint_404(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/nodes/notarealnode00000000/battery", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// TestBatteryThresholds_ConfigOverride confirms config overrides take effect.
func TestBatteryThresholds_ConfigOverride(t *testing.T) {
	cfg := &Config{
		BatteryThresholds: &BatteryThresholdsConfig{LowMv: 3500, CriticalMv: 3100},
	}
	if cfg.LowBatteryMv() != 3500 {
		t.Errorf("LowBatteryMv override failed: %d", cfg.LowBatteryMv())
	}
	if cfg.CriticalBatteryMv() != 3100 {
		t.Errorf("CriticalBatteryMv override failed: %d", cfg.CriticalBatteryMv())
	}

	empty := &Config{}
	if empty.LowBatteryMv() != 3300 {
		t.Errorf("default LowBatteryMv expected 3300, got %d", empty.LowBatteryMv())
	}
	if empty.CriticalBatteryMv() != 3000 {
		t.Errorf("default CriticalBatteryMv expected 3000, got %d", empty.CriticalBatteryMv())
	}
}
