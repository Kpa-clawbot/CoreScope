package dbschema

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	"github.com/meshcore-analyzer/dbx/dbxtest"
)

const probeFixtureDDL = `CREATE TABLE probe_t (id INTEGER PRIMARY KEY, name TEXT)`

// checkProbes runs both probes inside a transaction, then proves the
// transaction is still usable: on Postgres any failed statement would abort
// it, so a probe that ever errors on the wrong backend fails here.
func checkProbes(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(probeFixtureDDL); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	cases := []struct {
		table, col string
		want       bool
	}{
		{"probe_t", "name", true},
		{"probe_t", "id", true},
		{"probe_t", "missing", false},
		{"no_such_table", "name", false},
	}
	for _, c := range cases {
		got, err := TableHasColumn(tx, c.table, c.col)
		if err != nil || got != c.want {
			t.Fatalf("TableHasColumn(%s,%s) = %v, %v; want %v", c.table, c.col, got, err, c.want)
		}
	}
	for table, want := range map[string]bool{"probe_t": true, "no_such_table": false} {
		got, err := TableExists(tx, table)
		if err != nil || got != want {
			t.Fatalf("TableExists(%s) = %v, %v; want %v", table, got, err, want)
		}
	}
	if _, err := tx.Exec(`INSERT INTO probe_t (id, name) VALUES (1, 'still usable')`); err != nil {
		t.Fatalf("transaction unusable after probes: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit after probes: %v", err)
	}
}

func TestProbesSQLite(t *testing.T) {
	db, err := sql.Open("sqlite3", filepath.Join(t.TempDir(), "probe.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1) // the ingestor's shape: probes must not need a second connection
	checkProbes(t, db)
}

func TestProbesPostgres(t *testing.T) {
	checkProbes(t, dbxtest.OpenPostgres(t))
}

// A view is not a table: AssertReady's mustTable must not be satisfied by one.
func TestTableExistsIgnoresViewsPostgres(t *testing.T) {
	db := dbxtest.OpenPostgres(t)
	if _, err := db.Exec(`CREATE VIEW probe_v AS SELECT 1 AS x`); err != nil {
		t.Fatal(err)
	}
	if got, err := TableExists(db, "probe_v"); err != nil || got {
		t.Fatalf("TableExists(view) = %v, %v; want false", got, err)
	}
}
