package main

import (
	"math"
	"testing"
)

// TestUsefulnessWeightsSumToOne: the four axis weights must form a convex
// combination so the composite stays in [0,1].
func TestUsefulnessWeightsSumToOne(t *testing.T) {
	sum := usefulnessWeightBridge + usefulnessWeightCoverage +
		usefulnessWeightRedundancy + usefulnessWeightTraffic
	if math.Abs(sum-1.0) > 1e-9 {
		t.Errorf("axis weights must sum to 1.0, got %v", sum)
	}
}

// TestCompositeUsefulness_Extremes: all-1 axes give 1.0 / grade A; all-0
// give 0.0 / grade F.
func TestCompositeUsefulness_Extremes(t *testing.T) {
	if score, grade := compositeUsefulness(usefulnessAxes{1, 1, 1, 1}, true); math.Abs(score-1.0) > 1e-9 || grade != "A" {
		t.Errorf("all-ones: want 1.0/A, got %v/%s", score, grade)
	}
	// All-zero with axes NOT yet computed is the cold-start / no-signal case:
	// score 0 with an EMPTY grade (not "F"), so callers can omit the field
	// instead of showing a misleading failing grade at boot.
	if score, grade := compositeUsefulness(usefulnessAxes{0, 0, 0, 0}, false); score != 0 || grade != "" {
		t.Errorf("all-zeros cold-start: want 0.0 and empty grade, got %v/%q", score, grade)
	}
	// All-zero AFTER the recomputers ran is a genuinely isolated repeater: a
	// real, deserved "F" — not hidden (#1762 MAJOR-4).
	if score, grade := compositeUsefulness(usefulnessAxes{0, 0, 0, 0}, true); score != 0 || grade != "F" {
		t.Errorf("all-zeros computed: want 0.0 and grade F, got %v/%q", score, grade)
	}
	// A single non-zero axis is NOT cold-start — it grades normally regardless
	// of the computed flag.
	if _, grade := compositeUsefulness(usefulnessAxes{0, 0, 0, 0.0001}, false); grade == "" {
		t.Error("a non-zero axis should produce a non-empty grade")
	}
}

// TestCompositeUsefulness_Weighting: a single axis at 1.0 contributes
// exactly its weight. Bridge alone ⇒ 0.30; traffic alone ⇒ 0.20.
func TestCompositeUsefulness_Weighting(t *testing.T) {
	if score, _ := compositeUsefulness(usefulnessAxes{0, 1, 0, 0}, true); math.Abs(score-usefulnessWeightBridge) > 1e-9 {
		t.Errorf("bridge-only: want %v, got %v", usefulnessWeightBridge, score)
	}
	if score, _ := compositeUsefulness(usefulnessAxes{1, 0, 0, 0}, true); math.Abs(score-usefulnessWeightTraffic) > 1e-9 {
		t.Errorf("traffic-only: want %v, got %v", usefulnessWeightTraffic, score)
	}
	if score, _ := compositeUsefulness(usefulnessAxes{0, 0, 1, 0}, true); math.Abs(score-usefulnessWeightCoverage) > 1e-9 {
		t.Errorf("coverage-only: want %v, got %v", usefulnessWeightCoverage, score)
	}
	if score, _ := compositeUsefulness(usefulnessAxes{0, 0, 0, 1}, true); math.Abs(score-usefulnessWeightRedundancy) > 1e-9 {
		t.Errorf("redundancy-only: want %v, got %v", usefulnessWeightRedundancy, score)
	}
}

// TestCompositeUsefulness_Clamps: out-of-range axis inputs are clamped to
// [0,1] before weighting, so the composite cannot escape the unit
// interval.
func TestCompositeUsefulness_Clamps(t *testing.T) {
	// negative traffic clamps to 0, bridge>1 clamps to 1 ⇒ only bridge's
	// weight contributes.
	score, grade := compositeUsefulness(usefulnessAxes{-5, 2, 0, 0}, true)
	if math.Abs(score-usefulnessWeightBridge) > 1e-9 {
		t.Errorf("clamped: want %v, got %v", usefulnessWeightBridge, score)
	}
	if grade != "C" { // 0.30 ≥ gradeC threshold
		t.Errorf("clamped grade: want C at score %v, got %s", score, grade)
	}
}

// TestUsefulnessGrade_Thresholds: each grade boundary maps to the expected
// letter (inclusive lower bound).
func TestUsefulnessGrade_Thresholds(t *testing.T) {
	cases := []struct {
		score float64
		want  string
	}{
		{usefulnessGradeA, "A"},
		{usefulnessGradeA - 1e-9, "B"},
		{usefulnessGradeB, "B"},
		{usefulnessGradeB - 1e-9, "C"},
		{usefulnessGradeC, "C"},
		{usefulnessGradeC - 1e-9, "D"},
		{usefulnessGradeD, "D"},
		{usefulnessGradeD - 1e-9, "F"},
		{0, "F"},
		{1, "A"},
	}
	for _, c := range cases {
		if got := usefulnessGrade(c.score); got != c.want {
			t.Errorf("grade(%v): want %s, got %s", c.score, c.want, got)
		}
	}
}

// TestClamp01: bounds enforcement.
func TestClamp01(t *testing.T) {
	for _, c := range []struct{ in, want float64 }{
		{-1, 0}, {0, 0}, {0.5, 0.5}, {1, 1}, {2, 1},
	} {
		if got := clamp01(c.in); got != c.want {
			t.Errorf("clamp01(%v): want %v, got %v", c.in, c.want, got)
		}
	}
}

func TestMaxFloat(t *testing.T) {
	if v := maxFloat(nil); v != 0 {
		t.Errorf("maxFloat(nil): want 0, got %v", v)
	}
	if v := maxFloat(map[string]float64{"a": 0.05, "b": 0.4, "c": 0.1}); v != 0.4 {
		t.Errorf("maxFloat: want 0.4, got %v", v)
	}
}

// TestEnrichNodeUsefulness_TrafficNormalization is the regression guard for
// the #1762 BLOCKER: the exposed traffic_share_score must stay the RAW share,
// while the composite must use the max-NORMALIZED traffic so the 0.20 weight
// is fully realized (not collapsed to ~0.01 by a tiny raw fraction).
func TestEnrichNodeUsefulness_TrafficNormalization(t *testing.T) {
	node := map[string]interface{}{}
	// Raw share 0.05, but it IS the population max → normalized 1.0.
	enrichNodeUsefulness(node, 0.05, usefulnessAxes{1.0, 0, 0, 0}, true)

	if node["traffic_share_score"] != 0.05 {
		t.Errorf("traffic_share_score should be the RAW 0.05, got %v", node["traffic_share_score"])
	}
	// Composite = 0.20 * normalized(1.0) = 0.20, NOT 0.20*0.05 = 0.01.
	if got, _ := node["usefulness_score"].(float64); math.Abs(got-usefulnessWeightTraffic) > 1e-9 {
		t.Errorf("composite should be the full traffic weight %v, got %v", usefulnessWeightTraffic, got)
	}
	if node["usefulness_grade"] == nil {
		t.Error("a node with non-zero traffic should carry a grade")
	}
}

// TestEnrichNodeUsefulness_ColdStartOmitsGrade: all-zero axes BEFORE the
// recomputers have run (axesComputed=false) → no usefulness_grade field (cold
// start), not a misleading "F".
func TestEnrichNodeUsefulness_ColdStartOmitsGrade(t *testing.T) {
	node := map[string]interface{}{}
	enrichNodeUsefulness(node, 0, usefulnessAxes{0, 0, 0, 0}, false)
	if _, ok := node["usefulness_grade"]; ok {
		t.Errorf("usefulness_grade should be omitted on cold-start, got %v", node["usefulness_grade"])
	}
	if node["usefulness_score"] != float64(0) {
		t.Errorf("usefulness_score should be 0 on cold-start, got %v", node["usefulness_score"])
	}
}

// TestEnrichNodeUsefulness_IsolatedNodeGetsF: all-zero axes AFTER the
// recomputers have run (axesComputed=true) is a genuinely isolated repeater —
// it MUST surface a real "F" rather than have its grade withheld (#1762
// MAJOR-4).
func TestEnrichNodeUsefulness_IsolatedNodeGetsF(t *testing.T) {
	node := map[string]interface{}{}
	enrichNodeUsefulness(node, 0, usefulnessAxes{0, 0, 0, 0}, true)
	if g, ok := node["usefulness_grade"]; !ok || g != "F" {
		t.Errorf("isolated node (axes computed) should grade F, got %v ok=%v", g, ok)
	}
	if node["usefulness_score"] != float64(0) {
		t.Errorf("usefulness_score should be 0 for isolated node, got %v", node["usefulness_score"])
	}
}
