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

type weightedNeighbor struct {
	node int
	dist float64
}

type weightedGraph struct {
	nodes []string
	adj   [][]weightedNeighbor
}

// weightedDistanceAdjacency builds the symmetric distance adjacency from a
// weighted edge list: cost = 1/weight (lower distance = stronger affinity),
// keeping the cheapest edge per pair. Self-loops and edges with a non-finite
// weight (NaN/±Inf) or weight < bridgeMinWeightEpsilon are skipped — they
// would break Dijkstra's relaxation invariant. Note `w < epsilon` is false
// for NaN, so NaN must be rejected explicitly; otherwise 1/NaN = NaN would
// poison every reach computation downstream (#1762 review). Keys are the
// lowercased pubkey form.
func weightedDistanceAdjacency(edges []BridgeEdge) weightedGraph {
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
	// Convert pubkeys once, outside the all-sources shortest-path loops.
	// Keep the normalization and cheapest-duplicate rules above shared.
	graph := weightedGraph{nodes: make([]string, 0, len(adj)), adj: make([][]weightedNeighbor, len(adj))}
	index := make(map[string]int, len(adj))
	for node := range adj {
		index[node] = len(graph.nodes)
		graph.nodes = append(graph.nodes, node)
	}
	for i, node := range graph.nodes {
		graph.adj[i] = make([]weightedNeighbor, 0, len(adj[node]))
		for next, distance := range adj[node] {
			graph.adj[i] = append(graph.adj[i], weightedNeighbor{node: index[next], dist: distance})
		}
	}
	return graph
}

// distanceWorkspace reuses per-source state. Reset only reached vertices so
// disconnected graphs do not pay an all-vertices scan for every small component.
type distanceWorkspace struct {
	dist    []float64
	visited []bool
	reached []int
	queue   bridgePQ
	items   []bridgePQItem
}

func newDistanceWorkspace(size int) *distanceWorkspace {
	w := &distanceWorkspace{
		dist: make([]float64, size), visited: make([]bool, size),
		reached: make([]int, 0, size), queue: make(bridgePQ, 0, size),
		items: make([]bridgePQItem, size),
	}
	for i := range w.dist {
		w.dist[i] = math.Inf(1)
		w.items[i] = bridgePQItem{node: i, index: -1}
	}
	return w
}

func (w *distanceWorkspace) reset(src int) {
	for _, node := range w.reached {
		w.dist[node] = math.Inf(1)
		w.visited[node] = false
	}
	w.reached = w.reached[:0]
	w.queue = w.queue[:0]
	w.improve(src, 0)
}

// One item per vertex avoids boxing a new heap value on every relaxation.
// Decrease-key keeps the standard-library heap bounded to at most V entries.
func (w *distanceWorkspace) improve(node int, distance float64) {
	if math.IsInf(w.dist[node], 1) {
		w.reached = append(w.reached, node)
	}
	w.dist[node] = distance
	item := &w.items[node]
	item.dist = distance
	if item.index < 0 {
		heap.Push(&w.queue, item)
	} else {
		heap.Fix(&w.queue, item.index)
	}
}

// dijkstraFrom fills distances and reached vertices in the reusable workspace.
// Bridge uses the same graph/workspace with Brandes predecessor/path-count logic.
func dijkstraFrom(adj [][]weightedNeighbor, src int, work *distanceWorkspace) {
	work.reset(src)
	for work.queue.Len() > 0 {
		top := heap.Pop(&work.queue).(*bridgePQItem)
		v := top.node
		if work.visited[v] {
			continue
		}
		work.visited[v] = true
		for _, edge := range adj[v] {
			alt := top.dist + edge.dist
			if alt < work.dist[edge.node] {
				work.improve(edge.node, alt)
			}
		}
	}
}
