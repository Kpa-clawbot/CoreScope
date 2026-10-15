package main

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/meshcore-analyzer/users"
)

func TestBearerScopedRoutes(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	tok := f.deviceToken(t, "alice@example.org", pw, "Pixel").Token
	foreign := header("Origin", rxOrigin)

	w := f.do("GET", "/api/auth/me", nil, bearer(tok))
	expectStatus(t, w, http.StatusOK)
	if me := decode[meResponse](t, w); me.ID != alice.me.ID || me.CSRFToken != "" {
		t.Fatalf("me via bearer = %+v", me)
	}
	// A write from another origin without any CSRF header: allowed for a bearer.
	w = f.do("PUT", "/api/account/settings", settingsPutRequest{Doc: &settingsDoc{V: 1, Keys: map[string]string{"meshcore-theme": "dark"}}},
		bearer(tok), foreign)
	expectStatus(t, w, http.StatusOK)
	expectStatus(t, f.do("GET", "/api/account/settings", nil, bearer(tok)), http.StatusOK)

	for _, c := range []struct{ method, path string }{
		{"GET", "/api/account/sessions"},
		{"PATCH", "/api/account"},
		{"POST", "/api/account/password"},
		{"DELETE", "/api/account"},
		{"GET", "/api/admin/users"},
	} {
		if w := f.do(c.method, c.path, nil, bearer(tok), foreign); w.Code != http.StatusForbidden {
			t.Errorf("%s %s with a device token = %d, want 403", c.method, c.path, w.Code)
		}
	}
	if _, err := f.st.GetByID(alice.me.ID); err != nil {
		t.Fatalf("account touched by an out-of-scope request: %v", err)
	}
}

func TestBearerRejectsWebAndBadTokens(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	// alice.cookie.Value is a web session token; as a bearer it would skip CSRF.
	for _, tok := range []string{alice.cookie.Value, "nonsense", ""} {
		if w := f.do("GET", "/api/auth/me", nil, bearer(tok)); w.Code != http.StatusUnauthorized {
			t.Errorf("bearer %q = %d, want 401", tok, w.Code)
		}
	}
}

// A bearer header only counts without a session cookie: an auth proxy that
// adds its own Authorization: Bearer must not break cookie logins.
func TestBearerIgnoredWithCookie(t *testing.T) {
	f := newAuthFixture(t, "admin@example.org")
	admin := f.registerAndActivate(t, "admin@example.org", "Admin", pw)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	proxy := bearer("id-token-from-a-proxy")

	w := f.do("GET", "/api/auth/me", nil, as(alice), proxy)
	expectStatus(t, w, http.StatusOK)
	if me := decode[meResponse](t, w); me.ID != alice.me.ID || me.CSRFToken == "" {
		t.Fatalf("me with cookie + foreign bearer = %+v", me)
	}
	expectStatus(t, f.do("GET", "/api/account/sessions", nil, as(alice), proxy), http.StatusOK)
	expectStatus(t, f.do("GET", "/api/admin/users", nil, as(admin), proxy), http.StatusOK)

	// Writes take the cookie path, so the CSRF check applies.
	put := settingsPutRequest{Doc: &settingsDoc{V: 1, Keys: map[string]string{"meshcore-theme": "dark"}}}
	expectStatus(t, f.do("PUT", "/api/account/settings", put, as(alice), proxy), http.StatusOK)
	noCSRF := &client{cookie: alice.cookie}
	expectStatus(t, f.do("PUT", "/api/account/settings", put, as(noCSRF), proxy), http.StatusForbidden)

	// A device token next to a cookie is not used either: the cookie decides.
	tok := f.deviceToken(t, "alice@example.org", pw, "Pixel").Token
	expired := &client{cookie: &http.Cookie{Name: sessionCookieName, Value: "stale"}}
	expectStatus(t, f.do("GET", "/api/auth/me", nil, as(expired), bearer(tok)), http.StatusUnauthorized)

	// Logout ends the cookie session and leaves the device token alone.
	expectStatus(t, f.do("POST", "/api/auth/logout", nil, as(alice), bearer(tok)), http.StatusOK)
	expectStatus(t, f.do("GET", "/api/auth/me", nil, as(alice)), http.StatusUnauthorized)
	expectStatus(t, f.do("GET", "/api/auth/me", nil, bearer(tok)), http.StatusOK)
}

func TestBearerLogoutRevokesDevice(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	tok := f.deviceToken(t, "alice@example.org", pw, "Pixel").Token

	expectStatus(t, f.do("POST", "/api/auth/logout", nil, bearer(tok), header("Origin", rxOrigin)), http.StatusOK)
	if _, err := f.st.LookupSession(tok); !errors.Is(err, users.ErrNotFound) {
		t.Fatalf("device token survived logout: %v", err)
	}
	expectStatus(t, f.do("GET", "/api/auth/me", nil, bearer(tok)), http.StatusUnauthorized)
	expectStatus(t, f.do("GET", "/api/auth/me", nil, as(alice)), http.StatusOK)
	// The cookie logout keeps its origin check.
	expectStatus(t, f.do("POST", "/api/auth/logout", nil, as(alice), header("Origin", rxOrigin)), http.StatusForbidden)
}

func TestBearerSlidingExpiry(t *testing.T) {
	f := newAuthFixture(t)
	f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	f.st.SetClock(func() time.Time { return time.Now().Add(-80 * 24 * time.Hour) })
	tok := f.deviceToken(t, "alice@example.org", pw, "Pixel").Token
	f.st.SetClock(time.Now)

	expectStatus(t, f.do("GET", "/api/auth/me", nil, bearer(tok)), http.StatusOK)
	sess, err := f.st.LookupSession(tok)
	if err != nil || sess.ExpiresAt.Before(time.Now().Add(users.DeviceSessionTTL-time.Hour)) {
		t.Fatalf("device token not extended on use: %+v, %v", sess, err)
	}
}
