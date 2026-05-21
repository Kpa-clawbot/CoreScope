package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/meshcore-analyzer/prunequeue"
)

func TestRunPendingPruneRequests(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	// Seed two nodes; one will be pruned, one will be kept.
	if _, err := store.db.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen)
		VALUES ('aaaa', 'gone', 'companion', 1.0, 1.0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
		       ('bbbb', 'kept', 'companion', 2.0, 2.0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	id := prunequeue.NewID()
	if err := prunequeue.WriteRequest(dbPath, prunequeue.Request{
		ID:          id,
		RequestedAt: time.Now().UTC(),
		Reason:      "geo-prune-test",
		Pubkeys:     []string{"aaaa"},
	}); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}

	store.RunPendingPruneRequests()

	// Request file gone, result file present.
	if exists, _ := prunequeue.RequestExists(dbPath, id); exists {
		t.Error("request file should have been consumed")
	}
	res, err := prunequeue.ReadResult(dbPath, id)
	if err != nil || res == nil {
		t.Fatalf("ReadResult: res=%v err=%v", res, err)
	}
	if res.Deleted != 1 {
		t.Errorf("expected Deleted=1, got %d", res.Deleted)
	}
	if res.Error != "" {
		t.Errorf("unexpected error: %s", res.Error)
	}

	// Verify DB state: aaaa gone, bbbb kept.
	var n int
	store.db.QueryRow("SELECT COUNT(*) FROM nodes WHERE public_key='aaaa'").Scan(&n)
	if n != 0 {
		t.Errorf("expected 'aaaa' deleted, got count=%d", n)
	}
	store.db.QueryRow("SELECT COUNT(*) FROM nodes WHERE public_key='bbbb'").Scan(&n)
	if n != 1 {
		t.Errorf("expected 'bbbb' kept, got count=%d", n)
	}
}

func TestRunPendingPruneRequests_EmptyQueueIsNoop(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	// Must not panic / error on empty queue.
	store.RunPendingPruneRequests()
}
