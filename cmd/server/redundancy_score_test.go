package main

import (
	"math"
	"testing"
)

// TestRedundancyMinWeight_PinnedToAffinityConstants pins redundancyMinWeight
// to its derivation from the affinity-tuning constants. A silent change to
// affinitySaturationCount or affinityObserverSaturation would shift the
// edge-weight floor; this trips CI rather than relying on the doc comment.
func TestRedundancyMinWeight_PinnedToAffinityConstants(t *testing.T) {
	want := (1.0 / 100.0) / 3.0
	if math.Abs(redundancyMinWeight-want) > 1e-12 {
		t.Fatalf("redundancyMinWeight = %v, want (1.0/100)/3 = %v; affinity constants changed?", redundancyMinWeight, want)
	}
	// Cross-check it still equals the live constant-derived expression.
	derived := (1.0 / float64(affinitySaturationCount)) / affinityObserverSaturation
	if math.Abs(redundancyMinWeight-derived) > 1e-12 {
		t.Fatalf("redundancyMinWeight = %v, derived = %v", redundancyMinWeight, derived)
	}
}

// TestComputeRedundancyScores_Empty: empty edge list yields a non-nil
// empty map.
func TestComputeRedundancyScores_Empty(t *testing.T) {
	scores := ComputeRedundancyScores(nil)
	if scores == nil {
		t.Fatal("want non-nil empty map, got nil")
	}
	if len(scores) != 0 {
		t.Errorf("want empty map, got %d entries", len(scores))
	}
}

// TestComputeRedundancyScores_Line: on a 5-node line A-B-C-D-E the centre
// C is the most critical cut vertex (removing it severs {A,B} from {D,E}
// = 4 disconnected pairs), B and D next (3 pairs each), and the leaves A,E
// are non-critical (0). Normalized: C=1.0, B=D=0.75, A=E=0.
func TestComputeRedundancyScores_Line(t *testing.T) {
	edges := []BridgeEdge{
		{A: "a", B: "b", Weight: 1.0},
		{A: "b", B: "c", Weight: 1.0},
		{A: "c", B: "d", Weight: 1.0},
		{A: "d", B: "e", Weight: 1.0},
	}
	s := ComputeRedundancyScores(edges)
	assertInUnit(t, s)

	if math.Abs(s["c"]-1.0) > 1e-9 {
		t.Errorf("centre c should be the most critical (1.0), got %v", s["c"])
	}
	for _, n := range []string{"b", "d"} {
		if math.Abs(s[n]-0.75) > 1e-9 {
			t.Errorf("near-centre %q should be 0.75, got %v", n, s[n])
		}
	}
	for _, leaf := range []string{"a", "e"} {
		if v, ok := s[leaf]; !ok || v != 0 {
			t.Errorf("leaf %q: want 0 present, got %v ok=%v", leaf, v, ok)
		}
	}
	// Max-normalization invariant: the most-critical node tops out at 1.0.
	if maxScoreValue(s) != 1.0 {
		t.Errorf("max redundancy should normalize to 1.0, got %v", maxScoreValue(s))
	}
}

// TestComputeRedundancyScores_Triangle: a 2-connected triangle has no cut
// vertex — every node is fully replaceable, so all score 0 (but are
// present in the map).
func TestComputeRedundancyScores_Triangle(t *testing.T) {
	edges := []BridgeEdge{
		{A: "x", B: "y", Weight: 1.0},
		{A: "y", B: "z", Weight: 1.0},
		{A: "z", B: "x", Weight: 1.0},
	}
	s := ComputeRedundancyScores(edges)
	assertInUnit(t, s)
	for _, n := range []string{"x", "y", "z"} {
		if v, ok := s[n]; !ok || v != 0 {
			t.Errorf("triangle node %q: want 0 present, got %v ok=%v", n, v, ok)
		}
	}
}

// TestComputeRedundancyScores_Star: the hub is the sole cut vertex; the
// leaves are non-critical. Hub normalizes to 1.0, leaves to 0.
func TestComputeRedundancyScores_Star(t *testing.T) {
	edges := []BridgeEdge{
		{A: "s", B: "l1", Weight: 1.0},
		{A: "s", B: "l2", Weight: 1.0},
		{A: "s", B: "l3", Weight: 1.0},
	}
	s := ComputeRedundancyScores(edges)
	assertInUnit(t, s)
	if math.Abs(s["s"]-1.0) > 1e-9 {
		t.Errorf("hub should be the most critical (1.0), got %v", s["s"])
	}
	for _, leaf := range []string{"l1", "l2", "l3"} {
		if s[leaf] != 0 {
			t.Errorf("leaf %q: want 0, got %v", leaf, s[leaf])
		}
	}
}

// TestComputeRedundancyScores_BridgedCliques: two triangles joined by a
// single bridge edge C-D. The two bridge endpoints are the critical cut
// vertices (each severs its own triangle's other two nodes from the far
// side: 2×3 = 6 disconnected pairs); all other nodes are non-critical.
// Both endpoints tie at 1.0.
func TestComputeRedundancyScores_BridgedCliques(t *testing.T) {
	edges := []BridgeEdge{
		// triangle 1: a,b,c
		{A: "a", B: "b", Weight: 1.0},
		{A: "b", B: "c", Weight: 1.0},
		{A: "c", B: "a", Weight: 1.0},
		// triangle 2: d,e,f
		{A: "d", B: "e", Weight: 1.0},
		{A: "e", B: "f", Weight: 1.0},
		{A: "f", B: "d", Weight: 1.0},
		// bridge
		{A: "c", B: "d", Weight: 1.0},
	}
	s := ComputeRedundancyScores(edges)
	assertInUnit(t, s)
	for _, crit := range []string{"c", "d"} {
		if math.Abs(s[crit]-1.0) > 1e-9 {
			t.Errorf("bridge endpoint %q should be critical (1.0), got %v", crit, s[crit])
		}
	}
	for _, n := range []string{"a", "b", "e", "f"} {
		if s[n] != 0 {
			t.Errorf("in-clique node %q should be non-critical (0), got %v", n, s[n])
		}
	}
}
