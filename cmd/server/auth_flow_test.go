package main

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/users"
)

const pw = "correct horse battery"

func TestAuthRegisterActivateLoginLogout(t *testing.T) {
	f := newAuthFixture(t)
	w := f.do("POST", "/api/auth/register", registerRequest{Email: "Alice@Example.org", DisplayName: "Alice", Password: pw})
	expectStatus(t, w, 200)
	if m, _, _ := f.fake.Last(); m.To != "alice@example.org" || !strings.Contains(m.Subject, "Activate") {
		t.Fatalf("activation mail = %+v", m)
	}
	// Pending accounts cannot log in.
	expectStatus(t, f.do("POST", "/api/auth/login", loginRequest{Email: "alice@example.org", Password: pw}), 401)

	w = f.do("POST", "/api/auth/activate", tokenRequest{Token: f.lastToken(t)})
	expectStatus(t, w, 200)
	me := decode[meResponse](t, w)
	ck := sessionFrom(t, w)
	if me.Role != users.RoleUser || me.CSRFToken == "" || !ck.HttpOnly || !ck.Secure {
		t.Fatalf("activate: me=%+v cookie=%+v", me, ck)
	}
	c := &client{cookie: ck, csrf: me.CSRFToken}
	expectStatus(t, f.do("GET", "/api/auth/me", nil, as(c)), 200)
	expectStatus(t, f.do("POST", "/api/auth/logout", nil, as(c)), 200)
	expectStatus(t, f.do("GET", "/api/auth/me", nil, as(c)), 401)
	f.login(t, "alice@example.org", pw)
}

func TestAuthRegisterIsEnumerationSafe(t *testing.T) {
	f := newAuthFixture(t)
	f.registerAndActivate(t, "bob@example.org", "Bob", pw)
	first := f.do("POST", "/api/auth/register", registerRequest{Email: "carol@example.org", DisplayName: "Carol", Password: pw})
	again := f.do("POST", "/api/auth/register", registerRequest{Email: "bob@example.org", DisplayName: "Bobby", Password: pw})
	if first.Code != again.Code || first.Body.String() != again.Body.String() {
		t.Fatalf("responses differ: %d %q vs %d %q", first.Code, first.Body, again.Code, again.Body)
	}
	if m, _, _ := f.fake.Last(); m.To != "bob@example.org" || !strings.Contains(m.Subject, "Registration attempt") {
		t.Fatalf("existing-account notice = %+v", m)
	}
	// Re-registering a still-pending address re-sends the activation link.
	f.do("POST", "/api/auth/register", registerRequest{Email: "carol@example.org", DisplayName: "Carol", Password: pw})
	if m, _, _ := f.fake.Last(); m.To != "carol@example.org" || !strings.Contains(m.Subject, "Activate") {
		t.Fatalf("pending re-register mail = %+v", m)
	}
}

func TestAuthValidationErrors(t *testing.T) {
	f := newAuthFixture(t)
	w := f.do("POST", "/api/auth/register", registerRequest{Email: "x@example.org", DisplayName: "X", Password: pw})
	expectStatus(t, w, 400)
	w = f.do("POST", "/api/auth/register", registerRequest{Email: "x@example.org", DisplayName: "Xx", Password: "short"})
	expectStatus(t, w, 400)
	w = f.do("POST", "/api/auth/register", map[string]string{"email": "x@example.org", "bogus": "1"})
	expectStatus(t, w, 400)
}

func TestAuthActivateAdminFromConfig(t *testing.T) {
	f := newAuthFixture(t, "boss@example.org")
	c := f.registerAndActivate(t, "boss@example.org", "Boss", pw)
	if c.me.Role != users.RoleAdmin {
		t.Fatalf("config admin activated as %q", c.me.Role)
	}
	// Activation links are single-use.
	expectStatus(t, f.do("POST", "/api/auth/activate", tokenRequest{Token: "bogus"}), 410)
}

func TestAuthLoginGenericFailure(t *testing.T) {
	f := newAuthFixture(t)
	f.registerAndActivate(t, "dave@example.org", "Dave", pw)
	unknown := f.do("POST", "/api/auth/login", loginRequest{Email: "nobody@example.org", Password: pw})
	wrong := f.do("POST", "/api/auth/login", loginRequest{Email: "dave@example.org", Password: "wrong password!"})
	if unknown.Code != 401 || wrong.Code != 401 || unknown.Body.String() != wrong.Body.String() {
		t.Fatalf("unknown=%d %q wrong=%d %q", unknown.Code, unknown.Body, wrong.Code, wrong.Body)
	}
}

func TestAuthConfigAdminRoleRestoredOnLogin(t *testing.T) {
	f := newAuthFixture(t, "boss@example.org")
	c := f.registerAndActivate(t, "boss@example.org", "Boss", pw)
	f.st.SetRole(c.me.ID, users.RoleUser) // e.g. demoted by direct DB edit
	if again := f.login(t, "boss@example.org", pw); again.me.Role != users.RoleAdmin {
		t.Fatalf("config admin logged in as %q", again.me.Role)
	}
}

func TestAuthForgotAndReset(t *testing.T) {
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "erin@example.org", "Erin", pw)
	sent := len(f.fake.Sent())
	expectStatus(t, f.do("POST", "/api/auth/forgot", emailRequest{Email: "nobody@example.org"}), 200)
	if len(f.fake.Sent()) != sent {
		t.Fatal("forgot for an unknown address sent mail")
	}
	expectStatus(t, f.do("POST", "/api/auth/forgot", emailRequest{Email: "erin@example.org"}), 200)
	tok := f.lastToken(t)
	expectStatus(t, f.do("POST", "/api/auth/reset", resetRequest{Token: tok, Password: "a brand new secret"}), 200)
	expectStatus(t, f.do("GET", "/api/auth/me", nil, as(c)), 401) // all sessions ended
	f.login(t, "erin@example.org", "a brand new secret")
	expectStatus(t, f.do("POST", "/api/auth/reset", resetRequest{Token: tok, Password: "another new secret"}), 410)
}

func TestAuthForgotIsEnumerationSafe(t *testing.T) {
	f := newAuthFixture(t)
	f.registerAndActivate(t, "gina@example.org", "Gina", pw)
	unknown := f.do("POST", "/api/auth/forgot", emailRequest{Email: "nobody@example.org"})
	known := f.do("POST", "/api/auth/forgot", emailRequest{Email: "gina@example.org"})
	if unknown.Code != known.Code || unknown.Body.String() != known.Body.String() {
		t.Fatalf("responses differ: %d %q vs %d %q", unknown.Code, unknown.Body, known.Code, known.Body)
	}
}

func TestAuthWrongCSRFTokenRejected(t *testing.T) {
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "hank@example.org", "Hank", pw)
	f.router.HandleFunc("/api/test/mutate", f.srv.withUser(func(w http.ResponseWriter, r *http.Request, u *users.User, sess *users.Session) {
		writeJSON(w, okResponse{OK: true})
	})).Methods("POST")
	expectStatus(t, f.do("POST", "/api/test/mutate", nil, as(c)), 200)
	expectStatus(t, f.do("POST", "/api/test/mutate", nil, as(c), header(csrfHeader, "not-the-token")), 403)
}

func TestAuthLoginRateLimit(t *testing.T) {
	f := newAuthFixture(t)
	for i := 0; i < 10; i++ {
		expectStatus(t, f.do("POST", "/api/auth/login", loginRequest{Email: "x@example.org", Password: "nope nope nope"}), 401)
	}
	w := f.do("POST", "/api/auth/login", loginRequest{Email: "x@example.org", Password: "nope nope nope"})
	expectStatus(t, w, 429)
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After")
	}
	// Per-address bucket: another IP is still limited for the same address.
	expectStatus(t, f.do("POST", "/api/auth/login", loginRequest{Email: "x@example.org", Password: "nope"}, fromIP("198.51.100.7")), 429)
}

func TestAuthOriginRequiredForLogin(t *testing.T) {
	f := newAuthFixture(t)
	w := f.do("POST", "/api/auth/login", loginRequest{Email: "x@example.org", Password: pw}, header("Origin", "https://evil.example"))
	expectStatus(t, w, 403)
}

func TestAuthMailFailureRollsBackRegistration(t *testing.T) {
	f := newAuthFixture(t)
	f.fake.SetSendErr(errors.New("brevo down"))
	expectStatus(t, f.do("POST", "/api/auth/register", registerRequest{Email: "fay@example.org", DisplayName: "Fay", Password: pw}), 503)
	if _, err := f.st.GetByEmail("fay@example.org"); !errors.Is(err, users.ErrNotFound) {
		t.Fatalf("user kept after failed mail: %v", err)
	}
	f.fake.SetSendErr(nil)
	expectStatus(t, f.do("POST", "/api/auth/register", registerRequest{Email: "fay@example.org", DisplayName: "Fay", Password: pw}), 200)
}
