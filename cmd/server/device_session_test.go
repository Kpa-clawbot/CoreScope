package main

import (
	"net/http"
	"testing"
)

// LookupSession returns a session of any kind, so a device token sent as
// the session cookie must be refused by the cookie path.
func TestDeviceTokenIsNotACookieSession(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	raw, _, err := f.st.CreateDeviceSession(alice.me.ID, "Pixel", []string{"rx"}, "")
	if err != nil {
		t.Fatal(err)
	}
	forged := &client{cookie: &http.Cookie{Name: sessionCookieName, Value: raw}}
	for _, p := range []string{"/api/auth/me", "/api/account/sessions", "/api/account/settings"} {
		if w := f.do("GET", p, nil, as(forged)); w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s with a device token as cookie = %d, want 401", p, w.Code)
		}
	}
}

func TestSessionsListKindAndLabel(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	if _, _, err := f.st.CreateDeviceSession(alice.me.ID, "Pixel 8", []string{"rx"}, "CoreDriveRX/1.0"); err != nil {
		t.Fatal(err)
	}
	w := f.do("GET", "/api/account/sessions", nil, as(alice))
	expectStatus(t, w, http.StatusOK)
	byKind := map[string]sessionJSON{}
	for _, s := range decode[[]sessionJSON](t, w) {
		byKind[s.Kind] = s
	}
	if web, ok := byKind["web"]; !ok || web.Label != "" || !web.Current {
		t.Fatalf("web session = %+v (present %v)", web, ok)
	}
	if dev, ok := byKind["device"]; !ok || dev.Label != "Pixel 8" || dev.Current || dev.UserAgent != "CoreDriveRX/1.0" {
		t.Fatalf("device session = %+v (present %v)", dev, ok)
	}
}
