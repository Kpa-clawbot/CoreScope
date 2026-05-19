package main

import (
	"math"
	"testing"
)

// TestComputeBridgeScores_LineGraph asserts the canonical property of
// betweenness centrality on a 4-node line A-B-C-D: the two middle
// nodes B and C have non-zero centrality (every path between an end
// and a far end traverses them) while the two leaves A and D bridge
// no pairs and score zero. This is the RED test for issue #672 bridge
// axis — it fails on master where ComputeBridgeScores is a stub.
func TestComputeBridgeScores_LineGraph(t *testing.T) {
	edges := []BridgeEdge{
		{A: "a", B: "b", Weight: 1.0},
		{A: "b", B: "c", Weight: 1.0},
		{A: "c", B: "d", Weight: 1.0},
	}
	scores := ComputeBridgeScores(edges)

	for _, leaf := range []string{"a", "d"} {
		if v, ok := scores[leaf]; !ok || v != 0 {
			t.Errorf("leaf %q: want score 0 (present), got %v ok=%v", leaf, v, ok)
		}
	}
	for _, mid := range []string{"b", "c"} {
		v, ok := scores[mid]
		if !ok {
			t.Errorf("middle %q: missing from result map", mid)
			continue
		}
		if v <= 0 {
			t.Errorf("middle %q: want non-zero centrality, got %v", mid, v)
		}
	}
	// Normalization: max must equal 1.0 exactly when any node has
	// non-zero centrality.
	maxScore := 0.0
	for _, v := range scores {
		if v > maxScore {
			maxScore = v
		}
	}
	if math.Abs(maxScore-1.0) > 1e-9 {
		t.Errorf("max normalized score: want 1.0, got %v", maxScore)
	}
}

// TestComputeBridgeScores_TriangleNoBridge: in a fully connected
// triangle every node has at least one alternate path so betweenness
// is zero everywhere. The map should still contain all three nodes
// (so callers can distinguish "in graph but unimportant" from
// "not in graph") with explicit zero values.
func TestComputeBridgeScores_TriangleNoBridge(t *testing.T) {
	edges := []BridgeEdge{
		{A: "x", B: "y", Weight: 1.0},
		{A: "y", B: "z", Weight: 1.0},
		{A: "z", B: "x", Weight: 1.0},
	}
	scores := ComputeBridgeScores(edges)
	for _, n := range []string{"x", "y", "z"} {
		if v, ok := scores[n]; !ok || v != 0 {
			t.Errorf("triangle node %q: want 0 present, got %v ok=%v", n, v, ok)
		}
	}
}

// TestComputeBridgeScores_Empty: an empty edge list yields an empty
// (non-nil) map. Defensive check so the recomputer can swap in an
// empty result without crashing the lookup path.
func TestComputeBridgeScores_Empty(t *testing.T) {
	scores := ComputeBridgeScores(nil)
	if scores == nil {
		t.Fatal("want non-nil empty map, got nil")
	}
	if len(scores) != 0 {
		t.Errorf("want empty map, got %d entries", len(scores))
	}
}

// TestComputeBridgeScores_WeightSensitive verifies the algorithm uses
// edge weights as affinity (higher = preferred). In a graph A-B-D and
// A-C-D where the B-route has weight 1.0 and the C-route has weight
// 0.1, shortest path (max-affinity = min 1/w) goes through B, so B
// has positive centrality and C does not. This is the "mutation
// test" — flip the cost formula (e.g., remove the 1/w inversion) and
// this test inverts.
func TestComputeBridgeScores_WeightSensitive(t *testing.T) {
	edges := []BridgeEdge{
		{A: "a", B: "b", Weight: 1.0},
		{A: "b", B: "d", Weight: 1.0},
		{A: "a", B: "c", Weight: 0.1},
		{A: "c", B: "d", Weight: 0.1},
	}
	scores := ComputeBridgeScores(edges)
	if scores["b"] <= scores["c"] {
		t.Errorf("stronger-weight intermediary b should outrank c: b=%v c=%v",
			scores["b"], scores["c"])
	}
}
