package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// createTestDBMultiDay creates a test DB with packets spread across numDays days.
// txPerDay transmissions are inserted per day, oldest day first.
// Packets within each day are spaced 1 minute apart.
func createTestDBMultiDay(t *testing.T, numDays, txPerDay int) string {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	conn, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	execOrFail := func(s string) {
		if _, err := conn.Exec(s); err != nil {
			t.Fatalf("createTestDBMultiDay setup: %v", err)
		}
	}
	execOrFail(`CREATE TABLE transmissions (id INTEGER PRIMARY KEY, raw_hex TEXT, hash TEXT, first_seen TEXT, route_type INTEGER, payload_type INTEGER, payload_version INTEGER, decoded_json TEXT)`)
	execOrFail(`CREATE TABLE observations (id INTEGER PRIMARY KEY, transmission_id INTEGER, observer_id TEXT, observer_name TEXT, direction TEXT, snr REAL, rssi REAL, score INTEGER, path_json TEXT, timestamp TEXT, raw_hex TEXT)`)
	execOrFail(`CREATE TABLE observers (rowid INTEGER PRIMARY KEY, id TEXT, name TEXT)`)
	execOrFail(`CREATE TABLE nodes (pubkey TEXT PRIMARY KEY, name TEXT, role TEXT, lat REAL, lon REAL, last_seen TEXT, first_seen TEXT, frequency REAL)`)
	execOrFail(`CREATE TABLE schema_version (version INTEGER)`)
	execOrFail(`INSERT INTO schema_version (version) VALUES (1)`)
	execOrFail(`CREATE INDEX idx_tx_first_seen ON transmissions(first_seen)`)

	id := 1
	now := time.Now().UTC()
	for day := numDays; day >= 1; day-- {
		base := now.Add(-time.Duration(day) * 24 * time.Hour)
		for i := 0; i < txPerDay; i++ {
			ts := base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
			hash := fmt.Sprintf("hash%06d", id)
			conn.Exec("INSERT INTO transmissions VALUES (?,?,?,?,0,4,1,?)", id, "aa", hash, ts, `{}`)
			conn.Exec("INSERT INTO observations VALUES (?,?,?,?,?,?,?,?,?,?,?)", id, id, "obs1", "Obs1", "RX", -10.0, -80.0, 5, `[]`, ts, "")
			id++
		}
	}
	return dbPath
}

// waitForBackgroundLoad polls backgroundLoadDone until true or timeout.
func waitForBackgroundLoad(t *testing.T, store *PacketStore, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if store.backgroundLoadDone.Load() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("background load did not complete within %v", timeout)
}

func TestHotStartupConfig_Clamp(t *testing.T) {
	dbPath := createTestDB(t, 10)
	defer os.RemoveAll(filepath.Dir(dbPath))

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.conn.Close()

	// hotStartupHours > retentionHours → must be clamped
	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  24,
		HotStartupHours: 48,
	})
	if store.hotStartupHours != 24 {
		t.Errorf("expected hotStartupHours clamped to retentionHours=24, got %f", store.hotStartupHours)
	}
}

func TestHotStartupConfig_ZeroIsDisabled(t *testing.T) {
	dbPath := createTestDB(t, 10)
	defer os.RemoveAll(filepath.Dir(dbPath))

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  24,
		HotStartupHours: 0,
	})
	if store.hotStartupHours != 0 {
		t.Errorf("expected hotStartupHours=0, got %f", store.hotStartupHours)
	}
}

func TestHotStartup_LoadsOnlyHotWindow(t *testing.T) {
	// 50 old packets (48h ago), 10 recent (30min ago)
	dbPath := createTestDBWithAgedPackets(t, 10, 50)
	defer os.RemoveAll(filepath.Dir(dbPath))

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  72,
		HotStartupHours: 1, // load only last 1 hour
	})
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}

	// Only the 10 recent packets should be in memory
	if len(store.packets) != 10 {
		t.Errorf("expected 10 recent packets in hot window, got %d", len(store.packets))
	}
	// oldestLoaded should be ~1h ago
	if store.oldestLoaded == "" {
		t.Fatal("oldestLoaded must be set after Load()")
	}
	oldest, _ := time.Parse(time.RFC3339, store.oldestLoaded)
	diff := time.Since(oldest)
	if diff < 30*time.Minute || diff > 90*time.Minute {
		t.Errorf("oldestLoaded %s should be ~1h ago, got diff=%v", store.oldestLoaded, diff)
	}
	// backgroundLoadDone must not be set by Load() itself
	if store.backgroundLoadDone.Load() {
		t.Error("backgroundLoadDone must not be true after Load()")
	}
}

func TestHotStartup_DisabledWhenZero(t *testing.T) {
	// 50 old (48h ago), 10 recent (30min ago) — all within 72h retention
	dbPath := createTestDBWithAgedPackets(t, 10, 50)
	defer os.RemoveAll(filepath.Dir(dbPath))

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  72,
		HotStartupHours: 0, // disabled → load all retentionHours as before
	})
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}

	// All 60 packets should be loaded (both old and recent within 72h)
	if len(store.packets) != 60 {
		t.Errorf("expected 60 packets with hotStartupHours=0, got %d", len(store.packets))
	}
}

func TestHotStartup_loadChunk_AddsOlderData(t *testing.T) {
	// 50 old packets (48h ago), 10 recent (30min ago)
	dbPath := createTestDBWithAgedPackets(t, 10, 50)
	defer os.RemoveAll(filepath.Dir(dbPath))

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  72,
		HotStartupHours: 1,
	})
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if len(store.packets) != 10 {
		t.Fatalf("setup: expected 10 packets after hot Load, got %d", len(store.packets))
	}

	// Load the old chunk (covers the 50 old packets at ~48h ago)
	chunkEnd := time.Now().UTC().Add(-1 * time.Hour)
	chunkStart := time.Now().UTC().Add(-72 * time.Hour)
	if err := store.loadChunk(chunkStart, chunkEnd); err != nil {
		t.Fatalf("loadChunk failed: %v", err)
	}

	// Should have 10 recent + 50 old
	if len(store.packets) != 60 {
		t.Errorf("expected 60 packets after loadChunk, got %d", len(store.packets))
	}
	// Packets must remain sorted ASC by first_seen
	for i := 1; i < len(store.packets); i++ {
		if store.packets[i].FirstSeen < store.packets[i-1].FirstSeen {
			t.Fatalf("packets not in ASC order at index %d: %s < %s",
				i, store.packets[i].FirstSeen, store.packets[i-1].FirstSeen)
		}
	}
	// byHash must include the old packets
	if len(store.byHash) != 60 {
		t.Errorf("expected byHash len=60, got %d", len(store.byHash))
	}
}
