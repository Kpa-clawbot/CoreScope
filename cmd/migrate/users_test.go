package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/dbx"
	"github.com/meshcore-analyzer/users"
)

// usersPGURL-style target for tests. users.OpenPostgres always uses the
// schema corescope_users, so tests cannot share a database the way dbxtest
// shares one through per-test schemas. Each test gets its own throwaway
// DATABASE instead (created from $CORESCOPE_TEST_PG_DSN, dropped in
// t.Cleanup), inside which corescope_users is unique and owned by the
// connecting role, as OpenPostgres requires. Skips without the variable.
func usersPGTarget(t testing.TB) string {
	t.Helper()
	dsn := os.Getenv("CORESCOPE_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("CORESCOPE_TEST_PG_DSN not set; skipping Postgres test")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal("unparseable CORESCOPE_TEST_PG_DSN")
	}
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	name := "migrate_users_" + hex.EncodeToString(b[:])
	admin, err := sql.Open(dbx.PostgresDriverName, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(`CREATE DATABASE "` + name + `"`); err != nil {
		admin.Close()
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP DATABASE IF EXISTS "` + name + `" WITH (FORCE)`); err != nil {
			t.Errorf("drop test database: %v", err)
		}
		admin.Close()
	})
	u.Path = "/" + name
	q := u.Query()
	q.Del("search_path")
	u.RawQuery = q.Encode()
	return u.String()
}

// seedUsers creates a users.db through the users API with rows in every
// table, gaps in the id sequences, and returns its path.
func seedUsers(t testing.TB) (path string, adminPW string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "users.db")
	st, err := users.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	adminPW = "correct horse battery staple"
	hash := func(pw string) string {
		h, err := users.HashPassword(pw)
		must(err)
		return h
	}
	admin, err := st.CreatePending("admin@example.org", "Admin Ünï", hash(adminPW))
	must(err)
	must(st.Activate(admin.ID, users.RoleAdmin, nil))
	bob, err := st.CreatePending("bob@example.org", "Bob", hash("bob-pw"))
	must(err)
	must(st.Activate(bob.ID, users.RoleUser, &admin.ID))
	gone, err := st.CreatePending("gone@example.org", "Gone", hash("x"))
	must(err)
	must(st.Delete(gone.ID)) // a gap in users.id
	_, err = st.CreatePending("pending@example.org", "Pending", hash("p"))
	must(err)

	_, _, err = st.CreateSession(bob.ID, time.Hour, "agent/1")
	must(err)
	_, _, err = st.CreateDeviceSession(bob.ID, "phone", []string{"link"}, "app")
	must(err)
	_, err = st.IssueToken(bob.ID, users.PurposeReset, time.Hour, "")
	must(err)
	must(st.Audit(&admin.ID, "user.activate", &bob.ID, map[string]string{"by": "admin"}))
	must(st.Audit(nil, "system.start", nil, nil))
	mid, err := st.LogMail(&bob.ID, "bob@example.org", "activate", "msg-1")
	must(err)
	if mid == 0 {
		t.Fatal("no mail id")
	}
	// Events out of key order: rowid, not (mail_id, event, at), is insertion order.
	at := time.Now().Add(-time.Minute)
	for _, ev := range []string{"delivered", "bounced", "opened"} {
		_, _, err = st.RecordMailEvent("msg-1", ev, at, "r-"+ev)
		must(err)
		at = at.Add(time.Second)
	}
	_, err = st.PutSettings(bob.ID, users.SettingsVersion{}, `{"theme":"dark","name":"Zürich"}`)
	must(err)
	_, err = st.Propose("hashtag_channel", "#test", bob.ID, users.ProposalLimits{MaxPending: 10, PerUserPerDay: 10})
	must(err)
	_, err = st.SetNotifyPrefs(bob.ID, true, []string{users.NotifyNodeOffline})
	must(err)
	must(st.AddWatch(bob.ID, strings.Repeat("ff01", 16), 10))
	must(st.WriteNotifyStates([]users.NotifyState{{
		NotifyKey: users.NotifyKey{UserID: bob.ID, Event: users.NotifyNodeOffline, Subject: strings.Repeat("ff01", 16)},
		State:     users.NotifyBad, ChangedAt: time.Now(),
	}}))
	_, _, err = st.UpsertCompanionLink(bob.ID, strings.Repeat("ab12", 16), "Bob's companion")
	must(err)
	_, _, err = st.CreateLinkChallenge(bob.ID, strings.Repeat("cd34", 16))
	must(err)
	must(st.CreateMQTTLogin(users.MQTTLogin{UserID: bob.ID, Username: "u_bob", PasswordHash: "h", PasswordEnc: "e", CreatedAt: time.Now()}))
	_, _, err = st.BindObserver(strings.Repeat("ee55", 16), bob.ID, "admin")
	must(err)
	return path, adminPW
}

var usersSeededTables = []string{"users", "sessions", "tokens", "audit_log", "mail_log", "mail_events",
	"user_settings", "proposals", "notification_prefs", "notification_watches", "notification_state",
	"companion_links", "link_challenges", "mqtt_logins", "observer_bindings"}

func TestCopyUsersToPostgres(t *testing.T) {
	pgURL := usersPGTarget(t)
	path, adminPW := seedUsers(t)

	code, out := run(t, "copy-users", "-from", "sqlite:"+path, "-to", pgURL, "-batch", "2")
	wantOK(t, code, out)
	code, out = run(t, "verify-users", "-a", "sqlite:"+path, "-b", pgURL)
	wantOK(t, code, out)
	if strings.Contains(out, "schema_version") {
		t.Errorf("schema_version was compared:\n%s", out)
	}

	// Every table the schema has holds exactly the source's rows.
	src, err := sql.Open("sqlite3", sqliteReadOnlyDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	scoped, err := usersPGURL(pgURL)
	if err != nil {
		t.Fatal(err)
	}
	pg, err := sql.Open(dbx.PostgresDriverName, scoped)
	if err != nil {
		t.Fatal(err)
	}
	defer pg.Close()
	for _, tbl := range usersSeededTables {
		n := count(t, src, tbl)
		if n == 0 {
			t.Errorf("seed left %s empty", tbl)
		}
		if got := count(t, pg, tbl); got != n {
			t.Errorf("%s: %d rows in Postgres, %d in the source", tbl, got, n)
		}
	}

	// The copy is a working account store: open it as the server would.
	st, err := users.OpenPostgres(pgURL)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, err := st.GetByEmail("admin@example.org")
	if err != nil {
		t.Fatal(err)
	}
	var want string
	if err := src.QueryRow(`SELECT password_hash FROM users WHERE email = 'admin@example.org'`).Scan(&want); err != nil {
		t.Fatal(err)
	}
	if a.PasswordHash != want {
		t.Errorf("password hash changed in the copy")
	}
	if ok, err := users.VerifyPassword(a.PasswordHash, adminPW); err != nil || !ok {
		t.Errorf("the admin cannot log in with the original password: ok=%v err=%v", ok, err)
	}
	if a.Role != users.RoleAdmin || a.DisplayName != "Admin Ünï" {
		t.Errorf("admin = %+v", a)
	}
	bob, err := st.GetByEmail("bob@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if ev, err := st.MailByID(1); err != nil || ev == nil {
		t.Errorf("mail 1: %v %v", ev, err)
	}
	if links, err := st.ListCompanionLinks(bob.ID); err != nil || len(links) != 1 {
		t.Errorf("companion links = %v, %v", links, err)
	}

	// Identity sequences continue after the copied ids (gap included).
	var maxID int64
	if err := pg.QueryRow(`SELECT MAX(id) FROM users`).Scan(&maxID); err != nil {
		t.Fatal(err)
	}
	nu, err := st.CreatePending("new@example.org", "New", "h")
	if err != nil {
		t.Fatal(err)
	}
	if nu.ID != maxID+1 {
		t.Errorf("next user id = %d, want %d", nu.ID, maxID+1)
	}
	// mail_events.rowid keeps the source's insertion order.
	rows, err := pg.Query(`SELECT event FROM mail_events ORDER BY rowid`)
	if err != nil {
		t.Fatal(err)
	}
	var evs []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			t.Fatal(err)
		}
		evs = append(evs, e)
	}
	rows.Close()
	if strings.Join(evs, ",") != "delivered,bounced,opened" {
		t.Errorf("mail event order = %v", evs)
	}
}

func TestVerifyUsersDetectsDifference(t *testing.T) {
	pgURL := usersPGTarget(t)
	path, _ := seedUsers(t)
	code, out := run(t, "copy-users", "-from", "sqlite:"+path, "-to", pgURL)
	wantOK(t, code, out)

	st, err := users.OpenPostgres(pgURL)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := st.GetByEmail("bob@example.org")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetDisplayName(bob.ID, "Robert"); err != nil {
		t.Fatal(err)
	}
	st.Close()
	code, out = run(t, "verify-users", "-a", "sqlite:"+path, "-b", pgURL)
	wantFail(t, code, out, "MISMATCH", "display_name")
}

func TestCopyUsersRefusesNonEmptyTarget(t *testing.T) {
	pgURL := usersPGTarget(t)
	st, err := users.OpenPostgres(pgURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreatePending("keep@example.org", "Keep", "h"); err != nil {
		t.Fatal(err)
	}
	st.Close()
	path, _ := seedUsers(t)

	code, out := run(t, "copy-users", "-from", "sqlite:"+path, "-to", pgURL)
	wantFail(t, code, out, "target is not empty (table users has rows); refusing to overwrite")
	st, err = users.OpenPostgres(pgURL)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if l, err := st.List(users.ListFilter{}); err != nil || len(l) != 1 {
		t.Errorf("target users = %v, %v; want the 1 pre-existing", l, err)
	}
}

func TestCopyUsersRefusesVersionMismatch(t *testing.T) {
	pgURL := usersPGTarget(t)
	path, _ := seedUsers(t)
	db, err := sql.Open("sqlite3", dbschemaWriterDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `UPDATE schema_version SET version = version - 1`)
	db.Close()

	code, out := run(t, "copy-users", "-from", "sqlite:"+path, "-to", pgURL)
	wantFail(t, code, out, "account schema version", "start the CoreScope server once")
	// Nothing was created in the target.
	admin, err := sql.Open(dbx.PostgresDriverName, pgURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var n int
	if err := admin.QueryRow(`SELECT COUNT(*) FROM pg_namespace WHERE nspname = 'corescope_users'`).Scan(&n); err != nil || n != 0 {
		t.Errorf("corescope_users exists after a refusal: n=%d err=%v", n, err)
	}
}

func TestCopyUsersRefusesWrongBackends(t *testing.T) {
	path, _ := seedUsers(t)
	code, out := run(t, "copy-users", "-from", "sqlite:"+path, "-to", "sqlite:"+filepath.Join(t.TempDir(), "x.db"))
	wantFail(t, code, out, "writes the Postgres accounts schema")
	code, out = run(t, "copy-users", "-from", "postgres://u@127.0.0.1:1/db", "-to", "postgres://u@127.0.0.1:1/db")
	wantFail(t, code, out, "reads a SQLite users.db")
	code, out = run(t, "copy-users", "-from", "sqlite:"+path)
	if code != 2 {
		t.Errorf("missing -to: exit %d, want 2:\n%s", code, out)
	}
}

func TestCopyUsersRefusesNonUsersDB(t *testing.T) {
	notUsers := filepath.Join(t.TempDir(), "meshcore.db")
	newSource(t, notUsers, srcMigrations())
	code, out := run(t, "copy-users", "-from", "sqlite:"+notUsers, "-to", "postgres://u@127.0.0.1:1/db")
	wantFail(t, code, out, "not a CoreScope users.db")
}

func TestUsersRedactsPasswords(t *testing.T) {
	path, _ := seedUsers(t)
	const target = "postgres://alice:S3cretPw9@127.0.0.1:1/cs?connect_timeout=2"
	code, out := run(t, "copy-users", "-from", "sqlite:"+path, "-to", target)
	wantFail(t, code, out)
	code2, out2 := run(t, "verify-users", "-a", "sqlite:"+path, "-b", "sqlite:"+filepath.Join(t.TempDir(), "missing.db"))
	wantFail(t, code2, out2)
	code3, out3 := run(t, "verify-users", "-a", "sqlite:"+path, "-b", "postgres://alice:S3cretPw9@[::1/cs")
	wantFail(t, code3, out3, "unparseable postgres URL")
	for _, o := range []string{out, out2, out3} {
		if strings.Contains(o, "S3cretPw9") {
			t.Errorf("output leaks the password:\n%s", o)
		}
	}
}

func TestUsersPGURLSetsSearchPath(t *testing.T) {
	got, err := usersPGURL("postgres://u@h:5432/db?sslmode=require&search_path=other")
	if err != nil {
		t.Fatal(err)
	}
	u := mustURL(t, got)
	if u.Query().Get("search_path") != "corescope_users" || u.Query().Get("sslmode") != "require" {
		t.Errorf("url = %s", got)
	}
}

func dbschemaWriterDSN(path string) string { return dbschema.WriterDSN(path) }
