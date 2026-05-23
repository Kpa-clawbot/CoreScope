package main

import (
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
// this function is the ONLY path that mutates those columns at runtime
// (the schema itself is added by internal/dbschema). The server MUST
// NOT execute any UPDATE on nodes.multibyte_* — see
// cmd/server/readonly_invariant_test.go for the enforcement.
//
// Data-destruction guard: entries with Status=="unknown" (sup==0) are
// NEVER persisted — we never overwrite a previously confirmed/suspected
// DB value with a snapshot blank. Same guarantee the server-side helper
// originally enforced before relocation.
//
// STUB: real implementation lands in the following commit (TDD red→green).
func (s *Store) RunMultibyteCapPersist() (MultibyteCapPersistStats, error) {
	_ = mbcapqueue.SnapshotPath(s.path) // import retained for green commit
	return MultibyteCapPersistStats{}, nil
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
