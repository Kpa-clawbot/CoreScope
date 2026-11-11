package main

import (
	"maps"
	"slices"
	"strings"
)

// SnapshotEdges returns a deep copy of every edge, in no particular order.
// AllEdges hands out the graph's own pointers, which ingest keeps mutating
// under g.mu; the copies here (Observers, CountsByMode and Candidates
// included) share nothing with the graph, so callers may read or change
// them without any lock.
func (g *NeighborGraph) SnapshotEdges() []NeighborEdge {
	return g.snapshotEdges(nil)
}

// snapshotEdgesAmong is SnapshotEdges restricted to resolved edges whose two
// endpoints are both in pubkeys (case-insensitive). Filtering happens before
// the copy, so the read lock is held for one cheap pass over the edges plus
// copies of the few that match, not a clone of the whole graph.
func (g *NeighborGraph) snapshotEdgesAmong(pubkeys []string) []NeighborEdge {
	keep := make(map[string]struct{}, len(pubkeys))
	for _, pk := range pubkeys {
		keep[strings.ToLower(pk)] = struct{}{}
	}
	return g.snapshotEdges(keep)
}

// snapshotEdges copies the edges under the read lock; a nil keep copies all.
func (g *NeighborGraph) snapshotEdges(keep map[string]struct{}) []NeighborEdge {
	if g == nil {
		return nil
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	n := len(g.edges)
	if keep != nil {
		n = 0
	}
	out := make([]NeighborEdge, 0, n)
	for _, e := range g.edges {
		if keep != nil && !edgeAmong(e, keep) {
			continue
		}
		c := *e
		c.Observers = maps.Clone(e.Observers)
		c.CountsByMode = maps.Clone(e.CountsByMode)
		c.Candidates = slices.Clone(e.Candidates)
		out = append(out, c)
	}
	return out
}

func edgeAmong(e *NeighborEdge, keep map[string]struct{}) bool {
	if e.NodeB == "" {
		return false
	}
	if _, ok := keep[strings.ToLower(e.NodeA)]; !ok {
		return false
	}
	_, ok := keep[strings.ToLower(e.NodeB)]
	return ok
}
