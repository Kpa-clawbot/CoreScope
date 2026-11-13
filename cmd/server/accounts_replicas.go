package main

// Several server replicas sharing one PostgreSQL account store: background
// jobs that write the store run on one replica at a time, and the
// per-process caches of account data are reloaded so a change made on one
// replica reaches the others. On SQLite (users.db, one process) none of this
// changes anything: TryExclusive is always granted and nothing reloads.

import (
	"context"
	"log"
	"time"
)

// exclusiveLockTimeout bounds taking a job lock, and the janitor's store
// calls that take a context.
const exclusiveLockTimeout = 10 * time.Second

// pgCacheReloadInterval is how often a replica reloads its account caches
// from a PostgreSQL store, so another replica's change shows here within it.
const pgCacheReloadInterval = 30 * time.Second

// exclusiveJob is one background job's single-instance lock, with the state
// that keeps the skip from being logged on every tick. Not safe for
// concurrent use: each job's goroutine owns its exclusiveJob.
type exclusiveJob struct {
	name     string
	skipping bool // the last attempt found the lock held elsewhere
}

// acquire takes the job's lock through try (users.Store.TryExclusive). ok is
// false when another replica holds it, or when taking it failed (logged);
// the caller then skips this run. A replica that starts skipping logs it
// once, and once more when it runs the job again. release is never nil.
func (j *exclusiveJob) acquire(ctx context.Context, try func(context.Context, string) (func(), bool, error)) (release func(), ok bool) {
	release, ok, err := try(ctx, j.name)
	switch {
	case err != nil:
		log.Printf("[users] %s: job lock: %v; skipped this time", j.name, err)
		return release, false
	case !ok:
		if !j.skipping {
			log.Printf("[users] %s: another replica runs this job; this one skips it while that replica holds the lock", j.name)
		}
		j.skipping = true
		return release, false
	}
	if j.skipping {
		log.Printf("[users] %s: this replica holds the job lock again and runs it", j.name)
	}
	j.skipping = false
	return release, true
}

// reloadCachesLoop reloads the account caches every interval until stop
// closes. Only started for a PostgreSQL store.
func (a *authService) reloadCachesLoop(every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-a.stop:
			return
		case <-t.C:
			a.reloadCaches()
		}
	}
}

// reloadCaches re-reads what this process caches from the store: the
// approved channels and the observer login index. A failure is logged and
// keeps the cache as it was.
func (a *authService) reloadCaches() {
	a.refreshApproved()
	if a.obsIdx != nil {
		if err := a.obsIdx.reload(a.st); err != nil {
			log.Printf("[observer-logins] reload logins: %v", err)
		}
	}
}
