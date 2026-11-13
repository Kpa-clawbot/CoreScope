package pgschema

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/dbx"
	"github.com/meshcore-analyzer/dbx/dbxtest"
)

// randHex returns n random bytes as lower-case hex: a valid control-table id
// for n >= 8, unique across the tests sharing corescope_control.
func randHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// tempRole creates a NOLOGIN role with a random name and drops it (and every
// privilege it holds in this database) when the test ends. Roles are
// cluster-wide, so tests never use the real controlRole name.
func tempRole(t *testing.T, db *sql.DB, prefix string) string {
	t.Helper()
	name := prefix + "_" + randHex(t, 6)
	if _, err := db.Exec(`CREATE ROLE ` + dbx.QuoteIdent(name) + ` NOLOGIN`); err != nil {
		t.Fatalf("create role %s: %v", name, err)
	}
	t.Cleanup(func() {
		for _, s := range []string{`DROP OWNED BY ` + dbx.QuoteIdent(name), `DROP ROLE IF EXISTS ` + dbx.QuoteIdent(name)} {
			if _, err := db.Exec(s); err != nil {
				t.Errorf("%s: %v", s, err)
			}
		}
	})
	return name
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func controlOK(t *testing.T, db *sql.DB, role string) bool {
	t.Helper()
	return controlState(t, db, role) == ControlExact
}

func controlState(t *testing.T, db *sql.DB, role string) ControlGrantState {
	t.Helper()
	state, detail, err := checkControlGrants(context.Background(), db, role)
	if err != nil {
		t.Fatalf("checkControlGrants: %v", err)
	}
	if (state == ControlExact) != (detail == "") {
		t.Errorf("checkControlGrants = %v with detail %q", state, detail)
	}
	return state
}

func controlTableExists(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	var ok bool
	if err := db.QueryRow(`SELECT to_regclass(?) IS NOT NULL`, controlSchema+"."+table).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestControlMigrationFreshAndFromV1(t *testing.T) {
	ctx := context.Background()
	migs, err := loadMigrations(migrationFS)
	if err != nil {
		t.Fatal(err)
	}

	fresh := dbxtest.OpenPostgres(t)
	if err := Apply(ctx, fresh, nil); err != nil {
		t.Fatalf("Apply on a fresh schema: %v", err)
	}

	v1 := dbxtest.OpenPostgres(t)
	if err := apply(ctx, v1, nil, migs[:1]); err != nil {
		t.Fatalf("apply v1: %v", err)
	}
	if relationExists(t, v1, "ingestor_stats") {
		t.Fatal("ingestor_stats exists at version 1")
	}
	var rec logRecorder
	if err := Apply(ctx, v1, rec.logf); err != nil {
		t.Fatalf("Apply on a v1 schema: %v", err)
	}
	if n, m, h := rec.count("applied migration"), rec.count("0002_control.sql"), rec.count("0003_control_hardening.sql"); n != 2 || m != 1 || h != 1 {
		t.Errorf("upgrade from v1 logged %d applications (%d of 0002, %d of 0003), want 0002 and 0003: %v", n, m, h, rec.lines)
	}

	for name, db := range map[string]*sql.DB{"fresh": fresh, "from v1": v1} {
		if got := versionRows(t, db); len(got) != 3 || got[2] != 3 {
			t.Errorf("%s: schema_version = %v, want [1 2 3]", name, got)
		}
		if !relationExists(t, db, "ingestor_stats") {
			t.Errorf("%s: ingestor_stats missing", name)
		}
		for _, tp := range controlTablePrivs {
			if !controlTableExists(t, db, tp.table) {
				t.Errorf("%s: %s.%s missing", name, controlSchema, tp.table)
			}
		}
		if err := AssertReady(ctx, db); err != nil {
			t.Errorf("%s: AssertReady: %v", name, err)
		}
	}
}

// Nothing reaches PUBLIC: the control objects are created without PUBLIC
// privileges, and ingestor_stats gets only what the writer's default
// privileges give (none in a test schema).
func TestControlObjectsGrantNothingToPublic(t *testing.T) {
	db := dbxtest.OpenPostgres(t)
	if err := Apply(context.Background(), db, nil); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		CROSS JOIN LATERAL aclexplode(c.relacl) a
		WHERE a.grantee = 0 AND ((n.nspname = ? AND c.relname IN ('prune_requests', 'prune_results', 'mbcap_snapshots'))
			OR (n.nspname = current_schema() AND c.relname = 'ingestor_stats'))`, controlSchema).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("PUBLIC holds %d privileges on the new tables", n)
	}
	if err := db.QueryRow(`SELECT count(*) FROM pg_namespace CROSS JOIN LATERAL aclexplode(nspacl) a
		WHERE nspname = ? AND a.grantee = 0`, controlSchema).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("PUBLIC holds %d privileges on schema %s", n, controlSchema)
	}
}

func TestControlTableConstraints(t *testing.T) {
	db := dbxtest.OpenPostgres(t)
	if err := Apply(context.Background(), db, nil); err != nil {
		t.Fatal(err)
	}
	for _, tp := range controlTablePrivs {
		tbl := controlSchema + "." + tp.table
		good := randHex(t, 16)
		t.Cleanup(func() { db.Exec(`DELETE FROM `+tbl+` WHERE id = ?`, good) })
		if _, err := db.Exec(`INSERT INTO `+tbl+` (id, created_at, payload) VALUES (?, 'now', '{}')`, good); err != nil {
			t.Errorf("%s: valid row rejected: %v", tbl, err)
		}
		for _, bad := range []string{"", "0123456789abcde", "0123456789ABCDEF", "0123456789abcdeg", strings.Repeat("a", 65), "0123456789abcdef\n"} {
			if _, err := db.Exec(`INSERT INTO `+tbl+` (id, created_at, payload) VALUES (?, 'now', '{}')`, bad); err == nil {
				db.Exec(`DELETE FROM `+tbl+` WHERE id = ?`, bad)
				t.Errorf("%s: id %q accepted", tbl, bad)
			}
		}
		limit := controlPayloadMax[tp.table]
		atLimit := randHex(t, 16)
		t.Cleanup(func() { db.Exec(`DELETE FROM `+tbl+` WHERE id = ?`, atLimit) })
		if _, err := db.Exec(`INSERT INTO `+tbl+` (id, payload) VALUES (?, repeat('x', ?))`, atLimit, limit); err != nil {
			t.Errorf("%s: payload of exactly %d bytes rejected: %v", tbl, limit, err)
		}
		big := randHex(t, 16)
		// Multi-byte characters: the cap is in bytes, not characters.
		for _, payload := range []string{`repeat('x', ?)`, `repeat('é', ?)`} {
			n := limit + 1
			if strings.Contains(payload, "é") {
				n = limit/2 + 1
			}
			if _, err := db.Exec(`INSERT INTO `+tbl+` (id, payload) VALUES (?, `+payload+`)`, big, n); err == nil {
				db.Exec(`DELETE FROM `+tbl+` WHERE id = ?`, big)
				t.Errorf("%s: payload %s (n=%d) over %d bytes accepted", tbl, payload, n, limit)
			} else if !strings.Contains(err.Error(), tp.table+"_payload_max") {
				t.Errorf("%s: oversized payload: %v, want %s_payload_max violation", tbl, err, tp.table)
			}
		}
		if _, err := db.Exec(`INSERT INTO `+tbl+` (id, created_at, payload) VALUES (?, 'now', NULL)`, big); err == nil {
			db.Exec(`DELETE FROM `+tbl+` WHERE id = ?`, big)
			t.Errorf("%s: NULL payload accepted", tbl)
		}
	}
	mustExec(t, db, `INSERT INTO ingestor_stats (id, updated_at, payload) VALUES (1, 'now', '{}')`)
	if _, err := db.Exec(`INSERT INTO ingestor_stats (id, updated_at, payload) VALUES (2, 'now', '{}')`); err == nil {
		t.Error("ingestor_stats accepted a second row (id 2)")
	}
}

// controlPayloadMax is migration 0003's payload cap per control table, bytes.
var controlPayloadMax = map[string]int{
	"prune_requests":  1 << 20,
	"prune_results":   1 << 20,
	"mbcap_snapshots": 4 << 20,
}

func TestEnsureControlGrantsWithoutRole(t *testing.T) {
	db := dbxtest.OpenPostgres(t)
	ctx := context.Background()
	if err := Apply(ctx, db, nil); err != nil {
		t.Fatal(err)
	}
	absent := "cs_absent_" + randHex(t, 6)
	if err := ensureControlGrants(ctx, db, absent); err != nil {
		t.Fatalf("ensureControlGrants without the role: %v", err)
	}
	if s := controlState(t, db, absent); s != ControlAbsent {
		t.Errorf("checkControlGrants for a role that does not exist = %v, want absent", s)
	}
	// The real role may or may not exist on the test server; either way
	// EnsureControlGrants succeeds once the schema is applied.
	if err := EnsureControlGrants(ctx, db); err != nil {
		t.Fatalf("EnsureControlGrants: %v", err)
	}
}

func TestEnsureControlGrantsExactAndIdempotent(t *testing.T) {
	db := dbxtest.OpenPostgres(t)
	ctx := context.Background()
	if err := Apply(ctx, db, nil); err != nil {
		t.Fatal(err)
	}
	group := tempRole(t, db, "cs_ctl")
	if s := controlState(t, db, group); s != ControlIncomplete {
		t.Fatalf("checkControlGrants before any grant = %v, want incomplete", s)
	}

	// Extra privileges a careless operator might have handed out: all of
	// them must go.
	g := dbx.QuoteIdent(group)
	tables := controlSchema + ".prune_requests, " + controlSchema + ".prune_results, " + controlSchema + ".mbcap_snapshots"
	mustExec(t, db, `GRANT ALL ON SCHEMA `+controlSchema+` TO `+g)
	mustExec(t, db, `GRANT ALL ON TABLE `+tables+` TO `+g+` WITH GRANT OPTION`)
	mustExec(t, db, `GRANT UPDATE (payload) ON TABLE `+controlSchema+`.prune_results TO `+g)

	for i := 0; i < 2; i++ {
		if err := ensureControlGrants(ctx, db, group); err != nil {
			t.Fatalf("ensureControlGrants #%d: %v", i+1, err)
		}
		if !controlOK(t, db, group) {
			t.Fatalf("controlGrantsOK false after ensureControlGrants #%d", i+1)
		}
	}

	// Each deviation turns the check false, and ensureControlGrants repairs
	// those it owns.
	repairable := []string{
		`GRANT UPDATE ON TABLE ` + controlSchema + `.prune_requests TO ` + g,
		`GRANT UPDATE (created_at) ON TABLE ` + controlSchema + `.mbcap_snapshots TO ` + g,
		`REVOKE INSERT ON TABLE ` + controlSchema + `.mbcap_snapshots FROM ` + g,
		`REVOKE INSERT (payload) ON TABLE ` + controlSchema + `.prune_requests FROM ` + g,
		`GRANT INSERT ON TABLE ` + controlSchema + `.prune_requests TO ` + g,
		`GRANT INSERT (created_at) ON TABLE ` + controlSchema + `.prune_requests TO ` + g,
		`GRANT INSERT (id) ON TABLE ` + controlSchema + `.mbcap_snapshots TO ` + g + ` WITH GRANT OPTION`,
		`GRANT SELECT ON TABLE ` + controlSchema + `.prune_results TO ` + g + ` WITH GRANT OPTION`,
		`GRANT CREATE ON SCHEMA ` + controlSchema + ` TO ` + g,
		`REVOKE USAGE ON SCHEMA ` + controlSchema + ` FROM ` + g,
	}
	for _, s := range repairable {
		mustExec(t, db, s)
		if controlOK(t, db, group) {
			t.Errorf("controlGrantsOK true after %q", s)
		}
		if err := ensureControlGrants(ctx, db, group); err != nil {
			t.Fatal(err)
		}
		if !controlOK(t, db, group) {
			t.Errorf("ensureControlGrants did not repair %q", s)
		}
	}

	// Outside the control tables only the roles script can fix it, but the
	// check still sees it.
	other := tempRole(t, db, "cs_other")
	for _, c := range []struct{ do, undo string }{
		{`GRANT SELECT ON TABLE nodes TO ` + g, `REVOKE SELECT ON TABLE nodes FROM ` + g},
		{`GRANT CREATE ON DATABASE ` + dbx.QuoteIdent(currentDatabase(t, db)) + ` TO ` + g, `REVOKE CREATE ON DATABASE ` + dbx.QuoteIdent(currentDatabase(t, db)) + ` FROM ` + g},
		{`GRANT ` + dbx.QuoteIdent(other) + ` TO ` + g, `REVOKE ` + dbx.QuoteIdent(other) + ` FROM ` + g},
		{`ALTER ROLE ` + g + ` LOGIN`, `ALTER ROLE ` + g + ` NOLOGIN`},
		{`ALTER ROLE ` + g + ` BYPASSRLS`, `ALTER ROLE ` + g + ` NOBYPASSRLS`},
	} {
		mustExec(t, db, c.do)
		if controlOK(t, db, group) {
			t.Errorf("controlGrantsOK true after %q", c.do)
		}
		mustExec(t, db, c.undo)
		if !controlOK(t, db, group) {
			t.Errorf("controlGrantsOK false after undoing %q", c.do)
		}
	}
}

func currentDatabase(t *testing.T, db *sql.DB) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT current_database()`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// A member of the group role (as the server's reader is) may write exactly
// the two request tables, and only by INSERT.
func TestControlGroupMemberPrivileges(t *testing.T) {
	db := dbxtest.OpenPostgres(t)
	ctx := context.Background()
	if err := Apply(ctx, db, nil); err != nil {
		t.Fatal(err)
	}
	group := tempRole(t, db, "cs_ctl")
	member := tempRole(t, db, "cs_member")
	if err := ensureControlGrants(ctx, db, group); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `GRANT `+dbx.QuoteIdent(group)+` TO `+dbx.QuoteIdent(member))
	// What the roles script gives the reader on the main schema.
	var schema string
	if err := db.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `GRANT USAGE ON SCHEMA `+dbx.QuoteIdent(schema)+` TO `+dbx.QuoteIdent(member))
	mustExec(t, db, `GRANT SELECT ON ALL TABLES IN SCHEMA `+dbx.QuoteIdent(schema)+` TO `+dbx.QuoteIdent(member))

	var mainTables []string
	rows, err := db.Query(`SELECT table_name FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_type = 'BASE TABLE' ORDER BY table_name`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		mainTables = append(mainTables, s)
	}
	rows.Close()
	if len(mainTables) < 20 {
		t.Fatalf("only %d main-schema tables", len(mainTables))
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SET ROLE `+dbx.QuoteIdent(member)); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), `RESET ROLE`)

	var inserted []string // "table id", deleted again as the superuser
	t.Cleanup(func() {
		for _, s := range inserted {
			tbl, id, _ := strings.Cut(s, " ")
			db.Exec(`DELETE FROM `+tbl+` WHERE id = ?`, id)
		}
	})
	allowed := func(q string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(ctx, q, args...); err != nil {
			t.Errorf("member: %s: %v", q, err)
		}
	}
	denied := func(q string, args ...any) {
		t.Helper()
		_, err := conn.ExecContext(ctx, q, args...)
		if err == nil {
			t.Errorf("member: %s succeeded, want permission denied", q)
		} else if !strings.Contains(err.Error(), "42501") {
			t.Errorf("member: %s: %v, want permission denied (42501)", q, err)
		}
	}

	for _, tp := range controlTablePrivs {
		tbl := controlSchema + "." + tp.table
		id := randHex(t, 16)
		ins := `INSERT INTO ` + tbl + ` (id, payload) VALUES (?, '{}')`
		if len(tp.insertCols) > 0 {
			allowed(ins, id)
			inserted = append(inserted, tbl+" "+id)
			var createdAt string
			if err := conn.QueryRowContext(ctx, `SELECT created_at FROM `+tbl+` WHERE id = ?`, id).Scan(&createdAt); err != nil {
				t.Errorf("member: read back %s: %v", tbl, err)
			} else if !createdAtPattern.MatchString(createdAt) {
				t.Errorf("member: %s created_at = %q, want the database clock in pgNow format", tbl, createdAt)
			}
		} else {
			denied(ins, randHex(t, 16))
		}
		// The server never chooses the timestamp, not even through ON CONFLICT.
		denied(`INSERT INTO `+tbl+` (id, created_at, payload) VALUES (?, '9999-12-31T23:59:59.999999000Z', '{}')`, randHex(t, 16))
		denied(`INSERT INTO `+tbl+` (id, payload) VALUES (?, '{}') ON CONFLICT (id) DO UPDATE SET created_at = 'x'`, randHex(t, 16))
		allowed(`SELECT count(*) FROM ` + tbl)
		denied(`UPDATE ` + tbl + ` SET payload = payload WHERE false`)
		denied(`DELETE FROM ` + tbl + ` WHERE false`)
		denied(`TRUNCATE ` + tbl)
	}
	denied(`CREATE TABLE ` + controlSchema + `.sneaky (id int)`)
	denied(`INSERT INTO ingestor_stats (id, updated_at, payload) VALUES (1, 'now', '{}')`)
	for _, tbl := range mainTables {
		denied(`DELETE FROM ` + dbx.QuoteIdent(tbl) + ` WHERE false`)
		denied(`UPDATE ` + dbx.QuoteIdent(tbl) + ` SET ` + firstColumn(t, db, tbl) + ` = ` + firstColumn(t, db, tbl) + ` WHERE false`)
		denied(`TRUNCATE ` + dbx.QuoteIdent(tbl))
	}
	denied(`CREATE TABLE sneaky (id int)`)
}

func firstColumn(t *testing.T, db *sql.DB, table string) string {
	t.Helper()
	var c string
	if err := db.QueryRow(`SELECT column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = ? ORDER BY ordinal_position LIMIT 1`, table).Scan(&c); err != nil {
		t.Fatal(err)
	}
	return dbx.QuoteIdent(c)
}
