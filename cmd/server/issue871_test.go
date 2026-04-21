package main

import (
	"database/sql"
	"testing"
	"time"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// setupTestDB871 creates a test DB with schema and returns a read-only *DB handle.
func setupTestDB871(t *testing.T) (*DB, *sql.DB) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test871.db")

	// Open writable connection for setup
	rw, err := sql.Open("sqlite", "file:"+dbPath+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}

	_, err = rw.Exec(`
		CREATE TABLE IF NOT EXISTS nodes (
			public_key TEXT PRIMARY KEY,
			name TEXT, role TEXT,
			lat REAL, lon REAL,
			last_seen TEXT, first_seen TEXT,
			advert_count INTEGER DEFAULT 0,
			battery_mv INTEGER, temperature_c REAL
		);
		CREATE TABLE IF NOT EXISTS transmissions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			raw_hex TEXT NOT NULL,
			hash TEXT NOT NULL UNIQUE,
			first_seen TEXT NOT NULL,
			route_type INTEGER,
			payload_type INTEGER,
			payload_version INTEGER,
			decoded_json TEXT,
			created_at TEXT DEFAULT (datetime('now'))
		);
		CREATE TABLE IF NOT EXISTS observers (
			rowid INTEGER PRIMARY KEY AUTOINCREMENT,
			id TEXT NOT NULL UNIQUE,
			name TEXT
		);
		CREATE TABLE IF NOT EXISTS observations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			transmission_id INTEGER NOT NULL,
			observer_id TEXT,
			observer_name TEXT,
			direction TEXT,
			snr REAL, rssi REAL, score INTEGER,
			path_json TEXT, timestamp TEXT
		);
	`)
	if err != nil {
		t.Fatal(err)
	}

	// Open read-only handle for the store
	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		db.Close()
		rw.Close()
	})

	return db, rw
}

// TestEnrichObsFallbackToDB verifies that enrichObs falls back to the DB when
// the parent transmission has been evicted from memory (#871 root cause).
func TestEnrichObsFallbackToDB(t *testing.T) {
	db, rw := setupTestDB871(t)

	now := time.Now().UTC().Format(time.RFC3339)
	_, err := rw.Exec(
		`INSERT INTO transmissions (raw_hex, hash, first_seen, payload_type, decoded_json) VALUES (?, ?, ?, ?, ?)`,
		"aabbcc", "abc123", now, 4, `{"pubKey":"pk1"}`,
	)
	if err != nil {
		t.Fatal(err)
	}

	store := NewPacketStore(db, &PacketStoreConfig{})

	// Observation references tx_id=1, but tx is NOT in byTxID (simulates eviction)
	obs := &StoreObs{
		ID:             1,
		TransmissionID: 1,
		ObserverID:     "obs1",
		ObserverName:   "Observer1",
		Timestamp:      now,
	}

	result := store.enrichObs(obs)

	// hash must be present from DB fallback
	if result["hash"] == nil {
		t.Errorf("enrichObs: hash is nil — DB fallback failed")
	}
	if h, ok := result["hash"].(string); !ok || h != "abc123" {
		t.Errorf("enrichObs: expected hash 'abc123', got %v", result["hash"])
	}
	if result["payload_type"] == nil {
		t.Errorf("enrichObs: payload_type is nil — DB fallback failed")
	}

	// When tx IS in memory, it should use the in-memory path
	pt := 4
	store.byTxID[1] = &StoreTx{
		ID: 1, Hash: "abc123", FirstSeen: now,
		PayloadType: &pt, RawHex: "aabbcc",
	}

	result2 := store.enrichObs(obs)
	if result2["hash"] == nil {
		t.Errorf("enrichObs with in-memory tx: hash is nil")
	}
}

// TestGetNodeHealthRecentPacketsNoNilFields verifies that GetNodeHealth's
// recentPackets never contains entries with nil hash or timestamp.
func TestGetNodeHealthRecentPacketsNoNilFields(t *testing.T) {
	db, rw := setupTestDB871(t)

	now := time.Now().UTC().Format(time.RFC3339)
	_, err := rw.Exec(
		`INSERT INTO nodes (public_key, name, role, last_seen) VALUES (?, ?, ?, ?)`,
		"pk1", "TestNode", "repeater", now,
	)
	if err != nil {
		t.Fatal(err)
	}

	store := NewPacketStore(db, &PacketStoreConfig{})

	pt := 4
	tx := &StoreTx{
		ID: 1, Hash: "hash1", FirstSeen: now,
		PayloadType: &pt, DecodedJSON: `{"pubKey":"pk1"}`,
		obsKeys: make(map[string]bool), observerSet: make(map[string]bool),
	}
	store.byTxID[1] = tx
	store.byHash["hash1"] = tx
	store.byNode["pk1"] = []*StoreTx{tx}
	store.nodeHashes["pk1"] = map[string]bool{"hash1": true}

	result, err := store.GetNodeHealth("pk1")
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("GetNodeHealth returned nil")
	}

	packets, ok := result["recentPackets"].([]map[string]interface{})
	if !ok {
		t.Fatal("recentPackets is not []map[string]interface{}")
	}

	for i, p := range packets {
		if p["hash"] == nil {
			t.Errorf("recentPackets[%d] has nil hash", i)
		}
		if p["timestamp"] == nil {
			t.Errorf("recentPackets[%d] has nil timestamp", i)
		}
	}
}

// TestEnrichObsNilDB verifies enrichObs doesn't panic when db is nil.
func TestEnrichObsNilDB(t *testing.T) {
	store := &PacketStore{
		byTxID:  make(map[int]*StoreTx),
		byObsID: make(map[int]*StoreObs),
	}

	obs := &StoreObs{
		ID: 1, TransmissionID: 999,
		Timestamp: "2026-01-01T00:00:00Z",
	}

	// Should not panic
	result := store.enrichObs(obs)
	if result["hash"] != nil {
		t.Errorf("expected nil hash when no DB and no in-memory tx, got %v", result["hash"])
	}
}
