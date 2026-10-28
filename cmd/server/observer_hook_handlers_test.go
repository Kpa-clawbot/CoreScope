package main

import (
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/users"
)

const (
	hkSecret = "hook-secret-0123456789abcdef"
	hkPK     = "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789"
	hkPK2    = "2222222222222222222222222222222222222222222222222222222222222222"
)

func hkTopic(pk string) string { return "meshcore/BRU/" + pk + "/packets" }

type hookFixture struct {
	*authFixture
	idx *observerLoginIndex
}

// newHookFixture is an auth fixture with observer logins on and the hook
// routes registered.
func newHookFixture(t *testing.T, adminEmails ...string) *hookFixture {
	t.Helper()
	a, fake := newTestAuthService(t, adminEmails...)
	a.set.observerLogins = observerLoginSettings{enabled: true, hookSecret: hkSecret}
	idx, err := newObserverLoginIndex(a.st, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.obsIdx = idx
	srv := &Server{cfg: &Config{APIKey: testAPIKey}, perfStats: NewPerfStats(), auth: a}
	r := mux.NewRouter()
	srv.registerAuthRoutes(r)
	return &hookFixture{authFixture: &authFixture{srv: srv, router: r, fake: fake, st: a.st}, idx: idx}
}

// addLogin registers a user and gives them an MQTT login.
func (f *hookFixture) addLogin(t *testing.T, email, username, password string) int64 {
	t.Helper()
	c := f.registerAndActivate(t, email, "U "+username, "correct-horse-battery")
	l := users.MQTTLogin{UserID: c.me.ID, Username: username, PasswordHash: olHash(password), PasswordEnc: "enc", CreatedAt: time.Now()}
	if err := f.st.CreateMQTTLogin(l); err != nil {
		t.Fatal(err)
	}
	f.idx.setLogin(l)
	return c.me.ID
}

// post sends one hook request and returns the status and, for a 200, the result.
func (f *hookFixture) post(t *testing.T, path string, body any, secret string) (int, hookResult) {
	t.Helper()
	var mods []reqMod
	if secret != "" {
		mods = append(mods, header(hookSecretHeader, secret))
	}
	w := f.do("POST", path, body, mods...)
	if w.Code != 200 {
		return w.Code, ""
	}
	return w.Code, decode[emqxHookResp](t, w).Result
}

func (f *hookFixture) authn(t *testing.T, user, pw string) hookResult {
	t.Helper()
	_, r := f.post(t, "/api/mqtt-hook/emqx/authn", emqxAuthnReq{Username: user, Password: pw, ClientID: "c1"}, hkSecret)
	return r
}

func (f *hookFixture) authz(t *testing.T, user, topic, action string) hookResult {
	t.Helper()
	_, r := f.post(t, "/api/mqtt-hook/emqx/authz", emqxAuthzReq{Username: user, Topic: topic, Action: action}, hkSecret)
	return r
}

func TestHook_RequiresSecret(t *testing.T) {
	f := newHookFixture(t)
	f.addLogin(t, "a@example.org", "obs_aaaaaaaaaaaa", "pw-a")
	body := emqxAuthnReq{Username: "obs_aaaaaaaaaaaa", Password: "pw-a"}
	for _, path := range []string{"/api/mqtt-hook/emqx/authn", "/api/mqtt-hook/emqx/authz"} {
		for _, secret := range []string{"", "wrong", hkSecret + "x"} {
			if code, _ := f.post(t, path, body, secret); code != 401 {
				t.Fatalf("%s secret %q = %d, want 401", path, secret, code)
			}
		}
	}
	if code, r := f.post(t, "/api/mqtt-hook/emqx/authn", body, hkSecret); code != 200 || r != hookAllow {
		t.Fatalf("right secret = %d %v", code, r)
	}
}

func TestHook_BadJSONIs400(t *testing.T) {
	f := newHookFixture(t)
	w := f.do("POST", "/api/mqtt-hook/emqx/authn", "not an object", header(hookSecretHeader, hkSecret))
	if w.Code != 400 {
		t.Fatalf("code = %d", w.Code)
	}
}

func TestHook_EMQXAuthn(t *testing.T) {
	f := newHookFixture(t)
	f.addLogin(t, "a@example.org", "obs_aaaaaaaaaaaa", "pw-a")
	cases := []struct {
		user, pw string
		want     hookResult
	}{
		{"obs_aaaaaaaaaaaa", "pw-a", hookAllow},
		{"obs_aaaaaaaaaaaa", "nope", hookDeny},
		{"shared_user", "whatever", hookIgnore},
	}
	for _, c := range cases {
		if got := f.authn(t, c.user, c.pw); got != c.want {
			t.Fatalf("%s/%s = %q, want %q", c.user, c.pw, got, c.want)
		}
	}
}

func TestHook_AuthnTouchThrottled(t *testing.T) {
	f := newHookFixture(t)
	uid := f.addLogin(t, "a@example.org", "obs_aaaaaaaaaaaa", "pw-a")
	if f.authn(t, "obs_aaaaaaaaaaaa", "pw-a") != hookAllow {
		t.Fatal("authn not allowed")
	}
	f.srv.auth.waitAudits()
	l, _ := f.st.MQTTLoginForUser(uid)
	if l.LastAuthAt == nil {
		t.Fatal("first auth did not touch")
	}
	first := *l.LastAuthAt
	time.Sleep(1100 * time.Millisecond) // users.db stores whole seconds
	f.authn(t, "obs_aaaaaaaaaaaa", "pw-a")
	f.srv.auth.waitAudits()
	l, _ = f.st.MQTTLoginForUser(uid)
	if !l.LastAuthAt.Equal(first) {
		t.Fatalf("touched again inside the window: %v -> %v", first, l.LastAuthAt)
	}

	tt := &authTouches{}
	now := time.Now()
	if !tt.due(1, now) {
		t.Fatal("first touch must be due")
	}
	if tt.due(1, now.Add(9*time.Minute)) {
		t.Fatal("touch due after 9 minutes")
	}
	if !tt.due(1, now.Add(11*time.Minute)) {
		t.Fatal("touch not due after 11 minutes")
	}
}

func TestHook_EMQXAuthz_BindsAndAllows(t *testing.T) {
	f := newHookFixture(t)
	uid := f.addLogin(t, "a@example.org", "obs_aaaaaaaaaaaa", "pw-a")
	if r := f.authz(t, "obs_aaaaaaaaaaaa", hkTopic(hkPK), "publish"); r != hookAllow {
		t.Fatalf("authz = %q", r)
	}
	bs, err := f.st.ObserverBindingsForUser(uid)
	if err != nil || len(bs) != 1 || bs[0].Pubkey != hkPK || bs[0].BoundBy != "auto" {
		t.Fatalf("bindings = %+v, %v", bs, err)
	}
	cases := []struct {
		user, topic, action string
		want                hookResult
	}{
		{"obs_aaaaaaaaaaaa", hkTopic(hkPK), "subscribe", hookDeny},
		{"obs_aaaaaaaaaaaa", "meshcore/BRU/short/packets", "publish", hookDeny},
		{"shared_user", hkTopic(hkPK), "publish", hookIgnore},
	}
	for _, c := range cases {
		if got := f.authz(t, c.user, c.topic, c.action); got != c.want {
			t.Fatalf("%+v = %q", c, got)
		}
	}
	f.addLogin(t, "b@example.org", "obs_bbbbbbbbbbbb", "pw-b")
	if r := f.authz(t, "obs_bbbbbbbbbbbb", hkTopic(hkPK), "publish"); r != hookDeny {
		t.Fatalf("foreign publish = %q", r)
	}
}

func TestHook_RevokedLoginDeniedImmediately(t *testing.T) {
	f := newHookFixture(t)
	uid := f.addLogin(t, "a@example.org", "obs_aaaaaaaaaaaa", "pw-a")
	if r := f.authz(t, "obs_aaaaaaaaaaaa", hkTopic(hkPK), "publish"); r != hookAllow {
		t.Fatalf("before revoke = %q", r)
	}
	if err := f.st.RevokeMQTTLogin(uid); err != nil {
		t.Fatal(err)
	}
	f.idx.revoke(uid)
	if r := f.authz(t, "obs_aaaaaaaaaaaa", hkTopic(hkPK), "publish"); r != hookDeny {
		t.Fatalf("after revoke authz = %q", r)
	}
	if r := f.authn(t, "obs_aaaaaaaaaaaa", "pw-a"); r != hookDeny {
		t.Fatalf("after revoke authn = %q", r)
	}
}

func TestHook_KnownObserverBindMailsAdmins(t *testing.T) {
	f := newHookFixture(t, "admin@example.org")
	f.registerAndActivate(t, "admin@example.org", "Admin", "correct-horse-battery")
	db := setupTestDB(t)
	f.srv.db = db
	if _, err := db.conn.Exec(`INSERT INTO observers (id, name) VALUES (?, 'Known')`, strings.ToLower(hkPK)); err != nil {
		t.Fatal(err)
	}
	f.addLogin(t, "a@example.org", "obs_aaaaaaaaaaaa", "pw-a")
	before := len(f.fake.Sent())

	f.authz(t, "obs_aaaaaaaaaaaa", hkTopic(hkPK2), "publish")
	f.srv.auth.waitAudits()
	if got := len(f.fake.Sent()); got != before {
		t.Fatalf("unknown observer mailed: %d new", got-before)
	}

	f.authz(t, "obs_aaaaaaaaaaaa", hkTopic(hkPK), "publish")
	f.srv.auth.waitAudits()
	sent := f.fake.Sent()[before:]
	if len(sent) != 1 || sent[0].To != "admin@example.org" || sent[0].Tag != "observer-bound" {
		t.Fatalf("mails = %+v", sent)
	}

	f.authz(t, "obs_aaaaaaaaaaaa", hkTopic(hkPK), "publish")
	f.srv.auth.waitAudits()
	if got := len(f.fake.Sent()) - before; got != 1 {
		t.Fatalf("repeat publish mailed again: %d", got)
	}
}

func TestHookRoutesAbsentWhenDisabled(t *testing.T) {
	f := newAuthFixture(t)
	for _, p := range []string{"/api/mqtt-hook/emqx/authn", "/api/mqtt-hook/emqx/authz"} {
		w := f.serve("POST", p, emqxAuthnReq{}, header(hookSecretHeader, hkSecret))
		if w.Code != 404 {
			t.Fatalf("%s = %d, want 404", p, w.Code)
		}
	}
}

func TestNewAuthServiceBuildsIndexOnlyWhenEnabled(t *testing.T) {
	a, _ := newTestAuthService(t)
	if a.obsIdx != nil {
		t.Fatal("index built while disabled")
	}
	set := *a.set
	set.observerLogins = observerLoginSettings{enabled: true, hookSecret: hkSecret}
	b := newAuthService(&set, a.st, a.mail)
	if b.obsIdx == nil {
		t.Fatal("index not built while enabled")
	}
}
