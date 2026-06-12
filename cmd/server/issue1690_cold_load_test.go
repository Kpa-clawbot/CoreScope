package main

// Tests for issue #1690 — cold-load uses wrong time axis (first_seen instead
// of effective recency). Three tests live in this file:
//
//   Test1690_ColdLoad_TimeAxis  — long-lived transmissions (first_seen 30d
//                                  ago) with recent observations must load
//                                  under a 1h hotStartupHours window.
//   Test1690_BackgroundLoadHonesty — backgroundLoadComplete must NOT flip to
//                                     true when coverage is below threshold.
//   Test1690_PerfStats_NewFields — typed perf response must expose
//                                   retentionHours, oldestLoaded,
//                                   loadCoverageRatio.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// createTestDBWithLastSeen seeds a DB with the post-fix schema (last_seen
// column on transmissions). nowSec is the unix-second reference; fixture
// rows are placed relative to it.
//
// numTx transmissions, each with first_seen = nowSec - firstSeenAgo, and
// last_seen = nowSec - lastSeenAgo. Each tx has obsPerTx observations whose
// timestamps are within the last 20 minutes.
func createTestDBWithLastSeen(t *testing.T, dbPath string, numTx, obsPerTx int, nowSec int64, firstSeenAgo, lastSeenAgo time.Duration) {
	t.Helper()
	conn, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	execOrFail := func(s string) {
		if _, err := conn.Exec(s); err != nil {
			t.Fatalf("test DB exec: %v\nSQL: %s", err, s)
		}
	}
	// Use the post-fix schema shape: transmissions has a last_seen INTEGER column.
	execOrFail(`CREATE TABLE transmissions (
		id INTEGER PRIMARY KEY,
		raw_hex TEXT, hash TEXT, first_seen TEXT,
		route_type INTEGER, payload_type INTEGER,
		payload_version INTEGER, decoded_json TEXT,
		last_seen INTEGER NOT NULL DEFAULT 0
	)`)
	execOrFail(`CREATE TABLE observations (
		id INTEGER PRIMARY KEY, transmission_id INTEGER, observer_id TEXT, observer_name TEXT,
		direction TEXT, snr REAL, rssi REAL, score INTEGER,
		path_json TEXT, timestamp TEXT, raw_hex TEXT
	)`)
	execOrFail(`CREATE TABLE observers (rowid INTEGER PRIMARY KEY, id TEXT, name TEXT, iata TEXT)`)
	execOrFail(`CREATE TABLE nodes (pubkey TEXT PRIMARY KEY, name TEXT, role TEXT, lat REAL, lon REAL, last_seen TEXT, first_seen TEXT, frequency REAL)`)
	execOrFail(`CREATE TABLE schema_version (version INTEGER)`)
	execOrFail(`INSERT INTO schema_version (version) VALUES (1)`)
	execOrFail(`CREATE INDEX idx_tx_first_seen ON transmissions(first_seen)`)
	execOrFail(`CREATE INDEX idx_tx_last_seen ON transmissions(last_seen)`)

	firstSeenTime := time.Unix(nowSec, 0).UTC().Add(-firstSeenAgo).Format(time.RFC3339)
	lastSeenUnix := nowSec - int64(lastSeenAgo.Seconds())

	txStmt, err := conn.Prepare("INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json, last_seen) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		t.Fatalf("prepare tx: %v", err)
	}
	defer txStmt.Close()
	obsStmt, err := conn.Prepare("INSERT INTO observations (id, transmission_id, observer_id, observer_name, direction, snr, rssi, score, path_json, timestamp) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		t.Fatalf("prepare obs: %v", err)
	}
	defer obsStmt.Close()

	obsID := 1
	for i := 1; i <= numTx; i++ {
		hash := fmt.Sprintf("h%06d", i)
		if _, err := txStmt.Exec(i, "aabb", hash, firstSeenTime, 0, 4, 1, "{}", lastSeenUnix); err != nil {
			t.Fatalf("insert tx %d: %v", i, err)
		}
		for j := 0; j < obsPerTx; j++ {
			// Observations within the last 20 minutes relative to nowSec.
			obsTs := time.Unix(nowSec, 0).UTC().Add(-time.Duration(j)*time.Minute - time.Minute).Format(time.RFC3339)
			if _, err := obsStmt.Exec(obsID, i, "obs1", "Obs1", "RX", -10.0, -80.0, 5, "[]", obsTs); err != nil {
				t.Fatalf("insert obs: %v", err)
			}
			obsID++
		}
	}
}

// Test1690_ColdLoad_TimeAxis seeds 1000 transmissions whose hash *first
// appeared* 30 days ago but whose last observation was 30 minutes ago.
// With a 1h hotStartupHours, the pre-fix code (filtering on first_seen)
// loads zero rows; the post-fix code (filtering on last_seen) must load
// all 1000.
func Test1690_ColdLoad_TimeAxis(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	nowSec := time.Now().UTC().Unix()
	createTestDBWithLastSeen(t, dbPath, 1000, 1, nowSec,
		30*24*time.Hour, // first_seen = 30d ago
		30*time.Minute)  // last_seen = 30min ago

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  168,
		HotStartupHours: 1,
	})

	if err := store.LoadChunked(0); err != nil {
		t.Fatalf("LoadChunked: %v", err)
	}

	loaded := len(store.packets)
	if loaded < 1000 {
		t.Fatalf("Test1690_ColdLoad_TimeAxis: expected ≥1000 transmissions loaded "+
			"(all 1000 fixture rows have last_seen within 1h), got %d. "+
			"Pre-fix behavior: chunked_load.go filters t.first_seen >= now-1h "+
			"which excludes all 30d-old rows.", loaded)
	}
}

// Test1690_BackgroundLoadHonesty seeds 1000 transmissions but caps the
// store's memory budget so it can only fit a fraction. After
// loadBackgroundChunks runs, backgroundLoadDone must be FALSE and
// backgroundLoadFailed must be TRUE because actual coverage is < 90%.
func Test1690_BackgroundLoadHonesty(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	nowSec := time.Now().UTC().Unix()
	// 5000 rows + 1MB budget → maxPackets ≈ 1048 → only ~21% loaded.
	createTestDBWithLastSeen(t, dbPath, 5000, 1, nowSec,
		30*time.Minute, 30*time.Minute)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  168,
		HotStartupHours: 1,
		MaxMemoryMB:     1, // forces bounded load ≪ 5000 rows
	})
	if err := store.LoadChunked(0); err != nil {
		t.Fatalf("LoadChunked: %v", err)
	}
	store.loadBackgroundChunks()

	if store.backgroundLoadDone.Load() {
		t.Errorf("backgroundLoadDone=true with only %d/5000 packets loaded; "+
			"must be false until coverage ≥ 90%%", len(store.packets))
	}
	if !store.backgroundLoadFailed.Load() {
		t.Errorf("backgroundLoadFailed=false despite under-coverage "+
			"(%d/5000 packets loaded); must be true with a reason", len(store.packets))
	}
	// The error message must mention a percentage so operators can see
	// the actual ratio surface in the perf endpoint.
	errMsg := store.BackgroundLoadError()
	if !strings.Contains(errMsg, "%") {
		t.Errorf("backgroundLoadError=%q; expected human-readable ratio "+
			"(e.g. 'loaded X%% of Y rows')", errMsg)
	}
}

// Test1690_PerfStats_NewFields asserts the typed perf payload exposes the
// retention/coverage fields needed for prod observability.
func Test1690_PerfStats_NewFields(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	nowSec := time.Now().UTC().Unix()
	createTestDBWithLastSeen(t, dbPath, 10, 1, nowSec,
		30*time.Minute, 30*time.Minute)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  168,
		HotStartupHours: 1,
	})
	if err := store.LoadChunked(0); err != nil {
		t.Fatalf("LoadChunked: %v", err)
	}

	ps := store.GetPerfStoreStatsTyped()
	buf, err := json.Marshal(ps)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var asMap map[string]interface{}
	if err := json.Unmarshal(buf, &asMap); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"retentionHours", "oldestLoaded", "loadCoverageRatio"} {
		if _, ok := asMap[key]; !ok {
			t.Errorf("PerfPacketStoreStats missing %q field; payload=%s", key, string(buf))
		}
	}
}
