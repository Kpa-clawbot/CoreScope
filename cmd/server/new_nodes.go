// Package main: "New Nodes" feed -- part of dborup's requested "network
// changes" tooling (2026-07-29), starting with this piece: which nodes
// were seen on the mesh for the very first time, and which area(s) they're
// in. Follow-ups planned: a node-change audit log (role/name/position
// changes, tracked via a dedicated table so nothing is missed between
// polls) and a periodic network-activity digest built on top of both.
package main

import (
	"database/sql"
	"net/http"
	"sort"
	"strconv"
)

// NewNodeEntry is one row in the "New Nodes" feed.
type NewNodeEntry struct {
	PublicKey string   `json:"publicKey"`
	Name      string   `json:"name,omitempty"`
	Role      string   `json:"role,omitempty"`
	Lat       *float64 `json:"lat,omitempty"`
	Lon       *float64 `json:"lon,omitempty"`
	FirstSeen string   `json:"firstSeen"`
	// Areas: every configured area this node's position falls in (multi-
	// membership, same AreaKeysForPoint used by the Area Activity
	// leaderboard), alphabetized. Omitted when the node has no known
	// position or no areas are configured.
	Areas []string `json:"areas,omitempty"`
	// Foreign mirrors nodes.foreign_advert -- set when this node's ADVERT
	// GPS lay outside the configured geofilter polygon (#730, same flag
	// the node list's "foreign" field and MarkNodeForeign use). Not
	// omitempty: false is a real, meaningful value here (domestic), not
	// an absence of data.
	Foreign bool `json:"foreign"`
}

// newNodesSQLFetchCap bounds how many rows we ever pull from SQL before
// blacklist-filtering and truncating to the caller's requested limit in
// Go -- filtering blacklisted pubkeys AFTER a tight SQL LIMIT would silently
// undercount (e.g. requesting 50 but getting 48 back because 2 of the top
// 50 were blacklisted). This deployment's node count doesn't call for
// anything cleverer.
const newNodesSQLFetchCap = 500

// computeNewNodes returns the most recently first-seen nodes, newest
// first, capped at limit (clamped to [1, newNodesSQLFetchCap]).
//
// Excludes nodes that also appear in inactive_nodes. Why this matters:
// cmd/ingestor's stmtUpsertNode only sets nodes.first_seen on INSERT, never
// on the ON CONFLICT UPDATE path -- but MoveStaleNodes (retention pruning)
// copies a stale node's row into inactive_nodes and then DELETEs it from
// nodes outright. If that node advertises again later, UpsertNode's INSERT
// hits no conflict (the nodes row is gone) and writes a BRAND NEW
// first_seen = now, even though inactive_nodes still holds the original
// row with the true historical first_seen (inactive_nodes rows are never
// deleted). Without this check, a node that's simply been quiet for a
// while would misleadingly show up here as "new". That "came back after
// being pruned" case is a distinct, meaningful signal of its own -- it
// belongs in the planned node-change audit tool, not this feed.
func (s *Server) computeNewNodes(limit int) ([]NewNodeEntry, error) {
	if limit <= 0 || limit > newNodesSQLFetchCap {
		limit = newNodesSQLFetchCap
	}
	rows, err := s.db.conn.Query(`
		SELECT n.public_key, n.name, n.role, n.lat, n.lon, n.first_seen, n.foreign_advert
		FROM nodes n
		WHERE NOT EXISTS (SELECT 1 FROM inactive_nodes i WHERE i.public_key = n.public_key)
		  AND n.first_seen IS NOT NULL AND n.first_seen != ''
		ORDER BY n.first_seen DESC
		LIMIT ?`, newNodesSQLFetchCap)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]NewNodeEntry, 0, limit)
	for rows.Next() {
		var e NewNodeEntry
		var name, role sql.NullString
		var lat, lon sql.NullFloat64
		var foreignAdvert sql.NullInt64
		if err := rows.Scan(&e.PublicKey, &name, &role, &lat, &lon, &e.FirstSeen, &foreignAdvert); err != nil {
			continue
		}
		e.Foreign = foreignAdvert.Valid && foreignAdvert.Int64 != 0
		if s.cfg.IsBlacklisted(e.PublicKey) {
			continue
		}
		e.Name = name.String
		e.Role = role.String
		if lat.Valid {
			v := lat.Float64
			e.Lat = &v
		}
		if lon.Valid {
			v := lon.Float64
			e.Lon = &v
		}
		if e.Lat != nil && e.Lon != nil && s.cfg != nil && len(s.cfg.Areas) > 0 {
			for _, key := range AreaKeysForPoint(*e.Lat, *e.Lon, s.cfg.Areas) {
				if entry, ok := s.cfg.Areas[key]; ok {
					e.Areas = append(e.Areas, entry.Label)
				}
			}
			sort.Strings(e.Areas)
		}
		out = append(out, e)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// handleNewNodes serves Tools > New Nodes: the most recently first-seen
// nodes network-wide. Empty list (not an error) when nothing qualifies --
// a quiet network is a normal state, not a fault.
func (s *Server) handleNewNodes(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	entries, err := s.computeNewNodes(limit)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	writeJSON(w, map[string]interface{}{"newNodes": entries})
}
