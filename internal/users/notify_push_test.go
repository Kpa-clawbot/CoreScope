package users

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestPushBindingRelinkQueuesRevokeOnlyAfterActivation(t *testing.T) {
	st, clk := newTestStore(t)
	a := activeUser(t, st, "a@example.org", "A")
	b := activeUser(t, st, "b@example.org", "B")
	first, err := st.StagePushBinding("analyzer", "phone", a.ID, "first", "proof-a")
	if err != nil {
		t.Fatal(err)
	}
	first, err = st.ActivatePushBinding(first, "first-sub", clk.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, proof := range []string{"proof-a", "renewed-proof"} {
		retry, err := st.StagePushBinding("analyzer", "phone", a.ID, "new-id", proof)
		if err != nil || retry.BindingID != first.BindingID {
			t.Fatalf("stage renewal: %+v, %v", retry, err)
		}
		if _, err := st.ActivatePushBinding(retry, "first-sub", clk.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	second, err := st.StagePushBinding("analyzer", "phone", b.ID, "second", "proof-b")
	if err != nil || second.BindingID != "second" {
		t.Fatalf("stage relink: %+v, %v", second, err)
	}
	if jobs, err := st.DuePushJobs("analyzer", clk.Now(), 10); err != nil || len(jobs) != 0 {
		t.Fatalf("unverified relink changed active binding: %+v, %v", jobs, err)
	}
	if _, err := st.ActivatePushBinding(second, "second-sub", clk.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.RevokePushBinding("analyzer", "phone", a.ID); err != nil {
		t.Fatal(err)
	}
	jobs, err := st.DuePushJobs("analyzer", clk.Now(), 10)
	if err != nil || len(jobs) != 1 || jobs[0].Operation != "revoke" || jobs[0].SubscriptionID != "first-sub" {
		t.Fatalf("jobs after relink: %+v, %v", jobs, err)
	}
	if err := st.Delete(b.ID); err != nil {
		t.Fatal(err)
	}
	jobs, err = st.DuePushJobs("analyzer", clk.Now(), 10)
	if err != nil || len(jobs) != 2 || jobs[1].SubscriptionID != "second-sub" {
		t.Fatalf("jobs after account deletion: %+v, %v", jobs, err)
	}
}

func TestPushOutboxTracksWatchAndIgnoresEmailOptOut(t *testing.T) {
	st, clk := newTestStore(t)
	u := activeUser(t, st, "a@example.org", "A")
	binding, err := st.StagePushBinding("analyzer", "phone", u.ID, "bind", "proof")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ActivatePushBinding(binding, "sub", clk.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.AddWatch(u.ID, nPkA, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetNotifyPrefs(u.ID, false, []string{NotifyNodeOffline}); err != nil {
		t.Fatal(err)
	}
	event := PushEvent{UserID: u.ID, Event: NotifyNodeOffline, EventID: "event", Kind: "node.not_heard",
		NodePubkey: nPkA, NodeName: "Milliways 1W", OccurredAt: clk.Now(), ExpiresAt: clk.Now().Add(time.Hour)}
	states := []NotifyState{nState(u.ID, NotifyNodeOffline, nPkA, NotifyBad, clk.Now())}
	for range 2 {
		if err := st.ApplyNotifyChanges(nil, states, NotifyOutbox{PushAnalyzerID: "analyzer", PushEvents: []PushEvent{event}}); err != nil {
			t.Fatal(err)
		}
	}
	jobs, err := st.DuePushJobs("analyzer", clk.Now(), 10)
	if err != nil || len(jobs) != 1 || jobs[0].EventID != "event" || jobs[0].Operation != "deliver" || jobs[0].NodeName != event.NodeName {
		t.Fatalf("due jobs: %+v, %v", jobs, err)
	}
	job := jobs[0]
	if got := statesByKey(t, st)[states[0].NotifyKey].State; got != NotifyBad {
		t.Fatalf("state = %q", got)
	}
	if ok, err := st.PushJobEligible(job, clk.Now()); err != nil || !ok {
		t.Fatalf("delivery eligible = %v, %v", ok, err)
	}
	if err := st.RetryPushJob(job.ID, clk.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if jobs, err := st.DuePushJobs("analyzer", clk.Now(), 10); err != nil || len(jobs) != 0 {
		t.Fatalf("retry already due: %+v, %v", jobs, err)
	}
	clk.Advance(time.Minute)
	if jobs, err := st.DuePushJobs("analyzer", clk.Now(), 10); err != nil || len(jobs) != 1 || jobs[0].Attempts != 1 || jobs[0].NodeName != event.NodeName {
		t.Fatalf("retry missing: %+v, %v", jobs, err)
	}
	if err := st.SetStatus(u.ID, StatusDisabled); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.PushJobEligible(job, clk.Now()); err != nil || ok {
		t.Fatalf("disabled user's delivery eligible = %v, %v", ok, err)
	}
	if err := st.SetStatus(u.ID, StatusActive); err != nil {
		t.Fatal(err)
	}
	if err := st.RemoveWatch(u.ID, nPkA); err != nil {
		t.Fatal(err)
	}
	if err := st.AddWatch(u.ID, nPkA, 10); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.PushJobEligible(job, clk.Now()); err != nil || ok {
		t.Fatalf("old delivery eligible after rewatch = %v, %v", ok, err)
	}
	if err := st.CompletePushJob(job.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPushOutboxRollbackKeepsPriorNotifyState(t *testing.T) {
	st, clk := newTestStore(t)
	u := activeUser(t, st, "a@example.org", "A")
	prior := nState(u.ID, NotifyNodeOffline, nPkA, NotifyGood, clk.Now())
	if err := st.WriteNotifyStates([]NotifyState{prior}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec(`DROP TABLE notification_push_jobs`); err != nil {
		t.Fatal(err)
	}
	next := nState(u.ID, NotifyNodeOffline, nPkA, NotifyBad, clk.Now())
	event := PushEvent{UserID: u.ID, Event: NotifyNodeOffline, EventID: "event", Kind: "node.not_heard",
		NodePubkey: nPkA, OccurredAt: clk.Now(), ExpiresAt: clk.Now().Add(time.Hour)}
	if err := st.ApplyNotifyChanges(nil, []NotifyState{next}, NotifyOutbox{PushAnalyzerID: "analyzer", PushEvents: []PushEvent{event}}); err == nil {
		t.Fatal("missing outbox unexpectedly committed")
	}
	if got := statesByKey(t, st)[prior.NotifyKey].State; got != NotifyGood {
		t.Fatalf("state advanced without outbox: %q", got)
	}
}

func TestCancelledPushBindingCannotActivate(t *testing.T) {
	st, clk := newTestStore(t)
	u := activeUser(t, st, "a@example.org", "A")
	binding, err := st.StagePushBinding("analyzer", "phone", u.ID, "bind", "proof")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CancelPushBinding(binding); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ActivatePushBinding(binding, "sub", clk.Now().Add(time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cancelled activation error: %v", err)
	}
}

func TestPushOutboxSurvivesReopenAndPrunesExpiredWork(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	clk := &fakeClock{t: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)}
	st.SetClock(clk.Now)
	t.Cleanup(func() { st.Close() })
	user := activeUser(t, st, "a@example.org", "A")
	binding, err := st.StagePushBinding("analyzer", "phone", user.ID, "bind", "proof")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ActivatePushBinding(binding, "sub", clk.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.RevokePushBinding("analyzer", "phone", user.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st.SetClock(clk.Now)
	jobs, err := st.DuePushJobs("analyzer", clk.Now(), 10)
	if err != nil || len(jobs) != 1 || jobs[0].SubscriptionID != "sub" || !jobs[0].ExpiresAt.Equal(clk.Now().Add(time.Hour)) {
		t.Fatalf("durable revocation: %+v, %v", jobs, err)
	}
	clk.Advance(time.Hour)
	if got, err := st.DuePushJobs("analyzer", clk.Now(), 10); err != nil || len(got) != 0 {
		t.Fatalf("expired jobs returned: %+v, %v", got, err)
	}
	if ok, err := st.PushJobEligible(jobs[0], clk.Now().Add(-time.Minute)); err != nil || ok {
		t.Fatalf("expired job stayed in storage: %v, %v", ok, err)
	}
}

func TestPrunePushState(t *testing.T) {
	for _, tc := range []struct {
		name, keep string
		removed    int64
	}{
		{name: "unconfigured", removed: 2},
		{name: "keep configured analyzer", keep: "analyzer-a", removed: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, clk := newTestStore(t)
			u := activeUser(t, st, "a@example.org", "A")
			if err := st.AddWatch(u.ID, nPkA, 10); err != nil {
				t.Fatal(err)
			}

			for _, analyzer := range []string{"analyzer-a", "analyzer-b"} {
				binding, err := st.StagePushBinding(analyzer, "phone", u.ID, "binding", "proof")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := st.ActivatePushBinding(binding, analyzer+"-sub", clk.Now().Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
				if _, err := st.StagePushBinding(analyzer, "tablet", u.ID, "pending", "other-proof"); err != nil {
					t.Fatal(err)
				}
				event := PushEvent{UserID: u.ID, Event: NotifyNodeOffline, EventID: "event", Kind: "node.not_heard",
					NodePubkey: nPkA, OccurredAt: clk.Now(), ExpiresAt: clk.Now().Add(2 * time.Hour)}
				if err := st.ApplyNotifyChanges(nil, nil, NotifyOutbox{PushAnalyzerID: analyzer, PushEvents: []PushEvent{event}}); err != nil {
					t.Fatal(err)
				}
			}
			// Startup pruning must preserve expired rows for the configured analyzer.
			clk.Advance(time.Hour)

			counts, err := st.PrunePushState(tc.keep)
			want := PushPruneCounts{Bindings: tc.removed, Intents: tc.removed, Jobs: tc.removed}
			if err != nil || counts != want {
				t.Fatalf("PrunePushState(%q) = %+v, %v; want %+v", tc.keep, counts, err, want)
			}
			for _, table := range []string{"notification_push_bindings", "notification_push_intents", "notification_push_jobs"} {
				var kept, other int
				if err := st.db.QueryRow(`SELECT COUNT(CASE WHEN analyzer_id = ? THEN 1 END),
					COUNT(CASE WHEN analyzer_id <> ? THEN 1 END) FROM `+table, tc.keep, tc.keep).Scan(&kept, &other); err != nil {
					t.Fatal(err)
				}
				if kept != 2-int(tc.removed) || other != 0 {
					t.Fatalf("%s: kept %d, other %d", table, kept, other)
				}
			}

			counts, err = st.PrunePushState(tc.keep)
			if err != nil || counts != (PushPruneCounts{}) {
				t.Fatalf("second prune = %+v, %v; want zero counts", counts, err)
			}
			if tc.keep != "" {
				// Normal unlink still uses the trigger to replace queued deliveries with a revoke job.
				if err := st.RevokePushBinding(tc.keep, "phone", u.ID); err != nil {
					t.Fatal(err)
				}
				var count int
				var operation, subscription string
				err := st.db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(operation), ''),
					COALESCE(MAX(subscription_id), '') FROM notification_push_jobs WHERE analyzer_id = ?`, tc.keep).
					Scan(&count, &operation, &subscription)
				if err != nil || count != 1 || operation != "revoke" || subscription != tc.keep+"-sub" {
					t.Fatalf("after unlink, got %d jobs, operation %q, subscription %q, err %v", count, operation, subscription, err)
				}
			}
		})
	}
}
