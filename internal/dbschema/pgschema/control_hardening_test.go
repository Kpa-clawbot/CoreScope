package pgschema

import (
	"context"
	"database/sql"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/dbx"
	"github.com/meshcore-analyzer/dbx/dbxtest"
)

// createdAtPattern is the queues' pgNow text: UTC RFC3339 with a fixed
// nine-digit fraction (microseconds, then "000").
var createdAtPattern = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{6}000Z$`)

// pgNowSQL is internal/prunequeue's and internal/mbcapqueue's pgNow, copied
// (they are other modules): migration 0003's DEFAULT must produce the same.
const pgNowSQL = `to_char((clock_timestamp()) AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"000Z"')`

// openScratchDatabase creates a throwaway database on the test server and
// returns a connection to it (schema public), dropped when the test ends.
// corescope_control is per database, so tests that need it in a known state
// (a real v2 upgrade, triggers, no control tables at all) or that look at
// every schema in the database run here instead of in a dbxtest schema of the
// shared test database. Skips when the DSN is unset, is not a URL, or its
// role may not create databases.
func openScratchDatabase(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("CORESCOPE_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("CORESCOPE_TEST_PG_DSN not set; skipping Postgres test")
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Skip("CORESCOPE_TEST_PG_DSN is not a postgres:// URL; skipping scratch-database test")
	}
	admin, err := sql.Open(dbx.PostgresDriverName, dsn)
	if err != nil {
		t.Fatal(err)
	}
	name := "cs_scratch_" + randHex(t, 8)
	if _, err := admin.Exec(`CREATE DATABASE ` + dbx.QuoteIdent(name)); err != nil {
		admin.Close()
		if strings.Contains(err.Error(), "42501") {
			t.Skipf("test role may not CREATE DATABASE; skipping: %v", err)
		}
		t.Fatalf("create scratch database: %v", err)
	}
	u.Path = "/" + name
	db, err := sql.Open(dbx.PostgresDriverName, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		if _, err := admin.Exec(`DROP DATABASE IF EXISTS ` + dbx.QuoteIdent(name) + ` WITH (FORCE)`); err != nil {
			t.Errorf("drop scratch database %s: %v", name, err)
		}
		admin.Close()
	})
	return db
}

func controlStateDetail(t *testing.T, db *sql.DB, role string) (ControlGrantState, string) {
	t.Helper()
	state, detail, err := checkControlGrants(context.Background(), db, role)
	if err != nil {
		t.Fatalf("checkControlGrants: %v", err)
	}
	return state, detail
}

// A v2 database with rows in every control table upgrades to v3: the rows
// stay, created_at gets the pgNow DEFAULT and the payload caps are validated
// constraints.
func TestControlHardeningMigrationFromV2(t *testing.T) {
	ctx := context.Background()
	db := openScratchDatabase(t)
	migs, err := loadMigrations(migrationFS)
	if err != nil {
		t.Fatal(err)
	}
	if err := apply(ctx, db, nil, migs[:2]); err != nil {
		t.Fatalf("apply v2: %v", err)
	}
	old := map[string]string{} // table -> id of the row written at v2
	for _, tp := range controlTablePrivs {
		id := randHex(t, 16)
		old[tp.table] = id
		mustExec(t, db, `INSERT INTO `+controlSchema+`.`+tp.table+` (id, created_at, payload) VALUES (?, `+pgNowSQL+`, '{"v":2}')`, id)
		var def sql.NullString
		if err := db.QueryRow(`SELECT column_default FROM information_schema.columns
			WHERE table_schema = ? AND table_name = ? AND column_name = 'created_at'`, controlSchema, tp.table).Scan(&def); err != nil {
			t.Fatal(err)
		}
		if def.Valid {
			t.Fatalf("%s.created_at has a default at v2: %s", tp.table, def.String)
		}
	}

	var rec logRecorder
	if err := Apply(ctx, db, rec.logf); err != nil {
		t.Fatalf("Apply on a v2 database: %v", err)
	}
	if n, m := rec.count("applied migration"), rec.count("0003_control_hardening.sql"); n != 1 || m != 1 {
		t.Errorf("upgrade from v2 logged %d applications (%d of 0003), want only 0003: %v", n, m, rec.lines)
	}
	if got := versionRows(t, db); len(got) != 3 || got[2] != 3 {
		t.Errorf("schema_version = %v, want [1 2 3]", got)
	}

	for _, tp := range controlTablePrivs {
		tbl := controlSchema + "." + tp.table
		var oldAt, payload string
		if err := db.QueryRow(`SELECT created_at, payload FROM `+tbl+` WHERE id = ?`, old[tp.table]).Scan(&oldAt, &payload); err != nil {
			t.Fatalf("%s: v2 row lost: %v", tbl, err)
		}
		if payload != `{"v":2}` {
			t.Errorf("%s: v2 row payload = %q", tbl, payload)
		}
		var convalidated bool
		var condef string
		if err := db.QueryRow(`SELECT convalidated, pg_get_constraintdef(oid) FROM pg_constraint
			WHERE conrelid = to_regclass(?) AND conname = ?`, tbl, tp.table+"_payload_max").Scan(&convalidated, &condef); err != nil {
			t.Fatalf("%s: constraint %s_payload_max: %v", tbl, tp.table, err)
		}
		if !convalidated {
			t.Errorf("%s: %s_payload_max not validated", tbl, tp.table)
		}
		if want := "octet_length(payload) <= " + strconv.Itoa(controlPayloadMax[tp.table]); !strings.Contains(condef, want) {
			t.Errorf("%s: %s_payload_max = %s, want %s", tbl, tp.table, condef, want)
		}

		id := randHex(t, 16)
		mustExec(t, db, `INSERT INTO `+tbl+` (id, payload) VALUES (?, '{}')`, id)
		var newAt string
		var same bool
		// Same format as pgNow: compare against it rendered in one statement
		// alongside a fresh default, by shape and by order.
		if err := db.QueryRow(`SELECT created_at, created_at <= `+pgNowSQL+` FROM `+tbl+` WHERE id = ?`, id).Scan(&newAt, &same); err != nil {
			t.Fatal(err)
		}
		if !createdAtPattern.MatchString(newAt) {
			t.Errorf("%s: default created_at = %q, want pgNow format", tbl, newAt)
		}
		if !(oldAt < newAt) || !same {
			t.Errorf("%s: created_at not in text-time order: v2 row %q, v3 row %q (<= now: %v)", tbl, oldAt, newAt, same)
		}
	}

	// Re-running the file (as a second schema sharing corescope_control
	// does) changes nothing and does not fail.
	if _, err := db.Exec(migs[2].sql); err != nil {
		t.Fatalf("0003 again: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM pg_constraint c JOIN pg_class r ON r.oid = c.conrelid
		JOIN pg_namespace s ON s.oid = r.relnamespace WHERE s.nspname = ? AND c.conname LIKE '%payload_max'`, controlSchema).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("%d payload_max constraints after re-running 0003, want 3", n)
	}
}

// A v2 row over the new cap fails the upgrade, which rolls back to v2.
func TestControlHardeningMigrationRefusesOversizedRow(t *testing.T) {
	ctx := context.Background()
	db := openScratchDatabase(t)
	migs, err := loadMigrations(migrationFS)
	if err != nil {
		t.Fatal(err)
	}
	if err := apply(ctx, db, nil, migs[:2]); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO `+controlSchema+`.prune_requests (id, created_at, payload) VALUES (?, 'x', repeat('x', ?))`,
		randHex(t, 16), controlPayloadMax["prune_requests"]+1)
	if err := Apply(ctx, db, nil); err == nil || !strings.Contains(err.Error(), "prune_requests_payload_max") {
		t.Fatalf("Apply over an oversized v2 row = %v, want a prune_requests_payload_max violation", err)
	}
	if got := versionRows(t, db); len(got) != 2 {
		t.Errorf("schema_version after the failed upgrade = %v, want [1 2]", got)
	}
	var def sql.NullString
	if err := db.QueryRow(`SELECT column_default FROM information_schema.columns
		WHERE table_schema = ? AND table_name = 'prune_requests' AND column_name = 'created_at'`, controlSchema).Scan(&def); err != nil {
		t.Fatal(err)
	}
	if def.Valid {
		t.Errorf("created_at default survived the rolled-back migration: %s", def.String)
	}
}

func TestCheckControlGrantsStates(t *testing.T) {
	db := dbxtest.OpenPostgres(t)
	ctx := context.Background()
	if err := Apply(ctx, db, nil); err != nil {
		t.Fatal(err)
	}
	if s, d := controlStateDetail(t, db, "cs_absent_"+randHex(t, 6)); s != ControlAbsent || !strings.Contains(d, "does not exist") {
		t.Errorf("missing role: %v %q, want absent", s, d)
	}

	group := tempRole(t, db, "cs_ctl")
	g := dbx.QuoteIdent(group)
	if s, d := controlStateDetail(t, db, group); s != ControlIncomplete || !strings.Contains(d, "column "+controlSchema+".prune_requests.payload INSERT") {
		t.Errorf("no grants: %v %q, want incomplete naming the INSERT (payload) grant", s, d)
	}
	if err := ensureControlGrants(ctx, db, group); err != nil {
		t.Fatal(err)
	}
	if s, d := controlStateDetail(t, db, group); s != ControlExact || d != "" {
		t.Fatalf("after ensureControlGrants: %v %q, want exact", s, d)
	}
	// Exactly the contract: SELECT on all three, INSERT on two columns of
	// two tables, no table-level INSERT.
	var tableInsert, colInsert int
	if err := db.QueryRow(`SELECT
		(SELECT count(*) FROM information_schema.table_privileges WHERE grantee = ? AND table_schema = ? AND privilege_type = 'INSERT'),
		(SELECT count(*) FROM pg_attribute t JOIN pg_class c ON c.oid = t.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		   CROSS JOIN LATERAL aclexplode(t.attacl) a
		  WHERE n.nspname = ? AND a.grantee = (SELECT oid FROM pg_roles WHERE rolname = ?) AND a.privilege_type = 'INSERT')`,
		group, controlSchema, controlSchema, group).Scan(&tableInsert, &colInsert); err != nil {
		t.Fatal(err)
	}
	if tableInsert != 0 || colInsert != 4 {
		t.Errorf("table-level INSERT grants = %d (want 0), column-level = %d (want 4)", tableInsert, colInsert)
	}

	mustExec(t, db, `REVOKE SELECT ON TABLE `+controlSchema+`.prune_results FROM `+g)
	if s, d := controlStateDetail(t, db, group); s != ControlIncomplete || !strings.Contains(d, "prune_results SELECT") {
		t.Errorf("without SELECT on prune_results: %v %q, want incomplete", s, d)
	}
	// Excess wins over missing.
	mustExec(t, db, `GRANT UPDATE ON TABLE `+controlSchema+`.prune_results TO `+g)
	if s, d := controlStateDetail(t, db, group); s != ControlExcess || !strings.Contains(d, "prune_results UPDATE") || !strings.Contains(d, "lacks") {
		t.Errorf("UPDATE but no SELECT on prune_results: %v %q, want excess naming both", s, d)
	}
	if err := ensureControlGrants(ctx, db, group); err != nil {
		t.Fatal(err)
	}

	mustExec(t, db, `CREATE FUNCTION cs_probe() RETURNS int LANGUAGE sql AS 'SELECT 1'`)
	mustExec(t, db, `CREATE TABLE cs_owned (id int)`)
	mustExec(t, db, `CREATE SEQUENCE cs_seq`)
	mustExec(t, db, `CREATE TYPE cs_type AS (a int)`)
	other := tempRole(t, db, "cs_other")
	o := dbx.QuoteIdent(other)
	for _, c := range []struct{ do, undo, want string }{
		{`GRANT CREATE ON SCHEMA ` + controlSchema + ` TO ` + g, `REVOKE CREATE ON SCHEMA ` + controlSchema + ` FROM ` + g, "schema " + controlSchema + " CREATE"},
		{`GRANT USAGE ON SCHEMA ` + controlSchema + ` TO ` + g + ` WITH GRANT OPTION`, `REVOKE GRANT OPTION FOR USAGE ON SCHEMA ` + controlSchema + ` FROM ` + g, "USAGE WITH GRANT OPTION"},
		// FOR ROLE a role that never creates anything: default privileges of
		// the test's own role would reach every concurrent test's objects.
		{`ALTER DEFAULT PRIVILEGES FOR ROLE ` + o + ` IN SCHEMA ` + controlSchema + ` GRANT SELECT, INSERT ON TABLES TO ` + g,
			`ALTER DEFAULT PRIVILEGES FOR ROLE ` + o + ` IN SCHEMA ` + controlSchema + ` REVOKE ALL ON TABLES FROM ` + g,
			"default privileges for " + other + " in schema " + controlSchema + " on r INSERT"},
		{`ALTER DEFAULT PRIVILEGES FOR ROLE ` + o + ` GRANT USAGE ON SEQUENCES TO ` + g, `ALTER DEFAULT PRIVILEGES FOR ROLE ` + o + ` REVOKE ALL ON SEQUENCES FROM ` + g,
			"default privileges for " + other + " on S USAGE"},
		{`ALTER TABLE cs_owned OWNER TO ` + g, `ALTER TABLE cs_owned OWNER TO CURRENT_USER`, "owns table"},
		{`ALTER FUNCTION cs_probe() OWNER TO ` + g, `ALTER FUNCTION cs_probe() OWNER TO CURRENT_USER`, "owns function"},
		{`GRANT EXECUTE ON FUNCTION cs_probe() TO ` + g, `REVOKE EXECUTE ON FUNCTION cs_probe() FROM ` + g, "cs_probe() EXECUTE"},
		{`GRANT USAGE ON SEQUENCE cs_seq TO ` + g, `REVOKE USAGE ON SEQUENCE cs_seq FROM ` + g, "cs_seq USAGE"},
		{`GRANT USAGE ON TYPE cs_type TO ` + g, `REVOKE USAGE ON TYPE cs_type FROM ` + g, "cs_type USAGE"},
		{`GRANT SELECT (id) ON TABLE cs_owned TO ` + g, `REVOKE SELECT (id) ON TABLE cs_owned FROM ` + g, "cs_owned.id SELECT"},
		{`GRANT SET ON PARAMETER log_statement TO ` + g, `REVOKE SET ON PARAMETER log_statement FROM ` + g, "parameter log_statement SET"},
		{`GRANT CREATE ON TABLESPACE pg_default TO ` + g, `REVOKE CREATE ON TABLESPACE pg_default FROM ` + g, "tablespace pg_default CREATE"},
		{`GRANT ` + o + ` TO ` + g, `REVOKE ` + o + ` FROM ` + g, "member of " + other},
		{`ALTER ROLE ` + g + ` CREATEDB`, `ALTER ROLE ` + g + ` NOCREATEDB`, "attribute CREATEDB"},
		{`ALTER ROLE ` + g + ` LOGIN`, `ALTER ROLE ` + g + ` NOLOGIN`, "attribute LOGIN"},
	} {
		mustExec(t, db, c.do)
		if s, d := controlStateDetail(t, db, group); s != ControlExcess || !strings.Contains(d, c.want) {
			t.Errorf("after %q: %v %q, want excess naming %q", c.do, s, d, c.want)
		}
		mustExec(t, db, c.undo)
		if s, d := controlStateDetail(t, db, group); s != ControlExact {
			t.Errorf("after undoing %q: %v %q, want exact", c.do, s, d)
		}
	}
}

// Before the ingestor has created the control tables the state is absent,
// even for a role that exists.
func TestCheckControlGrantsAbsentWithoutTables(t *testing.T) {
	ctx := context.Background()
	db := openScratchDatabase(t)
	group := tempRole(t, db, "cs_ctl")
	if s, d := controlStateDetail(t, db, group); s != ControlAbsent || !strings.Contains(d, "prune_requests") {
		t.Errorf("no control schema: %v %q, want absent", s, d)
	}
	migs, err := loadMigrations(migrationFS)
	if err != nil {
		t.Fatal(err)
	}
	if err := apply(ctx, db, nil, migs[:1]); err != nil {
		t.Fatal(err)
	}
	if s, _ := controlStateDetail(t, db, group); s != ControlAbsent {
		t.Errorf("at version 1: %v, want absent", s)
	}
	if err := Apply(ctx, db, nil); err != nil {
		t.Fatal(err)
	}
	if s, _ := controlStateDetail(t, db, group); s != ControlIncomplete {
		t.Errorf("tables but no grants: %v, want incomplete", s)
	}
}

// Triggers run on the server's inserts as their definer: EnsureControlGrants
// refuses while any user trigger is on a control table. Runs in its own
// database: a trigger on the shared control tables would fail every other
// test's EnsureControlGrants.
func TestEnsureControlGrantsRefusesTriggers(t *testing.T) {
	ctx := context.Background()
	db := openScratchDatabase(t)
	if err := Apply(ctx, db, nil); err != nil {
		t.Fatal(err)
	}
	group := tempRole(t, db, "cs_ctl")
	mustExec(t, db, `CREATE FUNCTION cs_trig() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN RETURN NEW; END'`)
	// A foreign key adds internal triggers to the control table: not refused.
	mustExec(t, db, `ALTER TABLE `+controlSchema+`.prune_results ADD CONSTRAINT cs_fk FOREIGN KEY (id) REFERENCES `+controlSchema+`.prune_requests (id) NOT VALID`)
	if err := ensureControlGrants(ctx, db, group); err != nil {
		t.Fatalf("with only internal (FK) triggers: %v", err)
	}
	mustExec(t, db, `ALTER TABLE `+controlSchema+`.prune_results DROP CONSTRAINT cs_fk`)
	mustExec(t, db, `REVOKE ALL ON SCHEMA `+controlSchema+` FROM `+dbx.QuoteIdent(group))

	for _, tp := range controlTablePrivs {
		tbl := controlSchema + "." + tp.table
		mustExec(t, db, `CREATE TRIGGER cs_sneaky BEFORE INSERT ON `+tbl+` FOR EACH ROW EXECUTE FUNCTION cs_trig()`)
		err := ensureControlGrants(ctx, db, group)
		if err == nil || !strings.Contains(err.Error(), tp.table+".cs_sneaky") {
			t.Errorf("trigger on %s: ensureControlGrants = %v, want refusal naming it", tbl, err)
		}
		if s, _ := controlStateDetail(t, db, group); s == ControlExact {
			t.Errorf("trigger on %s: grants were made despite the refusal", tbl)
		}
		// Refused whether or not the role exists: the check comes first.
		if err := ensureControlGrants(ctx, db, "cs_absent_"+randHex(t, 6)); err == nil {
			t.Errorf("trigger on %s: ensureControlGrants for a missing role succeeded", tbl)
		}
		mustExec(t, db, `DROP TRIGGER cs_sneaky ON `+tbl)
	}
	if err := ensureControlGrants(ctx, db, group); err != nil {
		t.Fatalf("after dropping the triggers: %v", err)
	}
	if s, d := controlStateDetail(t, db, group); s != ControlExact {
		t.Errorf("after dropping the triggers: %v %q, want exact", s, d)
	}
}

func TestCheckSingleInstall(t *testing.T) {
	ctx := context.Background()
	db := openScratchDatabase(t)
	if err := CheckSingleInstall(ctx, db); err != nil {
		t.Fatalf("empty database: %v", err)
	}
	if err := Apply(ctx, db, nil); err != nil {
		t.Fatal(err)
	}
	if err := CheckSingleInstall(ctx, db); err != nil {
		t.Fatalf("one install: %v", err)
	}
	// corescope_control never counts, and neither does a schema without a
	// schema_version table or one whose schema_version is not a table.
	mustExec(t, db, `CREATE TABLE `+controlSchema+`.schema_version (version int)`)
	mustExec(t, db, `CREATE SCHEMA cs_unrelated`)
	mustExec(t, db, `CREATE TABLE cs_unrelated.other (id int)`)
	mustExec(t, db, `CREATE VIEW cs_unrelated.schema_version AS SELECT 1 AS version`)
	if err := CheckSingleInstall(ctx, db); err != nil {
		t.Fatalf("control schema and a view named schema_version: %v", err)
	}
	mustExec(t, db, `CREATE SCHEMA cs_second`)
	mustExec(t, db, `CREATE SCHEMA cs_third`)
	mustExec(t, db, `CREATE TABLE cs_second.schema_version (version int)`)
	mustExec(t, db, `CREATE TABLE cs_third.schema_version (version int)`)
	err := CheckSingleInstall(ctx, db)
	if err == nil {
		t.Fatal("two other installs: no error")
	}
	for _, want := range []string{"cs_second, cs_third", "one CoreScope install per database", "shared", "act on the first's requests"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "public") || strings.Contains(err.Error(), "cs_unrelated") {
		t.Errorf("error %q names a schema that does not count", err)
	}

	// A role with no privilege on the other schemas still sees them.
	reader := tempRole(t, db, "cs_reader")
	mustExec(t, db, `REVOKE ALL ON SCHEMA cs_second, cs_third FROM PUBLIC`)
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SET ROLE `+dbx.QuoteIdent(reader)); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), `RESET ROLE`)
	var n int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = 'schema_version' AND n.nspname IN ('cs_second', 'cs_third')`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("an unprivileged role sees %d of the 2 other schema_version tables", n)
	}
}

// In the shared test database every dbxtest schema is an install: the check
// names the others, never the caller's own schema.
func TestCheckSingleInstallSharedTestDatabase(t *testing.T) {
	ctx := context.Background()
	a := dbxtest.OpenPostgres(t)
	b := dbxtest.OpenPostgres(t)
	for _, db := range []*sql.DB{a, b} {
		if err := Apply(ctx, db, nil); err != nil {
			t.Fatal(err)
		}
	}
	var sa, sb string
	if err := a.QueryRow(`SELECT current_schema()`).Scan(&sa); err != nil {
		t.Fatal(err)
	}
	if err := b.QueryRow(`SELECT current_schema()`).Scan(&sb); err != nil {
		t.Fatal(err)
	}
	err := CheckSingleInstall(ctx, a)
	if err == nil || !strings.Contains(err.Error(), sb) || strings.Contains(err.Error(), sa) {
		t.Errorf("CheckSingleInstall from %s = %v, want an error naming %s and not %s", sa, err, sb, sa)
	}
}
