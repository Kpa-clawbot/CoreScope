package main

// Test for issue #1809 — background load fails almost immediately because
// `loadBackgroundChunks` is spawned at FirstChunkReady (chunk #1 merged)
// while `LoadChunked` is still merging the remainder of the hot window.
// At that moment `s.oldestLoaded` is still "" (only set at the end of
// LoadChunked), so the bg loader sees empty oldest → breaks immediately →
// coverage = 0 → `backgroundLoadFailed=true`.
//
// The fix extracts a `RunStartupLoad` helper that runs LoadChunked first
// and only then spawns the background loader. This test calls the helper
// directly and asserts the post-load state.

import (
	"path/filepath"
	"testing"
	"time"
)

// Test1809_StartupLoad_BgLoaderSeesOldestLoaded confirms that after
// RunStartupLoad returns, oldestLoaded is set and backgroundLoadFailed
// is false. The pre-fix code (spawn bg loader at FirstChunkReady)
// produces backgroundLoadFailed=true deterministically because the bg
// loader reads oldestLoaded="" and bails.
func Test1809_StartupLoad_BgLoaderSeesOldestLoaded(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	nowSec := time.Now().UTC().Unix()
	// 100 rows, all within the 1h hot window so LoadChunked picks them up
	// and bg loader has only ancient (empty) territory to walk back to.
	createTestDBWithLastSeen(t, dbPath, 100, 1, nowSec,
		30*time.Minute, // first_seen
		30*time.Minute) // last_seen

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
		t.Fatalf("RunStartupLoad: %v", err)
	}

	if store.oldestLoaded == "" {
		t.Fatalf("oldestLoaded is empty after RunStartupLoad; bg loader would bail")
	}
	if store.backgroundLoadFailed.Load() {
		t.Fatalf("backgroundLoadFailed=true after RunStartupLoad; "+
			"bg loader fired before LoadChunked set oldestLoaded "+
			"(error=%q, loaded=%d, oldest=%q)",
			store.BackgroundLoadError(), len(store.packets), store.oldestLoaded)
	}
	if !store.backgroundLoadDone.Load() {
		t.Fatalf("backgroundLoadDone=false after RunStartupLoad; expected true on success")
	}
}
