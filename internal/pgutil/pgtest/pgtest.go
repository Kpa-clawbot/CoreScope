// Package pgtest provisions disposable PostgreSQL test namespaces. Tests fail
// rather than silently skipping when CORESCOPE_TEST_POSTGRES_URL is missing.
package pgtest

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/pgutil"
)

func name() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "corescope_test_" + hex.EncodeToString(b[:])
}

func admin(t testing.TB) (*sql.DB, *url.URL) {
	t.Helper()
	dsn := os.Getenv("CORESCOPE_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Fatal("set CORESCOPE_TEST_POSTGRES_URL to a disposable PostgreSQL 18 administrator URL")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("invalid CORESCOPE_TEST_POSTGRES_URL")
	}
	db, err := pgutil.Open(dsn, false)
	if err != nil {
		t.Fatal(err)
	}
	return db, u
}

// NewSchema returns a URL selecting a fresh empty schema. No fixture tables
// are created. Register pool cleanup after this call so pools close first.
func NewSchema(t testing.TB) string {
	t.Helper()
	db, u := admin(t)
	schema := name()
	if _, err := db.Exec(`CREATE SCHEMA ` + schema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer db.Close()
		if _, err := db.Exec(`DROP SCHEMA ` + schema + ` CASCADE`); err != nil {
			t.Errorf("drop test schema: %v", err)
		}
	})
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return u.String()
}

// NewDatabase returns a fresh empty logical database for isolation and restore
// tests. Only this randomly named database is removed by cleanup.
func NewDatabase(t testing.TB) string {
	t.Helper()
	db, u := admin(t)
	database := name()
	if _, err := db.Exec(`CREATE DATABASE ` + database + ` TEMPLATE template0`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer db.Close()
		if _, err := db.Exec(`DROP DATABASE ` + database + ` WITH (FORCE)`); err != nil {
			t.Errorf("drop test database: %v", err)
		}
	})
	u.Path = "/" + database
	q := u.Query()
	q.Del("search_path")
	u.RawQuery = q.Encode()
	return u.String()
}

// ReadOnly provisions a reader for the selected schema and future tables.
// It grants neither schema creation nor any write privilege.
func ReadOnly(t testing.TB, writerDSN string) string {
	return role(t, writerDSN, false)
}

// Writer provisions DML and sequence privileges, without schema ownership or DDL.
// A store can revoke writes on its readiness metadata after provisioning.
func Writer(t testing.TB, ownerDSN string) string {
	return role(t, ownerDSN, true)
}

func role(t testing.TB, writerDSN string, write bool) string {
	t.Helper()
	db, err := pgutil.Open(writerDSN, false)
	if err != nil {
		t.Fatal(err)
	}
	role, password := name(), name()
	var schema string
	if err := db.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
		db.Close()
		t.Fatal(err)
	}
	quotedSchema := `"` + schema + `"`
	privileges := "SELECT"
	if write {
		privileges = "SELECT,INSERT,UPDATE,DELETE"
	}
	statements := []string{`CREATE ROLE ` + role + ` LOGIN PASSWORD '` + password + `'`,
		`GRANT USAGE ON SCHEMA ` + quotedSchema + ` TO ` + role,
		`GRANT ` + privileges + ` ON ALL TABLES IN SCHEMA ` + quotedSchema + ` TO ` + role,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA ` + quotedSchema + ` GRANT ` + privileges + ` ON TABLES TO ` + role}
	sequencePrivileges := "SELECT"
	if write {
		sequencePrivileges = "USAGE,SELECT"
	}
	statements = append(statements, `GRANT `+sequencePrivileges+` ON ALL SEQUENCES IN SCHEMA `+quotedSchema+` TO `+role,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA `+quotedSchema+` GRANT `+sequencePrivileges+` ON SEQUENCES TO `+role)
	// One implicit transaction preserves atomicity and avoids a durable commit
	// per grant in suites with hundreds of isolated roles.
	if _, err := db.Exec(strings.Join(statements, ";")); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		defer db.Close()
		if _, err := db.Exec(`DROP OWNED BY ` + role + `;DROP ROLE ` + role); err != nil {
			t.Errorf("drop test role: %v", err)
		}
	})
	u, _ := url.Parse(writerDSN)
	u.User = url.UserPassword(role, password)
	return u.String()
}
