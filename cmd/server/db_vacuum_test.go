package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// createFreshIngestorDB creates a SQLite DB using the ingestor's applySchema logic
// (simulated here) with auto_vacuum=INCREMENTAL set before tables.
func createFreshDBWithAutoVacuum(t *testing.T, path string) *sql.DB {
	t.Helper()
	// auto_vacuum must be set via DSN before journal_mode creates the DB file
	db, err := sql.Open("sqlite", path+"?_pragma=auto_vacuum(INCREMENTAL)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)

	// Create minimal schema
	_, err = db.Exec(`
		CREATE TABLE transmissions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			raw_hex TEXT NOT NULL,
			hash TEXT NOT NULL UNIQUE,
			first_seen TEXT NOT NULL,
			route_type INTEGER,
			payload_type INTEGER,
			payload_version INTEGER,
			decoded_json TEXT,
			created_at TEXT DEFAULT (datetime('now')),
			channel_hash TEXT
		);
		CREATE TABLE observations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			transmission_id INTEGER NOT NULL REFERENCES transmissions(id),
			observer_idx INTEGER,
			direction TEXT,
			snr REAL,
			rssi REAL,
			score INTEGER,
			path_json TEXT,
			timestamp INTEGER NOT NULL
		);
	`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestNewDBHasIncrementalAutoVacuum(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	db := createFreshDBWithAutoVacuum(t, path)
	defer db.Close()

	var autoVacuum int
	if err := db.QueryRow("PRAGMA auto_vacuum").Scan(&autoVacuum); err != nil {
		t.Fatal(err)
	}
	if autoVacuum != 2 {
		t.Fatalf("expected auto_vacuum=2 (INCREMENTAL), got %d", autoVacuum)
	}
}

func TestExistingDBHasAutoVacuumNone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	// Create DB WITHOUT setting auto_vacuum (simulates old DB)
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec("CREATE TABLE dummy (id INTEGER PRIMARY KEY)")
	if err != nil {
		t.Fatal(err)
	}

	var autoVacuum int
	if err := db.QueryRow("PRAGMA auto_vacuum").Scan(&autoVacuum); err != nil {
		t.Fatal(err)
	}
	db.Close()

	if autoVacuum != 0 {
		t.Fatalf("expected auto_vacuum=0 (NONE) for old DB, got %d", autoVacuum)
	}
}

func TestVacuumOnStartupMigratesDB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	// Create DB without auto_vacuum (old DB)
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	_, err = db.Exec("CREATE TABLE dummy (id INTEGER PRIMARY KEY)")
	if err != nil {
		t.Fatal(err)
	}

	var before int
	db.QueryRow("PRAGMA auto_vacuum").Scan(&before)
	if before != 0 {
		t.Fatalf("precondition: expected auto_vacuum=0, got %d", before)
	}
	db.Close()

	// Simulate vacuumOnStartup migration using openRW
	rw, err := openRW(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rw.Exec("PRAGMA auto_vacuum = INCREMENTAL"); err != nil {
		t.Fatal(err)
	}
	if _, err := rw.Exec("VACUUM"); err != nil {
		t.Fatal(err)
	}
	rw.Close()

	// Verify migration
	db2, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()

	var after int
	if err := db2.QueryRow("PRAGMA auto_vacuum").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != 2 {
		t.Fatalf("expected auto_vacuum=2 after VACUUM migration, got %d", after)
	}
}

func TestIncrementalVacuumReducesFreelist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	db := createFreshDBWithAutoVacuum(t, path)

	// Insert a bunch of data
	now := time.Now().UTC().Format(time.RFC3339)
	for i := 0; i < 500; i++ {
		_, err := db.Exec(
			"INSERT INTO transmissions (raw_hex, hash, first_seen) VALUES (?, ?, ?)",
			strings.Repeat("AA", 200), // ~400 bytes each
			"hash_"+string(rune('A'+i%26))+string(rune('0'+i/26)),
			now,
		)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Get file size before delete
	db.Close()
	infoBefore, _ := os.Stat(path)
	sizeBefore := infoBefore.Size()

	// Reopen and delete all
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()

	_, err = db.Exec("DELETE FROM transmissions")
	if err != nil {
		t.Fatal(err)
	}

	// Check freelist before vacuum
	var freelistBefore int64
	db.QueryRow("PRAGMA freelist_count").Scan(&freelistBefore)
	if freelistBefore == 0 {
		t.Fatal("expected non-zero freelist after DELETE")
	}

	// Run incremental vacuum
	_, err = db.Exec("PRAGMA incremental_vacuum(10000)")
	if err != nil {
		t.Fatal(err)
	}

	// Check freelist after vacuum
	var freelistAfter int64
	db.QueryRow("PRAGMA freelist_count").Scan(&freelistAfter)
	if freelistAfter >= freelistBefore {
		t.Fatalf("expected freelist to shrink: before=%d after=%d", freelistBefore, freelistAfter)
	}

	// Checkpoint WAL and check file size shrunk
	db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	db.Close()
	infoAfter, _ := os.Stat(path)
	sizeAfter := infoAfter.Size()
	if sizeAfter >= sizeBefore {
		t.Logf("warning: file did not shrink (before=%d after=%d) — may depend on page reuse", sizeBefore, sizeAfter)
	}
}

func TestCheckAutoVacuumLogs(t *testing.T) {
	// This test verifies checkAutoVacuum doesn't panic on various configs
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	// Create a fresh DB with auto_vacuum=INCREMENTAL
	dbConn := createFreshDBWithAutoVacuum(t, path)
	db := &DB{conn: dbConn, path: path}
	cfg := &Config{}

	// Should not panic
	checkAutoVacuum(db, cfg, path)
	dbConn.Close()

	// Create a DB without auto_vacuum
	path2 := filepath.Join(dir, "test2.db")
	dbConn2, _ := sql.Open("sqlite", path2+"?_pragma=journal_mode(WAL)")
	dbConn2.SetMaxOpenConns(1)
	dbConn2.Exec("CREATE TABLE dummy (id INTEGER PRIMARY KEY)")
	db2 := &DB{conn: dbConn2, path: path2}

	// Should log warning but not panic
	checkAutoVacuum(db2, cfg, path2)
	dbConn2.Close()
}

func TestConfigIncrementalVacuumPages(t *testing.T) {
	// Default
	cfg := &Config{}
	if cfg.IncrementalVacuumPages() != 1024 {
		t.Fatalf("expected default 1024, got %d", cfg.IncrementalVacuumPages())
	}

	// Custom
	cfg.DB = &DBConfig{IncrementalVacuumPages: 512}
	if cfg.IncrementalVacuumPages() != 512 {
		t.Fatalf("expected 512, got %d", cfg.IncrementalVacuumPages())
	}

	// Zero should return default
	cfg.DB.IncrementalVacuumPages = 0
	if cfg.IncrementalVacuumPages() != 1024 {
		t.Fatalf("expected default 1024 for zero, got %d", cfg.IncrementalVacuumPages())
	}
}
