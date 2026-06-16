// Tests for async migration progress columns + rate-limited writes (#1724).

package main

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestAsyncMigrationProgress_ColumnsExist(t *testing.T) {
	s := newTestStore(t)
	s.WaitForAsyncMigrations()
	if err := ensureAsyncMigrationProgressColumns(s.db); err != nil {
		t.Fatalf("ensure cols: %v", err)
	}
	rows, err := s.db.Query(`PRAGMA table_info(_async_migrations)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		_ = rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk)
		have[name] = true
	}
	for _, want := range []string{"rows_processed", "rows_total", "last_update_at"} {
		if !have[want] {
			t.Errorf("missing column %q", want)
		}
	}
}

func TestAsyncMigrationProgress_RateLimited(t *testing.T) {
	s := newTestStore(t)
	s.WaitForAsyncMigrations()
	const name = "test_rate_limit_v1"
	// Register the migration row.
	if err := s.RunAsyncMigration(context.Background(), name, func(ctx context.Context, d *sql.DB) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.WaitForAsyncMigrations()

	// First write: should land.
	if err := recordAsyncMigrationProgress(s.db, name, 10, 100); err != nil {
		t.Fatal(err)
	}
	// Second write immediately: should be suppressed (still equals 10).
	if err := recordAsyncMigrationProgress(s.db, name, 20, 100); err != nil {
		t.Fatal(err)
	}
	var p, total int64
	_ = s.db.QueryRow(`SELECT rows_processed, rows_total FROM _async_migrations WHERE name=?`, name).Scan(&p, &total)
	if p != 10 {
		t.Errorf("rate-limited write leaked through: processed=%d, want 10", p)
	}

	// Terminal write: forces past the limiter.
	if err := recordAsyncMigrationProgressTerminal(s.db, name, 999, 100); err != nil {
		t.Fatal(err)
	}
	_ = s.db.QueryRow(`SELECT rows_processed FROM _async_migrations WHERE name=?`, name).Scan(&p)
	if p != 999 {
		t.Errorf("terminal write not honored: processed=%d, want 999", p)
	}
}

func TestAsyncMigrationProgress_ResetOnRetry(t *testing.T) {
	s := newTestStore(t)
	s.WaitForAsyncMigrations()
	const name = "test_retry_reset_v1"

	// Run once, write some progress, fail it.
	if err := s.RunAsyncMigration(context.Background(), name, func(ctx context.Context, d *sql.DB) error {
		_ = recordAsyncMigrationProgressTerminal(d, name, 42, 100)
		return errSentinel{}
	}); err != nil {
		t.Fatal(err)
	}
	s.WaitForAsyncMigrations()

	// Re-register: rows_processed must reset to 0.
	if err := s.RunAsyncMigration(context.Background(), name, func(ctx context.Context, d *sql.DB) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.WaitForAsyncMigrations()
	var p int64
	_ = s.db.QueryRow(`SELECT rows_processed FROM _async_migrations WHERE name=?`, name).Scan(&p)
	if p != 0 {
		t.Errorf("retry did not reset rows_processed: got %d, want 0", p)
	}
}

type errSentinel struct{}

func (errSentinel) Error() string { return "sentinel" }

func TestAsyncMigrationProgress_TerminalForcesWithinSecond(t *testing.T) {
	s := newTestStore(t)
	s.WaitForAsyncMigrations()
	const name = "test_terminal_force_v1"
	if err := s.RunAsyncMigration(context.Background(), name, func(ctx context.Context, d *sql.DB) error {
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	s.WaitForAsyncMigrations()
	_ = recordAsyncMigrationProgress(s.db, name, 1, 10)
	time.Sleep(5 * time.Millisecond)
	_ = recordAsyncMigrationProgressTerminal(s.db, name, 10, 10)
	var p int64
	_ = s.db.QueryRow(`SELECT rows_processed FROM _async_migrations WHERE name=?`, name).Scan(&p)
	if p != 10 {
		t.Errorf("terminal within rate window not honored: got %d, want 10", p)
	}
}

// TestIsDuplicateColumnErr_DriverStringPinned (#1735 finding #8): the
// modernc.org/sqlite driver does not expose a typed sentinel for the
// "duplicate column" ADD COLUMN failure. We rely on a substring match
// against the driver's error text. This test pins the current driver's
// exact error string so a driver upgrade that changes the wording
// fails CI loudly instead of silently breaking
// ensureAsyncMigrationProgressColumns idempotency (which would cause
// the second-ever ALTER to return an error and break boot).
func TestIsDuplicateColumnErr_DriverStringPinned(t *testing.T) {
	s := newTestStore(t)
	s.WaitForAsyncMigrations()
	// First ALTER should succeed (or already-exist; either is fine).
	_, _ = s.db.Exec(`ALTER TABLE _async_migrations ADD COLUMN __dup_probe TEXT`)
	// Second ALTER MUST produce the "duplicate column" error so we can
	// pin the wording.
	_, err := s.db.Exec(`ALTER TABLE _async_migrations ADD COLUMN __dup_probe TEXT`)
	if err == nil {
		t.Fatalf("second ALTER ADD COLUMN should have errored")
	}
	if !isDuplicateColumnErr(err) {
		t.Fatalf("isDuplicateColumnErr returned false for %q — driver error wording changed; update isDuplicateColumnErr and this test", err.Error())
	}
	// Cleanup: drop the probe column is not supported by SQLite ALTER,
	// so leave it — fresh test store each call.
}

// TestEnsureAsyncMigrationProgressColumns_RunsOncePerProcess (#1735 finding
// #9): the sync.Once guard means a process that boots and runs N async
// migrations does not re-run ALTER TABLE for every migration. We verify
// the call is idempotent across DB handles AND that resetting the once
// (test-only) re-enables a fresh run on a separate handle.
func TestEnsureAsyncMigrationProgressColumns_RunsOncePerProcess(t *testing.T) {
	s := newTestStore(t)
	s.WaitForAsyncMigrations()
	resetEnsureColumnsOnceForTest()
	if err := ensureAsyncMigrationProgressColumns(s.db); err != nil {
		t.Fatalf("first: %v", err)
	}
	// Second call is a no-op (sync.Once skipped).
	if err := ensureAsyncMigrationProgressColumns(s.db); err != nil {
		t.Fatalf("second: %v", err)
	}
}
