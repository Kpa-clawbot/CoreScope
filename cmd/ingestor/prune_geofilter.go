// Package main: ingestor-side processor for the geo-prune requests written by
// the read-only server (see internal/prunequeue).
//
// The server cannot DELETE because it opens the database read-only
// (#1283/#1289). Instead it queues a request — request-<id>.json under
// <dataDir>/prune-requests/ on SQLite, a corescope_control.prune_requests row
// on Postgres — and the ingestor consumes it here.
package main

import (
	"errors"
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

// pruneResultsMaxAge is how long a completed prune result stays readable by
// the server's status endpoint before the ingestor drops it.
const pruneResultsMaxAge = 7 * 24 * time.Hour

// RunPendingPruneRequests processes the geo-prune requests the server queued
// (s.pruneQ: marker files next to the SQLite database, or the
// corescope_control tables on Postgres), oldest first. Each request is honored
// verbatim — the server is responsible for the TOCTOU snapshot (only pubkeys
// that were still outside the geofilter at confirm time). After the DELETE
// the ingestor completes the request: its result replaces it, in one atomic
// step. Results older than pruneResultsMaxAge are dropped on every pass.
//
// Safe to call from a ticker — no-op when the queue is empty.
func (s *Store) RunPendingPruneRequests() {
	if err := s.pruneQ.PruneResults(pruneResultsMaxAge); err != nil {
		log.Printf("[prune-queue] pruning old results in %s failed: %v", s.pruneQ.Location(), err)
	}
	ids, err := s.pruneQ.ListPending()
	if err != nil {
		log.Printf("[prune-queue] list pending in %s failed: %v", s.pruneQ.Location(), err)
		return
	}
	for _, id := range ids {
		req, err := s.pruneQ.ReadRequest(id)
		if errors.Is(err, os.ErrNotExist) {
			continue // gone since ListPending
		}
		if err == nil && req.ID != id {
			err = fmt.Errorf("request names id %q", req.ID)
		}
		if err != nil {
			s.discardUnreadablePruneRequest(id, err)
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
		if werr := s.pruneQ.Complete(res); werr != nil {
			log.Printf("[prune-queue] write result for %s failed: %v", req.ID, werr)
		}
	}
}

// discardUnreadablePruneRequest takes a request that cannot be honored (it
// does not parse, or names another id) out of the queue, so it is not retried
// on every pass.
//
// SQLite: the marker file is removed, as it always was.
//
// Postgres: the request is completed with an error result, the only way the
// ingestor removes one, which also tells the server's status poll why. A
// failed read there may instead be a database error, which must not cost a
// valid request, so the request is first probed: only when the probe sees it
// still pending is its content what is wrong. Otherwise it is left for the
// next pass.
func (s *Store) discardUnreadablePruneRequest(id string, readErr error) {
	if !s.pg {
		log.Printf("[prune-queue] read request %s failed: %v — removing", id, readErr)
		p, err := prunequeue.RequestPath(s.path, id)
		if err == nil {
			err = os.Remove(p)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("[prune-queue] removing request %s failed: %v", id, err)
		}
		return
	}
	pending, err := s.pruneQ.RequestExists(id)
	if err != nil {
		log.Printf("[prune-queue] read request %s failed: %v (will retry: %v)", id, readErr, err)
		return
	}
	if !pending {
		return // completed or gone since
	}
	log.Printf("[prune-queue] read request %s failed: %v — completing it with an error result", id, readErr)
	res := prunequeue.Result{
		ID:          id,
		CompletedAt: time.Now().UTC(),
		Error:       "unreadable prune request: " + readErr.Error(),
	}
	if err := s.pruneQ.Complete(res); err != nil {
		log.Printf("[prune-queue] write error result for %s failed: %v", id, err)
	}
}
