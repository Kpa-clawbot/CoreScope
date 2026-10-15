// Package main: bridge axis of repeater usefulness score (issue #672,
// axis 2 of 4). The "Bridge" signal is the betweenness centrality of a
// node in the (undirected, weighted) neighbor graph: a high value means
// the node lies on many shortest paths between other pairs and is hence
// structurally important — removing it would force traffic around or
// fragment the mesh.
//
// Algorithm: Brandes' algorithm (1) with Dijkstra for weighted
// shortest paths. Complexity O(V · (E + V log V)). For the staging
// graph (~600 nodes, ~2 000 edges) this is ~4.8M ops — trivial,
// completes in milliseconds. We accumulate raw betweenness across all
// sources, halve (an undirected pair is counted from each endpoint
// once), then normalize by the max observed value so the per-node
// score is in [0, 1].
//
// Edge weight follows the convention established by #1235: the
// affinity score (count + recency decay) is multiplied by the
// observer-diversity confidence — stronger, more corroborated
// neighborships are preferred when there is a choice of paths.
// Geo-rejected edges are already excluded from the input graph at
// build time (#1230) so we don't have to re-filter here.
//
// For Dijkstra we need a DISTANCE (lower = better) not an affinity
// (higher = better): cost = 1/weight. That conversion (plus the
// epsilon/non-finite-weight filtering and self-loop/dedup handling) now lives
// in the shared weightedDistanceAdjacency helper (graph_weighted.go), used by
// both this axis and Coverage so they see byte-identical graph structure;
// ComputeBridgeScores no longer builds the adjacency by hand.
//
// (1) Brandes, "A Faster Algorithm for Betweenness Centrality" (2001).
package main

import (
	"container/heap"
	"math"
)

// BridgeEdge is the algorithm-facing edge tuple consumed by
// ComputeBridgeScores. Endpoints A and B are pubkeys (case preserved
// by caller; we lowercase internally for stable keying). Weight is
// the affinity (higher = stronger connection). Edges with zero or
// negative weight are skipped — they would break Dijkstra's
// relaxation invariant.
type BridgeEdge struct {
	A, B   string
	Weight float64
}

// bridgeMinWeightEpsilon is the floor applied to weights before we
// invert them into Dijkstra distances. 1e-9 is small enough that any
// real weight (Score in [0,1] times Confidence in [0,1]) dominates,
// but large enough to avoid Inf when weight is exactly zero.
const bridgeMinWeightEpsilon = 1e-9

// ComputeBridgeScores returns a map pubkey → bridge score in [0, 1]
// computed via Brandes' weighted betweenness centrality on the
// undirected graph defined by `edges`. Returned map is keyed by the
// lowercase pubkey form (matching the byPathHop / persisted-edge
// convention). Nodes appearing in the graph but with zero betweenness
// are still present in the map with value 0.0.
//
// Self-loops (A == B) and edges with weight < epsilon are silently
// skipped. Duplicate edges between the same pair keep the cheapest
// (= the highest-weight) version — consistent with shortest-path
// semantics.
//
// Pure (no global state, no locks); safe to call concurrently.
// Cost: O(V · (E + V log V)).
func ComputeBridgeScores(edges []BridgeEdge) map[string]float64 {
	// 1. Build the distance adjacency (cost = 1/weight) — shared with the
	//    Coverage axis (graph_weighted.go) so both see identical structure.
	graph := weightedDistanceAdjacency(edges)
	if len(graph.nodes) == 0 {
		return map[string]float64{}
	}

	size := len(graph.nodes)
	bc := make([]float64, size)
	stack := make([]int, 0, size)
	pred := make([][]int, size)
	sigma, delta := make([]float64, size), make([]float64, size)
	work := newDistanceWorkspace(size)

	// 2. Brandes outer loop: one Dijkstra-based single-source shortest
	//    path computation per source vertex.
	for s := range graph.nodes {
		for _, node := range work.reached {
			pred[node] = pred[node][:0]
			sigma[node], delta[node] = 0, 0
		}
		stack = stack[:0]
		work.reset(s)
		sigma[s] = 1

		for work.queue.Len() > 0 {
			top := heap.Pop(&work.queue).(*bridgePQItem)
			v := top.node
			if work.visited[v] {
				continue
			}
			work.visited[v] = true
			stack = append(stack, v)

			for _, edge := range graph.adj[v] {
				w := edge.node
				alt := work.dist[v] + edge.dist
				if alt < work.dist[w]-1e-12 {
					work.improve(w, alt)
					sigma[w] = sigma[v]
					pred[w] = append(pred[w][:0], v)
				} else if math.Abs(alt-work.dist[w]) <= 1e-12 {
					sigma[w] += sigma[v]
					pred[w] = append(pred[w], v)
				}
			}
		}

		// 3. Back-propagation: walk the stack in reverse order.
		for i := len(stack) - 1; i >= 0; i-- {
			w := stack[i]
			for _, v := range pred[w] {
				if sigma[w] == 0 {
					continue
				}
				delta[v] += (sigma[v] / sigma[w]) * (1.0 + delta[w])
			}
			if w != s {
				bc[w] += delta[w]
			}
		}
	}

	// 4. Undirected graphs double-count each (s,t) pair, so halve.
	for i := range bc {
		bc[i] /= 2.0
	}

	// 5. Normalize by max so scores live in [0, 1]. If max is 0
	//    (clique or single edge) we leave everything at zero.
	maxBC := 0.0
	for _, v := range bc {
		if v > maxBC {
			maxBC = v
		}
	}
	out := make(map[string]float64, size)
	for i, key := range graph.nodes {
		out[key] = bc[i]
		if maxBC > 0 {
			out[key] /= maxBC
		}
	}
	return out
}

// ─── min-heap for Dijkstra ─────────────────────────────────────────────────────

type bridgePQItem struct {
	node  int
	dist  float64
	index int
}

type bridgePQ []*bridgePQItem

func (h bridgePQ) Len() int           { return len(h) }
func (h bridgePQ) Less(i, j int) bool { return h[i].dist < h[j].dist }
func (h bridgePQ) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index, h[j].index = i, j
}
func (h *bridgePQ) Push(x interface{}) {
	item := x.(*bridgePQItem)
	item.index = len(*h)
	*h = append(*h, item)
}
func (h *bridgePQ) Pop() interface{} {
	old := *h
	n := len(old)
	it := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	it.index = -1
	return it
}
