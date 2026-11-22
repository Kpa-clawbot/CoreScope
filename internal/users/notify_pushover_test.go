package users

import (
	"errors"
	"testing"
	"time"
)

func pushoverMessage(userID int64, text string, expires time.Time) PushoverMessage {
	return PushoverMessage{UserID: userID, Title: "CoreScope", Message: text, URL: "https://example.org/#/nodes/x",
		URLTitle: "Open", ExpiresAt: expires}
}

func TestPushoverKeyLifecycleDropsQueuedMessages(t *testing.T) {
	st, clk := newTestStore(t)
	u := activeUser(t, st, "a@example.org", "A")
	if _, ok, err := st.PushoverFor(u.ID); err != nil || ok {
		t.Fatalf("no key yet: ok=%v, %v", ok, err)
	}
	if _, err := st.SetPushoverEnabled(u.ID, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("enable without a key: %v", err)
	}
	p, err := st.SetPushoverKey(u.ID, "ukey1")
	if err != nil || !p.Enabled || p.UserKey != "ukey1" || p.ValidatedAt.IsZero() {
		t.Fatalf("set key: %+v, %v", p, err)
	}
	queue := func(text string) {
		t.Helper()
		out := NotifyOutbox{PushoverMessages: []PushoverMessage{pushoverMessage(u.ID, text, clk.Now().Add(time.Hour))}}
		if err := st.ApplyNotifyChanges(nil, nil, out); err != nil {
			t.Fatal(err)
		}
	}
	due := func() []PushoverJob {
		t.Helper()
		jobs, err := st.DuePushoverJobs(clk.Now(), 10)
		if err != nil {
			t.Fatal(err)
		}
		return jobs
	}
	queue("first")
	if jobs := due(); len(jobs) != 1 || jobs[0].UserKey != "ukey1" || jobs[0].Message != "first" {
		t.Fatalf("queued: %+v", jobs)
	}
	if _, err := st.SetPushoverEnabled(u.ID, false); err != nil {
		t.Fatal(err)
	}
	if jobs := due(); len(jobs) != 0 {
		t.Fatalf("turning off kept %d message(s)", len(jobs))
	}
	queue("while off")
	if jobs := due(); len(jobs) != 0 {
		t.Fatal("queued a message for a user with Pushover off")
	}
	if _, err := st.SetPushoverEnabled(u.ID, true); err != nil {
		t.Fatal(err)
	}
	queue("second")
	if _, err := st.SetPushoverKey(u.ID, "ukey2"); err != nil {
		t.Fatal(err)
	}
	if jobs := due(); len(jobs) != 0 {
		t.Fatal("a new key kept messages queued for the old one")
	}
	queue("third")
	if err := st.DeletePushover(u.ID); err != nil {
		t.Fatal(err)
	}
	if jobs := due(); len(jobs) != 0 {
		t.Fatal("removing the key kept queued messages")
	}
}

func TestPushoverQueueOnlyForActiveUsersAndExpires(t *testing.T) {
	st, clk := newTestStore(t)
	a := activeUser(t, st, "a@example.org", "A")
	b := activeUser(t, st, "b@example.org", "B")
	for _, u := range []*User{a, b} {
		if _, err := st.SetPushoverKey(u.ID, "key-"+u.Email); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetStatus(b.ID, StatusDisabled); err != nil {
		t.Fatal(err)
	}
	out := NotifyOutbox{PushoverMessages: []PushoverMessage{
		pushoverMessage(a.ID, "a", clk.Now().Add(time.Hour)),
		pushoverMessage(b.ID, "b", clk.Now().Add(time.Hour)),
		pushoverMessage(a.ID, "already expired", clk.Now()),
	}}
	if err := st.ApplyNotifyChanges(nil, nil, out); err != nil {
		t.Fatal(err)
	}
	jobs, err := st.DuePushoverJobs(clk.Now(), 10)
	if err != nil || len(jobs) != 1 || jobs[0].UserID != a.ID {
		t.Fatalf("due: %+v, %v", jobs, err)
	}
	if err := st.RetryPushoverJob(jobs[0].ID, clk.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if jobs, _ := st.DuePushoverJobs(clk.Now(), 10); len(jobs) != 0 {
		t.Fatal("a retried job was due before its time")
	}
	clk.Advance(2 * time.Minute)
	if jobs, _ := st.DuePushoverJobs(clk.Now(), 10); len(jobs) != 1 || jobs[0].Attempts != 1 {
		t.Fatalf("retry not due: %+v", jobs)
	}
	clk.Advance(time.Hour)
	if jobs, _ := st.DuePushoverJobs(clk.Now(), 10); len(jobs) != 0 {
		t.Fatal("an expired message stayed queued")
	}
}

func TestPushoverSendsCountAndInvalidKeyDisables(t *testing.T) {
	st, clk := newTestStore(t)
	u := activeUser(t, st, "a@example.org", "A")
	if _, err := st.SetPushoverKey(u.ID, "old"); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyNotifyChanges(nil, nil, NotifyOutbox{PushoverMessages: []PushoverMessage{
		pushoverMessage(u.ID, "x", clk.Now().Add(time.Hour))}}); err != nil {
		t.Fatal(err)
	}
	jobs, _ := st.DuePushoverJobs(clk.Now(), 10)
	if err := st.CompletePushoverJob(jobs[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordPushoverSend(); err != nil {
		t.Fatal(err)
	}
	if n, err := st.PushoverSentSince(clk.Now().Add(-time.Hour)); err != nil || n != 2 {
		t.Fatalf("sent = %d, %v; want 2", n, err)
	}
	clk.Advance(33 * 24 * time.Hour)
	if err := st.RecordPushoverSend(); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.PushoverSentSince(time.Unix(0, 0)); n != 1 {
		t.Fatalf("old sends not pruned: %d", n)
	}

	if _, err := st.SetPushoverKey(u.ID, "new"); err != nil {
		t.Fatal(err)
	}
	if done, err := st.DisablePushoverKey(u.ID, "old"); err != nil || done {
		t.Fatalf("a rejection of the old key turned off the new one: %v, %v", done, err)
	}
	if done, err := st.DisablePushoverKey(u.ID, "new"); err != nil || !done {
		t.Fatalf("rejected current key: %v, %v", done, err)
	}
	if p, _, _ := st.PushoverFor(u.ID); p.Enabled {
		t.Fatal("still enabled after the key was rejected")
	}
	if err := st.Delete(u.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.PushoverFor(u.ID); ok {
		t.Fatal("deleting the account kept its Pushover key")
	}
}
