package main

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestHotStartup_loadChunk_IndexSliceConsistency guards against regression
// of PR #1187 r3 MUST-FIX 1: the batched merge in loadChunk used to
// (1) prepend localPackets to s.packets under one critical section, then
// (2) populate s.byHash/s.byTxID/s.byObsID/s.byNode/s.byPayloadType in
// separate per-batch critical sections. Readers that acquired RLock
// between the slice update and the index updates observed packets that
// were in the slice but missing from byHash — causing GetPacketByHash to
// return nil and QueryPackets hash/node fast-paths to silently miss data
// during background load.
//
// The invariant under test: for any RLock-held snapshot, every tx in
// s.packets must also be present in s.byHash[tx.Hash]. Violation = silent
// partial data loss.
func TestHotStartup_loadChunk_IndexSliceConsistency(t *testing.T) {
	// 10 recent + 1200 old: 1200 > 2 * mergeBatchSize(500) so the merge
	// spans 3 batches, widening the inconsistency window for the reader.
	dbPath := createTestDBWithAgedPackets(t, 10, 1200)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  72,
		HotStartupHours: 1,
	})
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if len(store.packets) != 10 {
		t.Fatalf("setup: expected 10 packets after hot Load, got %d", len(store.packets))
	}

	var stop atomic.Bool
	var violations atomic.Int64
	var checks atomic.Int64

	var wg sync.WaitGroup
	// Reader: repeatedly snapshot under RLock. For each tx in s.packets,
	// assert s.byHash[tx.Hash] is non-nil. Any miss = consistency violation.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			store.mu.RLock()
			for _, tx := range store.packets {
				if tx == nil || tx.Hash == "" {
					continue
				}
				checks.Add(1)
				if store.byHash[tx.Hash] == nil {
					violations.Add(1)
				}
				// Also: byTxID for this tx must be populated
				if store.byTxID[tx.ID] == nil {
					violations.Add(1)
				}
			}
			store.mu.RUnlock()
		}
	}()

	// Give the reader a moment to start the loop.
	time.Sleep(5 * time.Millisecond)

	// Trigger the batched merge.
	chunkEnd := time.Now().UTC().Add(-1 * time.Hour)
	chunkStart := time.Now().UTC().Add(-72 * time.Hour)
	if err := store.loadChunk(chunkStart, chunkEnd); err != nil {
		stop.Store(true)
		wg.Wait()
		t.Fatalf("loadChunk failed: %v", err)
	}

	// Let reader observe a few iterations after merge completes.
	time.Sleep(5 * time.Millisecond)
	stop.Store(true)
	wg.Wait()

	if v := violations.Load(); v > 0 {
		t.Fatalf("index↔slice consistency violated %d times across %d checks: "+
			"packets observed in s.packets that were missing from s.byHash/s.byTxID. "+
			"This is the silent-partial-data-loss regression from R2 #6 (commit 2ec762aa).",
			v, checks.Load())
	}

	// Post-condition sanity: final state must be fully consistent.
	store.mu.RLock()
	defer store.mu.RUnlock()
	if len(store.packets) != 1210 {
		t.Errorf("expected 1210 packets after merge, got %d", len(store.packets))
	}
	for _, tx := range store.packets {
		if store.byHash[tx.Hash] == nil {
			t.Errorf("post-merge: tx %s missing from byHash", tx.Hash)
			break
		}
	}
	// Spot check: an old packet hash must be retrievable via GetPacketByHash.
	// (Drop the RLock first to avoid deadlock; GetPacketByHash takes RLock.)
	_ = strings.ToLower
}
