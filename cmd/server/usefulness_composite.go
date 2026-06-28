// Package main: composite repeater usefulness score + letter grade
// (issue #672). Combines the four per-axis scores — each already in
// [0, 1] — into a single weighted score, plus an A–F grade for at-a-
// glance ranking.
//
// Weights (sum = 1.0) follow the #672 proposal:
//
//	Bridge      0.30  structural betweenness (chokepoint)
//	Coverage    0.25  harmonic reach (how much of the mesh it reaches)
//	Redundancy  0.25  irreplaceability (fragmentation on removal)
//	Traffic     0.20  observed relayed load
//
// All four inputs are expected to be max-normalized over the current
// repeater population (top node = 1.0): Bridge/Coverage/Redundancy by their
// Compute* functions, and Traffic by the caller dividing the raw share by the
// population max BEFORE calling here (#1762 review — without this, the raw
// traffic share (~0.02–0.05) collapsed its 0.20 weight to a negligible
// contribution). The exposed `traffic_share_score` field keeps the RAW share;
// only the composite uses the normalized form.
package main

// #672 composite weights. Exposed as named constants so the maintainer can
// retune without hunting through the arithmetic. Must sum to 1.0.
const (
	usefulnessWeightBridge     = 0.30
	usefulnessWeightCoverage   = 0.25
	usefulnessWeightRedundancy = 0.25
	usefulnessWeightTraffic    = 0.20
)

// Grade thresholds on the composite score. These are a first-cut calibration,
// NOT empirically tuned against a labelled dataset: with all four axes
// max-normalized, the single most-important repeater approaches 1.0, so the
// bands are placed to spread the realistic mid-field — a node strong on two of
// the three .25–.30 structural axes clears B (~0.45), one dominant axis clears
// C (~0.30), and peripheral/low-traffic nodes fall to D/F. Revisit once score
// distributions from real meshes are available; they are named constants
// precisely so retuning is a one-line change.
//
// FOLLOW-UP: a separate tuning issue should be opened to recalibrate these
// bands against observed real-mesh score histograms (cannot be filed from
// here); until then treat the letter grade as a coarse first impression and
// rank on the numeric usefulness_score for anything precise.
const (
	usefulnessGradeA = 0.65
	usefulnessGradeB = 0.45
	usefulnessGradeC = 0.30
	usefulnessGradeD = 0.15
)

// usefulnessAxes bundles the four #672 axis scores (each already in [0,1])
// so callers pass them BY NAME rather than as four adjacent, easily-swapped
// float64 arguments (#1762 review). Traffic here is the max-normalized share
// used in the composite, not the raw traffic_share_score.
type usefulnessAxes struct {
	Traffic    float64
	Bridge     float64
	Coverage   float64
	Redundancy float64
}

// compositeUsefulness combines the four axis scores into a weighted
// composite in [0, 1] and its letter grade. Inputs are clamped defensively
// to [0, 1]; out-of-range axis values cannot push the composite outside
// the unit interval.
//
// The all-zero case is intentionally ambiguous and `axesComputed` disambiguates
// it (#1762 MAJOR-4):
//
//   - axesComputed == false → the recomputers have not populated any snapshot
//     yet (the first ~5 min after boot). All-zero then means "no signal YET",
//     so the grade is "" (empty) and callers omit usefulness_grade rather than
//     flash a misleading boot-time "F".
//   - axesComputed == true → the recomputers HAVE run and genuinely scored this
//     node zero on every axis (a fully isolated / unreached repeater). That is a
//     real, deserved "F" and is returned as such — not hidden.
//
// A node with any non-zero axis is graded normally regardless of the flag.
func compositeUsefulness(ax usefulnessAxes, axesComputed bool) (float64, string) {
	t, b, c, r := clamp01(ax.Traffic), clamp01(ax.Bridge), clamp01(ax.Coverage), clamp01(ax.Redundancy)
	score := usefulnessWeightBridge*b +
		usefulnessWeightCoverage*c +
		usefulnessWeightRedundancy*r +
		usefulnessWeightTraffic*t
	score = clamp01(score)
	if t == 0 && b == 0 && c == 0 && r == 0 && !axesComputed {
		// Cold start: no axes computed yet — withhold the grade.
		return 0, ""
	}
	return score, usefulnessGrade(score)
}

// usefulnessGrade maps a composite score in [0, 1] to an A–F letter grade.
func usefulnessGrade(score float64) string {
	switch {
	case score >= usefulnessGradeA:
		return "A"
	case score >= usefulnessGradeB:
		return "B"
	case score >= usefulnessGradeC:
		return "C"
	case score >= usefulnessGradeD:
		return "D"
	default:
		return "F"
	}
}

// clamp01 bounds v to [0, 1].
func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// maxFloat returns the largest value in m, or 0 for an empty map. The local
// is named `largest` (not `max`) to avoid shadowing the Go 1.21 builtin —
// consistent with maxScoreValue in coverage_score_test.go (#1762 nit).
func maxFloat(m map[string]float64) float64 {
	largest := 0.0
	for _, v := range m {
		if v > largest {
			largest = v
		}
	}
	return largest
}

// enrichNodeUsefulness writes the four #672 axes + composite + grade onto a
// node map (shared by the node-list and node-detail handlers). trafficRaw is
// the Traffic-axis share exposed verbatim as traffic_share_score; ax holds the
// composite inputs — its Traffic is the max-normalized share (across the
// repeater population) so all four axes contribute on a comparable [0,1]
// scale. axesComputed reports whether the structural-axis recomputers have
// produced a snapshot yet; it only matters for the all-zero node, where it
// distinguishes cold start (grade withheld) from a genuinely isolated repeater
// (real "F") — see compositeUsefulness. The usefulness_grade field is omitted
// only when the grade is empty (cold start).
func enrichNodeUsefulness(node map[string]interface{}, trafficRaw float64, ax usefulnessAxes, axesComputed bool) {
	if node == nil {
		return
	}
	node["traffic_share_score"] = trafficRaw
	node["bridge_score"] = ax.Bridge
	node["coverage_score"] = ax.Coverage
	node["redundancy_score"] = ax.Redundancy
	composite, grade := compositeUsefulness(ax, axesComputed)
	node["usefulness_score"] = composite
	if grade != "" {
		node["usefulness_grade"] = grade
	}
}
