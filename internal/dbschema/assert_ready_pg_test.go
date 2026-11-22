package dbschema_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/dbschema/pgschema"
	"github.com/meshcore-analyzer/dbx"
	"github.com/meshcore-analyzer/dbx/dbxtest"
)

// selectOnlyConn opens one connection that runs as a new role holding only
// USAGE on the test schema and SELECT on its tables: what the server's
// read-only role can see of the catalogues.
func selectOnlyConn(t *testing.T, owner *sql.DB) *sql.DB {
	t.Helper()
	var schema string
	if err := owner.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	role := `"dbschema_ro_` + hex.EncodeToString(b[:]) + `"`
	qs := `"` + schema + `"`
	for _, stmt := range []string{
		`CREATE ROLE ` + role + ` NOLOGIN`,
		`GRANT USAGE ON SCHEMA ` + qs + ` TO ` + role,
		`GRANT SELECT ON ALL TABLES IN SCHEMA ` + qs + ` TO ` + role,
	} {
		if _, err := owner.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	t.Cleanup(func() {
		// Runs before dbxtest drops the schema (cleanups are LIFO).
		_, _ = owner.Exec(`DROP OWNED BY ` + role)
		_, _ = owner.Exec(`DROP ROLE ` + role)
	})
	ro, err := sql.Open(dbx.PostgresDriverName, os.Getenv("CORESCOPE_TEST_PG_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ro.Close() })
	// One long-lived connection, so SET ROLE and search_path govern every query.
	ro.SetMaxOpenConns(1)
	ro.SetMaxIdleConns(1)
	ro.SetConnMaxLifetime(0)
	ro.SetConnMaxIdleTime(0)
	for _, stmt := range []string{`SET ROLE ` + role, `SET search_path TO ` + qs} {
		if _, err := ro.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	return ro
}

// The server runs dbschema.AssertReady on Postgres too (cmd/server main.go),
// with a SELECT-only role, so it must pass there on a schema the ingestor
// created and must not use SQLite-only probes such as PRAGMA.
func TestAssertReadyPostgres(t *testing.T) {
	owner := dbxtest.OpenPostgres(t)
	if err := pgschema.Apply(context.Background(), owner, t.Logf); err != nil {
		t.Fatalf("pgschema.Apply: %v", err)
	}
	ro := selectOnlyConn(t, owner)
	for name, db := range map[string]*sql.DB{"owner": owner, "select-only role": ro} {
		if err := dbschema.AssertReady(db); err != nil {
			t.Fatalf("AssertReady as %s on a migrated Postgres schema: %v", name, err)
		}
	}
	// A primary key that is not idx is refused, with the SQLite wording.
	for _, stmt := range []string{
		`ALTER TABLE observers DROP CONSTRAINT observers_pkey CASCADE`,
		`ALTER TABLE observers ADD PRIMARY KEY (id)`,
	} {
		if _, err := owner.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	for name, db := range map[string]*sql.DB{"owner": owner, "select-only role": ro} {
		err := dbschema.AssertReady(db)
		if err == nil || !strings.Contains(err.Error(), "observers.idx (not the primary key)") {
			t.Fatalf("AssertReady as %s with observers keyed on id = %v, want observers.idx (not the primary key)", name, err)
		}
	}
}
