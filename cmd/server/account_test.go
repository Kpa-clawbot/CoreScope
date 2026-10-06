package main

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestAccountProfileAndCSRF(t *testing.T) {
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "gil@example.org", "Gil", pw)
	noCSRF := &client{cookie: c.cookie}
	expectStatus(t, f.do("PATCH", "/api/account", profileRequest{DisplayName: "Gilbert"}, as(noCSRF)), 403)
	expectStatus(t, f.do("PATCH", "/api/account", profileRequest{DisplayName: "Gilbert"}, as(c), header("Origin", "https://evil.example")), 403)
	w := f.do("PATCH", "/api/account", profileRequest{DisplayName: "Gilbert"}, as(c))
	expectStatus(t, w, 200)
	if decode[meResponse](t, w).DisplayName != "Gilbert" {
		t.Fatal("display name not changed")
	}
	expectStatus(t, f.do("PATCH", "/api/account", profileRequest{DisplayName: "G"}, as(c)), 400)
}

func TestAccountPasswordChangeKeepsCurrentSession(t *testing.T) {
	f := newAuthFixture(t)
	a := f.registerAndActivate(t, "hal@example.org", "Hal", pw)
	b := f.login(t, "hal@example.org", pw)
	expectStatus(t, f.do("POST", "/api/account/password", passwordChangeRequest{CurrentPassword: "wrong one!!", NewPassword: "new secret pass"}, as(a)), 403)
	expectStatus(t, f.do("POST", "/api/account/password", passwordChangeRequest{CurrentPassword: pw, NewPassword: "new secret pass"}, as(a)), 200)
	expectStatus(t, f.do("GET", "/api/auth/me", nil, as(a)), 200)
	expectStatus(t, f.do("GET", "/api/auth/me", nil, as(b)), 401)
}

func TestAccountEmailChange(t *testing.T) {
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "ivy@example.org", "Ivy", pw)
	expectStatus(t, f.do("POST", "/api/account/email", emailChangeRequest{NewEmail: "ivy.new@example.org", CurrentPassword: pw}, as(c)), 200)
	sent := f.fake.Sent()
	confirm, notice := sent[len(sent)-2], sent[len(sent)-1]
	if confirm.To != "ivy.new@example.org" || notice.To != "ivy@example.org" || !strings.Contains(notice.Text, "ivy.new@example.org") {
		t.Fatalf("confirm=%+v notice=%+v", confirm, notice)
	}
	tok := tokenRE.FindStringSubmatch(confirm.Text)[1]
	expectStatus(t, f.do("POST", "/api/account/confirm-email", tokenRequest{Token: tok}, header("Origin", "")), 403) // no Origin
	expectStatus(t, f.do("POST", "/api/account/confirm-email", tokenRequest{Token: tok}), 200)
	f.login(t, "ivy.new@example.org", pw)
	expectStatus(t, f.do("POST", "/api/auth/login", loginRequest{Email: "ivy@example.org", Password: pw}), 401)
}

func TestAccountSessionsListAndRevoke(t *testing.T) {
	f := newAuthFixture(t)
	a := f.registerAndActivate(t, "jo@example.org", "Jo", pw)
	b := f.login(t, "jo@example.org", pw)
	w := f.do("GET", "/api/account/sessions", nil, as(a))
	expectStatus(t, w, 200)
	list := decode[[]sessionJSON](t, w)
	if len(list) != 2 {
		t.Fatalf("sessions = %+v", list)
	}
	var other int64
	for _, s := range list {
		if !s.Current {
			other = s.ID
		}
	}
	expectStatus(t, f.do("DELETE", fmt.Sprintf("/api/account/sessions/%d", other), nil, as(a)), 200)
	expectStatus(t, f.do("GET", "/api/auth/me", nil, as(b)), 401)
	expectStatus(t, f.do("DELETE", "/api/account/sessions/999999", nil, as(a)), 404)
}

func TestAccountDelete(t *testing.T) {
	f := newAuthFixture(t, "boss@example.org")
	boss := f.registerAndActivate(t, "boss@example.org", "Boss", pw)
	expectStatus(t, f.do("DELETE", "/api/account", passwordConfirmRequest{CurrentPassword: pw}, as(boss)), 409) // last admin
	u := f.registerAndActivate(t, "kim@example.org", "Kim", pw)
	expectStatus(t, f.do("DELETE", "/api/account", passwordConfirmRequest{CurrentPassword: "wrong one!!"}, as(u)), 403)
	w := f.do("DELETE", "/api/account", passwordConfirmRequest{CurrentPassword: pw}, as(u))
	expectStatus(t, w, 200)
	if ck := w.Result().Cookies(); len(ck) == 0 || ck[0].MaxAge >= 0 {
		t.Fatalf("cookie not cleared: %+v", ck)
	}
	expectStatus(t, f.do("POST", "/api/auth/login", loginRequest{Email: "kim@example.org", Password: pw}), 401)
}

// pendingLinks requests an email change to newEmail and a password reset for
// c (whose address is email) and returns both unused links.
func pendingLinks(t *testing.T, f *authFixture, c *client, email, newEmail string) (confirm, reset string) {
	t.Helper()
	expectStatus(t, f.do("POST", "/api/account/email", emailChangeRequest{NewEmail: newEmail, CurrentPassword: pw}, as(c), fromIP("192.0.2.1")), 200)
	sent := f.fake.Sent()
	confirm, _ = url.QueryUnescape(tokenRE.FindStringSubmatch(sent[len(sent)-2].Text)[1])
	expectStatus(t, f.do("POST", "/api/auth/forgot", emailRequest{Email: email}, fromIP("192.0.2.2")), 200)
	return confirm, f.lastToken(t)
}

func TestAuthResetKillsPendingEmailChange(t *testing.T) {
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "nia@example.org", "Nia", pw)
	confirm, reset := pendingLinks(t, f, c, "nia@example.org", "attacker@example.org")
	expectStatus(t, f.do("POST", "/api/auth/reset", resetRequest{Token: reset, Password: "a brand new secret"}), 200)
	expectStatus(t, f.do("POST", "/api/account/confirm-email", tokenRequest{Token: confirm}), 410)
	f.login(t, "nia@example.org", "a brand new secret")
}

func TestAccountPasswordChangeKillsPendingLinks(t *testing.T) {
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "oli@example.org", "Oli", pw)
	confirm, reset := pendingLinks(t, f, c, "oli@example.org", "attacker@example.org")
	expectStatus(t, f.do("POST", "/api/account/password", passwordChangeRequest{CurrentPassword: pw, NewPassword: "new secret pass"}, as(c)), 200)
	expectStatus(t, f.do("POST", "/api/account/confirm-email", tokenRequest{Token: confirm}), 410)
	expectStatus(t, f.do("POST", "/api/auth/reset", resetRequest{Token: reset, Password: "another new secret"}), 410)
	f.login(t, "oli@example.org", "new secret pass")
}

func TestAccountPasswordChangeTokenStoreFailureIs500(t *testing.T) {
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "pam@example.org", "Pam", pw)
	f.breakTable(t, "tokens")
	expectStatus(t, f.do("POST", "/api/account/password", passwordChangeRequest{CurrentPassword: pw, NewPassword: "new secret pass"}, as(c)), 500)
}
