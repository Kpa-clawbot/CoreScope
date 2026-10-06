package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/mailer"
	"github.com/meshcore-analyzer/users"
)

const (
	testBase   = "https://scope.example.org"
	testAPIKey = "test-secret-key-strong-enough"
	testHook   = "webhook-secret-0123456789"
)

type authFixture struct {
	srv    *Server
	router *mux.Router
	fake   *mailer.Fake
	st     *users.Store
}

// client is a browser: its session cookie and CSRF token.
type client struct {
	cookie *http.Cookie
	csrf   string
	me     meResponse
}

func newTestAuthService(t *testing.T, adminEmails ...string) (*authService, *mailer.Fake) {
	t.Helper()
	set := &userMgmtSettings{
		dbPath: filepath.Join(t.TempDir(), "users.db"), adminEmails: map[string]bool{},
		sessionTTL: 30 * 24 * time.Hour, provider: "fake",
		fromEmail: "noreply@example.org", fromName: "CoreScope", webhookSecret: testHook,
	}
	set.baseURL, _ = url.Parse(testBase)
	set.secureCookie = true
	for _, e := range adminEmails {
		set.adminEmails[e] = true
	}
	st, err := users.Open(set.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fake := &mailer.Fake{}
	return newAuthService(set, st, fake), fake
}

// newAuthFixture builds a Server with auth on and only the auth routes.
func newAuthFixture(t *testing.T, adminEmails ...string) *authFixture {
	t.Helper()
	a, fake := newTestAuthService(t, adminEmails...)
	srv := &Server{cfg: &Config{APIKey: testAPIKey}, perfStats: NewPerfStats(), auth: a}
	r := mux.NewRouter()
	srv.registerAuthRoutes(r)
	return &authFixture{srv: srv, router: r, fake: fake, st: a.st}
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

func (f *authFixture) do(method, path string, body any, mods ...reqMod) *httptest.ResponseRecorder {
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
	db, err := sql.Open("sqlite", f.srv.auth.set.dbPath)
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
	db, err := sql.Open("sqlite", f.srv.auth.set.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM tokens WHERE user_id = ? AND purpose = ? AND used_at IS NULL`, uid, string(p)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
