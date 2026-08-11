// Ingestor-side processor for infra-flag request marker files written
// by the read-only server (see internal/infraqueue) when an admin
// toggles a node's infrastructure status from the web panel.
//
// The server cannot UPDATE because it opens SQLite mode=ro (#1283/#1289).
// Instead, the server writes request-<id>.json under <dataDir>/infra-requests/
// and the ingestor consumes it here.
package main

import (
	"log"
	"os"
	"time"

	"github.com/meshcore-analyzer/infraqueue"
)

// SetInfrastructureFlag sets/clears the infrastructure flag on a node by
// exact public key, across both nodes and inactive_nodes (a node can be
// in either depending on retention state). Returns matched=false if
// neither table had a row for pubkey — the caller should treat that as
// an error to report back to the admin, not a silent success.
func (s *Store) SetInfrastructureFlag(pubkey string, on bool) (matched bool, err error) {
	val := 0
	if on {
		val = 1
	}
	res1, err := s.db.Exec(`UPDATE nodes SET infrastructure = ? WHERE public_key = ?`, val, pubkey)
	if err != nil {
		return false, err
	}
	res2, err := s.db.Exec(`UPDATE inactive_nodes SET infrastructure = ? WHERE public_key = ?`, val, pubkey)
	if err != nil {
		return false, err
	}
	n1, _ := res1.RowsAffected()
	n2, _ := res2.RowsAffected()
	return n1+n2 > 0, nil
}

// RunPendingInfraRequests scans the infra-requests/ directory next to
// the SQLite database and processes any request-<id>.json markers
// written by the server. After running the UPDATE, the ingestor writes
// result-<id>.json and removes the request file (atomic, via
// os.Rename in infraqueue.WriteResult).
//
// Safe to call from a ticker — no-op when the queue is empty.
func (s *Store) RunPendingInfraRequests() {
	paths, err := infraqueue.ListPending(s.path)
	if err != nil {
		log.Printf("[infra-queue] list pending failed: %v", err)
		return
	}
	if len(paths) == 0 {
		return
	}
	for _, p := range paths {
		req, err := infraqueue.ReadRequest(p)
		if err != nil {
			log.Printf("[infra-queue] read %s failed: %v — removing", p, err)
			_ = os.Remove(p)
			continue
		}
		actor := req.ActorUsername
		if actor == "" {
			actor = "unknown"
		}
		log.Printf("[infra-queue] processing request %s: pubkey=%s infrastructure=%v actor=%s",
			req.ID, req.PublicKey, req.Infrastructure, actor)
		start := time.Now()
		matched, serr := s.SetInfrastructureFlag(req.PublicKey, req.Infrastructure)
		res := infraqueue.Result{
			ID:          req.ID,
			RequestedAt: req.RequestedAt,
			CompletedAt: time.Now().UTC(),
			Applied:     matched,
		}
		switch {
		case serr != nil:
			res.Error = serr.Error()
			log.Printf("[infra-queue] request %s FAILED after %s: %v", req.ID, time.Since(start), serr)
		case !matched:
			res.Error = "no node found with that public key"
			log.Printf("[infra-queue] request %s: no node matched pubkey=%s", req.ID, req.PublicKey)
		default:
			log.Printf("[infra-queue] request %s applied in %s", req.ID, time.Since(start))
		}
		if werr := infraqueue.WriteResult(s.path, res); werr != nil {
			log.Printf("[infra-queue] write result for %s failed: %v", req.ID, werr)
		}
	}
}
