package main

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestEnsureServerIndexes_CreatesObservationsIndexes guards against
// regression of PR #1187 r3 MUST-FIX 2: legacy server-only DBs that lack
// the ingestor-created observation indexes used to full-scan the
// `SELECT ... FROM observations WHERE timestamp >= ?` subquery added to
// buildTransmissionWhere by 63cc1bc3. ensureServerIndexes must create
// idx_observations_timestamp (and the join companions) so the hot-startup
// chunk loader and RFC3339 since/until path don't full-scan observations.
func TestEnsureServerIndexes_CreatesObservationsIndexes(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(closeRWCache)
	dbPath := filepath.Join(dir, "schema_only.db")

	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// Minimal legacy server-only schema: tables present, no extra indexes.
	stmts := []string{
		`CREATE TABLE transmissions (id INTEGER PRIMARY KEY, raw_hex TEXT, hash TEXT, first_seen TEXT, route_type INTEGER, payload_type INTEGER, payload_version INTEGER, decoded_json TEXT)`,
		// v3 schema (observer_idx) — matches the ingestor-created shape
		// and the path that 63cc1bc3 / hot-startup loadChunk traverse.
		`CREATE TABLE observations (id INTEGER PRIMARY KEY, transmission_id INTEGER, observer_idx INTEGER, direction TEXT, snr REAL, rssi REAL, score INTEGER, path_json TEXT, timestamp TEXT, raw_hex TEXT)`,
		`CREATE TABLE observers (rowid INTEGER PRIMARY KEY, id TEXT, name TEXT)`,
		`CREATE TABLE nodes (pubkey TEXT PRIMARY KEY, name TEXT, role TEXT, lat REAL, lon REAL, last_seen TEXT, first_seen TEXT, frequency REAL)`,
		`CREATE TABLE schema_version (version INTEGER)`,
		`INSERT INTO schema_version (version) VALUES (1)`,
	}
	for _, s := range stmts {
		if _, err := conn.Exec(s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	conn.Close()

	if err := ensureServerIndexes(dbPath); err != nil {
		t.Fatalf("ensureServerIndexes: %v", err)
	}

	// Reopen and query sqlite_master for the indexes we expect.
	conn2, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer conn2.Close()

	required := []string{
		"idx_transmissions_first_seen",
		"idx_transmissions_hash",
		"idx_transmissions_payload_type",
		"idx_observations_timestamp",
		"idx_observations_transmission_id",
		"idx_observations_observer_idx",
	}
	for _, name := range required {
		var found string
		err := conn2.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, name).Scan(&found)
		if err != nil {
			t.Errorf("index %s not created (err=%v) — ensureServerIndexes must create it to avoid full scans on the SQL fallback path", name, err)
			continue
		}
		if found != name {
			t.Errorf("index lookup mismatch: want %s got %s", name, found)
		}
	}
}
