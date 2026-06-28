// Package main: redundancy axis of repeater usefulness score (issue #672,
// axis 4 of 4). The "Redundancy" signal measures how IRREPLACEABLE a node
// is — how much the mesh fragments if it disappears. Despite the name (it
// is the redundancy *of the surrounding network*, inverted), a HIGH score
// means LOW surrounding redundancy: the node is a cut vertex whose removal
// disconnects parts of the mesh — the classic "sole repeater bridging a
// valley". A score of 0 means the node is fully replaceable: alternate
// paths exist, so removing it disconnects no one.
//
// Definition: for node v, disconnectedPairs(v) = the number of node pairs
// that become unreachable from each other when v is removed from its
// connected component. If removing v splits its component (of N nodes,
// excluding v: S = N-1) into pieces of sizes p1, p2, …, then
//
//	disconnectedPairs(v) = (S² − Σ pᵢ²) / 2
//
// (every cross-piece pair is newly severed). For a non-cut vertex there is
// a single piece of size S, giving 0. Scores are normalized by the max
// observed so the single most-critical repeater is 1.0; if the mesh is
// 2-edge-connected (no cut vertices) every score is 0 — correct, nothing
// is irreplaceable.
//
// Efficiency: a single Tarjan articulation-point DFS (per connected
// component) computes every cut vertex AND the sizes of the pieces it
// separates in O(V + E) — no per-node removal + APSP. This is what makes
// the axis cheap enough to recompute on the same background cadence as the
// other three.
//
// The piece sizes come straight from the DFS: for a tree child c of v with
// low[c] ≥ disc[v], the subtree rooted at c (size[c] nodes) is cut off;
// the remaining nodes (still attached to v's parent / via back-edges) form
// one final "rest" piece. For the DFS root this condition holds for every
// child, correctly yielding one piece per child subtree.
package main

import (
	"math"
	"strings"
)

// redundancyMinWeight is the affinity-weight floor an edge must clear to count
// toward articulation structure (#1762 BLOCKER-2). Unlike the bridge/coverage
// axes — which keep every edge above bridgeMinWeightEpsilon (≈1e-9) and let the
// 1/weight distance down-weight flimsy ones — articulation analysis is binary:
// an edge either exists (and can hold a component together) or it does not.
// A single uncorroborated sighting would otherwise make a genuine cut vertex
// look redundant by "supplying" an alternate path that exists only on paper.
//
// The floor is the weight of exactly one such flimsy edge: a single fresh
// observation from a single observer, i.e.
//
//	Score   = min(1, Count/affinitySaturationCount)·decay = (1/100)·1
//	Conf    = max(1,|Observers|)/affinityObserverSaturation = 1/3
//	weight  = Score·Conf = (1/100)·(1/3) ≈ 0.00333
//
// (see NeighborEdge.Score / .Confidence). Requiring weight to EXCEED this means
// an edge must carry either more observations or more independent observers
// than a lone fresh sighting — i.e. real corroboration — before it can mask a
// cut vertex. Decayed-but-corroborated edges still clear it; lone fresh ones do
// not. This mirrors the same Score·Confidence signal the Bridge axis weights by.
//
// NOTE: this floor is derived from affinitySaturationCount and
// affinityObserverSaturation — re-evaluate it whenever either affinity-tuning
// constant changes, or the "one lone fresh sighting" threshold silently shifts
// (#1762 MINOR-13).
const redundancyMinWeight = (1.0 / float64(affinitySaturationCount)) / affinityObserverSaturation

// ComputeRedundancyScores returns a map pubkey → redundancy (criticality)
// score in [0, 1] over the undirected graph defined by `edges`. Connectivity
// (whether an edge exists), not the exact weight, drives articulation
// structure — but the edge must clear redundancyMinWeight first so a single
// uncorroborated sighting cannot fabricate an alternate path that masks a real
// cut vertex (#1762 BLOCKER-2). Keys are lowercase pubkeys.
//
// Self-loops, edges with a non-finite weight (NaN/±Inf — `w < x` is false for
// NaN, so it must be rejected explicitly), and edges below redundancyMinWeight
// are skipped. Pure (no global state, no locks); safe to call concurrently.
func ComputeRedundancyScores(edges []BridgeEdge) map[string]float64 {
	// Unweighted connectivity adjacency; a set dedups parallel edges.
	adj := make(map[string]map[string]struct{})
	addNode := func(a string) {
		if adj[a] == nil {
			adj[a] = make(map[string]struct{})
		}
	}
	for _, e := range edges {
		a := strings.ToLower(strings.TrimSpace(e.A))
		b := strings.ToLower(strings.TrimSpace(e.B))
		if a == "" || b == "" || a == b {
			continue
		}
		w := e.Weight
		if math.IsNaN(w) || math.IsInf(w, 0) || w < redundancyMinWeight {
			continue
		}
		addNode(a)
		addNode(b)
		adj[a][b] = struct{}{}
		adj[b][a] = struct{}{}
	}
	if len(adj) == 0 {
		return map[string]float64{}
	}

	nodes := make([]string, 0, len(adj))
	for n := range adj {
		nodes = append(nodes, n)
	}

	disc := make(map[string]int, len(adj))  // DFS discovery time (0 = unvisited)
	low := make(map[string]int, len(adj))   // lowest disc reachable via subtree + one back-edge
	size := make(map[string]int, len(adj))  // subtree size
	sep := make(map[string][]int, len(adj)) // per node: sizes of subtrees it cuts off
	timer := 0

	// Recursive Tarjan. NOTE the depth is the longest DFS-tree path, which a
	// pathological linear chain of N nodes makes N deep — i.e. unbounded in
	// principle. This is acceptable here because (a) Go grows the goroutine
	// stack on demand (default cap ~1GB ≫ a few thousand shallow frames) and
	// (b) real mesh components are low-diameter, not chains. If a degenerate
	// graph ever threatens the stack, convert this to an explicit-stack
	// iterative DFS — the piece-size accounting below is unaffected.
	// The visit appends every node it reaches to `component`, owned by the
	// caller (one fresh slice per connected component) and threaded through as
	// a pointer — so the accumulator's lifecycle is explicit at the call site
	// rather than reset via a shared closure variable (#1762 review).
	var dfs func(u, parent string, acc *[]string)
	dfs = func(u, parent string, acc *[]string) {
		timer++
		disc[u] = timer
		low[u] = timer
		size[u] = 1
		*acc = append(*acc, u)
		skippedParent := false
		for v := range adj[u] {
			if v == parent && !skippedParent {
				skippedParent = true // skip exactly one tree edge back to parent
				continue
			}
			if disc[v] == 0 {
				dfs(v, u, acc)
				size[u] += size[v]
				if low[v] < low[u] {
					low[u] = low[v]
				}
				if low[v] >= disc[u] {
					sep[u] = append(sep[u], size[v])
				}
			} else if disc[v] < low[u] {
				low[u] = disc[v]
			}
		}
	}

	type component struct {
		nodes []string
		total int
	}
	var comps []component
	for _, r := range nodes {
		if disc[r] != 0 {
			continue
		}
		var nodesInComp []string // fresh accumulator owned by this component
		dfs(r, "", &nodesInComp)
		comps = append(comps, component{nodes: nodesInComp, total: size[r]})
	}

	disconnected := make(map[string]float64, len(adj))
	maxDP := 0.0
	for _, c := range comps {
		s := float64(c.total - 1) // nodes in the component other than the removed one
		for _, u := range c.nodes {
			sepSum := 0
			var sumSq float64
			for _, ps := range sep[u] {
				sepSum += ps
				sumSq += float64(ps) * float64(ps)
			}
			rest := c.total - 1 - sepSum
			if rest > 0 {
				sumSq += float64(rest) * float64(rest)
			}
			dp := (s*s - sumSq) / 2.0
			if dp < 0 {
				dp = 0 // floating-point guard; algebraically dp ≥ 0
			}
			disconnected[u] = dp
			if dp > maxDP {
				maxDP = dp
			}
		}
	}

	if maxDP > 0 {
		for k, v := range disconnected {
			disconnected[k] = v / maxDP
		}
	}
	return disconnected
}
