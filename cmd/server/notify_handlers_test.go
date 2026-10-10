package main

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/users"
)

func notifyPath(pk string) string { return "/api/account/notifications/watches/" + pk }

func TestNotifyRoutesAbsentWhenOff(t *testing.T) {
	f := newNotifyFixture(t, notifySettings{})
	for _, rt := range []struct{ method, path string }{
		{"GET", "/api/account/notifications"}, {"PUT", "/api/account/notifications"},
		{"PUT", notifyPath(evPkA)}, {"DELETE", notifyPath(evPkA)},
		{"POST", "/api/account/notifications/watch-my-nodes"},
		{"GET", "/api/notifications/unsubscribe?token=x"}, {"POST", "/api/notifications/unsubscribe?token=x"},
	} {
		if w := f.do(rt.method, rt.path, nil); w.Code != 404 {
			t.Errorf("%s %s = %d with notifications off; want 404", rt.method, rt.path, w.Code)
		}
	}
}

func TestNotifyGetDefaultsAndAdminEvents(t *testing.T) {
	f := newNotifyFixture(t, defaultNotifySettings())
	boss := f.registerAndActivate(t, "admin@example.org", "Boss", pw)
	pat := f.registerAndActivate(t, "pat@example.org", "Pat", pw)
	expectStatus(t, f.do("GET", "/api/account/notifications", nil), 401)
	w := f.do("GET", "/api/account/notifications", nil, as(pat))
	expectStatus(t, w, 200)
	want := notifyAccountJSON{Enabled: true, Events: []string{"node.offline", "node.battery"}, AvailableEvents: []string{"node.offline", "node.battery"},
		Watches: []notifyWatchJSON{}, Limits: notifyLimitsJSON{MaxWatches: 50, PerUserPerDay: 20}}
	if got := decode[notifyAccountJSON](t, w); !reflect.DeepEqual(got, want) {
		t.Fatalf("user defaults = %+v; want %+v", got, want)
	}
	if !strings.Contains(w.Body.String(), `"watches":[]`) {
		t.Fatalf("empty watch list is not an array: %s", w.Body.String())
	}
	adm := decode[notifyAccountJSON](t, f.do("GET", "/api/account/notifications", nil, as(boss)))
	if !reflect.DeepEqual(adm.AvailableEvents, []string{"node.offline", "node.battery", "foreign.new", "observer.offline"}) ||
		!reflect.DeepEqual(adm.Events, []string{"node.offline", "node.battery"}) {
		t.Fatalf("admin = %+v", adm)
	}
}

func TestNotifyPutPrefs(t *testing.T) {
	f := newNotifyFixture(t, defaultNotifySettings())
	boss := f.registerAndActivate(t, "admin@example.org", "Boss", pw)
	pat := f.registerAndActivate(t, "pat@example.org", "Pat", pw)
	put := func(c *client, body any) *httptest.ResponseRecorder {
		return f.do("PUT", "/api/account/notifications", body, as(c))
	}
	expectStatus(t, put(&client{cookie: pat.cookie}, notifyPrefsRequest{Enabled: true}), 403)
	w := put(pat, notifyPrefsRequest{Enabled: false, Events: []string{"node.battery"}})
	expectStatus(t, w, 200)
	if got := decode[notifyAccountJSON](t, w); got.Enabled || !reflect.DeepEqual(got.Events, []string{"node.battery"}) {
		t.Fatalf("after PUT = %+v", got)
	}
	entries, err := f.st.AuditList(users.AuditFilter{Actions: []string{"notify.prefs"}})
	if err != nil || len(entries) != 1 || *entries[0].ActorUserID != pat.me.ID || entries[0].Detail["enabled"] != "false" || entries[0].Detail["events"] != "node.battery" {
		t.Fatalf("audit = %+v, %v", entries, err)
	}
	if w := put(pat, notifyPrefsRequest{Enabled: true, Events: []string{"node.reboot"}}); w.Code != 400 || !strings.Contains(w.Body.String(), "unknown event type") {
		t.Errorf("unknown event: %d %s", w.Code, w.Body.String())
	}
	if w := put(pat, notifyPrefsRequest{Enabled: true, Events: []string{"foreign.new"}}); w.Code != 403 || !strings.Contains(w.Body.String(), "only admins") {
		t.Errorf("admin event by a user: %d %s", w.Code, w.Body.String())
	}
	if w := put(pat, map[string]any{"enabled": true, "x": 1}); w.Code != 400 {
		t.Errorf("unknown field: %d", w.Code)
	}
	w = put(boss, notifyPrefsRequest{Enabled: true, Events: []string{"observer.offline", "foreign.new"}})
	if got := decode[notifyAccountJSON](t, w); w.Code != 200 || !reflect.DeepEqual(got.Events, []string{"foreign.new", "observer.offline"}) {
		t.Fatalf("admin PUT: %d %+v", w.Code, got)
	}
}

func TestWatchPutAndDelete(t *testing.T) {
	ns := defaultNotifySettings()
	ns.maxWatchesPerUser = 1
	f := newNotifyFixture(t, ns)
	f.setNode(evPkA, "Alpha", "repeater", time.Hour, nil)
	f.setNode(evPkB, "Bravo", "companion", time.Hour, nil)
	pat := f.registerAndActivate(t, "pat@example.org", "Pat", pw)
	expectStatus(t, f.do("PUT", notifyPath(evPkA), nil), 401)
	expectStatus(t, f.do("PUT", notifyPath(evPkA), nil, as(&client{cookie: pat.cookie})), 403)
	for _, bad := range []string{"xyz", evPkA[:63], evPkA + "0", strings.Repeat("g", 64)} {
		if w := f.do("PUT", notifyPath(bad), nil, as(pat)); w.Code != 400 {
			t.Errorf("PUT %q = %d; want 400", bad, w.Code)
		}
	}
	if w := f.do("PUT", notifyPath(evPkG), nil, as(pat)); w.Code != 404 || !strings.Contains(w.Body.String(), "node not found") {
		t.Fatalf("unknown node: %d %s", w.Code, w.Body.String())
	}
	w := f.do("PUT", notifyPath(strings.ToUpper(evPkA)), nil, as(pat))
	expectStatus(t, w, 200)
	got := decode[notifyAccountJSON](t, w)
	if len(got.Watches) != 1 || got.Watches[0].Pubkey != evPkA || got.Watches[0].Name != "Alpha" || !got.Watches[0].Known {
		t.Fatalf("after watching = %+v", got.Watches)
	}
	expectStatus(t, f.do("PUT", notifyPath(evPkA), nil, as(pat)), 200)
	if w := f.do("PUT", notifyPath(evPkB), nil, as(pat)); w.Code != 409 || !strings.Contains(w.Body.String(), "you watch the maximum of 1 nodes") {
		t.Fatalf("over the limit: %d %s", w.Code, w.Body.String())
	}
	delete(f.src.nodeMap, evPkA) // the node left the analyzer database
	if got := decode[notifyAccountJSON](t, f.do("GET", "/api/account/notifications", nil, as(pat))); got.Watches[0].Known || got.Watches[0].Name != "" {
		t.Fatalf("vanished node = %+v", got.Watches[0])
	}
	w = f.do("DELETE", notifyPath(evPkA), nil, as(pat))
	expectStatus(t, w, 200)
	if got := decode[notifyAccountJSON](t, w); len(got.Watches) != 0 {
		t.Fatalf("after DELETE = %+v", got.Watches)
	}
	expectStatus(t, f.do("DELETE", notifyPath(evPkA), nil, as(pat)), 200)
	expectStatus(t, f.do("DELETE", notifyPath("xyz"), nil, as(pat)), 400)
	if entries, _ := f.st.AuditList(users.AuditFilter{Actions: []string{"notify.*"}}); len(entries) != 0 {
		t.Fatalf("watch changes were audited: %+v", entries)
	}
}

func TestMyNodesPubkeys(t *testing.T) {
	for _, doc := range []string{"", "not json", `{"v":1,"keys":{}}`, `{"v":1,"keys":{"meshcore-my-nodes":"not json"}}`} {
		if got := myNodesPubkeys(doc); got != nil {
			t.Errorf("%q = %v; want nil", doc, got)
		}
	}
	got := myNodesPubkeys(`{"v":1,"keys":{"meshcore-my-nodes":"[{\"pubkey\":\"ab\",\"name\":\"x\",\"addedAt\":1},{\"name\":\"no key\"}]"}}`)
	if !reflect.DeepEqual(got, []string{"ab", ""}) {
		t.Fatalf("myNodesPubkeys = %v", got)
	}
}

func TestWatchMyNodes(t *testing.T) {
	ns := defaultNotifySettings()
	ns.maxWatchesPerUser = 1
	f := newNotifyFixture(t, ns)
	f.setNode(evPkA, "Alpha", "repeater", time.Hour, nil)
	f.setNode(evPkB, "Bravo", "companion", time.Hour, nil)
	pat := f.registerAndActivate(t, "pat@example.org", "Pat", pw)
	post := func() watchMyNodesJSON {
		t.Helper()
		w := f.do("POST", "/api/account/notifications/watch-my-nodes", nil, as(pat))
		expectStatus(t, w, 200)
		return decode[watchMyNodesJSON](t, w)
	}
	if r := post(); r.Added != 0 || r.Already != 0 || r.Skipped != 0 {
		t.Fatalf("without synced settings = %+v", r)
	}
	list, _ := json.Marshal([]map[string]string{{"pubkey": strings.ToUpper(evPkA)}, {"pubkey": evPkB}, {"pubkey": evPkG}, {"pubkey": "bad"}, {"pubkey": evPkA}})
	doc, _ := json.Marshal(settingsDoc{V: 1, Keys: map[string]string{"meshcore-my-nodes": string(list)}})
	if _, err := f.st.PutSettings(pat.me.ID, users.SettingsVersion{}, string(doc)); err != nil {
		t.Fatal(err)
	}
	// A watched, B over the limit, G not in the analyzer, "bad" malformed, the second A a duplicate.
	if r := post(); r.Added != 1 || r.Already != 0 || r.Skipped != 3 || len(r.Account.Watches) != 1 || r.Account.Watches[0].Pubkey != evPkA {
		t.Fatalf("first copy = %+v", r)
	}
	if r := post(); r.Added != 0 || r.Already != 1 || r.Skipped != 3 {
		t.Fatalf("second copy = %+v", r)
	}
	expectStatus(t, f.do("POST", "/api/account/notifications/watch-my-nodes", nil, as(&client{cookie: pat.cookie})), 403)
}

func TestUnsubscribe(t *testing.T) {
	f := newNotifyFixture(t, defaultNotifySettings())
	pat := f.registerAndActivate(t, "pat@example.org", "Pat", pw)
	p, err := f.st.NotifyPrefsFor(pat.me.ID)
	if err != nil {
		t.Fatal(err)
	}
	q := "/api/notifications/unsubscribe?token=" + p.UnsubToken
	w := f.do("GET", q, nil)
	if w.Code != 303 || w.Header().Get("Location") != testBase+"/#/account/unsubscribe?token="+p.UnsubToken {
		t.Fatalf("GET = %d, Location %q", w.Code, w.Header().Get("Location"))
	}
	if again, _ := f.st.NotifyPrefsFor(pat.me.ID); !again.Enabled {
		t.Fatal("a GET (a link scanner) turned notifications off")
	}
	// The provider's one-click POST: form body, a foreign Origin, no session.
	w = f.do("POST", q, nil, header("Origin", "https://mail.example.net"), header("Content-Type", "application/x-www-form-urlencoded"))
	expectStatus(t, w, 200)
	if r := decode[okResponse](t, w); !r.OK || !strings.Contains(r.Message, "Notifications are off") {
		t.Fatalf("POST = %+v", r)
	}
	if again, _ := f.st.NotifyPrefsFor(pat.me.ID); again.Enabled {
		t.Fatal("still enabled after the POST")
	}
	expectStatus(t, f.do("POST", q, nil), 200)
	entries, err := f.st.AuditList(users.AuditFilter{Actions: []string{"notify.unsubscribe"}})
	if err != nil || len(entries) != 1 || *entries[0].TargetUserID != pat.me.ID || entries[0].Detail["via"] != "link" {
		t.Fatalf("audit = %+v, %v; want one row for the change", entries, err)
	}
	for _, bad := range []string{"/api/notifications/unsubscribe?token=nope", "/api/notifications/unsubscribe"} {
		if w := f.do("POST", bad, nil); w.Code != 410 {
			t.Errorf("POST %s = %d; want 410", bad, w.Code)
		}
	}
}

func TestClientConfigAdvertisesNotifications(t *testing.T) {
	srv, router := setupTestServer(t)
	a, _ := newTestAuthService(t)
	a.notify = newNotifier(a, &fakeNotifySource{}, time.Now)
	srv.auth = a
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/config/client", nil))
	if !strings.Contains(w.Body.String(), `"userManagement":{"enabled":true,"notifications":true,"companionLinking":true}`) {
		t.Fatalf("client config = %s", w.Body.String())
	}
}

func TestAdminStatsNotifyBlock(t *testing.T) {
	f := newNotifyFixture(t, defaultNotifySettings())
	boss := f.registerAndActivate(t, "admin@example.org", "Boss", pw)
	f.watcher(t, "pat@example.org", "Pat", evPkA, evPkB)
	if _, err := f.st.LogMail(nil, "gone@example.org", users.NotifyMailPurpose, ""); err != nil {
		t.Fatal(err)
	}
	w := f.do("GET", "/api/admin/stats", nil, as(boss))
	expectStatus(t, w, 200)
	st := decode[adminStatsJSON](t, w)
	if st.Notify == nil || *st.Notify != (adminNotifyJSON{MailsLast24h: 1, MaxMailsPerDay: 300, Watches: 2, WatchingUsers: 1}) {
		t.Fatalf("notify block = %+v", st.Notify)
	}
	off, offBoss, _ := adminFixture(t)
	if w := off.do("GET", "/api/admin/stats", nil, as(offBoss)); strings.Contains(w.Body.String(), `"notify"`) {
		t.Fatalf("notify block with notifications off: %s", w.Body.String())
	}
}

// users.db never holds case variants: every route lowercases the key first.
func TestWatchRoutesStoreLowercaseKeys(t *testing.T) {
	f := newNotifyFixture(t, defaultNotifySettings())
	f.setNode(evPkA, "Alpha", "repeater", time.Hour, nil)
	f.setNode(evPkB, "Bravo", "companion", time.Hour, nil)
	pat := f.registerAndActivate(t, "pat@example.org", "Pat", pw)
	stored := func() []string {
		t.Helper()
		ws, err := f.st.WatchesFor(pat.me.ID)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, w := range ws {
			out = append(out, w.Pubkey)
		}
		return out
	}
	expectStatus(t, f.do("PUT", notifyPath(strings.ToUpper(evPkA)), nil, as(pat)), 200)
	expectStatus(t, f.do("PUT", notifyPath(evPkA), nil, as(pat)), 200)
	if got := stored(); !reflect.DeepEqual(got, []string{evPkA}) {
		t.Fatalf("after PUT upper then lower = %v", got)
	}
	expectStatus(t, f.do("DELETE", notifyPath(strings.ToUpper(evPkA)), nil, as(pat)), 200)
	if got := stored(); len(got) != 0 {
		t.Fatalf("DELETE with an uppercase key left %v", got)
	}
	list, _ := json.Marshal([]map[string]string{{"pubkey": strings.ToUpper(evPkB)}})
	doc, _ := json.Marshal(settingsDoc{V: 1, Keys: map[string]string{"meshcore-my-nodes": string(list)}})
	if _, err := f.st.PutSettings(pat.me.ID, users.SettingsVersion{}, string(doc)); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, f.do("POST", "/api/account/notifications/watch-my-nodes", nil, as(pat)), 200)
	if got := stored(); !reflect.DeepEqual(got, []string{evPkB}) {
		t.Fatalf("watch-my-nodes stored %v", got)
	}
}
