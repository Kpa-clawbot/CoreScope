package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/dbx"
	"github.com/meshcore-analyzer/mailer"
	"github.com/meshcore-analyzer/users"
)

// accountsPGTarget returns a DSN for a throwaway DATABASE created from
// $CORESCOPE_TEST_PG_DSN and dropped in Cleanup. users.OpenPostgres always
// uses schema corescope_users, so tests cannot share a database through
// per-test schemas the way dbxtest does (cmd/migrate's usersPGTarget does
// the same). Skips without the variable.
func accountsPGTarget(t *testing.T) string {
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
	name := "server_accounts_" + hex.EncodeToString(b[:])
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

// pgReplicaStores opens n account stores on one fresh Postgres database, as n
// server replicas would, each with its own pool. Closed in Cleanup (by the
// auth services built on them, or here).
func pgReplicaStores(t *testing.T, n int) []*users.Store {
	t.Helper()
	dsn := accountsPGTarget(t)
	out := make([]*users.Store, n)
	for i := range out {
		st, err := users.OpenPostgres(dsn)
		if err != nil {
			t.Fatalf("OpenPostgres: %v", err)
		}
		out[i] = st
	}
	return out
}

// pgReplicaAuth builds one auth service per store, sharing settings
// (admin@example.org a config admin).
func pgReplicaAuth(t *testing.T, stores []*users.Store) ([]*authService, []*mailer.Fake) {
	t.Helper()
	var as []*authService
	var fakes []*mailer.Fake
	for _, st := range stores {
		a, f := newTestAuthServiceOn(t, testUserMgmtSettings(t, "admin@example.org"), st)
		as = append(as, a)
		fakes = append(fakes, f)
	}
	return as, fakes
}

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return &buf
}

func TestResolveUserManagementAccountsDSN(t *testing.T) {
	base := func() *UserManagementConfig {
		return &UserManagementConfig{Enabled: true, PublicBaseURL: testBase,
			Mail: UserMailConfig{BrevoAPIKey: "k", FromEmail: "noreply@example.org"}}
	}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	cases := []struct {
		name, cfg, env, want string
	}{
		{"neither", "", "", ""},
		{"env fills an empty config", "", "postgres://env@h/db", "postgres://env@h/db"},
		{"config wins over env", " postgres://cfg@h/db ", "postgres://env@h/db", "postgres://cfg@h/db"},
		{"blank env is unset", "", "   ", ""},
	}
	for _, c := range cases {
		u := base()
		u.DSN = c.cfg
		set, err := resolveUserManagement(u, "/data/meshcore.db", env(map[string]string{accountsDSNEnv: c.env}))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if set.dsn != c.want {
			t.Errorf("%s: dsn = %q, want %q", c.name, set.dsn, c.want)
		}
		if set.dbPath != "/data/users.db" {
			t.Errorf("%s: dbPath = %q", c.name, set.dbPath)
		}
	}
}

func TestInitUserManagementOpensPostgresAccounts(t *testing.T) {
	dsn := accountsPGTarget(t)
	logs := captureLogs(t)
	dir := t.TempDir()
	// From the environment: the config leaves dsn empty.
	t.Setenv(accountsDSNEnv, dsn)
	srv, _ := setupTestServer(t) // SQLite measurement DB: no Postgres role to check
	srv.cfg.UserManagement = &UserManagementConfig{Enabled: true, PublicBaseURL: testBase,
		Mail: UserMailConfig{BrevoAPIKey: "k", FromEmail: "noreply@example.org"}}
	if err := srv.initUserManagement(filepath.Join(dir, "meshcore.db")); err != nil {
		t.Fatal(err)
	}
	if !srv.auth.st.IsPostgres() {
		t.Fatal("accounts not on Postgres with DB_DSN_ACCOUNTS set")
	}
	if _, err := srv.auth.st.CreatePending("pat@example.org", "Pat", "x"); err != nil {
		t.Fatalf("write through the Postgres store: %v", err)
	}
	// The janitor's first pass (prune, backup) runs before it sees stop.
	srv.closeUserManagement()
	for _, p := range []string{"users.db", "backups"} {
		if _, err := os.Stat(filepath.Join(dir, p)); !os.IsNotExist(err) {
			t.Errorf("%s created with the accounts on Postgres: %v", p, err)
		}
	}
	out := logs.String()
	if !strings.Contains(out, "db=postgres (schema corescope_users)") || !strings.Contains(out, "pg_dump") {
		t.Errorf("startup log lacks the backend or the pg_dump note:\n%s", out)
	}
	u, _ := url.Parse(dsn)
	if strings.Contains(out, dsn) || strings.Contains(out, u.User.String()+"@") || strings.Contains(out, u.Host) {
		t.Errorf("startup log leaks the DSN:\n%s", out)
	}
}

func TestAccountsPGRoleCheck(t *testing.T) {
	seed := pgSchemaSeed(t)
	reader, _, readerDSN := pgTestRole(t, seed, pgReadOnlyGrants...)
	d, err := OpenPostgresDB(readerDSN)
	if err != nil {
		t.Fatalf("OpenPostgresDB: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	ctx := context.Background()

	if err := verifyAccountsPGRole(ctx, d.conn, reader); err == nil || !strings.Contains(err.Error(), "measurement reader role") {
		t.Fatalf("the reader role as accounts role: err = %v", err)
	}
	clean, _, _ := pgTestRole(t, seed)
	if err := verifyAccountsPGRole(ctx, d.conn, clean); err != nil {
		t.Fatalf("a role with no grants refused: %v", err)
	}
	if err := verifyAccountsPGRole(ctx, d.conn, "cs_no_such_role"); err != nil {
		t.Fatalf("a role unknown to the cluster refused: %v", err)
	}

	writer, writerPW, writerDSN := pgTestRole(t, seed, `GRANT INSERT ON nodes TO {role}`)
	err = verifyAccountsPGRole(ctx, d.conn, writer)
	if err == nil || !strings.Contains(err.Error(), "nodes") || !strings.Contains(err.Error(), writer) {
		t.Fatalf("a role with INSERT on nodes: err = %v", err)
	}

	group := pgGroupRole(t, seed)
	if _, err := seed.Exec(`GRANT UPDATE ON observers TO ` + group); err != nil {
		t.Fatal(err)
	}
	member, _, _ := pgTestRole(t, seed, `GRANT `+group+` TO {role}`)
	if err := verifyAccountsPGRole(ctx, d.conn, member); err == nil || !strings.Contains(err.Error(), "via role "+group) {
		t.Fatalf("a role writing through a group: err = %v", err)
	}

	var control bool
	if err := seed.QueryRow(`SELECT to_regclass('` + pgControlSchema + `.prune_requests') IS NOT NULL`).Scan(&control); err != nil {
		t.Fatal(err)
	}
	if control {
		ctl, _, _ := pgTestRole(t, seed, `GRANT INSERT ON `+pgControlSchema+`.prune_requests TO {role}`)
		if err := verifyAccountsPGRole(ctx, d.conn, ctl); err == nil || !strings.Contains(err.Error(), "prune_requests") {
			t.Fatalf("a role with INSERT on the control table: err = %v", err)
		}
	}

	// Through startup: refused before the store is opened.
	srv := &Server{db: d, cfg: &Config{UserManagement: &UserManagementConfig{Enabled: true, DSN: writerDSN,
		PublicBaseURL: testBase, Mail: UserMailConfig{BrevoAPIKey: "k", FromEmail: "noreply@example.org"}}}}
	err = srv.initUserManagement(filepath.Join(t.TempDir(), "meshcore.db"))
	if err == nil || srv.auth != nil {
		t.Fatalf("startup with a writing accounts role: auth=%v err=%v", srv.auth, err)
	}
	if strings.Contains(err.Error(), writerPW) || strings.Contains(err.Error(), writerDSN) {
		t.Fatalf("refusal leaks the DSN: %v", err)
	}
	var made bool
	if err := seed.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'corescope_users' AND pg_get_userbyid(nspowner) = ?)`, writer).Scan(&made); err != nil || made {
		t.Fatalf("the refused role's store was opened anyway: made=%v err=%v", made, err)
	}
}

func TestAccountsPGRoleFromDSN(t *testing.T) {
	for dsn, want := range map[string]string{
		"postgres://acct:secret@db.example.org/corescope": "acct",
		"host=db.example.org user=acct2 password=secret":  "acct2",
	} {
		if got, err := accountsPGRole(dsn); err != nil || got != want {
			t.Errorf("accountsPGRole(%q) = %q, %v; want %q", dsn, got, err, want)
		}
	}
	if _, err := accountsPGRole("postgres://acct:s3cret@[::1"); err == nil || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("bad DSN: err = %v", err)
	}
}

// pgWatcher creates an active user watching pk, with default preferences.
func pgWatcher(t *testing.T, st *users.Store, email, pk string) int64 {
	t.Helper()
	u, err := st.CreatePending(email, "Pat", "x")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Activate(u.ID, users.RoleUser, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.NotifyPrefsFor(u.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := st.AddWatches(u.ID, []string{pk}, 0); err != nil {
		t.Fatal(err)
	}
	return u.ID
}

// barrierSource is a fake analyzer whose lastHeard (after the states are
// read, before they are written) waits for wait, so two evaluations
// overlap there.
type barrierSource struct {
	*fakeNotifySource
	wait func()
}

func (b *barrierSource) lastHeard(pks []string) (map[string]time.Time, time.Duration) {
	if b.wait != nil {
		b.wait()
	}
	return b.fakeNotifySource.lastHeard(pks)
}

type pgNotifyReplicas struct {
	as    []*authService
	fakes []*mailer.Fake
	ns    []*notifier
	srcs  []*barrierSource
	clk   *notifyClock
}

// newPGNotifyReplicas is two replicas on one Postgres store with
// notifications on, one watcher of evPkA, and that node offline-to-be.
func newPGNotifyReplicas(t *testing.T) *pgNotifyReplicas {
	t.Helper()
	stores := pgReplicaStores(t, 2)
	as, fakes := pgReplicaAuth(t, stores)
	r := &pgNotifyReplicas{as: as, fakes: fakes, clk: &notifyClock{t: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}}
	for i, a := range as {
		a.set.notify = defaultNotifySettings()
		stores[i].SetClock(r.clk.Now)
		src := &barrierSource{fakeNotifySource: &fakeNotifySource{isReady: true, hs: (&Config{}).GetHealthThresholds(), low: 3300,
			nodeMap: map[string]notifyNode{}, heard: map[string]time.Time{}, newestAt: r.clk.Now}}
		src.nodeMap[evPkA] = notifyNode{Pubkey: evPkA, Name: "Alpha", Role: "companion",
			LastSeen: r.clk.t.Add(-time.Hour).Format(time.RFC3339)}
		r.srcs = append(r.srcs, src)
		n := newNotifier(a, src, r.clk.Now)
		a.notify = n
		r.ns = append(r.ns, n)
	}
	pgWatcher(t, stores[0], "pat@example.org", evPkA)
	return r
}

func (r *pgNotifyReplicas) notifyMails() int {
	n := 0
	for _, f := range r.fakes {
		for _, m := range f.Sent() {
			if m.Tag == "notify" {
				n++
			}
		}
	}
	return n
}

func TestPGNotifierRunsOnOneReplica(t *testing.T) {
	r := newPGNotifyReplicas(t)
	ctx := context.Background()
	st := r.as[0].st
	release, ok, err := st.TryExclusive(ctx, notifyJobName)
	if err != nil || !ok {
		t.Fatalf("TryExclusive: ok=%v err=%v", ok, err)
	}
	logs := captureLogs(t)
	r.ns[1].tick(ctx)
	r.ns[1].tick(ctx)
	if states, err := st.AllNotifyStates(); err != nil || len(states) != 0 {
		t.Fatalf("replica 2 evaluated while replica 1 held the lock: states=%v err=%v", states, err)
	}
	if c := strings.Count(logs.String(), "another replica runs this job"); c != 1 {
		t.Errorf("skip logged %d times over two ticks; want once:\n%s", c, logs.String())
	}
	release()
	r.ns[1].tick(ctx)
	if states, err := st.AllNotifyStates(); err != nil || len(states) == 0 {
		t.Fatalf("replica 2 did not evaluate after the lock was freed: states=%v err=%v", states, err)
	}
	if !strings.Contains(logs.String(), "holds the job lock again") {
		t.Errorf("taking the lock again is not logged:\n%s", logs.String())
	}
}

// Two evaluations that overlap (the lock lost with its connection) read the
// same states and find the same change; the claim lets one mail it.
func TestPGNotifierOverlapMailsOnce(t *testing.T) {
	r := newPGNotifyReplicas(t)
	ctx := context.Background()
	r.ns[0].tick(ctx) // the silent first evaluation
	if r.notifyMails() != 0 {
		t.Fatal("the first evaluation mailed")
	}
	for _, a := range r.as {
		a.tryExclusive = func(context.Context, string) (func(), bool, error) { return func() {}, true, nil }
	}
	r.clk.Advance(25 * time.Hour)
	var arrived sync.WaitGroup
	arrived.Add(2)
	for _, s := range r.srcs {
		s.wait = func() { arrived.Done(); arrived.Wait() }
	}
	var done sync.WaitGroup
	for _, n := range r.ns {
		done.Add(1)
		go func(n *notifier) {
			defer done.Done()
			n.tick(ctx)
		}(n)
	}
	done.Wait()
	if got := r.notifyMails(); got != 1 {
		t.Fatalf("notification mails after two overlapping evaluations = %d; want 1", got)
	}
	// The next transition of the same subject is a new claim.
	r.clk.Advance(5 * time.Minute)
	for _, s := range r.srcs {
		s.wait = nil
		s.heard[evPkA] = r.clk.t.Add(-time.Minute)
	}
	r.ns[1].tick(ctx)
	r.ns[0].tick(ctx)
	if got := r.notifyMails(); got != 2 {
		t.Fatalf("mails after the node came back = %d; want 2", got)
	}
}

func TestPGJanitorRunsOnOneReplica(t *testing.T) {
	stores := pgReplicaStores(t, 2)
	as, _ := pgReplicaAuth(t, stores)
	ctx := context.Background()
	old := time.Now().Add(-5 * time.Hour)
	// An ended window: max 1, so a further hit is refused while the row
	// exists and allowed once the janitor pruned it.
	probe := func() bool {
		t.Helper()
		ok, err := stores[0].RateLimitHit(ctx, "probe", time.Hour, 1, old)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !probe() {
		t.Fatal("first probe hit refused")
	}
	release, ok, err := stores[0].TryExclusive(ctx, janitorJobName)
	if err != nil || !ok {
		t.Fatalf("TryExclusive: ok=%v err=%v", ok, err)
	}
	as[1].janitorTick()
	if probe() {
		t.Fatal("replica 2 pruned while replica 1 held the janitor lock")
	}
	release()
	as[1].janitorTick()
	if !probe() {
		t.Fatal("the janitor did not prune the ended rate-limit window")
	}
}

func TestPGRateLimitSharedAcrossReplicas(t *testing.T) {
	stores := pgReplicaStores(t, 2)
	as, _ := pgReplicaAuth(t, stores)
	for i := 0; i < 10; i++ {
		if ok, _ := as[i%2].login.take("ip:192.0.2.1"); !ok {
			t.Fatalf("login attempt %d refused", i+1)
		}
	}
	ok, wait := as[1].login.take("ip:192.0.2.1")
	if ok || wait <= 0 || wait > 15*time.Minute {
		t.Fatalf("11th attempt across replicas: ok=%v wait=%v; want refused within the 15-minute window", ok, wait)
	}
	if ok, _ := as[0].login.take("ip:192.0.2.2"); !ok {
		t.Fatal("another key refused")
	}
	// Limiters keep their own rows: the same key on another limiter counts apart.
	if ok, _ := as[0].signup.take("ip:192.0.2.1"); !ok {
		t.Fatal("the signup limiter counted the login hits")
	}
}

func TestPGCachesReloadFromOtherReplica(t *testing.T) {
	stores := pgReplicaStores(t, 2)
	as, _ := pgReplicaAuth(t, stores)
	for _, a := range as {
		a.set.proposals = resolveProposals(&ChannelProposalsConfig{Enabled: true})
	}
	ix, err := newObserverLoginIndex(stores[1], nil)
	if err != nil {
		t.Fatal(err)
	}
	as[1].obsIdx = ix

	u, err := stores[0].CreatePending("pat@example.org", "Pat", "x")
	if err != nil {
		t.Fatal(err)
	}
	if err := stores[0].Activate(u.ID, users.RoleAdmin, nil); err != nil {
		t.Fatal(err)
	}
	p, err := stores[0].Propose(users.KindHashtagChannel, "#reload", u.ID, users.ProposalLimits{MaxPending: 10, PerUserPerDay: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stores[0].Decide(p.ID, users.ProposalApprove, u.ID, "", 0); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("observer-password"))
	if err := stores[0].CreateMQTTLogin(users.MQTTLogin{UserID: u.ID, Username: "obs_reload000000",
		PasswordHash: hex.EncodeToString(sum[:]), PasswordEnc: "e", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if got := as[1].approvedChannels(); len(got) != 0 {
		t.Fatalf("replica 2 saw the approval before a reload: %v", got)
	}
	if got := ix.authenticate("obs_reload000000", "observer-password"); got != hookIgnore {
		t.Fatalf("replica 2 knew the login before a reload: %v", got)
	}

	as[1].wg.Add(1)
	go func() {
		defer as[1].wg.Done()
		as[1].reloadCachesLoop(20 * time.Millisecond)
	}()
	t.Cleanup(func() {
		as[1].stopOnce.Do(func() { close(as[1].stop) })
		as[1].wg.Wait()
	})
	deadline := time.Now().Add(5 * time.Second)
	for {
		ch := as[1].approvedChannels()
		auth := ix.authenticate("obs_reload000000", "observer-password")
		if len(ch) == 1 && ch[0] == "#reload" && auth == hookAllow {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("replica 2 after reloads: channels=%v login=%v", ch, auth)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A reload never replaces a change made in place while it read the store.
func TestObserverLoginIndexReloadKeepsConcurrentChange(t *testing.T) {
	a, _ := newTestAuthService(t)
	ix, err := newObserverLoginIndex(a.st, nil)
	if err != nil {
		t.Fatal(err)
	}
	login := users.MQTTLogin{UserID: 7, Username: "obs_concurrent0", PasswordHash: strings.Repeat("ab", 32)}
	replaced, err := ix.reloadFrom(func() (*observerLoginIndex, error) {
		fresh, err := newObserverLoginIndex(a.st, nil) // read before the change below
		ix.setLogin(login)
		return fresh, err
	})
	if err != nil || replaced {
		t.Fatalf("reload during a change: replaced=%v err=%v", replaced, err)
	}
	if _, ok := ix.userIDFor(login.Username); !ok {
		t.Fatal("the change made during the reload was lost")
	}
	replaced, err = ix.reloadFrom(func() (*observerLoginIndex, error) { return newObserverLoginIndex(a.st, nil) })
	if err != nil || !replaced {
		t.Fatalf("quiet reload: replaced=%v err=%v", replaced, err)
	}
	if _, ok := ix.userIDFor(login.Username); ok {
		t.Fatal("a quiet reload kept a login the store does not have")
	}
	if _, err := ix.reloadFrom(func() (*observerLoginIndex, error) { return nil, errors.New("boom") }); err == nil {
		t.Fatal("a failed load was not reported")
	}
}

func TestPGUsersBackupIs501AndExportWorks(t *testing.T) {
	stores := pgReplicaStores(t, 1)
	a, fake := newTestAuthServiceOn(t, testUserMgmtSettings(t, "boss@example.org"), stores[0])
	a.set.backup = backupSettings{enabled: true, dir: filepath.Join(t.TempDir(), "backups"), keep: 7}
	srv := &Server{cfg: &Config{APIKey: testAPIKey}, perfStats: NewPerfStats(), auth: a}
	r := mux.NewRouter()
	srv.registerAuthRoutes(r)
	f := &authFixture{srv: srv, router: r, fake: fake, st: a.st}
	boss := f.registerAndActivate(t, "boss@example.org", "Boss", pw)

	w := f.do("GET", "/api/admin/users-backup", nil, as(boss))
	expectStatus(t, w, 501)
	if got := strings.TrimSpace(w.Body.String()); got != `{"error":"accounts are stored in PostgreSQL; back them up with pg_dump"}` {
		t.Fatalf("body = %s", got)
	}
	expectStatus(t, f.do("GET", "/api/account/export", nil, as(boss)), 200)

	a.maybeBackup(time.Now())
	if _, err := os.Stat(a.set.backup.dir); !os.IsNotExist(err) {
		t.Fatalf("scheduled backup ran on Postgres: %v", err)
	}
}

// fakeSharedLimits counts hits per key in memory, or fails.
type fakeSharedLimits struct {
	hits map[string]int
	err  error
}

func (f *fakeSharedLimits) RateLimitHit(_ context.Context, key string, _ time.Duration, max int, _ time.Time) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	f.hits[key]++
	return f.hits[key] <= max, nil
}

func TestSharedRateLimiterKeysAndFallback(t *testing.T) {
	logs := captureLogs(t)
	fs := &fakeSharedLimits{hits: map[string]int{}}
	l := newRateLimiter(2, time.Hour)
	now := time.Date(2026, 10, 7, 12, 59, 30, 0, time.UTC)
	l.now = func() time.Time { return now }
	l.share(fs, "login")
	l.take("ip:a")
	l.take("ip:a")
	ok, wait := l.take("ip:a")
	if ok || wait != 30*time.Second {
		t.Fatalf("third hit: ok=%v wait=%v; want refused for the 30 s left in the window", ok, wait)
	}
	if fs.hits["login:ip:a"] != 3 || len(fs.hits) != 1 {
		t.Fatalf("store keys = %v; want login:ip:a only", fs.hits)
	}
	if len(l.buckets) != 0 {
		t.Fatal("the in-memory bucket was used while the store answered")
	}
	// Store down: the in-memory bucket limits, and the failure is logged once a minute.
	fs.err = errors.New("connection refused")
	for i := 0; i < 2; i++ {
		if ok, _ := l.take("ip:b"); !ok {
			t.Fatalf("fallback hit %d refused", i+1)
		}
	}
	if ok, _ := l.take("ip:b"); ok {
		t.Fatal("the fallback bucket did not limit")
	}
	if c := strings.Count(logs.String(), "shared rate limit login"); c != 1 {
		t.Fatalf("store failure logged %d times; want 1:\n%s", c, logs.String())
	}
}

func TestFixedWindowLeft(t *testing.T) {
	at := time.Date(2026, 10, 7, 12, 14, 0, 500e6, time.UTC)
	if got := fixedWindowLeft(at, 15*time.Minute); got != 59*time.Second+500*time.Millisecond {
		t.Fatalf("left = %v", got)
	}
	if got := fixedWindowLeft(time.Date(2026, 10, 7, 12, 15, 0, 0, time.UTC), 15*time.Minute); got != 15*time.Minute {
		t.Fatalf("left at a window start = %v", got)
	}
}

// claimNotification on the SQLite store (same RateLimitHit SQL): one claim
// per key among claims less than notifyClaimOverlap apart, across a window
// boundary too.
func TestClaimNotification(t *testing.T) {
	a, _ := newTestAuthService(t)
	ctx := context.Background()
	claim := func(key string, at time.Time) bool {
		t.Helper()
		ok, err := claimNotification(ctx, a.st, key, at)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	edge := time.Date(2026, 10, 7, 14, 0, 0, 0, time.UTC) // a notifyClaimWindow boundary
	for _, c := range []struct {
		name   string
		first  time.Time
		second time.Time
	}{
		{"same window", edge.Add(-90 * time.Minute), edge.Add(-80 * time.Minute)},
		{"across the boundary", edge.Add(-time.Second), edge.Add(time.Second)},
		{"59 minutes apart", edge.Add(-30 * time.Minute), edge.Add(29 * time.Minute)},
		{"second first", edge.Add(10 * time.Minute), edge.Add(-40 * time.Minute)},
	} {
		key := "notify-claim:" + c.name
		if !claim(key, c.first) {
			t.Fatalf("%s: first claim refused", c.name)
		}
		if claim(key, c.second) {
			t.Fatalf("%s: second claim granted", c.name)
		}
	}
}

func TestNotifyClaimKey(t *testing.T) {
	c := notifyChange{Event: users.NotifyNodeOffline, Subject: evPkA, To: users.NotifyBad, At: time.Now()}
	k := users.NotifyKey{UserID: 3, Event: c.Event, Subject: c.Subject}
	t0 := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	prev := map[users.NotifyKey]users.NotifyState{k: {NotifyKey: k, State: users.NotifyGood, ChangedAt: t0}}
	key := notifyClaimKey(3, c, prev)
	later := c
	later.At = later.At.Add(time.Minute) // evaluation time is not part of the transition
	if notifyClaimKey(3, later, prev) != key {
		t.Fatal("the key depends on the evaluation time")
	}
	moved := map[users.NotifyKey]users.NotifyState{k: {NotifyKey: k, State: users.NotifyGood, ChangedAt: t0.Add(time.Hour)}}
	if notifyClaimKey(3, c, moved) == key || notifyClaimKey(4, c, prev) == key || notifyClaimKey(3, c, nil) == key {
		t.Fatal("different transitions share a key")
	}
}
