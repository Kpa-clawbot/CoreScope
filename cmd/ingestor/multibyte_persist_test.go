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
