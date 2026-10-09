// Async data backfills resume through durable bookkeeping. Schema DDL belongs
// exclusively to the offline migration command; runtime callbacks must not
// create or alter tables/indexes. Long backfills use bounded transactions and
// yield between batches so live ingestion can continue.

package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
)

// ensureAsyncMigrationsTable checks bootstrap-owned bookkeeping without DDL.
func ensureAsyncMigrationsTable(db *sql.DB) error {
	var present bool
	if err := db.QueryRow(`SELECT to_regclass('_async_migrations') IS NOT NULL`).Scan(&present); err != nil {
		return err
	}
	if !present {
		return fmt.Errorf("missing migration bookkeeping; run the offline migration command")
	}
	return nil
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

	var existing string
	row := s.db.QueryRow(`SELECT status FROM _async_migrations WHERE name = $1`, name)
	switch err := row.Scan(&existing); err {
	case nil:
		if existing == "done" {
			return nil // already complete, nothing to do
		}
		// pending_async or failed → reset and retry.
		if _, err := s.db.Exec(`
			UPDATE _async_migrations
			SET status = 'pending_async', started_at = to_char(CURRENT_TIMESTAMP AT TIME ZONE 'UTC','YYYY-MM-DD HH24:MI:SS'), ended_at = NULL, error = NULL
			WHERE name = $1`, name); err != nil {
			return fmt.Errorf("reset async migration %q: %w", name, err)
		}
	case sql.ErrNoRows:
		if _, err := s.db.Exec(`
			INSERT INTO _async_migrations (name, status) VALUES ($1, 'pending_async')`,
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
					SET status = 'failed', ended_at = to_char(CURRENT_TIMESTAMP AT TIME ZONE 'UTC','YYYY-MM-DD HH24:MI:SS'), error = $1
					WHERE name = $2`, runErr.Error(), name); err != nil {
					log.Printf("[async-migration] failed to record failure for %q: %v", name, err)
				}
				log.Printf("[async-migration] %q FAILED: %v", name, runErr)
				return
			}
			if _, err := s.db.Exec(`
				UPDATE _async_migrations
				SET status = 'done', ended_at = to_char(CURRENT_TIMESTAMP AT TIME ZONE 'UTC','YYYY-MM-DD HH24:MI:SS'), error = NULL
				WHERE name = $1`, name); err != nil {
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
	err := s.db.QueryRow(`SELECT status FROM _async_migrations WHERE name = $1`, name).Scan(&status)
	return status, err
}

// WaitForAsyncMigrations blocks until all currently-scheduled async migrations
// finish. Intended for tests + graceful shutdown; production boot path does NOT
// call this (that's the whole point).
func (s *Store) WaitForAsyncMigrations() {
	s.backfillWg.Wait()
}
