// tx_last_seen_backfill — chunked backfill of transmissions.last_seen.
//
// Issue #1724: PR #1691 ran the populate as a single correlated UPDATE; on a
// prod-shaped DB (71K tx / 1.5M obs) that pinned the SQLite writer for 10-15
// min, starving every reader (p95 catastrophic across /api/stats,
// /api/healthz, /api/packets, /api/analytics/hash-sizes, ...). The writer
// path is a SINGLE connection (db.SetMaxOpenConns(1) in OpenStoreWithInterval)
// — every reader queues behind whatever statement currently holds it.
//
// The fix here chunks the UPDATE into batches of `batchSize` rows and sleeps
// `yieldDelay` between batches. Each batch releases + re-acquires the writer
// connection, so reader queries that arrived during the previous batch get
// served in the gap. Progress is reported via the optional callback so the
// migration runner can surface live state on /api/perf and the warm-up
// banner can stay up until the backfill finishes.
//
// Defaults (5000 rows / 100ms sleep) are tuned for prod ARM64 hardware:
// at ~5000 UPDATEs per batch, wall time per batch on the prod DB is
// ~30-80 ms; the 100 ms sleep keeps the writer idle ~55-75% of the time.
// On 1.5M rows that's ~300 batches × ~150 ms = ~45 s of wall time end-to-end
// (vs ~5-7 min of writer-locked dead-air pre-fix). Smaller batches raise
// the overhead-to-work ratio; larger batches risk extending lock windows
// past reader-visible (~200 ms) thresholds.

package main

import (
	"context"
	"database/sql"
	"time"
)

// TxLastSeenBackfillProgress is the snapshot reported to the optional
// progress callback after each batch.
type TxLastSeenBackfillProgress struct {
	RowsProcessed int64
	RowsTotal     int64
	BatchNum      int
	ElapsedMs     int64
}

// TxLastSeenBackfillOpts tunes the chunked backfill. Zero values fall back
// to production defaults.
type TxLastSeenBackfillOpts struct {
	BatchSize  int           // rows per UPDATE chunk (default 5000)
	YieldDelay time.Duration // sleep between batches (default 100ms); negative means no sleep
	Progress   func(TxLastSeenBackfillProgress)
}

const (
	defaultTxBackfillBatchSize  = 5000
	defaultTxBackfillYieldDelay = 100 * time.Millisecond
)

// runTxLastSeenBackfillChunked backfills transmissions.last_seen in bounded
// batches. Returns the total number of rows updated. The function is the
// body of the tx_last_seen_backfill_v1 async migration registered in
// OpenStoreWithInterval (#1690 backfill, #1724 chunking).
//
// Contract (pinned by tx_last_seen_backfill_test.go):
//   - MUST NOT execute a single full-table UPDATE; readers in another
//     goroutine must be able to make forward progress while the backfill
//     runs.
//   - MUST invoke opts.Progress at least once per batch (when non-nil) so
//     the migration runner can surface mid-flight state.
//   - MUST honor ctx cancellation between batches; an in-flight batch
//     completes, then the loop returns ctx.Err().
//   - Idempotent: once `last_seen=0` rows are exhausted the loop exits.
func runTxLastSeenBackfillChunked(ctx context.Context, db *sql.DB, opts TxLastSeenBackfillOpts) (int64, error) {
	batch := opts.BatchSize
	if batch <= 0 {
		batch = defaultTxBackfillBatchSize
	}
	yield := opts.YieldDelay
	if yield == 0 {
		yield = defaultTxBackfillYieldDelay
	}
	if yield < 0 {
		yield = 0
	}

	// One-shot count of pending rows so the progress callback can
	// report ETA. This SELECT is a normal reader on the writer
	// connection — runs once before the loop so it doesn't extend
	// per-batch lock windows.
	//
	// Snapshot the max transmission id at start (#1724): the chunked
	// loop must only process rows that existed when the migration
	// began. Without this bound, new INSERTs that land between
	// batches (every observation insert that creates a fresh hash
	// goes through last_seen=0 → bump) would keep the loop alive
	// indefinitely, deadlocking shutdown paths that wait on
	// backfillWg. New rows are already maintained inline by
	// InsertTransmission's last_seen bumper (#1690 writer path), so
	// the backfill explicitly does NOT need to catch them.
	var maxID int64
	_ = db.QueryRowContext(ctx, `SELECT COALESCE(MAX(id), 0) FROM transmissions`).Scan(&maxID)
	var total int64
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM transmissions WHERE last_seen = 0 AND id <= ?`, maxID).Scan(&total)

	start := time.Now()
	var processed int64
	var batchNum int
	for {
		if err := ctx.Err(); err != nil {
			return processed, err
		}
		res, err := db.ExecContext(ctx, `
			UPDATE transmissions
			SET last_seen = COALESCE((
				SELECT MAX(timestamp) FROM observations WHERE transmission_id = transmissions.id
			), last_seen)
			WHERE id IN (
				SELECT id FROM transmissions WHERE last_seen = 0 AND id <= ? LIMIT ?
			)`, maxID, batch)
		if err != nil {
			return processed, err
		}
		n, _ := res.RowsAffected()
		batchNum++
		processed += n
		if opts.Progress != nil {
			opts.Progress(TxLastSeenBackfillProgress{
				RowsProcessed: processed,
				RowsTotal:     total,
				BatchNum:      batchNum,
				ElapsedMs:     time.Since(start).Milliseconds(),
			})
		}
		if n == 0 {
			return processed, nil
		}
		if yield > 0 {
			// Use a timer so ctx cancellation interrupts the sleep.
			select {
			case <-ctx.Done():
				return processed, ctx.Err()
			case <-time.After(yield):
			}
		}
	}
}
