// tx_last_seen_backfill — chunked backfill of transmissions.last_seen (#1724).
//
// Stub for the chunked backfill. PR #1691 originally ran the populate as a
// single correlated UPDATE; on a prod-shaped DB (71K tx / 1.5M obs) that
// pinned the SQLite writer for 10-15 min, starving every reader. The fix
// (forthcoming, gated by tx_last_seen_backfill_test.go) replaces this body
// with a batched loop that yields the writer between batches.

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
// to production defaults (see GREEN commit for tuning rationale).
type TxLastSeenBackfillOpts struct {
	BatchSize  int
	YieldDelay time.Duration
	Progress   func(TxLastSeenBackfillProgress)
}

// runTxLastSeenBackfillChunked is the body of the
// tx_last_seen_backfill_v1 async migration. RED stub: still runs the
// single-shot UPDATE that caused #1724. Replaced in the GREEN commit
// with a chunked implementation.
func runTxLastSeenBackfillChunked(ctx context.Context, db *sql.DB, opts TxLastSeenBackfillOpts) (int64, error) {
	start := time.Now()
	res, err := db.ExecContext(ctx, `
		UPDATE transmissions
		SET last_seen = COALESCE((
			SELECT MAX(timestamp) FROM observations WHERE transmission_id = transmissions.id
		), last_seen)
		WHERE last_seen = 0
	`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if opts.Progress != nil {
		opts.Progress(TxLastSeenBackfillProgress{
			RowsProcessed: n,
			RowsTotal:     n,
			BatchNum:      1,
			ElapsedMs:     time.Since(start).Milliseconds(),
		})
	}
	return n, nil
}
