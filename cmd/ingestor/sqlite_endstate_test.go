package main

import (
	"database/sql"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/dbschema"
)

// TestSQLiteEndStateMatchesIngestor pins dbschema.SQLiteEndState (the schema
// the copy tool and the server's tests create without running the ingestor's
// migration history) to what a fresh OpenStore actually builds: the same
// tables, indexes, views and triggers, each with the same SQL up to
// whitespace. A schema change in the ingestor that is not mirrored in
// internal/dbschema/sqlite_endstate.sql fails here, naming the object.
func TestSQLiteEndStateMatchesIngestor(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "ingestor.db"))
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()
	store.WaitForAsyncMigrations() // obs_observer_ts_idx_v1 builds its index async

	fixture, err := sql.Open("sqlite3", filepath.Join(dir, "endstate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	if _, err := fixture.Exec(dbschema.SQLiteEndState); err != nil {
		t.Fatalf("executing dbschema.SQLiteEndState: %v", err)
	}

	got := sqliteObjects(t, store.db)
	want := sqliteObjects(t, fixture)
	names := map[string]bool{}
	for k := range got {
		names[k] = true
	}
	for k := range want {
		names[k] = true
	}
	sorted := make([]string, 0, len(names))
	for k := range names {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		g, inStore := got[k]
		w, inFixture := want[k]
		switch {
		case !inFixture:
			t.Errorf("%s exists in a fresh OpenStore but not in sqlite_endstate.sql:\n  %s", k, g)
		case !inStore:
			t.Errorf("%s is in sqlite_endstate.sql but a fresh OpenStore does not create it:\n  %s", k, w)
		case g != w:
			t.Errorf("%s differs:\n  OpenStore:          %s\n  sqlite_endstate.sql: %s", k, g, w)
		}
	}
	if len(got) == 0 {
		t.Fatal("no schema objects read from OpenStore")
	}
}

// sqliteObjects maps "type name" to the object's whitespace-collapsed SQL for
// every user table, index, view and trigger. SQLite's own objects (sqlite_*,
// including the implicit autoindexes, whose SQL is NULL) are left out: they
// follow from the CREATE statements compared here.
func sqliteObjects(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT type, name, sql FROM sqlite_master
		WHERE type IN ('table', 'index', 'view', 'trigger')
		  AND name NOT LIKE 'sqlite\_%' ESCAPE '\' AND sql IS NOT NULL`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var typ, name, text string
		if err := rows.Scan(&typ, &name, &text); err != nil {
			t.Fatal(err)
		}
		out[typ+" "+name] = strings.Join(strings.Fields(text), " ")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
