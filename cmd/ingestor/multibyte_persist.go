package main

import (
	"encoding/json"
	"errors"
	"log"
	"os"

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
// (cold start), when the snapshot is empty, when the snapshot is
// malformed (#1386), or when running against a legacy DB that
// pre-dates the multibyte_sup migration (#1386).
func (s *Store) RunMultibyteCapPersist() (MultibyteCapPersistStats, error) {
	var stats MultibyteCapPersistStats
	snap, err := mbcapqueue.ReadSnapshot(s.path)
	if err != nil {
		// os.ErrNotExist is the steady state until the server's first
		// analytics cycle completes — silent no-op. A malformed file
		// is operator-actionable: log it (but still no-op, no error
		// surfaced to the ticker — a corrupt snapshot must not stop
		// the maintenance loop).
		if errors.Is(err, os.ErrNotExist) {
			return stats, nil
		}
		// All other ReadSnapshot errors today are wrap-arounds of
		// io / unmarshal failures — both classify as "malformed
		// snapshot on disk" from this loop's perspective.
		var jsonErr *json.SyntaxError
		if errors.As(err, &jsonErr) || isMalformedSnapshotErr(err) {
			log.Printf("[multibyte-persist] malformed snapshot on disk (no-op): %v", err)
			return stats, nil
		}
		log.Printf("[multibyte-persist] read snapshot: %v (no-op)", err)
		return stats, nil
	}
	stats.ReadEntries = len(snap.Entries)
	if len(snap.Entries) == 0 {
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

	for _, e := range snap.Entries {
		sup := multibyteStatusToInt(e.Status)
		if sup == 0 {
			stats.Skipped++
			continue
		}
		// Probe once. If hit, UPDATE nodes; else UPDATE inactive_nodes.
		var hit int
		if err := stmtProbe.QueryRow(e.PublicKey).Scan(&hit); err == nil {
			if r, err := stmtN.Exec(sup, e.Evidence, e.PublicKey); err == nil {
				if n, _ := r.RowsAffected(); n > 0 {
					stats.UpdatedActive += n
				}
			}
		} else {
			if r, err := stmtI.Exec(sup, e.Evidence, e.PublicKey); err == nil {
				if n, _ := r.RowsAffected(); n > 0 {
					stats.UpdatedInactive += n
				}
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

// isMalformedSnapshotErr returns true if err looks like a JSON parse /
// IO-truncation failure surfaced by mbcapqueue.ReadSnapshot. The
// queue wraps errors with %w but mbcapqueue currently formats with
// %w only for "read:"/"unmarshal:" prefixes — we substring-match
// those so the operator-actionable log message is unambiguous.
func isMalformedSnapshotErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, frag := range []string{"unmarshal", "invalid character", "unexpected end of JSON"} {
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
	rows, err := s.db.Query(`PRAGMA table_info(nodes)`)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false
		}
		if name == "multibyte_sup" {
			return true
		}
	}
	return false
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
