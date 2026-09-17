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
// holds RLock for the entire compute, so each writer cycle waits behind an
// active reader. Post-fix, readers hold RLock only long enough to grab
// slice headers, so writer cycles complete unimpeded.
//
// The threshold comes from measurement, and the gap it has to straddle is
// enormous (issue #2038). On CI runners:
//
//	healthy    156µs, 222µs, 402µs   (three readings, three commits)
//	regressed  201203µs              (RLock deliberately held across the compute)
//
// The old limit was a flat 150µs, which sits *inside* the healthy band, so
// it failed on two consecutive master commits that passed on re-run without
// a byte changed. 5ms is 12x above the worst healthy reading and 40x below
// the regression, which is the widest possible separation from both. The
// scale of the gap is the point: a #1239 regression is not marginal, it
// serializes a millisecond-scale compute behind every writer.
func TestComputeAnalyticsDistanceLockHoldDuration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping concurrency timing test in -short mode")
	}

	db := setupTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)

	// Populate distHops/distPaths with enough records that compute takes
	// a measurable amount of time (~ms). With region="", compute never
	// dereferences distHopRecord.tx, so dummy zero-value records suffice.
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

	// Background readers churn computeAnalyticsDistance.
	const Readers = 8
	var stop atomic.Bool
	var readerErrs atomic.Int64
	var wg sync.WaitGroup
	wg.Add(Readers)
	for i := 0; i < Readers; i++ {
		go func() {
			defer wg.Done()
			for !stop.Load() {
				rr := store.computeAnalyticsDistance("", "")
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

	// Measure writer (mu.Lock/Unlock) throughput.
	const WriterCycles = 200
	start := time.Now()
	for i := 0; i < WriterCycles; i++ {
		store.mu.Lock()
		store.mu.Unlock()
	}
	elapsed := time.Since(start)

	stop.Store(true)
	wg.Wait()

	if readerErrs.Load() > 0 {
		t.Fatalf("readers returned empty/invalid results: %d", readerErrs.Load())
	}

	avgMicros := elapsed.Microseconds() / int64(WriterCycles)
	t.Logf("avg writer Lock/Unlock cycle: %dµs over %d cycles (total %v) with %d concurrent readers, %d hops, %d paths",
		avgMicros, WriterCycles, elapsed, Readers, N, len(paths))

	// See the measurements in the doc comment: healthy runs land in the
	// hundreds of microseconds, a regression two orders of magnitude above
	// that. Anything in between is a genuine change in lock-hold behaviour
	// and deserves to be looked at, not re-run.
	const MaxAvgMicros = 5000
	if avgMicros > MaxAvgMicros {
		t.Fatalf("avg writer Lock/Unlock cycle %dµs exceeds %dµs threshold — computeAnalyticsDistance is holding the main RLock for too long and blocking writers (issue #1239)",
			avgMicros, MaxAvgMicros)
	}
}
