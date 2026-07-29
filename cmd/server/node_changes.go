// Package main: "Node Changes" audit log -- second piece of dborup's
// requested network-changes tooling (2026-07-29), after New Nodes
// (new_nodes.go). Reads node_changes, written by cmd/ingestor's
// detectAndLogNodeChange as ADVERTs arrive (role/name/position changes,
// plus "returned after being pruned to inactive_nodes"). A dedicated
// audit table rather than a periodic snapshot diff, so nothing is missed
// between polls.
package main

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
)

// NodeChangeEntry is one row in the node-change audit log.
type NodeChangeEntry struct {
	ID        int64  `json:"id"`
	PublicKey string `json:"publicKey"`
	// Name is the node's CURRENT display name (resolved via a bulk nodes
	// lookup, same as ping_scores.go's relay-name resolution) -- not a
	// historical snapshot at the time of the change.
	Name       string `json:"name,omitempty"`
	ChangeType string `json:"changeType"` // "role" | "name" | "position" | "resurrected"
	OldValue   string `json:"oldValue,omitempty"`
	NewValue   string `json:"newValue,omitempty"`
	DetectedAt string `json:"detectedAt"`
	// DistanceKm is set only for changeType=="position" -- parsed from
	// the "lat,lon" old/new value strings cmd/ingestor writes.
	DistanceKm *float64 `json:"distanceKm,omitempty"`
}

const nodeChangesSQLFetchCap = 500

// computeNodeChanges returns the most recent node_changes rows, newest
// first, capped at limit (clamped to [1, nodeChangesSQLFetchCap]).
func (s *Server) computeNodeChanges(limit int) ([]NodeChangeEntry, error) {
	if limit <= 0 || limit > nodeChangesSQLFetchCap {
		limit = nodeChangesSQLFetchCap
	}
	rows, err := s.db.conn.Query(`
		SELECT id, public_key, change_type, old_value, new_value, detected_at
		FROM node_changes
		ORDER BY id DESC
		LIMIT ?`, nodeChangesSQLFetchCap)
	if err != nil {
		return nil, err
	}

	out := make([]NodeChangeEntry, 0, limit)
	for rows.Next() {
		var e NodeChangeEntry
		var oldValue, newValue sql.NullString
		if err := rows.Scan(&e.ID, &e.PublicKey, &e.ChangeType, &oldValue, &newValue, &e.DetectedAt); err != nil {
			continue
		}
		if s.cfg.IsBlacklisted(e.PublicKey) {
			continue
		}
		e.OldValue = oldValue.String
		e.NewValue = newValue.String
		if e.ChangeType == "position" {
			e.DistanceKm = positionChangeDistanceKm(e.OldValue, e.NewValue)
		}
		out = append(out, e)
		if len(out) >= limit {
			break
		}
	}
	// Explicitly closed (not deferred) BEFORE the nested query below --
	// issuing a second query while this cursor is still open (the loop
	// above may `break` without draining every row) deadlocks the
	// single-connection SQLite pool used in tests. Recurring lesson in
	// this codebase; see ping_scores.go/observer neighbor code for the
	// same pattern.
	rows.Close()

	pubkeys := make([]string, 0, len(out))
	for _, e := range out {
		pubkeys = append(pubkeys, e.PublicKey)
	}
	if len(pubkeys) > 0 {
		names, _ := s.db.namesAndRolesForPubkeys(pubkeys)
		for i := range out {
			if name := names[out[i].PublicKey]; name != "" {
				out[i].Name = name
			}
		}
	}
	return out, nil
}

// positionChangeDistanceKm parses cmd/ingestor's "lat,lon" old/new value
// strings and returns the haversine distance between them, or nil if
// either fails to parse.
func positionChangeDistanceKm(oldValue, newValue string) *float64 {
	oldLat, oldLon, ok1 := parseLatLonPair(oldValue)
	newLat, newLon, ok2 := parseLatLonPair(newValue)
	if !ok1 || !ok2 {
		return nil
	}
	d := haversineKm(oldLat, oldLon, newLat, newLon)
	return &d
}

func parseLatLonPair(s string) (lat, lon float64, ok bool) {
	parts := strings.SplitN(s, ",", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	lat, err1 := strconv.ParseFloat(parts[0], 64)
	lon, err2 := strconv.ParseFloat(parts[1], 64)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return lat, lon, true
}

// handleNodeChanges serves Tools > Node Changes: the most recent audited
// role/name/position changes and pruned-node returns, network-wide. Empty
// list (not an error) when nothing has been logged yet.
func (s *Server) handleNodeChanges(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	entries, err := s.computeNodeChanges(limit)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]interface{}{"nodeChanges": entries})
}
