package main

import (
	"log"
	"sync/atomic"
	"time"
)

// migrateContentHashesAsync recomputes content hashes in batches after the
// server is already serving HTTP.  Packets whose hash changes are updated in
// both the DB and the in-memory byHash index.  The migration is idempotent:
// once all hashes match the current formula it completes instantly.
func migrateContentHashesAsync(store *PacketStore, batchSize int, yieldDuration time.Duration) {
	// Eviction compacts the slice, and background loads may prepend packets.
	// Keep one boundary pointer, not a snapshot that pins all data.
	store.mu.RLock()
	total := len(store.packets)
	evicted := atomic.LoadInt64(&store.evicted)
	var last *StoreTx
	if total > 0 {
		last = store.packets[total-1]
	}
	store.mu.RUnlock()
	// Caller holds store.mu. Appends leave the original range intact; merges
	// before its end move the boundary and invalidate offset-based traversal.
	sameRange := func() bool {
		return len(store.packets) >= total && atomic.LoadInt64(&store.evicted) == evicted &&
			(total == 0 || store.packets[total-1] == last)
	}
	inputChanged := false

	// #1856: every DB failure below continues to the next batch, so the loop
	// always reaches the deferred completion. Setting the flag unconditionally
	// therefore reported success after migrating nothing, which is exactly what
	// happens on the read-only handle the server holds since #1283: begin,
	// prepare and commit all fail, every batch is skipped, and /api/stats then
	// answers hashMigrationComplete: true.
	failedBatches := 0
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[hash-migrate] panic recovered: %v", r)
			failedBatches++
		}
		if failedBatches > 0 {
			log.Printf("[hash-migrate] INCOMPLETE: %d batch(es) could not be written; "+
				"hashMigrationComplete stays false. On a read-only DB handle this is expected "+
				"and the migration belongs in the ingestor (#1856).", failedBatches)
			return
		}
		store.mu.RLock()
		defer store.mu.RUnlock()
		if inputChanged || !sameRange() {
			log.Printf("[hash-migrate] INCOMPLETE: packet set changed during migration")
			return
		}
		store.hashMigrationComplete.Store(true)
	}()

	migrated := 0
	for offset := 0; offset < total; offset += batchSize {
		// Collect stale hashes in this batch under RLock.
		type hashUpdate struct {
			tx      *StoreTx
			oldHash string
			newHash string
		}
		var updates []hashUpdate

		func() {
			store.mu.RLock()
			defer store.mu.RUnlock()
			if !sameRange() {
				inputChanged = true
				return
			}
			end := min(offset+batchSize, min(total, len(store.packets)))
			for _, tx := range store.packets[offset:end] {
				if tx.RawHex == "" {
					continue
				}
				newHash := ComputeContentHash(tx.RawHex)
				if newHash != tx.Hash {
					updates = append(updates, hashUpdate{tx: tx, oldHash: tx.Hash, newHash: newHash})
				}
			}
		}()
		if inputChanged {
			return
		}

		if len(updates) == 0 {
			continue
		}

		// Write batch to DB in a single transaction.
		dbTx, err := store.db.conn.Begin()
		if err != nil {
			log.Printf("[hash-migrate] begin tx: %v", err)
			failedBatches++
			continue
		}
		stmt, err := dbTx.Prepare("UPDATE transmissions SET hash = ? WHERE id = ?")
		if err != nil {
			log.Printf("[hash-migrate] prepare: %v", err)
			dbTx.Rollback()
			failedBatches++
			continue
		}

		for _, u := range updates {
			if _, err := stmt.Exec(u.newHash, u.tx.ID); err != nil {
				// UNIQUE constraint = two old hashes map to the same new hash (duplicate).
				// Merge observations to the surviving tx, delete the duplicate.
				log.Printf("[hash-migrate] tx %d collides — merging duplicate", u.tx.ID)
				var survID int
				if err2 := dbTx.QueryRow("SELECT id FROM transmissions WHERE hash = ?", u.newHash).Scan(&survID); err2 == nil {
					dbTx.Exec("UPDATE observations SET transmission_id = ? WHERE transmission_id = ?", survID, u.tx.ID)
					dbTx.Exec("DELETE FROM transmissions WHERE id = ?", u.tx.ID)
					u.newHash = "" // mark for in-memory removal only
				}
			}
		}
		stmt.Close()

		if err := dbTx.Commit(); err != nil {
			log.Printf("[hash-migrate] commit: %v", err)
			failedBatches++
			continue
		}

		// Update in-memory index under write lock.
		func() {
			store.mu.Lock()
			defer store.mu.Unlock()
			for _, u := range updates {
				// SQL can wait while eviction removes or replaces this object.
				if store.byTxID[u.tx.ID] != u.tx {
					inputChanged = true
					continue
				}
				delete(store.byHash, u.oldHash)
				if u.newHash == "" {
					// Merged duplicate — remove from packets slice and indexes.
					delete(store.byTxID, u.tx.ID)
					// Move observations to survivor if present.
					if surv := store.byHash[ComputeContentHash(u.tx.RawHex)]; surv != nil {
						for _, obs := range u.tx.Observations {
							surv.Observations = append(surv.Observations, obs)
							surv.ObservationCount++
						}
					}
				} else {
					u.tx.Hash = u.newHash
					store.byHash[u.newHash] = u.tx
				}
				migrated++
			}
		}()

		// Yield to let HTTP handlers run.
		time.Sleep(yieldDuration)
	}

	if migrated > 0 {
		log.Printf("[hash-migrate] Migrated %d content hashes to new formula", migrated)
	}
}
