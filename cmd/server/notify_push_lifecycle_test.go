package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/meshcore-analyzer/users"
)

func linkLifecycleFixture(t *testing.T, f *notifyFixture, analyzer, installation, subscription string, userID int64) {
	t.Helper()
	binding, err := f.st.StagePushBinding(analyzer, installation, userID, "binding_"+installation, "proof_"+installation)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.ActivatePushBinding(binding, subscription, f.clk.Now().Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func TestPushRelayPauseSkipsPushAndPreservesEmailAndLinks(t *testing.T) {
	const analyzer = "an_fixture"
	var calls atomic.Int32
	relay := newTestNotifyRelayClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/subscriptions/sub_other" {
			t.Errorf("unexpected alert dispatch: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	ns := defaultNotifySettings()
	ns.relay, ns.relayAnalyzerID, ns.relayOrigin = relay, analyzer, relay.origin
	f := newNotifyFixture(t, ns)
	a := f.srv.auth
	pat := f.watcher(t, "pat@example.org", "Pat", evPkA)
	linkLifecycleFixture(t, f, analyzer, "device", "sub_active", pat.me.ID)
	linkLifecycleFixture(t, f, analyzer, "other", "sub_other", pat.me.ID)
	if err := f.st.RevokePushBinding(analyzer, "other", pat.me.ID); err != nil {
		t.Fatal(err)
	}
	// Start with a queued alert and a queued revoke so Pause has both to handle.
	event := users.PushEvent{
		UserID: pat.me.ID, Event: users.NotifyNodeOffline, EventID: "before_pause",
		Kind: "node.not_heard", NodePubkey: evPkA, OccurredAt: f.clk.Now(), ExpiresAt: f.clk.Now().Add(time.Hour),
	}
	if err := f.st.ApplyNotifyChanges(nil, nil, users.NotifyOutbox{PushAnalyzerID: analyzer, PushEvents: []users.PushEvent{event}}); err != nil {
		t.Fatal(err)
	}
	if jobs, err := f.st.DuePushJobs(analyzer, f.clk.Now(), 10); err != nil || len(jobs) != 2 {
		t.Fatalf("expected a delivery and revoke before pause, got %v, %v", jobs, err)
	}

	f.setNode(evPkA, "Alpha", "companion", time.Hour, nil)
	f.tick()
	if err := a.setRelayDeliveryPaused(true); err != nil {
		t.Fatal(err)
	}
	restarted := &authService{st: f.st, set: a.set}
	restarted.restoreRelayLifecycle()
	if !a.relayDeliveryPaused() || !restarted.relayDeliveryPaused() {
		t.Fatal("pause did not persist")
	}
	w := f.do(http.MethodGet, "/api/account/notifications", nil, as(pat))
	if capability := decode[notifyAccountJSON](t, w).PushRelay; capability == nil || capability.AnalyzerID != analyzer {
		t.Fatalf("pause removed the advertised capability: %+v", capability)
	}

	f.clk.Advance(25 * time.Hour)
	f.tick()
	if len(f.notifyMails()) != 1 {
		t.Fatal("paused delivery affected email")
	}
	if err := relayDest(f.n).dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("paused delivery called the relay")
	}
	if jobs, err := f.st.DuePushJobs(analyzer, f.clk.Now(), 10); err != nil || len(jobs) != 1 || jobs[0].Operation != "revoke" {
		t.Fatalf("pause did not drop alerts and preserve revocations: %v, %v", jobs, err)
	}

	if err := a.setRelayDeliveryPaused(false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.relayLifecyclePath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("resume left a pause marker: %v", err)
	}
	f.tick()
	if err := relayDest(f.n).dispatch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("resume did not process exactly one revocation: %d", calls.Load())
	}
	// The original subscription can still be used. A new transition queues
	// normally after resuming, without re-enabling the phone.
	f.setNode(evPkA, "Alpha", "companion", 0, nil)
	f.tick()
	if jobs, err := f.st.DuePushJobs(analyzer, f.clk.Now(), 10); err != nil || len(jobs) != 1 ||
		jobs[0].SubscriptionID != "sub_active" || jobs[0].Kind != "node.heard_again" {
		t.Fatalf("resume did not keep the existing binding: %v, %v", jobs, err)
	}
}

func TestPushRelayDisconnectRetriesWithoutRestoringDelivery(t *testing.T) {
	const analyzer = "an_disconnected"
	sender := strings.Repeat("s", 43)
	var calls atomic.Int32
	relay := newTestNotifyRelayClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/analyzer" || r.Header.Get("Authorization") != "Bearer "+sender {
			t.Errorf("unexpected revoke request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	f := newNotifyFixture(t, defaultNotifySettings())
	a := f.srv.auth
	pat := f.watcher(t, "pat@example.org", "Pat", evPkA)
	linkLifecycleFixture(t, f, analyzer, "device", "sub_active", pat.me.ID)
	if err := a.activateRelayEnrollment(relay.origin, nil, analyzer, sender); err != nil {
		t.Fatal(err)
	}
	active := a.relaySettings()
	active.relay = relay
	a.enrolledRelay.Store(&active)

	if err := a.disconnectRelayEnrollment(context.Background()); !errors.Is(err, errRelayRevoke) {
		t.Fatalf("temporary relay failure returned %v", err)
	}
	if !a.relayDeliveryPaused() || !a.relayDisconnectPending() || a.relayRevoked() {
		t.Fatal("unconfirmed disconnect must stay paused and retryable")
	}
	if err := a.setRelayDeliveryPaused(false); !errors.Is(err, errRelayDisconnectPending) {
		t.Fatalf("resume during pending disconnect returned %v", err)
	}

	f.execDB(t, `CREATE TRIGGER test_prune_fail BEFORE DELETE ON notification_push_bindings
		BEGIN SELECT RAISE(ABORT, 'test prune failure'); END`)
	if err := a.disconnectRelayEnrollment(context.Background()); err == nil || !a.relayRevoked() {
		t.Fatalf("cleanup failure should keep confirmed revocation: %v", err)
	}
	w := f.do(http.MethodGet, "/api/account/notifications", nil, as(pat))
	if capability := decode[notifyAccountJSON](t, w).PushRelay; capability != nil {
		t.Fatalf("revoked analyzer advertised its capability: %+v", capability)
	}

	restarted := &authService{st: f.st, set: a.set}
	if id, err := restarted.restoreRelayEnrollment(); err != nil || id != analyzer {
		t.Fatalf("could not restore pending disconnect enrollment: %q, %v", id, err)
	}
	restarted.restoreRelayLifecycle()
	if !restarted.relayRevoked() {
		t.Fatal("confirmed revocation did not survive restart")
	}
	f.execDB(t, `DROP TRIGGER test_prune_fail`)
	if err := restarted.disconnectRelayEnrollment(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("local cleanup retried the completed relay request: %d calls", calls.Load())
	}
	if restarted.relaySettings().relay != nil || restarted.relayDisconnectPending() {
		t.Fatal("successful disconnect left an active enrollment")
	}
	if _, err := os.Stat(a.relayEnrollmentPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("disconnect left a credential file: %v", err)
	}
	if counts, err := f.st.PrunePushState(""); err != nil || counts != (users.PushPruneCounts{}) {
		t.Fatalf("disconnect left stale push state: %v, %v", counts, err)
	}
}
