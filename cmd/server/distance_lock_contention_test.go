package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestComputeAnalyticsDistanceLockHoldDuration asserts that
// computeAnalyticsDistance does NOT hold s.mu.RLock() for the entire
// compute — otherwise readers serialize writers (which need s.mu.Lock for
// ingest / buildDistanceIndex), turning a 3s analytics call into 15s under
// heavy ingest (issue #1239).
//
// Methodology: run N reader goroutines calling computeAnalyticsDistance
// continuously, while the test goroutine measures how long it takes to
// complete W bare mu.Lock()/mu.Unlock() cycles. Each writer cycle must
// wait for ALL currently-holding RLocks to release. Pre-fix, every reader
// holds RLock for the entire compute (~ms), so each writer cycle waits
// behind an active reader → avg cycle hundreds of microseconds to
// milliseconds. Post-fix, readers hold RLock only long enough to grab
// slice headers (microseconds), so writer cycles complete unimpeded.
//
// The threshold is calibrated per run, not hardcoded. Eight readers
// saturating the CPU slow the writer's own cycles down whether or not any
// lock is held, and an absolute limit cannot tell that apart from a lock
// held too long: this test failed twice on 2026-09-17 CI at 156µs and
// 222µs against a flat 150µs limit, on trees that passed on re-run without
// a single byte changed. So the same measurement runs twice, the second
// time against a store the writer never locks, and the readers' CPU cost
// is subtracted by comparison instead of guessed.
func TestComputeAnalyticsDistanceLockHoldDuration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrency timing test in -short mode")
	}

	db := setupTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)

	hops, paths := distLockFixture()
	store.mu.Lock()
	store.distHops = hops
	store.distPaths = paths
	store.mu.Unlock()

	// Sanity: result is non-empty.
	r := store.computeAnalyticsDistance("", "")
	if r == nil {
		t.Fatal("expected non-nil result")
	}
	if _, ok := r["topHops"]; !ok {
		t.Fatal("expected topHops in result")
	}

	// Control: the identical compute, on a second store that the writer
	// never locks. Same code path, same allocation churn, same number of
	// runnable goroutines competing for the same cores, and no interaction
	// whatsoever with the mutex being measured. Whatever this costs is the
	// price of a busy machine rather than of a lock held too long.
	//
	// The two stores share the fixture slices. Both only ever read them,
	// and the writer's Lock/Unlock cycles mutate nothing.
	control := NewPacketStore(db, nil)
	control.mu.Lock()
	control.distHops = hops
	control.distPaths = paths
	control.mu.Unlock()

	baselineMicros := distLockWriterCycles(t, store, control)
	avgMicros := distLockWriterCycles(t, store, store)

	// A regression under #1239 is not marginal: readers would hold the
	// RLock across a compute that takes milliseconds at this data scale,
	// so writer cycles land an order of magnitude or more above the
	// control. 4x leaves room for the real handoff cost of a correctly
	// short RLock without letting that regression through.
	//
	// The old flat 150µs stays as a floor, so on a quiet machine this test
	// is exactly as strict as it was before, and it only ever relaxes when
	// the control proves the machine itself is slow.
	const maxOverControl = 4
	const minLimitMicros = 150
	limit := baselineMicros * maxOverControl
	if limit < minLimitMicros {
		limit = minLimitMicros
	}

	t.Logf("avg writer Lock/Unlock cycle: %dµs with readers on the same store, %dµs with readers on a separate store (control), limit %dµs (max(%dx control, %dµs))",
		avgMicros, baselineMicros, limit, maxOverControl, minLimitMicros)

	if avgMicros > limit {
		t.Fatalf("avg writer Lock/Unlock cycle %dµs exceeds %dµs (%dx the %dµs control) — computeAnalyticsDistance is holding the main RLock for too long and blocking writers (issue #1239)",
			avgMicros, limit, maxOverControl, baselineMicros)
	}
}

// distLockFixture builds distHops/distPaths large enough that one compute
// takes a measurable amount of time (~ms). With region="", compute never
// dereferences distHopRecord.tx, so dummy zero-value records suffice.
func distLockFixture() ([]distHopRecord, []distPathRecord) {
	const N = 20000
	hops := make([]distHopRecord, N)
	for i := 0; i < N; i++ {
		hops[i] = distHopRecord{
			FromName:   "A",
			FromPk:     "aa",
			ToName:     "B",
			ToPk:       "bb",
			Dist:       float64(i%500) + 0.5,
			Type:       []string{"R↔R", "C↔R", "C↔C"}[i%3],
			Hash:       "h",
			Timestamp:  "2024-01-01T00:00:00Z",
			HourBucket: "2024-01-01-00",
		}
	}
	paths := make([]distPathRecord, 200)
	for i := range paths {
		paths[i] = distPathRecord{
			Hash:      "p",
			TotalDist: float64(i),
			HopCount:  3,
			Timestamp: "2024-01-01T00:00:00Z",
			Hops: []distHopDetail{
				{FromName: "A", FromPk: "aa", ToName: "B", ToPk: "bb", Dist: 1},
			},
		}
	}
	return hops, paths
}

// distLockWriterCycles returns the average duration of a bare
// writeTo.mu.Lock/Unlock cycle, in microseconds, while eight goroutines
// churn readFrom.computeAnalyticsDistance.
//
// Passing the same store as both arguments measures what this test is
// about. Passing a different store as readFrom measures the same work
// under the same CPU load with the mutex left alone, which is the control.
func distLockWriterCycles(t *testing.T, writeTo, readFrom *PacketStore) int64 {
	t.Helper()

	const Readers = 8
	const WriterCycles = 200

	var stop atomic.Bool
	var readerErrs atomic.Int64
	var wg sync.WaitGroup
	wg.Add(Readers)
	for i := 0; i < Readers; i++ {
		go func() {
			defer wg.Done()
			for !stop.Load() {
				rr := readFrom.computeAnalyticsDistance("", "")
				if rr == nil {
					readerErrs.Add(1)
				}
				if _, ok := rr["topHops"]; !ok {
					readerErrs.Add(1)
				}
			}
		}()
	}

	// Let readers ramp up.
	time.Sleep(50 * time.Millisecond)

	start := time.Now()
	for i := 0; i < WriterCycles; i++ {
		writeTo.mu.Lock()
		writeTo.mu.Unlock()
	}
	elapsed := time.Since(start)

	stop.Store(true)
	wg.Wait()

	if readerErrs.Load() > 0 {
		t.Fatalf("readers returned empty/invalid results: %d", readerErrs.Load())
	}
	return elapsed.Microseconds() / int64(WriterCycles)
}
