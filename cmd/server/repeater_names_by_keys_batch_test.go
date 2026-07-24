package main

import (
	"fmt"
	"testing"
)

// TestGetRepeaterNamesByKeys_Basic covers the role filter and the
// "unnamed repeater falls back to its own key" behavior.
func TestGetRepeaterNamesByKeys_Basic(t *testing.T) {
	db := setupTestDB(t)
	insertTestNode(t, db, "repeaterkey1", "Repeater One", "repeater")
	insertTestNode(t, db, "roomkey1", "", "room") // unnamed — should fall back to its own key
	insertTestNode(t, db, "clientkey1", "Some Client", "client")

	result := db.GetRepeaterNamesByKeys([]string{"repeaterkey1", "roomkey1", "clientkey1", "nonexistent"})

	if got := result["repeaterkey1"]; got != "Repeater One" {
		t.Errorf("repeaterkey1 = %q, want %q", got, "Repeater One")
	}
	if got := result["roomkey1"]; got != "roomkey1" {
		t.Errorf("unnamed room should fall back to its own key, got %q", got)
	}
	if _, ok := result["clientkey1"]; ok {
		t.Error("client role should be excluded — only repeater/room are relays")
	}
	if _, ok := result["nonexistent"]; ok {
		t.Error("a key with no matching node row should not appear in the result")
	}
}

// TestGetRepeaterNamesByKeys_BatchesAcrossChunkBoundary is a regression test
// for the SQL IN (...) clause batching (bot review on PR #1852: an
// unbounded IN clause risks hitting SQLite's SQLITE_MAX_VARIABLE_NUMBER on
// large deployments). Inserts more repeaters than
// repeaterNamesByKeysBatchSize and asserts every single one still resolves
// — proving the chunking loop doesn't drop or duplicate results at the
// batch boundary.
func TestGetRepeaterNamesByKeys_BatchesAcrossChunkBoundary(t *testing.T) {
	db := setupTestDB(t)
	n := repeaterNamesByKeysBatchSize + 50 // spans two chunks
	keys := make([]string, 0, n)
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("repkey%04d", i)
		insertTestNode(t, db, key, fmt.Sprintf("Repeater %d", i), "repeater")
		keys = append(keys, key)
	}

	result := db.GetRepeaterNamesByKeys(keys)

	if len(result) != n {
		t.Fatalf("resolved %d of %d repeaters across the chunk boundary, want all %d", len(result), n, n)
	}
	for i, key := range keys {
		want := fmt.Sprintf("Repeater %d", i)
		if got := result[key]; got != want {
			t.Errorf("key %s = %q, want %q", key, got, want)
		}
	}
}

// insertTestNode inserts a minimal nodes row for GetRepeaterNamesByKeys tests.
func insertTestNode(t *testing.T, db *DB, pubkey, name, role string) {
	t.Helper()
	_, err := db.conn.Exec(
		`INSERT INTO nodes (public_key, name, role) VALUES (?, ?, ?)`,
		pubkey, name, role,
	)
	if err != nil {
		t.Fatalf("insert test node %s: %v", pubkey, err)
	}
}
