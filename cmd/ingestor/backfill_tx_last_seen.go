package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
)

// backfillTxLastSeenBatch bounds how many transmissions one backfill
// transaction touches before it commits and releases the writer lock.
//
// Releasing between batches is the entire point. writerMu serialises every
// wrapped writer call, so while this migration holds it the MQTT ingest path
// is blocked; the unbounded statement this replaces held it for the whole
// table. Go's sync.Mutex hands off to a waiter that has been blocked for 1ms,
// so an ingest goroutine queued behind a batch is served at the next batch
// boundary rather than after the entire backfill.
//
// 500 rows because the work per row is one indexed MAX() over that
// transmission's observations, far cheaper than the prune's per-row delete
// cascade — and the commit count stays low: a 71k-transmission database is
// 143 transactions, not 71k.
const backfillTxLastSeenBatch = 500

// backfillTxLastSeenBatchIDs selects the next window of transmissions to
// consider. The cursor is doing two jobs and both are load-bearing:
//
//   - It bounds the scan. Each batch resumes at the id the previous one
//     stopped at, so the migration walks the table once in total rather than
//     re-scanning from the start for every batch.
//   - It guarantees termination. A transmission with no observations keeps
//     last_seen = 0 — MAX() over no rows is NULL and COALESCE falls back to
//     the existing value — so a loop keyed on `last_seen = 0` alone would hand
//     itself the same rows forever. Advancing past every row the window
//     returned, filled or not, visits each row exactly once.
//
// ORDER BY id with id > ? is satisfiable from the rowid, which
// TestBackfillTxLastSeenBatchUsesRowidRange pins.
const backfillTxLastSeenBatchIDs = `SELECT id FROM transmissions WHERE last_seen = 0 AND id > ? ORDER BY id LIMIT ?`

// backfillTxLastSeen fills transmissions.last_seen from the newest observation
// of each transmission, in bounded transactions.
//
// Returns the number of transmissions that actually received a value, which is
// not the number of rows considered: rows with no observations are passed over
// and stay at 0, to be filled by the per-observation UPDATE if one ever
// arrives.
//
// On error the rows already committed keep their values and the count so far
// is returned alongside it — those writes are done, and reporting 0 would be
// wrong. The next boot re-runs the migration, which resumes from the first
// row still at 0.
func (s *Store) backfillTxLastSeen(ctx context.Context) (int64, error) {
	var total int64
	var cursor int64

	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}

		var ids []int64
		var updated int64
		// Tagged for writer-perf visibility (#1340), the same way the prune is.
		err := s.WriterTx("tx_last_seen_backfill", func(tx *sql.Tx) error {
			rows, err := tx.QueryContext(ctx, backfillTxLastSeenBatchIDs, cursor, backfillTxLastSeenBatch)
			if err != nil {
				return fmt.Errorf("select batch: %w", err)
			}
			for rows.Next() {
				var id int64
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return fmt.Errorf("scan batch id: %w", err)
				}
				ids = append(ids, id)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return fmt.Errorf("batch rows: %w", err)
			}
			rows.Close()
			if len(ids) == 0 {
				return nil
			}

			// The EXISTS guard keeps the statement from writing a row back to
			// itself: a transmission with no observations has nothing to derive,
			// and without it COALESCE writes that row's existing 0 over its
			// existing 0 — pointless WAL churn, and RowsAffected would then report
			// rows considered rather than rows filled, which is what gets logged.
			res, err := tx.ExecContext(ctx, `
				UPDATE transmissions
				SET last_seen = COALESCE((
					SELECT MAX(timestamp) FROM observations WHERE transmission_id = transmissions.id
				), last_seen)
				WHERE id IN (`+placeholders(len(ids))+`)
				  AND EXISTS (SELECT 1 FROM observations WHERE transmission_id = transmissions.id)`, toArgs(ids)...)
			if err != nil {
				return fmt.Errorf("backfill batch: %w", err)
			}
			updated, _ = res.RowsAffected()
			return nil
		})
		if err != nil {
			return total, err
		}
		if len(ids) == 0 {
			break
		}

		// Past every row the window returned, not just the filled ones.
		cursor = ids[len(ids)-1]
		total += updated
		if len(ids) < backfillTxLastSeenBatch {
			break
		}
	}

	return total, nil
}

// placeholders returns "?, ?, ?" for n arguments. The batch is bounded by
// backfillTxLastSeenBatch, so this stays far below SQLITE_MAX_VARIABLE_NUMBER.
func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func toArgs(ids []int64) []interface{} {
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return args
}

// logBackfillTxLastSeen runs the backfill and reports what it did, so the
// async-migration call site stays a one-liner.
func (s *Store) logBackfillTxLastSeen(ctx context.Context) error {
	log.Println("[migration/async] Backfilling transmissions.last_seen from MAX(observations.timestamp)...")
	n, err := s.backfillTxLastSeen(ctx)
	if err != nil {
		log.Printf("[migration/async] transmissions.last_seen backfill failed after %d rows: %v", n, err)
		return err
	}
	log.Printf("[migration/async] transmissions.last_seen backfill complete: %d rows updated", n)
	return nil
}
