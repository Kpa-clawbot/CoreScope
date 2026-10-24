package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

const (
	nbNode = "e07d3b0011223344556677889900aabbccddeeff00112233445566779f41aa00"
	nbObsA = "7e76aa00112233445566778899aabbccddeeff00112233445566778899009400"
	nbObsB = "3b9f000011223344556677889900aabbccddeeff0011223344556677889f5e00"
)

func createNeighborBatteryTable(t *testing.T, db *DB) {
	t.Helper()
	_, err := db.conn.Exec(`CREATE TABLE neighbor_battery (
		node_id TEXT NOT NULL, observer_id TEXT NOT NULL, sampled_at TEXT NOT NULL,
		battery_mv INTEGER NOT NULL, snr REAL, rssi INTEGER, method TEXT NOT NULL,
		source TEXT NOT NULL, reply_hops INTEGER NOT NULL, received_at TEXT NOT NULL,
		PRIMARY KEY (node_id, observer_id, sampled_at)) WITHOUT ROWID`)
	if err != nil {
		t.Fatal(err)
	}
}

func insertNeighborBattery(t *testing.T, db *DB, observer string, at time.Time, mv int, snr float64, hops int) {
	t.Helper()
	ts := at.UTC().Format("2006-01-02T15:04:05Z")
	if _, err := db.conn.Exec(`INSERT INTO neighbor_battery VALUES (?,?,?,?,?,?,?,?,?,?)`,
		nbNode, observer, ts, mv, snr, -90, "status", "poll", hops, ts); err != nil {
		t.Fatal(err)
	}
}

func batteryRouter(t *testing.T, db *DB) *mux.Router {
	t.Helper()
	srv := NewServer(db, &Config{Port: 3000}, NewHub())
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)
	return router
}

func getBattery(t *testing.T, router *mux.Router) map[string]interface{} {
	t.Helper()
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/nodes/"+nbNode+"/battery?days=7", nil))
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func seedNeighborNode(t *testing.T, db *DB) time.Time {
	t.Helper()
	now := time.Now().UTC()
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, last_seen, first_seen) VALUES (?, 'West Lomond', 'repeater', ?, ?)`,
		nbNode, now.Format(time.RFC3339), now.Add(-72*time.Hour).Format(time.RFC3339))
	db.conn.Exec(`INSERT INTO observers (id, name, last_seen, first_seen) VALUES (?, 'Corstorphine OBS', ?, ?)`,
		strings.ToUpper(nbObsA), now.Format(time.RFC3339), now.Format(time.RFC3339))
	db.conn.Exec(`INSERT INTO observers (id, name, last_seen, first_seen) VALUES (?, 'Pentland OBS', ?, ?)`,
		strings.ToUpper(nbObsB), now.Format(time.RFC3339), now.Format(time.RFC3339))
	createNeighborBatteryTable(t, db)
	return now
}

// Neighbour readings appear as samples naming their observer, and the best
// heard observer (highest median SNR) supplies the latest value.
func TestNodeBatteryEndpoint_NeighborReadings(t *testing.T) {
	db := setupTestDB(t)
	now := seedNeighborNode(t, db)
	insertNeighborBattery(t, db, nbObsA, now.Add(-3*time.Hour), 3900, 7.5, 0)
	insertNeighborBattery(t, db, nbObsA, now.Add(-2*time.Hour), 3850, 7.0, 0)
	insertNeighborBattery(t, db, nbObsB, now.Add(-1*time.Hour), 3200, -4.0, 2)

	body := getBattery(t, batteryRouter(t, db))
	samples := body["samples"].([]interface{})
	if len(samples) != 3 {
		t.Fatalf("samples = %d, want 3", len(samples))
	}
	first := samples[0].(map[string]interface{})
	if first["source"] != "neighbor" || first["observer_id"] != nbObsA || first["observer_name"] != "Corstorphine OBS" {
		t.Errorf("first sample = %v", first)
	}
	if first["snr"].(float64) != 7.5 || first["reply_hops"].(float64) != 0 {
		t.Errorf("first sample snr/hops = %v/%v", first["snr"], first["reply_hops"])
	}

	sel := body["selected"].(map[string]interface{})
	if sel["observer_id"] != nbObsA {
		t.Errorf("selected = %v, want the stronger observer A although B reported last", sel["observer_id"])
	}
	if body["latest_mv"].(float64) != 3850 || body["status"] != "ok" {
		t.Errorf("latest = %v %v, want 3850 ok", body["latest_mv"], body["status"])
	}
	sources := body["sources"].([]interface{})
	if len(sources) != 2 {
		t.Fatalf("sources = %d, want 2", len(sources))
	}
	var a map[string]interface{}
	for _, src := range sources {
		if m := src.(map[string]interface{}); m["observer_id"] == nbObsA {
			a = m
		}
	}
	if a["samples"].(float64) != 2 || a["median_snr"].(float64) != 7.25 || a["observer_name"] != "Corstorphine OBS" {
		t.Errorf("source A = %v", a)
	}
}

// A fresh self-report beats any neighbour; a stale one doesn't.
func TestNodeBatteryEndpoint_SelfWinsWhenFresh(t *testing.T) {
	db := setupTestDB(t)
	now := seedNeighborNode(t, db)
	db.conn.Exec(`INSERT INTO observers (id, name, last_seen, first_seen) VALUES (?, 'West Lomond', ?, ?)`,
		strings.ToUpper(nbNode), now.Format(time.RFC3339), now.Format(time.RFC3339))
	insertNeighborBattery(t, db, nbObsA, now.Add(-10*time.Minute), 3700, 8, 0)
	db.conn.Exec(`INSERT INTO observer_metrics (observer_id, timestamp, battery_mv) VALUES (?, ?, ?)`,
		strings.ToUpper(nbNode), now.Add(-30*time.Minute).Format(time.RFC3339), 4100)

	body := getBattery(t, batteryRouter(t, db))
	if sel := body["selected"].(map[string]interface{}); sel["source"] != "self" || body["latest_mv"].(float64) != 4100 {
		t.Errorf("selected = %v latest %v, want self 4100", sel, body["latest_mv"])
	}

	db2 := setupTestDB(t)
	now = seedNeighborNode(t, db2)
	db2.conn.Exec(`INSERT INTO observers (id, name, last_seen, first_seen) VALUES (?, 'West Lomond', ?, ?)`,
		strings.ToUpper(nbNode), now.Format(time.RFC3339), now.Format(time.RFC3339))
	insertNeighborBattery(t, db2, nbObsA, now.Add(-10*time.Minute), 3700, 8, 0)
	db2.conn.Exec(`INSERT INTO observer_metrics (observer_id, timestamp, battery_mv) VALUES (?, ?, ?)`,
		strings.ToUpper(nbNode), now.Add(-5*time.Hour).Format(time.RFC3339), 4100)
	body = getBattery(t, batteryRouter(t, db2))
	if sel := body["selected"].(map[string]interface{}); sel["source"] != "neighbor" || body["latest_mv"].(float64) != 3700 {
		t.Errorf("selected = %v latest %v, want neighbour 3700 over a 5 h old self-report", sel, body["latest_mv"])
	}
}

// 0 mV is "no battery fitted", never critical.
func TestNodeBatteryEndpoint_ZeroIsNoBattery(t *testing.T) {
	db := setupTestDB(t)
	now := seedNeighborNode(t, db)
	insertNeighborBattery(t, db, nbObsA, now.Add(-time.Hour), 0, 6, 0)
	body := getBattery(t, batteryRouter(t, db))
	if body["status"] != "no_battery" {
		t.Errorf("status = %v, want no_battery", body["status"])
	}
}

// Without the table (an ingestor that hasn't run this build yet) the
// endpoint answers as before.
func TestNodeBatteryEndpoint_NoNeighborTable(t *testing.T) {
	db := setupTestDB(t)
	now := time.Now().UTC()
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, last_seen, first_seen) VALUES (?, 'N', 'repeater', ?, ?)`,
		nbNode, now.Format(time.RFC3339), now.Format(time.RFC3339))
	body := getBattery(t, batteryRouter(t, db))
	if body["status"] != "unknown" || len(body["samples"].([]interface{})) != 0 || len(body["sources"].([]interface{})) != 0 {
		t.Errorf("body = %v", body)
	}
	if body["selected"] != nil {
		t.Errorf("selected = %v, want null", body["selected"])
	}
}

func TestSelectBatterySourceFallsBackToNewest(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-48 * time.Hour).Format(time.RFC3339)
	older := now.Add(-72 * time.Hour).Format(time.RFC3339)
	got := selectBatterySource([]BatterySource{
		{Source: batterySourceSelf, LatestMv: 4000, LatestTs: older},
		{Source: batterySourceNeighbor, ObserverID: nbObsA, LatestMv: 3600, LatestTs: old},
	}, now)
	if got == nil || got.ObserverID != nbObsA {
		t.Fatalf("got %+v, want the newest source when nothing is recent", got)
	}
	if selectBatterySource(nil, now) != nil {
		t.Error("no sources should select nothing")
	}
}
