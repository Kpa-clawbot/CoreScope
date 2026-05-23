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
	r := store.computeAnalyticsDistance("")
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
				rr := store.computeAnalyticsDistance("")
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

	// If readers hold the main RLock for their entire compute, every
	// writer Lock cycle waits for an active reader to release: avg cycle
	// >> 100µs at this data scale. After the refactor, readers hold the
	// main RLock only long enough to snapshot slice headers (<1µs), so
	// writer cycles complete in tens of microseconds.
	const MaxAvgMicros = 150
	if avgMicros > MaxAvgMicros {
		t.Fatalf("avg writer Lock/Unlock cycle %dµs exceeds %dµs threshold — computeAnalyticsDistance is holding the main RLock for too long and blocking writers (issue #1239)",
			avgMicros, MaxAvgMicros)
	}
}
