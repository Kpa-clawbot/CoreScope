// Progress-write rate limiter + schema-progress columns for async migrations
// (issue #1724).
//
// Long-running async backfills (tx_last_seen_backfill_v1, future ones) need
// to surface progress so operators can see motion during cold-load. Writing
// every per-batch progress update to the bookkeeping table would compete
// with the migration's own writer lock and add an UPDATE per chunk. Cap to
// ≤1 write/sec per migration; force a final write on terminal calls.
//
// The schema columns (rows_processed, rows_total, last_update_at) are
// additive on legacy DBs via ADD COLUMN; only the "duplicate column" error
// is swallowed — every other error propagates. The CREATE TABLE body
// includes the new columns for fresh installs.

package main

import (
	"database/sql"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"
)

// progressGate throttles per-migration progress writes.
//
// #1735 finding #9 (Group D): we deliberately do NOT use sync.Once to
// suppress repeated runtime warn logs. Sync.Once silences ALL future
// errors — losing observability of an ongoing problem. Instead, we
// rate-limit the warn log to ~1/min using a wall-clock timestamp so
// every error is visible but doesn't flood the log.
var (
	progressGateMu      sync.Mutex
	progressLastWriteAt = map[string]time.Time{}

	// schemaWarnMu guards schemaWarnLastAt. Per-process rate-limiter
	// (NOT per-DB) — the warn is a log-only side effect, not a
	// correctness-affecting cache, so package-level scope is fine.
	schemaWarnMu     sync.Mutex
	schemaWarnLastAt time.Time

	// ensureColumnsOnce guards the per-process ALTER TABLE storm. The
	// migration progress columns only need to be added once per
	// process lifetime; subsequent RunAsyncMigration calls don't need
	// to re-run three ALTER TABLEs every time.
	ensureColumnsOnce sync.Once
	ensureColumnsErr  error
)

// resetSchemaWarnRateLimiterForTest is exported for tests that need to
// re-trigger the warn log on a second failure. Production code never
// calls this.
func resetSchemaWarnRateLimiterForTest() {
	schemaWarnMu.Lock()
	schemaWarnLastAt = time.Time{}
	schemaWarnMu.Unlock()
}

// resetEnsureColumnsOnceForTest lets a test exercise the per-process
// "add columns once" path repeatedly. Production code never calls this.
func resetEnsureColumnsOnceForTest() {
	ensureColumnsOnce = sync.Once{}
	ensureColumnsErr = nil
}

// ensureAsyncMigrationProgressColumns adds rows_processed / rows_total /
// last_update_at to _async_migrations on legacy DBs. Idempotent. Only the
// "duplicate column" SQLite error is swallowed.
//
// #1735 finding #9 (Group D): guarded by sync.Once so a process running
// many async migrations doesn't re-run 3 ALTER TABLE statements per call.
// The column set is fixed at build time so once-per-process is safe.
func ensureAsyncMigrationProgressColumns(db *sql.DB) error {
	ensureColumnsOnce.Do(func() {
		ensureColumnsErr = ensureAsyncMigrationProgressColumnsRaw(db)
	})
	return ensureColumnsErr
}

func ensureAsyncMigrationProgressColumnsRaw(db *sql.DB) error {
	// Make sure the base table exists first (fresh-install path covered).
	if err := ensureAsyncMigrationsTable(db); err != nil {
		return err
	}
	cols := []struct{ name, typ string }{
		{"rows_processed", "INTEGER NOT NULL DEFAULT 0"},
		{"rows_total", "INTEGER NOT NULL DEFAULT 0"},
		{"last_update_at", "TEXT"},
	}
	for _, c := range cols {
		// PREFLIGHT: async=true reason="_async_migrations is the migration bookkeeping table itself — bounded to one row per known migration name (single-digit rows in practice, never grows with data). ALTER TABLE ADD COLUMN on this table is O(rows) and completes in microseconds even on prod-size DBs."
		_, err := db.Exec(fmt.Sprintf(
			`ALTER TABLE _async_migrations ADD COLUMN %s %s`, c.name, c.typ))
		if err != nil && !isDuplicateColumnErr(err) {
			return fmt.Errorf("add column %s: %w", c.name, err)
		}
	}
	return nil
}

// isDuplicateColumnErr matches the SQLite ADD COLUMN duplicate-column
// error.
//
// #1735 finding #8 (Group D): the modernc.org/sqlite driver does NOT
// expose a typed sentinel for "duplicate column". The error path goes
// through *sqlite.Error with Code()=SQLITE_ERROR (1), which is generic
// — every prepare/exec error returns SQLITE_ERROR with the message
// distinguishing the specific failure. So we must match on the message
// text. The current driver (v1.34.5) emits the literal string
// "duplicate column name: <name>" (origin: SQLite's
// sqlite3AddColumn() in src/build.c). The test
// TestIsDuplicateColumnErr_DriverStringPinned in
// async_migration_progress_test.go pins this string so a driver
// upgrade that changes the wording fails the test instead of silently
// breaking idempotency.
func isDuplicateColumnErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "duplicate column")
}

// recordAsyncMigrationProgress writes (processed, total) for `name`, but
// no more than once per second per migration name. Pass terminal=true to
// force the write (used for final stable counts).
func recordAsyncMigrationProgress(db *sql.DB, name string, processed, total int64) error {
	return recordAsyncMigrationProgressEx(db, name, processed, total, false)
}

// recordAsyncMigrationProgressTerminal forces a write of the final stable
// counts, bypassing the rate limiter.
func recordAsyncMigrationProgressTerminal(db *sql.DB, name string, processed, total int64) error {
	return recordAsyncMigrationProgressEx(db, name, processed, total, true)
}

func recordAsyncMigrationProgressEx(db *sql.DB, name string, processed, total int64, terminal bool) error {
	now := time.Now()
	progressGateMu.Lock()
	last, ok := progressLastWriteAt[name]
	if !terminal && ok && now.Sub(last) < time.Second {
		progressGateMu.Unlock()
		return nil
	}
	progressLastWriteAt[name] = now
	progressGateMu.Unlock()

	res, err := db.Exec(`
		UPDATE _async_migrations
		SET rows_processed = ?, rows_total = ?, last_update_at = ?
		WHERE name = ?`,
		processed, total, now.UTC().Format(time.RFC3339), name)
	if err != nil {
		// Most likely schema-missing on a legacy DB that didn't run
		// ensureAsyncMigrationProgressColumns. #1735 finding #9:
		// rate-limit the warn log to ~1/min so every error stays
		// observable (sync.Once silenced all future errors — that
		// hides ongoing problems).
		now := time.Now()
		schemaWarnMu.Lock()
		if schemaWarnLastAt.IsZero() || now.Sub(schemaWarnLastAt) > time.Minute {
			schemaWarnLastAt = now
			log.Printf("[async-migration] progress write failed (rate-limited 1/min): %v", err)
		}
		schemaWarnMu.Unlock()
		return err
	}
	// #1735 finding #7: a UPDATE that affects 0 rows means the migration
	// bookkeeping row is missing — every caller of this function expects
	// RunAsyncMigration to have inserted the row already. Silently
	// returning nil would let backfills "succeed" while their progress
	// surface stays at 0/0 forever. Treat as a hard error so the caller
	// can mark the migration failed.
	n, raErr := res.RowsAffected()
	if raErr != nil {
		return fmt.Errorf("recordAsyncMigrationProgress(%s) RowsAffected: %w", name, raErr)
	}
	if n == 0 {
		return fmt.Errorf("recordAsyncMigrationProgress(%s): no row updated (bookkeeping row missing)", name)
	}
	return nil
}

// resetAsyncMigrationProgress wipes per-migration progress fields on retry
// so the new run's denominator is honest.
func resetAsyncMigrationProgress(db *sql.DB, name string) error {
	progressGateMu.Lock()
	delete(progressLastWriteAt, name)
	progressGateMu.Unlock()
	_, err := db.Exec(`
		UPDATE _async_migrations
		SET rows_processed = 0, rows_total = 0, last_update_at = NULL
		WHERE name = ?`, name)
	return err
}
