package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/meshcore-analyzer/mbcapqueue"
)

// TestRunMultibyteCapPersist_AppliesSnapshot enforces the architectural
// invariant from #1289 + #1322 + #1324 follow-up: the multi-byte
// capability columns (multibyte_sup / multibyte_evidence) on
// nodes / inactive_nodes MUST be written by the ingestor, NEVER by the
// read-only server. The server publishes a snapshot file via
// internal/mbcapqueue; the ingestor's maintenance loop applies it here.
//
// Pre-relocation (PR #1324 as-shipped), the server held a write handle
// and executed UPDATE … nodes SET multibyte_sup directly — which is
// impossible after #1289 made the server's *sql.DB read-only. This test
// asserts the relocated path: snapshot in → UPDATEs out, from the
// ingestor side.
func TestRunMultibyteCapPersist_AppliesSnapshot(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	// Seed two nodes: one active, one inactive.
	if _, err := store.db.Exec(`INSERT INTO nodes (public_key, name, role, last_seen, multibyte_sup, multibyte_evidence)
		VALUES ('aa11', 'Alpha', 'repeater', '2026-01-01T00:00:00Z', 0, NULL)`); err != nil {
		t.Fatalf("seed nodes: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO inactive_nodes (public_key, name, role, last_seen, multibyte_sup, multibyte_evidence)
		VALUES ('bb22', 'Bravo', 'repeater', '2025-01-01T00:00:00Z', 0, NULL)`); err != nil {
		t.Fatalf("seed inactive_nodes: %v", err)
	}
	// Seed a third node already confirmed, then send "unknown" for it —
	// the data-destruction guard must keep its DB value.
	if _, err := store.db.Exec(`INSERT INTO nodes (public_key, name, role, last_seen, multibyte_sup, multibyte_evidence)
		VALUES ('cc33', 'Charlie', 'repeater', '2026-01-01T00:00:00Z', 2, 'advert')`); err != nil {
		t.Fatalf("seed cc33: %v", err)
	}

	snap := mbcapqueue.Snapshot{Entries: []mbcapqueue.Entry{
		{PublicKey: "aa11", Status: "confirmed", Evidence: "advert"},
		{PublicKey: "bb22", Status: "suspected", Evidence: "path"},
		{PublicKey: "cc33", Status: "unknown"}, // must NOT overwrite
	}}
	if err := mbcapqueue.WriteSnapshot(dbPath, snap); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}
	// Sanity: snapshot file landed where we expect.
	if _, err := os.Stat(filepath.Join(filepath.Dir(dbPath), mbcapqueue.QueueDirName, mbcapqueue.SnapshotFileName)); err != nil {
		t.Fatalf("snapshot not on disk: %v", err)
	}

	stats, err := store.RunMultibyteCapPersist()
	if err != nil {
		t.Fatalf("RunMultibyteCapPersist: %v", err)
	}
	if stats.ReadEntries != 3 {
		t.Errorf("ReadEntries = %d, want 3", stats.ReadEntries)
	}
	if stats.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1 (the unknown entry)", stats.Skipped)
	}
	if stats.UpdatedActive == 0 {
		t.Errorf("UpdatedActive = 0; expected aa11 to be updated in nodes")
	}
	if stats.UpdatedInactive == 0 {
		t.Errorf("UpdatedInactive = 0; expected bb22 to be updated in inactive_nodes")
	}

	// Verify DB state.
	var sup int
	var evid string
	if err := store.db.QueryRow(`SELECT multibyte_sup, COALESCE(multibyte_evidence,'') FROM nodes WHERE public_key='aa11'`).Scan(&sup, &evid); err != nil {
		t.Fatalf("read aa11: %v", err)
	}
	if sup != 2 || evid != "advert" {
		t.Errorf("aa11 after persist: sup=%d evid=%q, want sup=2 evid=advert", sup, evid)
	}
	if err := store.db.QueryRow(`SELECT multibyte_sup, COALESCE(multibyte_evidence,'') FROM inactive_nodes WHERE public_key='bb22'`).Scan(&sup, &evid); err != nil {
		t.Fatalf("read bb22: %v", err)
	}
	if sup != 1 || evid != "path" {
		t.Errorf("bb22 after persist: sup=%d evid=%q, want sup=1 evid=path", sup, evid)
	}
	// Data-destruction guard: cc33 must still be confirmed=2/'advert'.
	if err := store.db.QueryRow(`SELECT multibyte_sup, COALESCE(multibyte_evidence,'') FROM nodes WHERE public_key='cc33'`).Scan(&sup, &evid); err != nil {
		t.Fatalf("read cc33: %v", err)
	}
	if sup != 2 || evid != "advert" {
		t.Errorf("cc33 was overwritten by unknown entry: sup=%d evid=%q, want sup=2 evid=advert", sup, evid)
	}
}

// TestRunMultibyteCapPersist_NoSnapshot_NoOp verifies that the persist
// step is a clean no-op when the server hasn't written a snapshot yet
// (cold start; the analytics cycle takes ~15s after server boot).
func TestRunMultibyteCapPersist_NoSnapshot_NoOp(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	stats, err := store.RunMultibyteCapPersist()
	if err != nil {
		t.Fatalf("RunMultibyteCapPersist (no snapshot): %v", err)
	}
	if stats.ReadEntries != 0 || stats.UpdatedActive != 0 || stats.UpdatedInactive != 0 {
		t.Errorf("expected zero-valued stats on cold start, got %+v", stats)
	}
}

// TestRunMultibyteCapPersist_RoundTrip exercises the full end-to-end
// contract claimed by PR #1324: the server writes a snapshot, the
// ingestor persists it, and after a simulated restart (close + reopen
// the store) the DB still carries the persisted state.
//
// The audit (#1386) flagged this as the #1 missing test: the two halves
// (persist / read-back) were each tested in isolation, but no single
// test proved the persist path produces a database state the loader
// can later consume — so a column-rename or snapshot-version drift
// would slip past.
func TestRunMultibyteCapPersist_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// --- Phase 1: open store, seed, persist snapshot ---
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO nodes (public_key, name, role, last_seen, multibyte_sup, multibyte_evidence)
		VALUES ('dd44', 'Delta', 'repeater', '2026-01-01T00:00:00Z', 0, NULL)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO inactive_nodes (public_key, name, role, last_seen, multibyte_sup, multibyte_evidence)
		VALUES ('ee55', 'Echo', 'companion', '2025-12-01T00:00:00Z', 0, NULL)`); err != nil {
		t.Fatalf("seed inactive: %v", err)
	}
	snap := mbcapqueue.Snapshot{Entries: []mbcapqueue.Entry{
		{PublicKey: "dd44", Status: "confirmed", Evidence: "advert"},
		{PublicKey: "ee55", Status: "suspected", Evidence: "path"},
	}}
	if err := mbcapqueue.WriteSnapshot(dbPath, snap); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}
	if _, err := store.RunMultibyteCapPersist(); err != nil {
		t.Fatalf("RunMultibyteCapPersist: %v", err)
	}
	// Capture original state for round-trip comparison.
	var origActiveSup, origInactiveSup int
	var origActiveEvid, origInactiveEvid string
	if err := store.db.QueryRow(`SELECT multibyte_sup, COALESCE(multibyte_evidence,'') FROM nodes WHERE public_key='dd44'`).Scan(&origActiveSup, &origActiveEvid); err != nil {
		t.Fatalf("read dd44 (phase1): %v", err)
	}
	if err := store.db.QueryRow(`SELECT multibyte_sup, COALESCE(multibyte_evidence,'') FROM inactive_nodes WHERE public_key='ee55'`).Scan(&origInactiveSup, &origInactiveEvid); err != nil {
		t.Fatalf("read ee55 (phase1): %v", err)
	}
	// Simulate restart: drop the in-memory Store entirely.
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// --- Phase 2: fresh Store, verify persisted state survived ---
	store2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore (reopen): %v", err)
	}
	defer store2.Close()
	var sup int
	var evid string
	if err := store2.db.QueryRow(`SELECT multibyte_sup, COALESCE(multibyte_evidence,'') FROM nodes WHERE public_key='dd44'`).Scan(&sup, &evid); err != nil {
		t.Fatalf("read dd44 after reopen: %v", err)
	}
	if sup != origActiveSup || evid != origActiveEvid {
		t.Errorf("dd44 after restart: sup=%d evid=%q, want sup=%d evid=%q", sup, evid, origActiveSup, origActiveEvid)
	}
	if sup != 2 || evid != "advert" {
		t.Errorf("dd44 after restart: sup=%d evid=%q, want sup=2 evid=advert", sup, evid)
	}
	if err := store2.db.QueryRow(`SELECT multibyte_sup, COALESCE(multibyte_evidence,'') FROM inactive_nodes WHERE public_key='ee55'`).Scan(&sup, &evid); err != nil {
		t.Fatalf("read ee55 after reopen: %v", err)
	}
	if sup != origInactiveSup || evid != origInactiveEvid {
		t.Errorf("ee55 after restart: sup=%d evid=%q, want sup=%d evid=%q", sup, evid, origInactiveSup, origInactiveEvid)
	}
	if sup != 1 || evid != "path" {
		t.Errorf("ee55 after restart: sup=%d evid=%q, want sup=1 evid=path", sup, evid)
	}
}

// TestRunMultibyteCapPersist_MalformedSnapshot verifies the persist
// path is safe against a corrupted/truncated snapshot file: it must
// return without error (no-op), MUST NOT crash, AND MUST log a warning
// distinguishing the malformed case from the steady-state "no
// snapshot yet" cold-start case.
//
// Audit (#1386, kent-beck) flagged: "Snapshot file malformed /
// truncated / wrong-version — RunMultibyteCapPersist error vs.
// silent-skip behavior is unspecified by any test."
func TestRunMultibyteCapPersist_MalformedSnapshot(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	// Write malformed JSON directly to the snapshot path.
	if err := mbcapqueue.EnsureDir(dbPath); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	if err := os.WriteFile(mbcapqueue.SnapshotPath(dbPath), []byte("not-json{{{garbage"), 0o644); err != nil {
		t.Fatalf("write malformed: %v", err)
	}

	// Capture log output to assert the warning is emitted.
	logBuf := captureLogs(t)

	// Must not panic.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RunMultibyteCapPersist panicked on malformed snapshot: %v", r)
		}
	}()
	stats, err := store.RunMultibyteCapPersist()
	if err != nil {
		t.Errorf("RunMultibyteCapPersist on malformed snapshot returned error %v; expected silent no-op", err)
	}
	if stats.ReadEntries != 0 || stats.UpdatedActive != 0 || stats.UpdatedInactive != 0 {
		t.Errorf("expected zero-valued stats on malformed snapshot, got %+v", stats)
	}
	if !logContains(logBuf, "malformed") && !logContains(logBuf, "invalid") && !logContains(logBuf, "corrupt") {
		t.Errorf("expected log to mention malformed/invalid/corrupt snapshot; got: %s", logBuf.String())
	}
}

// TestRunMultibyteCapPersist_MissingSchemaColumns verifies the persist
// path is a clean no-op on a legacy DB that doesn't yet have the
// multibyte_sup / multibyte_evidence columns. Currently the persist
// would fail at tx.Prepare with a SQL error; the audit requires it
// skip cleanly instead.
//
// We simulate a legacy DB by DROPping the columns post-migration
// (SQLite ≥ 3.35 supports ALTER TABLE DROP COLUMN).
func TestRunMultibyteCapPersist_MissingSchemaColumns(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	// Drop the multibyte columns from both tables to simulate a legacy DB.
	for _, stmt := range []string{
		`ALTER TABLE nodes DROP COLUMN multibyte_sup`,
		`ALTER TABLE nodes DROP COLUMN multibyte_evidence`,
		`ALTER TABLE inactive_nodes DROP COLUMN multibyte_sup`,
		`ALTER TABLE inactive_nodes DROP COLUMN multibyte_evidence`,
	} {
		if _, err := store.db.Exec(stmt); err != nil {
			t.Fatalf("simulate legacy DB (%q): %v", stmt, err)
		}
	}
	// Confirm columns are gone.
	if columnExists(t, store.db, "nodes", "multibyte_sup") {
		t.Fatalf("setup failed: nodes.multibyte_sup still present after DROP")
	}

	snap := mbcapqueue.Snapshot{Entries: []mbcapqueue.Entry{
		{PublicKey: "ff66", Status: "confirmed", Evidence: "advert"},
	}}
	if err := mbcapqueue.WriteSnapshot(dbPath, snap); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}

	logBuf := captureLogs(t)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RunMultibyteCapPersist panicked on legacy DB: %v", r)
		}
	}()
	stats, err := store.RunMultibyteCapPersist()
	if err != nil {
		t.Errorf("RunMultibyteCapPersist on legacy DB returned error %v; expected clean skip", err)
	}
	if stats.UpdatedActive != 0 || stats.UpdatedInactive != 0 {
		t.Errorf("expected zero writes on legacy DB, got %+v", stats)
	}
	// Must explicitly detect + log the skip — otherwise the "clean skip"
	// is silent UPDATE-affected-zero accident, not defensive code.
	if !logContains(logBuf, "legacy") && !logContains(logBuf, "schema") && !logContains(logBuf, "multibyte_sup") {
		t.Errorf("expected explicit log on missing schema columns; got: %s", logBuf.String())
	}
}

// TestRunMultibyteCapPersist_PreservesConfirmedOnUnknown is the
// data-destruction guard the PR claims to enforce: a snapshot Entry
// with status="unknown" must NEVER overwrite an existing "confirmed"
// (or "suspected") DB row. The audit's mutation test: revert the
// `if sup == 0 { continue }` guard in multibyte_persist.go — this
// test must fail.
func TestRunMultibyteCapPersist_PreservesConfirmedOnUnknown(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	// Seed a confirmed active node and a suspected inactive node.
	if _, err := store.db.Exec(`INSERT INTO nodes (public_key, name, role, last_seen, multibyte_sup, multibyte_evidence)
		VALUES ('gg77', 'Golf', 'repeater', '2026-01-01T00:00:00Z', 2, 'advert')`); err != nil {
		t.Fatalf("seed gg77: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO inactive_nodes (public_key, name, role, last_seen, multibyte_sup, multibyte_evidence)
		VALUES ('hh88', 'Hotel', 'companion', '2025-12-01T00:00:00Z', 1, 'path')`); err != nil {
		t.Fatalf("seed hh88: %v", err)
	}

	// Snapshot has only "unknown" entries for both — must skip both.
	snap := mbcapqueue.Snapshot{Entries: []mbcapqueue.Entry{
		{PublicKey: "gg77", Status: "unknown"},
		{PublicKey: "hh88", Status: "unknown"},
	}}
	if err := mbcapqueue.WriteSnapshot(dbPath, snap); err != nil {
		t.Fatalf("WriteSnapshot: %v", err)
	}

	stats, err := store.RunMultibyteCapPersist()
	if err != nil {
		t.Fatalf("RunMultibyteCapPersist: %v", err)
	}
	if stats.Skipped != 2 {
		t.Errorf("Skipped = %d, want 2 (both unknown entries)", stats.Skipped)
	}
	if stats.UpdatedActive != 0 || stats.UpdatedInactive != 0 {
		t.Errorf("expected zero updates, got %+v", stats)
	}

	// Verify the existing values were NOT clobbered.
	var sup int
	var evid string
	if err := store.db.QueryRow(`SELECT multibyte_sup, COALESCE(multibyte_evidence,'') FROM nodes WHERE public_key='gg77'`).Scan(&sup, &evid); err != nil {
		t.Fatalf("read gg77: %v", err)
	}
	if sup != 2 || evid != "advert" {
		t.Errorf("gg77 was clobbered by unknown snapshot: sup=%d evid=%q, want sup=2 evid=advert", sup, evid)
	}
	if err := store.db.QueryRow(`SELECT multibyte_sup, COALESCE(multibyte_evidence,'') FROM inactive_nodes WHERE public_key='hh88'`).Scan(&sup, &evid); err != nil {
		t.Fatalf("read hh88: %v", err)
	}
	if sup != 1 || evid != "path" {
		t.Errorf("hh88 was clobbered by unknown snapshot: sup=%d evid=%q, want sup=1 evid=path", sup, evid)
	}
}
