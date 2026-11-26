package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/users"
)

// recordingDestination stands in for a further destination (Pushover, a
// webhook) to pin what the notifier promises every destination.
type recordingDestination struct {
	t        *testing.T
	st       *users.Store
	calls    []string
	staged   int
	written  bool
	delivers int
}

func (d *recordingDestination) beginTick() { d.calls = append(d.calls, "begin") }

func (d *recordingDestination) stage(c notifyChanges, out *users.NotifyOutbox) func() {
	d.calls = append(d.calls, "stage")
	for _, list := range c.byUser {
		d.staged += len(list)
	}
	return func() {
		d.calls = append(d.calls, "done")
		states, err := d.st.AllNotifyStates()
		d.written = err == nil && len(states) > 0
	}
}

func (d *recordingDestination) deliver(_ context.Context, _ time.Time, c notifyChanges) int {
	d.calls = append(d.calls, "deliver")
	for _, list := range c.byUser {
		d.delivers += len(list)
	}
	return 7
}

func (d *recordingDestination) logKey() string { return "recorded" }

func TestNotifierHandsEveryDestinationTheSameChanges(t *testing.T) {
	f := newNotifyFixture(t, defaultNotifySettings())
	f.watcher(t, "pat@example.org", "Pat", evPkA, evPkB)
	f.setNode(evPkA, "Alpha", "companion", time.Hour, nil)
	f.setNode(evPkB, "Bravo", "companion", time.Hour, nil)
	rec := &recordingDestination{t: t, st: f.st}
	f.n.destinations = append(f.n.destinations, rec)

	f.tick()
	if got := strings.Join(rec.calls, ","); got != "begin,stage,done,deliver" {
		t.Fatalf("first tick calls = %s", got)
	}
	if !rec.written {
		t.Fatal("stage's done ran before the states were written")
	}
	f.clk.Advance(25 * time.Hour)
	f.src.newestAt = f.clk.Now
	rec.calls, rec.staged, rec.delivers = nil, 0, 0
	logs := captureLog(f.tick)
	if rec.staged != 2 || rec.delivers != 2 || len(f.notifyMails()) != 1 {
		t.Fatalf("staged %d, delivered %d, mails %d; want 2, 2, 1", rec.staged, rec.delivers, len(f.notifyMails()))
	}
	if !strings.Contains(logs, "changes=2 mails=1 recorded=7 ") {
		t.Fatalf("tick log = %q", logs)
	}
}

// Users with email off are still evaluated (for push), so a transition of
// theirs is routine and not logged as a skipped mail.
func TestEmailOffIsNotLoggedAsASkippedMail(t *testing.T) {
	f := newNotifyFixture(t, defaultNotifySettings())
	pat := f.watcher(t, "pat@example.org", "Pat", evPkA)
	if _, err := f.st.SetNotifyPrefs(pat.me.ID, false, []string{users.NotifyNodeOffline}); err != nil {
		t.Fatal(err)
	}
	f.setNode(evPkA, "Alpha", "companion", time.Hour, nil)
	f.tick()
	f.clk.Advance(25 * time.Hour)
	f.src.newestAt = f.clk.Now
	logs := captureLog(f.tick)
	if !strings.Contains(logs, "changes=1 mails=0") {
		t.Fatalf("no transition for the email-off user: %q", logs)
	}
	if strings.Contains(logs, "not mailed") {
		t.Fatalf("email-off user logged as skipped: %q", logs)
	}
}
