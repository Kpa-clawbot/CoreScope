package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/mailer"
	"github.com/meshcore-analyzer/pgutil/pgtest"
	"github.com/meshcore-analyzer/users"
)

const (
	testBase   = "https://scope.example.org"
	testAPIKey = "test-secret-key-strong-enough"
	testHook   = "webhook-secret-0123456789"
)

type authFixture struct {
	ownerURL string
	srv      *Server
	router   *mux.Router
	fake     *mailer.Fake
	st       *users.Store
}

// client is a browser: its session cookie and CSRF token.
type client struct {
	cookie *http.Cookie
	csrf   string
	me     meResponse
}

func newTestAuthService(t *testing.T, adminEmails ...string) (*authService, *mailer.Fake) {
	a, fake, _ := newTestAuthServiceWithURL(t, postgresTestDSN(t), adminEmails...)
	return a, fake
}

func newTestBackupAuthService(t *testing.T, adminEmails ...string) (*authService, *mailer.Fake) {
	a, fake, _ := newTestAuthServiceWithURL(t, testDatabaseDSN(t), adminEmails...)
	return a, fake
}

func newTestAuthServiceWithURL(t *testing.T, ownerURL string, adminEmails ...string) (*authService, *mailer.Fake, string) {
	t.Helper()
	runtimeURL := ownerURL
	if testBackend(t) == dbconfig.Postgres {
		owner, err := openFixtureSQL(ownerURL)
		if err != nil {
			t.Fatal(err)
		}
		defer owner.Close()
		if err := users.Apply(owner); err != nil {
			t.Fatal(err)
		}
		runtimeURL = pgtest.Writer(t, ownerURL)
		u, _ := url.Parse(runtimeURL)
		if _, err := owner.Exec(`REVOKE INSERT,UPDATE,DELETE ON corescope_schema,schema_version FROM "` + u.User.Username() + `"`); err != nil {
			t.Fatal(err)
		}
	}
	set := &userMgmtSettings{
		databaseURL: runtimeURL, adminEmails: map[string]bool{},
		sessionTTL: 30 * 24 * time.Hour, provider: "fake",
		fromEmail: "noreply@example.org", fromName: "CoreScope", webhookSecret: testHook,
	}
	set.baseURL, _ = url.Parse(testBase)
	set.secureCookie = true
	for _, e := range adminEmails {
		set.adminEmails[e] = true
	}
	st, err := users.Open(set.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	fake := &mailer.Fake{}
	a := newAuthService(set, st, fake)
	t.Cleanup(func() {
		a.waitAudits()
		st.Close()
	})
	return a, fake, ownerURL
}

// newAuthFixture builds a Server with auth on and only the auth routes.
func newAuthFixture(t *testing.T, adminEmails ...string) *authFixture {
	return newAuthFixtureWithURL(t, postgresTestDSN(t), adminEmails...)
}

func newBackupAuthFixture(t *testing.T, adminEmails ...string) *authFixture {
	return newAuthFixtureWithURL(t, testDatabaseDSN(t), adminEmails...)
}

func newAuthFixtureWithURL(t *testing.T, databaseURL string, adminEmails ...string) *authFixture {
	t.Helper()
	a, fake, ownerURL := newTestAuthServiceWithURL(t, databaseURL, adminEmails...)
	srv := &Server{cfg: &Config{APIKey: testAPIKey}, perfStats: NewPerfStats(), auth: a}
	r := mux.NewRouter()
	srv.registerAuthRoutes(r)
	return &authFixture{srv: srv, router: r, fake: fake, st: a.st, ownerURL: ownerURL}
}

type reqMod func(*http.Request)

func as(c *client) reqMod {
	return func(r *http.Request) {
		if c.cookie != nil {
			r.AddCookie(c.cookie)
		}
		if c.csrf != "" {
			r.Header.Set(csrfHeader, c.csrf)
		}
	}
}

func header(k, v string) reqMod { return func(r *http.Request) { r.Header.Set(k, v) } }
func fromIP(ip string) reqMod   { return func(r *http.Request) { r.RemoteAddr = ip + ":5555" } }

// do serves one request and then waits for its background audit writes,
// so tests see audit rows in request order.
func (f *authFixture) do(method, path string, body any, mods ...reqMod) *httptest.ResponseRecorder {
	w := f.serve(method, path, body, mods...)
	f.srv.auth.waitAudits()
	return w
}

// serve serves one request and returns without waiting for audit writes.
func (f *authFixture) serve(method, path string, body any, mods ...reqMod) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rd)
	req.RemoteAddr = "203.0.113.10:5555"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if !isSafeMethod(method) {
		req.Header.Set("Origin", testBase)
	}
	for _, m := range mods {
		m(req)
	}
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %T from %q: %v", v, w.Body.String(), err)
	}
	return v
}

func expectStatus(t *testing.T, w *httptest.ResponseRecorder, code int) {
	t.Helper()
	if w.Code != code {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, code, w.Body.String())
	}
}

var tokenRE = regexp.MustCompile(`token=([A-Za-z0-9_%\-]+)`)

// lastToken extracts the token from the newest fake mail's link.
func (f *authFixture) lastToken(t *testing.T) string {
	t.Helper()
	m, _, ok := f.fake.Last()
	if !ok {
		t.Fatal("no mail was sent")
	}
	sm := tokenRE.FindStringSubmatch(m.Text)
	if sm == nil {
		t.Fatalf("no token in mail text: %q", m.Text)
	}
	tok, _ := url.QueryUnescape(sm[1])
	return tok
}

func sessionFrom(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == sessionCookieName && c.Value != "" {
			return c
		}
	}
	t.Fatalf("no %s cookie in response", sessionCookieName)
	return nil
}

// registerAndActivate runs the link flow and returns the logged-in client.
func (f *authFixture) registerAndActivate(t *testing.T, email, name, password string) *client {
	t.Helper()
	w := f.do("POST", "/api/auth/register", registerRequest{Email: email, DisplayName: name, Password: password})
	expectStatus(t, w, 200)
	w = f.do("POST", "/api/auth/activate", activateRequest{Token: f.lastToken(t), Password: password})
	expectStatus(t, w, 200)
	me := decode[meResponse](t, w)
	return &client{cookie: sessionFrom(t, w), csrf: me.CSRFToken, me: me}
}

func (f *authFixture) login(t *testing.T, email, password string) *client {
	t.Helper()
	w := f.do("POST", "/api/auth/login", loginRequest{Email: email, Password: password})
	expectStatus(t, w, 200)
	me := decode[meResponse](t, w)
	return &client{cookie: sessionFrom(t, w), csrf: me.CSRFToken, me: me}
}

// breakTable renames a users.db table behind the store's back, so the next
// store call that touches it fails with a DB error (not ErrNotFound).
func (f *authFixture) breakTable(t *testing.T, table string) {
	t.Helper()
	db, err := openFixtureSQL(f.ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`ALTER TABLE ` + table + ` RENAME TO ` + table + `_broken`); err != nil {
		t.Fatal(err)
	}
}

// unusedTokens counts uid's outstanding links of purpose p, read straight
// from users.db (the raw tokens are not observable when no mail left).
func (f *authFixture) unusedTokens(t *testing.T, uid int64, p users.Purpose) int {
	t.Helper()
	db, err := openFixtureSQL(f.ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tokens WHERE user_id = $1 AND purpose = $2 AND used_at IS NULL`, uid, string(p)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// execDB runs raw SQL on users.db behind the store's back (triggers that
// simulate a concurrent writer or a failing statement).
func (f *authFixture) execDB(t *testing.T, stmt string) {
	t.Helper()
	db, err := openFixtureSQL(f.ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(stmt); err != nil {
		t.Fatal(err)
	}
}
