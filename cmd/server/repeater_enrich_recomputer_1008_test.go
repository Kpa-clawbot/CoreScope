// Issue #1008 review M1: StartRepeaterEnrichmentRecomputer must wait
// for the background subpath+pathHop index builds before doing its
// synchronous prewarm — otherwise the prewarm reads an empty
// s.byPathHop and locks zeroed enrichment into s.repeaterRelayCache
// for the entire ticker interval.
package main

import (
	"testing"
	"time"
)

// TestIssue1008_M1_PrewarmWaitsForIndexes asserts that when the index
// ready flags are FALSE at the moment StartRepeaterEnrichmentRecomputer
// is called, the synchronous prewarm does NOT populate
// repeaterRelayCache (it either waits for ready, or skips). Without the
// fix the prewarm runs immediately against empty byPathHop and the
// cache becomes non-nil.
func TestIssue1008_M1_PrewarmWaitsForIndexes(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Wait for the background builder to finish so it can't race past
	// our Store(false) below. Once it's done it won't write the flags
	// again, so flipping them back to false is stable.
	if !store.WaitIndexesReady(5 * time.Second) {
		t.Fatal("background builds never finished")
	}
	// Force the ready flags back to false to simulate the race where
	// the recomputer is started before background builds finish.
	store.subpathReady.Store(false)
	store.pathHopReady.Store(false)

	// Use a tiny wait so the test runs fast. With the fix in place the
	// prewarm should time out waiting for ready and SKIP, leaving the
	// cache untouched. Without the fix it would compute immediately
	// against the empty byPathHop.
	prev := repeaterEnrichmentPrewarmWait
	repeaterEnrichmentPrewarmWait = 50 * time.Millisecond
	defer func() { repeaterEnrichmentPrewarmWait = prev }()

	stop := store.StartRepeaterEnrichmentRecomputer(24, time.Hour)
	defer stop()

	// Give the prewarm time to complete (or to skip).
	time.Sleep(150 * time.Millisecond)

	store.repeaterEnrichMu.Lock()
	cached := store.repeaterRelayCache
	at := store.repeaterRelayAt
	store.repeaterEnrichMu.Unlock()

	if cached != nil || !at.IsZero() {
		t.Fatalf("expected prewarm to SKIP when indexes not ready (cache==nil, at==zero); got cache=%v at=%v (#1008 M1)",
			cached != nil, at)
	}
}

// TestIssue1008_M1_PrewarmRunsWhenReady asserts the prewarm still runs
// (cache populated) when the indexes are already ready.
func TestIssue1008_M1_PrewarmRunsWhenReady(t *testing.T) {
	db := setupRichTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !store.WaitIndexesReady(5 * time.Second) {
		t.Fatal("indexes never ready")
	}

	stop := store.StartRepeaterEnrichmentRecomputer(24, time.Hour)
	defer stop()

	// Prewarm is synchronous on the caller's goroutine, so after
	// Start returns the cache must be populated.
	store.repeaterEnrichMu.Lock()
	at := store.repeaterRelayAt
	store.repeaterEnrichMu.Unlock()

	if at.IsZero() {
		t.Fatal("expected prewarm to populate repeaterRelayAt when indexes ready (#1008 M1)")
	}
}
