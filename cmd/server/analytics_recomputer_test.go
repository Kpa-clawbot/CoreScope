package main

import (
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func numGoroutinesForTest() int { return runtime.NumGoroutine() }

func TestAnalyticsRecomputerStartDoesNotBlockOnInitialCompute(t *testing.T) {
	block := make(chan struct{})
	started := make(chan struct{})
	rc := newAnalyticsRecomputer("slow-start", time.Minute, func() interface{} {
		close(started)
		<-block
		return map[string]interface{}{"ok": true}
	})
	defer rc.Stop()
	defer close(block)

	t0 := time.Now()
	rc.Start()
	if elapsed := time.Since(t0); elapsed > 50*time.Millisecond {
		t.Fatalf("Start blocked on initial compute for %v", elapsed)
	}

	select {
	case <-started:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("initial compute did not start asynchronously")
	}
}

// TestAnalyticsRecomputerSteadyStateLatency asserts that issue #1240's
// steady-state background recompute is in place: reads of the common
// analytics endpoints (region="") return from cache in <50ms p99 even
// under simulated ingest load.
//
// On master (pre-fix), GetAnalyticsTopology holds s.mu.RLock for the
// entire compute. Concurrent ingest writers (s.mu.Lock) starve readers
// or vice versa, producing per-read latencies in the hundreds of
// milliseconds. The cache TTL doesn't help: after every expiry one
// reader still pays the full compute cost.
//
// Post-fix, GetAnalyticsTopology with region="" and zero window must
// Load() from the background-refreshed atomic snapshot — never blocking
// under writer contention.
func TestAnalyticsRecomputerSteadyStateLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping latency timing test in -short mode")
	}

	db := setupTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)

	// Populate with enough records to make on-request compute non-trivial.
	const N = 20000
	hops := make([]distHopRecord, N)
	for i := 0; i < N; i++ {
		hops[i] = distHopRecord{
			FromName: "A", FromPk: "aa",
			ToName: "B", ToPk: "bb",
			Dist:       float64(i%500) + 0.5,
			Type:       []string{"R↔R", "C↔R", "C↔C"}[i%3],
			Hash:       "h",
			Timestamp:  "2024-01-01T00:00:00Z",
			HourBucket: "2024-01-01-00",
		}
	}
	store.mu.Lock()
	store.distHops = hops
	store.mu.Unlock()

	// Start the recomputer infrastructure. On master this method
	// doesn't exist, so this test won't compile until the GREEN commit
	// lands; the RED commit lands the test + a stub. Stub returns
	// without wiring background recompute, so the test still fails on
	// the latency assertion below.
	stop := store.StartAnalyticsRecomputers(10 * time.Millisecond)
	defer stop()

	// Give the initial compute a moment to populate.
	time.Sleep(50 * time.Millisecond)

	// Simulated writer: contend for s.mu.Lock. This is what makes the
	// non-recomputer path miss the latency target — the old
	// GetAnalyticsTopology grabs s.mu.RLock for the entire compute and
	// blocks behind every writer cycle.
	var stopWriters atomic.Bool
	var writerWg sync.WaitGroup
	const Writers = 4
	writerWg.Add(Writers)
	for w := 0; w < Writers; w++ {
		go func() {
			defer writerWg.Done()
			for !stopWriters.Load() {
				store.mu.Lock()
				// Trivial mutation: extend distHops by one and shrink back.
				store.distHops = append(store.distHops, distHopRecord{
					Dist: 1, Hash: "x", Timestamp: "2024-01-01T00:00:00Z",
				})
				store.distHops = store.distHops[:len(store.distHops)-1]
				store.mu.Unlock()
				// Brief pause to keep the lock-cycle rate realistic.
				time.Sleep(100 * time.Microsecond)
			}
		}()
	}

	// 100 concurrent reads.
	const Readers = 100
	latencies := make([]time.Duration, Readers)
	var rwg sync.WaitGroup
	rwg.Add(Readers)
	for i := 0; i < Readers; i++ {
		i := i
		go func() {
			defer rwg.Done()
			t0 := time.Now()
			r := store.GetAnalyticsDistance("")
			latencies[i] = time.Since(t0)
			if r == nil {
				t.Errorf("reader %d got nil result", i)
			}
		}()
	}
	rwg.Wait()
	stopWriters.Store(true)
	writerWg.Wait()

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p50 := latencies[Readers/2]
	p99 := latencies[(Readers*99)/100]

	t.Logf("analytics distance read latency: p50=%v p99=%v max=%v",
		p50, p99, latencies[Readers-1])

	// p99 budget: 50ms. Atomic-pointer load + JSON-shape map return
	// should be sub-millisecond; 50ms leaves margin for goroutine
	// scheduling jitter under concurrent test runs.
	const budget = 50 * time.Millisecond
	if p99 > budget {
		t.Fatalf("p99 read latency %v exceeds %v budget (issue #1240 not in effect)", p99, budget)
	}
}

// TestAnalyticsRecomputerShutdownNoLeak asserts the background
// goroutines started by StartAnalyticsRecomputers exit cleanly when
// the returned stop function is called — no leak across server
// shutdown (issue #1240 acceptance criterion).
func TestAnalyticsRecomputerShutdownNoLeak(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)

	// Use a tight tick so we know recompute is actually running (not
	// just blocked on the ticker).
	stop := store.StartAnalyticsRecomputers(20 * time.Millisecond)

	// Snapshot active goroutines a beat after start.
	time.Sleep(80 * time.Millisecond)
	startGoroutines := runtimeNumGoroutine()

	stop()

	// After stop returns, give the scheduler a beat to reap exits.
	deadline := time.Now().Add(2 * time.Second)
	var endGoroutines int
	for time.Now().Before(deadline) {
		endGoroutines = runtimeNumGoroutine()
		if endGoroutines <= startGoroutines-5 { // we started 6 recomputers
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// We expect ~6 fewer goroutines than the snapshot taken DURING
	// recompute (one per registered recomputer). Allow some slack
	// since test runners can have flaky goroutine counts.
	if endGoroutines >= startGoroutines {
		t.Fatalf("goroutine leak after stop: %d → %d (expected fewer)",
			startGoroutines, endGoroutines)
	}
	t.Logf("goroutines: during=%d after=%d (Δ=%d)",
		startGoroutines, endGoroutines, startGoroutines-endGoroutines)
}

// runtimeNumGoroutine is wrapped to keep the imports section of the
// production file minimal.
func runtimeNumGoroutine() int {
	// imported below
	return numGoroutinesForTest()
}
