package main

import (
	"log"
	"time"
)

// verifyContentHashesAsync reports whether the loaded hashes are current. The
// server never rewrites telemetry or its hash index independently of storage.
// Legacy hash repair is a separate offline operation; import preserves hashes.
func verifyContentHashesAsync(store *PacketStore, batchSize int, yieldDuration time.Duration) {
	if batchSize <= 0 {
		batchSize = 5000
	}
	store.mu.RLock()
	total := len(store.packets)
	store.mu.RUnlock()
	for offset := 0; offset < total; offset += batchSize {
		store.mu.RLock()
		end := min(offset+batchSize, min(total, len(store.packets)))
		stale := false
		for _, tx := range store.packets[min(offset, end):end] {
			if tx.RawHex != "" && ComputeContentHash(tx.RawHex) != tx.Hash {
				stale = true
				break
			}
		}
		store.mu.RUnlock()
		if stale {
			log.Print("[hash-verify] stale content hashes found; perform explicit offline hash repair on a working copy of the legacy snapshot before import. The importer preserves existing hashes")
			return
		}
		if yieldDuration > 0 {
			time.Sleep(yieldDuration)
		}
	}
	store.hashMigrationComplete.Store(true)
}
