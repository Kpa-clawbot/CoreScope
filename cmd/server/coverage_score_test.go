package main

import (
	"math"
	"testing"
)

// TestComputeCoverageScores_Empty: empty edge list yields a non-nil empty
// map (the recomputer swaps this in before the first graph lands).
func TestComputeCoverageScores_Empty(t *testing.T) {
	scores := ComputeCoverageScores(nil)
	if scores == nil {
		t.Fatal("want non-nil empty map, got nil")
	}
	if len(scores) != 0 {
		t.Errorf("want empty map, got %d entries", len(scores))
	}
}

// TestComputeCoverageScores_LineGraph: on a 4-node line A-B-C-D the two
// middle nodes reach the rest more cheaply (harmonic reach) than the
// leaves, so B and C tie for the top (1.0 after normalization) and the
// leaves A, D tie below them.
func TestComputeCoverageScores_LineGraph(t *testing.T) {
	edges := []BridgeEdge{
		{A: "a", B: "b", Weight: 1.0},
		{A: "b", B: "c", Weight: 1.0},
		{A: "c", B: "d", Weight: 1.0},
	}
	s := ComputeCoverageScores(edges)
	assertInUnit(t, s)

	if math.Abs(s["b"]-s["c"]) > 1e-9 {
		t.Errorf("symmetry: b and c should tie, got b=%v c=%v", s["b"], s["c"])
	}
	if math.Abs(s["a"]-s["d"]) > 1e-9 {
		t.Errorf("symmetry: a and d should tie, got a=%v d=%v", s["a"], s["d"])
	}
	if !(s["b"] > s["a"]) {
		t.Errorf("middle b should out-cover leaf a: b=%v a=%v", s["b"], s["a"])
	}
	if math.Abs(maxScoreValue(s)-1.0) > 1e-9 {
		t.Errorf("normalization: max should be 1.0, got %v", maxScoreValue(s))
	}
}

// TestComputeCoverageScores_Star: the hub of a star reaches every leaf in
// one hop and is the unique top scorer; the leaves tie below it (each
// reaches the hub directly and every other leaf via the hub).
func TestComputeCoverageScores_Star(t *testing.T) {
	edges := []BridgeEdge{
		{A: "s", B: "l1", Weight: 1.0},
		{A: "s", B: "l2", Weight: 1.0},
		{A: "s", B: "l3", Weight: 1.0},
	}
	s := ComputeCoverageScores(edges)
	assertInUnit(t, s)

	if math.Abs(s["s"]-1.0) > 1e-9 {
		t.Errorf("hub should score 1.0, got %v", s["s"])
	}
	for _, leaf := range []string{"l1", "l2", "l3"} {
		if !(s[leaf] < s["s"]) {
			t.Errorf("leaf %q should cover less than the hub: %v vs %v", leaf, s[leaf], s["s"])
		}
	}
	if math.Abs(s["l1"]-s["l2"]) > 1e-9 || math.Abs(s["l2"]-s["l3"]) > 1e-9 {
		t.Errorf("leaves should tie: %v %v %v", s["l1"], s["l2"], s["l3"])
	}
}

// TestComputeCoverageScores_Disconnected: harmonic reach must treat
// unreachable nodes as 0 contribution. With two separate 2-node
// components every node reaches exactly one peer at distance 1, so all
// four tie (and normalize to 1.0). If unreachable nodes leaked in, the
// symmetry would break.
func TestComputeCoverageScores_Disconnected(t *testing.T) {
	edges := []BridgeEdge{
		{A: "a", B: "b", Weight: 1.0},
		{A: "c", B: "d", Weight: 1.0},
	}
	s := ComputeCoverageScores(edges)
	assertInUnit(t, s)
	if len(s) != 4 {
		t.Fatalf("want 4 nodes, got %d", len(s))
	}
	for _, n := range []string{"a", "b", "c", "d"} {
		if math.Abs(s[n]-1.0) > 1e-9 {
			t.Errorf("node %q: want 1.0 (each reaches one peer), got %v", n, s[n])
		}
	}
}

// TestComputeCoverageScores_WeightSensitive: a stronger edge is a shorter
// distance, so the node bridging both a strong and a weak edge reaches the
// most. A-B weight 1.0 (near), A-C weight 0.1 (far) ⇒ A out-covers B
// out-covers C. Flip the 1/w distance convention and this inverts.
func TestComputeCoverageScores_WeightSensitive(t *testing.T) {
	edges := []BridgeEdge{
		{A: "a", B: "b", Weight: 1.0},
		{A: "a", B: "c", Weight: 0.1},
	}
	s := ComputeCoverageScores(edges)
	assertInUnit(t, s)
	if !(s["a"] > s["b"] && s["b"] > s["c"]) {
		t.Errorf("want a > b > c, got a=%v b=%v c=%v", s["a"], s["b"], s["c"])
	}
}

// --- shared test helpers; assertInUnit and maxScoreValue are both used by
// redundancy_score_test.go as well as the coverage tests above. ---

func assertInUnit(t *testing.T, m map[string]float64) {
	t.Helper()
	for k, v := range m {
		if v < 0 || v > 1 || math.IsNaN(v) || math.IsInf(v, 0) {
			t.Errorf("score %q=%v out of [0,1]", k, v)
		}
	}
}

// maxScoreValue avoids shadowing the Go 1.21 `max` builtin (#1762 nit).
func maxScoreValue(m map[string]float64) float64 {
	largest := 0.0
	for _, v := range m {
		if v > largest {
			largest = v
		}
	}
	return largest
}
