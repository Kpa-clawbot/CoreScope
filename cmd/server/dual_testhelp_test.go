package main

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/meshcore-analyzer/dbschema"
)

// The dual-backend harness: the same end-state schema on SQLite and on
// Postgres, the same seeded rows in both, and an assertion that a server
// method returns identical results from each. It is how the server's SQL is
// proven equivalent on both backends rather than merely runnable on Postgres.

// sqliteEndStateSchema is the SQLite end-state schema (kept as a name for the
// tests that already use it; the source of truth is dbschema.SQLiteEndState).
var sqliteEndStateSchema = dbschema.SQLiteEndState

type dualDB struct {
	sqlite, pg     *DB     // server handles: read-only, exactly as production opens them
	sqliteRW, pgRW *sql.DB // writable handles on the same databases, for seeding only
}

// newDualTestDB builds both backends. The SQLite side is opened through the
// real OpenDB (mode=ro); the Postgres side through newPostgresTestDB. Skips
// when $CORESCOPE_TEST_PG_DSN is unset.
func newDualTestDB(t *testing.T) *dualDB {
	t.Helper()
	pgDB, pgRW := newPostgresTestDB(t)

	path := filepath.Join(t.TempDir(), "dual.db")
	rw, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rw.Close() })
	rw.SetMaxOpenConns(1)
	if _, err := rw.Exec(dbschema.SQLiteEndState); err != nil {
		t.Fatalf("apply SQLite end-state schema: %v", err)
	}
	sq, err := OpenDB(path)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { sq.conn.Close() })
	return &dualDB{sqlite: sq, pg: pgDB, sqliteRW: rw, pgRW: pgRW}
}

// exec runs one seeding statement on both backends.
func (d *dualDB) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := d.sqliteRW.Exec(query, args...); err != nil {
		t.Fatalf("seed sqlite: %v\n%s", err, query)
	}
	if _, err := d.pgRW.Exec(query, args...); err != nil {
		t.Fatalf("seed postgres: %v\n%s", err, query)
	}
}

// requireSame calls f on both backends and fails unless both succeed with
// deeply equal results. It returns the (shared) result for further checks —
// callers should still assert it is non-trivial, since two empty results are
// trivially equal.
func requireSame[T any](t *testing.T, d *dualDB, name string, f func(*DB) (T, error)) T {
	t.Helper()
	sv, serr := f(d.sqlite)
	pv, perr := f(d.pg)
	if serr != nil || perr != nil {
		t.Fatalf("%s: sqlite err=%v, postgres err=%v", name, serr, perr)
	}
	if !reflect.DeepEqual(sv, pv) {
		t.Fatalf("%s differs between backends:\n sqlite:   %#v\n postgres: %#v", name, sv, pv)
	}
	return sv
}

func TestDualHarnessSmoke(t *testing.T) {
	d := newDualTestDB(t)
	d.exec(t, `INSERT INTO nodes (public_key, name, role, last_seen, first_seen, advert_count)
		VALUES (?, ?, ?, ?, ?, ?)`, "aabbccdd", "Zed", "repeater", "2026-11-01T00:00:00Z", "2026-10-01T00:00:00Z", 3)
	d.exec(t, `INSERT INTO nodes (public_key, name, role, last_seen, first_seen, advert_count)
		VALUES (?, ?, ?, ?, ?, ?)`, "00112233", "alpha", "companion", "2026-11-02T00:00:00Z", "2026-10-02T00:00:00Z", 1)
	got := requireSame(t, d, "names ordered", func(db *DB) ([]string, error) {
		rows, err := db.conn.Query(`SELECT name FROM nodes ORDER BY name`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		return out, rows.Err()
	})
	// Byte order ("Zed" < "alpha"), proving COLLATE "C" matches SQLite.
	if want := []string{"Zed", "alpha"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
