package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/dbx"
	"github.com/meshcore-analyzer/dbx/dbxtest"
)

// usersPGSchemaForTest points the readers at a schema of this test's own (the
// throwaway schema dbxtest made, plus "_users") instead of the real
// corescope_users: that name is fixed and shared by every test on the
// database, so tables created there would collide across parallel tests. The
// store's own search_path is the test schema, so the qualified names are what
// is under test. It returns the connection and the schema name.
func usersPGSchemaForTest(t *testing.T, withTables bool) (*sql.DB, string) {
	t.Helper()
	db := dbxtest.OpenPostgres(t)
	var main string
	if err := db.QueryRow(`SELECT current_schema()`).Scan(&main); err != nil {
		t.Fatal(err)
	}
	schema := main + "_users"
	old := usersPGSchema
	usersPGSchema = schema
	t.Cleanup(func() { usersPGSchema = old })
	if withTables {
		q := `"` + schema + `"`
		for _, stmt := range []string{
			`CREATE SCHEMA ` + q,
			`CREATE TABLE ` + q + `.proposals (id BIGSERIAL PRIMARY KEY, kind TEXT NOT NULL, subject TEXT NOT NULL, status TEXT NOT NULL, decided_at BIGINT)`,
			`CREATE TABLE ` + q + `.companion_links (pubkey TEXT PRIMARY KEY, user_id BIGINT NOT NULL, linked_at BIGINT NOT NULL)`,
		} {
			if _, err := db.Exec(stmt); err != nil {
				t.Fatalf("%s: %v", stmt, err)
			}
		}
		// Registered after dbxtest's cleanup, so it runs before the db closes.
		t.Cleanup(func() { db.Exec(`DROP SCHEMA ` + q + ` CASCADE`) })
	}
	return db, schema
}

// testAccountsReader is the reader the sets use, on the test database. Its
// session searches pg_catalog only, so the throwaway schema's search_path
// plays no part: the qualified names are what is under test.
func testAccountsReader(t testing.TB) *accountsReader {
	t.Helper()
	dsn := os.Getenv("CORESCOPE_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("CORESCOPE_TEST_PG_DSN not set; skipping Postgres test")
	}
	r := newAccountsReader(dsn)
	t.Cleanup(r.Close)
	return r
}

func pgSetProposals(t *testing.T, db *sql.DB, schema string, rows ...testProposal) {
	t.Helper()
	q := `"` + schema + `".proposals`
	if _, err := db.Exec(`DELETE FROM ` + q); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO `+q+` (kind, subject, status, decided_at) VALUES ('hashtag_channel', ?, ?, ?)`, r.subject, r.status, r.decidedAt); err != nil {
			t.Fatal(err)
		}
	}
}

func pgSetLinks(t *testing.T, db *sql.DB, schema string, pks ...string) {
	t.Helper()
	q := `"` + schema + `".companion_links`
	if _, err := db.Exec(`DELETE FROM ` + q); err != nil {
		t.Fatal(err)
	}
	for i, pk := range pks {
		if _, err := db.Exec(`INSERT INTO `+q+` (pubkey, user_id, linked_at) VALUES (?, 1, ?)`, pk, i); err != nil {
			t.Fatal(err)
		}
	}
}

func captureLog(t *testing.T) *bytes.Buffer {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

// The schema name is duplicated here because the ingestor never imports
// internal/users; this keeps the copy honest.
func TestUsersPGSchemaMatchesUsersPackage(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "internal", "users", "postgres.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `PostgresSchema = "corescope_users"`) || usersPGSchema != "corescope_users" {
		t.Fatalf("internal/users.PostgresSchema and usersPGSchema differ (%q)", usersPGSchema)
	}
}

func TestPGUsersQueryIsSchemaQualified(t *testing.T) {
	got := pgUsersQuery(approvedChannelsQuery, "proposals")
	if !strings.Contains(got, `FROM "corescope_users".proposals WHERE`) {
		t.Fatalf("query = %s", got)
	}
	if got := pgUsersQuery(linkedCompanionsQuery, "companion_links"); got != `SELECT pubkey FROM "corescope_users".companion_links` {
		t.Fatalf("query = %s", got)
	}
}

func TestChannelKeySetPostgresReadsAndRefreshes(t *testing.T) {
	db, schema := usersPGSchemaForTest(t, true)
	pgSetProposals(t, db, schema,
		testProposal{"#mycity", "approved", 1}, testProposal{"#pending", "pending", 2}, testProposal{"#late", "approved", 3})
	s := newChannelKeySet(configuredKeys(), "/nonexistent/users.db", 128)
	s.usePostgres(testAccountsReader(t))
	s.refresh()
	snap := s.Snapshot()
	if snap["#mycity"] == "" || snap["#late"] == "" || snap["#pending"] != "" || len(snap) != 4 {
		t.Fatalf("snapshot = %v; want configured + #mycity + #late", snap)
	}
	pgSetProposals(t, db, schema, testProposal{"#mycity", "approved", 1})
	s.refresh()
	if snap := s.Snapshot(); snap["#late"] != "" || snap["#mycity"] == "" {
		t.Fatalf("after revoke: %v", snap)
	}
	// The cap keeps the oldest approvals, as on SQLite.
	pgSetProposals(t, db, schema, testProposal{"#c", "approved", 30}, testProposal{"#a", "approved", 10}, testProposal{"#b", "approved", 20})
	s.max = 2
	s.refresh()
	if snap := s.Snapshot(); snap["#a"] == "" || snap["#b"] == "" || snap["#c"] != "" {
		t.Fatalf("cap: %v", snap)
	}
}

func TestChannelKeySetPostgresKeepsLastGoodSetOnFailure(t *testing.T) {
	db, schema := usersPGSchemaForTest(t, true)
	pgSetProposals(t, db, schema, testProposal{"#mycity", "approved", 1})
	s := newChannelKeySet(configuredKeys(), "", 128)
	s.usePostgres(testAccountsReader(t))
	s.refresh()
	if _, err := db.Exec(`DROP TABLE "` + schema + `".proposals`); err != nil {
		t.Fatal(err)
	}
	s.refresh()
	if s.Snapshot()["#mycity"] == "" {
		t.Fatal("a failing read removed an approved key")
	}
}

func TestChannelKeySetPostgresMissingSchemaIsEmptyAndLogsOnce(t *testing.T) {
	usersPGSchemaForTest(t, false) // schema never created
	buf := captureLog(t)
	s := newChannelKeySet(configuredKeys(), "", 128)
	s.usePostgres(testAccountsReader(t))
	s.refresh()
	s.refresh()
	if len(s.Snapshot()) != 2 {
		t.Fatalf("snapshot = %v; want the configured keys", s.Snapshot())
	}
	if n := strings.Count(buf.String(), "approved channels unavailable"); n != 1 {
		t.Fatalf("logged %d times; want once:\n%s", n, buf.String())
	}
}

// Read-only pin, mirroring TestChannelKeySetIsReadOnly: the reader's own
// transaction refuses a write even for a role that could write.
func TestChannelKeySetPostgresIsReadOnly(t *testing.T) {
	db, schema := usersPGSchemaForTest(t, true)
	pgSetProposals(t, db, schema, testProposal{"#mycity", "approved", 1})
	assertPGReaderTxIsReadOnly(t, fmt.Sprintf(`INSERT INTO "%s".proposals (kind, subject, status) VALUES ('hashtag_channel', '#x', 'approved')`, schema))
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM "` + schema + `".proposals`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows = %d, err %v; the refused write must leave the table alone", n, err)
	}
}

// assertPGReaderTxIsReadOnly runs write in an accountsReader session, the
// one every accounts read uses, and requires Postgres' read-only refusal.
func assertPGReaderTxIsReadOnly(t *testing.T, write string) {
	t.Helper()
	err := testAccountsReader(t).session(func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, write)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "read-only transaction") {
		t.Fatalf("write in the reader's transaction: err = %v; want a read-only refusal", err)
	}
}

func TestLinkedCompanionSetPostgresReadsAndRefreshes(t *testing.T) {
	db, schema := usersPGSchemaForTest(t, true)
	pgSetLinks(t, db, schema, strings.ToUpper(testCompanionPK))
	clk := &linkClock{t: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)}
	s := newLinkedCompanionSet("")
	s.now = clk.now
	s.usePostgres(testAccountsReader(t))
	s.refresh()
	if !s.Allow(testCompanionPK) || s.Allow(testUnlinkedPK) {
		t.Fatal("want the linked pubkey allowed (any case) and the other refused")
	}
	pgSetLinks(t, db, schema, testCompanionPK, testUnlinkedPK)
	if s.Allow(testUnlinkedPK) { // inside the miss gap: no re-read
		t.Fatal("re-read inside the 5 s gap")
	}
	clk.add(linkedCompanionsMissGap)
	if !s.Allow(testUnlinkedPK) {
		t.Fatal("a miss after the gap re-reads and accepts the new link")
	}
	pgSetLinks(t, db, schema, testCompanionPK)
	s.refresh()
	if s.Allow(testUnlinkedPK) {
		t.Fatal("unlink not honoured after the periodic refresh")
	}
}

func TestLinkedCompanionSetPostgresMissingSchemaPassesAndLogsOnce(t *testing.T) {
	usersPGSchemaForTest(t, false)
	buf := captureLog(t)
	s := newLinkedCompanionSet("")
	s.usePostgres(testAccountsReader(t))
	s.refresh()
	s.refresh()
	if !s.Allow(testUnlinkedPK) {
		t.Fatal("an unreadable accounts schema must pass data, as a missing users.db does")
	}
	// No accounts schema means users.db, which is missing here too: the
	// once-per-streak "unavailable" line, as on SQLite.
	if n := strings.Count(buf.String(), "has no companion_links table") + strings.Count(buf.String(), "linked companions unavailable"); n != 1 {
		t.Fatalf("logged %d times; want once:\n%s", n, buf.String())
	}
}

func TestLinkedCompanionSetPostgresMissingTableWarnsOnce(t *testing.T) {
	db, schema := usersPGSchemaForTest(t, true)
	if _, err := db.Exec(`DROP TABLE "` + schema + `".companion_links`); err != nil {
		t.Fatal(err)
	}
	buf := captureLog(t)
	s := newLinkedCompanionSet("")
	s.usePostgres(testAccountsReader(t))
	s.refresh()
	s.refresh()
	if !s.Allow(testUnlinkedPK) {
		t.Fatal("a missing table must pass data")
	}
	if n := strings.Count(buf.String(), "has no companion_links table"); n != 1 {
		t.Fatalf("logged %d times; want once:\n%s", n, buf.String())
	}
}

func TestLinkedCompanionSetPostgresIsReadOnly(t *testing.T) {
	db, schema := usersPGSchemaForTest(t, true)
	pgSetLinks(t, db, schema, testCompanionPK)
	assertPGReaderTxIsReadOnly(t, fmt.Sprintf(`INSERT INTO "%s".companion_links (pubkey, user_id, linked_at) VALUES ('x', 1, 1)`, schema))
	s := newLinkedCompanionSet("")
	s.usePostgres(testAccountsReader(t))
	s.refresh()
	if !s.Allow(testCompanionPK) {
		t.Fatal("reader failed")
	}
}

// Postgres measurement data with the accounts still in users.db (one server
// replica with a volume): no accounts schema, so the approved channels come
// from the file, as before accounts could move to PostgreSQL.
func TestChannelKeySetPostgresNoAccountsSchemaReadsUsersDB(t *testing.T) {
	usersPGSchemaForTest(t, false)
	path := filepath.Join(t.TempDir(), "users.db")
	writeUsersDB(t, path, testProposal{"#mycity", "approved", 1})
	s := newChannelKeySet(configuredKeys(), path, 128)
	t.Cleanup(s.Close)
	s.usePostgres(testAccountsReader(t))
	s.refresh()
	if _, ok := s.Snapshot()["#mycity"]; !ok || len(s.Snapshot()) != 3 {
		t.Fatalf("snapshot = %v; want the configured keys plus #mycity from users.db", s.Snapshot())
	}
	if s.source() != path {
		t.Fatalf("source = %q; want %q", s.source(), path)
	}
}

// Once the accounts schema exists, it is the source and users.db is ignored.
func TestChannelKeySetPostgresAccountsSchemaWinsOverUsersDB(t *testing.T) {
	db, schema := usersPGSchemaForTest(t, true)
	pgSetProposals(t, db, schema, testProposal{"#othercity", "approved", 1})
	path := filepath.Join(t.TempDir(), "users.db")
	writeUsersDB(t, path, testProposal{"#mycity", "approved", 1})
	s := newChannelKeySet(configuredKeys(), path, 128)
	t.Cleanup(s.Close)
	s.usePostgres(testAccountsReader(t))
	s.refresh()
	snap := s.Snapshot()
	if _, ok := snap["#othercity"]; !ok {
		t.Fatalf("snapshot = %v; want #othercity from %s", snap, schema)
	}
	if _, ok := snap["#mycity"]; ok {
		t.Fatalf("snapshot = %v; users.db must be ignored while the accounts schema exists", snap)
	}
}

func TestLinkedCompanionSetPostgresNoAccountsSchemaReadsUsersDB(t *testing.T) {
	usersPGSchemaForTest(t, false)
	path := filepath.Join(t.TempDir(), "users.db")
	setLinks(t, path, testCompanionPK)
	s := newLinkedCompanionSet(path)
	t.Cleanup(s.Close)
	s.usePostgres(testAccountsReader(t))
	s.refresh()
	if !s.Allow(testCompanionPK) || s.Allow(testUnlinkedPK) {
		t.Fatal("with no accounts schema the links must come from users.db")
	}
}

// plantAccountsFunction creates, as the test's superuser, a log table in the
// test's own schema and a function in the accounts schema that appends to it
// (as the accounts role could, against any table the writer can write). It
// returns the function's qualified name and a func counting the log rows.
func plantAccountsFunction(t *testing.T, db *sql.DB, schema string) (string, func() int) {
	t.Helper()
	var main string
	if err := db.QueryRow(`SELECT current_schema()`).Scan(&main); err != nil {
		t.Fatal(err)
	}
	logTable := `"` + main + `".planted_log`
	fn := `"` + schema + `".planted()`
	for _, stmt := range []string{
		`CREATE TABLE ` + logTable + ` (at TIMESTAMPTZ NOT NULL DEFAULT now())`,
		`CREATE FUNCTION ` + fn + ` RETURNS boolean LANGUAGE plpgsql AS $$ BEGIN INSERT INTO ` + logTable + ` DEFAULT VALUES; RETURN true; END $$`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	count := func() int {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM ` + logTable).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// The function works: called by a session that may write, it logs.
	if _, err := db.Exec(`SELECT ` + fn); err != nil || count() != 1 {
		t.Fatalf("planted function did not log (err %v)", err)
	}
	if _, err := db.Exec(`DELETE FROM ` + logTable); err != nil {
		t.Fatal(err)
	}
	return fn, count
}

// The accounts role owns proposals and companion_links, so it could turn one
// into something whose SELECT runs its code as the writer. Each is refused
// before the SELECT, logged once per streak, keeps the keys in force, and
// never runs the planted function.
func TestAccountsReaderRefusesNonPlainTables(t *testing.T) {
	for _, tc := range []struct {
		name  string
		table string
		plant func(q, fn string) []string // q is the quoted accounts schema
	}{
		{"view replacing proposals", "proposals", func(q, fn string) []string {
			return []string{
				`ALTER TABLE ` + q + `.proposals RENAME TO proposals_real`,
				`CREATE VIEW ` + q + `.proposals AS SELECT * FROM ` + q + `.proposals_real WHERE ` + fn,
			}
		}},
		{"row-level security on companion_links", "companion_links", func(q, fn string) []string {
			return []string{
				`ALTER TABLE ` + q + `.companion_links ENABLE ROW LEVEL SECURITY`,
				`ALTER TABLE ` + q + `.companion_links FORCE ROW LEVEL SECURITY`,
				`CREATE POLICY planted ON ` + q + `.companion_links USING (` + fn + `)`,
			}
		}},
		{"rule on companion_links", "companion_links", func(q, fn string) []string {
			return []string{`CREATE RULE planted AS ON INSERT TO ` + q + `.companion_links WHERE ` + fn + ` DO ALSO NOTHING`}
		}},
		{"column of the accounts role's own type", "companion_links", func(q, fn string) []string {
			return []string{
				`CREATE DOMAIN ` + q + `.pk AS text`,
				`ALTER TABLE ` + q + `.companion_links ALTER COLUMN pubkey TYPE ` + q + `.pk`,
			}
		}},
		{"inheritance child of proposals", "proposals", func(q, fn string) []string {
			return []string{`CREATE TABLE ` + q + `.proposals_child () INHERITS (` + q + `.proposals)`}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, schema := usersPGSchemaForTest(t, true)
			pgSetProposals(t, db, schema, testProposal{"#mycity", "approved", 1})
			pgSetLinks(t, db, schema, testCompanionPK)
			fn, ran := plantAccountsFunction(t, db, schema)
			for _, stmt := range tc.plant(`"`+schema+`"`, fn) {
				if _, err := db.Exec(stmt); err != nil {
					t.Fatalf("%s: %v", stmt, err)
				}
			}
			buf := captureLog(t)
			want := `"` + schema + `".` + tc.table + ` is not a plain table; refusing to read it as the writer`
			if tc.table == "proposals" {
				s := newChannelKeySet(configuredKeys(), "", 128)
				t.Cleanup(s.Close)
				s.usePostgres(testAccountsReader(t))
				s.refresh()
				s.refresh()
				if len(s.Snapshot()) != 2 {
					t.Fatalf("snapshot = %v; want the configured keys only", s.Snapshot())
				}
			} else {
				s := newLinkedCompanionSet("")
				t.Cleanup(s.Close)
				s.usePostgres(testAccountsReader(t))
				s.refresh()
				s.refresh()
				if s.loaded.Load() || !s.Allow(testUnlinkedPK) {
					t.Fatal("a refused read must leave the set unloaded (pass-through)")
				}
			}
			if n := strings.Count(buf.String(), want); n != 1 {
				t.Fatalf("refusal logged %d times; want once (%q):\n%s", n, want, buf.String())
			}
			if n := ran(); n != 0 {
				t.Fatalf("the planted function ran %d times", n)
			}
		})
	}
}

// An advisory lock taken on the reader's connection does not outlive the
// read: released after a clean read, gone with the discarded connection after
// a failed one. Either way the writer-privileged session holds nothing.
func TestAccountsReaderLeavesNoAdvisoryLock(t *testing.T) {
	db, _ := usersPGSchemaForTest(t, false) // no accounts schema: the last read reads nothing
	r := testAccountsReader(t)
	advisoryLocks := func(pid int) int {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND pid = ?`, pid).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	backendAlive := func(pid int) bool {
		var ok bool
		if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid = ?)`, pid).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	lock := func(fail bool) int {
		var pid int
		err := r.session(func(ctx context.Context, tx *sql.Tx) error {
			if err := tx.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_lock(?), pg_advisory_lock_shared(?)`, int64(7340101), int64(7340102)); err != nil {
				return err
			}
			if n := advisoryLocks(pid); n != 2 {
				t.Errorf("advisory locks inside the read = %d; want 2", n)
			}
			if fail {
				return fmt.Errorf("read failed")
			}
			return nil
		})
		if fail != (err != nil) {
			t.Fatalf("session err = %v", err)
		}
		return pid
	}

	pid := lock(false)
	if n := advisoryLocks(pid); n != 0 {
		t.Fatalf("%d advisory locks survive a clean read", n)
	}
	if !backendAlive(pid) {
		t.Fatal("a clean read discarded its connection; want it kept (and unlocked)")
	}

	pid = lock(true)
	deadline := time.Now().Add(5 * time.Second)
	for backendAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if backendAlive(pid) || advisoryLocks(pid) != 0 {
		t.Fatal("a failed read kept its connection, and the advisory lock with it")
	}
	// The next read opens a fresh connection and works.
	if _, inPG, err := r.readAccounts("companion_links", linkedCompanionsColumns, pgUsersQuery(linkedCompanionsQuery, "companion_links")); err != nil || inPG {
		t.Fatalf("read after a discard: inPG %v, err %v", inPG, err)
	}
}

// The reader's session settings are its own: a DSN that tries to loosen them
// (as a parameter or through options) does not.
func TestAccountsReaderSessionSettings(t *testing.T) {
	base := os.Getenv("CORESCOPE_TEST_PG_DSN")
	if base == "" {
		t.Skip("CORESCOPE_TEST_PG_DSN not set; skipping Postgres test")
	}
	u, err := url.Parse(base)
	if err != nil || !dbx.IsPostgresURL(base) {
		t.Fatal("CORESCOPE_TEST_PG_DSN must be a postgres:// URL for this test")
	}
	q := u.Query()
	q.Set("default_transaction_read_only", "off")
	q.Set("search_path", "public")
	q.Set("options", "-c statement_timeout=0 -c lock_timeout=0 -c row_security=on")
	u.RawQuery = strings.ReplaceAll(q.Encode(), "+", "%20") // pgx reads %20, not "+", as a space
	r := newAccountsReader(u.String())
	t.Cleanup(r.Close)
	got := map[string]string{}
	err = r.session(func(ctx context.Context, tx *sql.Tx) error {
		for k := range accountsReaderParams {
			var v string
			if err := tx.QueryRowContext(ctx, `SELECT current_setting(?)`, k).Scan(&v); err != nil {
				return err
			}
			got[k] = v
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"default_transaction_read_only":       "on",
		"search_path":                         "pg_catalog",
		"statement_timeout":                   "2s",
		"lock_timeout":                        "1s",
		"idle_in_transaction_session_timeout": "5s",
		"row_security":                        "off",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q; want %q", k, got[k], v)
		}
	}
}

// A lock held on companion_links (the accounts role can take ACCESS
// EXCLUSIVE on its own table) costs the one miss that re-reads at most
// accountsReadTimeout, and no other Allow waits for it.
func TestLinkedCompanionAllowBoundedWhileCompanionLinksLocked(t *testing.T) {
	db, schema := usersPGSchemaForTest(t, true)
	pgSetLinks(t, db, schema, testCompanionPK)
	clk := &linkClock{t: time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)}
	s := newLinkedCompanionSet("")
	s.now = clk.now
	t.Cleanup(s.Close)
	s.usePostgres(testAccountsReader(t))
	s.refresh()
	if !s.loaded.Load() {
		t.Fatal("initial read failed")
	}

	holder, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	if _, err := holder.ExecContext(t.Context(), `BEGIN`); err != nil {
		t.Fatal(err)
	}
	defer holder.ExecContext(context.Background(), `ROLLBACK`)
	if _, err := holder.ExecContext(t.Context(), `LOCK TABLE "`+schema+`".companion_links IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}

	clk.add(linkedCompanionsMissGap)
	base := s.readCount()
	type result struct {
		ok   bool
		took time.Duration
	}
	done := make(chan result, 1)
	go func() {
		start := time.Now()
		ok := s.Allow(testUnlinkedPK)
		done <- result{ok, time.Since(start)}
	}()
	// Wait for that miss to start its re-read, then call Allow beside it.
	deadline := time.Now().Add(2 * time.Second)
	for s.readCount() == base && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.readCount() == base {
		t.Fatal("the miss after the gap did not start a re-read")
	}
	clk.add(linkedCompanionsMissGap) // a second re-read would be due, but one is in flight
	start := time.Now()
	other, linked := s.Allow("ff"+testUnlinkedPK[2:]), s.Allow(testCompanionPK)
	if took := time.Since(start); took > 250*time.Millisecond {
		t.Fatalf("a concurrent Allow waited %v for the read in flight", took)
	}
	if other || !linked {
		t.Fatalf("concurrent Allow: unlinked %v, linked %v; want the current snapshot's answers", other, linked)
	}
	select {
	case r := <-done:
		if r.ok {
			t.Fatal("an unlinked pubkey passed while the read failed")
		}
		if r.took > 3500*time.Millisecond {
			t.Fatalf("Allow took %v with companion_links locked", r.took)
		}
		t.Logf("Allow returned in %v with companion_links locked", r.took)
	case <-time.After(5 * time.Second):
		t.Fatal("Allow still blocked 5 s after companion_links was locked")
	}
	if n := s.readCount() - base; n != 1 {
		t.Fatalf("%d re-reads; want 1", n)
	}
}
