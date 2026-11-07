package main

import (
	"path/filepath"
	"testing"
	"time"
)

func TestHealthzCountsExcludeUnpublishedObservations(t *testing.T) {
	readiness.Store(1)
	t.Cleanup(func() { readiness.Store(0) })
	t.Setenv("CORESCOPE_INGESTOR_STATS", filepath.Join(t.TempDir(), "absent.json"))
	db := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	now := time.Now().UTC()
	hot, cold := now.Add(-30*time.Minute), now.Add(-48*time.Hour)
	mustExec(t, db, `INSERT INTO observers(id) VALUES ('obs1'),('obs2')`)
	mustExec(t, db, `INSERT INTO transmissions(id,raw_hex,hash,first_seen) VALUES (1,'AA','hot',?),(2,'BB','cold',?)`, hot.Format(time.RFC3339), cold.Format(time.RFC3339))
	mustExec(t, db, `INSERT INTO observations(id,transmission_id,observer_idx,path_json,timestamp) VALUES (1,1,1,'[]',?),(2,2,1,'[]',?)`, hot.Unix(), cold.Unix())
	store := NewPacketStore(db, &PacketStoreConfig{RetentionHours: 72, HotStartupHours: 2})
	if err := store.LoadChunked(1); err != nil {
		t.Fatal(err)
	}
	assertHealthzCounts(t, store, 1, 1)

	// Derive the pending packet and its indexes from the real cold loader.
	chunk := NewPacketStore(db, nil)
	if err := chunk.loadChunk(now.Add(-72*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	assertHealthzCounts(t, chunk, 1, 1)
	localObs, localBytes := chunk.totalObs, chunk.trackedBytes
	pending := chunk.packets[0]

	// This is loadChunk's batch-publication state before its final merge:
	// indexes are visible, but localPackets/localTotalObs are not. These
	// packets have no payload/node/relay indexes. An observation poll that
	// passed refreshAdvertEvidence before the loader acquired its gate can
	// merge a late row here. Construct the state without scheduler races.
	store.mu.Lock()
	store.byHash[pending.Hash] = pending
	store.byTxID[pending.ID] = pending
	for id, obs := range chunk.byObsID {
		store.byObsID[id] = obs
		store.byObserver[obs.ObserverID] = append(store.byObserver[obs.ObserverID], obs)
	}
	store.mu.Unlock()
	mustExec(t, db, `INSERT INTO observations(id,transmission_id,observer_idx,path_json,timestamp) VALUES (3,2,2,'[]',?)`, now.Unix())
	if got := store.IngestNewObservations(2, 10); len(got) != 1 {
		t.Fatalf("late observation was not ingested: %d broadcasts", len(got))
	}
	if len(store.packets) != 1 || len(pending.Observations) != 2 || store.totalObs != 2 {
		t.Fatalf("publication fixture: packets=%d pending observations=%d counter=%d", len(store.packets), len(pending.Observations), store.totalObs)
	}
	if !store.LoadComplete() || isClosedForTest(store.StartupLoadDone()) {
		t.Fatal("hot load must be complete while background publication remains pending")
	}
	got := readHealthzCounts(t, store)
	if got.LoadedTx != 1 || got.LoadedObs != 1 {
		t.Errorf("during background publication: healthz=%d/%d, want 1/1; the late observation belongs to an unpublished packet", got.LoadedTx, got.LoadedObs)
	}

	// Finish the same packet/counter publication as loadChunk, using the
	// original local contribution; live ingestion already counted the late row.
	store.mu.Lock()
	store.packets = mergeChunkIntoPackets(chunk.packets, store.packets)
	store.totalObs += localObs
	store.trackedBytes += localBytes
	store.maxTxID = chunk.maxTxID
	store.invalidateAdvertEvidence()
	store.mu.Unlock()
	store.signalStartupLoadDone()
	assertHealthzCounts(t, store, 2, 3)
}
