package main

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/users"
)

// rxOrigin is a CoreDrive RX deployment on another origin than testBase.
const rxOrigin = "https://rx.example.net"

func bearer(tok string) reqMod { return header("Authorization", "Bearer "+tok) }

// deviceToken logs in like CoreDrive RX: from a foreign origin, no cookie.
func (f *authFixture) deviceToken(t *testing.T, email, password, name string) deviceTokenResponse {
	t.Helper()
	w := f.do("POST", "/api/auth/device-token", deviceTokenRequest{Email: email, Password: password, DeviceName: name},
		header("Origin", rxOrigin))
	expectStatus(t, w, http.StatusOK)
	return decode[deviceTokenResponse](t, w)
}

func TestDeviceTokenIssue(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	got := f.deviceToken(t, "alice@example.org", pw, "  Pixel\x07 8  ")
	if got.Token == "" || got.User.ID != alice.me.ID || got.User.DisplayName != "Alice" {
		t.Fatalf("device-token response = %+v", got)
	}
	exp, err := time.Parse(time.RFC3339, got.ExpiresAt)
	if err != nil || exp.Before(time.Now().Add(users.DeviceSessionTTL-time.Hour)) {
		t.Fatalf("expiresAt = %q, %v", got.ExpiresAt, err)
	}
	sess, err := f.st.LookupSession(got.Token)
	if err != nil || sess.Kind != users.SessionKindDevice || sess.Label != "Pixel 8" ||
		!sess.HasScope(deviceScopeRX) || sess.HasScope("admin") {
		t.Fatalf("stored device session = %+v, %v", sess, err)
	}
	if w := f.serve("GET", "/api/auth/device-token", nil); w.Code == http.StatusOK {
		t.Fatal("GET answered 200")
	}
	entries, err := f.st.AuditFor(alice.me.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Action == "user.login" && e.Detail["via"] == "device" && e.Detail["device"] == "Pixel 8" {
			return
		}
	}
	t.Fatalf("no device login audit row in %+v", entries)
}

func TestDeviceTokenFailuresMatchLogin(t *testing.T) {
	f := newAuthFixture(t)
	alice := f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	for _, req := range []deviceTokenRequest{
		{Email: "alice@example.org", Password: "wrong password here", DeviceName: "x"},
		{Email: "nobody@example.org", Password: pw, DeviceName: "x"},
		{Email: "not an address", Password: pw, DeviceName: "x"},
	} {
		w := f.do("POST", "/api/auth/device-token", req)
		expectStatus(t, w, http.StatusUnauthorized)
		if !strings.Contains(w.Body.String(), msgBadLogin) {
			t.Fatalf("%s: body %s, want the login message", req.Email, w.Body.String())
		}
	}
	entries, _ := f.st.AuditFor(alice.me.ID, 20)
	failed := 0
	for _, e := range entries {
		if e.Action == "user.login.failed" && e.Detail["reason"] == "wrong_password" {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("failed-login audit rows = %d, want 1: %+v", failed, entries)
	}
	list, _ := f.st.ListSessions(alice.me.ID)
	for _, s := range list {
		if s.Kind == users.SessionKindDevice {
			t.Fatalf("a refused login created a device session: %+v", s)
		}
	}
}

// Device-token attempts and browser logins draw from the same buckets, so
// the endpoint is no way around the login rate limit.
func TestDeviceTokenSharesLoginRateLimit(t *testing.T) {
	f := newAuthFixture(t)
	f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	for i := 0; i < 10; i++ {
		f.do("POST", "/api/auth/login", loginRequest{Email: "alice@example.org", Password: "wrong password here"})
	}
	w := f.do("POST", "/api/auth/device-token", deviceTokenRequest{Email: "alice@example.org", Password: pw, DeviceName: "x"})
	expectStatus(t, w, http.StatusTooManyRequests)
	if w.Header().Get("Retry-After") == "" {
		t.Fatal("429 without Retry-After")
	}
}
