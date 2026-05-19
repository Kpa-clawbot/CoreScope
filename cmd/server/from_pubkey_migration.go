// Package main: from_pubkey backfill shim (issue #1287).
//
// The actual backfill moved to cmd/ingestor (see
// cmd/ingestor/maintenance.go: BackfillFromPubkey) because the server
// is the read path and may not write to SQLite (#1283/#1287). This
// file retains the snapshot getter so /api/healthz still compiles —
// it always reports done=true with zero counters. Operators monitor
// the ingestor's stats file for true progress.
package main

import "sync"

var (
	fromPubkeyBackfillMu        sync.RWMutex
	fromPubkeyBackfillTotal     int64
	fromPubkeyBackfillProcessed int64
	fromPubkeyBackfillDone      = true
)

// fromPubkeyBackfillSnapshot returns the current backfill progress.
// In the post-#1287 world the server does not run the backfill, so
// the snapshot is always done=true with zeros. The real progress
// lives in the ingestor stats file.
func fromPubkeyBackfillSnapshot() (total, processed int64, done bool) {
	fromPubkeyBackfillMu.RLock()
	defer fromPubkeyBackfillMu.RUnlock()
	return fromPubkeyBackfillTotal, fromPubkeyBackfillProcessed, fromPubkeyBackfillDone
}

// fromPubkeyBackfillReset is a test helper used by legacy tests; in
// the post-#1287 world it merely resets the static snapshot fields.
func fromPubkeyBackfillReset() {
	fromPubkeyBackfillMu.Lock()
	fromPubkeyBackfillTotal = 0
	fromPubkeyBackfillProcessed = 0
	fromPubkeyBackfillDone = true
	fromPubkeyBackfillMu.Unlock()
}
