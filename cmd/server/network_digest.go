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
	Since         string `json:"since"`
	NewNodes      int    `json:"newNodes"`
	RoleChanges   int    `json:"roleChanges"`
	NameChanges   int    `json:"nameChanges"`
	PositionMoves int    `json:"positionMoves"`
	Resurrections int    `json:"resurrections"`
	// TopArea is the configured area with the most new-node activity in
	// the window. Omitted when no new node in the window has a known
	// area (no areas configured, or none resolved).
	TopArea *AreaGrowth `json:"topArea,omitempty"`
	// NewNodesCapped/ChangesCapped are true when newNodesSQLFetchCap /
	// nodeChangesSQLFetchCap was hit AND the oldest fetched row still
	// falls inside the window -- meaning there may be more qualifying
	// rows older than the cap that were never fetched, so the
	// corresponding counts above are a floor, not an exact total.
	NewNodesCapped bool `json:"newNodesCapped,omitempty"`
	ChangesCapped  bool `json:"changesCapped,omitempty"`
}

// computeNetworkDigest summarizes New Nodes and Node Changes activity
// since the given cutoff.
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
// render e.g. "500+" instead of a falsely-precise number.
func (s *Server) computeNetworkDigest(since time.Time) (*NetworkDigest, error) {
	digest := &NetworkDigest{Since: since.UTC().Format(time.RFC3339)}

	newNodes, err := s.computeNewNodes(newNodesSQLFetchCap)
	if err != nil {
		return nil, err
	}
	areaCounts := map[string]int{}
	for _, n := range newNodes {
		t, perr := time.Parse(time.RFC3339, n.FirstSeen)
		if perr != nil || t.Before(since) {
			continue
		}
		digest.NewNodes++
		for _, a := range n.Areas {
			areaCounts[a]++
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
		digest.TopArea = &areas[0]
	}

	changes, err := s.computeNodeChanges(nodeChangesSQLFetchCap)
	if err != nil {
		return nil, err
	}
	for _, c := range changes {
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
// this codebase (parseWindowDuration).
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
	since := time.Now().UTC().Add(-dur)
	digest, err := s.computeNetworkDigest(since)
	if err != nil {
		writeError(w, 500, err.Error())
		return
	}
	digest.Window = window
	writeJSON(w, digest)
}
