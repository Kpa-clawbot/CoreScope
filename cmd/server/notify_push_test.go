package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/users"
)

func TestPushEventsUseOnlySupportedTransitionsForActiveAccounts(t *testing.T) {
	at := time.Date(2026, 11, 18, 20, 0, 0, 0, time.UTC)
	active := int64(42)
	inactive := int64(43)
	changes := map[int64][]notifyChange{
		active: {
			{Event: users.NotifyNodeOffline, Subject: evPkA, To: users.NotifyBad, At: at},
			{Event: users.NotifyNodeOffline, Subject: evPkA, To: users.NotifyGood, At: at},
			{Event: users.NotifyNodeBattery, Subject: evPkA, To: users.NotifyBad, At: at},
			{Event: users.NotifyNodeBattery, Subject: evPkA, To: users.NotifyGood, At: at},
			{Event: users.NotifyNodeExternal, Subject: evPkA, To: users.NotifyBad, At: at},
			{Event: users.NotifyNodeOffline, Subject: "invalid", To: users.NotifyBad, At: at},
		},
		inactive: {{Event: users.NotifyNodeOffline, Subject: evPkA, To: users.NotifyBad, At: at}},
	}
	accounts := map[int64]users.User{active: {Status: users.StatusActive}, inactive: {Status: users.StatusDisabled}}
	got := notifyPushEvents(changes, accounts, map[string]notifyNode{evPkA: {Name: "  Café\nRepeater  "}})
	want := []string{"node.not_heard", "node.heard_again", "node.battery_low", "node.battery_recovered"}
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d", len(got), len(want))
	}
	seen := map[string]bool{}
	for i, event := range got {
		if event.Kind != want[i] || event.UserID != active || event.NodePubkey != evPkA || event.NodeName != "Café Repeater" ||
			event.Event != changes[active][i].Event || event.EventID == "" || seen[event.EventID] ||
			!event.OccurredAt.Equal(at) || !event.ExpiresAt.Equal(at.Add(time.Hour)) {
			t.Errorf("unexpected event: %+v", event)
		}
		seen[event.EventID] = true
	}
}

func TestNotifyPushRetryPolicy(t *testing.T) {
	for _, tc := range []struct {
		err      error
		attempts int
		want     time.Duration
	}{
		{nil, 0, 0},
		{&notifyRelayError{Status: http.StatusGone}, 0, 0},
		{&notifyRelayError{Status: http.StatusUnauthorized}, 0, 0},
		{&notifyRelayError{Status: http.StatusTooManyRequests, RetryAfter: time.Minute}, 0, time.Minute},
		{&notifyRelayError{Status: http.StatusServiceUnavailable}, 2, 20 * time.Second},
		{errors.New("network"), 8, 5 * time.Minute},
	} {
		if got := notifyPushRetry(tc.err, tc.attempts); got != tc.want {
			t.Errorf("retry for %v (attempt %d) = %v, want %v", tc.err, tc.attempts, got, tc.want)
		}
	}
}

func TestResolveNotifyRelayRequiresExplicitConfiguration(t *testing.T) {
	var ns notifySettings
	getenv := func(string) string { return "" }
	if err := ns.configureRelay(nil, getenv); err != nil || ns.relay != nil {
		t.Fatalf("relay should default off: %v", err)
	}
	c := &NotifyRelayConfig{Origin: "https://push-staging.nodescope.app", AnalyzerID: "analyzer_test", Platforms: []string{"ios", "android"}}
	if err := ns.configureRelay(c, getenv); err == nil {
		t.Fatal("accepted configuration without sender credential")
	}
	secret := func(key string) string {
		if key == "CORESCOPE_PUSH_RELAY_CREDENTIAL" {
			return strings.Repeat("a", 43)
		}
		return ""
	}
	if err := ns.configureRelay(c, secret); err != nil {
		t.Fatal(err)
	}
	if ns.relay == nil || ns.relayAnalyzerID != c.AnalyzerID || ns.relayOrigin != c.Origin || len(ns.relayPlatforms) != 2 {
		t.Fatal("relay configuration was not preserved")
	}
}

// A pushRelay block with an analyzerId is a manual enrollment; with only an
// origin it locks self-service enrollment to that relay.
// relayDest returns the notifier's push relay destination.
func relayDest(n *notifier) *relayDestination {
	for _, d := range n.destinations {
		if r, ok := d.(*relayDestination); ok {
			return r
		}
	}
	panic("notifier has no push relay destination")
}

func TestPushRelayConfigModes(t *testing.T) {
	noEnv := func(string) string { return "" }
	withRelay := func(relay *NotifyRelayConfig) *UserManagementConfig {
		u := validUM()
		u.Notifications = &NotificationsConfig{Enabled: true, PushRelay: relay}
		return u
	}
	set, err := resolveUserManagement(withRelay(&NotifyRelayConfig{Origin: "https://Relay.Example.org/"}), "meshcore.db", noEnv)
	if err != nil || set.selfEnrollOrigin != "https://relay.example.org" || strings.Join(set.selfEnrollPlatforms, ",") != "ios,android" || set.notify.relay != nil {
		t.Fatalf("origin-only block: %+v, %v", set, err)
	}
	if _, err := resolveUserManagement(withRelay(&NotifyRelayConfig{Origin: "http://relay.example.org"}), "meshcore.db", noEnv); err == nil {
		t.Fatal("accepted a locked origin without HTTPS")
	}
	manual := &NotifyRelayConfig{Origin: "https://relay.example.org", AnalyzerID: "an_manual", Platforms: []string{"ios"}}
	if _, err := resolveUserManagement(withRelay(manual), "meshcore.db", noEnv); err == nil {
		t.Fatal("accepted a manual enrollment without its credential")
	}
	set, err = resolveUserManagement(withRelay(manual), "meshcore.db", func(k string) string {
		if k == "CORESCOPE_PUSH_RELAY_CREDENTIAL" {
			return strings.Repeat("m", 43)
		}
		return ""
	})
	if err != nil || set.notify.relay == nil || set.notify.relayAnalyzerID != "an_manual" || set.selfEnrollOrigin != "" {
		t.Fatalf("manual enrollment: %v", err)
	}
	if set, err := resolveUserManagement(withRelay(nil), "meshcore.db", noEnv); err != nil || set.selfEnrollOrigin != "" || set.notify.relay != nil {
		t.Fatalf("no pushRelay block: %v", err)
	}
}

func TestPruneUnconfiguredPushStateLogsOnlyRemovals(t *testing.T) {
	f := newNotifyFixture(t, defaultNotifySettings())
	pat := f.registerAndActivate(t, "pat@example.org", "Pat", pw)
	binding, err := f.st.StagePushBinding("analyzer_old", "installation_test", pat.me.ID, "binding_test", "proof_hash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.ActivatePushBinding(binding, "subscription_test", f.clk.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })

	if err := pruneUnconfiguredPushState(f.st, ""); err != nil {
		t.Fatal(err)
	}
	const want = "[notify] removed 1 push binding(s), 0 intent(s), 0 job(s) for analyzers no longer configured"
	if got := strings.TrimSpace(output.String()); !strings.HasSuffix(got, want) || strings.Contains(got, "\n") {
		t.Fatalf("cleanup log = %q, want one line ending with %q", got, want)
	}
	output.Reset()
	if err := pruneUnconfiguredPushState(f.st, ""); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "" {
		t.Fatalf("no-op cleanup logged %q", got)
	}
}

func TestStartupPrunesPushStateWithNotificationsDisabled(t *testing.T) {
	for _, tc := range []struct {
		name  string
		relay *NotifyRelayConfig
		fail  bool
		wantA int
		wantB int
	}{
		{name: "keep configured analyzer", relay: &NotifyRelayConfig{AnalyzerID: "analyzer-a", Origin: "invalid with push off"}, wantA: 1},
		{name: "keep all without a relay", wantA: 1, wantB: 1},
		{name: "prune error rolls back and startup continues", relay: &NotifyRelayConfig{AnalyzerID: "analyzer-a", Origin: "invalid with push off"}, fail: true, wantA: 1, wantB: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CORESCOPE_PUSH_RELAY_CREDENTIAL", "")
			f := newNotifyFixture(t, defaultNotifySettings())
			pat := f.watcher(t, "pat@example.org", "Pat", evPkA)
			for _, analyzer := range []string{"analyzer-a", "analyzer-b"} {
				binding, err := f.st.StagePushBinding(analyzer, "phone", pat.me.ID, "binding", "proof")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.st.ActivatePushBinding(binding, analyzer+"-sub", f.clk.Now().Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
				if _, err := f.st.StagePushBinding(analyzer, "tablet", pat.me.ID, "pending", "other-proof"); err != nil {
					t.Fatal(err)
				}
				event := users.PushEvent{UserID: pat.me.ID, Event: users.NotifyNodeOffline, EventID: "event",
					Kind: "node.not_heard", NodePubkey: evPkA, OccurredAt: f.clk.Now(), ExpiresAt: f.clk.Now().Add(time.Hour)}
				if err := f.st.ApplyNotifyChanges(nil, nil, users.NotifyOutbox{PushAnalyzerID: analyzer, PushEvents: []users.PushEvent{event}}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.fail {
				f.execDB(t, `CREATE TRIGGER test_prune_failure BEFORE DELETE ON notification_push_bindings
					BEGIN SELECT RAISE(ABORT, 'test prune failure'); END`)
			}

			dbPath := f.srv.auth.set.dbPath
			srv := &Server{cfg: &Config{UserManagement: &UserManagementConfig{
				Enabled: true, PublicBaseURL: testBase, DBPath: dbPath,
				Mail:          UserMailConfig{Provider: "brevo", BrevoAPIKey: "k", FromEmail: "noreply@example.org"},
				Notifications: &NotificationsConfig{PushRelay: tc.relay},
			}}}
			var output bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&output)
			defer log.SetOutput(previous)
			if err := srv.initUserManagement(filepath.Join(t.TempDir(), "meshcore.db")); err != nil {
				t.Fatalf("start user management: %v", err)
			}
			defer srv.closeUserManagement()
			if srv.auth.notify != nil || srv.auth.set.notify.relay != nil {
				t.Fatal("started notifications or a relay with notifications disabled")
			}
			if _, err := srv.auth.st.CountActiveAdmins(); err != nil {
				t.Fatalf("startup store is unusable: %v", err)
			}
			srv.closeUserManagement()
			got := output.String()
			if tc.fail {
				if !strings.Contains(got, "[notify] push state cleanup failed:") ||
					!strings.Contains(got, "test prune failure") || !strings.Contains(got, "will retry on next start") {
					t.Fatalf("missing cleanup failure in startup log: %q", got)
				}
			} else if strings.Contains(got, "push state cleanup failed") {
				t.Fatalf("unexpected cleanup failure in startup log: %q", got)
			}

			db, err := sql.Open("sqlite", dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for _, table := range []string{"notification_push_bindings", "notification_push_intents", "notification_push_jobs"} {
				var a, b int
				err := db.QueryRow(`SELECT COUNT(CASE WHEN analyzer_id = 'analyzer-a' THEN 1 END),
					COUNT(CASE WHEN analyzer_id = 'analyzer-b' THEN 1 END) FROM `+table).Scan(&a, &b)
				if err != nil || a != tc.wantA || b != tc.wantB {
					t.Fatalf("%s: got (%d, %d), err %v; want (%d, %d)", table, a, b, err, tc.wantA, tc.wantB)
				}
			}
		})
	}
}

func TestNotifierQueuesAndRetriesPushWithEmailOff(t *testing.T) {
	attempts := make(chan notifyRelayDeliveryRequest, 5)
	relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/deliveries" ||
			r.Header.Get("Authorization") != "Bearer staging-credential" {
			t.Errorf("unexpected relay request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var req notifyRelayDeliveryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("delivery request decode: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		first := len(attempts) == 0
		attempts <- req
		if first {
			w.Header().Set("Retry-After", "15")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"delivery_id":"delivery_test","status":"queued"}`))
	}))
	defer relay.Close()
	settings := defaultNotifySettings()
	var err error
	settings.relay, err = newNotifyRelayClient(relay.URL, relay.Client().Transport)
	if err != nil {
		t.Fatal(err)
	}
	settings.relayAnalyzerID = "analyzer_test"
	settings.relayCredential = "staging-credential"
	f := newNotifyFixture(t, settings)
	pat := f.watcher(t, "pat@example.org", "Pat", evPkA)
	if _, err := f.st.SetNotifyPrefs(pat.me.ID, false, users.NodeNotifyEvents); err != nil {
		t.Fatal(err)
	}
	binding, err := f.st.StagePushBinding(settings.relayAnalyzerID, "installation_test", pat.me.ID, "binding_test", "proof_hash")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.ActivatePushBinding(binding, "subscription_test", f.clk.Now().Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}

	f.setNode(evPkA, "Alpha", "companion", time.Hour, nil)
	f.tick()
	if jobs, err := f.st.DuePushJobs(settings.relayAnalyzerID, f.clk.Now(), 25); err != nil || len(jobs) != 0 {
		t.Fatalf("new watch produced %d jobs: %v", len(jobs), err)
	}
	f.clk.Advance(25 * time.Hour)
	f.tick()
	if mails := f.notifyMails(); len(mails) != 0 {
		t.Fatalf("email opt-out produced %d messages", len(mails))
	}
	if err := relayDest(f.n).dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := relayDest(f.n).dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 1 {
		t.Fatalf("got %d attempts before retry time", len(attempts))
	}

	f.clk.Advance(15 * time.Second)
	restarted := newNotifier(f.srv.auth, f.src, f.clk.Now)
	if err := relayDest(restarted).dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(attempts) != 2 {
		t.Fatalf("got %d attempts after retry", len(attempts))
	}
	first, second := <-attempts, <-attempts
	if first != second || first.EventID == "" || first.SubscriptionID != "subscription_test" ||
		first.Kind != "node.not_heard" || first.NodePubkey != evPkA {
		t.Fatalf("unexpected delivery or unstable retry: %+v then %+v", first, second)
	}
	if jobs, err := f.st.DuePushJobs(settings.relayAnalyzerID, f.clk.Now(), 25); err != nil || len(jobs) != 0 {
		t.Fatalf("accepted delivery left %d jobs: %v", len(jobs), err)
	}
}
