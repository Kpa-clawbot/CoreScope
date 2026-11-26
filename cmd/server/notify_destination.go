package main

import (
	"context"
	"time"

	"github.com/meshcore-analyzer/users"
)

// notifyDestination is one way node notifications reach users: email, a
// push relay. The notifier evaluates once per tick and hands every
// destination the same changes; it does not know which destinations exist.
//
// A tick calls beginTick before evaluating, stage for each destination
// while the states are saved, and deliver once they are saved.
type notifyDestination interface {
	// beginTick runs before the evaluation, so stage can tell whether the
	// destination's own configuration changed while the tick evaluated.
	beginTick()
	// stage adds the destination's rows to out, which is written in the
	// same transaction as the states. The returned func runs after that
	// write, whatever its outcome; it may be nil.
	stage(c notifyChanges, out *users.NotifyOutbox) (done func())
	// deliver runs after the states are saved and sends what the
	// destination delivers within the tick. It returns a count for the
	// tick log line, reported under logKey; an empty logKey reports none.
	deliver(ctx context.Context, now time.Time, c notifyChanges) int
	logKey() string
}

// notifyWorker is a destination with its own delivery loop (a queue
// drained between ticks). It runs until stop closes.
type notifyWorker interface {
	run(stop <-chan struct{})
}

// notifyChanges is one evaluation's result as the destinations see it.
type notifyChanges struct {
	byUser   map[int64][]notifyChange
	accounts map[int64]users.User
	// prefs holds the users' stored preferences; a user without a row was
	// evaluated with the defaults.
	prefs map[int64]users.NotifyPrefs
	nodes map[string]notifyNode
}

// notifyDestinations lists the destinations in the order they are logged.
// Email is always present; the push relay stages and sends nothing until
// the analyzer is enrolled with one.
func notifyDestinations(a *authService, now func() time.Time) []notifyDestination {
	return []notifyDestination{
		emailDestination{a: a},
		&relayDestination{a: a, now: now},
	}
}

// emailDestination mails each user's changes once per tick, within the
// email preference, the unsubscribe link and the daily limits.
type emailDestination struct{ a *authService }

func (emailDestination) beginTick()                                      {}
func (emailDestination) stage(notifyChanges, *users.NotifyOutbox) func() { return nil }
func (emailDestination) logKey() string                                  { return "mails" }
