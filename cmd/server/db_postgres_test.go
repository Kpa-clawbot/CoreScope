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
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/dbschema/pgschema"
	"github.com/meshcore-analyzer/dbx/dbxtest"
)

func TestPGDSNWithReadOnlyURL(t *testing.T) {
	for _, in := range []string{
		"postgres://u:s3cret@h:5432/db?sslmode=disable",
		"postgresql://u:s3cret@h/db?default_transaction_read_only=off&sslmode=disable",
	} {
		out, pw, err := pgDSNWithReadOnly(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if pw != "s3cret" {
			t.Errorf("%q: password = %q", in, pw)
		}
		u, err := url.Parse(out)
		if err != nil {
			t.Fatalf("result does not parse: %v", err)
		}
		q := u.Query()
		if got := q[pgReadOnlyParam]; len(got) != 1 || got[0] != "on" {
			t.Errorf("%q: %s = %v, want exactly [on]", in, pgReadOnlyParam, got)
		}
		if q.Get("sslmode") != "disable" || u.Host == "" || u.Path != "/db" {
			t.Errorf("%q: other parts not preserved: %q", in, out)
		}
	}
}

func TestPGDSNWithReadOnlyKeywordValue(t *testing.T) {
	in := `host=h  user = u password='p w\'x\\y' dbname=db default_transaction_read_only=off sslmode=disable`
	out, pw, err := pgDSNWithReadOnly(in)
	if err != nil {
		t.Fatal(err)
	}
	if pw != `p w'x\y` {
		t.Errorf("password = %q", pw)
	}
	pairs, err := parsePGKeywordValue(out)
	if err != nil {
		t.Fatalf("result does not re-parse: %v (%s)", err, out)
	}
	got := map[string][]string{}
	for _, kv := range pairs {
		got[kv[0]] = append(got[kv[0]], kv[1])
	}
	want := map[string]string{"host": "h", "user": "u", "password": `p w'x\y`, "dbname": "db", "sslmode": "disable", pgReadOnlyParam: "on"}
	for k, v := range want {
		if len(got[k]) != 1 || got[k][0] != v {
			t.Errorf("%s = %q, want [%q]", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("keys = %v, want %v", got, want)
	}
}

func TestPGDSNWithReadOnlyErrorsOmitInput(t *testing.T) {
	for _, in := range []string{
		"host=h password='unterminated-s3cret",
		"host=h s3cret-without-equals",
		"postgres://u:s3cret@h:bad port/db",
	} {
		_, _, err := pgDSNWithReadOnly(in)
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

// The Postgres stats are served from cache within pgStatsTTL.
func TestPostgresDBStatsCached(t *testing.T) {
	db, _ := newPostgresTestDB(t)
	first := db.postgresDBStats()
	pgStatsCache.mu.Lock()
	pgStatsCache.stats.DbSizeMB = -1 // marker: only a cache hit returns it
	pgStatsCache.mu.Unlock()
	if got := db.postgresDBStats(); got.DbSizeMB != -1 {
		t.Errorf("second call recomputed (dbSizeMB %v, first %v); want the cached value", got.DbSizeMB, first.DbSizeMB)
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
