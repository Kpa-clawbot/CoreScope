// Package main: shared weighted-graph primitives for the structural
// usefulness axes (issue #672). The Bridge (betweenness) and Coverage
// (harmonic reach) axes operate on the same undirected, affinity-weighted
// neighbor graph and previously each built the distance adjacency by hand —
// a line-for-line duplicate (#1762 review). These helpers are the single
// source of truth for that construction. Within a single recomputeUsefulnessAxes
// call the Bridge and Coverage axes are fed from the SAME bridgeEdgesFromGraph
// snapshot, so they see byte-identical structure there. This does NOT extend
// across recomputers: the bridge recomputer and the usefulness-axes recomputer
// run on independent cadences and take their own graph snapshots, so the bridge
// map surfaced by handleNodes need not match the Coverage axis's snapshot.
package main

import (
	"container/heap"
	"math"
	"strings"
)

// weightedDistanceAdjacency builds the symmetric distance adjacency from a
// weighted edge list: cost = 1/weight (lower distance = stronger affinity),
// keeping the cheapest edge per pair. Self-loops and edges with a non-finite
// weight (NaN/±Inf) or weight < bridgeMinWeightEpsilon are skipped — they
// would break Dijkstra's relaxation invariant. Note `w < epsilon` is false
// for NaN, so NaN must be rejected explicitly; otherwise 1/NaN = NaN would
// poison every reach computation downstream (#1762 review). Keys are the
// lowercased pubkey form.
func weightedDistanceAdjacency(edges []BridgeEdge) map[string]map[string]float64 {
	adj := make(map[string]map[string]float64)
	addOrMerge := func(a, b string, dist float64) {
		m, ok := adj[a]
		if !ok {
			m = make(map[string]float64)
			adj[a] = m
		}
		if existing, has := m[b]; !has || dist < existing {
			m[b] = dist
		}
	}
	for _, e := range edges {
		a := strings.ToLower(strings.TrimSpace(e.A))
		b := strings.ToLower(strings.TrimSpace(e.B))
		if a == "" || b == "" || a == b {
			continue
		}
		w := e.Weight
		if math.IsNaN(w) || math.IsInf(w, 0) || w < bridgeMinWeightEpsilon {
			continue
		}
		dist := 1.0 / w
		addOrMerge(a, b, dist)
		addOrMerge(b, a, dist)
	}
	return adj
}

// dijkstraFrom returns the shortest-path distance from src to every reachable
// node over the distance adjacency (unreachable nodes are absent). Reuses the
// bridgePQ min-heap. Used by the Coverage axis; the Bridge axis runs its own
// Brandes-coupled SSSP that additionally tracks predecessors and path counts.
func dijkstraFrom(adj map[string]map[string]float64, src string) map[string]float64 {
	dist := map[string]float64{src: 0}
	pq := &bridgePQ{}
	heap.Init(pq)
	heap.Push(pq, bridgePQItem{node: src, dist: 0})

	visited := make(map[string]bool)
	for pq.Len() > 0 {
		top := heap.Pop(pq).(bridgePQItem)
		v := top.node
		if visited[v] {
			continue
		}
		visited[v] = true
		for w, edgeDist := range adj[v] {
			alt := top.dist + edgeDist
			if cur, ok := dist[w]; !ok || alt < cur {
				dist[w] = alt
				heap.Push(pq, bridgePQItem{node: w, dist: alt})
			}
		}
	}
	return dist
}
