package main

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestConcurrentIngestAndEviction exercises the race between IngestNewFromDB
// adding packets (via direct store manipulation simulating the locked section)
// and RunEviction removing packets. Without proper locking this would trigger
// the race detector and produce inconsistent index state.
func TestConcurrentIngestAndEviction(t *testing.T) {
	// Seed store with 200 old packets that are eligible for eviction
	startTime := time.Now().UTC().Add(-48 * time.Hour)
	store := makeTestStore(200, startTime, 1)
	store.retentionHours = 24 // everything older than 24h is evictable
	store.loaded = true

	// Track bytes for all seeded packets
	for _, tx := range store.packets {
		store.trackedBytes += estimateStoreTxBytes(tx)
		for _, obs := range tx.Observations {
			store.trackedBytes += estimateStoreObsBytes(obs)
		}
	}

	const numIngestGoroutines = 5
	const packetsPerGoroutine = 50
	const numEvictionGoroutines = 3

	var wg sync.WaitGroup
	var ingestedCount int64

	// Concurrent ingest: simulate what IngestNewFromDB does under the lock
	for g := 0; g < numIngestGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < packetsPerGoroutine; i++ {
				txID := 1000 + goroutineID*1000 + i
				hash := fmt.Sprintf("new_hash_%d_%04d", goroutineID, i)
				pt := 5 // GRP_TXT
				ts := time.Now().UTC().Format(time.RFC3339)

				tx := &StoreTx{
					ID:          txID,
					Hash:        hash,
					FirstSeen:   ts,
					LatestSeen:  ts,
					PayloadType: &pt,
					DecodedJSON: fmt.Sprintf(`{"pubKey":"newpk_%d_%04d"}`, goroutineID, i),
					obsKeys:     make(map[string]bool),
					observerSet: make(map[string]bool),
				}

				obs := &StoreObs{
					ID:             txID*10 + 1,
					TransmissionID: txID,
					ObserverID:     fmt.Sprintf("obs_g%d", goroutineID),
					ObserverName:   fmt.Sprintf("Observer_g%d", goroutineID),
					Timestamp:      ts,
				}
				tx.Observations = append(tx.Observations, obs)
				tx.ObservationCount = 1

				// Acquire write lock (same as IngestNewFromDB)
				store.mu.Lock()
				store.packets = append(store.packets, tx)
				store.byHash[hash] = tx
				store.byTxID[txID] = tx
				store.byObsID[obs.ID] = obs
				store.byObserver[obs.ObserverID] = append(store.byObserver[obs.ObserverID], obs)
				store.byPayloadType[pt] = append(store.byPayloadType[pt], tx)
				pk := fmt.Sprintf("newpk_%d_%04d", goroutineID, i)
				if store.nodeHashes[pk] == nil {
					store.nodeHashes[pk] = make(map[string]bool)
				}
				store.nodeHashes[pk][hash] = true
				store.byNode[pk] = append(store.byNode[pk], tx)
				store.trackedBytes += estimateStoreTxBytes(tx)
				store.trackedBytes += estimateStoreObsBytes(obs)
				store.totalObs++
				store.mu.Unlock()

				atomic.AddInt64(&ingestedCount, 1)
			}
		}(g)
	}

	// Concurrent eviction goroutines
	var evictedTotal int64
	for g := 0; g < numEvictionGoroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				store.mu.Lock()
				n := store.EvictStale()
				store.mu.Unlock()
				atomic.AddInt64(&evictedTotal, int64(n))
				time.Sleep(time.Millisecond)
			}
		}()
	}

	// Concurrent readers (QueryPackets uses RLock)
	for g := 0; g < 3; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				store.mu.RLock()
				_ = len(store.packets)
				_ = len(store.byHash)
				store.mu.RUnlock()
				time.Sleep(500 * time.Microsecond)
			}
		}()
	}

	wg.Wait()

	// --- Post-state assertions ---
	store.mu.RLock()
	defer store.mu.RUnlock()

	totalIngested := int(atomic.LoadInt64(&ingestedCount))
	totalEvicted := int(atomic.LoadInt64(&evictedTotal))

	if totalIngested != numIngestGoroutines*packetsPerGoroutine {
		t.Fatalf("expected %d ingested, got %d", numIngestGoroutines*packetsPerGoroutine, totalIngested)
	}

	// Invariant: packets remaining = initial(200) + ingested - evicted
	expectedRemaining := 200 + totalIngested - totalEvicted
	if len(store.packets) != expectedRemaining {
		t.Fatalf("packets count mismatch: got %d, expected %d (200 + %d ingested - %d evicted)",
			len(store.packets), expectedRemaining, totalIngested, totalEvicted)
	}

	// Invariant: byHash must be consistent with packets slice
	if len(store.byHash) != len(store.packets) {
		t.Fatalf("byHash size %d != packets len %d", len(store.byHash), len(store.packets))
	}

	// Invariant: every packet in the slice must be in byHash
	for _, tx := range store.packets {
		if store.byHash[tx.Hash] != tx {
			t.Fatalf("packet %s in slice but not in byHash (or points to different tx)", tx.Hash)
		}
	}

	// Invariant: byTxID must map to packets in the slice
	byTxIDCount := 0
	for _, tx := range store.packets {
		if store.byTxID[tx.ID] == tx {
			byTxIDCount++
		}
	}
	if byTxIDCount != len(store.packets) {
		t.Fatalf("byTxID consistency: %d/%d packets found", byTxIDCount, len(store.packets))
	}

	// Invariant: trackedBytes must be non-negative
	if store.trackedBytes < 0 {
		t.Fatalf("trackedBytes went negative: %d", store.trackedBytes)
	}

	// Verify eviction actually happened (old packets were eligible)
	if totalEvicted == 0 {
		t.Fatal("expected some evictions to occur but got 0")
	}

	t.Logf("OK: ingested=%d, evicted=%d, remaining=%d, trackedBytes=%d",
		totalIngested, totalEvicted, len(store.packets), store.trackedBytes)
}

// TestConcurrentIngestNewObservationsAndEviction exercises the race between
// adding new observations to existing transmissions and eviction removing those
// same transmissions. This targets the IngestNewObservations path.
func TestConcurrentIngestNewObservationsAndEviction(t *testing.T) {
	// Create store with 100 packets, half old (evictable), half recent
	now := time.Now().UTC()
	store := makeTestStore(0, now, 1) // empty, we'll add manually
	store.retentionHours = 1

	// Add 50 old packets (2h ago) and 50 recent packets
	for i := 0; i < 100; i++ {
		var ts time.Time
		if i < 50 {
			ts = now.Add(-2 * time.Hour).Add(time.Duration(i) * time.Second)
		} else {
			ts = now.Add(-time.Duration(100-i) * time.Second)
		}
		hash := fmt.Sprintf("obs_hash_%04d", i)
		txID := i + 1
		pt := 4
		tx := &StoreTx{
			ID:          txID,
			Hash:        hash,
			FirstSeen:   ts.UTC().Format(time.RFC3339),
			LatestSeen:  ts.UTC().Format(time.RFC3339),
			PayloadType: &pt,
			DecodedJSON: fmt.Sprintf(`{"pubKey":"pk%04d"}`, i),
			obsKeys:     make(map[string]bool),
			observerSet: make(map[string]bool),
		}
		store.packets = append(store.packets, tx)
		store.byHash[hash] = tx
		store.byTxID[txID] = tx
		store.byPayloadType[pt] = append(store.byPayloadType[pt], tx)
		store.trackedBytes += estimateStoreTxBytes(tx)
	}
	store.loaded = true

	const numObsGoroutines = 4
	const obsPerGoroutine = 100

	var wg sync.WaitGroup
	var addedObs int64

	// Goroutines adding observations to RECENT packets (index 50-99)
	for g := 0; g < numObsGoroutines; g++ {
		wg.Add(1)
		go func(gID int) {
			defer wg.Done()
			for i := 0; i < obsPerGoroutine; i++ {
				targetIdx := 50 + (i % 50) // only target recent packets
				hash := fmt.Sprintf("obs_hash_%04d", targetIdx)

				store.mu.Lock()
				tx := store.byHash[hash]
				if tx != nil {
					obsID := 50000 + gID*10000 + i
					obs := &StoreObs{
						ID:             obsID,
						TransmissionID: tx.ID,
						ObserverID:     fmt.Sprintf("obs_new_%d", gID),
						ObserverName:   fmt.Sprintf("NewObs_%d", gID),
						Timestamp:      time.Now().UTC().Format(time.RFC3339),
					}
					dk := obs.ObserverID + "|"
					if !tx.obsKeys[dk] || true { // allow duplicates for stress
						tx.Observations = append(tx.Observations, obs)
						tx.ObservationCount++
						store.byObsID[obsID] = obs
						store.byObserver[obs.ObserverID] = append(store.byObserver[obs.ObserverID], obs)
						store.trackedBytes += estimateStoreObsBytes(obs)
						store.totalObs++
						atomic.AddInt64(&addedObs, 1)
					}
				}
				store.mu.Unlock()
			}
		}(g)
	}

	// Concurrent eviction
	var evictedTotal int64
	for g := 0; g < 2; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 15; i++ {
				store.mu.Lock()
				n := store.EvictStale()
				store.mu.Unlock()
				atomic.AddInt64(&evictedTotal, int64(n))
				time.Sleep(500 * time.Microsecond)
			}
		}()
	}

	wg.Wait()

	// --- Assertions ---
	store.mu.RLock()
	defer store.mu.RUnlock()

	totalEvicted := int(atomic.LoadInt64(&evictedTotal))
	totalAdded := int(atomic.LoadInt64(&addedObs))

	// All 50 old packets should have been evicted
	if totalEvicted < 50 {
		t.Fatalf("expected at least 50 evictions (old packets), got %d", totalEvicted)
	}

	// Recent packets (50) should survive
	if len(store.packets) < 50 {
		t.Fatalf("expected at least 50 remaining packets (recent ones), got %d", len(store.packets))
	}

	// byHash consistency
	for _, tx := range store.packets {
		if store.byHash[tx.Hash] != tx {
			t.Fatalf("byHash inconsistency for %s", tx.Hash)
		}
	}

	// No evicted packet should remain in byHash
	for i := 0; i < 50; i++ {
		hash := fmt.Sprintf("obs_hash_%04d", i)
		if store.byHash[hash] != nil {
			t.Fatalf("evicted packet %s still in byHash", hash)
		}
	}

	// byObsID should not reference observations from evicted packets
	for obsID, obs := range store.byObsID {
		if store.byTxID[obs.TransmissionID] == nil {
			t.Fatalf("byObsID[%d] references evicted transmission %d", obsID, obs.TransmissionID)
		}
	}

	// trackedBytes non-negative
	if store.trackedBytes < 0 {
		t.Fatalf("trackedBytes negative: %d", store.trackedBytes)
	}

	t.Logf("OK: evicted=%d, added_obs=%d, remaining=%d, trackedBytes=%d",
		totalEvicted, totalAdded, len(store.packets), store.trackedBytes)
}

// TestConcurrentRunEvictionWithReads exercises RunEviction's two-phase locking
// against concurrent read operations (simulating QueryPackets / GetStoreStats).
// Without proper RWMutex usage, this would race on slice/map reads.
func TestConcurrentRunEvictionWithReads(t *testing.T) {
	startTime := time.Now().UTC().Add(-3 * time.Hour)
	store := makeTestStore(500, startTime, 1)
	store.retentionHours = 1
	store.loaded = true

	for _, tx := range store.packets {
		store.trackedBytes += estimateStoreTxBytes(tx)
		for _, obs := range tx.Observations {
			store.trackedBytes += estimateStoreObsBytes(obs)
		}
	}

	var wg sync.WaitGroup

	// Multiple RunEviction calls (uses its own locking)
	var evicted int64
	for g := 0; g < 3; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n := store.RunEviction()
			atomic.AddInt64(&evicted, int64(n))
		}()
	}

	// Concurrent readers using the public read-lock pattern
	var readCount int64
	for g := 0; g < 5; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				store.mu.RLock()
				count := len(store.packets)
				_ = count
				// Iterate a portion of byHash (simulating query)
				for hash, tx := range store.byHash {
					_ = hash
					_ = tx.ObservationCount
					break // just access one
				}
				store.mu.RUnlock()
				atomic.AddInt64(&readCount, 1)
			}
		}()
	}

	wg.Wait()

	store.mu.RLock()
	defer store.mu.RUnlock()

	totalEvicted := int(atomic.LoadInt64(&evicted))

	// Must have evicted packets older than 1h (most of the 500 are 1-3h old)
	if totalEvicted == 0 {
		t.Fatal("expected evictions but got 0")
	}

	// Consistency: byHash == packets len
	if len(store.byHash) != len(store.packets) {
		t.Fatalf("byHash %d != packets %d after concurrent RunEviction+reads",
			len(store.byHash), len(store.packets))
	}

	// All reads completed without panic
	if atomic.LoadInt64(&readCount) != 250 {
		t.Fatalf("not all reads completed: %d/250", atomic.LoadInt64(&readCount))
	}

	t.Logf("OK: evicted=%d, remaining=%d, reads=%d",
		totalEvicted, len(store.packets), atomic.LoadInt64(&readCount))
}
