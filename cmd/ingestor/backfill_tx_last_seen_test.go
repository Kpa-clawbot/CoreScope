package main

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// openBackfillStore is the prune tests' fixture shape: a real store on a
// throwaway file, so the backfill runs against the schema it runs against in
// production rather than a hand-rolled subset.
func openBackfillStore(t *testing.T, name string) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	// OpenStore schedules tx_last_seen_backfill_v1 itself, in a goroutine. Seed
	// before it finishes and the migration races the test for the same rows —
	// which is how this fixture first reported 1036 of 1037 rows backfilled.
	store.WaitForAsyncMigrations()
	return store
}

// seedTxWithObservations inserts n transmissions with last_seen = 0, each
// carrying obsPerTx observations whose timestamps are the row's own index plus
// an offset, so every transmission has a distinct, checkable MAX(timestamp).
// Returns the expected last_seen per transmission id.
func seedTxWithObservations(t *testing.T, store *Store, n, obsPerTx int) map[int64]int64 {
	t.Helper()
	want := map[int64]int64{}
	// transmissions.hash is UNIQUE, and a test may seed twice.
	seedRun := nextBackfillSeedRun()
	base := time.Now().UTC().Add(-24 * time.Hour).Unix()
	for i := 0; i < n; i++ {
		res, err := store.db.Exec(
			`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json, last_seen)
			 VALUES ('AA', ?, ?, 0, 1, 1, '{}', 0)`,
			fmt.Sprintf("backfill-%d-%d", seedRun, i), time.Now().UTC().Format(time.RFC3339),
		)
		if err != nil {
			t.Fatalf("seed tx %d: %v", i, err)
		}
		txID, err := res.LastInsertId()
		if err != nil {
			t.Fatalf("seed tx %d id: %v", i, err)
		}
		var max int64
		for j := 0; j < obsPerTx; j++ {
			ts := base + int64(i*10+j)
			if ts > max {
				max = ts
			}
			if _, err := store.db.Exec(
				`INSERT INTO observations (transmission_id, observer_idx, direction, snr, rssi, score, path_json, timestamp)
				 VALUES (?, ?, 'rx', 1.0, -100, 0, '[]', ?)`,
				txID, j, ts,
			); err != nil {
				t.Fatalf("seed obs %d/%d: %v", i, j, err)
			}
		}
		if obsPerTx > 0 {
			want[txID] = max
		}
	}
	return want
}

func txLastSeen(t *testing.T, store *Store, id int64) int64 {
	t.Helper()
	var v int64
	if err := store.db.QueryRow(`SELECT last_seen FROM transmissions WHERE id = ?`, id).Scan(&v); err != nil {
		t.Fatalf("read last_seen for %d: %v", id, err)
	}
	return v
}

// TestBackfillTxLastSeenRunsInBoundedBatches is the perf assertion. The
// migration this replaces issued ONE unbounded UPDATE with a correlated
// subquery per row, which on an operator database (71k tx / 1.5M obs) wedged
// every reader behind the writer lock for 10-15 minutes (#1724). Counting
// writer transactions is the deterministic proxy: N transactions means the
// lock was released N-1 times, so the worst-case stall is one batch.
func TestBackfillTxLastSeenRunsInBoundedBatches(t *testing.T) {
	store := openBackfillStore(t, "backfill-batched.db")
	const n = backfillTxLastSeenBatch*2 + 37
	want := seedTxWithObservations(t, store, n, 2)

	ResetWriterStatsForTest()

	updated, err := store.backfillTxLastSeen(context.Background())
	if err != nil {
		t.Fatalf("backfillTxLastSeen: %v", err)
	}
	if updated != int64(n) {
		t.Fatalf("updated %d transmissions, want %d", updated, n)
	}

	// 2 full batches + 1 partial, which ends the loop.
	if got := store.WriterStatsSnapshot()["tx_last_seen_backfill"].Count; got != 3 {
		t.Fatalf("expected 3 writer transactions for %d rows at batch size %d, got %d "+
			"(1 means the backfill is not chunked and holds the writer lock for the whole table)",
			n, backfillTxLastSeenBatch, got)
	}

	for id, ts := range want {
		if got := txLastSeen(t, store, id); got != ts {
			t.Fatalf("transmission %d: last_seen = %d, want %d", id, got, ts)
		}
	}
}

// TestBackfillTxLastSeenTerminatesOnRowsItCannotFill is the correctness risk
// the batching introduces. A transmission with no observations keeps
// last_seen = 0, so a loop that re-selects on `last_seen = 0` alone would hand
// itself the same rows forever. The cursor is what makes each row be visited
// once; without it this test hangs rather than fails, which is why the batch
// count is asserted too.
func TestBackfillTxLastSeenTerminatesOnRowsItCannotFill(t *testing.T) {
	store := openBackfillStore(t, "backfill-unfillable.db")
	seedTxWithObservations(t, store, 5, 0) // no observations: nothing to backfill
	want := seedTxWithObservations(t, store, 3, 1)

	ResetWriterStatsForTest()

	done := make(chan struct{})
	var updated int64
	var err error
	go func() {
		updated, err = store.backfillTxLastSeen(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("backfillTxLastSeen did not terminate: rows it cannot fill are being re-selected")
	}
	if err != nil {
		t.Fatalf("backfillTxLastSeen: %v", err)
	}
	if updated != 3 {
		t.Fatalf("updated %d, want 3 (the five observation-less rows must not count)", updated)
	}
	if got := store.WriterStatsSnapshot()["tx_last_seen_backfill"].Count; got != 1 {
		t.Fatalf("expected the 8 rows to fit one batch, got %d transactions", got)
	}
	for id, ts := range want {
		if got := txLastSeen(t, store, id); got != ts {
			t.Fatalf("transmission %d: last_seen = %d, want %d", id, got, ts)
		}
	}
}

// TestBackfillTxLastSeenLeavesFilledRowsAlone pins the WHERE clause: a row
// that already carries a value is the steady-state maintained by the
// per-observation UPDATE, and re-deriving it would undo a value newer than the
// observations this database still holds after a retention prune.
func TestBackfillTxLastSeenLeavesFilledRowsAlone(t *testing.T) {
	store := openBackfillStore(t, "backfill-filled.db")
	want := seedTxWithObservations(t, store, 3, 1)

	var pinned int64
	for id := range want {
		pinned = id
		break
	}
	const sentinel = int64(4102444800) // 2100-01-01, far from any seeded timestamp
	if _, err := store.db.Exec(`UPDATE transmissions SET last_seen = ? WHERE id = ?`, sentinel, pinned); err != nil {
		t.Fatal(err)
	}

	if _, err := store.backfillTxLastSeen(context.Background()); err != nil {
		t.Fatalf("backfillTxLastSeen: %v", err)
	}

	if got := txLastSeen(t, store, pinned); got != sentinel {
		t.Fatalf("already-filled transmission %d: last_seen = %d, want it untouched at %d", pinned, got, sentinel)
	}
}

// TestBackfillTxLastSeenBatchUsesRowidRange pins the query plan of the batch
// selector. The cursor exists so each batch resumes where the last one
// stopped; if a future edit costs it the rowid range, every batch rescans the
// table from the start and the chunking makes the migration slower than the
// single statement it replaced. Transaction counts cannot see that.
func TestBackfillTxLastSeenBatchUsesRowidRange(t *testing.T) {
	store := openBackfillStore(t, "backfill-plan.db")
	seedTxWithObservations(t, store, 20, 1)

	rows, err := store.db.Query("EXPLAIN QUERY PLAN "+backfillTxLastSeenBatchIDs, 0, backfillTxLastSeenBatch)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()
	var plan string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatalf("scan plan row: %v", err)
		}
		plan += detail + "\n"
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("plan rows: %v", err)
	}
	// The shape that matters is a SEARCH bounded by the cursor, whichever index
	// SQLite picks — today the partial idx_tx_last_seen_zero, which covers it
	// outright. A SCAN here means every batch re-reads the table from the start.
	if !containsAll(plan, "SEARCH", "transmissions", "(id>?)") {
		t.Fatalf("batch selector does not resume from the cursor; plan was:\n%s", plan)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		found := false
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// nextBackfillSeedRun hands each seeding call its own hash namespace.
var backfillSeedRuns int

func nextBackfillSeedRun() int {
	backfillSeedRuns++
	return backfillSeedRuns
}
