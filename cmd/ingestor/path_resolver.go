package main

import (
	"strings"
	"sync/atomic"
)

// Context-aware hop resolver (#1560 — full restore of pre-#1289
// disambiguation semantics).
//
// Status in this commit: STUB. The real algorithm lands in the
// next commit. Tests in resolved_path_test.go (1-byte collision,
// no-adjacency-nil, ADVERT anchoring) exercise the expected
// post-fix behavior and MUST fail against this stub — that is the
// red-then-green gate this commit establishes.

// NeighborGraph is the in-memory adjacency snapshot used by the
// context-aware resolver.
type NeighborGraph struct {
	adj map[string]map[string]struct{}
}

// NewNeighborGraph returns an empty graph.
func NewNeighborGraph() *NeighborGraph {
	return &NeighborGraph{adj: make(map[string]map[string]struct{})}
}

// AddEdge adds an undirected adjacency a↔b. Lowercased internally.
func (g *NeighborGraph) AddEdge(a, b string) {
	a = strings.ToLower(a)
	b = strings.ToLower(b)
	if a == "" || b == "" || a == b {
		return
	}
	if g.adj[a] == nil {
		g.adj[a] = make(map[string]struct{})
	}
	if g.adj[b] == nil {
		g.adj[b] = make(map[string]struct{})
	}
	g.adj[a][b] = struct{}{}
	g.adj[b][a] = struct{}{}
}

// IsAdjacent reports whether a and b appear together in any neighbor edge.
func (g *NeighborGraph) IsAdjacent(a, b string) bool {
	if g == nil {
		return false
	}
	a = strings.ToLower(a)
	b = strings.ToLower(b)
	if a == "" || b == "" {
		return false
	}
	nbrs, ok := g.adj[a]
	if !ok {
		return false
	}
	_, present := nbrs[b]
	return present
}

// neighborGraphHolder caches the graph for hot-path reads.
type neighborGraphHolder struct {
	v atomic.Value // holds *NeighborGraph
}

func (h *neighborGraphHolder) load() *NeighborGraph {
	if v := h.v.Load(); v != nil {
		return v.(*NeighborGraph)
	}
	return nil
}

func (h *neighborGraphHolder) store(g *NeighborGraph) {
	h.v.Store(g)
}

// resolveHopWithContext — STUB: ignores anchor + graph, just returns
// the naive single-candidate match. Real implementation lands in the
// follow-up green commit.
func resolveHopWithContext(hop string, anchor string, graph *NeighborGraph, idx prefixIndex) *string {
	if idx == nil {
		return nil
	}
	h := strings.ToLower(hop)
	candidates := idx[h]
	if len(candidates) == 1 {
		pk := candidates[0]
		return &pk
	}
	return nil
}

// resolvePathWithContext — STUB delegating to the naive resolver.
// Real implementation in the follow-up green commit.
func resolvePathWithContext(hops []string, fromPubkey string, graph *NeighborGraph, idx prefixIndex) []*string {
	return resolvePath(hops, idx)
}
