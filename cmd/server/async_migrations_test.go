// Tests for the server-side reader of _async_migrations (#1724).
//
// These pin the contract that the warm-up banner + /api/perf depend on:
// running migrations are surfaced with non-zero rate / ETA when the
// snapshot has enough info; completed migrations report status="complete"
// with no remaining work.

package main

import (
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newAsyncMigrationsTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	// PREFLIGHT: async=true reason="in-memory test DB fixture — not a prod migration; mirrors the ingestor's _async_migrations bookkeeping table shape"
	if _, err := db.Exec(`
		CREATE TABLE _async_migrations (
			name           TEXT PRIMARY KEY,
			status         TEXT NOT NULL,
			started_at     TEXT NOT NULL,
			ended_at       TEXT,
			error          TEXT,
			rows_processed INTEGER NOT NULL DEFAULT 0,
			rows_total     INTEGER NOT NULL DEFAULT 0,
			last_update_at TEXT
		)`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func TestReadAsyncMigrations_RunningHasRateAndEta(t *testing.T) {
	db := newAsyncMigrationsTestDB(t)
	startedAt := time.Now().UTC().Add(-10 * time.Second).Format("2006-01-02 15:04:05")
	if _, err := db.Exec(`INSERT INTO _async_migrations
		(name, status, started_at, rows_processed, rows_total)
		VALUES (?, 'pending_async', ?, 5000, 50000)`, "tx_last_seen_backfill_v1", startedAt); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got := readAsyncMigrations(db)
	if len(got) != 1 {
		t.Fatalf("got %d migrations, want 1", len(got))
	}
	m := got[0]
	if m.Status != "running" {
		t.Fatalf("status = %q, want running", m.Status)
	}
	if m.RowsProcessed != 5000 || m.RowsTotal != 50000 {
		t.Fatalf("rows: processed=%d total=%d", m.RowsProcessed, m.RowsTotal)
	}
	if m.Rate <= 0 {
		t.Fatalf("rate should be >0 for running migration with elapsed time, got %v", m.Rate)
	}
	if m.EtaSeconds <= 0 {
		t.Fatalf("eta should be >0 when rows_total > rows_processed, got %d", m.EtaSeconds)
	}
	if !anyAsyncMigrationRunning(got) {
		t.Fatal("anyAsyncMigrationRunning should be true")
	}
}

func TestReadAsyncMigrations_DoneHasCompleteStatus(t *testing.T) {
	db := newAsyncMigrationsTestDB(t)
	startedAt := time.Now().UTC().Add(-30 * time.Second).Format("2006-01-02 15:04:05")
	endedAt := time.Now().UTC().Add(-1 * time.Second).Format("2006-01-02 15:04:05")
	if _, err := db.Exec(`INSERT INTO _async_migrations
		(name, status, started_at, ended_at, rows_processed, rows_total)
		VALUES (?, 'done', ?, ?, 12345, 12345)`,
		"tx_last_seen_backfill_v1", startedAt, endedAt); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got := readAsyncMigrations(db)
	if len(got) != 1 {
		t.Fatalf("got %d", len(got))
	}
	m := got[0]
	if m.Status != "complete" {
		t.Fatalf("status = %q, want complete (done → complete mapping)", m.Status)
	}
	if anyAsyncMigrationRunning(got) {
		t.Fatal("anyAsyncMigrationRunning should be false for done migration")
	}
}

func TestReadAsyncMigrations_TableMissingReturnsNil(t *testing.T) {
	db, _ := sql.Open("sqlite", ":memory:")
	defer db.Close()
	got := readAsyncMigrations(db)
	if got != nil {
		t.Fatalf("expected nil when table missing, got %+v", got)
	}
}
