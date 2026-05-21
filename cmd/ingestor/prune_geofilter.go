// Package main: ingestor-side processor for prune-request marker files
// written by the read-only server (see internal/prunequeue).
//
// The server cannot DELETE because it opens SQLite mode=ro (#1283/#1289).
// Instead, the server writes request-<id>.json under <dataDir>/prune-requests/
// and the ingestor consumes it here.
package main

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/meshcore-analyzer/prunequeue"
)

// DeleteNodesByPubkeys deletes nodes by public key. Returns the count deleted.
// Only the ingestor calls this (server has no write handle).
func (s *Store) DeleteNodesByPubkeys(pubkeys []string) (int64, error) {
	if len(pubkeys) == 0 {
		return 0, nil
	}
	// Chunk to keep statements under SQLite's variable limit (default 999).
	const chunk = 500
	var total int64
	for start := 0; start < len(pubkeys); start += chunk {
		end := start + chunk
		if end > len(pubkeys) {
			end = len(pubkeys)
		}
		batch := pubkeys[start:end]
		placeholders := strings.Repeat("?,", len(batch))
		placeholders = placeholders[:len(placeholders)-1]
		args := make([]interface{}, len(batch))
		for i, pk := range batch {
			args[i] = pk
		}
		// Cascade cleanup: a node row carries the canonical identity, but
		// observations/transmissions reference the pubkey too via observer
		// metadata and originator fields. There are no FK constraints in
		// the current schema (#669 review note), so we explicitly clear
		// the most obvious follow-on rows that would otherwise become
		// orphans visible to operators.
		//
		// Conservative scope: only the `nodes` row is removed here. The
		// referenced observation/transmission history is retained for
		// audit; operators can run the regular packet-retention prune to
		// age it out. If a future schema introduces FKs, revisit.
		res, err := s.db.Exec("DELETE FROM nodes WHERE public_key IN ("+placeholders+")", args...)
		if err != nil {
			return total, fmt.Errorf("delete batch [%d:%d]: %w", start, end, err)
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

// RunPendingPruneRequests scans the prune-requests/ directory next to the
// SQLite database and processes any request-<id>.json markers written by
// the server. Each request is honored verbatim — the server is responsible
// for the TOCTOU snapshot (only pubkeys that were still outside the
// geofilter at confirm time). After running DELETE, the ingestor writes
// result-<id>.json and removes the request file (atomic, via os.Rename in
// prunequeue.WriteResult).
//
// Safe to call from a ticker — no-op when the queue is empty.
func (s *Store) RunPendingPruneRequests() {
	paths, err := prunequeue.ListPending(s.path)
	if err != nil {
		log.Printf("[prune-queue] list pending failed: %v", err)
		return
	}
	if len(paths) == 0 {
		return
	}
	for _, p := range paths {
		req, err := prunequeue.ReadRequest(p)
		if err != nil {
			log.Printf("[prune-queue] read %s failed: %v — removing", p, err)
			_ = os.Remove(p)
			continue
		}
		log.Printf("[prune-queue] processing request %s: %d pubkey(s) (%s)",
			req.ID, len(req.Pubkeys), req.Reason)
		start := time.Now()
		deleted, derr := s.DeleteNodesByPubkeys(req.Pubkeys)
		res := prunequeue.Result{
			ID:          req.ID,
			RequestedAt: req.RequestedAt,
			CompletedAt: time.Now().UTC(),
			Deleted:     deleted,
		}
		if derr != nil {
			res.Error = derr.Error()
			log.Printf("[prune-queue] request %s FAILED after %s: %v", req.ID, time.Since(start), derr)
		} else {
			log.Printf("[prune-queue] request %s deleted %d node(s) in %s", req.ID, deleted, time.Since(start))
		}
		if werr := prunequeue.WriteResult(s.path, res); werr != nil {
			log.Printf("[prune-queue] write result for %s failed: %v", req.ID, werr)
		}
	}
}
