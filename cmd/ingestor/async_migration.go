// Async migration helper — runs schema/backfill work that may take minutes on
// large prod tables WITHOUT blocking ingestor startup.
//
// MIGRATION ANNOTATION CONVENTION (read this before touching migrations):
//
//   Sync schema/data migrations (CREATE INDEX, ALTER TABLE, UPDATE ... WHERE)
//   that run inline during OpenStore() block the ingestor from accepting
//   packets until they finish. On an empty dev DB they return in milliseconds;
//   at prod scale (1.9M+ observations, 80K+ adverts) they can pin the boot
//   for minutes and trigger restart loops. This regression class has bitten us
//   repeatedly (#791 resolved_path backfill, #1483 obs_observer_ts_idx_v1).
//
//   ANY new CREATE INDEX / ALTER TABLE / data-rewrite migration MUST EITHER:
//     1. Run via Store.RunAsyncMigration(...) below (preferred for backfills
//        and any work that may touch >1K rows). The migration is recorded as
//        `pending_async` immediately, returns to the caller (boot proceeds),
//        and completes in a goroutine. Status flips to `done` (or `failed`
//        with an error message) when fn returns.
//     2. Carry the preflight annotation comment immediately above the
//        migration block, e.g.
//             // PREFLIGHT: async=true reason="<one-line justification>"
//        Use this for migrations that are genuinely cheap at any scale
//        (e.g. ALTER TABLE ADD COLUMN, CREATE INDEX on a known-bounded
//        table). The annotation is grepped by
//        ~/.openclaw/skills/pr-preflight/scripts/check-async-migrations.sh
//        — its absence on a touched migration block is a hard-fail gate.
//
//   See MIGRATIONS.md in the repo root for the full policy and examples.

package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
)

// ensureAsyncMigrationsTable creates the bookkeeping table used by
// RunAsyncMigration / AsyncMigrationStatus. Idempotent.
func ensureAsyncMigrationsTable(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS _async_migrations (
			name           TEXT PRIMARY KEY,
			status         TEXT NOT NULL,             -- pending_async | done | failed
			started_at     TEXT NOT NULL DEFAULT (datetime('now')),
			ended_at       TEXT,
			error          TEXT,
			rows_processed INTEGER NOT NULL DEFAULT 0,
			rows_total     INTEGER NOT NULL DEFAULT 0,
			last_update_at TEXT
		)
	`)
	return err
}

// RunAsyncMigration registers `name` as a pending async migration and
// schedules `fn` to run in a background goroutine. It returns to the caller
// immediately so the ingestor can keep booting.
//
// Contract (pinned by async_migration_test.go):
//   - status is `pending_async` IMMEDIATELY after this returns.
//   - fn runs in a goroutine; on success status becomes `done`, on error or
//     panic status becomes `failed` and the error is recorded.
//   - Idempotent: if a row with the same name already exists in `done`
//     state, fn is NOT re-run. If in `failed` or `pending_async` state,
//     fn IS re-scheduled (a previous run may have crashed mid-flight).
//   - The caller's WaitGroup tracks the goroutine so tests/shutdown can
//     wait via Store.WaitForAsyncMigrations().
func (s *Store) RunAsyncMigration(ctx context.Context, name string, fn func(context.Context, *sql.DB) error) error {
	if err := ensureAsyncMigrationsTable(s.db); err != nil {
		return fmt.Errorf("ensure _async_migrations: %w", err)
	}
	if err := ensureAsyncMigrationProgressColumns(s.db); err != nil {
		return fmt.Errorf("ensure _async_migrations progress columns: %w", err)
	}

	var existing string
	row := s.db.QueryRow(`SELECT status FROM _async_migrations WHERE name = ?`, name)
	switch err := row.Scan(&existing); err {
	case nil:
		if existing == "done" {
			return nil // already complete, nothing to do
		}
		// pending_async or failed → reset and retry. Wipe per-row progress
		// so a fresh denominator/processed pair lands on the next callback.
		if _, err := s.db.Exec(`
			UPDATE _async_migrations
			SET status = 'pending_async', started_at = datetime('now'), ended_at = NULL,
			    error = NULL, rows_processed = 0, rows_total = 0, last_update_at = NULL
			WHERE name = ?`, name); err != nil {
			return fmt.Errorf("reset async migration %q: %w", name, err)
		}
		// Also clear the in-memory rate-limit cache so the first new
		// progress write isn't suppressed.
		progressGateMu.Lock()
		delete(progressLastWriteAt, name)
		progressGateMu.Unlock()
	case sql.ErrNoRows:
		if _, err := s.db.Exec(`
			INSERT INTO _async_migrations (name, status) VALUES (?, 'pending_async')`,
			name); err != nil {
			return fmt.Errorf("register async migration %q: %w", name, err)
		}
	default:
		return fmt.Errorf("lookup async migration %q: %w", name, err)
	}

	s.backfillWg.Add(1)
	go func() {
		defer s.backfillWg.Done()
		var runErr error
		defer func() {
			if r := recover(); r != nil {
				runErr = fmt.Errorf("panic: %v", r)
				log.Printf("[async-migration] %q panic recovered: %v", name, r)
			}
			if runErr != nil {
				if _, err := s.db.Exec(`
					UPDATE _async_migrations
					SET status = 'failed', ended_at = datetime('now'), error = ?
					WHERE name = ?`, runErr.Error(), name); err != nil {
					log.Printf("[async-migration] failed to record failure for %q: %v", name, err)
				}
				log.Printf("[async-migration] %q FAILED: %v", name, runErr)
				return
			}
			if _, err := s.db.Exec(`
				UPDATE _async_migrations
				SET status = 'done', ended_at = datetime('now'), error = NULL
				WHERE name = ?`, name); err != nil {
				log.Printf("[async-migration] failed to mark %q done: %v", name, err)
				return
			}
			log.Printf("[async-migration] %q done", name)
		}()
		log.Printf("[async-migration] %q starting (boot continues)", name)
		runErr = fn(ctx, s.db)
	}()

	return nil
}

// AsyncMigrationStatus returns the current status of an async migration
// (one of "pending_async", "done", "failed") or sql.ErrNoRows if no such
// migration has been registered.
func (s *Store) AsyncMigrationStatus(name string) (string, error) {
	if err := ensureAsyncMigrationsTable(s.db); err != nil {
		return "", err
	}
	var status string
	err := s.db.QueryRow(`SELECT status FROM _async_migrations WHERE name = ?`, name).Scan(&status)
	return status, err
}

// WaitForAsyncMigrations blocks until all currently-scheduled async migrations
// finish. Intended for tests + graceful shutdown; production boot path does NOT
// call this (that's the whole point).
func (s *Store) WaitForAsyncMigrations() {
	s.backfillWg.Wait()
}
