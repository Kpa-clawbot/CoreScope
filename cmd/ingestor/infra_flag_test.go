package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/meshcore-analyzer/infraqueue"
)

func TestSetInfrastructureFlag_NodesTable(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	if _, err := store.db.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen)
		VALUES ('aaaa', 'tower', 'repeater', 1.0, 1.0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	matched, err := store.SetInfrastructureFlag("aaaa", true)
	if err != nil {
		t.Fatalf("SetInfrastructureFlag: %v", err)
	}
	if !matched {
		t.Fatal("expected matched=true")
	}

	var val int
	if err := store.db.QueryRow("SELECT infrastructure FROM nodes WHERE public_key='aaaa'").Scan(&val); err != nil {
		t.Fatalf("query: %v", err)
	}
	if val != 1 {
		t.Errorf("expected infrastructure=1, got %d", val)
	}

	// Toggle back off.
	matched, err = store.SetInfrastructureFlag("aaaa", false)
	if err != nil || !matched {
		t.Fatalf("toggle off: matched=%v err=%v", matched, err)
	}
	store.db.QueryRow("SELECT infrastructure FROM nodes WHERE public_key='aaaa'").Scan(&val)
	if val != 0 {
		t.Errorf("expected infrastructure=0, got %d", val)
	}
}

func TestSetInfrastructureFlag_InactiveNodesTable(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	// Only present in inactive_nodes (retention moved it there).
	if _, err := store.db.Exec(`INSERT INTO inactive_nodes (public_key, name, role, lat, lon, last_seen, first_seen)
		VALUES ('bbbb', 'stale-tower', 'repeater', 2.0, 2.0, '2025-01-01T00:00:00Z', '2025-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	matched, err := store.SetInfrastructureFlag("bbbb", true)
	if err != nil {
		t.Fatalf("SetInfrastructureFlag: %v", err)
	}
	if !matched {
		t.Fatal("expected matched=true for inactive_nodes row")
	}

	var val int
	store.db.QueryRow("SELECT infrastructure FROM inactive_nodes WHERE public_key='bbbb'").Scan(&val)
	if val != 1 {
		t.Errorf("expected infrastructure=1, got %d", val)
	}
}

func TestSetInfrastructureFlag_NoMatch(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	matched, err := store.SetInfrastructureFlag("nonexistent", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if matched {
		t.Error("expected matched=false for a pubkey with no row anywhere")
	}
}

func TestRunPendingInfraRequests(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	if _, err := store.db.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen)
		VALUES ('cccc', 'tower', 'repeater', 1.0, 1.0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	id := infraqueue.NewID()
	if err := infraqueue.WriteRequest(dbPath, infraqueue.Request{
		ID:             id,
		RequestedAt:    time.Now().UTC(),
		PublicKey:      "cccc",
		Infrastructure: true,
		ActorUsername:  "alice",
	}); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}

	store.RunPendingInfraRequests()

	if exists, _ := infraqueue.RequestExists(dbPath, id); exists {
		t.Error("request file should have been consumed")
	}
	res, err := infraqueue.ReadResult(dbPath, id)
	if err != nil || res == nil {
		t.Fatalf("ReadResult: res=%v err=%v", res, err)
	}
	if !res.Applied {
		t.Errorf("expected Applied=true, got false (error=%q)", res.Error)
	}
	if res.Error != "" {
		t.Errorf("unexpected error: %s", res.Error)
	}

	var val int
	store.db.QueryRow("SELECT infrastructure FROM nodes WHERE public_key='cccc'").Scan(&val)
	if val != 1 {
		t.Errorf("expected infrastructure=1 in DB, got %d", val)
	}
}

func TestRunPendingInfraRequests_NoMatchWritesError(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	id := infraqueue.NewID()
	if err := infraqueue.WriteRequest(dbPath, infraqueue.Request{
		ID:             id,
		RequestedAt:    time.Now().UTC(),
		PublicKey:      "nonexistent",
		Infrastructure: true,
	}); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}

	store.RunPendingInfraRequests()

	res, err := infraqueue.ReadResult(dbPath, id)
	if err != nil || res == nil {
		t.Fatalf("ReadResult: res=%v err=%v", res, err)
	}
	if res.Applied {
		t.Error("expected Applied=false")
	}
	if res.Error == "" {
		t.Error("expected a non-empty Error explaining the no-match")
	}
}

func TestRunPendingInfraRequests_EmptyQueueIsNoop(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	// Must not panic / error on empty queue.
	store.RunPendingInfraRequests()
}
