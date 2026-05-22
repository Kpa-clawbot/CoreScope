package dbschema

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// minimalDB bootstraps a SQLite DB with just enough tables for the
// ensure_* helpers to run against, but WITHOUT any of the optional
// columns that dbschema.Apply is responsible for ensuring.
func minimalDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "schema.db")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_journal_mode=WAL")
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{
		// Bare-bones tables, mirroring the legacy/empty fixture shape
		// pre-migration. Intentionally omit columns we expect Apply to add.
		`CREATE TABLE transmissions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			raw_hex TEXT NOT NULL,
			hash TEXT NOT NULL UNIQUE,
			first_seen TEXT NOT NULL,
			route_type INTEGER,
			payload_type INTEGER,
			payload_version INTEGER,
			decoded_json TEXT
		)`,
		`CREATE TABLE observations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			transmission_id INTEGER NOT NULL,
			observer_idx INTEGER,
			direction TEXT,
			snr REAL,
			rssi REAL,
			score INTEGER,
			path_json TEXT,
			timestamp INTEGER NOT NULL
		)`,
		`CREATE TABLE observers (
			id TEXT PRIMARY KEY,
			name TEXT
		)`,
		`CREATE TABLE nodes (
			public_key TEXT PRIMARY KEY,
			name TEXT
		)`,
		`CREATE TABLE inactive_nodes (
			public_key TEXT PRIMARY KEY,
			name TEXT,
			last_seen TEXT
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("bootstrap: %v", err)
		}
	}
	return db
}

// TestApplyAddsOptionalColumns_CanonicalSource is the regression gate
// for issue #1321: dbschema.Apply must be the single source of truth
// for ALL optional columns the server PRAGMA-detects. Previously
// scope_name/default_scope/observations.raw_hex lived ONLY in
// cmd/ingestor/db.go applySchema, so the server (which runs
// detectSchema AFTER dbschema.AssertReady) could race the writer and
// cache stale false values when ingestor hadn't yet finished its
// applySchema migrations.
func TestApplyAddsOptionalColumns_CanonicalSource(t *testing.T) {
	db := minimalDB(t)
	defer db.Close()

	if err := Apply(db, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	cases := []struct {
		table, col string
	}{
		{"transmissions", "scope_name"},
		{"nodes", "default_scope"},
		{"inactive_nodes", "default_scope"},
		{"observations", "raw_hex"},
	}
	for _, c := range cases {
		has, err := TableHasColumn(db, c.table, c.col)
		if err != nil {
			t.Fatalf("probe %s.%s: %v", c.table, c.col, err)
		}
		if !has {
			t.Errorf("after Apply: %s.%s missing — dbschema must be the source of truth for this optional column (#1321)", c.table, c.col)
		}
	}
}

// TestAssertReady_RequiresOptionalColumns enforces that AssertReady
// REFUSES a DB missing the optional columns the server depends on —
// proving dbschema.AssertReady (not server-side PRAGMA detection) is
// the gate.
func TestAssertReady_RequiresOptionalColumns(t *testing.T) {
	db := minimalDB(t)
	defer db.Close()
	// Run pre-existing ensures so we only fail on the new ones.
	noop := Logger(func(string, ...interface{}) {})
	if err := ensureNeighborEdgesTable(db); err != nil {
		t.Fatal(err)
	}
	if err := ensureResolvedPathColumn(db, noop); err != nil {
		t.Fatal(err)
	}
	if err := ensureObserverInactiveColumn(db, noop); err != nil {
		t.Fatal(err)
	}
	if err := ensureLastPacketAtColumn(db, noop); err != nil {
		t.Fatal(err)
	}
	if err := ensureObserverIATAColumn(db, noop); err != nil {
		t.Fatal(err)
	}
	if err := ensureForeignAdvertColumn(db, noop); err != nil {
		t.Fatal(err)
	}
	if err := ensureFromPubkeyColumn(db, noop); err != nil {
		t.Fatal(err)
	}

	// At this point the OLD AssertReady set is satisfied but the NEW
	// columns are NOT — AssertReady must still fail.
	err := AssertReady(db)
	if err == nil {
		t.Fatal("AssertReady should fail when scope_name/default_scope/observations.raw_hex are missing (#1321)")
	}
	for _, must := range []string{"scope_name", "default_scope", "raw_hex"} {
		if !contains(err.Error(), must) {
			t.Errorf("AssertReady error should mention missing %q; got: %v", must, err)
		}
	}

	// After full Apply, AssertReady passes.
	if err := Apply(db, nil); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if err := AssertReady(db); err != nil {
		t.Fatalf("AssertReady after full Apply: %v", err)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
