package users

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/dbx"
	"github.com/meshcore-analyzer/dbx/dbxtest"
)

// pgReaderTestLock serialises, across test processes, the tests that create
// or drop the cluster-wide UsersReaderRole.
const pgReaderTestLock = 0x43535552 // "CSUR"

// The ingestor's two reads (cmd/ingestor approvedChannelsQuery and
// linkedCompanionsQuery, qualified with the schema as it does; a package
// main cannot be imported, so keep these in step with it).
const (
	ingestorChannelsQuery   = `SELECT subject FROM %s.proposals WHERE kind = 'hashtag_channel' AND status = 'approved' ORDER BY decided_at, id LIMIT 10`
	ingestorCompanionsQuery = `SELECT pubkey FROM %s.companion_links`
)

func TestPostgresGrantsReaderRole(t *testing.T) {
	pgTestDSN(t)
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
		// Only the columns the ingestor reads, never a whole table.
		for _, rc := range pgReaderColumns {
			for _, c := range strings.Split(rc.columns, ", ") {
				if got := priv(st, `SELECT has_column_privilege(?, ?, ?, 'SELECT')`, UsersReaderRole, schema+"."+rc.table, c); got != want {
					t.Errorf("SELECT on %s.%s = %v, want %v", rc.table, c, got, want)
				}
			}
		}
		for _, c := range []string{"proposals.note", "proposals.proposer_id", "proposals.reviewer_id", "proposals.created_at",
			"companion_links.user_id", "companion_links.name", "companion_links.linked_at"} {
			table, col, _ := strings.Cut(c, ".")
			if priv(st, `SELECT has_column_privilege(?, ?, ?, 'SELECT')`, UsersReaderRole, schema+"."+table, col) {
				t.Errorf("SELECT on %s granted", c)
			}
		}
		// Nothing else: accounts, sessions and secrets stay private.
		for _, table := range []string{"proposals", "companion_links", "users", "sessions", "mqtt_logins", "rate_limits"} {
			if priv(st, `SELECT has_table_privilege(?, ?, 'SELECT')`, UsersReaderRole, schema+"."+table) {
				t.Errorf("table-wide SELECT on %s granted", table)
			}
		}
		if priv(st, `SELECT has_table_privilege(?, ?, 'INSERT,UPDATE,DELETE')`, UsersReaderRole, schema+".proposals") {
			t.Error("write on proposals granted")
		}
	}

	if !existed {
		// Role absent: the open succeeds and grants nothing.
		st0, err := openPostgres(newPostgresTestTarget(t))
		if err != nil {
			t.Fatalf("open without the reader role: %v", err)
		}
		st0.Close()
		if _, err := admin.Exec(`CREATE ROLE ` + UsersReaderRole + ` NOLOGIN`); err != nil {
			t.Skipf("cannot create role %s here: %v", UsersReaderRole, err)
		}
	}
	dsn, schema := newPostgresTestTarget(t)
	st, err := openPostgres(dsn, schema)
	if err != nil {
		t.Fatalf("open with the reader role: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	check(st, schema, true)
	// A table-wide SELECT, as an earlier version granted: the next open
	// replaces it with the column grants.
	if _, err := st.db.Exec(`GRANT SELECT ON proposals, companion_links TO ` + UsersReaderRole); err != nil {
		t.Fatal(err)
	}
	// Idempotent: a second replica opening the same schema grants again.
	st2, err := openPostgres(dsn, schema)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	st2.Close()
	check(st, schema, true)

	// As the ingestor: a login role that is a member of the group (as the
	// writer is) runs both queries, and nothing beyond those columns.
	ingestor, password := pgTestRole(t, admin, true, "")
	if _, err := admin.Exec(`GRANT ` + UsersReaderRole + ` TO ` + ingestor); err != nil {
		t.Fatal(err)
	}
	u := mustCreate(t, st, "grants@example.org", "Grants")
	for _, seed := range []string{
		`INSERT INTO proposals (kind, subject, status, created_at, decided_at) VALUES ('hashtag_channel', '#grants', 'approved', 1, 2)`,
		fmt.Sprintf(`INSERT INTO companion_links (pubkey, user_id, linked_at) VALUES ('aa11', %d, 1)`, u.ID),
	} {
		if _, err := st.db.Exec(seed); err != nil {
			t.Fatalf("%s: %v", seed, err)
		}
	}
	conn, err := sql.Open(dbx.PostgresDriverName, pgTestRoleDSN(t, ingestor, password, ""))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	q := `"` + schema + `"`
	for query, want := range map[string]string{
		fmt.Sprintf(ingestorChannelsQuery, q):   "#grants",
		fmt.Sprintf(ingestorCompanionsQuery, q): "aa11",
	} {
		var got string
		if err := conn.QueryRow(query).Scan(&got); err != nil || got != want {
			t.Fatalf("the ingestor's %q = %q, %v; want %q", query, got, err, want)
		}
	}
	for _, query := range []string{`SELECT note FROM ` + q + `.proposals`, `SELECT user_id FROM ` + q + `.companion_links`,
		`SELECT * FROM ` + q + `.proposals`, `SELECT email FROM ` + q + `.users`} {
		if _, err := conn.Exec(query); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("%q as the ingestor: err = %v, want permission denied", query, err)
		}
	}
}

func TestExportedNames(t *testing.T) {
	if PostgresSchema != "corescope_users" || UsersReaderRole != "corescope_users_reader" {
		t.Fatalf("PostgresSchema=%q UsersReaderRole=%q", PostgresSchema, UsersReaderRole)
	}
}
