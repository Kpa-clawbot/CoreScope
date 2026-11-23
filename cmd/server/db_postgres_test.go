package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/dbschema/pgschema"
	"github.com/meshcore-analyzer/dbx"
	"github.com/meshcore-analyzer/dbx/dbxtest"
)

var testPGParams = pgServerParams(120)

func TestPGServerParams(t *testing.T) {
	got := map[string]string{}
	for _, p := range pgServerParams(0) {
		got[p.name] = p.value
	}
	want := map[string]string{pgReadOnlyParam: "on", pgStmtTimeoutParam: "0", pgIdleTxParam: "60000"}
	if len(got) != len(want) {
		t.Fatalf("params = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	for _, p := range testPGParams {
		if p.name == pgStmtTimeoutParam && p.value != "120000" {
			t.Errorf("120s statement timeout = %q ms", p.value)
		}
	}
}

func TestPGServerDSNURL(t *testing.T) {
	for _, in := range []string{
		"postgres://u:s3cret@h:5432/db?sslmode=disable",
		"postgresql://u:s3cret@h/db?default_transaction_read_only=off&sslmode=disable",
		// Postgres matches parameter names case-insensitively, so must we.
		"postgresql://u:s3cret@h/db?Default_Transaction_Read_Only=off&STATEMENT_TIMEOUT=0&sslmode=disable",
		"postgres://u:s3cret@h/db?sslmode=disable&options=-c%20search_path%3Dcs",
	} {
		out, pw, err := pgServerDSN(in, testPGParams)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if !slices.Contains(pw, "s3cret") {
			t.Errorf("%q: password = %q", in, pw)
		}
		u, err := url.Parse(out)
		if err != nil {
			t.Fatalf("result does not parse: %v", err)
		}
		q := u.Query()
		for _, p := range testPGParams {
			n := 0
			for k, vs := range q {
				if strings.EqualFold(k, p.name) {
					n += len(vs)
					if k != p.name || len(vs) != 1 || vs[0] != p.value {
						t.Errorf("%q: %s = %v, want exactly [%s]", in, k, vs, p.value)
					}
				}
			}
			if n != 1 {
				t.Errorf("%q: %d values for %s, want 1 (%s)", in, n, p.name, out)
			}
		}
		if q.Get("sslmode") != "disable" || u.Host == "" || u.Path != "/db" {
			t.Errorf("%q: other parts not preserved: %q", in, out)
		}
	}
}

func TestPGServerDSNKeywordValue(t *testing.T) {
	in := `host=h  user = u password='p w\'x\\y' dbname=db DEFAULT_TRANSACTION_READ_ONLY=off statement_timeout=0 sslmode=disable options='-c search_path=cs'`
	out, pw, err := pgServerDSN(in, testPGParams)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(pw, `p w'x\y`) {
		t.Errorf("password = %q", pw)
	}
	pairs, err := parsePGKeywordValue(out)
	if err != nil {
		t.Fatalf("result does not re-parse: %v (%s)", err, out)
	}
	got := map[string][]string{}
	for _, kv := range pairs {
		got[strings.ToLower(kv[0])] = append(got[strings.ToLower(kv[0])], kv[1])
	}
	want := map[string]string{"host": "h", "user": "u", "password": `p w'x\y`, "dbname": "db", "sslmode": "disable",
		"options": "-c search_path=cs", pgReadOnlyParam: "on", pgStmtTimeoutParam: "120000", pgIdleTxParam: "60000"}
	for k, v := range want {
		if len(got[k]) != 1 || got[k][0] != v {
			t.Errorf("%s = %q, want [%q]", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("keys = %v, want %v", got, want)
	}
}

// An options parameter that sets a forced parameter is refused, not dropped:
// whoever wrote it expects a different value than the server will use.
func TestPGServerDSNRejectsForcedParamInOptions(t *testing.T) {
	for _, in := range []string{
		`host=h password=s3cret options='-c default_transaction_read_only=off'`,
		`host=h password=s3cret OPTIONS='-c Statement_Timeout=0'`,
		`host=h password=s3cret options='--idle-in-transaction-session-timeout=0'`,
		"postgres://u:s3cret@h/db?options=-c%20default_transaction_read_only%3Doff",
		"postgres://u:s3cret@h/db?Options=--statement-timeout%3D0",
	} {
		_, _, err := pgServerDSN(in, testPGParams)
		if err == nil || !strings.Contains(err.Error(), "options") {
			t.Errorf("%q: err = %v, want a refusal naming options", in, err)
			continue
		}
		if strings.Contains(err.Error(), "s3cret") {
			t.Errorf("error quotes the DSN: %v", err)
		}
	}
}

func TestPGServerDSNErrorsOmitInput(t *testing.T) {
	for _, in := range []string{
		"host=h password='unterminated-s3cret",
		"host=h s3cret-without-equals",
		"postgres://u:s3cret@h:bad port/db",
	} {
		_, _, err := pgServerDSN(in, testPGParams)
		if err == nil {
			t.Errorf("%q: expected an error", in)
			continue
		}
		if strings.Contains(err.Error(), "s3cret") {
			t.Errorf("error quotes the DSN: %v", err)
		}
	}
}

func TestScrubPGSecret(t *testing.T) {
	inner := errors.New("auth failed for password hunter2 (hunter2)")
	err := scrubPGSecret(inner, "hunter2")
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("password survived: %v", err)
	}
	if !errors.Is(err, inner) {
		t.Error("scrubbed error must unwrap to the original")
	}
	if scrubPGSecret(inner, "") != inner {
		t.Error("an empty password must leave the error alone")
	}
}

// pgTestRole creates a LOGIN role with a random name and password, runs grant
// (with %s for the quoted role and schema) on the seed handle, and returns a
// DSN logging in as it with search_path set to the test schema. The role is
// dropped in Cleanup; close any handle opened with the DSN in a Cleanup
// registered after this call, so it runs first.
func pgTestRole(t *testing.T, seed *sql.DB, grants ...string) (role, password, dsn string) {
	t.Helper()
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	role = "cs_test_" + hex.EncodeToString(b[:])
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	password = "pw" + hex.EncodeToString(b[:])
	var schema string
	if err := seed.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if _, err := seed.Exec(`CREATE ROLE ` + role + ` LOGIN PASSWORD '` + password + `'`); err != nil {
		t.Fatalf("create role: %v", err)
	}
	t.Cleanup(func() {
		for _, q := range []string{`DROP OWNED BY ` + role, `DROP ROLE ` + role} {
			if _, err := seed.Exec(q); err != nil {
				t.Errorf("%s: %v", q, err)
			}
		}
	})
	for _, g := range grants {
		g = strings.ReplaceAll(strings.ReplaceAll(g, "{schema}", `"`+schema+`"`), "{role}", role)
		if _, err := seed.Exec(g); err != nil {
			t.Fatalf("%s: %v", g, err)
		}
	}
	u, err := url.Parse(os.Getenv("CORESCOPE_TEST_PG_DSN"))
	if err != nil {
		t.Fatal("unparseable test DSN")
	}
	u.User = url.UserPassword(role, password)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return role, password, u.String()
}

// pgSchemaSeed returns a writable handle on a fresh end-state schema.
func pgSchemaSeed(t *testing.T) *sql.DB {
	t.Helper()
	seed := dbxtest.OpenPostgres(t)
	if err := pgschema.Apply(context.Background(), seed, t.Logf); err != nil {
		t.Fatalf("pgschema.Apply: %v", err)
	}
	return seed
}

var pgReadOnlyGrants = []string{
	`GRANT USAGE ON SCHEMA {schema} TO {role}`,
	`GRANT SELECT ON ALL TABLES IN SCHEMA {schema} TO {role}`,
}

func TestOpenPostgresDBReadOnlyRole(t *testing.T) {
	seed := pgSchemaSeed(t)
	_, _, dsn := pgTestRole(t, seed, pgReadOnlyGrants...)

	d, err := OpenPostgresDB(dsn)
	if err != nil {
		t.Fatalf("OpenPostgresDB with a read-only role: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	if !d.isPostgres() || d.path != "" || !d.isV3 {
		t.Fatalf("pg=%v path=%q isV3=%v", d.pg, d.path, d.isV3)
	}
	if d.stmtCountTransmissions == nil {
		t.Error("prepared statements were not prepared")
	}
	if _, err := d.GetStats(); err != nil {
		t.Errorf("GetStats through the read-only handle: %v", err)
	}

	// Layer 1: the session default refuses the write before privileges are
	// even consulted.
	_, err = d.conn.Exec(`INSERT INTO nodes (public_key, name) VALUES ('x', 'y')`)
	if err == nil || !strings.Contains(err.Error(), "SQLSTATE 25006") {
		t.Fatalf("write through the server handle: err = %v, want a read-only-transaction error (25006)", err)
	}

	// Layer 2: a session that turns the default off still cannot write,
	// because the role holds no write privilege.
	ctx := context.Background()
	c, err := d.conn.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.ExecContext(ctx, `SET default_transaction_read_only = off`); err != nil {
		t.Fatal(err)
	}
	defer c.ExecContext(ctx, `RESET default_transaction_read_only`)
	_, err = c.ExecContext(ctx, `INSERT INTO nodes (public_key, name) VALUES ('x', 'y')`)
	if err == nil || !strings.Contains(err.Error(), "SQLSTATE 42501") {
		t.Fatalf("write with the default overridden: err = %v, want permission denied (42501)", err)
	}
}

func TestOpenPostgresDBRefusesWritableRole(t *testing.T) {
	seed := pgSchemaSeed(t)
	role, password, dsn := pgTestRole(t, seed, append(pgReadOnlyGrants, `GRANT INSERT ON nodes TO {role}`)...)

	d, err := OpenPostgresDB(dsn)
	if err == nil {
		d.Close()
		t.Fatal("OpenPostgresDB accepted a role with INSERT on nodes")
	}
	msg := err.Error()
	for _, want := range []string{role, "nodes", "GRANT USAGE ON SCHEMA", "GRANT SELECT ON ALL TABLES IN SCHEMA", "ALTER DEFAULT PRIVILEGES"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error lacks %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, password) || strings.Contains(msg, dsn) {
		t.Errorf("error leaks the DSN or password:\n%s", msg)
	}
}

func TestOpenPostgresDBConnectErrorOmitsSecret(t *testing.T) {
	seed := pgSchemaSeed(t)
	_, password, dsn := pgTestRole(t, seed, pgReadOnlyGrants...)
	bad := strings.Replace(dsn, password, password+"wrong", 1)

	d, err := OpenPostgresDB(bad)
	if err == nil {
		d.Close()
		t.Fatal("OpenPostgresDB succeeded with a wrong password")
	}
	if strings.Contains(err.Error(), password) || strings.Contains(err.Error(), bad) {
		t.Errorf("error leaks the DSN or password: %v", err)
	}
}

// pgRouter serves srvDB through the real routes, with an API key so the
// admin-gated /api/backup is reachable.
func pgRouter(t *testing.T, db *DB) *mux.Router {
	t.Helper()
	srv := NewServer(db, &Config{Port: 3000, APIKey: "test-secret-key-strong-enough"}, NewHub())
	r := mux.NewRouter()
	srv.RegisterRoutes(r)
	return r
}

func serveJSON(t *testing.T, r *mux.Router, path string) (int, map[string]json.RawMessage) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("X-API-Key", "test-secret-key-strong-enough")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var body map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: body is not a JSON object: %q", path, w.Body.String())
	}
	return w.Code, body
}

func TestPostgresPerfAndAdminEndpoints(t *testing.T) {
	db, seed := newPostgresTestDB(t)
	if _, err := seed.Exec(`INSERT INTO nodes (public_key, name) VALUES ('aa', 'n')`); err != nil {
		t.Fatal(err)
	}
	r := pgRouter(t, db)

	code, body := serveJSON(t, r, "/api/perf")
	if code != http.StatusOK {
		t.Fatalf("/api/perf: %d", code)
	}
	var st SqliteStats
	if err := json.Unmarshal(body["sqlite"], &st); err != nil {
		t.Fatalf("decode sqlite block: %v", err)
	}
	if st.Engine != "postgres" {
		t.Errorf("engine = %q, want postgres", st.Engine)
	}
	if st.DbSizeMB <= 0 {
		t.Errorf("dbSizeMB = %v, want > 0", st.DbSizeMB)
	}
	if st.WalSizeMB != 0 || st.FreelistMB != 0 || st.WalPages == nil || *st.WalPages != (WalPages{}) {
		t.Errorf("SQLite-only figures not zero: wal=%v freelist=%v walPages=%+v", st.WalSizeMB, st.FreelistMB, st.WalPages)
	}
	if st.Rows == nil || st.Rows.Transmissions < 0 || st.Rows.Observations < 0 || st.Rows.Nodes < 0 || st.Rows.Observers < 0 {
		t.Errorf("rows = %+v, want non-negative counts", st.Rows)
	}

	code, body = serveJSON(t, r, "/api/perf/sqlite")
	if code != http.StatusNotFound || string(body["error"]) != `"not available: the database is PostgreSQL"` {
		t.Errorf("/api/perf/sqlite: %d %s", code, body["error"])
	}

	code, body = serveJSON(t, r, "/api/backup")
	if code != http.StatusNotImplemented || string(body["error"]) != `"backups of a PostgreSQL database are taken with pg_dump; see docs/postgresql.md"` {
		t.Errorf("/api/backup: %d %s", code, body["error"])
	}
}

func TestSQLitePerfReportsEngine(t *testing.T) {
	_, r := setupTestServer(t)
	code, body := serveJSON(t, r, "/api/perf")
	if code != http.StatusOK {
		t.Fatalf("/api/perf: %d", code)
	}
	var st SqliteStats
	if err := json.Unmarshal(body["sqlite"], &st); err != nil {
		t.Fatal(err)
	}
	if st.Engine != "sqlite" {
		t.Errorf("engine = %q, want sqlite", st.Engine)
	}
}

// pgGroupRole creates a NOLOGIN role with a random name, dropped in Cleanup.
func pgGroupRole(t *testing.T, seed *sql.DB) string {
	t.Helper()
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	name := "cs_group_" + hex.EncodeToString(b[:])
	if _, err := seed.Exec(`CREATE ROLE ` + name + ` NOLOGIN`); err != nil {
		t.Fatalf("create group role: %v", err)
	}
	t.Cleanup(func() {
		for _, q := range []string{`DROP OWNED BY ` + name, `DROP ROLE ` + name} {
			if _, err := seed.Exec(q); err != nil {
				t.Errorf("%s: %v", q, err)
			}
		}
	})
	return name
}

func pgServerVersionNum(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRow(`SELECT current_setting('server_version_num')::int`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// Each way a role can hold write power that a table-only, direct-grant check
// would miss must refuse startup.
func TestOpenPostgresDBRefusesIndirectWritePower(t *testing.T) {
	cases := []struct {
		name  string
		min   int                                       // server_version_num needed; 0 = any
		setup func(seed *sql.DB, group string) []string // extra statements; {role} is the login role
		inErr string
	}{
		{"insert on auto-updatable view", 0, func(*sql.DB, string) []string {
			return []string{`GRANT INSERT ON packets_v TO {role}`}
		}, "packets_v"},
		{"column-level update grant", 0, func(*sql.DB, string) []string {
			return []string{`GRANT UPDATE (name) ON nodes TO {role}`}
		}, "nodes"},
		{"maintain on table", 170000, func(*sql.DB, string) []string {
			return []string{`GRANT MAINTAIN ON nodes TO {role}`}
		}, "nodes"},
		{"maintain on materialized view", 170000, func(seed *sql.DB, _ string) []string {
			return []string{`CREATE MATERIALIZED VIEW cs_test_mv AS SELECT 1 AS x`, `GRANT MAINTAIN ON cs_test_mv TO {role}`}
		}, "cs_test_mv"},
		{"non-inherited membership in a writing role", 0, func(_ *sql.DB, group string) []string {
			return []string{`GRANT INSERT ON nodes TO ` + group, `ALTER ROLE {role} NOINHERIT`, `GRANT ` + group + ` TO {role}`}
		}, "via role"},
		{"membership in pg_write_all_data", 140000, func(*sql.DB, string) []string {
			return []string{`GRANT pg_write_all_data TO {role}`}
		}, "pg_write_all_data"},
		{"trigger privilege", 0, func(*sql.DB, string) []string {
			return []string{`GRANT TRIGGER ON nodes TO {role}`}
		}, "nodes"},
		{"createrole attribute", 0, func(*sql.DB, string) []string {
			return []string{`ALTER ROLE {role} CREATEROLE`}
		}, "CREATEROLE"},
		{"bypassrls through a group", 0, func(_ *sql.DB, group string) []string {
			return []string{`ALTER ROLE ` + group + ` BYPASSRLS`, `GRANT ` + group + ` TO {role}`}
		}, "BYPASSRLS"},
		{"superuser group, not inherited", 0, func(_ *sql.DB, group string) []string {
			return []string{`ALTER ROLE ` + group + ` SUPERUSER`, `ALTER ROLE {role} NOINHERIT`, `GRANT ` + group + ` TO {role}`}
		}, "SUPERUSER"},
		{"create on the search_path schema", 0, func(*sql.DB, string) []string {
			return []string{`GRANT CREATE ON SCHEMA {schema} TO {role}`}
		}, "can CREATE in schema"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seed := pgSchemaSeed(t)
			if v := pgServerVersionNum(t, seed); v < tc.min {
				t.Skipf("needs server_version_num >= %d, have %d", tc.min, v)
			}
			group := pgGroupRole(t, seed)
			grants := append(append([]string{}, pgReadOnlyGrants...), tc.setup(seed, group)...)
			role, password, dsn := pgTestRole(t, seed, grants...)
			d, err := OpenPostgresDB(dsn)
			if err == nil {
				d.Close()
				t.Fatal("OpenPostgresDB accepted a role with write power")
			}
			msg := err.Error()
			for _, want := range []string{role, tc.inErr, "GRANT SELECT ON ALL TABLES IN SCHEMA"} {
				if !strings.Contains(msg, want) {
					t.Errorf("error lacks %q:\n%s", want, msg)
				}
			}
			if strings.Contains(msg, password) {
				t.Errorf("error leaks the password:\n%s", msg)
			}
		})
	}
}

// SELECT granted through a group role is the common setup and must start.
func TestOpenPostgresDBAcceptsReadOnlyGroupMembership(t *testing.T) {
	seed := pgSchemaSeed(t)
	group := pgGroupRole(t, seed)
	_, _, dsn := pgTestRole(t, seed,
		`GRANT USAGE ON SCHEMA {schema} TO `+group,
		`GRANT SELECT ON ALL TABLES IN SCHEMA {schema} TO `+group,
		`GRANT `+group+` TO {role}`,
		`GRANT pg_read_all_stats TO {role}`)
	d, err := OpenPostgresDB(dsn)
	if err != nil {
		t.Fatalf("read-only group membership refused: %v", err)
	}
	d.Close()
}

func TestOpenPostgresDBStatementTimeout(t *testing.T) {
	seed := pgSchemaSeed(t)
	_, _, dsn := pgTestRole(t, seed, pgReadOnlyGrants...)

	if _, err := OpenPostgresDBWithStatementTimeout(dsn, -1); err == nil {
		t.Error("negative statement timeout accepted")
	}

	d, err := OpenPostgresDBWithStatementTimeout(dsn, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	for name, want := range map[string]string{pgStmtTimeoutParam: "1000", pgIdleTxParam: "60000", pgReadOnlyParam: "on"} {
		var got string
		if err := d.conn.QueryRow(`SELECT setting FROM pg_settings WHERE name = ?`, name).Scan(&got); err != nil || got != want {
			t.Errorf("%s = %q (%v), want %q", name, got, err, want)
		}
	}
	_, err = d.conn.Exec(`SELECT pg_sleep(3)`)
	if err == nil || !strings.Contains(err.Error(), "SQLSTATE 57014") {
		t.Errorf("3s query under a 1s statement_timeout: err = %v, want query_canceled (57014)", err)
	}

	d0, err := OpenPostgresDBWithStatementTimeout(dsn, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer d0.Close()
	var got string
	if err := d0.conn.QueryRow(`SELECT setting FROM pg_settings WHERE name = 'statement_timeout'`).Scan(&got); err != nil || got != "0" {
		t.Errorf("timeout 0: statement_timeout = %q (%v), want 0 (disabled)", got, err)
	}
}

// Overrides that never pass through the DSN — a role-level setting and
// PGOPTIONS in the environment — cannot defeat the forced parameters: startup
// parameters take precedence over both, and the open verifies the effective
// values in pg_settings.
func TestOpenPostgresDBForcedParamsBeatOutsideOverrides(t *testing.T) {
	seed := pgSchemaSeed(t)
	_, _, dsn := pgTestRole(t, seed, append(append([]string{}, pgReadOnlyGrants...),
		`ALTER ROLE {role} SET idle_in_transaction_session_timeout = 0`,
		`ALTER ROLE {role} SET default_transaction_read_only = off`)...)
	t.Setenv("PGOPTIONS", "-c statement_timeout=0 -c default_transaction_read_only=off")
	d, err := OpenPostgresDB(dsn)
	if err != nil {
		t.Fatalf("OpenPostgresDB: %v", err)
	}
	defer d.Close()
	for name, want := range map[string]string{pgStmtTimeoutParam: "120000", pgIdleTxParam: "60000", pgReadOnlyParam: "on"} {
		var got string
		if err := d.conn.QueryRow(`SELECT setting FROM pg_settings WHERE name = ?`, name).Scan(&got); err != nil || got != want {
			t.Errorf("%s = %q (%v), want %q", name, got, err, want)
		}
	}
}

// resetPGStatsCache gives a test a clean stats slot and hook.
func resetPGStatsCache(t *testing.T) *int {
	t.Helper()
	calls := 0
	pgStatsCache.mu.Lock()
	pgStatsCache.db = nil
	pgStatsCache.mu.Unlock()
	pgStatsQueryHook = func() { calls++ }
	t.Cleanup(func() { pgStatsQueryHook = nil })
	return &calls
}

// Within pgStatsTTL the stats are served from cache without a query.
func TestPostgresDBStatsCached(t *testing.T) {
	db, _ := newPostgresTestDB(t)
	calls := resetPGStatsCache(t)
	first := db.postgresDBStats()
	second := db.postgresDBStats()
	if *calls != 1 {
		t.Errorf("queries = %d, want 1", *calls)
	}
	if first.Stale || second.Stale || first.DbSizeMB != second.DbSizeMB {
		t.Errorf("first %+v second %+v", first, second)
	}
}

// While one caller refreshes, others get the last value flagged stale at
// once instead of waiting on the lock behind the query.
func TestPostgresDBStatsServesStaleDuringRefresh(t *testing.T) {
	db, _ := newPostgresTestDB(t)
	resetPGStatsCache(t)
	good := db.postgresDBStats()

	pgStatsCache.mu.Lock()
	pgStatsCache.at = time.Now().Add(-2 * pgStatsTTL)
	pgStatsCache.mu.Unlock()
	entered, release := make(chan struct{}), make(chan struct{})
	pgStatsQueryHook = func() { close(entered); <-release }
	done := make(chan SqliteStats)
	go func() { done <- db.postgresDBStats() }()
	<-entered

	start := time.Now()
	got := db.postgresDBStats()
	if time.Since(start) > time.Second {
		t.Errorf("concurrent caller waited %v for the refresh", time.Since(start))
	}
	if !got.Stale || got.DbSizeMB != good.DbSizeMB {
		t.Errorf("during refresh: %+v, want the last value (dbSizeMB %v) flagged stale", got, good.DbSizeMB)
	}
	close(release)
	if r := <-done; r.Stale {
		t.Errorf("refresher's own result is stale: %+v", r)
	}
}

// During an outage the last good value is served stale, and the failure is
// cached for pgStatsFailTTL so the dead database is not queried per request.
func TestPostgresDBStatsClosedDB(t *testing.T) {
	dsn := os.Getenv("CORESCOPE_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("CORESCOPE_TEST_PG_DSN not set; skipping Postgres test")
	}
	conn, err := sql.Open(dbx.PostgresDriverName, dsn)
	if err != nil {
		t.Fatal(err)
	}
	db := &DB{conn: conn, pg: true}
	calls := resetPGStatsCache(t)
	good := db.postgresDBStats()
	if good.Stale || good.DbSizeMB <= 0 {
		t.Fatalf("first read: %+v", good)
	}

	conn.Close()
	pgStatsCache.mu.Lock()
	pgStatsCache.at = time.Now().Add(-2 * pgStatsTTL)
	pgStatsCache.mu.Unlock()
	for i := 0; i < 3; i++ {
		got := db.postgresDBStats()
		if !got.Stale || got.DbSizeMB != good.DbSizeMB {
			t.Errorf("call %d after close: %+v, want the last value flagged stale", i, got)
		}
	}
	if *calls != 2 {
		t.Errorf("queries = %d, want 2 (one good, one failed; the failure cached)", *calls)
	}

	pgStatsCache.mu.Lock()
	pgStatsCache.failedAt = time.Now().Add(-2 * pgStatsFailTTL)
	pgStatsCache.mu.Unlock()
	db.postgresDBStats()
	if *calls != 3 {
		t.Errorf("queries = %d after the failure TTL, want 3", *calls)
	}
}

// With no good value yet, a failed read is an empty block flagged stale.
func TestPostgresDBStatsNoValueYet(t *testing.T) {
	conn, err := sql.Open(dbx.PostgresDriverName, "postgres://unused.invalid/db")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	db := &DB{conn: conn, pg: true}
	calls := resetPGStatsCache(t)
	got := db.postgresDBStats()
	if !got.Stale || got.Engine != "postgres" || got.Rows == nil || got.WalPages == nil {
		t.Errorf("got %+v", got)
	}
	db.postgresDBStats()
	if *calls != 1 {
		t.Errorf("queries = %d, want 1 (failure cached)", *calls)
	}
}

func TestPGServerDSNCollectsEveryPassword(t *testing.T) {
	_, secrets, err := pgServerDSN("postgres://u:inline@h/db?Password=q%20s%2Bcret&sslmode=disable", testPGParams)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"inline", "q s+cret", "q+s%2Bcret", "q%20s+cret"} {
		if !slices.Contains(secrets, want) {
			t.Errorf("secrets lack %q: %q", want, secrets)
		}
	}
	err = scrubPGSecret(errors.New("x q s+cret y q+s%2Bcret z q%20s+cret inline"), secrets...)
	for _, leak := range []string{"inline", "cret"} {
		if strings.Contains(err.Error(), leak) {
			t.Errorf("scrubbed error still has %q: %v", leak, err)
		}
	}
}

// pgDSNWithSearchPath returns dsn with path appended to its search_path.
func pgDSNWithSearchPath(t *testing.T, dsn, extra string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("unparseable DSN")
	}
	q := u.Query()
	q.Set("search_path", q.Get("search_path")+","+extra)
	u.RawQuery = q.Encode()
	return u.String()
}

// A schema on the search_path owned by a role the session can reach lets it
// plant objects that later run as the writer.
func TestOpenPostgresDBRefusesOwnedSearchPathSchema(t *testing.T) {
	seed := pgSchemaSeed(t)
	group := pgGroupRole(t, seed)
	extra := group + "_s"
	role, _, dsn := pgTestRole(t, seed, append(append([]string{}, pgReadOnlyGrants...),
		`GRANT `+group+` TO {role}`, `CREATE SCHEMA `+extra+` AUTHORIZATION `+group)...)
	t.Cleanup(func() { seed.Exec(`DROP SCHEMA IF EXISTS ` + extra + ` CASCADE`) })

	d, err := OpenPostgresDB(pgDSNWithSearchPath(t, dsn, extra))
	if err == nil {
		d.Close()
		t.Fatal("OpenPostgresDB accepted a role that owns a schema on its search_path")
	}
	for _, want := range []string{role, "owns schema " + extra} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
}

// The role parameter can make current_user a lesser role than session_user;
// the session user can SET ROLE back, so its own write power counts.
func TestOpenPostgresDBChecksSessionUser(t *testing.T) {
	seed := pgSchemaSeed(t)
	lesser := pgGroupRole(t, seed)
	role, _, dsn := pgTestRole(t, seed,
		`GRANT USAGE ON SCHEMA {schema} TO `+lesser,
		`GRANT SELECT ON ALL TABLES IN SCHEMA {schema} TO `+lesser,
		`GRANT `+lesser+` TO {role}`,
		`GRANT USAGE ON SCHEMA {schema} TO {role}`,
		`GRANT INSERT ON nodes TO {role}`,
		`ALTER ROLE {role} SET role = `+lesser)

	d, err := OpenPostgresDB(dsn)
	if err == nil {
		d.Close()
		t.Fatal("OpenPostgresDB accepted a session user with INSERT behind a read-only current role")
	}
	for _, want := range []string{"session user " + strconv.Quote(role), "nodes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
}

// A wrong password given as a password= query parameter never surfaces.
func TestOpenPostgresDBConnectErrorOmitsQueryPassword(t *testing.T) {
	seed := pgSchemaSeed(t)
	_, password, dsn := pgTestRole(t, seed, pgReadOnlyGrants...)
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.User(u.User.Username())
	q := u.Query()
	q.Set("password", password+"wrong")
	u.RawQuery = q.Encode()

	d, err := OpenPostgresDB(u.String())
	if err == nil {
		d.Close()
		t.Fatal("OpenPostgresDB succeeded with a wrong password")
	}
	if strings.Contains(err.Error(), password) {
		t.Errorf("error leaks the password: %v", err)
	}
}
