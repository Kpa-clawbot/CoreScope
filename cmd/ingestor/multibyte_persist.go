package main

import (
	"log"

	"github.com/meshcore-analyzer/mbcapqueue"
)

// MultibyteCapPersistStats holds counts for /api/healthz exposure / logging.
type MultibyteCapPersistStats struct {
	ReadEntries     int   // entries read from snapshot
	UpdatedActive   int64 // rows updated in nodes
	UpdatedInactive int64 // rows updated in inactive_nodes
	Skipped         int   // entries skipped (status=="unknown")
}

// RunMultibyteCapPersist consumes the latest multi-byte capability snapshot
// written by the server (internal/mbcapqueue) and persists it to nodes /
// inactive_nodes. Owned by the ingestor per #1287: the server is read-only
// since #1289 and cannot UPDATE these columns itself.
//
// INVARIANT (canonical owner): multibyte_sup / multibyte_evidence are
// derived/cached columns. The server COMPUTES the value during its
// analytics cycle (from observed packets) and writes a snapshot file;
// this function is the ONLY runtime path that mutates those columns
// (the schema itself is added by internal/dbschema). The server MUST
// NOT execute any UPDATE on nodes.multibyte_* — see
// cmd/server/readonly_invariant_test.go for the enforcement.
//
// Data-destruction guard: entries with Status=="unknown" (sup==0) are
// NEVER persisted — we never overwrite a previously confirmed/suspected
// DB value with a snapshot blank. Same guarantee the original
// server-side helper enforced before relocation.
//
// Safe to call from a ticker; no-op when no snapshot has been written
// (cold start) or when the snapshot is empty.
func (s *Store) RunMultibyteCapPersist() (MultibyteCapPersistStats, error) {
	var stats MultibyteCapPersistStats
	snap, err := mbcapqueue.ReadSnapshot(s.path)
	if err != nil {
		// Missing snapshot is the steady state until the server's first
		// analytics cycle completes — treat as no-op.
		return stats, nil
	}
	stats.ReadEntries = len(snap.Entries)
	if len(snap.Entries) == 0 {
		return stats, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return stats, err
	}
	defer tx.Rollback() //nolint:errcheck
	stmtN, err := tx.Prepare(`UPDATE nodes SET multibyte_sup=?, multibyte_evidence=? WHERE public_key=?`)
	if err != nil {
		return stats, err
	}
	defer stmtN.Close()
	stmtI, err := tx.Prepare(`UPDATE inactive_nodes SET multibyte_sup=?, multibyte_evidence=? WHERE public_key=?`)
	if err != nil {
		return stats, err
	}
	defer stmtI.Close()
	for _, e := range snap.Entries {
		sup := multibyteStatusToInt(e.Status)
		if sup == 0 {
			stats.Skipped++
			continue
		}
		if r, err := stmtN.Exec(sup, e.Evidence, e.PublicKey); err == nil {
			if n, _ := r.RowsAffected(); n > 0 {
				stats.UpdatedActive += n
			}
		}
		if r, err := stmtI.Exec(sup, e.Evidence, e.PublicKey); err == nil {
			if n, _ := r.RowsAffected(); n > 0 {
				stats.UpdatedInactive += n
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return stats, err
	}
	if stats.UpdatedActive+stats.UpdatedInactive > 0 {
		log.Printf("[multibyte-persist] applied snapshot: %d entries (%d skipped); updated %d active + %d inactive nodes",
			stats.ReadEntries, stats.Skipped, stats.UpdatedActive, stats.UpdatedInactive)
	}
	return stats, nil
}

// multibyteStatusToInt mirrors the mapping the server used before relocation.
// 0 = unknown (never persisted), 1 = suspected, 2 = confirmed.
func multibyteStatusToInt(status string) int {
	switch status {
	case "confirmed":
		return 2
	case "suspected":
		return 1
	default:
		return 0
	}
}
