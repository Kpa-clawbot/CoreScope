package main

import (
	"database/sql"
	"strings"
	"testing"
)

// #1483: server's GetNodeLocationsByKeys lookup relies on stored
// public_key being lowercase (LOWER(public_key) was dropped for perf).
// The ingestor must normalize any legacy uppercase rows on boot so
// the lookup remains correct.
func TestPublicKeyLowercaseNormalizationMigration(t *testing.T) {
	dbPath := tempDBPath(t)
	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("first OpenStore: %v", err)
	}
	// Seed an uppercase row directly, bypassing UpsertNode's lowercase.
	if _, err := s.db.Exec(
		`INSERT INTO nodes (public_key, name, role, last_seen, first_seen)
		 VALUES ('AABBCCDDEEFF11223344', 'mixed-case-node', 'companion', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed uppercase row: %v", err)
	}
	// Sanity: verify the uppercase row is there pre-normalization.
	var pk string
	if err := s.db.QueryRow(`SELECT public_key FROM nodes WHERE public_key = 'AABBCCDDEEFF11223344'`).Scan(&pk); err != nil {
		t.Fatalf("pre-check select: %v", err)
	}
	if pk != "AABBCCDDEEFF11223344" {
		t.Fatalf("pre-check: expected uppercase, got %s", pk)
	}
	s.Close()

	// Reopen — the boot-time migration should normalize the row.
	s2, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	// The uppercase row should be gone.
	var still int
	if err := s2.db.QueryRow(`SELECT COUNT(*) FROM nodes WHERE public_key = 'AABBCCDDEEFF11223344'`).Scan(&still); err != nil {
		t.Fatalf("post-check uppercase count: %v", err)
	}
	if still != 0 {
		t.Fatalf("expected 0 uppercase rows after migration, got %d", still)
	}
	// The lowercase form should match.
	var lower string
	err = s2.db.QueryRow(`SELECT public_key FROM nodes WHERE public_key = 'aabbccddeeff11223344'`).Scan(&lower)
	if err == sql.ErrNoRows {
		t.Fatalf("expected lowercase row to exist after migration")
	}
	if err != nil {
		t.Fatalf("post-check lowercase select: %v", err)
	}
	if lower != strings.ToLower("AABBCCDDEEFF11223344") {
		t.Fatalf("got %s, want lowercase form", lower)
	}
}
