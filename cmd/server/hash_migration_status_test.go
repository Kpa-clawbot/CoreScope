package main

import "testing"

// TestHashMigrationCompleteReadsIngestorStatus pins /api/stats
// hashMigrationComplete to the ingestor's _async_migrations row (#1856):
// false until the row says done, then latched true.
func TestHashMigrationCompleteReadsIngestorStatus(t *testing.T) {
	db := setupTestDBv2(t)

	if db.hashMigrationComplete() {
		t.Fatal("missing _async_migrations table must read as not complete")
	}

	// The ingestor's bookkeeping table (cmd/ingestor/async_migration.go).
	if _, err := db.conn.Exec(`CREATE TABLE _async_migrations (
		name TEXT PRIMARY KEY, status TEXT NOT NULL, started_at TEXT, ended_at TEXT, error TEXT)`); err != nil {
		t.Fatal(err)
	}
	if db.hashMigrationComplete() {
		t.Fatal("missing row must read as not complete")
	}
	for _, status := range []string{"pending_async", "failed", "cancelled"} {
		if _, err := db.conn.Exec(`INSERT OR REPLACE INTO _async_migrations (name, status) VALUES (?, ?)`,
			contentHashMigrationName, status); err != nil {
			t.Fatal(err)
		}
		if db.hashMigrationComplete() {
			t.Fatalf("status %q must read as not complete", status)
		}
	}

	if _, err := db.conn.Exec(`UPDATE _async_migrations SET status = 'done' WHERE name = ?`, contentHashMigrationName); err != nil {
		t.Fatal(err)
	}
	if !db.hashMigrationComplete() {
		t.Fatal("done must read as complete")
	}

	// done is final, so the answer is latched and the table is not read again.
	if _, err := db.conn.Exec(`DROP TABLE _async_migrations`); err != nil {
		t.Fatal(err)
	}
	if !db.hashMigrationComplete() {
		t.Fatal("complete must stay latched once seen")
	}
}
