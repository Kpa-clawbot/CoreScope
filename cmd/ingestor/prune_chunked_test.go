package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// seedAgedTransmissions inserts n transmissions stamped `age` days in the
// past, each with obsPerTx child observations, and returns nothing — the
// caller asserts against the store. Kept local to this file so the batching
// tests own their fixture shape.
func seedAgedTransmissions(t *testing.T, store *Store, n, obsPerTx, ageDays int) {
	t.Helper()
	ts := time.Now().UTC().AddDate(0, 0, -ageDays).Format(time.RFC3339)
	for i := 0; i < n; i++ {
		res, err := store.db.Exec(
			`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json)
			 VALUES (?, ?, ?, 0, 1, 1, '{}')`,
			"AA", fmt.Sprintf("h%d-%d", ageDays, i), ts,
		)
		if err != nil {
			t.Fatalf("seed tx %d: %v", i, err)
		}
		txID, err := res.LastInsertId()
		if err != nil {
			t.Fatalf("seed tx %d LastInsertId: %v", i, err)
		}
		for j := 0; j < obsPerTx; j++ {
			if _, err := store.db.Exec(
				`INSERT INTO observations (transmission_id, observer_idx, direction, snr, rssi, score, path_json, timestamp)
				 VALUES (?, ?, 'rx', 1.0, -100, 0, '[]', ?)`,
				txID, j, time.Now().Unix(),
			); err != nil {
				t.Fatalf("seed obs %d/%d: %v", i, j, err)
			}
		}
	}
}

func countRows(t *testing.T, store *Store, table string) int {
	t.Helper()
	var n int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func openPruneStore(t *testing.T, name string) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// TestPruneOldPacketsDeletesInBoundedBatches is the perf assertion for the
// chunked prune: it enforces that a retention day is deleted across many
// bounded transactions rather than one long-held writer lock.
//
// The old implementation issued exactly ONE WriterTx regardless of row
// count, holding writerMu — and therefore blocking MQTT ingest — for the
// whole delete. Measured on a production instance doing ~260k
// observations/day that was prune_packets hold_ms_p50 = 33905, with
// mqtt_handler wait_ms_max = 35797.
//
// Counting prune_packets transactions is the deterministic proxy for that
// characteristic: N transactions means the lock was released N-1 times, so
// the worst-case ingest stall is one batch rather than the full day.
func TestPruneOldPacketsDeletesInBoundedBatches(t *testing.T) {
	store := openPruneStore(t, "prune-batched.db")

	// Two full batches plus a partial one. The partial batch is what ends the
	// loop, so no terminating empty batch should run.
	const aged = pruneBatchTransmissions*2 + 37
	seedAgedTransmissions(t, store, aged, 2, 10)

	ResetWriterStatsForTest()

	n, err := store.PruneOldPackets(5)
	if err != nil {
		t.Fatalf("PruneOldPackets: %v", err)
	}
	if n != aged {
		t.Fatalf("expected %d transmissions pruned, got %d", aged, n)
	}

	// 2 full batches + 1 partial batch, which proves nothing is left.
	wantTx := int64(3)
	got := store.WriterStatsSnapshot()["prune_packets"]
	if got.Count != wantTx {
		t.Fatalf("expected %d prune_packets transactions for %d rows at batch size %d, got %d "+
			"(1 means the delete was not chunked and holds the writer lock for the whole retention day)",
			wantTx, aged, pruneBatchTransmissions, got.Count)
	}

	if remaining := countRows(t, store, "transmissions"); remaining != 0 {
		t.Fatalf("expected all aged transmissions gone, %d remain", remaining)
	}
	if remaining := countRows(t, store, "observations"); remaining != 0 {
		t.Fatalf("expected all child observations gone, %d remain", remaining)
	}
}

// TestPruneOldPacketsExactMultipleTerminates covers the one case the
// short-batch exit cannot catch on its own: when the aged rows are an exact
// multiple of the batch size, the last batch comes back full, so the loop
// must run one more — empty — batch to learn that nothing is left.
func TestPruneOldPacketsExactMultipleTerminates(t *testing.T) {
	store := openPruneStore(t, "prune-exact.db")

	const aged = pruneBatchTransmissions * 2
	seedAgedTransmissions(t, store, aged, 1, 10)

	ResetWriterStatsForTest()

	n, err := store.PruneOldPackets(5)
	if err != nil {
		t.Fatalf("PruneOldPackets: %v", err)
	}
	if n != aged {
		t.Fatalf("expected %d pruned, got %d", aged, n)
	}

	// 2 full batches + 1 empty batch that finds nothing and ends the loop.
	if got := store.WriterStatsSnapshot()["prune_packets"].Count; got != 3 {
		t.Fatalf("expected 3 prune_packets transactions for an exact multiple of the batch size, got %d", got)
	}
	if remaining := countRows(t, store, "transmissions"); remaining != 0 {
		t.Fatalf("expected all aged transmissions gone, %d remain", remaining)
	}
	if remaining := countRows(t, store, "observations"); remaining != 0 {
		t.Fatalf("expected all child observations gone, %d remain", remaining)
	}
}

// TestPruneOldPacketsSpansBatchesAndKeepsFreshRows covers the correctness
// risk the batching introduces: with a LIMIT on both statements, a cutoff
// that straddles several batches must still delete every aged row and no
// fresh one, and must not orphan observations.
func TestPruneOldPacketsSpansBatchesAndKeepsFreshRows(t *testing.T) {
	store := openPruneStore(t, "prune-mixed.db")

	const aged = pruneBatchTransmissions + 11
	const fresh = 17
	const obsPerTx = 3
	seedAgedTransmissions(t, store, aged, obsPerTx, 10)
	seedAgedTransmissions(t, store, fresh, obsPerTx, 0)

	n, err := store.PruneOldPackets(5)
	if err != nil {
		t.Fatalf("PruneOldPackets: %v", err)
	}
	if n != aged {
		t.Fatalf("expected %d pruned, got %d", aged, n)
	}

	if remaining := countRows(t, store, "transmissions"); remaining != fresh {
		t.Fatalf("expected %d fresh transmissions kept, got %d", fresh, remaining)
	}
	if remaining := countRows(t, store, "observations"); remaining != fresh*obsPerTx {
		t.Fatalf("expected %d observations kept, got %d", fresh*obsPerTx, remaining)
	}

	// No observation may reference a transmission that is gone.
	var orphans int
	if err := store.db.QueryRow(`
		SELECT COUNT(*) FROM observations o
		LEFT JOIN transmissions t ON t.id = o.transmission_id
		WHERE t.id IS NULL`).Scan(&orphans); err != nil {
		t.Fatalf("orphan check: %v", err)
	}
	if orphans != 0 {
		t.Fatalf("expected no orphaned observations, got %d", orphans)
	}
}

// TestPruneOldPacketsDisabledTakesNoWriterLock pins the 0/negative
// short-circuit: retention disabled must not open a transaction at all.
func TestPruneOldPacketsDisabledTakesNoWriterLock(t *testing.T) {
	store := openPruneStore(t, "prune-disabled.db")
	seedAgedTransmissions(t, store, 5, 1, 10)

	ResetWriterStatsForTest()

	for _, days := range []int{0, -1} {
		n, err := store.PruneOldPackets(days)
		if err != nil {
			t.Fatalf("PruneOldPackets(%d): %v", days, err)
		}
		if n != 0 {
			t.Fatalf("PruneOldPackets(%d) = %d, want 0", days, n)
		}
	}

	if got := store.WriterStatsSnapshot()["prune_packets"].Count; got != 0 {
		t.Fatalf("expected no prune_packets transactions when retention is disabled, got %d", got)
	}
	if remaining := countRows(t, store, "transmissions"); remaining != 5 {
		t.Fatalf("expected 5 transmissions untouched, got %d", remaining)
	}
}

// TestPruneOldPacketsNothingToDeleteRunsOneEmptyBatch documents the
// steady-state cost when nothing has aged out yet: a single empty batch, then
// the loop exits. That batch is only cheap because the subquery is walked off
// idx_transmissions_first_seen — see
// TestPruneAgedTransmissionIDsUsesFirstSeenIndex.
func TestPruneOldPacketsNothingToDeleteRunsOneEmptyBatch(t *testing.T) {
	store := openPruneStore(t, "prune-noop.db")
	seedAgedTransmissions(t, store, 9, 2, 0)

	ResetWriterStatsForTest()

	n, err := store.PruneOldPackets(5)
	if err != nil {
		t.Fatalf("PruneOldPackets: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 pruned, got %d", n)
	}
	if got := store.WriterStatsSnapshot()["prune_packets"].Count; got != 1 {
		t.Fatalf("expected exactly 1 empty prune_packets transaction, got %d", got)
	}
	if remaining := countRows(t, store, "transmissions"); remaining != 9 {
		t.Fatalf("expected 9 transmissions kept, got %d", remaining)
	}
}

// TestPruneAgedTransmissionIDsUsesFirstSeenIndex pins the query plan of the
// batch subquery and of both statements that embed it.
//
// Ordering the batch by id instead of first_seen makes SQLite drop
// idx_transmissions_first_seen for a rowid SCAN. On a 1M-row table that took
// the terminating, nothing-left batch from ~10µs to ~73ms — once per statement,
// under writerMu, in the state an instance is in whenever nothing has aged out.
// Transaction counts cannot see that regression, so the plan is the assertion.
func TestPruneAgedTransmissionIDsUsesFirstSeenIndex(t *testing.T) {
	store := openPruneStore(t, "prune-plan.db")
	seedAgedTransmissions(t, store, 20, 2, 10)

	cutoff := time.Now().UTC().AddDate(0, 0, -5).Format(time.RFC3339)
	for name, q := range map[string]string{
		"batch subquery":       pruneAgedTransmissionIDs,
		"observations delete":  pruneObservationsBatch,
		"transmissions delete": pruneTransmissionsBatch,
	} {
		rows, err := store.db.Query("EXPLAIN QUERY PLAN "+q, cutoff, pruneBatchTransmissions)
		if err != nil {
			t.Fatalf("%s: EXPLAIN QUERY PLAN: %v", name, err)
		}
		var steps []string
		for rows.Next() {
			var id, parent, notused int
			var detail string
			if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
				rows.Close()
				t.Fatalf("%s: scan plan row: %v", name, err)
			}
			steps = append(steps, detail)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("%s: plan rows: %v", name, err)
		}
		rows.Close()
		plan := strings.Join(steps, " | ")

		if !strings.Contains(plan, "idx_transmissions_first_seen") {
			t.Errorf("%s: plan does not use idx_transmissions_first_seen: %s", name, plan)
		}
		if strings.Contains(plan, "SCAN transmissions") {
			t.Errorf("%s: plan scans transmissions, so the empty terminating batch walks the whole table under writerMu: %s", name, plan)
		}
		if strings.Contains(plan, "TEMP B-TREE") {
			t.Errorf("%s: plan sorts in a temp b-tree instead of walking the index in order: %s", name, plan)
		}
	}
}
