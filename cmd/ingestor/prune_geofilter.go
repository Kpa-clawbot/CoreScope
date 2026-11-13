// Package main: ingestor-side processor for the geo-prune requests written by
// the read-only server (see internal/prunequeue).
//
// The server cannot DELETE because it opens the database read-only
// (#1283/#1289). Instead it queues a request — request-<id>.json under
// <dataDir>/prune-requests/ on SQLite, a corescope_control.prune_requests row
// on Postgres — and the ingestor consumes it here.
package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/meshcore-analyzer/prunequeue"
)

// sqlExecer is what the node DELETE needs: *sql.DB or *sql.Tx.
type sqlExecer interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
}

// pruneChunk keeps statements under SQLite's variable limit (default 999).
const pruneChunk = 500

// DeleteNodesByPubkeys deletes nodes by public key. Returns the count deleted.
// Only the ingestor calls this (server has no write handle).
func (s *Store) DeleteNodesByPubkeys(pubkeys []string) (int64, error) {
	return deleteNodesByPubkeys(s.db, pubkeys)
}

func deleteNodesByPubkeys(db sqlExecer, pubkeys []string) (int64, error) {
	var total int64
	for start := 0; start < len(pubkeys); start += pruneChunk {
		end := min(start+pruneChunk, len(pubkeys))
		batch := pubkeys[start:end]
		// Conservative scope: only the `nodes` row is removed here. There
		// are no FK constraints in the schema (#669 review note); the
		// referenced observation/transmission history is retained for
		// audit, and the regular packet-retention prune ages it out. If a
		// future schema introduces FKs, revisit.
		res, err := db.Exec("DELETE FROM nodes WHERE public_key IN ("+placeholders(len(batch))+")", stringArgs(batch)...)
		if err != nil {
			return total, fmt.Errorf("delete batch [%d:%d]: %w", start, end, err)
		}
		n, _ := res.RowsAffected()
		total += n
	}
	return total, nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func stringArgs(ss []string) []interface{} {
	args := make([]interface{}, len(ss))
	for i, v := range ss {
		args[i] = v
	}
	return args
}

// Why a requested pubkey was not deleted. The server is public-facing, so a
// request is only a list of candidates: the ingestor deletes a node only when
// its OWN geo_filter puts it outside, with the server's preview semantics
// (cmd/server handlePruneGeoFilter): a polygon of at least three points is
// required, a node without a position is always kept, and so is (0,0), which
// the shared geometry treats as "no fix".
const (
	pruneSkipNoFilter   = "no geo_filter polygon configured on the ingestor"
	pruneSkipNotFound   = "not a node"
	pruneSkipNoPosition = "no position"
	pruneSkipInside     = "inside the ingestor's geo_filter"
)

// pruneSkipOrder is the order skip reasons are reported in.
var pruneSkipOrder = []string{pruneSkipNoFilter, pruneSkipNotFound, pruneSkipNoPosition, pruneSkipInside}

// geoPruneOutcome is what one prune request did.
type geoPruneOutcome struct {
	Deleted int64
	Skipped map[string]int64 // reason -> count of distinct pubkeys
}

// skippedTotal is how many distinct requested pubkeys were not deleted.
func (o geoPruneOutcome) skippedTotal() int64 {
	var n int64
	for _, c := range o.Skipped {
		n += c
	}
	return n
}

// skipSummary renders the skips as "3 skipped (1 not a node, 2 no position)",
// or "" when nothing was skipped.
func (o geoPruneOutcome) skipSummary() string {
	total := o.skippedTotal()
	if total == 0 {
		return ""
	}
	var parts []string
	for _, r := range pruneSkipOrder {
		if c := o.Skipped[r]; c > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", c, r))
		}
	}
	return fmt.Sprintf("%d skipped (%s)", total, strings.Join(parts, ", "))
}

// hasGeoPrunePolygon reports whether gf defines the polygon a geo-prune needs
// (the server refuses to preview one without it).
func hasGeoPrunePolygon(gf *GeoFilterConfig) bool {
	return gf != nil && len(gf.Polygon) >= 3
}

// pruneNodesOutsideGeoFilter deletes those of pubkeys that are nodes the
// ingestor's own geo_filter gf puts outside, and accounts for every other
// distinct pubkey in the outcome's Skipped. The positions are read and the
// rows deleted in one writer transaction on the store's single connection,
// so no ingest write can move a node between the check and the DELETE.
func (s *Store) pruneNodesOutsideGeoFilter(pubkeys []string, gf *GeoFilterConfig) (geoPruneOutcome, error) {
	out := geoPruneOutcome{Skipped: map[string]int64{}}
	seen := make(map[string]struct{}, len(pubkeys))
	uniq := make([]string, 0, len(pubkeys))
	for _, pk := range pubkeys {
		if _, dup := seen[pk]; !dup {
			seen[pk] = struct{}{}
			uniq = append(uniq, pk)
		}
	}
	if len(uniq) == 0 {
		return out, nil
	}
	if !hasGeoPrunePolygon(gf) {
		out.Skipped[pruneSkipNoFilter] = int64(len(uniq))
		return out, nil
	}
	err := s.WriterTx("prune_geo", func(tx *sql.Tx) error {
		var outside []string
		found := 0
		for start := 0; start < len(uniq); start += pruneChunk {
			batch := uniq[start:min(start+pruneChunk, len(uniq))]
			rows, err := tx.Query("SELECT public_key, lat, lon FROM nodes WHERE public_key IN ("+placeholders(len(batch))+")", stringArgs(batch)...)
			if err != nil {
				return fmt.Errorf("read node positions: %w", err)
			}
			for rows.Next() {
				var pk string
				var lat, lon sql.NullFloat64
				if err := rows.Scan(&pk, &lat, &lon); err != nil {
					rows.Close()
					return fmt.Errorf("read node positions: %w", err)
				}
				found++
				switch {
				case !lat.Valid || !lon.Valid:
					out.Skipped[pruneSkipNoPosition]++
				case NodePassesGeoFilter(&lat.Float64, &lon.Float64, gf):
					out.Skipped[pruneSkipInside]++
				default:
					outside = append(outside, pk)
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return fmt.Errorf("read node positions: %w", err)
			}
		}
		if missing := len(uniq) - found; missing > 0 {
			out.Skipped[pruneSkipNotFound] = int64(missing)
		}
		n, err := deleteNodesByPubkeys(tx, outside)
		out.Deleted = n
		return err
	})
	if err != nil {
		return geoPruneOutcome{Skipped: map[string]int64{}}, err
	}
	return out, nil
}

// pruneResultsMaxAge is how long a completed prune result stays readable by
// the server's status endpoint before the ingestor drops it.
const pruneResultsMaxAge = 7 * 24 * time.Hour

// pruneTooManyPending is the error result of a request rejected because more
// than prunequeue.MaxPendingList were pending.
const pruneTooManyPending = "too many pending prune requests"

// pruneOverflowPerPass bounds how many excess requests one pass rejects, so a
// flood of rows cannot hold the prune pass (and the memory of the ids it has
// seen) unbounded; the rest are rejected on the following passes.
const pruneOverflowPerPass = 10 * prunequeue.MaxPendingList

// RunPendingPruneRequests processes the geo-prune requests the server queued
// (s.pruneQ: marker files next to the SQLite database, or the
// corescope_control tables on Postgres), oldest first. A request is untrusted
// input: its pubkeys are only candidates, and of those the ingestor deletes
// just the nodes its own geo_filter gf puts outside
// (pruneNodesOutsideGeoFilter); the rest are counted as skipped, by reason.
// After the DELETE the ingestor completes the request: its result replaces
// it, in one atomic step. At most prunequeue.MaxPendingList requests are
// honored per pass; when more are pending, the excess beyond the oldest ones
// is completed with a "too many pending prune requests" error. Results older
// than pruneResultsMaxAge are dropped on every pass.
//
// Safe to call from a ticker — no-op when the queue is empty.
func (s *Store) RunPendingPruneRequests(gf *GeoFilterConfig) {
	if err := s.pruneQ.PruneResults(pruneResultsMaxAge); err != nil {
		log.Printf("[prune-queue] pruning old results in %s failed: %v", s.pruneQ.Location(), err)
	}
	total, err := s.pruneQ.PendingCount()
	if err != nil {
		log.Printf("[prune-queue] count pending in %s failed: %v", s.pruneQ.Location(), err)
		return
	}
	if total == 0 {
		return
	}
	ids, err := s.pruneQ.ListPending()
	if err != nil {
		log.Printf("[prune-queue] list pending in %s failed: %v", s.pruneQ.Location(), err)
		return
	}
	if len(ids) > prunequeue.MaxPendingList {
		ids = ids[:prunequeue.MaxPendingList]
	}
	for _, id := range ids {
		s.runPruneRequest(id, gf)
	}
	if total > prunequeue.MaxPendingList {
		s.rejectExcessPruneRequests(total-prunequeue.MaxPendingList, ids)
	}
}

// runPruneRequest honors the pending request id as RunPendingPruneRequests
// describes.
func (s *Store) runPruneRequest(id string, gf *GeoFilterConfig) {
	req, err := s.pruneQ.ReadRequest(id)
	if errors.Is(err, os.ErrNotExist) {
		return // gone since ListPending
	}
	if err == nil && req.ID != id {
		err = fmt.Errorf("request names id %q", req.ID)
	}
	if err != nil {
		s.discardUnreadablePruneRequest(id, err)
		return
	}
	log.Printf("[prune-queue] processing request %s: %d pubkey(s) (%s)",
		req.ID, len(req.Pubkeys), pruneReasonForLog(req.Reason))
	start := time.Now()
	out, derr := s.pruneNodesOutsideGeoFilter(req.Pubkeys, gf)
	res := prunequeue.Result{
		ID:          req.ID,
		RequestedAt: req.RequestedAt,
		CompletedAt: time.Now().UTC(),
		Deleted:     out.Deleted,
	}
	if n := out.skippedTotal(); n > 0 {
		res.Skipped = n
		res.SkipReasons = out.Skipped
	}
	if derr != nil {
		res.Error = derr.Error()
		log.Printf("[prune-queue] request %s FAILED after %s: %v", req.ID, time.Since(start), derr)
	} else {
		msg := fmt.Sprintf("[prune-queue] request %s deleted %d node(s) in %s", req.ID, out.Deleted, time.Since(start))
		if sum := out.skipSummary(); sum != "" {
			msg += "; " + sum
		}
		log.Print(msg)
	}
	if werr := s.pruneQ.Complete(res); werr != nil {
		log.Printf("[prune-queue] write result for %s failed: %v", req.ID, werr)
	}
}

// pruneReasonMaxLog bounds how much of a request's free-text reason, which
// the server writes, reaches the log.
const pruneReasonMaxLog = 80

// pruneReasonForLog is reason without control characters and cut to
// pruneReasonMaxLog runes.
func pruneReasonForLog(reason string) string {
	r := []rune(sanitizeLogString(reason))
	if len(r) > pruneReasonMaxLog {
		return string(r[:pruneReasonMaxLog]) + "..."
	}
	return string(r)
}

// rejectExcessPruneRequests completes up to excess pending requests (at most
// pruneOverflowPerPass), oldest first after the batch this pass honored, with
// a "too many pending prune requests" error result and deletes nothing for
// them. honored are the ids this pass processed: one whose completion failed
// is still pending and must not be rejected for it.
func (s *Store) rejectExcessPruneRequests(excess int, honored []string) {
	excess = min(excess, pruneOverflowPerPass)
	skip := make(map[string]struct{}, len(honored)+excess)
	for _, id := range honored {
		skip[id] = struct{}{}
	}
	rejected := 0
	for rejected < excess {
		ids, err := s.pruneQ.ListPending()
		if err != nil {
			log.Printf("[prune-queue] list pending in %s failed: %v", s.pruneQ.Location(), err)
			break
		}
		progress := false
		for _, id := range ids {
			if rejected >= excess {
				break
			}
			if _, done := skip[id]; done {
				continue
			}
			skip[id] = struct{}{}
			progress = true
			rejected++
			res := prunequeue.Result{ID: id, CompletedAt: time.Now().UTC(), Error: pruneTooManyPending}
			if err := s.pruneQ.Complete(res); err != nil {
				log.Printf("[prune-queue] write error result for %s failed: %v", id, err)
			}
		}
		if !progress {
			break
		}
	}
	if rejected > 0 {
		log.Printf("[prune-queue] more than %d requests pending in %s: rejected %d (%s)",
			prunequeue.MaxPendingList, s.pruneQ.Location(), rejected, pruneTooManyPending)
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
