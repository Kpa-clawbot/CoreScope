// Test for issue #1724 — the tx_last_seen backfill MUST chunk its
// UPDATE so SQLite readers can make forward progress while the
// backfill runs. The original PR #1691 implementation ran a single
// correlated UPDATE that pinned the writer 10-15 min on a prod-sized
// DB; this test asserts the chunked behavior (≤ batchSize rows per
// batch + multiple progress callbacks).

package main

import (
	"context"
	"testing"
	"time"
)

// TestIssue1724_TxLastSeenBackfillIsChunked seeds 12k transmissions
// with last_seen=0 and runs runTxLastSeenBackfillChunked with a
// batchSize of 1000. It asserts:
//
//  1. The progress callback fires more than once (proving the loop
//     batches, not single-shots).
//  2. Every per-batch RowsProcessed delta is ≤ batchSize+epsilon
//     (proving each UPDATE is bounded, not full-table).
//
// Pre-fix (single full-table UPDATE) the callback fires exactly once
// with RowsProcessed=12000, failing both assertions on an assertion
// (not a build/import error).
func TestIssue1724_TxLastSeenBackfillIsChunked(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// The OpenStore-scheduled tx_last_seen_backfill_v1 fires against the
	// empty DB; wait for it to complete before seeding so the goroutine
	// doesn't race our INSERTs and consume rows from under the manual
	// backfill call below.
	s.WaitForAsyncMigrations()

	const seedN = 12000
	const batchSize = 1000

	// Seed transmissions with last_seen=0 and one matching observation
	// each so the correlated MAX(timestamp) subquery returns a non-zero
	// value (forces RowsAffected to be non-zero).
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	insTx, err := tx.Prepare(`INSERT INTO transmissions(raw_hex, hash, first_seen, last_seen) VALUES('00','h'||?, '2024-01-01T00:00:00Z', 0)`)
	if err != nil {
		t.Fatalf("prep tx: %v", err)
	}
	insObs, err := tx.Prepare(`INSERT INTO observations(transmission_id, observer_idx, timestamp) VALUES(?, 1, ?)`)
	if err != nil {
		t.Fatalf("prep obs: %v", err)
	}
	for i := 0; i < seedN; i++ {
		res, err := insTx.Exec(i)
		if err != nil {
			t.Fatalf("seed tx %d: %v", i, err)
		}
		id, _ := res.LastInsertId()
		if _, err := insObs.Exec(id, time.Now().Unix()+int64(i)); err != nil {
			t.Fatalf("seed obs %d: %v", i, err)
		}
	}
	insTx.Close()
	insObs.Close()
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var snapshots []TxLastSeenBackfillProgress
	progress := func(p TxLastSeenBackfillProgress) {
		snapshots = append(snapshots, p)
	}

	total, err := runTxLastSeenBackfillChunked(ctx, s.db, TxLastSeenBackfillOpts{
		BatchSize:  batchSize,
		YieldDelay: time.Millisecond,
		Progress:   progress,
	})
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if total != seedN {
		t.Fatalf("total rows updated = %d, want %d", total, seedN)
	}

	// Invariant 1: the loop must batch.
	if len(snapshots) < 2 {
		t.Fatalf("progress callback fired %d times; want ≥ 2 (chunked loop should emit one per batch; pre-fix #1724 emits exactly 1 for the full-table UPDATE)",
			len(snapshots))
	}

	// Invariant 2: per-batch delta must be bounded by batchSize.
	var prev int64
	for i, snap := range snapshots {
		delta := snap.RowsProcessed - prev
		if delta > int64(batchSize) {
			t.Fatalf("snapshot[%d] delta=%d exceeds batchSize=%d; backfill is not chunking (pre-fix #1724 ran one full-table UPDATE)",
				i, delta, batchSize)
		}
		prev = snap.RowsProcessed
	}
}
