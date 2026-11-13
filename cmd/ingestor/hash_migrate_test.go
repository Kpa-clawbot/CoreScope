package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

// Two encodings of one logical packet: the same direct-route payload, once
// with an empty path and once relayed through hop AA. The content hash skips
// the path, so both hash to the same value.
const (
	hashMigrateRawA = "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976"
	hashMigrateRawB = "0A01AAD69FD7A5A7475DB07337749AE61FA53A4788E976"
)

func openHashMigrateStore(t *testing.T) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "hash-migrate.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	// Nothing existed when the store opened, so the scheduled migration has
	// nothing in range; wait for it so it cannot overlap the test's own run.
	store.WaitForAsyncMigrations()
	return store
}

func insertHashTx(t *testing.T, store *Store, rawHex, hash, firstSeen string, lastSeen int64) int64 {
	t.Helper()
	res, err := store.db.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, last_seen, route_type, payload_type)
		VALUES (?, ?, ?, ?, 2, 2)`, rawHex, hash, firstSeen, lastSeen)
	if err != nil {
		t.Fatalf("insert transmission %s: %v", hash, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func insertHashObs(t *testing.T, store *Store, txID int64, observerIdx, pathJSON interface{}) int64 {
	t.Helper()
	res, err := store.db.Exec(`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp)
		VALUES (?, ?, ?, 1700000000)`, txID, observerIdx, pathJSON)
	if err != nil {
		t.Fatalf("insert observation for tx %d: %v", txID, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func txHash(t *testing.T, store *Store, id int64) string {
	t.Helper()
	var h string
	if err := store.db.QueryRow(`SELECT hash FROM transmissions WHERE id = ?`, id).Scan(&h); err != nil {
		t.Fatalf("hash of tx %d: %v", id, err)
	}
	return h
}

func countWhere(t *testing.T, store *Store, query string, args ...interface{}) int {
	t.Helper()
	var n int
	if err := store.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func TestMigrateContentHashesRewritesStaleHash(t *testing.T) {
	store := openHashMigrateStore(t)
	stale := insertHashTx(t, store, hashMigrateRawA, "deadbeef12345678", "2026-01-01T00:00:00Z", 100)
	obs := insertHashObs(t, store, stale, 1, "[]")
	// Companion-relayed messages carry no raw bytes; their hash is not a
	// content hash and must never be touched.
	noRaw := insertHashTx(t, store, "", "companionhash001", "2026-01-01T00:00:00Z", 100)

	rewritten, merged, err := store.MigrateContentHashes(context.Background(), 100)
	if err != nil {
		t.Fatalf("MigrateContentHashes: %v", err)
	}
	if rewritten != 1 || merged != 0 {
		t.Fatalf("got rewritten=%d merged=%d, want 1, 0", rewritten, merged)
	}
	if got, want := txHash(t, store, stale), ComputeContentHash(hashMigrateRawA); got != want {
		t.Errorf("stale row hash = %s, want %s", got, want)
	}
	if got := txHash(t, store, noRaw); got != "companionhash001" {
		t.Errorf("row without raw_hex was rewritten to %s", got)
	}
	if n := countWhere(t, store, `SELECT COUNT(*) FROM observations WHERE id = ? AND transmission_id = ?`, obs, stale); n != 1 {
		t.Errorf("a rewritten row's observation moved or vanished")
	}
}

// TestMigrateContentHashesMergesCollision covers a stale row whose correct
// hash already belongs to another row: the old formula split one logical
// packet in two, and the migration folds the duplicate into the survivor.
func TestMigrateContentHashesMergesCollision(t *testing.T) {
	store := openHashMigrateStore(t)
	newHash := ComputeContentHash(hashMigrateRawA)
	surv := insertHashTx(t, store, hashMigrateRawA, newHash, "2026-01-02T00:00:00Z", 500)
	dup := insertHashTx(t, store, hashMigrateRawB, "stalestalestale1", "2026-01-01T00:00:00Z", 900)

	insertHashObs(t, store, surv, 1, `["AA"]`)
	insertHashObs(t, store, surv, nil, `["BB"]`)
	// Same observer and path as one the survivor holds: moving it would
	// violate idx_observations_dedup, so it is dropped with its radio row.
	conflict := insertHashObs(t, store, dup, 1, `["AA"]`)
	moved := insertHashObs(t, store, dup, 2, `[]`)
	// NULL observer_idx never conflicts under the unique index, so these
	// move even though the survivor has a NULL-observer row on the same path.
	nullA := insertHashObs(t, store, dup, nil, `["BB"]`)
	nullB := insertHashObs(t, store, dup, nil, nil)
	for _, o := range []int64{conflict, moved} {
		if _, err := store.db.Exec(`INSERT INTO observation_radios (observation_id, radio_id) VALUES (?, 'r')`, o); err != nil {
			t.Fatalf("seed observation_radios: %v", err)
		}
	}
	if _, err := store.db.Exec(`INSERT INTO advert_route_evidence (tx_id, bit) VALUES (?, 1)`, dup); err != nil {
		t.Fatalf("seed advert_route_evidence: %v", err)
	}

	rewritten, merged, err := store.MigrateContentHashes(context.Background(), 100)
	if err != nil {
		t.Fatalf("MigrateContentHashes: %v", err)
	}
	if rewritten != 0 || merged != 1 {
		t.Fatalf("got rewritten=%d merged=%d, want 0, 1", rewritten, merged)
	}

	if n := countWhere(t, store, `SELECT COUNT(*) FROM transmissions WHERE id = ?`, dup); n != 0 {
		t.Error("duplicate transmission still present")
	}
	var firstSeen string
	var lastSeen int64
	if err := store.db.QueryRow(`SELECT first_seen, last_seen FROM transmissions WHERE id = ?`, surv).Scan(&firstSeen, &lastSeen); err != nil {
		t.Fatal(err)
	}
	if firstSeen != "2026-01-01T00:00:00Z" || lastSeen != 900 {
		t.Errorf("survivor first_seen=%s last_seen=%d, want the earliest and the latest of the pair", firstSeen, lastSeen)
	}
	for _, o := range []int64{moved, nullA, nullB} {
		if n := countWhere(t, store, `SELECT COUNT(*) FROM observations WHERE id = ? AND transmission_id = ?`, o, surv); n != 1 {
			t.Errorf("observation %d was not moved onto the survivor", o)
		}
	}
	if n := countWhere(t, store, `SELECT COUNT(*) FROM observations WHERE id = ?`, conflict); n != 0 {
		t.Error("conflicting observation was kept; it duplicates one the survivor already has")
	}
	if n := countWhere(t, store, `SELECT COUNT(*) FROM observations WHERE transmission_id = ?`, surv); n != 5 {
		t.Errorf("survivor holds %d observations, want 5 (2 own + 3 moved)", n)
	}
	if n := countWhere(t, store, `SELECT COUNT(*) FROM observation_radios WHERE observation_id = ?`, conflict); n != 0 {
		t.Error("radio row of the dropped observation survived")
	}
	if n := countWhere(t, store, `SELECT COUNT(*) FROM observation_radios WHERE observation_id = ?`, moved); n != 1 {
		t.Error("radio row of a moved observation was deleted")
	}
	if n := countWhere(t, store, `SELECT COUNT(*) FROM advert_route_evidence WHERE tx_id = ?`, dup); n != 0 {
		t.Error("advert_route_evidence of the duplicate was not cascaded")
	}

	// Idempotent: everything now matches the formula.
	rewritten, merged, err = store.MigrateContentHashes(context.Background(), 100)
	if err != nil || rewritten != 0 || merged != 0 {
		t.Fatalf("second run = (%d, %d, %v), want (0, 0, nil)", rewritten, merged, err)
	}
}

// Two stale rows of one packet, neither holding the correct hash yet: the
// first is rewritten, and the second then collides with it inside the same
// batch and merges. A batch size of 1 also walks the id cursor across batches.
func TestMigrateContentHashesStalePairAcrossBatches(t *testing.T) {
	for _, batch := range []int{1, 100} {
		store := openHashMigrateStore(t)
		a := insertHashTx(t, store, hashMigrateRawA, "staleA0000000000", "2026-01-01T00:00:00Z", 1)
		b := insertHashTx(t, store, hashMigrateRawB, "staleB0000000000", "2026-01-01T00:00:00Z", 1)
		insertHashObs(t, store, b, 7, `["AA"]`)

		rewritten, merged, err := store.MigrateContentHashes(context.Background(), batch)
		if err != nil {
			t.Fatalf("batch %d: %v", batch, err)
		}
		if rewritten != 1 || merged != 1 {
			t.Fatalf("batch %d: got rewritten=%d merged=%d, want 1, 1", batch, rewritten, merged)
		}
		if got := txHash(t, store, a); got != ComputeContentHash(hashMigrateRawA) {
			t.Errorf("batch %d: first row hash = %s", batch, got)
		}
		if n := countWhere(t, store, `SELECT COUNT(*) FROM observations WHERE transmission_id = ?`, a); n != 1 {
			t.Errorf("batch %d: second row's observation did not move to the first", batch)
		}
		if n := countWhere(t, store, `SELECT COUNT(*) FROM transmissions`); n != 1 {
			t.Errorf("batch %d: %d transmissions left, want 1", batch, n)
		}
	}
}

func TestMigrateContentHashesNoOp(t *testing.T) {
	store := openHashMigrateStore(t)
	id := insertHashTx(t, store, hashMigrateRawA, ComputeContentHash(hashMigrateRawA), "2026-01-01T00:00:00Z", 1)
	rewritten, merged, err := store.MigrateContentHashes(context.Background(), 100)
	if err != nil || rewritten != 0 || merged != 0 {
		t.Fatalf("got (%d, %d, %v), want (0, 0, nil)", rewritten, merged, err)
	}
	if got := txHash(t, store, id); got != ComputeContentHash(hashMigrateRawA) {
		t.Errorf("current hash changed to %s", got)
	}
}

func TestMigrateContentHashesStopsOnCancel(t *testing.T) {
	store := openHashMigrateStore(t)
	id := insertHashTx(t, store, hashMigrateRawA, "deadbeef12345678", "2026-01-01T00:00:00Z", 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	rewritten, merged, err := store.MigrateContentHashes(ctx, 100)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want one wrapping context.Canceled (RunAsyncMigration records it as cancelled)", err)
	}
	if rewritten != 0 || merged != 0 {
		t.Errorf("got rewritten=%d merged=%d on a cancelled context", rewritten, merged)
	}
	if got := txHash(t, store, id); got != "deadbeef12345678" {
		t.Errorf("row rewritten despite cancellation: %s", got)
	}
}

// Ported from the deleted server test (#1856): a migration that cannot write
// must report failure, never success, so _async_migrations stays not-done.
func TestMigrateContentHashesReportsWriteFailure(t *testing.T) {
	store := openHashMigrateStore(t)
	insertHashTx(t, store, hashMigrateRawA, "deadbeef12345678", "2026-01-01T00:00:00Z", 1)
	if err := store.db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.MigrateContentHashes(context.Background(), 100); err == nil {
		t.Fatal("expected an error from a closed handle; a nil error would mark the migration done")
	}
}

// TestContentHashMigrationScheduledOnOpen pins the wiring: OpenStore schedules
// the migration over the rows that existed at boot, records it done, and
// leaves rows inserted after boot alone (this process writes current hashes,
// so anything newer is never stale — and the bound keeps the background pass
// from racing writes it has no business touching).
func TestContentHashMigrationScheduledOnOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hash-migrate-boot.db")
	first, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	first.WaitForAsyncMigrations()
	old := insertHashTx(t, first, hashMigrateRawA, "deadbeef12345678", "2026-01-01T00:00:00Z", 1)
	// The first open ran the migration over an empty table and recorded it
	// done. Forget that, so the reopen behaves like the first boot of this
	// build on a DB written by an older one.
	if _, err := first.db.Exec(`DELETE FROM _async_migrations WHERE name = ?`, contentHashMigrationName); err != nil {
		t.Fatal(err)
	}
	first.Close()

	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	fresh := insertHashTx(t, store, "0A00AABBCCDD", "afterboot0000000", "2026-01-01T00:00:00Z", 1)
	store.WaitForAsyncMigrations()

	status, err := store.AsyncMigrationStatus(contentHashMigrationName)
	if err != nil || status != "done" {
		t.Fatalf("status = %q, %v; want done", status, err)
	}
	if got := txHash(t, store, old); got != ComputeContentHash(hashMigrateRawA) {
		t.Errorf("pre-boot stale row not migrated: %s", got)
	}
	if got := txHash(t, store, fresh); got != "afterboot0000000" {
		t.Errorf("row inserted after boot was touched: %s", got)
	}
}
