package main

import (
	"encoding/json"
	"errors"
	"log"
	"os"

	"github.com/meshcore-analyzer/dbschema"
)

// MultibyteCapPersistStats holds counts for /api/healthz exposure / logging.
type MultibyteCapPersistStats struct {
	ReadEntries     int   // entries read from snapshot
	UpdatedActive   int64 // rows updated in nodes
	UpdatedInactive int64 // rows updated in inactive_nodes
	Skipped         int   // entries skipped (status=="unknown")
	Ignored         int   // entries ignored: no such node, a repeated pubkey, or past the node count
}

// RunMultibyteCapPersist consumes the latest multi-byte capability snapshot
// written by the server (internal/mbcapqueue, through s.mbcap: a file next to
// the SQLite database, or corescope_control.mbcap_snapshots on Postgres) and
// persists it to nodes / inactive_nodes. Owned by the ingestor per #1287: the server is read-only
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
// The snapshot is untrusted input (the server is public-facing): only
// entries for existing nodes are applied, each pubkey once, and no more
// entries are looked at than there are nodes (nodes + inactive_nodes); the
// rest are counted in Ignored.
//
// Safe to call from a ticker; no-op when no snapshot has been written
// (cold start), when the snapshot is empty, or when running against a
// legacy DB that pre-dates the multibyte_sup migration (#1386). A malformed
// newest snapshot (#1386) is logged and discarded together with the older
// ones (see discardMalformedMultibyteSnapshots), so a bad row neither
// blocks the prune nor lets the table grow.
func (s *Store) RunMultibyteCapPersist() (MultibyteCapPersistStats, error) {
	var stats MultibyteCapPersistStats
	snap, err := s.mbcap.ReadSnapshot()
	if err != nil {
		// os.ErrNotExist is the steady state until the server's first
		// analytics cycle completes — silent no-op. A malformed snapshot
		// is operator-actionable: log it and discard it (no error
		// surfaced to the ticker — a corrupt snapshot must not stop the
		// maintenance loop). Any other error is the store failing to
		// read (a database error on Postgres): retried next tick.
		if errors.Is(err, os.ErrNotExist) {
			return stats, nil
		}
		var jsonErr *json.SyntaxError
		if errors.As(err, &jsonErr) || isMalformedSnapshotErr(err) {
			log.Printf("[multibyte-persist] malformed snapshot in %s (discarded): %v", s.mbcap.Location(), err)
			s.discardMalformedMultibyteSnapshots()
			return stats, nil
		}
		log.Printf("[multibyte-persist] read snapshot: %v (no-op)", err)
		return stats, nil
	}
	stats.ReadEntries = len(snap.Entries)
	if len(snap.Entries) == 0 {
		s.pruneMultibyteSnapshots()
		return stats, nil
	}

	// Defensive schema check: a legacy DB that pre-dates the
	// multibyte_sup migration would fail at tx.Prepare with a SQL
	// error. Detect early and skip cleanly so the ticker keeps
	// running on heterogeneous deployments.
	if !s.hasMultibyteSupColumns() {
		log.Printf("[multibyte-persist] schema missing: nodes.multibyte_sup not present on this DB (legacy schema) — skipping %d entries", stats.ReadEntries)
		return stats, nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return stats, err
	}
	defer tx.Rollback() //nolint:errcheck
	// The bound on entries: an honest snapshot has at most one per node.
	var nodeCount int
	if err := tx.QueryRow(`SELECT (SELECT COUNT(*) FROM nodes) + (SELECT COUNT(*) FROM inactive_nodes)`).Scan(&nodeCount); err != nil {
		return stats, err
	}
	// Combined dispatch: each pubkey lives in exactly one of nodes /
	// inactive_nodes. The pre-#1386 implementation issued one UPDATE
	// against each table per entry — 50% guaranteed-empty. We now
	// look up the table once, then issue the matching UPDATE.
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
	// Membership probe: one indexed PK lookup. Cheap; avoids the
	// guaranteed-miss second UPDATE.
	stmtProbe, err := tx.Prepare(`SELECT 1 FROM nodes WHERE public_key=? LIMIT 1`)
	if err != nil {
		return stats, err
	}
	defer stmtProbe.Close()

	entries := snap.Entries
	if len(entries) > nodeCount {
		stats.Ignored += len(entries) - nodeCount
		entries = entries[:nodeCount]
	}
	seen := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		if _, dup := seen[e.PublicKey]; dup {
			stats.Ignored++
			continue
		}
		seen[e.PublicKey] = struct{}{}
		sup := multibyteStatusToInt(e.Status)
		if sup == 0 {
			stats.Skipped++
			continue
		}
		// Probe once. If hit, UPDATE nodes; else UPDATE inactive_nodes,
		// where no row matched means no such node.
		var hit int
		if err := stmtProbe.QueryRow(e.PublicKey).Scan(&hit); err == nil {
			if r, err := stmtN.Exec(sup, e.Evidence, e.PublicKey); err == nil {
				if n, _ := r.RowsAffected(); n > 0 {
					stats.UpdatedActive += n
				}
			}
		} else {
			r, err := stmtI.Exec(sup, e.Evidence, e.PublicKey)
			if err != nil {
				continue
			}
			if n, _ := r.RowsAffected(); n > 0 {
				stats.UpdatedInactive += n
			} else {
				stats.Ignored++
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return stats, err
	}
	s.pruneMultibyteSnapshots()
	if stats.UpdatedActive+stats.UpdatedInactive > 0 || stats.Ignored > 0 {
		log.Printf("[multibyte-persist] applied snapshot: %d entries (%d skipped, %d ignored); updated %d active + %d inactive nodes",
			stats.ReadEntries, stats.Skipped, stats.Ignored, stats.UpdatedActive, stats.UpdatedInactive)
	}
	return stats, nil
}

// multibyteSnapshotsKept is how many of the newest snapshots survive a prune.
// Only the newest is ever read; the rest are kept for inspection.
const multibyteSnapshotsKept = 3

// pruneMultibyteSnapshots drops all but the newest multibyteSnapshotsKept
// snapshots once the newest has been applied (a no-op for the single file on
// SQLite). A failure is logged and retried after the next persist; it never
// fails the persist that already committed.
func (s *Store) pruneMultibyteSnapshots() {
	if err := s.mbcap.Prune(multibyteSnapshotsKept); err != nil {
		log.Printf("[multibyte-persist] pruning old snapshots in %s: %v", s.mbcap.Location(), err)
	}
}

// discardMalformedMultibyteSnapshots drops every snapshot after the newest
// one failed to parse: Prune(0) is the only removal the mbcapqueue.Store API
// offers, and keeping the multibyteSnapshotsKept newest would keep the bad
// one, which would then be read (and the table never pruned) on every tick.
// The older snapshots are never read anyway. A snapshot the server inserts
// between the read and this prune is lost with them; its next analytics
// cycle writes another. On SQLite this is a no-op: the single file is
// replaced by the next write and cannot accumulate.
func (s *Store) discardMalformedMultibyteSnapshots() {
	if err := s.mbcap.Prune(0); err != nil {
		log.Printf("[multibyte-persist] discarding malformed snapshot in %s: %v", s.mbcap.Location(), err)
	}
}

// isMalformedSnapshotErr returns true if err looks like a JSON parse /
// IO-truncation failure, or a document over the size cap ("exceeds"),
// surfaced by mbcapqueue.ReadSnapshot. The
// queue wraps errors with %w but mbcapqueue currently formats with
// %w only for "read:"/"unmarshal:" prefixes — we substring-match
// those so the operator-actionable log message is unambiguous.
func isMalformedSnapshotErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, frag := range []string{"unmarshal", "invalid character", "unexpected end of JSON", "exceeds"} {
		if containsCI(msg, frag) {
			return true
		}
	}
	return false
}

func containsCI(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	// case-insensitive Contains without importing strings (already
	// imported in db.go, but keeping helper local to avoid widening
	// this file's imports).
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			a, b := s[i+j], sub[j]
			if a >= 'A' && a <= 'Z' {
				a += 32
			}
			if b >= 'A' && b <= 'Z' {
				b += 32
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// hasMultibyteSupColumns probes whether the active DB carries the
// multibyte_sup column on the `nodes` table. Used to short-circuit
// RunMultibyteCapPersist on legacy DBs that pre-date the
// internal/dbschema migration (#1386).
func (s *Store) hasMultibyteSupColumns() bool {
	has, err := dbschema.TableHasColumn(s.db, "nodes", "multibyte_sup")
	return err == nil && has
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
