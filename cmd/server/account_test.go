package main

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/users"
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

// A token-store failure still answers 500, but only after the other
// sessions are gone: a thief's session must not outlive the change.
func TestAccountPasswordChangeTokenStoreFailureIs500(t *testing.T) {
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "pam@example.org", "Pam", pw)
	thief := f.login(t, "pam@example.org", pw)
	f.breakTable(t, "tokens")
	expectStatus(t, f.do("POST", "/api/account/password", passwordChangeRequest{CurrentPassword: pw, NewPassword: "new secret pass"}, as(c)), 500)
	expectStatus(t, f.do("GET", "/api/auth/me", nil, as(thief)), 401)
	expectStatus(t, f.do("GET", "/api/auth/me", nil, as(c)), 200)
}

// Same for reset: the email-change invalidation fails, all sessions are
// already ended.
func TestAuthResetTokenStoreFailureEndsSessionsFirst(t *testing.T) {
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "pia@example.org", "Pia", pw)
	thief := f.login(t, "pia@example.org", pw)
	_, reset := pendingLinks(t, f, c, "pia@example.org", "attacker@example.org")
	f.execDB(t, `CREATE FUNCTION down() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'token store down'; END $$; CREATE TRIGGER down BEFORE UPDATE ON tokens FOR EACH ROW WHEN (OLD.purpose='email_change') EXECUTE FUNCTION down()`)
	expectStatus(t, f.do("POST", "/api/auth/reset", resetRequest{Token: reset, Password: "a brand new secret"}), 500)
	expectStatus(t, f.do("GET", "/api/auth/me", nil, as(thief)), 401)
	expectStatus(t, f.do("GET", "/api/auth/me", nil, as(c)), 401)
}

func TestAccountEmailChangeTakenLooksLikeFree(t *testing.T) {
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "quin@example.org", "Quin", pw)
	f.registerAndActivate(t, "rae@example.org", "Rae", pw)
	free := f.do("POST", "/api/account/email", emailChangeRequest{NewEmail: "quin.new@example.org", CurrentPassword: pw}, as(c), fromIP("192.0.2.1"))
	before := len(f.fake.Sent())
	taken := f.do("POST", "/api/account/email", emailChangeRequest{NewEmail: "rae@example.org", CurrentPassword: pw}, as(c), fromIP("192.0.2.2"))
	if free.Code != 200 || free.Code != taken.Code || free.Body.String() != taken.Body.String() {
		t.Fatalf("responses differ: %d %q vs %d %q", free.Code, free.Body, taken.Code, taken.Body)
	}
	sent := f.fake.Sent()[before:]
	if len(sent) != 1 || sent[0].To != "quin@example.org" || !strings.Contains(sent[0].Text, "rae@example.org") {
		t.Fatalf("taken branch sent %+v, want only the notice to the old address", sent)
	}
}

func TestAccountEmailChangeMailFailureLeavesNoLink(t *testing.T) {
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "sam@example.org", "Sam", pw)
	f.fake.SetSendErr(errors.New("brevo down"))
	expectStatus(t, f.do("POST", "/api/account/email", emailChangeRequest{NewEmail: "sam.new@example.org", CurrentPassword: pw}, as(c)), 503)
	if n := f.unusedTokens(t, c.me.ID, users.PurposeEmailChange); n != 0 {
		t.Fatalf("%d usable email-change links after a failed mail", n)
	}
}

// Taken branch: the notice to the old address is the only mail; when it
// fails the answer is 503, the address stays and no earlier link survives.
func TestAccountEmailChangeTakenNoticeFailureIs503(t *testing.T) {
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "sol@example.org", "Sol", pw)
	f.registerAndActivate(t, "tam@example.org", "Tam", pw)
	expectStatus(t, f.do("POST", "/api/account/email", emailChangeRequest{NewEmail: "sol.new@example.org", CurrentPassword: pw}, as(c), fromIP("192.0.2.1")), 200)
	f.fake.SetSendErr(errors.New("brevo down"))
	w := f.do("POST", "/api/account/email", emailChangeRequest{NewEmail: "tam@example.org", CurrentPassword: pw}, as(c), fromIP("192.0.2.2"))
	expectStatus(t, w, 503)
	if !strings.Contains(w.Body.String(), msgMailFailed) {
		t.Fatalf("body = %s", w.Body.String())
	}
	if got, _ := f.st.GetByID(c.me.ID); got.Email != "sol@example.org" {
		t.Fatalf("address changed to %q", got.Email)
	}
	if n := f.unusedTokens(t, c.me.ID, users.PurposeEmailChange); n != 0 {
		t.Fatalf("%d usable email-change links after a failed notice", n)
	}
}

func TestAccountEmailChangeRateLimitPerUser(t *testing.T) {
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "tia@example.org", "Tia", pw)
	change := func(i int) *httptest.ResponseRecorder {
		return f.do("POST", "/api/account/email", emailChangeRequest{NewEmail: fmt.Sprintf("tia%d@example.org", i), CurrentPassword: pw},
			as(c), fromIP(fmt.Sprintf("198.51.100.%d", i+1)))
	}
	for i := 0; i < 5; i++ {
		expectStatus(t, change(i), 200)
	}
	expectStatus(t, change(5), 429)
}

func TestAccountEmailChangeRateLimitPerAddress(t *testing.T) {
	f := newAuthFixture(t)
	ip := 0
	change := func(c *client) *httptest.ResponseRecorder {
		ip++
		return f.do("POST", "/api/account/email", emailChangeRequest{NewEmail: "target@example.org", CurrentPassword: pw},
			as(c), fromIP(fmt.Sprintf("198.51.100.%d", ip)))
	}
	a := f.registerAndActivate(t, "uli@example.org", "Uli", pw)
	b := f.registerAndActivate(t, "val@example.org", "Val", pw)
	d := f.registerAndActivate(t, "wes@example.org", "Wes", pw)
	for i := 0; i < 3; i++ {
		expectStatus(t, change(a), 200)
	}
	for i := 0; i < 2; i++ {
		expectStatus(t, change(b), 200)
	}
	expectStatus(t, change(d), 429)
}

func TestConfirmEmailForNonActiveUserIs410(t *testing.T) {
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "xan@example.org", "Xan", pw)
	expectStatus(t, f.do("POST", "/api/account/email", emailChangeRequest{NewEmail: "xan.new@example.org", CurrentPassword: pw}, as(c)), 200)
	sent := f.fake.Sent()
	tok, _ := url.QueryUnescape(tokenRE.FindStringSubmatch(sent[len(sent)-2].Text)[1])
	if err := f.st.SetStatus(c.me.ID, users.StatusDisabled); err != nil { // e.g. a direct DB edit; disable itself burns the link
		t.Fatal(err)
	}
	w := f.do("POST", "/api/account/confirm-email", tokenRequest{Token: tok})
	expectStatus(t, w, 410)
	if !strings.Contains(w.Body.String(), "this link is invalid or was already used") {
		t.Fatalf("body = %s", w.Body.String())
	}
	if got, _ := f.st.GetByID(c.me.ID); got.Email != "xan@example.org" {
		t.Fatalf("address changed for a disabled user: %q", got.Email)
	}
}
