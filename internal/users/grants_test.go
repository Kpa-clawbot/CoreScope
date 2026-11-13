package users

import (
	"context"
	"testing"

	"github.com/meshcore-analyzer/dbx/dbxtest"
)

// pgReaderTestLock serialises, across test processes, the tests that create
// or drop the cluster-wide UsersReaderRole.
const pgReaderTestLock = 0x43535552 // "CSUR"

func TestPostgresGrantsReaderRole(t *testing.T) {
	dsn := pgTestDSN(t)
	admin := dbxtest.OpenPostgres(t)
	ctx := context.Background()
	lock, err := admin.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock.ExecContext(ctx, `SELECT pg_advisory_lock(?)`, pgReaderTestLock); err != nil {
		lock.Close()
		t.Fatalf("advisory lock: %v", err)
	}
	var existed bool
	if err := admin.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = ?)`, UsersReaderRole).Scan(&existed); err != nil {
		t.Fatal(err)
	}
	// Registered before the stores, so it runs after they close.
	t.Cleanup(func() {
		if !existed {
			for _, q := range []string{`DROP OWNED BY ` + UsersReaderRole, `DROP ROLE ` + UsersReaderRole} {
				if _, err := admin.Exec(q); err != nil {
					t.Errorf("%s: %v", q, err)
				}
			}
		}
		_, _ = lock.ExecContext(ctx, `SELECT pg_advisory_unlock(?)`, pgReaderTestLock)
		lock.Close()
	})

	priv := func(st *Store, q string, args ...any) bool {
		t.Helper()
		var ok bool
		if err := st.db.QueryRow(q, args...).Scan(&ok); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return ok
	}
	check := func(st *Store, schema string, want bool) {
		t.Helper()
		if got := priv(st, `SELECT has_schema_privilege(?, ?, 'USAGE')`, UsersReaderRole, schema); got != want {
			t.Errorf("USAGE on schema = %v, want %v", got, want)
		}
		for _, table := range []string{"proposals", "companion_links"} {
			if got := priv(st, `SELECT has_table_privilege(?, ?, 'SELECT')`, UsersReaderRole, schema+"."+table); got != want {
				t.Errorf("SELECT on %s = %v, want %v", table, got, want)
			}
		}
		// Nothing else: accounts, sessions and secrets stay private.
		for _, table := range []string{"users", "sessions", "mqtt_logins", "rate_limits"} {
			if priv(st, `SELECT has_table_privilege(?, ?, 'SELECT')`, UsersReaderRole, schema+"."+table) {
				t.Errorf("SELECT on %s granted", table)
			}
		}
		if priv(st, `SELECT has_table_privilege(?, ?, 'INSERT,UPDATE,DELETE')`, UsersReaderRole, schema+".proposals") {
			t.Error("write on proposals granted")
		}
	}

	if !existed {
		// Role absent: the open succeeds and grants nothing.
		st0, err := openPostgres(dsn, newPostgresTestSchema(t))
		if err != nil {
			t.Fatalf("open without the reader role: %v", err)
		}
		st0.Close()
		if _, err := admin.Exec(`CREATE ROLE ` + UsersReaderRole + ` NOLOGIN`); err != nil {
			t.Skipf("cannot create role %s here: %v", UsersReaderRole, err)
		}
	}
	schema := newPostgresTestSchema(t)
	st, err := openPostgres(dsn, schema)
	if err != nil {
		t.Fatalf("open with the reader role: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	check(st, schema, true)
	// Idempotent: a second replica opening the same schema grants again.
	st2, err := openPostgres(dsn, schema)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	st2.Close()
	check(st, schema, true)
}

func TestExportedNames(t *testing.T) {
	if PostgresSchema != "corescope_users" || UsersReaderRole != "corescope_users_reader" {
		t.Fatalf("PostgresSchema=%q UsersReaderRole=%q", PostgresSchema, UsersReaderRole)
	}
}
