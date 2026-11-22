package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/meshcore-analyzer/users"
)

func pushoverOutbox(m ...users.PushoverMessage) users.NotifyOutbox {
	return users.NotifyOutbox{PushoverMessages: m}
}

var (
	testPushoverToken = strings.Repeat("a", 30)
	testPushoverKey   = strings.Repeat("u", 30)
	testPushoverOther = strings.Repeat("v", 30)
)

// fakePushover answers like api.pushover.net for testPushoverKey; any
// other key is unknown. mode changes how messages are answered.
type fakePushover struct {
	mu       sync.Mutex
	mode     string // "", "500", "429", "400"
	messages []map[string]string
	srv      *httptest.Server
}

func newFakePushover(t *testing.T) *fakePushover {
	f := &fakePushover{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.PostForm.Get("token") != testPushoverToken {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{"status": 0, "token": "invalid", "errors": []string{"application token is invalid"}})
			return
		}
		user := r.PostForm.Get("user")
		if user != testPushoverKey {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]any{"status": 0, "user": "invalid", "errors": []string{"user identifier is not a valid user"}})
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path == "/messages.json" {
			switch f.mode {
			case "500":
				w.WriteHeader(http.StatusInternalServerError)
				return
			case "429":
				w.WriteHeader(http.StatusTooManyRequests)
				json.NewEncoder(w).Encode(map[string]any{"status": 0, "errors": []string{"application over limit"}})
				return
			case "400":
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]any{"status": 0, "message": "invalid", "errors": []string{"message is too long"}})
				return
			}
			msg := map[string]string{}
			for k := range r.PostForm {
				msg[k] = r.PostForm.Get(k)
			}
			f.messages = append(f.messages, msg)
		}
		json.NewEncoder(w).Encode(map[string]any{"status": 1, "request": "req"})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePushover) setMode(m string) { f.mu.Lock(); f.mode = m; f.mu.Unlock() }

func (f *fakePushover) sent() []map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]string(nil), f.messages...)
}

func pushoverNotifySettings(fake *fakePushover, maxPerMonth int) notifySettings {
	ns := defaultNotifySettings()
	ns.pushover = &pushoverSettings{client: newPushoverClient(fake.srv.URL, testPushoverToken, nil), maxPerMonth: maxPerMonth}
	return ns
}

func pushoverDest(n *notifier) *pushoverDestination {
	for _, d := range n.destinations {
		if p, ok := d.(*pushoverDestination); ok {
			return p
		}
	}
	panic("notifier has no Pushover destination")
}

func TestResolvePushover(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == pushoverTokenEnv {
				return v
			}
			return ""
		}
	}
	if ps, err := resolvePushover(nil, env(testPushoverToken)); ps != nil || err != nil {
		t.Fatalf("no block: %v, %v", ps, err)
	}
	for _, token := range []string{"", "short", strings.Repeat("a", 29) + "!"} {
		if _, err := resolvePushover(&PushoverConfig{}, env(token)); err == nil {
			t.Errorf("accepted token %q", token)
		}
	}
	if _, err := resolvePushover(&PushoverConfig{MaxPerMonth: -1}, env(testPushoverToken)); err == nil {
		t.Error("accepted a negative cap")
	}
	ps, err := resolvePushover(&PushoverConfig{}, env(" "+testPushoverToken+" "))
	if err != nil || ps.maxPerMonth != defaultPushoverMaxMonthly || ps.client.token != testPushoverToken {
		t.Fatalf("default: %+v, %v", ps, err)
	}
}

func TestPushoverMessageText(t *testing.T) {
	f := newNotifyFixture(t, defaultNotifySettings())
	at := time.Date(2026, 10, 8, 13, 0, 0, 0, time.UTC)
	one := []notifyChange{{Event: "node.offline", Subject: evPkA, Name: "Alpha‮", To: "bad", At: at}}
	m := f.srv.auth.pushoverMessage(7, one, at)
	if base, _ := url.Parse(testBase); m.Title != base.Host {
		t.Fatalf("title = %q", m.Title)
	}
	if m.Message != "Alpha : offline, 2026-10-08 13:00 UTC" || m.URL != testBase+"/#/nodes/"+evPkA || !m.ExpiresAt.Equal(at.Add(time.Hour)) {
		t.Fatalf("one change: %+v", m)
	}
	var many []notifyChange
	for i := 0; i < 60; i++ {
		many = append(many, notifyChange{Event: "node.offline", Subject: evPkA, Name: strings.Repeat("N", 30), To: "bad", At: at})
	}
	m = f.srv.auth.pushoverMessage(7, many, at)
	if n := utf8.RuneCountInString(m.Message); n > pushoverMaxMessage || !strings.Contains(m.Message, "more") {
		t.Fatalf("many changes: %d characters, ends %q", n, m.Message[len(m.Message)-20:])
	}
	if m.URL != testBase+"/#/account?section=notifications" {
		t.Fatalf("many changes URL = %q", m.URL)
	}
}

// Pushover is independent of the email toggle and goes through the queue
// to the user's key.
func TestPushoverDeliversWithEmailOff(t *testing.T) {
	fake := newFakePushover(t)
	f := newNotifyFixture(t, pushoverNotifySettings(fake, 100))
	pat := f.watcher(t, "pat@example.org", "Pat", evPkA)
	if _, err := f.st.SetNotifyPrefs(pat.me.ID, false, []string{"node.offline"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.SetPushoverKey(pat.me.ID, testPushoverKey); err != nil {
		t.Fatal(err)
	}
	f.setNode(evPkA, "Alpha", "companion", time.Hour, nil)
	f.tick()
	f.clk.Advance(25 * time.Hour)
	f.src.newestAt = f.clk.Now
	f.tick()
	if len(f.notifyMails()) != 0 {
		t.Fatal("mailed a user with email off")
	}
	if err := pushoverDest(f.n).dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	sent := fake.sent()
	if len(sent) != 1 || sent[0]["user"] != testPushoverKey || !strings.Contains(sent[0]["message"], "Alpha: offline") ||
		sent[0]["url"] != testBase+"/#/nodes/"+evPkA {
		t.Fatalf("sent = %+v", sent)
	}
	if n, _ := f.st.PushoverSentSince(f.clk.Now().Add(-time.Hour)); n != 1 {
		t.Fatalf("sends counted = %d", n)
	}
	if jobs, _ := f.st.DuePushoverJobs(f.clk.Now(), 10); len(jobs) != 0 {
		t.Fatal("a sent message stayed queued")
	}
}

func queuePushoverJob(t *testing.T, f *notifyFixture, uid int64) {
	t.Helper()
	m := f.srv.auth.pushoverMessage(uid, []notifyChange{{Event: "node.offline", Subject: evPkA, Name: "Alpha", To: "bad", At: f.clk.Now()}}, f.clk.Now())
	if err := f.st.ApplyNotifyChanges(nil, nil, pushoverOutbox(m)); err != nil {
		t.Fatal(err)
	}
}

func TestPushoverRetryDropAndRejectedKey(t *testing.T) {
	fake := newFakePushover(t)
	f := newNotifyFixture(t, pushoverNotifySettings(fake, 100))
	pat := f.registerAndActivate(t, "pat@example.org", "Pat", pw)
	if _, err := f.st.SetPushoverKey(pat.me.ID, testPushoverKey); err != nil {
		t.Fatal(err)
	}
	d := pushoverDest(f.n)
	ctx := context.Background()

	fake.setMode("500")
	queuePushoverJob(t, f, pat.me.ID)
	if err := d.dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if jobs, _ := f.st.DuePushoverJobs(f.clk.Now().Add(6*time.Second), 10); len(jobs) != 1 || jobs[0].Attempts != 1 {
		t.Fatalf("a 500 was not retried: %+v", jobs)
	}
	fake.setMode("")
	f.clk.Advance(6 * time.Second)
	if err := d.dispatch(ctx); err != nil || len(fake.sent()) != 1 {
		t.Fatalf("retry not sent: %v, %d", err, len(fake.sent()))
	}

	fake.setMode("400")
	queuePushoverJob(t, f, pat.me.ID)
	logs := captureLog(func() {
		if err := d.dispatch(ctx); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(logs, "drop Pushover message") {
		t.Fatalf("a 400 was not dropped and logged: %q", logs)
	}
	if jobs, _ := f.st.DuePushoverJobs(f.clk.Now().Add(time.Hour-time.Second), 10); len(jobs) != 0 {
		t.Fatal("a 400 stayed queued")
	}

	fake.setMode("")
	if _, err := f.st.SetPushoverKey(pat.me.ID, testPushoverOther); err != nil {
		t.Fatal(err)
	}
	queuePushoverJob(t, f, pat.me.ID)
	if err := d.dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	if p, _, _ := f.st.PushoverFor(pat.me.ID); p.Enabled {
		t.Fatal("a rejected key stayed on")
	}
}

func TestPushoverMonthlyCapHoldsSends(t *testing.T) {
	fake := newFakePushover(t)
	f := newNotifyFixture(t, pushoverNotifySettings(fake, 1))
	pat := f.registerAndActivate(t, "pat@example.org", "Pat", pw)
	if _, err := f.st.SetPushoverKey(pat.me.ID, testPushoverKey); err != nil {
		t.Fatal(err)
	}
	if err := f.st.RecordPushoverSend(); err != nil {
		t.Fatal(err)
	}
	queuePushoverJob(t, f, pat.me.ID)
	d := pushoverDest(f.n)
	logs := captureLog(func() {
		for i := 0; i < 2; i++ {
			if err := d.dispatch(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
	})
	if len(fake.sent()) != 0 || strings.Count(logs, "monthly cap") != 1 {
		t.Fatalf("capped: sent %d, log %q", len(fake.sent()), logs)
	}
	f.clk.Advance(31 * 24 * time.Hour)
	if err := d.dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if jobs, _ := f.st.DuePushoverJobs(f.clk.Now(), 10); len(jobs) != 0 {
		t.Fatal("an expired capped message stayed queued")
	}
}

func TestPushoverAccountRoutes(t *testing.T) {
	fake := newFakePushover(t)
	f := newNotifyFixture(t, pushoverNotifySettings(fake, 100))
	pat := f.registerAndActivate(t, "pat@example.org", "Pat", pw)
	const path = "/api/account/notifications/pushover"
	state := func(w *httptest.ResponseRecorder) notifyAccountJSON {
		t.Helper()
		var out notifyAccountJSON
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.Pushover == nil {
			t.Fatalf("HTTP %d %s: %v", w.Code, w.Body.String(), err)
		}
		return out
	}
	if got := state(f.do(http.MethodGet, "/api/account/notifications", nil, as(pat))); got.Pushover.Configured {
		t.Fatalf("new account: %+v", got.Pushover)
	}
	key := func(k string) map[string]any { return map[string]any{"userKey": k} }
	if w := f.do(http.MethodPut, path, key("short"), as(pat)); w.Code != http.StatusBadRequest {
		t.Fatalf("malformed key: %d", w.Code)
	}
	if w := f.do(http.MethodPut, path, key(testPushoverOther), as(pat)); w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), "doesn't recognize") {
		t.Fatalf("unknown key: %d %s", w.Code, w.Body.String())
	}
	if w := f.do(http.MethodPost, path+"/test", nil, as(pat)); w.Code != http.StatusConflict {
		t.Fatalf("test without a key: %d", w.Code)
	}
	got := state(f.do(http.MethodPut, path, key(" "+testPushoverKey+" "), as(pat)))
	if !got.Pushover.Configured || !got.Pushover.Enabled || got.Pushover.KeyHint != "uuuu" {
		t.Fatalf("saved: %+v", got.Pushover)
	}
	if strings.Contains(f.do(http.MethodGet, "/api/account/notifications", nil, as(pat)).Body.String(), testPushoverKey) {
		t.Fatal("the account state exposed the whole key")
	}
	if w := f.do(http.MethodPost, path+"/test", nil, as(pat)); w.Code != http.StatusNoContent || len(fake.sent()) != 1 ||
		!strings.Contains(fake.sent()[0]["message"], "Test from") {
		t.Fatalf("test: %d, %+v", w.Code, fake.sent())
	}
	if got := state(f.do(http.MethodPut, path, map[string]any{"enabled": false}, as(pat))); got.Pushover.Enabled {
		t.Fatal("turning off kept it on")
	}
	if w := f.do(http.MethodPost, path+"/test", nil, as(pat)); w.Code != http.StatusConflict {
		t.Fatalf("test while off: %d", w.Code)
	}
	if got := state(f.do(http.MethodDelete, path, nil, as(pat))); got.Pushover.Configured {
		t.Fatal("removing kept the key")
	}
	if w := f.do(http.MethodPut, path, map[string]any{"enabled": true}, as(pat)); w.Code != http.StatusConflict {
		t.Fatalf("enable without a key: %d", w.Code)
	}

	plain := newNotifyFixture(t, defaultNotifySettings())
	quinn := plain.registerAndActivate(t, "quinn@example.org", "Quinn", pw)
	if strings.Contains(plain.do(http.MethodGet, "/api/account/notifications", nil, as(quinn)).Body.String(), "pushover") {
		t.Fatal("Pushover offered without configuration")
	}
	if w := plain.do(http.MethodPut, path, key(testPushoverKey), as(quinn)); w.Code == http.StatusOK {
		t.Fatal("Pushover route answered without configuration")
	}
}
