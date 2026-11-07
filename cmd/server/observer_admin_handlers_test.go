package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/users"
)

const (
	oadmList = "/api/admin/observer-logins"
	oadmPK2  = "1111111111111111111111111111111111111111111111111111111111111111"
)

type oadmFixture struct {
	*hookFixture
	admin *client
}

func newOadmFixture(t *testing.T) *oadmFixture {
	t.Helper()
	f := newHookFixture(t, "boss@example.org")
	f.srv.auth.sealbox = mustSecretBox(t)
	admin := f.registerAndActivate(t, "boss@example.org", "Boss", "correct-horse-battery")
	return &oadmFixture{hookFixture: f, admin: admin}
}

func mustSecretBox(t *testing.T) *users.SecretBox {
	t.Helper()
	b, err := users.NewSecretBox(oaKey)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (f *oadmFixture) revokePath(uid int64) string {
	return fmt.Sprintf("/api/admin/observer-logins/%d/revoke", uid)
}
func (f *oadmFixture) reissuePath(uid int64) string {
	return fmt.Sprintf("/api/admin/observer-logins/%d/reissue", uid)
}

func TestObserverAdmin_NonAdmin403(t *testing.T) {
	f := newOadmFixture(t)
	uid := f.addLogin(t, "a@example.org", "obs_aaaaaaaaaaaa", "pw-a")
	user := f.login(t, "a@example.org", "correct-horse-battery")
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"GET", oadmList, nil},
		{"POST", f.revokePath(uid), nil},
		{"POST", f.reissuePath(uid), nil},
		{"PUT", "/api/admin/observer-bindings/" + hkPK, observerBindingRequest{}},
	} {
		expectStatus(t, f.do(c.method, c.path, c.body, as(user)), 403)
		expectStatus(t, f.do(c.method, c.path, c.body), 401)
	}
}

func TestObserverAdmin_ListShowsLoginsAndBindings(t *testing.T) {
	f := newOadmFixture(t)
	uid := f.addLogin(t, "a@example.org", "obs_aaaaaaaaaaaa", "pw-a")
	if _, _, err := f.st.BindObserver(hkPK, uid, "auto"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.st.BindObserver(hkPK2, uid, "auto"); err != nil {
		t.Fatal(err)
	}
	w := f.do("GET", oadmList, nil, as(f.admin))
	expectStatus(t, w, 200)
	got := decode[adminObserverLoginsResponse](t, w)
	if len(got.Logins) != 1 || got.Logins[0].UserID != uid || got.Logins[0].Email != "a@example.org" ||
		got.Logins[0].Username != "obs_aaaaaaaaaaaa" || len(got.Logins[0].Observers) != 2 || got.Logins[0].Revoked {
		t.Fatalf("logins = %+v", got.Logins)
	}
	if len(got.Bindings) != 2 || got.Bindings[0].UserID == nil || got.Bindings[0].BoundBy != "auto" || got.Bindings[0].ConfirmedAt == nil {
		t.Fatalf("bindings = %+v", got.Bindings)
	}
	for _, bad := range []string{"pw-a", olHash("pw-a"), `"enc"`} {
		if strings.Contains(w.Body.String(), bad) {
			t.Fatalf("list leaks %q", bad)
		}
	}
}

func TestObserverAdmin_ConfirmedAtFollowsOwnerPublish(t *testing.T) {
	f := newOadmFixture(t)
	f.idx.shared = map[string]bool{"meshcore_shared": true}
	a := f.addLogin(t, "a@example.org", "obs_aaaaaaaaaaaa", "pw-a")
	b := f.addLogin(t, "b@example.org", "obs_bbbbbbbbbbbb", "pw-b")
	confirmedAt := func() *string {
		t.Helper()
		w := f.do("GET", oadmList, nil, as(f.admin))
		expectStatus(t, w, 200)
		got := decode[adminObserverLoginsResponse](t, w)
		if len(got.Bindings) != 1 {
			t.Fatalf("bindings = %+v", got.Bindings)
		}
		return got.Bindings[0].ConfirmedAt
	}
	path := "/api/admin/observer-bindings/" + hkPK
	expectStatus(t, f.do("PUT", path, observerBindingRequest{UserID: &a}, as(f.admin)), 200)
	if c := confirmedAt(); c != nil {
		t.Fatalf("admin assignment confirmed: %v", *c)
	}
	if !strings.Contains(f.do("GET", oadmList, nil, as(f.admin)).Body.String(), `"confirmedAt":null`) {
		t.Fatal("confirmedAt not serialised as null")
	}
	if f.authz(t, "meshcore_shared", hkTopic(hkPK), "publish") != hookIgnore {
		t.Fatal("shared login not ignored before confirmation")
	}
	if f.authz(t, "obs_aaaaaaaaaaaa", hkTopic(hkPK), "publish") != hookAllow {
		t.Fatal("owner publish not allowed")
	}
	c := confirmedAt()
	if c == nil {
		t.Fatal("owner publish did not confirm")
	}
	if _, err := time.Parse(time.RFC3339, *c); err != nil {
		t.Fatalf("confirmedAt %q: %v", *c, err)
	}
	if f.authz(t, "meshcore_shared", hkTopic(hkPK), "publish") != hookDeny {
		t.Fatal("shared login not denied after confirmation")
	}
	if f.authz(t, "meshcore_shared", "meshcore/#", "subscribe") != hookIgnore {
		t.Fatal("shared login subscribe not ignored")
	}
	// Reassign to the same owner keeps it, to another owner clears it.
	expectStatus(t, f.do("PUT", path, observerBindingRequest{UserID: &a}, as(f.admin)), 200)
	if confirmedAt() == nil || f.authz(t, "meshcore_shared", hkTopic(hkPK), "publish") != hookDeny {
		t.Fatal("same-owner reassign cleared the confirmation")
	}
	expectStatus(t, f.do("PUT", path, observerBindingRequest{UserID: &b}, as(f.admin)), 200)
	if confirmedAt() != nil || f.authz(t, "meshcore_shared", hkTopic(hkPK), "publish") != hookIgnore {
		t.Fatal("reassign to another owner kept the confirmation")
	}
}

func TestObserverAdmin_OrphanHasNoConfirmedAt(t *testing.T) {
	f := newOadmFixture(t)
	a := f.addLogin(t, "a@example.org", "obs_aaaaaaaaaaaa", "pw-a")
	if _, _, err := f.st.BindObserver(hkPK, a, "auto"); err != nil {
		t.Fatal(err)
	}
	if err := f.st.Delete(a); err != nil {
		t.Fatal(err)
	}
	w := f.do("GET", oadmList, nil, as(f.admin))
	expectStatus(t, w, 200)
	got := decode[adminObserverLoginsResponse](t, w)
	if len(got.Bindings) != 1 || got.Bindings[0].UserID != nil || got.Bindings[0].ConfirmedAt != nil {
		t.Fatalf("bindings = %+v", got.Bindings)
	}
}

func TestObserverAdmin_RevokeDeniesAtOnce(t *testing.T) {
	f := newOadmFixture(t)
	uid := f.addLogin(t, "a@example.org", "obs_aaaaaaaaaaaa", "pw-a")
	if f.authz(t, "obs_aaaaaaaaaaaa", hkTopic(hkPK), "publish") != hookAllow {
		t.Fatal("publish should be allowed before revoke")
	}
	expectStatus(t, f.do("POST", f.revokePath(uid), nil, as(f.admin)), 200)
	if f.authz(t, "obs_aaaaaaaaaaaa", hkTopic(hkPK), "publish") != hookDeny {
		t.Fatal("authz not denied after revoke")
	}
	if f.authn(t, "obs_aaaaaaaaaaaa", "pw-a") != hookDeny {
		t.Fatal("authn not denied after revoke")
	}
	l, _ := f.st.MQTTLoginForUser(uid)
	if l.RevokedAt == nil {
		t.Fatal("store not revoked")
	}
	expectStatus(t, f.do("POST", f.revokePath(9999), nil, as(f.admin)), 404)
	expectStatus(t, f.do("POST", "/api/admin/observer-logins/x/revoke", nil, as(f.admin)), 400)
}

func TestObserverAdmin_ReissueReplacesCredentials(t *testing.T) {
	f := newOadmFixture(t)
	uid := f.addLogin(t, "a@example.org", "obs_aaaaaaaaaaaa", "pw-a")
	expectStatus(t, f.do("POST", f.revokePath(uid), nil, as(f.admin)), 200)
	before := len(f.fake.Sent())
	w := f.do("POST", f.reissuePath(uid), nil, as(f.admin))
	expectStatus(t, w, 200)
	if strings.TrimSpace(w.Body.String()) != `{"sent":true}` {
		t.Fatalf("body = %s", w.Body.String())
	}
	l, err := f.st.MQTTLoginForUser(uid)
	if err != nil {
		t.Fatal(err)
	}
	if l.Username == "obs_aaaaaaaaaaaa" || l.RevokedAt != nil {
		t.Fatalf("login = %+v", l)
	}
	pw, err := f.srv.auth.sealbox.Open(l.PasswordEnc)
	if err != nil {
		t.Fatal(err)
	}
	if f.authn(t, "obs_aaaaaaaaaaaa", "pw-a") != hookIgnore {
		t.Fatal("old username should be ignored")
	}
	if f.authn(t, l.Username, pw) != hookAllow {
		t.Fatal("new login does not authenticate")
	}
	sent := f.fake.Sent()[before:]
	if len(sent) != 1 || sent[0].Tag != "observer-credentials" || sent[0].To != "a@example.org" ||
		!strings.Contains(sent[0].Text, l.Username) || !strings.Contains(sent[0].Text, pw) {
		t.Fatalf("mail = %+v", sent)
	}
	if strings.Contains(w.Body.String(), pw) {
		t.Fatal("response leaks the password")
	}
	expectStatus(t, f.do("POST", f.reissuePath(f.admin.me.ID), nil, as(f.admin)), 404)
}

func TestObserverAdmin_ReassignOrphan(t *testing.T) {
	f := newOadmFixture(t)
	a := f.addLogin(t, "a@example.org", "obs_aaaaaaaaaaaa", "pw-a")
	b := f.addLogin(t, "b@example.org", "obs_bbbbbbbbbbbb", "pw-b")
	if f.authz(t, "obs_aaaaaaaaaaaa", hkTopic(hkPK), "publish") != hookAllow {
		t.Fatal("A should bind first")
	}
	path := "/api/admin/observer-bindings/" + hkPK
	expectStatus(t, f.do("PUT", path, observerBindingRequest{UserID: &b}, as(f.admin)), 200)
	if f.authz(t, "obs_bbbbbbbbbbbb", hkTopic(hkPK), "publish") != hookAllow {
		t.Fatal("B should own it after reassign")
	}
	if f.authz(t, "obs_aaaaaaaaaaaa", hkTopic(hkPK), "publish") != hookDeny {
		t.Fatal("A should be denied after reassign")
	}
	bs, _ := f.st.ObserverBindings()
	if len(bs) != 1 || bs[0].UserID == nil || *bs[0].UserID != b || bs[0].BoundBy != "admin" {
		t.Fatalf("bindings = %+v", bs)
	}
	if f.srv.auth.obsIdx.ownedCount(a) != 0 || f.srv.auth.obsIdx.ownedCount(b) != 1 {
		t.Fatal("owned counters not moved")
	}
	expectStatus(t, f.do("PUT", path, observerBindingRequest{UserID: idPtr(9999)}, as(f.admin)), 404)
	expectStatus(t, f.do("PUT", "/api/admin/observer-bindings/nothex", observerBindingRequest{}, as(f.admin)), 400)
}

func TestObserverAdmin_UnbindLetsNextPublisherBind(t *testing.T) {
	f := newOadmFixture(t)
	f.addLogin(t, "a@example.org", "obs_aaaaaaaaaaaa", "pw-a")
	f.addLogin(t, "b@example.org", "obs_bbbbbbbbbbbb", "pw-b")
	if f.authz(t, "obs_aaaaaaaaaaaa", hkTopic(hkPK), "publish") != hookAllow {
		t.Fatal("A should bind first")
	}
	expectStatus(t, f.do("PUT", "/api/admin/observer-bindings/"+hkPK, observerBindingRequest{}, as(f.admin)), 200)
	if bs, _ := f.st.ObserverBindings(); len(bs) != 0 {
		t.Fatalf("bindings = %+v", bs)
	}
	if f.authz(t, "obs_bbbbbbbbbbbb", hkTopic(hkPK), "publish") != hookAllow {
		t.Fatal("B should bind after unbind")
	}
	if f.authz(t, "obs_aaaaaaaaaaaa", hkTopic(hkPK), "publish") != hookDeny {
		t.Fatal("A should be denied")
	}
}

func TestObserverAdmin_AuditRowsWithoutPassword(t *testing.T) {
	f := newOadmFixture(t)
	uid := f.addLogin(t, "a@example.org", "obs_aaaaaaaaaaaa", "pw-a")
	expectStatus(t, f.do("POST", f.revokePath(uid), nil, as(f.admin)), 200)
	expectStatus(t, f.do("POST", f.reissuePath(uid), nil, as(f.admin)), 200)
	expectStatus(t, f.do("PUT", "/api/admin/observer-bindings/"+hkPK, observerBindingRequest{UserID: &uid}, as(f.admin)), 200)
	entries, err := f.st.AuditAllFor(f.admin.me.ID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Action] = true
	}
	for _, want := range []string{"observer.login.revoke", "observer.login.reissue", "observer.binding.set"} {
		if !seen[want] {
			t.Fatalf("no %s audit row; have %v", want, seen)
		}
	}
	l, _ := f.st.MQTTLoginForUser(uid)
	pw, _ := f.srv.auth.sealbox.Open(l.PasswordEnc)
	b, _ := json.Marshal(entries)
	for _, bad := range []string{pw, l.PasswordHash, l.PasswordEnc} {
		if strings.Contains(string(b), bad) {
			t.Fatalf("audit leaks %q", bad)
		}
	}
}

func TestObserverAdmin_RoutesAbsentWhenOff(t *testing.T) {
	f := newAuthFixture(t, "boss@example.org")
	admin := f.registerAndActivate(t, "boss@example.org", "Boss", "correct-horse-battery")
	if code := f.do("GET", oadmList, nil, as(admin)).Code; code != 404 && code != 405 {
		t.Fatalf("feature off: GET = %d", code)
	}
}

func TestAccountDeleteRemovesObserverLogin(t *testing.T) {
	f := newOadmFixture(t)
	a := f.addLogin(t, "a@example.org", "obs_aaaaaaaaaaaa", "pw-a")
	f.addLogin(t, "b@example.org", "obs_bbbbbbbbbbbb", "pw-b")
	if f.authz(t, "obs_aaaaaaaaaaaa", hkTopic(hkPK), "publish") != hookAllow {
		t.Fatal("A should bind first")
	}
	c := f.login(t, "a@example.org", "correct-horse-battery")
	expectStatus(t, f.do("DELETE", "/api/account", passwordConfirmRequest{CurrentPassword: "correct-horse-battery"}, as(c)), 200)
	if f.authn(t, "obs_aaaaaaaaaaaa", "pw-a") != hookIgnore {
		t.Fatal("deleted user's login should be ignored")
	}
	if f.authz(t, "obs_bbbbbbbbbbbb", hkTopic(hkPK), "publish") != hookDeny {
		t.Fatal("orphaned pubkey was auto-claimed by another user")
	}
	if f.srv.auth.obsIdx.ownedCount(a) != 0 {
		t.Fatal("owned counter not cleared")
	}
}

func TestAdminDeleteRemovesObserverLogin(t *testing.T) {
	f := newOadmFixture(t)
	a := f.addLogin(t, "a@example.org", "obs_aaaaaaaaaaaa", "pw-a")
	f.addLogin(t, "b@example.org", "obs_bbbbbbbbbbbb", "pw-b")
	f.authz(t, "obs_aaaaaaaaaaaa", hkTopic(hkPK), "publish")
	expectStatus(t, f.do("DELETE", fmt.Sprintf("/api/admin/users/%d", a), nil, as(f.admin)), 200)
	if f.authn(t, "obs_aaaaaaaaaaaa", "pw-a") != hookIgnore {
		t.Fatal("deleted user's login should be ignored")
	}
	if f.authz(t, "obs_bbbbbbbbbbbb", hkTopic(hkPK), "publish") != hookDeny {
		t.Fatal("orphaned pubkey was auto-claimed")
	}
}

func TestAdminDisableDeniesObserverLogin(t *testing.T) {
	f := newOadmFixture(t)
	a := f.addLogin(t, "a@example.org", "obs_aaaaaaaaaaaa", "pw-a")
	f.addLogin(t, "r@example.org", "obs_rrrrrrrrrrrr", "pw-r")
	rid, _ := f.srv.auth.obsIdx.userIDFor("obs_rrrrrrrrrrrr")
	expectStatus(t, f.do("POST", f.revokePath(rid), nil, as(f.admin)), 200)

	expectStatus(t, f.do("POST", fmt.Sprintf("/api/admin/users/%d/disable", a), nil, as(f.admin)), 200)
	if f.authn(t, "obs_aaaaaaaaaaaa", "pw-a") != hookDeny || f.authz(t, "obs_aaaaaaaaaaaa", hkTopic(hkPK), "publish") != hookDeny {
		t.Fatal("disabled user's login not denied")
	}
	expectStatus(t, f.do("POST", fmt.Sprintf("/api/admin/users/%d/enable", a), nil, as(f.admin)), 200)
	if f.authn(t, "obs_aaaaaaaaaaaa", "pw-a") != hookAllow || f.authz(t, "obs_aaaaaaaaaaaa", hkTopic(hkPK), "publish") != hookAllow {
		t.Fatal("login not allowed again after enable")
	}

	expectStatus(t, f.do("POST", fmt.Sprintf("/api/admin/users/%d/disable", rid), nil, as(f.admin)), 200)
	expectStatus(t, f.do("POST", fmt.Sprintf("/api/admin/users/%d/enable", rid), nil, as(f.admin)), 200)
	if f.authn(t, "obs_rrrrrrrrrrrr", "pw-r") != hookDeny {
		t.Fatal("enable un-revoked a revoked login")
	}
}

func TestIndexLoadMarksDisabledUsers(t *testing.T) {
	f := newOadmFixture(t)
	a := f.addLogin(t, "a@example.org", "obs_aaaaaaaaaaaa", "pw-a")
	f.addLogin(t, "b@example.org", "obs_bbbbbbbbbbbb", "pw-b")
	if err := f.st.SetStatus(a, users.StatusDisabled); err != nil {
		t.Fatal(err)
	}
	ix, err := newObserverLoginIndex(f.st, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ix.authenticate("obs_aaaaaaaaaaaa", "pw-a") != hookDeny {
		t.Fatal("login of a disabled user allowed after load")
	}
	if ix.authenticate("obs_bbbbbbbbbbbb", "pw-b") != hookAllow {
		t.Fatal("login of an active user denied after load")
	}
}

func TestIndexReissueKeepsDisabled(t *testing.T) {
	f := newHookFixture(t)
	a := f.addLogin(t, "a@example.org", "obs_aaaaaaaaaaaa", "pw-a")
	f.idx.setDisabled(a, true)
	f.idx.setLogin(users.MQTTLogin{UserID: a, Username: "obs_new000000000", PasswordHash: olHash("pw-n")})
	if f.idx.authenticate("obs_new000000000", "pw-n") != hookDeny {
		t.Fatal("reissue cleared the disabled flag")
	}
}

func TestObserverAudit_ActionNamesPassFilter(t *testing.T) {
	for _, a := range []string{"observer.login.create", "observer.login.resend", "observer.login.revoke",
		"observer.login.reissue", "observer.binding.set", "observer.*", "observer.login.*"} {
		flt, err := parseAuditFilter(url.Values{"action": {a}})
		if err != nil || len(flt.Actions) != 1 || flt.Actions[0] != a {
			t.Errorf("%s: filter = %+v, err = %v", a, flt, err)
		}
	}
}

func TestObserverAudit_BindingSetNamesPreviousOwner(t *testing.T) {
	f := newOadmFixture(t)
	a := f.addLogin(t, "a@example.org", "obs_aaaaaaaaaaaa", "pw-a")
	b := f.addLogin(t, "b@example.org", "obs_bbbbbbbbbbbb", "pw-b")
	if f.authz(t, "obs_aaaaaaaaaaaa", hkTopic(hkPK), "publish") != hookAllow {
		t.Fatal("A should bind first")
	}
	path := "/api/admin/observer-bindings/" + hkPK
	expectStatus(t, f.do("PUT", path, observerBindingRequest{UserID: &b}, as(f.admin)), 200)
	expectStatus(t, f.do("PUT", path, observerBindingRequest{}, as(f.admin)), 200)
	expectStatus(t, f.do("PUT", "/api/admin/observer-bindings/"+oadmPK2, observerBindingRequest{UserID: &a}, as(f.admin)), 200)
	entries, err := f.st.AuditAllFor(f.admin.me.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		if e.Action == "observer.binding.set" {
			got = append(got, e.Detail["pubkey"][:4]+" prev="+e.Detail["previousUserId"])
		}
	}
	pk, pk2 := strings.ToUpper(hkPK)[:4], strings.ToUpper(oadmPK2)[:4]
	want := map[string]bool{
		fmt.Sprintf("%s prev=%d", pk, a): true,
		fmt.Sprintf("%s prev=%d", pk, b): true,
		pk2 + " prev=none":               true,
	}
	if len(got) != 3 {
		t.Fatalf("rows = %v", got)
	}
	for _, g := range got {
		if !want[g] {
			t.Fatalf("unexpected row %q in %v", g, got)
		}
	}
}

// A reissue whose mail fails has already replaced the credentials, so the
// 503 must say so and point at the user's own resend, not invite a retry
// that would reissue again.
func TestObserverAdmin_ReissueMailFailureSaysReissued(t *testing.T) {
	f := newOadmFixture(t)
	uid := f.addLogin(t, "a@example.org", "obs_aaaaaaaaaaaa", "pw-a")
	f.fake.SetSendErr(errors.New("brevo down"))
	w := f.do("POST", f.reissuePath(uid), nil, as(f.admin))
	expectStatus(t, w, 503)
	if !strings.Contains(w.Body.String(), "reissued") || !strings.Contains(w.Body.String(), "account page") {
		t.Fatalf("body = %s", w.Body.String())
	}
	if l, err := f.st.MQTTLoginForUser(uid); err != nil || l.Username == "obs_aaaaaaaaaaaa" {
		t.Fatalf("login = %+v, %v; want the reissued username", l, err)
	}
}
