// Package main: chunked tx_last_seen_v1 backfill (issue #1724).
//
// Background: the original #1690 backfill was a single correlated UPDATE
//
//	UPDATE transmissions
//	SET last_seen = (SELECT MAX(timestamp) FROM observations WHERE transmission_id = transmissions.id)
//	WHERE last_seen = 0
//
// run under the ingestor's SetMaxOpenConns(1) writer. On real-size
// operator DBs (~71K transmissions / ~1.5M observations / ~2GB) it pinned
// the single writer connection for 10-15 minutes after
// backgroundLoadComplete=true, queueing every reader behind
// sqlite_busy_timeout. The result was an unresponsive system long after
// the warm-up banner cleared.
//
// Fix: chunk the work, yield the writer between chunks. Each chunk is one
// bounded UPDATE (size = batchSize transmissions). Between chunks we sleep
// yieldDelay so concurrent readers can slot in. The maxID snapshot taken
// at start ensures concurrent ingest (id > maxID) cannot trap us in an
// infinite loop. Orphan transmissions (no matching observation row) are
// filtered out via EXISTS so the loop always terminates.
//
// Math reality-check (for the operator-scale DB):
//   - 71K transmissions, batchSize=5000 → ~15 chunks
//   - per-chunk cost: bounded scan over (idx_tx_last_seen) + ~5K
//     correlated MAX lookups on observations(transmission_id) → tens of ms
//   - per-chunk yield: 100ms
//   - wall time: ~15 * (~50ms + 100ms) ≈ 2-3s total, with readers slotted
//     in every 150ms ceiling latency.
//   - Previously: one ~10-15min UPDATE held the writer for the full duration.
//
// The original PR description claimed "300 batches × 150ms ≈ 45s" — that
// was wrong; it confused observations (1.5M) with transmissions (71K).
// The real number is ~20x smaller. See PR #1725 review history.

package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// txBackfillProgressFn is invoked AFTER each non-empty batch with the running
// (processed, total) counts AND once more at the end with the final stable
// counts (terminal callback). It is NOT invoked on a stale n=0 batch.
// Callers must tolerate the terminal call being identical to the last
// in-flight call (this is a benign no-op for status surfaces).
type txBackfillProgressFn func(processed, total int64)

// chunkedTxLastSeenBackfill is the orphan-safe, reader-yielding replacement
// for the single-statement #1690 backfill.
//
// Contract:
//   - batchSize MUST be > 0 (rejected at the entrance — no <0 sentinel; explicit failure).
//   - yieldDelay MUST be >= 0 (zero = no sleep, still releases the writer between Execs).
//   - On ctx cancellation BETWEEN batches, the function returns
//     context.Canceled (or ctx.Err()) with the partial-progress counts —
//     it does NOT mark the migration done.
//   - All errors propagate (snapshot maxID, count total, UPDATE, RowsAffected).
//   - Progress callback fires per non-empty batch + once terminal with final
//     counts. #1735 finding #10: the terminal fire is SUPPRESSED when the
//     last in-loop fire already reported the same (processed,total) — happens
//     when the final batch is exactly batchSize-sized and we then break on
//     the next n=0 chunk. Avoids duplicate progress events to listeners.
//   - #1735 finding #14: the progress callback runs inside defer-recover so
//     a panicking callback does NOT crash the ingestor. A recovered panic
//     is converted to an error and returned.
//   - Concurrent INSERTs (id > maxID) are intentionally skipped — they're
//     handled inline by stmtBumpTxLastSeen on the writer fast-path.
//   - Orphan transmissions (no observations) are skipped via EXISTS so the
//     loop terminates deterministically.
func chunkedTxLastSeenBackfill(
	ctx context.Context,
	db *sql.DB,
	batchSize int,
	yieldDelay time.Duration,
	progress txBackfillProgressFn,
) (processed int64, total int64, err error) {
	if batchSize <= 0 {
		return 0, 0, fmt.Errorf("chunkedTxLastSeenBackfill: batchSize must be > 0 (got %d)", batchSize)
	}
	if yieldDelay < 0 {
		return 0, 0, fmt.Errorf("chunkedTxLastSeenBackfill: yieldDelay must be >= 0 (got %v)", yieldDelay)
	}

	// #1735 finding #14 (Group E): wrap the progress callback so a
	// panic inside operator-supplied code (or a buggy bookkeeping
	// write) returns as an error instead of crashing the ingestor
	// goroutine. We also remember the last reported counts so the
	// terminal fire can be suppressed when redundant (finding #10).
	var (
		lastFiredAt  bool
		lastFiredP   int64
		lastFiredT   int64
		callbackErr  error
	)
	safeProgress := func(p, t int64) {
		if progress == nil {
			lastFiredAt = true
			lastFiredP, lastFiredT = p, t
			return
		}
		defer func() {
			if r := recover(); r != nil {
				callbackErr = fmt.Errorf("progress callback panic: %v", r)
			}
		}()
		progress(p, t)
		lastFiredAt = true
		lastFiredP, lastFiredT = p, t
	}

	var maxID int64
	if err := db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(id), 0) FROM transmissions`).Scan(&maxID); err != nil {
		return 0, 0, fmt.Errorf("snapshot transmissions.max(id): %w", err)
	}

	// Count only the rows we actually intend to touch (last_seen=0, has obs,
	// id<=maxID). This gives operators an honest denominator.
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM transmissions t
		WHERE t.last_seen = 0
		  AND t.id <= ?
		  AND EXISTS (SELECT 1 FROM observations o WHERE o.transmission_id = t.id)
	`, maxID).Scan(&total); err != nil {
		return 0, 0, fmt.Errorf("count backfill total: %w", err)
	}

	if total == 0 {
		safeProgress(0, 0)
		if callbackErr != nil {
			return 0, 0, callbackErr
		}
		return 0, 0, nil
	}

	for {
		if cerr := ctx.Err(); cerr != nil {
			safeProgress(processed, total)
			if callbackErr != nil {
				return processed, total, callbackErr
			}
			return processed, total, cerr
		}

		// One bounded chunk. The inner ORDER BY id + LIMIT gives deterministic
		// forward progress; EXISTS skips orphans; id<=maxID keeps concurrent
		// inserts out of scope so the loop terminates.
		res, uerr := db.ExecContext(ctx, `
			UPDATE transmissions
			SET last_seen = (
				SELECT MAX(timestamp) FROM observations
				WHERE transmission_id = transmissions.id
			)
			WHERE id IN (
				SELECT id FROM transmissions
				WHERE last_seen = 0
				  AND id <= ?
				  AND EXISTS (
					SELECT 1 FROM observations WHERE transmission_id = transmissions.id
				  )
				ORDER BY id
				LIMIT ?
			)
		`, maxID, batchSize)
		if uerr != nil {
			return processed, total, fmt.Errorf("chunk update: %w", uerr)
		}
		n, raErr := res.RowsAffected()
		if raErr != nil {
			return processed, total, fmt.Errorf("chunk RowsAffected: %w", raErr)
		}
		if n == 0 {
			// All eligible rows processed (or none left). Do NOT fire a stale
			// progress here; terminal fire happens once outside the loop.
			break
		}
		processed += n
		safeProgress(processed, total)
		if callbackErr != nil {
			return processed, total, callbackErr
		}

		if yieldDelay > 0 {
			t := time.NewTimer(yieldDelay)
			select {
			case <-ctx.Done():
				if !t.Stop() {
					<-t.C
				}
				safeProgress(processed, total)
				if callbackErr != nil {
					return processed, total, callbackErr
				}
				return processed, total, ctx.Err()
			case <-t.C:
				// timer fired; loop continues.
			}
		}
	}

	// Terminal callback: only fire if counts differ from the last
	// in-loop fire (#1735 finding #10). When the last batch is exactly
	// batchSize-sized, the in-loop fire already reported the final
	// (processed,total) — a second identical fire is redundant noise.
	if !lastFiredAt || lastFiredP != processed || lastFiredT != total {
		safeProgress(processed, total)
		if callbackErr != nil {
			return processed, total, callbackErr
		}
	}
	return processed, total, nil
}
