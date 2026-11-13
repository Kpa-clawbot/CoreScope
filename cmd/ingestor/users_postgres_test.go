package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	s.usePostgres(db)
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
	s.usePostgres(db)
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
	db, _ := usersPGSchemaForTest(t, false) // schema never created
	buf := captureLog(t)
	s := newChannelKeySet(configuredKeys(), "", 128)
	s.usePostgres(db)
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
	assertPGReaderTxIsReadOnly(t, db, fmt.Sprintf(`INSERT INTO "%s".proposals (kind, subject, status) VALUES ('hashtag_channel', '#x', 'approved')`, schema))
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM "` + schema + `".proposals`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows = %d, err %v; the refused write must leave the table alone", n, err)
	}
}

// assertPGReaderTxIsReadOnly opens a transaction exactly as queryUsersPG does
// and requires a write in it to fail with Postgres' read-only refusal.
func assertPGReaderTxIsReadOnly(t *testing.T, db *sql.DB, write string) {
	t.Helper()
	tx, err := db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = tx.Exec(write)
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
	s.usePostgres(db)
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
	db, _ := usersPGSchemaForTest(t, false)
	buf := captureLog(t)
	s := newLinkedCompanionSet("")
	s.usePostgres(db)
	s.refresh()
	s.refresh()
	if !s.Allow(testUnlinkedPK) {
		t.Fatal("an unreadable accounts schema must pass data, as a missing users.db does")
	}
	// Postgres reports a missing schema as a missing relation, so this is the
	// once-only "no companion_links table" warning.
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
	s.usePostgres(db)
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
	assertPGReaderTxIsReadOnly(t, db, fmt.Sprintf(`INSERT INTO "%s".companion_links (pubkey, user_id, linked_at) VALUES ('x', 1, 1)`, schema))
	s := newLinkedCompanionSet("")
	s.usePostgres(db)
	s.refresh()
	if !s.Allow(testCompanionPK) {
		t.Fatal("reader failed")
	}
}
