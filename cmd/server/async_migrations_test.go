// Tests for async migration server-side surface (#1724).

package main

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func openAsyncTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// PREFLIGHT: async=true reason="test fixture CREATE TABLE on a fresh in-memory SQLite DB — not a real schema migration; runs in test setup only."
	_, err = db.Exec(`
		CREATE TABLE _async_migrations (
			name           TEXT PRIMARY KEY,
			status         TEXT NOT NULL,
			started_at     TEXT,
			ended_at       TEXT,
			error          TEXT,
			rows_processed INTEGER DEFAULT 0,
			rows_total     INTEGER DEFAULT 0,
			last_update_at TEXT
		)
	`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func TestMapAsyncStatus(t *testing.T) {
	cases := map[string]string{
		"pending_async": "running",
		"done":          "done",
		"failed":        "failed",
		"":              "unknown",
		"garbage":       "unknown",
	}
	for in, want := range cases {
		if got := mapAsyncStatus(in); got != want {
			t.Errorf("mapAsyncStatus(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestAnyAsyncMigrationRunning_FalseOnFailed(t *testing.T) {
	infos := []AsyncMigrationInfo{
		{Name: "x", Status: "failed"},
		{Name: "y", Status: "done"},
	}
	if anyAsyncMigrationRunning(infos) {
		t.Errorf("anyAsyncMigrationRunning should be false when no migration is 'running'")
	}
}

func TestAnyAsyncMigrationRunning_TrueOnRunning(t *testing.T) {
	infos := []AsyncMigrationInfo{
		{Name: "x", Status: "done"},
		{Name: "y", Status: "running"},
	}
	if !anyAsyncMigrationRunning(infos) {
		t.Errorf("anyAsyncMigrationRunning should be true when any migration is 'running'")
	}
}

func TestReadAsyncMigrations_EtaAndRateRunning(t *testing.T) {
	db := openAsyncTestDB(t)
	// Fix "now" to a known value relative to started_at.
	fixed := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	asyncMigrationsNow = func() time.Time { return fixed }
	t.Cleanup(func() { asyncMigrationsNow = time.Now })
	invalidateAsyncMigrationsCache()

	// Started 10s ago, processed 100/1000.
	_, err := db.Exec(`INSERT INTO _async_migrations
		(name, status, started_at, rows_processed, rows_total)
		VALUES ('m1','pending_async','2026-06-16 11:59:50',100,1000)`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readAsyncMigrations(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 row, got %d", len(got))
	}
	m := got[0]
	if m.Status != "running" {
		t.Errorf("status=%q, want running", m.Status)
	}
	if m.ElapsedSec < 9.5 || m.ElapsedSec > 10.5 {
		t.Errorf("ElapsedSec=%v, want ~10", m.ElapsedSec)
	}
	// rate = 100/10 = 10 rows/sec; remaining = 900 → eta = 90s.
	if m.RatePerSec < 9.5 || m.RatePerSec > 10.5 {
		t.Errorf("RatePerSec=%v, want ~10", m.RatePerSec)
	}
	if m.EtaSec < 85 || m.EtaSec > 95 {
		t.Errorf("EtaSec=%v, want ~90", m.EtaSec)
	}
}

func TestReadAsyncMigrations_FailedSurfacesErrorMessage(t *testing.T) {
	db := openAsyncTestDB(t)
	invalidateAsyncMigrationsCache()
	asyncMigrationsNow = func() time.Time { return time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { asyncMigrationsNow = time.Now })

	_, err := db.Exec(`INSERT INTO _async_migrations
		(name, status, started_at, ended_at, error, rows_processed, rows_total)
		VALUES ('boom','failed','2026-06-16 11:59:00','2026-06-16 11:59:30','disk full',50,100)`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readAsyncMigrations(db)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Status != "failed" {
		t.Fatalf("expected one failed row, got %+v", got)
	}
	if got[0].ErrorMessage != "disk full" {
		t.Errorf("ErrorMessage=%q, want 'disk full'", got[0].ErrorMessage)
	}
	if got[0].ElapsedSec < 29 || got[0].ElapsedSec > 31 {
		t.Errorf("ElapsedSec=%v, want ~30", got[0].ElapsedSec)
	}
	if anyAsyncMigrationRunning(got) {
		t.Errorf("failed migration must not count as running (banner would stick)")
	}
}

func TestReadAsyncMigrations_DoneClearsErrorMessage(t *testing.T) {
	db := openAsyncTestDB(t)
	invalidateAsyncMigrationsCache()
	_, _ = db.Exec(`INSERT INTO _async_migrations
		(name, status, started_at, ended_at, error)
		VALUES ('ok','done','2026-06-16 11:59:00','2026-06-16 11:59:05','stale')`)
	got, err := readAsyncMigrations(db)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].ErrorMessage != "" {
		t.Errorf("done status must not surface ErrorMessage, got %q", got[0].ErrorMessage)
	}
}

func TestReadAsyncMigrations_PropagatesErrors(t *testing.T) {
	db, _ := sql.Open("sqlite", ":memory:")
	db.Close()
	invalidateAsyncMigrationsCache()
	_, err := readAsyncMigrationsRaw(db)
	if err == nil {
		t.Errorf("closed DB must propagate error, not return nil")
	}
}

func TestParseAsyncTime(t *testing.T) {
	if _, err := parseAsyncTime("2026-06-16T11:59:00Z"); err != nil {
		t.Errorf("RFC3339 should parse: %v", err)
	}
	if _, err := parseAsyncTime("2026-06-16 11:59:00"); err != nil {
		t.Errorf("SQLite datetime should parse: %v", err)
	}
	if _, err := parseAsyncTime("not a time"); err == nil {
		t.Errorf("bogus value must error")
	}
	tz, err := parseAsyncTime("")
	if err != nil || !tz.IsZero() {
		t.Errorf("empty should be zero+nil, got %v / %v", tz, err)
	}
}

func TestReadAsyncMigrations_CachesWithinTTL(t *testing.T) {
	db := openAsyncTestDB(t)
	invalidateAsyncMigrationsCache()
	_, _ = db.Exec(`INSERT INTO _async_migrations(name,status,started_at) VALUES ('a','done','2026-06-16 11:59:00')`)
	g1, _ := readAsyncMigrations(db)
	// Add another row; cached result must NOT include it.
	_, _ = db.Exec(`INSERT INTO _async_migrations(name,status,started_at) VALUES ('b','done','2026-06-16 11:59:00')`)
	g2, _ := readAsyncMigrations(db)
	if len(g1) != len(g2) {
		t.Errorf("cache TTL not honored: g1=%d g2=%d", len(g1), len(g2))
	}
}

func TestErrParseAsyncTime_Message(t *testing.T) {
	e := errParseAsyncTime{s: "x"}
	if !strings.Contains(e.Error(), "x") {
		t.Errorf("error message missing input")
	}
}
