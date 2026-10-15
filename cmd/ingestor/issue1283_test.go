package main

import (
	"path/filepath"
	"testing"
	"time"
)

// TestIngestorPruneOldPackets enforces #1283: the writer for
// transmissions retention lives on the ingestor's *Store. Before the fix,
// this lived on cmd/server/*DB and raced with ingestor INSERTs. After
// the fix, ingestor owns it and runs it on its own write-locked handle.
func TestIngestorPruneOldPackets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "prune.db")
	store, err := openPostgresTestStore(t, path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	old := time.Now().UTC().AddDate(0, 0, -10).Format(time.RFC3339)
	new := time.Now().UTC().Format(time.RFC3339)
	for i, ts := range []string{old, old, new} {
		_, err := store.db.Exec(
			`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json)
			 VALUES ($1, $2, $3, 0, 1, 1, '{}')`,
			"AA", "h"+string(rune('a'+i)), ts,
		)
		if err != nil {
			t.Fatalf("seed tx: %v", err)
		}
	}

	n, err := store.PruneOldPackets(5)
	if err != nil {
		t.Fatalf("PruneOldPackets: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 pruned, got %d", n)
	}

	var remaining int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM transmissions`).Scan(&remaining); err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("expected 1 transmission remaining, got %d", remaining)
	}
}

func TestPostgresRuntimeCannotChangeTableVacuumPolicy(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`ALTER TABLE transmissions SET (autovacuum_enabled=false)`); err == nil {
		t.Fatal("runtime writer changed owner-only vacuum policy")
	}
}
