package main

import (
	"sync"
	"testing"
	"time"
)

// TestGetPerfStoreStatsTyped_ConcurrentWithIngest is a regression test for the
// data race where GetPerfStoreStatsTyped read s.trackedBytes after releasing
// s.mu.RLock, while ingest mutated s.trackedBytes under s.mu.Lock.
//
// It runs a stats reader concurrently with a writer that grows s.packets and
// s.trackedBytes under the write lock — exactly the ingest pattern. Run with
// `go test -race`; before the fix this fails with a reported data race on
// PacketStore.trackedBytes.
func TestGetPerfStoreStatsTyped_ConcurrentWithIngest(t *testing.T) {
	store := makeTestStore(10, time.Now().Add(-1*time.Hour), 5)

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Writer: simulate ingest growing the store under s.mu.Lock.
	wg.Add(1)
	go func() {
		defer wg.Done()
		nextID := 10000
		for {
			select {
			case <-stop:
				return
			default:
			}
			tx := &StoreTx{
				ID:        nextID,
				Hash:      "racehash",
				FirstSeen: time.Now().UTC().Format(time.RFC3339),
			}
			store.mu.Lock()
			store.packets = append(store.packets, tx)
			store.trackedBytes += estimateStoreTxBytes(tx)
			store.mu.Unlock()
			nextID++
		}
	}()

	// Readers: hit the stats endpoint that reads trackedBytes.
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				stats := store.GetPerfStoreStatsTyped()
				_ = stats.TrackedMB
				_ = stats.AvgBytesPerPacket
				// trackedMemoryMB takes its own RLock — exercise that path too.
				_ = store.trackedMemoryMB()
			}
		}()
	}

	// Let readers run for a bit, then stop the writer.
	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}
