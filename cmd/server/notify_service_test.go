package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/mailer"
	"github.com/meshcore-analyzer/users"
)

type fakeNotifySource struct {
	isReady        bool
	hs             HealthThresholds
	low            int
	nodeMap        map[string]notifyNode
	heard          map[string]time.Time
	relayed        map[string]time.Time
	obs            []notifyObserver
	newestAt       func() time.Time // newest packet in the store; nil means none
	panicNext      bool
	heardAsked     []string
	foreignAsked   int
	observersAsked int
}

func (f *fakeNotifySource) ready() bool              { return f.isReady }
func (f *fakeNotifySource) health() HealthThresholds { return f.hs }
func (f *fakeNotifySource) lowBatteryMv() int        { return f.low }
func (f *fakeNotifySource) nodes(pubkeys []string, withForeign bool) (map[string]notifyNode, error) {
	if f.panicNext {
		f.panicNext = false
		panic("fake source panic")
	}
	out := map[string]notifyNode{}
	for _, pk := range pubkeys {
		if n, ok := f.nodeMap[pk]; ok {
			out[pk] = n
		}
	}
	if withForeign {
		f.foreignAsked++
		for pk, n := range f.nodeMap {
			if n.Foreign {
				out[pk] = n
			}
		}
	}
	return out, nil
}
func (f *fakeNotifySource) lastHeard(pks []string) map[string]time.Time {
	f.heardAsked = append([]string(nil), pks...)
	return f.heard
}
func (f *fakeNotifySource) lastRelayed([]string) map[string]time.Time { return f.relayed }
func (f *fakeNotifySource) newestPacket() time.Time {
	if f.newestAt == nil {
		return time.Time{}
	}
	return f.newestAt()
}
func (f *fakeNotifySource) observers() ([]notifyObserver, error) {
	f.observersAsked++
	return f.obs, nil
}

type notifyClock struct{ t time.Time }

func (c *notifyClock) Now() time.Time          { return c.t }
func (c *notifyClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func defaultNotifySettings() notifySettings {
	return notifySettings{enabled: true, interval: 5 * time.Minute, perUserPerDay: 20, maxMailsPerDay: 300, maxWatchesPerUser: 50}
}

type notifyFixture struct {
	*authFixture
	n   *notifier
	src *fakeNotifySource
	clk *notifyClock
}

// newNotifyFixture is newAuthFixture with notifications set to ns, a fake
// analyzer source and one fixed clock for the notifier and users.db.
// admin@example.org is a config admin.
func newNotifyFixture(t *testing.T, ns notifySettings) *notifyFixture {
	t.Helper()
	a, fake := newTestAuthService(t, "admin@example.org")
	a.set.notify = ns
	clk := &notifyClock{t: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
	a.st.SetClock(clk.Now)
	src := &fakeNotifySource{isReady: true, hs: (&Config{}).GetHealthThresholds(), low: 3300,
		nodeMap: map[string]notifyNode{}, heard: map[string]time.Time{}, relayed: map[string]time.Time{}, newestAt: clk.Now}
	if ns.enabled {
		a.notify = newNotifier(a, src, clk.Now)
	}
	srv := &Server{cfg: &Config{APIKey: testAPIKey}, perfStats: NewPerfStats(), auth: a}
	r := mux.NewRouter()
	srv.registerAuthRoutes(r)
	return &notifyFixture{authFixture: &authFixture{srv: srv, router: r, fake: fake, st: a.st}, n: a.notify, src: src, clk: clk}
}

// watcher registers and activates a user with default prefs watching pubkeys.
func (f *notifyFixture) watcher(t *testing.T, email, name string, pubkeys ...string) *client {
	t.Helper()
	c := f.registerAndActivate(t, email, name, pw)
	if _, err := f.st.NotifyPrefsFor(c.me.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := f.st.AddWatches(c.me.ID, pubkeys, 0); err != nil {
		t.Fatal(err)
	}
	return c
}

// setNode puts a node in the fake analyzer, last seen lastSeen before now.
func (f *notifyFixture) setNode(pk, name, role string, lastSeen time.Duration, battery *int) {
	f.src.nodeMap[pk] = notifyNode{Pubkey: pk, Name: name, Role: role, LastSeen: f.clk.t.Add(-lastSeen).Format(time.RFC3339), BatteryMv: battery}
}

func (f *notifyFixture) tick() { f.n.tick(context.Background()) }

func (f *notifyFixture) notifyMails() []mailer.Message {
	var out []mailer.Message
	for _, m := range f.fake.Sent() {
		if m.Tag == "notify" {
			out = append(out, m)
		}
	}
	return out
}

func TestNotifierMailsAChangeAfterTheSilentFirstTick(t *testing.T) {
	f := newNotifyFixture(t, defaultNotifySettings())
	pat := f.watcher(t, "pat@example.org", "Pat", evPkA)
	f.setNode(evPkA, "Alpha", "companion", time.Hour, nil)
	f.tick()
	if m := f.notifyMails(); len(m) != 0 {
		t.Fatalf("first tick mailed: %+v", m)
	}
	f.clk.Advance(25 * time.Hour)
	f.tick()
	m := f.notifyMails()
	if len(m) != 1 {
		t.Fatalf("mails = %d; want 1", len(m))
	}
	p, _ := f.st.NotifyPrefsFor(pat.me.ID)
	if m[0].To != "pat@example.org" || m[0].Subject != "[CoreScope] 1 change on your watched nodes" ||
		!strings.Contains(m[0].Text, "Alpha: offline, 2026-10-08 13:00 UTC") ||
		!strings.Contains(m[0].Text, testBase+"/#/nodes/"+evPkA) ||
		!strings.Contains(m[0].Text, testBase+"/#/account?section=notifications") ||
		!strings.Contains(m[0].Text, testBase+"/#/account/unsubscribe?token="+p.UnsubToken) {
		t.Fatalf("mail = %+v", m[0])
	}
	if m[0].Headers["List-Unsubscribe"] != "<"+testBase+"/api/notifications/unsubscribe?token="+p.UnsubToken+">" ||
		m[0].Headers["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" {
		t.Fatalf("headers = %v", m[0].Headers)
	}
	if total, per, err := f.st.NotifyMailCounts(f.clk.t.Add(-time.Hour)); err != nil || total != 1 || per[pat.me.ID] != 1 {
		t.Fatalf("mail_log notify rows = %d, %v, %v", total, per, err)
	}
	f.clk.Advance(5 * time.Minute)
	f.tick()
	if len(f.notifyMails()) != 1 {
		t.Fatal("an unchanged state was mailed again")
	}
	f.src.heard[evPkA] = f.clk.t.Add(-time.Minute)
	f.tick()
	if m := f.notifyMails(); len(m) != 2 || !strings.Contains(m[1].Text, "Alpha: back online") {
		t.Fatalf("back-online mail = %+v", m)
	}
}

func TestNotifierOneMailPerUserPerTick(t *testing.T) {
	f := newNotifyFixture(t, defaultNotifySettings())
	f.watcher(t, "pat@example.org", "Pat", evPkA, evPkB)
	f.watcher(t, "quinn@example.org", "Quinn", evPkA)
	f.setNode(evPkA, "Alpha", "companion", time.Hour, nil)
	f.setNode(evPkB, "Bravo", "companion", time.Hour, nil)
	f.tick()
	f.clk.Advance(25 * time.Hour)
	f.tick()
	m := f.notifyMails()
	if len(m) != 2 {
		t.Fatalf("mails = %d; want one per user", len(m))
	}
	byTo := map[string]mailer.Message{m[0].To: m[0], m[1].To: m[1]}
	pat, quinn := byTo["pat@example.org"], byTo["quinn@example.org"]
	if pat.Subject != "[CoreScope] 2 changes on your watched nodes" || !strings.Contains(pat.Text, "Alpha: offline") || !strings.Contains(pat.Text, "Bravo: offline") {
		t.Fatalf("Pat's mail = %+v", pat)
	}
	if quinn.Subject != "[CoreScope] 1 change on your watched nodes" || strings.Contains(quinn.Text, "Bravo") {
		t.Fatalf("Quinn's mail = %+v", quinn)
	}
}

func TestNotifierSkipsAreNeverMailedLater(t *testing.T) {
	cases := []struct {
		name  string
		tune  func(*notifySettings)
		block func(*notifyFixture, int64) error
		lift  func(*notifyFixture, int64) error
	}{
		{"notifications off", nil,
			func(f *notifyFixture, uid int64) error {
				_, err := f.st.SetNotifyPrefs(uid, false, users.NodeNotifyEvents)
				return err
			},
			func(f *notifyFixture, uid int64) error {
				_, err := f.st.SetNotifyPrefs(uid, true, users.NodeNotifyEvents)
				return err
			}},
		{"address bounces", nil,
			func(f *notifyFixture, uid int64) error { return f.st.SetEmailBouncing(uid, true) },
			func(f *notifyFixture, uid int64) error { return f.st.SetEmailBouncing(uid, false) }},
		{"account disabled", nil,
			func(f *notifyFixture, uid int64) error { return f.st.SetStatus(uid, users.StatusDisabled) },
			func(f *notifyFixture, uid int64) error { return f.st.SetStatus(uid, users.StatusActive) }},
		{"per-user limit", func(ns *notifySettings) { ns.perUserPerDay = 1 },
			func(f *notifyFixture, uid int64) error {
				_, err := f.st.LogMail(&uid, "pat@example.org", users.NotifyMailPurpose, "")
				return err
			},
			func(f *notifyFixture, uid int64) error { f.clk.Advance(25 * time.Hour); return nil }},
		{"instance limit", func(ns *notifySettings) { ns.maxMailsPerDay = 1 },
			func(f *notifyFixture, uid int64) error {
				_, err := f.st.LogMail(nil, "gone@example.org", users.NotifyMailPurpose, "")
				return err
			},
			func(f *notifyFixture, uid int64) error { f.clk.Advance(25 * time.Hour); return nil }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ns := defaultNotifySettings()
			if c.tune != nil {
				c.tune(&ns)
			}
			f := newNotifyFixture(t, ns)
			pat := f.watcher(t, "pat@example.org", "Pat", evPkA)
			f.setNode(evPkA, "Alpha", "companion", time.Hour, nil)
			f.tick()
			// Advance first, so a blocking mail_log row lies inside the
			// rolling 24 hours of the next tick.
			f.clk.Advance(25 * time.Hour)
			if err := c.block(f, pat.me.ID); err != nil {
				t.Fatal(err)
			}
			f.tick()
			if m := f.notifyMails(); len(m) != 0 {
				t.Fatalf("mailed while blocked: %+v", m)
			}
			if err := c.lift(f, pat.me.ID); err != nil {
				t.Fatal(err)
			}
			f.tick()
			if m := f.notifyMails(); len(m) != 0 {
				t.Fatalf("a skipped change was mailed later: %+v", m)
			}
		})
	}
}

func TestNotifierRestartAndNewWatchStaySilent(t *testing.T) {
	f := newNotifyFixture(t, defaultNotifySettings())
	pat := f.watcher(t, "pat@example.org", "Pat", evPkA)
	f.setNode(evPkA, "Alpha", "companion", time.Hour, nil)
	f.tick()
	f.clk.Advance(25 * time.Hour)
	f.tick()
	if len(f.notifyMails()) != 1 {
		t.Fatal("no mail for the change")
	}
	restarted := newNotifier(f.srv.auth, f.src, f.clk.Now) // the states live in users.db
	f.setNode(evPkB, "Bravo", "companion", 48*time.Hour, nil)
	if _, _, _, err := f.st.AddWatches(pat.me.ID, []string{evPkB}, 0); err != nil {
		t.Fatal(err)
	}
	f.clk.Advance(5 * time.Minute)
	restarted.tick(context.Background())
	if m := f.notifyMails(); len(m) != 1 {
		t.Fatalf("the restart or the new watch mailed: %+v", m[1:])
	}
}

func TestNotifierSendFailureIsNotRetried(t *testing.T) {
	f := newNotifyFixture(t, defaultNotifySettings())
	f.watcher(t, "pat@example.org", "Pat", evPkA)
	f.setNode(evPkA, "Alpha", "companion", time.Hour, nil)
	f.tick()
	f.fake.SetSendErr(errors.New("provider down"))
	f.clk.Advance(25 * time.Hour)
	f.tick()
	f.fake.SetSendErr(nil)
	f.clk.Advance(5 * time.Minute)
	f.tick()
	if m := f.notifyMails(); len(m) != 0 {
		t.Fatalf("a failed send was retried: %+v", m)
	}
	if total, _, _ := f.st.NotifyMailCounts(f.clk.t.Add(-48 * time.Hour)); total != 0 {
		t.Fatalf("mail_log has %d notify rows for a mail that never left", total)
	}
}

func TestNotifierStateWriteFailureSendsNothing(t *testing.T) {
	f := newNotifyFixture(t, defaultNotifySettings())
	f.watcher(t, "pat@example.org", "Pat", evPkA)
	f.setNode(evPkA, "Alpha", "companion", time.Hour, nil)
	f.tick()
	f.execDB(t, `CREATE TRIGGER notify_state_fail BEFORE UPDATE ON notification_state BEGIN SELECT RAISE(ABORT, 'boom'); END`)
	f.clk.Advance(25 * time.Hour)
	f.tick()
	if m := f.notifyMails(); len(m) != 0 {
		t.Fatalf("mailed although the states were not written: %+v", m)
	}
	f.execDB(t, `DROP TRIGGER notify_state_fail`)
	f.tick()
	if m := f.notifyMails(); len(m) != 1 {
		t.Fatalf("mails after the write works again = %d; want 1", len(m))
	}
}

func TestNotifierWaitsForTheAnalyzer(t *testing.T) {
	f := newNotifyFixture(t, defaultNotifySettings())
	f.watcher(t, "pat@example.org", "Pat", evPkA)
	f.setNode(evPkA, "Alpha", "companion", time.Hour, nil)
	f.src.isReady = false
	f.tick()
	if states, _ := f.st.AllNotifyStates(); len(states) != 0 {
		t.Fatalf("evaluated while the store was loading: %+v", states)
	}
	f.src.isReady = true
	f.tick()
	if states, _ := f.st.AllNotifyStates(); len(states) != 1 {
		t.Fatalf("states once ready = %+v", states)
	}
}

func TestNotifierPanicIsRecovered(t *testing.T) {
	f := newNotifyFixture(t, defaultNotifySettings())
	f.watcher(t, "pat@example.org", "Pat", evPkA)
	f.setNode(evPkA, "Alpha", "companion", time.Hour, nil)
	f.src.panicNext = true
	f.n.safeTick(context.Background())
	f.tick()
	if states, _ := f.st.AllNotifyStates(); len(states) != 1 {
		t.Fatalf("the tick after the panic did not run: %+v", states)
	}
}

func TestNotifierReadsForeignAndObserversOnlyForAdmins(t *testing.T) {
	f := newNotifyFixture(t, defaultNotifySettings())
	f.watcher(t, "pat@example.org", "Pat", evPkA)
	f.setNode(evPkA, "Alpha", "companion", time.Hour, nil)
	f.tick()
	if f.src.foreignAsked != 0 || f.src.observersAsked != 0 {
		t.Fatalf("admin data read without an admin asking: foreign %d, observers %d", f.src.foreignAsked, f.src.observersAsked)
	}
	boss := f.registerAndActivate(t, "admin@example.org", "Boss", pw)
	if _, err := f.st.SetNotifyPrefs(boss.me.ID, true, []string{users.NotifyForeignNew}); err != nil {
		t.Fatal(err)
	}
	f.tick()
	if f.src.foreignAsked != 1 || f.src.observersAsked != 0 {
		t.Fatalf("foreign %d, observers %d; want 1, 0", f.src.foreignAsked, f.src.observersAsked)
	}
	if err := f.st.SetRole(boss.me.ID, users.RoleUser); err != nil {
		t.Fatal(err)
	}
	f.tick()
	if f.src.foreignAsked != 1 {
		t.Fatal("foreign nodes read for a demoted admin")
	}
}

func TestNotifyMailEscapesNodeNames(t *testing.T) {
	f := newNotifyFixture(t, defaultNotifySettings())
	f.watcher(t, "pat@example.org", "Pat", evPkA)
	f.setNode(evPkA, `<b>Bad</b> & "co"`, "companion", time.Hour, nil)
	f.tick()
	f.clk.Advance(25 * time.Hour)
	f.tick()
	m := f.notifyMails()
	if len(m) != 1 || strings.Contains(m[0].HTML, "<b>Bad") || !strings.Contains(m[0].HTML, "&lt;b&gt;Bad&lt;/b&gt; &amp; &#34;co&#34;") {
		t.Fatalf("HTML = %q", m[0].HTML)
	}
}

// Names come from whoever owns a node or runs an observer: a line break
// must not forge a line in the mail, a bidi override must not reorder it.
func TestNotifyMailSanitisesNames(t *testing.T) {
	f := newNotifyFixture(t, defaultNotifySettings())
	f.watcher(t, "pat@example.org", "Pat", evPkA)
	admin := f.registerAndActivate(t, "admin@example.org", "Ada", pw)
	if _, err := f.st.SetNotifyPrefs(admin.me.ID, true, []string{users.NotifyObserverOffline}); err != nil {
		t.Fatal(err)
	}
	f.setNode(evPkA, "x\r\nAccount suspended, sign in: https://evil.example\t\u202egnp.exe", "companion", time.Hour, nil)
	seen := f.clk.t.Add(-time.Minute).Format(time.RFC3339)
	f.src.obs = []notifyObserver{{ID: "OBS1", Name: "Roof\n\u2066Fake line\u2069", LastSeen: seen}, {ID: "OBS2", Name: "\n\u202e", LastSeen: seen}}
	f.tick()
	f.clk.Advance(25 * time.Hour)
	f.tick()
	m := f.notifyMails()
	if len(m) != 2 {
		t.Fatalf("mails = %d, want 2", len(m))
	}
	for _, msg := range m {
		for _, part := range []string{msg.Text, msg.HTML} {
			for _, bad := range []string{"\r", "\t", "\u202e", "\u2066", "\u2069", "\nAccount", "\nFake"} {
				if strings.Contains(part, bad) {
					t.Fatalf("mail part contains %q:\n%s", bad, part)
				}
			}
		}
	}
	body := m[0].Text + m[1].Text
	for _, want := range []string{"- x Account suspended, sign in: https://evil.example gnp.exe: offline,",
		"- Roof Fake line: observer offline,", "- OBS2: observer offline,"} {
		if !strings.Contains(body, want) {
			t.Fatalf("text lacks %q:\n%s", want, body)
		}
	}
}

func TestMailSafeText(t *testing.T) {
	cases := map[string]string{
		"plain":                  "plain",
		"  a \n\n b\r\tc  ":      "a b c",
		"a\u202eb\u2066c\u2069d": "a b c d",
		"a\u202ab\u202dc":        "a b c",
		"a\u0085b\x7fc":          "a b c",
		"\n\u202a":               "",
		"Zo\u00eb \u00e9":        "Zo\u00eb \u00e9",
	}
	for in, want := range cases {
		if got := mailSafeText(in); got != want {
			t.Errorf("mailSafeText(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestNotifyChangeText(t *testing.T) {
	at := time.Date(2026, 10, 7, 9, 5, 0, 0, time.UTC)
	cases := []struct {
		c    notifyChange
		want string
	}{
		{notifyChange{Event: users.NotifyNodeOffline, Name: "A", To: users.NotifyBad, At: at}, "A: offline, 2026-10-07 09:05 UTC"},
		{notifyChange{Event: users.NotifyNodeOffline, Name: "A", To: users.NotifyGood, At: at}, "A: back online, 2026-10-07 09:05 UTC"},
		{notifyChange{Event: users.NotifyNodeBattery, Name: "A", To: users.NotifyBad, BatteryMv: evMv(3250), At: at}, "A: battery low (3250 mV), 2026-10-07 09:05 UTC"},
		{notifyChange{Event: users.NotifyNodeBattery, Name: "A", To: users.NotifyGood, BatteryMv: evMv(3410), At: at}, "A: battery recovered (3410 mV), 2026-10-07 09:05 UTC"},
		{notifyChange{Event: users.NotifyForeignNew, Name: "F", To: users.NotifyTold, At: at}, "F: new foreign node, 2026-10-07 09:05 UTC"},
		{notifyChange{Event: users.NotifyObserverOffline, Name: "O", To: users.NotifyBad, At: at}, "O: observer offline, 2026-10-07 09:05 UTC"},
		{notifyChange{Event: users.NotifyObserverOffline, Name: "O", To: users.NotifyGood, At: at}, "O: observer back online, 2026-10-07 09:05 UTC"},
	}
	for _, c := range cases {
		if got := notifyChangeText(c.c); got != c.want {
			t.Errorf("%q; want %q", got, c.want)
		}
	}
	if got := notifySubjectURL("https://e.org", notifyChange{Event: users.NotifyObserverOffline, Subject: "AB CD"}); got != "https://e.org/#/observers/AB%20CD" {
		t.Errorf("observer URL = %q", got)
	}
	if got := notifySubjectURL("https://e.org", notifyChange{Event: users.NotifyNodeOffline, Subject: evPkA}); got != "https://e.org/#/nodes/"+evPkA {
		t.Errorf("node URL = %q", got)
	}
}

func TestNotifySkipReason(t *testing.T) {
	ns := defaultNotifySettings()
	ok := users.User{Status: users.StatusActive}
	on := users.NotifyPrefs{Enabled: true}
	cases := []struct {
		u           users.User
		p           users.NotifyPrefs
		mine, total int
		want        string
	}{
		{ok, on, 0, 0, ""},
		{users.User{Status: users.StatusDisabled}, on, 0, 0, "account not active"},
		{users.User{Status: users.StatusActive, EmailBouncing: true}, on, 0, 0, "address bounces"},
		{ok, users.NotifyPrefs{}, 0, 0, "notifications off"},
		{ok, on, 20, 0, "daily limit for this user reached"},
		{ok, on, 19, 300, "daily limit for this instance reached"},
		{ok, on, 19, 299, ""},
	}
	for _, c := range cases {
		if got := notifySkipReason(c.u, c.p, c.mine, c.total, ns); got != c.want {
			t.Errorf("%+v %+v %d %d = %q; want %q", c.u, c.p, c.mine, c.total, got, c.want)
		}
	}
}

func TestInitUserManagementStartsTheNotifierOnlyWhenEnabled(t *testing.T) {
	for _, on := range []bool{false, true} {
		um := &UserManagementConfig{Enabled: true, PublicBaseURL: testBase,
			Mail: UserMailConfig{BrevoAPIKey: "k", FromEmail: "noreply@example.org"}}
		if on {
			um.Notifications = &NotificationsConfig{Enabled: true}
		}
		srv := &Server{cfg: &Config{UserManagement: um}}
		if err := srv.initUserManagement(filepath.Join(t.TempDir(), "meshcore.db")); err != nil {
			t.Fatal(err)
		}
		if (srv.auth.notify != nil) != on {
			t.Fatalf("notifications %v: notifier = %v", on, srv.auth.notify)
		}
		done := make(chan struct{})
		go func() { srv.closeUserManagement(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("closeUserManagement did not stop the notifier")
		}
	}
}

func TestNotifierDeletesAdminStatesOfADemotedAdmin(t *testing.T) {
	f := newNotifyFixture(t, defaultNotifySettings())
	boss := f.registerAndActivate(t, "admin@example.org", "Boss", pw)
	if _, err := f.st.SetNotifyPrefs(boss.me.ID, true, []string{users.NotifyForeignNew}); err != nil {
		t.Fatal(err)
	}
	f.src.nodeMap[evPkB] = notifyNode{Pubkey: evPkB, Name: "Far", Foreign: true}
	f.tick()
	if states, _ := f.st.AllNotifyStates(); len(states) != 2 {
		t.Fatalf("baseline states = %+v; want the sentinel and Far", states)
	}
	if err := f.st.SetRole(boss.me.ID, users.RoleUser); err != nil {
		t.Fatal(err)
	}
	f.tick()
	if states, _ := f.st.AllNotifyStates(); len(states) != 0 {
		t.Fatalf("states of a demoted admin = %+v; want none", states)
	}
	if err := f.st.SetRole(boss.me.ID, users.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	f.tick()
	if m := f.notifyMails(); len(m) != 0 {
		t.Fatalf("re-promotion mailed: %+v", m)
	}
}

func TestNotifierMailsAUserWithoutAPrefsRow(t *testing.T) {
	f := newNotifyFixture(t, defaultNotifySettings())
	pat := f.registerAndActivate(t, "pat@example.org", "Pat", pw)
	if _, _, _, err := f.st.AddWatches(pat.me.ID, []string{evPkA}, 0); err != nil {
		t.Fatal(err)
	}
	f.setNode(evPkA, "Alpha", "companion", time.Hour, nil)
	f.tick()
	if states, _ := f.st.AllNotifyStates(); len(states) != 1 {
		t.Fatalf("states = %+v; want the default node.offline baseline", states)
	}
	if prefs, _ := f.st.AllNotifyPrefs(); len(prefs) != 0 {
		t.Fatalf("a silent tick created prefs: %+v", prefs)
	}
	f.clk.Advance(25 * time.Hour)
	f.tick()
	m := f.notifyMails()
	p, err := f.st.NotifyPrefsFor(pat.me.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(m) != 1 || p.UnsubToken == "" || !strings.Contains(m[0].Text, "unsubscribe?token="+p.UnsubToken) ||
		m[0].Headers["List-Unsubscribe"] != "<"+testBase+"/api/notifications/unsubscribe?token="+p.UnsubToken+">" {
		t.Fatalf("mail = %+v, prefs = %+v", m, p)
	}
}

func TestNotifierNormalisesMixedCaseWatches(t *testing.T) {
	f := newNotifyFixture(t, defaultNotifySettings())
	pat := f.watcher(t, "pat@example.org", "Pat", strings.ToUpper(evPkA))
	if _, _, _, err := f.st.AddWatches(pat.me.ID, []string{evPkA}, 0); err != nil {
		t.Fatal(err)
	}
	f.setNode(evPkA, "Alpha", "companion", time.Hour, nil) // keyed lowercase, like NotifyNodes
	f.src.heard[evPkA] = f.clk.t.Add(-time.Minute)
	f.tick()
	if len(f.src.heardAsked) != 1 || f.src.heardAsked[0] != evPkA {
		t.Fatalf("last heard asked for %v; want [%s]", f.src.heardAsked, evPkA)
	}
	states, _ := f.st.AllNotifyStates()
	if len(states) != 1 || states[0].Subject != evPkA || states[0].State != users.NotifyGood {
		t.Fatalf("states = %+v; want one good row under the lowercase key", states)
	}
	delete(f.src.heard, evPkA)
	f.clk.Advance(25 * time.Hour)
	f.tick()
	if m := f.notifyMails(); len(m) != 1 || !strings.Contains(m[0].Text, "Alpha: offline") ||
		strings.Count(m[0].Text, "Alpha:") != 1 {
		t.Fatalf("mail = %+v; want one Alpha offline line", m)
	}
}

// cancelAfterSend cancels the tick's context once a mail went out, like a
// shutdown arriving mid-delivery.
type cancelAfterSend struct {
	mailer.Mailer
	cancel context.CancelFunc
}

func (c cancelAfterSend) Send(ctx context.Context, m mailer.Message) (string, error) {
	id, err := c.Mailer.Send(ctx, m)
	c.cancel()
	return id, err
}

func TestNotifierStopsDeliveringAfterShutdown(t *testing.T) {
	f := newNotifyFixture(t, defaultNotifySettings())
	f.watcher(t, "pat@example.org", "Pat", evPkA)
	f.watcher(t, "quinn@example.org", "Quinn", evPkA)
	f.setNode(evPkA, "Alpha", "companion", time.Hour, nil)
	f.tick()
	f.clk.Advance(25 * time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.n.a.mail = cancelAfterSend{Mailer: f.n.a.mail, cancel: cancel}
	f.n.tick(ctx)
	if m := f.notifyMails(); len(m) != 1 {
		t.Fatalf("mails after a shutdown mid-delivery = %d; want 1", len(m))
	}
}

func TestNotifierPausesOfflineChecksWhileIngestIsStale(t *testing.T) {
	f := newNotifyFixture(t, defaultNotifySettings())
	f.watcher(t, "pat@example.org", "Pat", evPkA)
	f.setNode(evPkA, "Alpha", "companion", time.Hour, nil)
	f.tick() // baseline: online
	lastPacket := f.clk.t
	f.src.newestAt = func() time.Time { return lastPacket }

	logs := captureLog(func() {
		f.clk.Advance(25 * time.Hour) // feed down for a day: Alpha looks offline
		f.tick()
		f.clk.Advance(5 * time.Minute)
		f.tick()
	})
	if m := f.notifyMails(); len(m) != 0 {
		t.Fatalf("mailed %d while ingest is stale", len(m))
	}
	paused := "[notify] ingest stale since " + lastPacket.UTC().Format(time.RFC3339) + "; offline checks paused"
	if n := strings.Count(logs, paused); n != 1 {
		t.Fatalf("pause logged %d times; want once:\n%s", n, logs)
	}

	f.src.newestAt = f.clk.Now // feed back, Alpha still silent
	logs = captureLog(f.tick)
	if m := f.notifyMails(); len(m) != 1 || !strings.Contains(m[0].Text, "Alpha: offline") {
		t.Fatalf("mails after ingest resumed = %+v; want Alpha offline", m)
	}
	if n := strings.Count(logs, "[notify] ingest fresh again; offline checks resumed"); n != 1 {
		t.Fatalf("resume logged %d times; want once:\n%s", n, logs)
	}
}

func TestNotifierTreatsAnEmptyStoreAsStale(t *testing.T) {
	f := newNotifyFixture(t, defaultNotifySettings())
	f.watcher(t, "pat@example.org", "Pat", evPkA)
	f.setNode(evPkA, "Alpha", "companion", time.Hour, nil)
	f.src.newestAt = nil
	if logs := captureLog(f.tick); !strings.Contains(logs, "[notify] ingest stale since (no packets); offline checks paused") {
		t.Fatalf("log = %q", logs)
	}
	if s, err := f.st.AllNotifyStates(); err != nil || len(s) != 0 {
		t.Fatalf("states with no packets in the store = %+v, %v", s, err)
	}
}
