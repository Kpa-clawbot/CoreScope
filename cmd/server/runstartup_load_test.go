package main

// Tests for RunStartupLoad branch behavior and #1809 invariants
// (PR #1811 round-1 followups B2/B3/B4/B5).
//
// The pre-#1811 RunStartupLoad left several steady states undefined:
//   * hotStartupHours == 0 → backgroundLoadDone stayed false forever
//   * LoadChunked error    → both done & failed stayed false
//   * empty DB + no bg work needed → backgroundLoadDone stayed false
//
// These tests codify the post-#1811 contract:
//   * LoadChunked error → backgroundLoadFailed=true, done=false
//   * hotStartupHours == 0 → backgroundLoadDone=true, failed=false,
//     bg loader NOT called
//   * empty DB + hot window → backgroundLoadDone reflects coverage
//     (1.0 on empty DB → done=true, failed=false)
//   * call ordering inside RunStartupLoad: LoadChunked completes
//     before loadBackgroundChunks executes (so oldestLoaded is set)

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

// TestRunStartupLoad_HotStartupHoursZero_SetsDoneImmediately covers B3:
// when hotStartupHours == 0 the bg loader has no work to do; healthz
// must NOT be stuck on backgroundLoadComplete=false.
func TestRunStartupLoad_HotStartupHoursZero_SetsDoneImmediately(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	nowSec := time.Now().UTC().Unix()
	createTestDBWithLastSeen(t, dbPath, 10, 1, nowSec,
		30*time.Minute, 30*time.Minute)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  168,
		HotStartupHours: 0, // disable hot window → bg loader must not run
	})

	if err := store.RunStartupLoad(500); err != nil {
		t.Fatalf("RunStartupLoad: %v", err)
	}
	if !store.backgroundLoadDone.Load() {
		t.Fatalf("backgroundLoadDone must be true when hotStartupHours=0 (no bg work needed)")
	}
	if store.backgroundLoadFailed.Load() {
		t.Fatalf("backgroundLoadFailed must be false on the no-bg-work path; got error=%q",
			store.BackgroundLoadError())
	}
}

// TestRunStartupLoad_LoadChunkedError_SetsFailedTerminal covers B2:
// when LoadChunked errors, the steady state must be terminal
// (failed=true) — not the pre-fix (done=false, failed=false).
func TestRunStartupLoad_LoadChunkedError_SetsFailedTerminal(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	nowSec := time.Now().UTC().Unix()
	createTestDBWithLastSeen(t, dbPath, 5, 1, nowSec,
		30*time.Minute, 30*time.Minute)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	// Close the underlying connection to force LoadChunked to fail on
	// its very first query. We're explicitly verifying the failure path
	// terminal state, not the success path.
	_ = db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  168,
		HotStartupHours: 1,
	})

	loadErr := store.RunStartupLoad(500)
	if loadErr == nil {
		t.Fatalf("RunStartupLoad must return an error when LoadChunked fails")
	}
	if !store.backgroundLoadFailed.Load() {
		t.Fatalf("backgroundLoadFailed must be true after LoadChunked error (terminal state)")
	}
	if store.backgroundLoadDone.Load() {
		t.Fatalf("backgroundLoadDone must remain false on LoadChunked error")
	}
	if store.BackgroundLoadError() == "" {
		t.Fatalf("BackgroundLoadError must be non-empty after LoadChunked failure")
	}
}

// TestRunStartupLoad_EmptyDB_SetsDoneTerminal covers B4: empty DB with
// hot window > 0 — oldestLoaded stays "" because there are no packets.
// loadBackgroundChunks must reach its coverage block (totalInDB==0 →
// ratio=1.0) and set done=true rather than leaving the store stuck.
func TestRunStartupLoad_EmptyDB_SetsDoneTerminal(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	createTestDBWithLastSeen(t, dbPath, 0, 0, time.Now().UTC().Unix(),
		30*time.Minute, 30*time.Minute)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  168,
		HotStartupHours: 1,
	})

	if err := store.RunStartupLoad(500); err != nil {
		t.Fatalf("RunStartupLoad on empty DB: %v", err)
	}
	if !store.backgroundLoadDone.Load() {
		t.Fatalf("backgroundLoadDone must be true after empty-DB load (nothing to load == complete)")
	}
	if store.backgroundLoadFailed.Load() {
		t.Fatalf("backgroundLoadFailed must be false on empty DB; got %q",
			store.BackgroundLoadError())
	}
}

// TestRunStartupLoad_BgLoaderRunsAfterLoadChunkedSets_OldestLoaded
// covers B5/B6: assert the in-process call ordering inside
// RunStartupLoad. The OnChunkLoaded hook fires from LoadChunked; the
// loadBackgroundChunks panic guard fires only if oldestLoaded=="" at
// entry. So observing the chunk callback strictly before the bg loader
// (which is exercised via the loop continuing without panic) is the
// minimum guarantee. If a future refactor re-introduces the parallel
// spawn pattern, the runtime assertion in loadBackgroundChunks will
// trip and this test will fail.
func TestRunStartupLoad_BgLoaderRunsAfterLoadChunkedSets_OldestLoaded(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	nowSec := time.Now().UTC().Unix()
	createTestDBWithLastSeen(t, dbPath, 50, 1, nowSec,
		30*time.Minute, 30*time.Minute)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  168,
		HotStartupHours: 1,
	})

	// Hook: LoadChunked fires OnChunkLoaded after each chunk merge.
	// We record whether it fired before RunStartupLoad returned. The
	// runtime assertion in loadBackgroundChunks ensures the bg loader
	// observes a non-empty oldestLoaded; if a future refactor parallels
	// the bg-loader spawn with LoadChunked, that assertion panics.
	chunkSeen := false
	store.OnChunkLoaded(func(rowsThisChunk, totalRows int) {
		chunkSeen = true
	})

	if err := store.RunStartupLoad(500); err != nil {
		t.Fatalf("RunStartupLoad: %v", err)
	}
	if !chunkSeen {
		t.Fatalf("LoadChunked OnChunkLoaded did not fire — chunk loop did not execute before bg loader")
	}
	if store.oldestLoaded == "" {
		t.Fatalf("oldestLoaded is empty after RunStartupLoad — bg loader would have read \"\" and bailed")
	}
}

// TestLoadBackgroundChunks_PanicsOnOldestLoadedEmpty_Invariant covers the
// runtime assertion (A7). Manually populate s.packets without setting
// oldestLoaded and call loadBackgroundChunks directly — the panic guard
// must fire so future refactors cannot silently re-introduce the
// #1809 race.
func TestLoadBackgroundChunks_PanicsOnOldestLoadedEmpty_Invariant(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	// Reuse the existing schema-only fixture helper (0 rows) so this
	// test does not introduce a new inline CREATE TABLE block (pr-preflight
	// async-migration gate). The fixture provides exactly the bare schema
	// loadBackgroundChunks needs to reach its panic guard.
	createTestDBWithLastSeen(t, dbPath, 0, 0, time.Now().UTC().Unix(),
		30*time.Minute, 30*time.Minute)

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	defer db.conn.Close()

	store := NewPacketStore(db, &PacketStoreConfig{
		RetentionHours:  168,
		HotStartupHours: 1,
	})
	// Simulate the #1809 race: packets present, oldestLoaded never set.
	store.mu.Lock()
	store.packets = append(store.packets, &StoreTx{ID: 1, Hash: "deadbeef", FirstSeen: "2025-01-01T00:00:00Z"})
	store.oldestLoaded = ""
	store.mu.Unlock()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("loadBackgroundChunks must panic when oldestLoaded=\"\" with packets in store (#1809 invariant)")
		}
		msg := fmt.Sprintf("%v", r)
		if msg == "" {
			t.Fatalf("panic message must be non-empty")
		}
	}()
	store.loadBackgroundChunks()
}
