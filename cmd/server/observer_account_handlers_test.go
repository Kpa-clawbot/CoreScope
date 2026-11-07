package main

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/users"
)

const oaPath = "/api/account/observer-login"

var oaKey = []byte("0123456789abcdef0123456789abcdef")

type oaFixture struct {
	*authFixture
	c     *client
	clock *oaClock
}

type oaClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *oaClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *oaClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// newOAFixture is an auth fixture with observer logins on (key, broker
// settings, index, sealed-password box), one logged-in user and a clock the
// test controls.
func newOAFixture(t *testing.T) *oaFixture {
	t.Helper()
	f := newAuthFixture(t)
	a := f.srv.auth
	a.set.observerLogins = observerLoginSettings{enabled: true, hookSecret: hkSecret, key: oaKey,
		host: "mqtt.example.org", port: 8883, transport: "tcp", tls: true}
	idx, err := newObserverLoginIndex(a.st, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.obsIdx = idx
	if a.sealbox, err = users.NewSecretBox(oaKey); err != nil {
		t.Fatal(err)
	}
	clk := &oaClock{t: time.Now()}
	a.st.SetClock(clk.now)
	a.now = clk.now
	f.router = mux.NewRouter()
	f.srv.registerAuthRoutes(f.router)
	c := f.registerAndActivate(t, "obs@example.org", "Obs Owner", "correct-horse-battery")
	return &oaFixture{authFixture: f, c: c, clock: clk}
}

// storedPassword decrypts the password the store holds for the user.
func (f *oaFixture) storedPassword(t *testing.T) (username, password string) {
	t.Helper()
	l, err := f.st.MQTTLoginForUser(f.c.me.ID)
	if err != nil {
		t.Fatal(err)
	}
	pw, err := f.srv.auth.sealbox.Open(l.PasswordEnc)
	if err != nil {
		t.Fatal(err)
	}
	return l.Username, pw
}

func TestObserverLogin_CreateMailsOnce(t *testing.T) {
	f := newOAFixture(t)
	before := len(f.fake.Sent())
	w := f.do("POST", oaPath, nil, as(f.c))
	expectStatus(t, w, 201)
	if strings.TrimSpace(w.Body.String()) != `{"sent":true}` {
		t.Fatalf("body = %s", w.Body.String())
	}
	user, pw := f.storedPassword(t)
	if strings.Contains(w.Body.String(), pw) {
		t.Fatal("response contains the password")
	}
	sent := f.fake.Sent()[before:]
	if len(sent) != 1 {
		t.Fatalf("%d mails, want 1", len(sent))
	}
	m := sent[0]
	if m.Tag != "observer-credentials" || m.To != "obs@example.org" {
		t.Fatalf("mail = tag %q to %q", m.Tag, m.To)
	}
	for _, want := range []string{user, pw, `method = "password"`, "set mqtt4.username " + user, "set mqtt4.password " + pw,
		`server = "mqtt.example.org"`, "port = 8883", "enabled = true", "set mqtt4.port 8883", "set mqtt.iata <your IATA code>"} {
		if !strings.Contains(m.Text, want) {
			t.Fatalf("text part lacks %q:\n%s", want, m.Text)
		}
	}
	if !strings.HasPrefix(user, "obs_") {
		t.Fatalf("username %q", user)
	}
	// The index knows the new login, so the broker can authenticate it.
	if f.srv.auth.obsIdx.authenticate(user, pw) != hookAllow {
		t.Fatal("new login not in the index")
	}
	rows, _ := f.st.MailForUser(f.c.me.ID, 10)
	n := 0
	for _, r := range rows {
		if r.Purpose == "observer-credentials" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d observer-credentials mail_log rows, want 1", n)
	}
}

func TestObserverLogin_GetNeverLeaksPassword(t *testing.T) {
	f := newOAFixture(t)
	w := f.do("GET", oaPath, nil, as(f.c))
	expectStatus(t, w, 200)
	if strings.TrimSpace(w.Body.String()) != `{"login":null}` {
		t.Fatalf("empty body = %s", w.Body.String())
	}
	expectStatus(t, f.do("POST", oaPath, nil, as(f.c)), 201)
	user, pw := f.storedPassword(t)
	l, _ := f.st.MQTTLoginForUser(f.c.me.ID)
	w = f.do("GET", oaPath, nil, as(f.c))
	expectStatus(t, w, 200)
	body := w.Body.String()
	for _, bad := range []string{pw, l.PasswordHash, l.PasswordEnc, "assword"} {
		if strings.Contains(body, bad) {
			t.Fatalf("GET leaks %q: %s", bad, body)
		}
	}
	var got struct {
		Login struct {
			Username  string `json:"username"`
			CreatedAt string `json:"createdAt"`
			Revoked   bool   `json:"revoked"`
		} `json:"login"`
		Observers []json.RawMessage `json:"observers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Login.Username != user || got.Login.CreatedAt == "" || got.Login.Revoked {
		t.Fatalf("login = %+v", got.Login)
	}
	if !strings.Contains(body, `"observers":[]`) {
		t.Fatalf("observers = %s", body)
	}
}

func TestObserverLogin_GetListsBoundObserversWithNameAndLastSeen(t *testing.T) {
	f := newOAFixture(t)
	db := setupTestDB(t)
	f.srv.db = db
	expectStatus(t, f.do("POST", oaPath, nil, as(f.c)), 201)
	pk := "ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789"
	pk2 := "1111111111111111111111111111111111111111111111111111111111111111"
	if _, err := db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count) VALUES (?, 'Roof', 'BRU', '2026-10-25T10:00:00Z', '2026-01-01T00:00:00Z', 1)`, pk); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{pk, pk2} {
		if _, _, err := f.st.BindObserver(p, f.c.me.ID, "auto"); err != nil {
			t.Fatal(err)
		}
	}
	w := f.do("GET", oaPath, nil, as(f.c))
	expectStatus(t, w, 200)
	var got struct {
		Observers []struct {
			Pubkey     string  `json:"pubkey"`
			Name       string  `json:"name"`
			LastSeenAt *string `json:"lastSeenAt"`
		} `json:"observers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Observers) != 2 {
		t.Fatalf("observers = %s", w.Body.String())
	}
	by := map[string]int{}
	for i, o := range got.Observers {
		by[strings.ToUpper(o.Pubkey)] = i
	}
	known, unknown := got.Observers[by[pk]], got.Observers[by[pk2]]
	if known.Name != "Roof" || known.LastSeenAt == nil || *known.LastSeenAt != "2026-10-25T10:00:00Z" {
		t.Fatalf("known = %+v", known)
	}
	if unknown.Name != "" || unknown.LastSeenAt != nil {
		t.Fatalf("unknown = %+v", unknown)
	}
}

func TestObserverLogin_CreateTwice409(t *testing.T) {
	f := newOAFixture(t)
	expectStatus(t, f.do("POST", oaPath, nil, as(f.c)), 201)
	user, pw := f.storedPassword(t)
	expectStatus(t, f.do("POST", oaPath, nil, as(f.c)), 409)
	if u2, p2 := f.storedPassword(t); u2 != user || p2 != pw {
		t.Fatal("second POST changed the credentials")
	}
	if n := len(f.fake.Sent()); n != 2 { // activation + one credentials mail
		t.Fatalf("%d mails", n)
	}
}

func TestObserverLogin_RevokedCannotCreate403(t *testing.T) {
	f := newOAFixture(t)
	expectStatus(t, f.do("POST", oaPath, nil, as(f.c)), 201)
	if err := f.st.RevokeMQTTLogin(f.c.me.ID); err != nil {
		t.Fatal(err)
	}
	before := len(f.fake.Sent())
	expectStatus(t, f.do("POST", oaPath, nil, as(f.c)), 403)
	expectStatus(t, f.do("POST", oaPath+"/resend", nil, as(f.c)), 403)
	if len(f.fake.Sent()) != before {
		t.Fatal("mail sent for a revoked login")
	}
	w := f.do("GET", oaPath, nil, as(f.c))
	if !strings.Contains(w.Body.String(), `"revoked":true`) {
		t.Fatalf("GET = %s", w.Body.String())
	}
}

func TestObserverLogin_ResendSameCredentials(t *testing.T) {
	f := newOAFixture(t)
	expectStatus(t, f.do("POST", oaPath+"/resend", nil, as(f.c)), 404) // nothing to resend yet
	expectStatus(t, f.do("POST", oaPath, nil, as(f.c)), 201)
	user, pw := f.storedPassword(t)
	w := f.do("POST", oaPath+"/resend", nil, as(f.c))
	expectStatus(t, w, 200)
	if strings.TrimSpace(w.Body.String()) != `{"sent":true}` {
		t.Fatalf("body = %s", w.Body.String())
	}
	sent := f.fake.Sent()
	last := sent[len(sent)-1]
	if last.Tag != "observer-credentials" || !strings.Contains(last.Text, "password = \""+pw+"\"") || !strings.Contains(last.Text, user) {
		t.Fatalf("resend mail:\n%s", last.Text)
	}
}

func TestObserverLogin_ResendLimit(t *testing.T) {
	f := newOAFixture(t)
	expectStatus(t, f.do("POST", oaPath, nil, as(f.c)), 201)           // 1
	expectStatus(t, f.do("POST", oaPath+"/resend", nil, as(f.c)), 200) // 2
	expectStatus(t, f.do("POST", oaPath+"/resend", nil, as(f.c)), 200) // 3
	before := len(f.fake.Sent())
	expectStatus(t, f.do("POST", oaPath+"/resend", nil, as(f.c)), 429) // 4th
	if len(f.fake.Sent()) != before {
		t.Fatal("mail sent over the limit")
	}
	f.clock.advance(23 * time.Hour)
	expectStatus(t, f.do("POST", oaPath+"/resend", nil, as(f.c)), 429)
	f.clock.advance(2 * time.Hour)
	expectStatus(t, f.do("POST", oaPath+"/resend", nil, as(f.c)), 200)
}

func TestMailLogHasNoPassword(t *testing.T) {
	f := newOAFixture(t)
	expectStatus(t, f.do("POST", oaPath, nil, as(f.c)), 201)
	expectStatus(t, f.do("POST", oaPath+"/resend", nil, as(f.c)), 200)
	_, pw := f.storedPassword(t)
	rows, err := f.st.MailForUser(f.c.me.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(rows)
	if strings.Contains(string(b), pw) {
		t.Fatal("mail_log rows contain the password")
	}
	audit, _ := f.st.AuditAllFor(f.c.me.ID)
	b, _ = json.Marshal(audit)
	if strings.Contains(string(b), pw) {
		t.Fatal("audit rows contain the password")
	}
}

func TestObserverLogin_RoutesAbsentWhenOff(t *testing.T) {
	f := newAuthFixture(t)
	c := f.registerAndActivate(t, "x@example.org", "Xavier", "correct-horse-battery")
	if code := f.do("GET", oaPath, nil, as(c)).Code; code != 404 && code != 405 {
		t.Fatalf("feature off: GET = %d", code)
	}
}

func TestObserverLogin_RequiresSession(t *testing.T) {
	f := newOAFixture(t)
	expectStatus(t, f.do("GET", oaPath, nil), 401)
	expectStatus(t, f.do("POST", oaPath, nil), 401)
}

func TestObserverLogin_NoBox503(t *testing.T) {
	f := newOAFixture(t)
	f.srv.auth.sealbox = nil
	expectStatus(t, f.do("POST", oaPath, nil, as(f.c)), 503)
}

func TestExportHasObserverLoginWithoutPassword(t *testing.T) {
	f := newOAFixture(t)
	expectStatus(t, f.do("POST", oaPath, nil, as(f.c)), 201)
	if _, _, err := f.st.BindObserver(olPK, f.c.me.ID, "auto"); err != nil {
		t.Fatal(err)
	}
	user, pw := f.storedPassword(t)
	l, _ := f.st.MQTTLoginForUser(f.c.me.ID)
	w := f.do("GET", "/api/account/export", nil, as(f.c))
	expectStatus(t, w, 200)
	body := w.Body.String()
	if !strings.Contains(body, user) {
		t.Fatalf("export lacks username: %s", body)
	}
	for _, bad := range []string{pw, l.PasswordHash, l.PasswordEnc} {
		if strings.Contains(body, bad) {
			t.Fatalf("export leaks credential material %q", bad)
		}
	}
	for _, want := range []string{`"observerLogin"`, `"observerBindings"`, `"boundBy":"auto"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("export lacks %s: %s", want, body)
		}
	}
}

func TestCreateLoginEnablesOwnEvent(t *testing.T) {
	f := newOAFixture(t)
	expectStatus(t, f.do("POST", oaPath, nil, as(f.c)), 201)
	p, err := f.st.NotifyPrefsFor(f.c.me.ID)
	if err != nil || !p.Has(users.NotifyObserverOfflineOwn) || !p.Has(users.NotifyNodeOffline) {
		t.Fatalf("prefs after create = %+v, %v", p, err)
	}
}

func TestObserverLogin_CreateAndResendAuditRows(t *testing.T) {
	f := newOAFixture(t)
	expectStatus(t, f.do("POST", oaPath, nil, as(f.c)), 201)
	expectStatus(t, f.do("POST", oaPath+"/resend", nil, as(f.c)), 200)
	f.srv.auth.waitAudits()
	entries, err := f.st.AuditAllFor(f.c.me.ID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Action] = true
	}
	for _, want := range []string{"observer.login.create", "observer.login.resend"} {
		if !seen[want] {
			t.Fatalf("no %s audit row; have %v", want, seen)
		}
	}
}
