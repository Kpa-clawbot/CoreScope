// Package main: bridge axis of repeater usefulness score (issue #672,
// axis 2 of 4). The "Bridge" signal is the betweenness centrality of a
// node in the (undirected, weighted) neighbor graph. See follow-up
// commit for the actual Brandes implementation; this commit lands the
// stub + the failing test so the test gates the change.
package main

// BridgeEdge is the algorithm-facing edge tuple consumed by
// ComputeBridgeScores. Endpoints A and B are pubkeys. Weight is the
// affinity (higher = stronger connection).
type BridgeEdge struct {
	A, B   string
	Weight float64
}

// ComputeBridgeScores will return a map pubkey → bridge score in [0,1]
// computed via Brandes' weighted betweenness centrality. STUB: returns
// an empty map so the accompanying test fails on assertion (not on a
// missing symbol) — proving the test gates the change. Real
// implementation lands in the next commit.
func ComputeBridgeScores(edges []BridgeEdge) map[string]float64 {
	_ = edges
	return map[string]float64{}
}
