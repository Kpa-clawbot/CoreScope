// Package dbxtest is the shared PostgreSQL test helper: each test gets its own
// throwaway schema in the database named by $CORESCOPE_TEST_PG_DSN, so suites
// can run in parallel against one server without seeing each other's tables.
package dbxtest

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"testing"

	"github.com/meshcore-analyzer/dbx"
)

// dsnEnv names the environment variable holding the test Postgres DSN.
const dsnEnv = "CORESCOPE_TEST_PG_DSN"

// OpenPostgres returns a *sql.DB (driver PostgresDriverName) connected to the
// database in $CORESCOPE_TEST_PG_DSN, inside a fresh schema created for this
// test and dropped (CASCADE) in t.Cleanup. Every pooled connection must use
// that schema as its search_path. Skips the test (t.Skip) when the env var is
// unset, so suites stay green without Postgres.
func OpenPostgres(t testing.TB) *sql.DB {
	t.Helper()
	dsn := os.Getenv(dsnEnv)
	if dsn == "" {
		t.Skipf("%s not set; skipping Postgres test", dsnEnv)
	}

	schema := newSchemaName(t)

	admin, err := sql.Open(dbx.PostgresDriverName, dsn)
	if err != nil {
		t.Fatalf("dbxtest: open admin connection: %v", err)
	}
	// The name is generated from [a-z0-9_] only, so quoting is not needed for
	// safety; it is quoted anyway so the DDL stays correct if that changes.
	if _, err := admin.Exec(`CREATE SCHEMA "` + schema + `"`); err != nil {
		admin.Close()
		t.Fatalf("dbxtest: create schema: %v", err)
	}

	// search_path is sent as a startup parameter, so it applies to every
	// connection the pool ever opens, not just the first one.
	scoped, err := withSearchPath(dsn, schema)
	if err != nil {
		dropSchema(t, admin, schema)
		admin.Close()
		t.Fatalf("dbxtest: %v", err)
	}
	db, err := sql.Open(dbx.PostgresDriverName, scoped)
	if err != nil {
		dropSchema(t, admin, schema)
		admin.Close()
		t.Fatalf("dbxtest: open test connection: %v", err)
	}

	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("dbxtest: close test db: %v", err)
		}
		dropSchema(t, admin, schema)
		if err := admin.Close(); err != nil {
			t.Errorf("dbxtest: close admin db: %v", err)
		}
	})

	var got string
	if err := db.QueryRow(`SELECT current_schema()`).Scan(&got); err != nil {
		t.Fatalf("dbxtest: connect: %v", err)
	}
	if got != schema {
		t.Fatalf("dbxtest: current_schema() = %q, want %q", got, schema)
	}
	return db
}

func newSchemaName(t testing.TB) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("dbxtest: random schema name: %v", err)
	}
	return "dbxtest_" + hex.EncodeToString(b[:])
}

func dropSchema(t testing.TB, admin *sql.DB, schema string) {
	if _, err := admin.Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`); err != nil {
		t.Errorf("dbxtest: drop schema %s: %v", schema, err)
	}
}

// withSearchPath sets search_path=<schema> in a URL or keyword/value DSN.
func withSearchPath(dsn, schema string) (string, error) {
	out, err := dbx.WithSearchPath(dsn, schema)
	if err != nil {
		// Its own words: dbx's error is phrased for db.dsn.
		return "", errors.New("unparseable $" + dsnEnv + " URL")
	}
	return out, nil
}
