package users

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/meshcore-analyzer/dbx/dbxtest"
)

func TestPostgresMigrationsMatchSQLite(t *testing.T) {
	if got := pgBaselineVersion + len(pgMigrations); got != len(migrations) {
		t.Fatalf("Postgres schema reaches version %d, SQLite %d: add the new migration to pgMigrations too", got, len(migrations))
	}
}

func TestValidSchemaName(t *testing.T) {
	for _, ok := range []string{"corescope_users", "_x", "a1"} {
		if !validSchemaName(ok) {
			t.Errorf("validSchemaName(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "1a", "A", `x"y`, "a-b", "a.b", strings.Repeat("a", 64)} {
		if validSchemaName(bad) {
			t.Errorf("validSchemaName(%q) = true", bad)
		}
	}
}

func pgTestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(testPGDSNEnv)
	if dsn == "" {
		t.Skipf("%s not set; skipping Postgres test", testPGDSNEnv)
	}
	return dsn
}

func pgTestSuffix(t *testing.T) string {
	t.Helper()
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}

// pgTestRole creates a role with the given attributes (CREATE ROLE's
// defaults otherwise: no SUPERUSER, CREATEROLE, CREATEDB or BYPASSRLS),
// dropped in Cleanup with whatever it owns in admin's database, after the
// cleanups registered later (stores, databases). login gives it a password,
// returned. Skips when the test role may not create roles.
func pgTestRole(t *testing.T, admin *sql.DB, login bool, attrs string) (role, password string) {
	t.Helper()
	role = "users_test_" + pgTestSuffix(t)
	opts := "NOLOGIN"
	if login {
		password = "pw" + pgTestSuffix(t)
		opts = "LOGIN PASSWORD '" + password + "'"
	}
	if _, err := admin.Exec(`CREATE ROLE ` + role + ` ` + opts + ` ` + attrs); err != nil {
		t.Skipf("cannot create a role here: %v", err)
	}
	t.Cleanup(func() {
		for _, q := range []string{`DROP OWNED BY ` + role, `DROP ROLE ` + role} {
			if _, err := admin.Exec(q); err != nil {
				t.Errorf("%s: %v", q, err)
			}
		}
	})
	return role, password
}

// pgTestRoleDSN is $CORESCOPE_TEST_PG_DSN as role (with password), on
// database db ("" keeps the DSN's), without a search_path.
func pgTestRoleDSN(t *testing.T, role, password, db string) string {
	t.Helper()
	u, err := url.Parse(pgTestDSN(t))
	if err != nil || u.Scheme == "" {
		t.Skip("CORESCOPE_TEST_PG_DSN is not a URL; skipping")
	}
	u.User = url.UserPassword(role, password)
	if db != "" {
		u.Path = "/" + db
	}
	q := u.Query()
	q.Del("search_path")
	u.RawQuery = q.Encode()
	return u.String()
}

// pgTestDatabase creates a database (owned by owner unless ""), dropped in
// Cleanup before the roles created earlier. Skips when the test role may not
// create databases.
func pgTestDatabase(t *testing.T, admin *sql.DB, owner string) string {
	t.Helper()
	name := "users_test_" + pgTestSuffix(t)
	stmt := `CREATE DATABASE ` + name
	if owner != "" {
		stmt += ` OWNER ` + owner
	}
	if _, err := admin.Exec(stmt); err != nil {
		t.Skipf("cannot create a database here: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP DATABASE IF EXISTS ` + name + ` WITH (FORCE)`); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
	})
	return name
}

// newPostgresTestTarget returns a DSN for a fresh login role holding nothing
// beyond PUBLIC's privileges, as scripts/postgres-roles.sh sets up the
// accounts role, and an empty schema it owns (dropped when the test ends,
// after the stores opened later). OpenPostgres refuses the superuser that
// $CORESCOPE_TEST_PG_DSN usually is. Skips without the variable.
func newPostgresTestTarget(t *testing.T) (dsn, schema string) {
	t.Helper()
	admin := dbxtest.OpenPostgres(t) // skips without $CORESCOPE_TEST_PG_DSN
	if err := admin.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	role, password := pgTestRole(t, admin, true, "")
	if _, err := admin.Exec(`ALTER SCHEMA "` + schema + `" OWNER TO ` + role); err != nil {
		t.Fatal(err)
	}
	return pgTestRoleDSN(t, role, password, ""), schema
}

// twoPostgresStores opens two stores (two pools, as two replicas would) on
// one fresh schema, sharing one clock.
func twoPostgresStores(t *testing.T) (*Store, *Store, *fakeClock) {
	t.Helper()
	dsn, schema := newPostgresTestTarget(t)
	a, err := openPostgres(dsn, schema)
	if err != nil {
		t.Fatal(err)
	}
	a, clk := withTestClock(t, a)
	b, err := openPostgres(dsn, schema)
	if err != nil {
		t.Fatal(err)
	}
	b.SetClock(clk.Now)
	t.Cleanup(func() { b.Close() })
	return a, b, clk
}

// race runs fn(0..n-1) in n goroutines released together and returns the
// errors by index.
func race(n int, fn func(i int) error) []error {
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = fn(i)
		}(i)
	}
	close(start)
	wg.Wait()
	return errs
}

// raceStores alternates the two stores across the goroutines.
func pick(i int, a, b *Store) *Store {
	if i%2 == 0 {
		return a
	}
	return b
}

// countOutcomes counts nil errors and errors matching want; any other error fails the test.
func countOutcomes(t *testing.T, errs []error, want error) (ok, lost int) {
	t.Helper()
	for i, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, want):
			lost++
		default:
			t.Fatalf("racer %d: unexpected error %v", i, err)
		}
	}
	return ok, lost
}

const racers = 8

func TestOpenPostgresFreshSchemaThenNoOp(t *testing.T) {
	dsn, schema := newPostgresTestTarget(t)
	st, err := openPostgres(dsn, schema)
	if err != nil {
		t.Fatal(err)
	}
	if !st.IsPostgres() {
		t.Fatal("IsPostgres = false")
	}
	if v, err := st.SchemaVersion(); err != nil || v != len(migrations) {
		t.Fatalf("SchemaVersion = %d, %v; want %d", v, err, len(migrations))
	}
	u := mustCreate(t, st, "keep@example.org", "Keep")
	st.Close()

	st2, err := openPostgres(dsn, schema)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer st2.Close()
	if v, _ := st2.SchemaVersion(); v != len(migrations) {
		t.Fatalf("version after reopen = %d", v)
	}
	var rows int
	if err := st2.db.QueryRow(`SELECT COUNT(*) FROM schema_version`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("schema_version rows = %d, %v; want 1", rows, err)
	}
	if got, err := st2.GetByID(u.ID); err != nil || got.Email != "keep@example.org" {
		t.Fatalf("user after reopen = %+v, %v", got, err)
	}
}

func TestOpenPostgresConcurrentOpensBothSucceed(t *testing.T) {
	dsn, schema := newPostgresTestTarget(t)
	stores := make([]*Store, 2)
	errs := race(2, func(i int) error {
		st, err := openPostgres(dsn, schema)
		stores[i] = st
		return err
	})
	for i, err := range errs {
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		defer stores[i].Close()
		if v, err := stores[i].SchemaVersion(); err != nil || v != len(migrations) {
			t.Fatalf("store %d SchemaVersion = %d, %v", i, v, err)
		}
	}
	var rows int
	if err := stores[0].db.QueryRow(`SELECT COUNT(*) FROM schema_version`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("schema_version rows = %d, %v; want 1", rows, err)
	}
}

// OpenPostgres itself uses corescope_users. The test leaves an existing
// schema of that name alone (skips) and drops the one it created.
func TestOpenPostgresUsesCorescopeUsersSchema(t *testing.T) {
	admin := dbxtest.OpenPostgres(t)
	var exists bool
	if err := admin.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = ?)`, PostgresSchema).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Skipf("schema %s already exists in the test database; not touching it", PostgresSchema)
	}
	role, password := pgTestRole(t, admin, true, "")
	// As scripts/postgres-roles.sh does: the store no longer creates it.
	if _, err := admin.Exec(`CREATE SCHEMA "` + PostgresSchema + `" AUTHORIZATION ` + role); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP SCHEMA IF EXISTS "` + PostgresSchema + `" CASCADE`); err != nil {
			t.Errorf("drop %s: %v", PostgresSchema, err)
		}
	})
	st, err := OpenPostgres(pgTestRoleDSN(t, role, password, ""))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var cur string
	if err := st.db.QueryRow(`SELECT current_schema()`).Scan(&cur); err != nil || cur != PostgresSchema {
		t.Fatalf("current_schema = %q, %v", cur, err)
	}
	if v, err := st.SchemaVersion(); err != nil || v != len(migrations) {
		t.Fatalf("SchemaVersion = %d, %v", v, err)
	}
}

func TestOpenPostgresKeywordValueDSN(t *testing.T) {
	dsn, schema := newPostgresTestTarget(t)
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := func(v string) string { return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(v) + "'" }
	kv := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s search_path=public",
		q(cfg.Host), cfg.Port, q(cfg.User), q(cfg.Password), q(cfg.Database))
	if cfg.TLSConfig == nil {
		kv += " sslmode=disable"
	}
	st, err := openPostgres(kv, schema)
	if err != nil {
		t.Fatalf("keyword=value DSN: %v", err)
	}
	defer st.Close()
	var cur string
	if err := st.db.QueryRow(`SELECT current_schema()`).Scan(&cur); err != nil || cur != schema {
		t.Fatalf("current_schema = %q, %v; want %q (the DSN's search_path is replaced)", cur, err, schema)
	}
}

func TestOpenPostgresErrorsHideDSN(t *testing.T) {
	const secret = "s3cretPW"
	for _, dsn := range []string{
		"postgres://u:" + secret + "@localhost:notaport/db",
		"host=localhost port=notaport user=u password=" + secret,
		"postgres://u:" + secret + "@localhost/db?options=-c%20search_path%3Dother",
	} {
		_, err := openPostgres(dsn, "x")
		if err == nil {
			t.Fatalf("openPostgres accepted a bad DSN")
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "notaport") {
			t.Errorf("error quotes the DSN: %v", err)
		}
	}
}

func TestOpenPostgresRejectsNewerSchema(t *testing.T) {
	dsn, schema := newPostgresTestTarget(t)
	st, err := openPostgres(dsn, schema)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`UPDATE schema_version SET version = 999`); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if _, err := openPostgres(dsn, schema); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("open with newer schema err = %v", err)
	}
}

func TestOpenPostgresRefusesSchemaOfAnotherRole(t *testing.T) {
	dsn, _ := newPostgresTestTarget(t)
	admin := dbxtest.OpenPostgres(t)
	var b [6]byte
	rand.Read(b[:])
	suffix := hex.EncodeToString(b[:])
	role, schema := "users_test_role_"+suffix, "users_test_foreign_"+suffix
	if _, err := admin.Exec(`CREATE ROLE "` + role + `" NOLOGIN`); err != nil {
		t.Skipf("cannot create a role here: %v", err)
	}
	t.Cleanup(func() { admin.Exec(`DROP ROLE IF EXISTS "` + role + `"`) })
	if _, err := admin.Exec(`CREATE SCHEMA "` + schema + `" AUTHORIZATION "` + role + `"`); err != nil {
		t.Skipf("cannot create a schema for another role here: %v", err)
	}
	t.Cleanup(func() { admin.Exec(`DROP SCHEMA IF EXISTS "` + schema + `" CASCADE`) })
	if _, err := openPostgres(dsn, schema); err == nil || !strings.Contains(err.Error(), "owned by role") {
		t.Fatalf("open of a schema owned by another role err = %v", err)
	}
}

func TestSnapshotUnsupportedOnPostgres(t *testing.T) {
	st, _ := newPostgresTestStore(t)
	path := filepath.Join(t.TempDir(), "copy.db")
	if err := st.Snapshot(path); !errors.Is(err, ErrSnapshotUnsupported) {
		t.Fatalf("Snapshot = %v; want ErrSnapshotUnsupported", err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("Snapshot left a file: %v", err)
	}
}

// The Postgres baseline has SQLite v8's tables, columns (in order, with the
// same NOT NULL), defaults and indexes; mail_events.rowid is the one
// addition (SQLite's implicit rowid).
func TestPostgresSchemaMatchesSQLite(t *testing.T) {
	lite, _ := newSQLiteTestStore(t)
	pg, _ := newPostgresTestStore(t)

	type col struct {
		name    string
		notNull bool
		dflt    string
	}
	liteCols := map[string][]col{}
	// A primary-key column counts as NOT NULL: Postgres makes it so, and
	// the store never writes NULL into one on SQLite either.
	rows, err := lite.db.Query(`SELECT m.name, p.name, p."notnull" OR p.pk > 0, COALESCE(p.dflt_value, '')
		FROM sqlite_master m, pragma_table_info(m.name) p WHERE m.type = 'table' ORDER BY m.name, p.cid`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var table string
		var c col
		if err := rows.Scan(&table, &c.name, &c.notNull, &c.dflt); err != nil {
			t.Fatal(err)
		}
		liteCols[table] = append(liteCols[table], c)
	}
	rows.Close()
	pgCols := map[string][]col{}
	rows, err = pg.db.Query(`SELECT table_name, column_name, is_nullable = 'NO', COALESCE(column_default, '')
		FROM information_schema.columns WHERE table_schema = current_schema() ORDER BY table_name, ordinal_position`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var table string
		var c col
		if err := rows.Scan(&table, &c.name, &c.notNull, &c.dflt); err != nil {
			t.Fatal(err)
		}
		if table == "mail_events" && c.name == "rowid" {
			continue
		}
		// Postgres prints text defaults with a cast: 'user'::text.
		c.dflt = strings.TrimSuffix(c.dflt, "::text")
		pgCols[table] = append(pgCols[table], c)
	}
	rows.Close()
	if fmt.Sprint(liteCols) != fmt.Sprint(pgCols) {
		t.Fatalf("columns differ:\nsqlite   %v\npostgres %v", liteCols, pgCols)
	}

	names := func(st *Store, q string) []string {
		rows, err := st.db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var n string
			if err := rows.Scan(&n); err != nil {
				t.Fatal(err)
			}
			out = append(out, n)
		}
		sort.Strings(out)
		return out
	}
	liteIdx := names(lite, `SELECT name FROM sqlite_master WHERE type = 'index' AND sql IS NOT NULL`)
	// Postgres names its implicit primary-key and UNIQUE indexes; SQLite's
	// are autoindexes without sql. Compare the explicitly created ones.
	pgIdx := names(pg, `SELECT indexname FROM pg_indexes WHERE schemaname = current_schema()
		AND indexname NOT LIKE '%\_pkey' AND indexname NOT LIKE '%\_key'`)
	if fmt.Sprint(liteIdx) != fmt.Sprint(pgIdx) {
		t.Fatalf("indexes differ:\nsqlite   %v\npostgres %v", liteIdx, pgIdx)
	}
}

func TestPostgresRaceConsumeToken(t *testing.T) {
	a, b, _ := twoPostgresStores(t)
	u := mustCreate(t, a, "tok@example.org", "Tok")
	raw, err := a.IssueToken(u.ID, PurposeReset, time.Hour, "")
	if err != nil {
		t.Fatal(err)
	}
	errs := race(racers, func(i int) error {
		_, _, err := pick(i, a, b).ConsumeToken(raw, PurposeReset)
		return err
	})
	if ok, lost := countOutcomes(t, errs, ErrTokenInvalid); ok != 1 || lost != racers-1 {
		t.Fatalf("ConsumeToken winners = %d, losers = %d; want 1, %d", ok, lost, racers-1)
	}

	act, err := a.IssueToken(u.ID, PurposeActivate, time.Hour, "")
	if err != nil {
		t.Fatal(err)
	}
	errs = race(racers, func(i int) error {
		return pick(i, a, b).ActivateWithToken(act, u.ID, RoleUser, u.PasswordHash)
	})
	if ok, lost := countOutcomes(t, errs, ErrTokenInvalid); ok != 1 || lost != racers-1 {
		t.Fatalf("ActivateWithToken winners = %d, losers = %d; want 1, %d", ok, lost, racers-1)
	}
}

func TestPostgresRaceIssueTokenLeavesOneLive(t *testing.T) {
	a, b, _ := twoPostgresStores(t)
	u := mustCreate(t, a, "issue@example.org", "Issue")
	errs := race(racers, func(i int) error {
		_, err := pick(i, a, b).IssueToken(u.ID, PurposeReset, time.Hour, "")
		return err
	})
	if ok, _ := countOutcomes(t, errs, nil); ok != racers {
		t.Fatalf("IssueToken successes = %d", ok)
	}
	var live int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM tokens WHERE user_id = ? AND purpose = 'reset' AND used_at IS NULL`, u.ID).Scan(&live); err != nil || live != 1 {
		t.Fatalf("live reset tokens = %d, %v; want 1", live, err)
	}
}

func TestPostgresRaceNotificationPrefs(t *testing.T) {
	a, b, _ := twoPostgresStores(t)
	u := mustCreate(t, a, "notify@example.org", "Notify")
	tokens := make([]string, racers)
	errs := race(racers, func(i int) error {
		p, err := pick(i, a, b).NotifyPrefsFor(u.ID)
		tokens[i] = p.UnsubToken
		return err
	})
	countOutcomes(t, errs, nil)
	for i, tok := range tokens {
		if tok == "" || tok != tokens[0] {
			t.Fatalf("racer %d got unsubscribe token %q, racer 0 %q; want one shared row", i, tok, tokens[0])
		}
	}

	was := make([]bool, racers)
	errs = race(racers, func(i int) error {
		uid, w, err := pick(i, a, b).DisableNotifyByToken(tokens[0])
		if err == nil && uid != u.ID {
			return fmt.Errorf("user %d", uid)
		}
		was[i] = w
		return err
	})
	countOutcomes(t, errs, nil)
	n := 0
	for _, w := range was {
		if w {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d racers saw notifications enabled; want exactly 1", n)
	}
}

func TestPostgresRaceUniqueInserts(t *testing.T) {
	a, b, _ := twoPostgresStores(t)
	errs := race(racers, func(i int) error {
		_, err := pick(i, a, b).CreatePending("same@example.org", "Same", "h")
		return err
	})
	if ok, lost := countOutcomes(t, errs, ErrEmailTaken); ok != 1 || lost != racers-1 {
		t.Fatalf("CreatePending winners = %d, losers = %d", ok, lost)
	}
	u, err := a.GetByEmail("same@example.org")
	if err != nil {
		t.Fatal(err)
	}
	errs = race(racers, func(i int) error {
		return pick(i, a, b).CreateMQTTLogin(mkLogin(u.ID, fmt.Sprintf("login%d", i)))
	})
	if ok, lost := countOutcomes(t, errs, ErrMQTTLoginExists); ok != 1 || lost != racers-1 {
		t.Fatalf("CreateMQTTLogin winners = %d, losers = %d", ok, lost)
	}
	errs = race(racers, func(i int) error {
		_, err := pick(i, a, b).Propose(KindHashtagChannel, "#same", u.ID, ProposalLimits{})
		return err
	})
	if ok, lost := countOutcomes(t, errs, ErrProposalPending); ok != 1 || lost != racers-1 {
		t.Fatalf("Propose winners = %d, losers = %d", ok, lost)
	}
}

func TestPostgresRaceLimits(t *testing.T) {
	a, b, _ := twoPostgresStores(t)
	u := mustCreate(t, a, "limits@example.org", "Limits")
	errs := race(racers, func(i int) error {
		return pick(i, a, b).AddWatch(u.ID, fmt.Sprintf("%064x", i), 3)
	})
	if ok, lost := countOutcomes(t, errs, ErrWatchLimit); ok != 3 || lost != racers-3 {
		t.Fatalf("AddWatch winners = %d, losers = %d; want 3, %d", ok, lost, racers-3)
	}
	if w, err := a.WatchesFor(u.ID); err != nil || len(w) != 3 {
		t.Fatalf("watches = %d, %v; want 3", len(w), err)
	}

	errs = race(racers, func(i int) error {
		_, err := pick(i, a, b).Propose(KindHashtagChannel, fmt.Sprintf("#p%d", i), u.ID, ProposalLimits{MaxPending: 2})
		return err
	})
	if ok, lost := countOutcomes(t, errs, ErrProposalPendingLimit); ok != 2 || lost != racers-2 {
		t.Fatalf("Propose under MaxPending winners = %d, losers = %d; want 2", ok, lost)
	}
	pending, err := a.ListProposals(ProposalFilter{Status: ProposalPending})
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending = %d, %v", len(pending), err)
	}
	errs = race(len(pending), func(i int) error {
		_, err := pick(i, a, b).Decide(pending[i].ID, ProposalApprove, u.ID, "", 1)
		return err
	})
	if ok, lost := countOutcomes(t, errs, ErrProposalApprovedLimit); ok != 1 || lost != 1 {
		t.Fatalf("Decide under maxApproved winners = %d, losers = %d; want 1, 1", ok, lost)
	}
	// One of the two is still pending: of concurrent rejections exactly
	// one changes it, the rest find it rejected.
	still := pending[0]
	if p, err := a.ListProposals(ProposalFilter{Status: ProposalPending}); err != nil || len(p) != 1 {
		t.Fatalf("pending after approvals = %d, %v; want 1", len(p), err)
	} else {
		still = p[0]
	}
	errs = race(racers, func(i int) error {
		_, err := pick(i, a, b).Decide(still.ID, ProposalReject, u.ID, "", 0)
		return err
	})
	if ok, lost := countOutcomes(t, errs, ErrProposalTransition); ok != 1 || lost != racers-1 {
		t.Fatalf("Decide reject winners = %d, losers = %d; want 1, %d", ok, lost, racers-1)
	}
}

func TestPostgresRaceSettings(t *testing.T) {
	a, b, _ := twoPostgresStores(t)
	u := mustCreate(t, a, "settings@example.org", "Settings")
	errs := race(racers, func(i int) error {
		_, err := pick(i, a, b).PutSettings(u.ID, SettingsVersion{}, fmt.Sprintf(`{"v":%d}`, i))
		return err
	})
	if ok, lost := countOutcomes(t, errs, ErrSettingsConflict); ok != 1 || lost != racers-1 {
		t.Fatalf("first PutSettings winners = %d, losers = %d", ok, lost)
	}
	_, base, err := a.GetSettings(u.ID)
	if err != nil {
		t.Fatal(err)
	}
	errs = race(racers, func(i int) error {
		_, err := pick(i, a, b).PutSettings(u.ID, base, fmt.Sprintf(`{"w":%d}`, i))
		return err
	})
	if ok, lost := countOutcomes(t, errs, ErrSettingsConflict); ok != 1 || lost != racers-1 {
		t.Fatalf("PutSettings on one base winners = %d, losers = %d", ok, lost)
	}
	if _, v, _ := a.GetSettings(u.ID); v.Revision != 2 || v.Generation != base.Generation {
		t.Fatalf("version = %+v; want revision 2 of %s", v, base.Generation)
	}
}

func TestPostgresRaceCompanionLink(t *testing.T) {
	a, b, _ := twoPostgresStores(t)
	pk := strings.Repeat("ab", 32)
	users := make([]*User, racers)
	for i := range users {
		users[i] = mustCreate(t, a, fmt.Sprintf("c%d@example.org", i), "C")
	}
	prevs := make([]int64, racers)
	errs := race(racers, func(i int) error {
		_, prev, err := pick(i, a, b).UpsertCompanionLink(users[i].ID, pk, "dev")
		prevs[i] = prev
		return err
	})
	countOutcomes(t, errs, nil)
	// The links form one chain: the first saw no owner, each later one the
	// owner the previous left, so the previous owners are all distinct.
	firsts, seen := 0, map[int64]bool{}
	for _, p := range prevs {
		if p == 0 {
			firsts++
			continue
		}
		if seen[p] {
			t.Fatalf("two links both replaced user %d: prevs %v", p, prevs)
		}
		seen[p] = true
	}
	if firsts != 1 {
		t.Fatalf("%d links saw no previous owner; want 1 (prevs %v)", firsts, prevs)
	}
}

func TestPostgresRaceMailEventKeepsNewest(t *testing.T) {
	a, b, clk := twoPostgresStores(t)
	if _, err := a.LogMail(nil, "x@example.org", "reset", "msg-1"); err != nil {
		t.Fatal(err)
	}
	base := clk.Now()
	errs := race(racers, func(i int) error {
		_, found, err := pick(i, a, b).RecordMailEvent("msg-1", fmt.Sprintf("e%d", i), base.Add(time.Duration(i)*time.Minute), "")
		if err == nil && !found {
			return errors.New("not found")
		}
		return err
	})
	countOutcomes(t, errs, nil)
	var last string
	var at int64
	if err := a.db.QueryRow(`SELECT last_event, last_event_at FROM mail_log WHERE provider_message_id = 'msg-1'`).Scan(&last, &at); err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("e%d", racers-1); last != want || at != unix(base.Add(time.Duration(racers-1)*time.Minute)) {
		t.Fatalf("summary = %s at %d; want the newest event %s", last, at, want)
	}
}

// The Postgres counterpart of TestUpdatePendingRegistrationCannotOverwriteConcurrentActivation:
// an activation holds the user row uncommitted on another connection; the
// registration update waits for it and then sees the committed status.
func TestPostgresUpdatePendingRegistrationWaitsForActivation(t *testing.T) {
	a, b, _ := twoPostgresStores(t)
	u := mustCreate(t, a, "activation@example.org", "Original")
	tx, err := b.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE users SET status = 'active', role = 'admin' WHERE id = ?`, u.ID); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- a.UpdatePendingRegistration(u.ID, "replacement-hash", "Replacement") }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting int
		if err := b.db.QueryRow(`SELECT COUNT(*) FROM pg_stat_activity WHERE datname = current_database()
			AND wait_event_type = 'Lock' AND query LIKE 'UPDATE users SET password_hash%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("registration returned before activation committed: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("registration never waited on the activation's row lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, ErrAccountChanged) {
			t.Fatalf("err = %v; want ErrAccountChanged", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("registration did not finish")
	}
	if got, _ := a.GetByID(u.ID); got.PasswordHash != u.PasswordHash || got.Status != StatusActive {
		t.Fatalf("user = %+v; the stale registration overwrote it", got)
	}
}

// The store never creates its schema: a missing one is an error that says
// how to create it, and nothing is created.
func TestOpenPostgresMissingSchemaIsAnError(t *testing.T) {
	dsn, own := newPostgresTestTarget(t)
	missing := own + "_missing"
	_, err := openPostgres(dsn, missing)
	if err == nil || !strings.Contains(err.Error(), "does not exist") || !strings.Contains(err.Error(), "scripts/postgres-roles.sh") ||
		!strings.Contains(err.Error(), "CORESCOPE_ACCOUNTS_PASSWORD") || !errors.Is(err, ErrPostgresSchemaMissing) {
		t.Fatalf("open of a missing schema err = %v", err)
	}
	admin := dbxtest.OpenPostgres(t)
	var made bool
	if err := admin.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = ?)`, missing).Scan(&made); err != nil || made {
		t.Fatalf("schema %s created anyway: %v, %v", missing, made, err)
	}
}

// A role that could create a schema or a role is refused before the schema
// is looked at, whatever reaches it: its own attributes, a membership chain
// (NOINHERIT does not matter: SET ROLE still works), owning the database or
// CREATE on it. The error names the finding and never the DSN.
func TestOpenPostgresRefusesPowerfulRoles(t *testing.T) {
	admin := dbxtest.OpenPostgres(t)
	refused := func(t *testing.T, role, password, db, want string) {
		t.Helper()
		_, err := openPostgres(pgTestRoleDSN(t, role, password, db), "users_test_unused")
		if err == nil {
			t.Fatalf("role %s accepted, want refusal for %q", role, want)
		}
		if !errors.Is(err, ErrPostgresRoleRefused) {
			t.Errorf("role %s: err = %v, not ErrPostgresRoleRefused", role, err)
		}
		msg := err.Error()
		if !strings.Contains(msg, want) || !strings.Contains(msg, "postgres-roles.sh") {
			t.Fatalf("role %s: err = %v; want it to name %q", role, err, want)
		}
		if strings.Contains(msg, password) || strings.Contains(msg, "does not exist") {
			t.Fatalf("role %s: err = %v (leaks the password or got past the role check)", role, err)
		}
	}
	for _, attr := range []string{"SUPERUSER", "CREATEROLE", "BYPASSRLS"} {
		t.Run(attr, func(t *testing.T) {
			role, password := pgTestRole(t, admin, true, attr)
			refused(t, role, password, "", "role "+role+" has "+attr)
		})
	}
	t.Run("membership", func(t *testing.T) {
		top, _ := pgTestRole(t, admin, false, "CREATEROLE")
		mid, _ := pgTestRole(t, admin, false, "")
		role, password := pgTestRole(t, admin, true, "NOINHERIT")
		for _, g := range []string{`GRANT ` + top + ` TO ` + mid, `GRANT ` + mid + ` TO ` + role} {
			if _, err := admin.Exec(g); err != nil {
				t.Fatalf("%s: %v", g, err)
			}
		}
		refused(t, role, password, "", "role "+top+" has CREATEROLE")
	})
	t.Run("owns database", func(t *testing.T) {
		role, password := pgTestRole(t, admin, true, "")
		db := pgTestDatabase(t, admin, role)
		refused(t, role, password, db, "role "+role+" owns database "+db)
	})
	t.Run("CREATE on database", func(t *testing.T) {
		group, _ := pgTestRole(t, admin, false, "")
		role, password := pgTestRole(t, admin, true, "NOINHERIT")
		if _, err := admin.Exec(`GRANT ` + group + ` TO ` + role); err != nil {
			t.Fatal(err)
		}
		// A database of its own: GRANTs on the shared test database's row
		// would race other test processes ("tuple concurrently updated").
		db := pgTestDatabase(t, admin, "")
		if _, err := admin.Exec(`GRANT CREATE ON DATABASE ` + db + ` TO ` + group); err != nil {
			t.Fatal(err)
		}
		refused(t, role, password, db, "role "+group+" can CREATE in database "+db)
	})
	t.Run("PUBLIC CREATE on database", func(t *testing.T) {
		role, password := pgTestRole(t, admin, true, "")
		db := pgTestDatabase(t, admin, "")
		if _, err := admin.Exec(`GRANT CREATE ON DATABASE ` + db + ` TO PUBLIC`); err != nil {
			t.Fatal(err)
		}
		refused(t, role, password, db, "role "+role+" can CREATE in database "+db)
	})
}

// scripts/postgres-roles.sh grants the users reader group the same columns
// as pgGrantReader, so whichever ran last the ingestor reads the same.
func TestReaderColumnsMatchRolesScript(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "scripts", "postgres-roles.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	const grant = `GRANT SELECT (%s) ON TABLE corescope_users.%I TO %I`
	i := strings.Index(text, grant)
	if i < 0 || strings.Count(text, grant) != 1 {
		t.Fatalf("roles script: want exactly one %q", grant)
	}
	block := text[i:]
	block = block[:strings.Index(block, `\gexec`)]
	got := map[string]string{}
	for _, m := range regexp.MustCompile(`\('([a-z_]+)', '([a-z_, ]+)'\)`).FindAllStringSubmatch(block, -1) {
		got[m[1]] = m[2]
	}
	want := map[string]string{}
	for _, rc := range pgReaderColumns {
		want[rc.table] = rc.columns
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("roles script grants %v, pgReaderColumns %v", got, want)
	}
}
