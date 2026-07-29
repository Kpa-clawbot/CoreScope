// Package main: Network Digest -- third and final piece of dborup's
// requested network-changes tooling (2026-07-29), summarizing New Nodes
// (new_nodes.go) and Node Changes (node_changes.go) activity over a
// rolling window into a single "what happened lately" view.
package main

import (
	"net/http"
	"sort"
	"time"
)

// AreaGrowth is one area's share of new-node activity in a digest window.
type AreaGrowth struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

// NetworkDigest summarizes New Nodes + Node Changes activity since a
// cutoff time.
type NetworkDigest struct {
	Window        string `json:"window"`
	Origin        string `json:"origin"`
	Since         string `json:"since"`
	NewNodes      int    `json:"newNodes"`
	RoleChanges   int    `json:"roleChanges"`
	NameChanges   int    `json:"nameChanges"`
	PositionMoves int    `json:"positionMoves"`
	Resurrections int    `json:"resurrections"`
	// AreaBreakdown ranks every configured area that had at least one new
	// node in the window, most active first (ties alphabetical). Each
	// node counts toward its single most-specific area only (see
	// AreaForPoint in computeNetworkDigest), so a broad umbrella area
	// doesn't dominate just because it also contains everything else.
	// Omitted when no new node in the window has a known area.
	AreaBreakdown []AreaGrowth `json:"areaBreakdown,omitempty"`
	// NewNodesCapped/ChangesCapped are true when newNodesSQLFetchCap /
	// nodeChangesSQLFetchCap was hit AND the oldest fetched row still
	// falls inside the window -- meaning there may be more qualifying
	// rows older than the cap that were never fetched, so the
	// corresponding counts above are a floor, not an exact total.
	NewNodesCapped bool `json:"newNodesCapped,omitempty"`
	ChangesCapped  bool `json:"changesCapped,omitempty"`
}

// matchesOrigin reports whether a domestic/foreign flag satisfies the
// requested origin filter ("all" | "domestic" | "foreign"); unrecognized
// values behave like "all", matching handleNetworkDigest's own
// validation already having rejected anything else.
func matchesOrigin(foreign bool, origin string) bool {
	switch origin {
	case "domestic":
		return !foreign
	case "foreign":
		return foreign
	default:
		return true
	}
}

// computeNetworkDigest summarizes New Nodes and Node Changes activity
// since the given cutoff, optionally narrowed to "domestic" or "foreign"
// nodes only (origin == "all" for no filtering).
//
// Reuses computeNewNodes/computeNodeChanges (with their existing
// blacklist filtering and, for new nodes, resurrection exclusion) rather
// than re-querying from scratch. Counts are exact up to
// newNodesSQLFetchCap/nodeChangesSQLFetchCap (500) rows fetched from
// each -- a deliberate tradeoff favoring reuse over a second set of
// dedicated COUNT queries. On networks with enough churn to exceed the
// cap within the requested window (observed on this deployment's 30d
// window), the affected counts become a floor rather than an exact
// total; NewNodesCapped/ChangesCapped flag that case so the frontend can
// render e.g. "500+" instead of a falsely-precise number. The capped
// check always runs against the unfiltered fetch, since it's asking
// "did the SQL LIMIT cut off rows we didn't see" -- a question that
// doesn't depend on the origin filter applied afterward.
func (s *Server) computeNetworkDigest(since time.Time, origin string) (*NetworkDigest, error) {
	digest := &NetworkDigest{Since: since.UTC().Format(time.RFC3339)}

	newNodes, err := s.computeNewNodes(newNodesSQLFetchCap)
	if err != nil {
		return nil, err
	}
	areaCounts := map[string]int{}
	for _, n := range newNodes {
		if !matchesOrigin(n.Foreign, origin) {
			continue
		}
		t, perr := time.Parse(time.RFC3339, n.FirstSeen)
		if perr != nil || t.Before(since) {
			continue
		}
		digest.NewNodes++
		// Most-specific area only (AreaForPoint), not every overlapping
		// area (AreaKeysForPoint/n.Areas) -- a broad umbrella area like
		// "Europa" contains virtually every node, so tallying all
		// matches makes it "win" Most Growth every time regardless of
		// where the real activity is. AreaForPoint already exists for
		// exactly this per-node "which area does this belong to"
		// question (smallest bounding box wins on overlap).
		if n.Lat != nil && n.Lon != nil && len(s.cfg.Areas) > 0 {
			if label, ok := AreaForPoint(*n.Lat, *n.Lon, s.cfg.Areas); ok {
				areaCounts[label]++
			}
		}
	}
	if len(newNodes) == newNodesSQLFetchCap {
		oldest := newNodes[len(newNodes)-1]
		if t, perr := time.Parse(time.RFC3339, oldest.FirstSeen); perr == nil && !t.Before(since) {
			digest.NewNodesCapped = true
		}
	}
	if len(areaCounts) > 0 {
		areas := make([]AreaGrowth, 0, len(areaCounts))
		for label, count := range areaCounts {
			areas = append(areas, AreaGrowth{Label: label, Count: count})
		}
		sort.Slice(areas, func(i, j int) bool {
			if areas[i].Count != areas[j].Count {
				return areas[i].Count > areas[j].Count
			}
			return areas[i].Label < areas[j].Label
		})
		digest.AreaBreakdown = areas
	}

	changes, err := s.computeNodeChanges(nodeChangesSQLFetchCap)
	if err != nil {
		return nil, err
	}
	// node_changes rows don't carry their own foreign_advert snapshot --
	// resolved live against the node's current state via a bulk lookup,
	// same shape as computeNodeChanges' own name resolution. Skipped
	// entirely for origin=="all" since it's an extra query nobody asked
	// for in the common case.
	var foreignFlags map[string]bool
	if origin != "all" {
		pubkeys := make([]string, 0, len(changes))
		for _, c := range changes {
			pubkeys = append(pubkeys, c.PublicKey)
		}
		foreignFlags = s.db.foreignFlagsForPubkeys(pubkeys)
	}
	for _, c := range changes {
		if origin != "all" && !matchesOrigin(foreignFlags[c.PublicKey], origin) {
			continue
		}
		t, perr := time.Parse(time.RFC3339, c.DetectedAt)
		if perr != nil || t.Before(since) {
			continue
		}
		switch c.ChangeType {
		case "role":
			digest.RoleChanges++
		case "name":
			digest.NameChanges++
		case "position":
			digest.PositionMoves++
		case "resurrected":
			digest.Resurrections++
		}
	}
	if len(changes) == nodeChangesSQLFetchCap {
		oldest := changes[len(changes)-1]
		if t, perr := time.Parse(time.RFC3339, oldest.DetectedAt); perr == nil && !t.Before(since) {
			digest.ChangesCapped = true
		}
	}
	return digest, nil
}

// handleNetworkDigest serves Tools > Network Digest: a rolling-window
// summary of New Nodes + Node Changes activity. Defaults to a 7-day
// window, same "d" suffix convention as other window query params in
// this codebase (parseWindowDuration), and an "all" origin (same
// All/Domestic/Foreign vocabulary as Tools > New Nodes' toggle).
func (s *Server) handleNetworkDigest(w http.ResponseWriter, r *http.Request) {
	window := r.URL.Query().Get("window")
	if window == "" {
		window = "7d"
	}
	dur, err := parseWindowDuration(window)
	if err != nil {
		writeError(w, 400, "invalid window: "+window)
		return
	}
	origin := r.URL.Query().Get("origin")
	if origin == "" {
		origin = "all"
	}
	if origin != "all" && origin != "domestic" && origin != "foreign" {
		writeError(w, 400, "invalid origin: "+origin)
		return
	}
	since := time.Now().UTC().Add(-dur)
	digest, err := s.computeNetworkDigest(since, origin)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	digest.Window = window
	digest.Origin = origin
	writeJSON(w, digest)
}
