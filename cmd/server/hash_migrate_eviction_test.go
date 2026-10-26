package main

import (
	"context"
	"database/sql"
	"runtime"
	"testing"
	"time"
)

func TestMigrateContentHashesAsyncSurvivesEviction(t *testing.T) {
	for _, tc := range []struct {
		name      string
		batchSize int
		replace   bool
	}{
		{name: "later_batch", batchSize: 1},
		{name: "pending_batch", batchSize: 4},
		{name: "replacement_with_same_ID", batchSize: 4, replace: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := createTestDBWithResolvedPath(t, 4, nil)
			// Reuse the valid raw packet from the existing migration regression.
			// The fixture's legacy hashes intentionally need migration.
			writer, err := sql.Open("sqlite3", dbPath)
			if err != nil {
				t.Fatal(err)
			}
			_, err = writer.Exec("UPDATE transmissions SET raw_hex = ?", "0A00D69FD7A5A7475DB07337749AE61FA53A4788E976")
			writer.Close()
			if err != nil {
				t.Fatal(err)
			}
			db, err := OpenDB(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			store := NewPacketStore(db, &PacketStoreConfig{RetentionHours: 24})
			now := time.Now().UTC()
			if err := store.loadChunk(now.Add(-72*time.Hour), now); err != nil {
				t.Fatal(err)
			}
			if len(store.packets) != 4 {
				t.Fatalf("fixture loaded %d packets, want 4", len(store.packets))
			}
			original := append([]*StoreTx(nil), store.packets...)

			// Occupy the native read-only SQL pool until the real migration has
			// captured its first batch and released the store's read lock.
			db.conn.SetMaxOpenConns(1)
			conn, err := db.conn.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			before := db.conn.Stats().WaitCount
			done := make(chan struct{})
			go func() {
				defer close(done)
				migrateContentHashesAsync(store, tc.batchSize, 0)
			}()
			finish := func() {
				t.Helper()
				conn.Close()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("migration did not return after releasing the SQL connection")
				}
			}
			defer finish()
			deadline := time.Now().Add(5 * time.Second)
			for db.conn.Stats().WaitCount == before && time.Now().Before(deadline) {
				runtime.Gosched()
			}
			if db.conn.Stats().WaitCount == before {
				t.Fatal("migration did not reach its first SQL batch")
			}
			if got := store.RunEviction(); got != 4 {
				t.Fatalf("evicted %d packets, want 4", got)
			}

			var replacements []*StoreTx
			if tc.replace {
				// A reloaded transmission has the same ID but a new pointer.
				// Refill to the original length so a length-only check is insufficient.
				store.mu.Lock()
				for _, tx := range original {
					replacement := &StoreTx{ID: tx.ID, Hash: tx.Hash, RawHex: tx.RawHex, FirstSeen: now.Format(time.RFC3339)}
					replacements = append(replacements, replacement)
					store.packets = append(store.packets, replacement)
					store.byTxID[replacement.ID] = replacement
					store.byHash[replacement.Hash] = replacement
				}
				store.mu.Unlock()
			}
			finish()

			if !store.mu.TryLock() {
				// The migration has returned and no other readers exist. Release
				// its leaked RLock so the assertion failure does not strand the test.
				store.mu.RUnlock()
				t.Error("migration returned while retaining the store read lock")
			} else {
				store.mu.Unlock()
			}
			if !store.mu.TryRLock() {
				t.Fatal("store read lock is unusable after migration")
			}
			defer store.mu.RUnlock()
			if got := len(store.packets); got != len(replacements) {
				t.Errorf("primary packet count = %d, want %d", got, len(replacements))
			}
			if got := len(store.byTxID); got != len(replacements) {
				t.Errorf("byTxID count = %d, want %d", got, len(replacements))
			}
			if got := len(store.byHash); got != len(replacements) {
				t.Errorf("byHash count = %d, want %d; pending migration republished evicted packets", got, len(replacements))
			}
			for _, tx := range original {
				for hash, cached := range store.byHash {
					if cached == tx {
						t.Errorf("byHash[%q] retained evicted transmission %d", hash, tx.ID)
					}
				}
			}
			for _, tx := range replacements {
				if store.byTxID[tx.ID] != tx || store.byHash[tx.Hash] != tx {
					t.Errorf("migration changed ownership of replacement transmission %d", tx.ID)
				}
			}
			if store.hashMigrationComplete.Load() {
				t.Error("migration must not claim completion after its input changed")
			}
			var unchanged int
			if err := db.conn.QueryRow("SELECT COUNT(*) FROM transmissions WHERE hash LIKE 'hash1558_%'").Scan(&unchanged); err != nil {
				t.Fatal(err)
			}
			if unchanged != 4 {
				t.Errorf("read-only fixture retained %d original hashes, want 4", unchanged)
			}
		})
	}
}
