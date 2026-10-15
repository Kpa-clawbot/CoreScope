package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/users"
)

// Tests for the node.external event and its feed (#2177).

func extFeed(at time.Time, alerts ...string) []byte {
	return []byte(fmt.Sprintf(`{"generatedAt":%q,"alerts":[%s]}`, at.UTC().Format(time.RFC3339), strings.Join(alerts, ",")))
}

func extAlert(pk, key, text, url string) string {
	return fmt.Sprintf(`{"pubkey":%q,"key":%q,"text":%q,"url":%q}`, pk, key, text, url)
}

func TestParseExternalFeed(t *testing.T) {
	long := strings.Repeat("x", externalTextMaxLen+10)
	body := extFeed(evNow.Add(-time.Hour),
		extAlert(strings.ToUpper(evPkA), "SE", "not heard\nto the south-east", "https://example.org/coverage"),
		extAlert(evPkA, "N", long, "javascript:alert(1)"),
		extAlert(evPkA, "SE", "duplicate", ""),
		extAlert("abc", "SE", "short key", ""),
		extAlert(evPkB, "", "no key", ""),
		extAlert(evPkB, "*", "reserved key", ""),
		extAlert(evPkB, strings.Repeat("k", externalKeyMaxLen+1), "long key", ""),
		extAlert(evPkB, "W", " ", ""),
	)
	f, err := parseExternalFeed(body, evNow, 48*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if f.Skipped != 6 || len(f.Alerts) != 1 || len(f.Alerts[evPkA]) != 2 {
		t.Fatalf("feed = %+v; want 2 alerts on A and 6 skipped", f)
	}
	if a := f.Alerts[evPkA]["SE"]; a.Text != "not heard to the south-east" || a.URL != "https://example.org/coverage" {
		t.Fatalf("SE = %+v", a)
	}
	if a := f.Alerts[evPkA]["N"]; a.URL != "" || len(a.Text) != externalTextMaxLen+3 || !strings.HasSuffix(a.Text, "...") {
		t.Fatalf("N = %+v; want a truncated text and no URL", a)
	}
	for name, c := range map[string]struct {
		body []byte
		want string
	}{
		"too old":      {extFeed(evNow.Add(-49 * time.Hour)), "older than"},
		"no timestamp": {[]byte(`{"alerts":[]}`), "generatedAt"},
		"not json":     {[]byte(`<html>`), "decode"},
	} {
		if _, err := parseExternalFeed(c.body, evNow, 48*time.Hour); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v; want %q", name, err, c.want)
		}
	}
}

func TestFetchExternalFeed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			fmt.Fprint(w, `{"generatedAt":"2026-10-07T11:00:00Z","alerts":[]}`)
		case "/big":
			w.Write([]byte(strings.Repeat(" ", externalFeedMaxBytes+1)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	if body, err := fetchExternalFeed(srv.Client(), srv.URL+"/ok"); err != nil || !strings.Contains(string(body), "generatedAt") {
		t.Fatalf("ok = %q, %v", body, err)
	}
	if _, err := fetchExternalFeed(srv.Client(), srv.URL+"/gone"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("404 = %v", err)
	}
	if _, err := fetchExternalFeed(srv.Client(), srv.URL+"/big"); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Errorf("oversized = %v", err)
	}
}

func TestResolveExternalAlerts(t *testing.T) {
	if got, err := resolveExternalAlerts(nil); err != nil || got.url != "" {
		t.Fatalf("absent = %+v, %v; want not configured", got, err)
	}
	got, err := resolveExternalAlerts(&ExternalAlertsConfig{URL: " http://coverage-job/alerts.json "})
	if err != nil || got.url != "http://coverage-job/alerts.json" || got.maxAge != 48*time.Hour || got.label != "External alert" {
		t.Fatalf("defaults = %+v, %v", got, err)
	}
	got, err = resolveExternalAlerts(&ExternalAlertsConfig{URL: "https://x.example/a", MaxAgeHours: 30, Label: " Coverage\n"})
	if err != nil || got.maxAge != 30*time.Hour || got.label != "Coverage" {
		t.Fatalf("set = %+v, %v", got, err)
	}
	for _, bad := range []string{"", "alerts.json", "file:///etc/passwd", "ftp://x/a"} {
		if _, err := resolveExternalAlerts(&ExternalAlertsConfig{URL: bad}); err == nil {
			t.Errorf("url %q accepted", bad)
		}
	}
}

// extInput: evUser watches evPkA with node.external chosen.
func extInput(alerts map[string]map[string]externalAlert) notifyInput {
	in := evInput()
	in.Prefs = []users.NotifyPrefs{{UserID: evUser, Enabled: true, Events: []string{users.NotifyNodeExternal}}}
	in.Nodes[evPkA] = notifyNode{Pubkey: evPkA, Name: "Alpha", Role: "repeater"}
	if alerts != nil {
		in.External = &externalFeed{GeneratedAt: evNow, Alerts: alerts}
	}
	return in
}

func TestEvaluateExternalBaselineThenChanges(t *testing.T) {
	se := map[string]map[string]externalAlert{evPkA: {"SE": {Text: "never heard to the SE", URL: "https://example.org/c"}}}
	res := evaluateNotifications(extInput(se))
	if len(res.Changes) != 0 {
		t.Fatalf("first fresh feed mailed: %+v", res.Changes)
	}
	if s, ok := evState(res, evUser, users.NotifyNodeExternal, evPkA+"/*"); !ok || s != users.NotifyGood {
		t.Fatal("no baseline stored")
	}
	if s, _ := evState(res, evUser, users.NotifyNodeExternal, evPkA+"/SE"); s != users.NotifyBad {
		t.Fatal("current alert not stored as bad")
	}

	base := evStateRow(evUser, users.NotifyNodeExternal, evPkA+"/*", users.NotifyGood)
	seBad := evStateRow(evUser, users.NotifyNodeExternal, evPkA+"/SE", users.NotifyBad)

	// A new alert after the baseline mails, an unchanged one does not.
	in := extInput(map[string]map[string]externalAlert{evPkA: {"SE": se[evPkA]["SE"], "W": {Text: "never heard to the W"}}})
	in.States = []users.NotifyState{base, seBad}
	res = evaluateNotifications(in)
	want := []notifyChange{{Event: users.NotifyNodeExternal, Subject: evPkA + "/W", Name: "Alpha", To: users.NotifyBad, Text: "never heard to the W", At: evNow}}
	if !reflect.DeepEqual(res.Changes[evUser], want) {
		t.Fatalf("new alert = %+v; want %+v", res.Changes[evUser], want)
	}

	// An alert gone from a fresh feed is resolved.
	in = extInput(map[string]map[string]externalAlert{})
	in.States = []users.NotifyState{base, seBad}
	res = evaluateNotifications(in)
	if c := res.Changes[evUser]; len(c) != 1 || c[0].Subject != evPkA+"/SE" || c[0].To != users.NotifyGood {
		t.Fatalf("resolved = %+v", c)
	}

	// It comes back: bad again, mailed.
	in = extInput(se)
	in.States = []users.NotifyState{base, evStateRow(evUser, users.NotifyNodeExternal, evPkA+"/SE", users.NotifyGood)}
	if c := evaluateNotifications(in).Changes[evUser]; len(c) != 1 || c[0].To != users.NotifyBad {
		t.Fatalf("returning alert = %+v", c)
	}
}

func TestEvaluateExternalStaleFeedChangesNothing(t *testing.T) {
	in := extInput(nil)
	in.States = []users.NotifyState{
		evStateRow(evUser, users.NotifyNodeExternal, evPkA+"/*", users.NotifyGood),
		evStateRow(evUser, users.NotifyNodeExternal, evPkA+"/SE", users.NotifyBad),
	}
	if res := evaluateNotifications(in); len(res.Changes) != 0 || len(res.States) != 0 {
		t.Fatalf("stale feed: changes %+v, states %+v; want neither", res.Changes, res.States)
	}
}

func TestEvaluateExternalOnlyWhenChosenAndQuietWhenOff(t *testing.T) {
	se := map[string]map[string]externalAlert{evPkA: {"SE": {Text: "x"}}}
	in := extInput(se)
	in.Prefs[0].Events = []string{users.NotifyNodeBattery}
	if _, ok := evState(evaluateNotifications(in), evUser, users.NotifyNodeExternal, evPkA+"/*"); ok {
		t.Fatal("node.external evaluated for a user who did not choose it")
	}
	in = extInput(se)
	in.Prefs[0].Enabled = false
	in.States = []users.NotifyState{evStateRow(evUser, users.NotifyNodeExternal, evPkA+"/*", users.NotifyGood)}
	res := evaluateNotifications(in)
	if len(res.Changes) != 0 {
		t.Fatalf("notifications off mailed: %+v", res.Changes)
	}
	if s, _ := evState(res, evUser, users.NotifyNodeExternal, evPkA+"/SE"); s != users.NotifyBad {
		t.Fatal("state not kept current while notifications are off")
	}
}

func TestNotifyExternalChangeTextAndURL(t *testing.T) {
	at := time.Date(2026, 10, 7, 9, 5, 0, 0, time.UTC)
	bad := notifyChange{Event: users.NotifyNodeExternal, Subject: evPkA + "/SE", Name: "Alpha", To: users.NotifyBad,
		Text: "never heard to the south-east", URL: "https://example.org/c", At: at}
	if got := notifyChangeText(bad, "Coverage"); got != "Alpha: Coverage: never heard to the south-east, 2026-10-07 09:05 UTC" {
		t.Errorf("bad = %q", got)
	}
	good := notifyChange{Event: users.NotifyNodeExternal, Subject: evPkA + "/SE", Name: "Alpha", To: users.NotifyGood, At: at}
	if got := notifyChangeText(good, "Coverage"); got != "Alpha: Coverage resolved (SE), 2026-10-07 09:05 UTC" {
		t.Errorf("good = %q", got)
	}
	if got := notifySubjectURL("https://e.org", bad); got != "https://example.org/c" {
		t.Errorf("alert URL = %q", got)
	}
	if got := notifySubjectURL("https://e.org", good); got != "https://e.org/#/nodes/"+evPkA {
		t.Errorf("resolved URL = %q", got)
	}
}

func extNotifySettings() notifySettings {
	ns := defaultNotifySettings()
	ns.external = externalAlertSettings{url: "http://coverage-job/alerts.json", maxAge: 48 * time.Hour, label: "Coverage"}
	return ns
}

func TestNotifierMailsExternalAlertsAndSkipsAStaleFeed(t *testing.T) {
	f := newNotifyFixture(t, extNotifySettings())
	pat := f.watcher(t, "pat@example.org", "Pat", evPkA)
	if _, err := f.st.SetNotifyPrefs(pat.me.ID, true, []string{users.NotifyNodeExternal}); err != nil {
		t.Fatal(err)
	}
	f.setNode(evPkA, "Alpha", "repeater", time.Hour, nil)
	f.src.feed = extFeed(f.clk.t)
	f.tick()
	f.src.feed = extFeed(f.clk.t, extAlert(evPkA, "SE", "never heard to the south-east", "https://example.org/c"))
	f.tick()
	m := f.notifyMails()
	if len(m) != 1 || !strings.Contains(m[0].Text, "Alpha: Coverage: never heard to the south-east") ||
		!strings.Contains(m[0].Text, "https://example.org/c") {
		t.Fatalf("mails = %+v", m)
	}
	// The feed dies: nothing is resolved.
	f.src.feed, f.src.feedErr = nil, errors.New("HTTP 502")
	f.tick()
	f.src.feedErr = nil
	f.src.feed = extFeed(f.clk.t.Add(-72 * time.Hour))
	f.tick()
	if len(f.notifyMails()) != 1 {
		t.Fatal("a failed or old feed mailed")
	}
	f.src.feed = extFeed(f.clk.t)
	f.tick()
	if m := f.notifyMails(); len(m) != 2 || !strings.Contains(m[1].Text, "Alpha: Coverage resolved (SE)") {
		t.Fatalf("resolved mail = %+v", m)
	}
}

func TestNotifierFetchesTheFeedOnlyWhenChosenAndConfigured(t *testing.T) {
	f := newNotifyFixture(t, extNotifySettings())
	f.watcher(t, "pat@example.org", "Pat", evPkA)
	f.setNode(evPkA, "Alpha", "repeater", time.Hour, nil)
	f.tick()
	if f.src.feedAsked != 0 {
		t.Fatal("feed fetched although nobody chose node.external")
	}
	g := newNotifyFixture(t, defaultNotifySettings())
	pat := g.watcher(t, "pat@example.org", "Pat", evPkA)
	if _, err := g.st.SetNotifyPrefs(pat.me.ID, true, []string{users.NotifyNodeExternal}); err != nil {
		t.Fatal(err)
	}
	g.setNode(evPkA, "Alpha", "repeater", time.Hour, nil)
	g.tick()
	if g.src.feedAsked != 0 {
		t.Fatal("feed fetched although externalAlerts is not configured")
	}
}

func TestNotifyAccountOffersExternalOnlyWhenConfigured(t *testing.T) {
	f := newNotifyFixture(t, defaultNotifySettings())
	pat := f.registerAndActivate(t, "pat@example.org", "Pat", pw)
	got := decode[notifyAccountJSON](t, f.do("GET", "/api/account/notifications", nil, as(pat)))
	if reflect.DeepEqual(got.AvailableEvents, nil) || got.ExternalLabel != "" || strings.Contains(strings.Join(got.AvailableEvents, ","), "node.external") {
		t.Fatalf("not configured = %+v", got)
	}
	w := f.do("PUT", "/api/account/notifications", notifyPrefsRequest{Enabled: true, Events: []string{"node.external"}}, as(pat))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "not configured") {
		t.Fatalf("PUT while not configured: %d %s", w.Code, w.Body.String())
	}

	g := newNotifyFixture(t, extNotifySettings())
	sam := g.registerAndActivate(t, "sam@example.org", "Sam", pw)
	got = decode[notifyAccountJSON](t, g.do("GET", "/api/account/notifications", nil, as(sam)))
	if !reflect.DeepEqual(got.AvailableEvents, []string{"node.offline", "node.battery", "node.external"}) ||
		got.ExternalLabel != "Coverage" || !reflect.DeepEqual(got.Events, []string{"node.offline", "node.battery"}) {
		t.Fatalf("configured = %+v", got)
	}
	w = g.do("PUT", "/api/account/notifications", notifyPrefsRequest{Enabled: true, Events: []string{"node.external", "node.offline"}}, as(sam))
	if got := decode[notifyAccountJSON](t, w); w.Code != 200 || !reflect.DeepEqual(got.Events, []string{"node.offline", "node.external"}) {
		t.Fatalf("PUT: %d %+v", w.Code, got)
	}
}
