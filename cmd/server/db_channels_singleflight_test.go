package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestGetChannels_SingleflightCoalescesQueries (issue #2029) asserts that N
// concurrent cache-miss callers trigger at most ONE real query, using a call
// counter rather than timing. Timing alone ("finish within Nms of each
// other") passes on a fast machine even without coalescing, since it doesn't
// distinguish "one shared execution" from "N independent executions that all
// happened to be fast" — same rationale as TestEnsureNeighborGraph_Singleflight
// (#1203 Pair A) and statsSF (#1910).
//
// Anti-tautology: revert channelsSF.Do back to a bare call and this test
// fails (it observes N instead of 1).
func TestGetChannels_SingleflightCoalescesQueries(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	var calls int32
	db.channelsQueryHook = func() {
		atomic.AddInt32(&calls, 1)
		time.Sleep(50 * time.Millisecond) // ensure callers actually overlap
	}

	var wg sync.WaitGroup
	const N = 10
	errs := make(chan error, N)
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			if _, err := db.GetChannels(); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent GetChannels: %v", err)
	}

	got := atomic.LoadInt32(&calls)
	if got != 1 {
		// got==0 would mean the hook never fired (a broken test, not a
		// broken fix). got>1 means the query ran once per caller, i.e.
		// singleflight is missing or keyed wrong. Both are caught by !=1.
		t.Fatalf("expected exactly 1 real query under singleflight, got %d", got)
	}
}

// TestGetEncryptedChannels_SingleflightCoalescesQueries mirrors the above for
// GetEncryptedChannels/encChannelsSF, which has the identical bug shape and
// fix (see #2029).
func TestGetEncryptedChannels_SingleflightCoalescesQueries(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	var calls int32
	db.encChannelsQueryHook = func() {
		atomic.AddInt32(&calls, 1)
		time.Sleep(50 * time.Millisecond)
	}

	var wg sync.WaitGroup
	const N = 10
	errs := make(chan error, N)
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			if _, err := db.GetEncryptedChannels(); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent GetEncryptedChannels: %v", err)
	}

	got := atomic.LoadInt32(&calls)
	if got != 1 {
		t.Fatalf("expected exactly 1 real query under singleflight, got %d", got)
	}
}

// TestGetChannels_SingleflightPerRegion asserts channelsSF is keyed per
// region, not a single shared slot — two different regions queried
// concurrently must each get their own query, or a caller for region A would
// wrongly receive region B's coalesced result.
func TestGetChannels_SingleflightPerRegion(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	var calls int32
	db.channelsQueryHook = func() {
		atomic.AddInt32(&calls, 1)
		time.Sleep(30 * time.Millisecond)
	}

	var wg sync.WaitGroup
	regions := []string{"SAT", "DFW"}
	wg.Add(len(regions) * 5)
	for _, region := range regions {
		region := region
		for i := 0; i < 5; i++ {
			go func() {
				defer wg.Done()
				_, _ = db.GetChannels(region)
			}()
		}
	}
	wg.Wait()

	got := atomic.LoadInt32(&calls)
	if got != 2 {
		t.Fatalf("expected exactly 1 real query per distinct region key (2 regions), got %d", got)
	}
}
