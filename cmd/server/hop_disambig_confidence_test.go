package main

import (
	"testing"
	"time"
)

// Issue #1229 (Option C): edge source-diversity confidence weighting.
//
// The tier-1 affinity scorer must demote edges contributed by a single
// observer relative to edges corroborated by multiple distinct observers.
// Without this guard, one observer with a chatty link can dominate the
// global graph and force resolution to the "wrong" candidate in a region
// it doesn't actually cover.
//
// Fixture (two "8a" candidates from the same anchor's neighborhood):
//   candX: 25 contributions from 1 observer (single-source, suspect)
//   candY: 30 contributions from 6 distinct observers (corroborated)
//
// Raw count score:
//   candX score ≈ 0.25, candY score ≈ 0.30 — ratio ≈ 1.2× (below 3×, falls
//   through to tier 2). Without confidence weighting tier 2 would pick
//   candX because we placed it geo-near the anchor — exactly the
//   cross-region pollution failure mode described in the issue.
//
// Confidence-weighted score (multiplier = min(1, |observers|/3)):
//   candX = 0.25 × (1/3)  ≈ 0.083
//   candY = 0.30 × 1.0    = 0.30
//   ratio ≈ 3.6× — clears affinityConfidenceRatio, tier-1 returns candY
//   with method "neighbor_affinity".

func seedAffinityFromObservers(g *NeighborGraph, anchor, candPK, prefix string, observers []string, perObserver int) {
	now := time.Now()
	step := 0
	for _, obs := range observers {
		for i := 0; i < perObserver; i++ {
			g.upsertEdge(anchor, candPK, prefix, obs, nil, now.Add(-time.Duration(step)*time.Minute))
			step++
		}
	}
}

func TestResolveWithContext_Tier1_ConfidencePrefersMultiObserverEdge(t *testing.T) {
	nodes := []nodeInfo{
		// candX: placed near the anchor so tier-2 (geo) would pick it.
		{PublicKey: "8aaaaaaaaaaa", Role: "repeater", Name: "candX", HasGPS: true, Lat: 34.06, Lon: -118.26},
		// candY: far from anchor; only source-diversity confidence rescues it.
		{PublicKey: "8abbbbbbbbbb", Role: "repeater", Name: "candY", HasGPS: true, Lat: 47.6, Lon: -122.3},
		{PublicKey: "ffeeeeeeeeee", Role: "repeater", Name: "anchor", HasGPS: true, Lat: 34.05, Lon: -118.25},
	}
	anchor := "ffeeeeeeeeee"

	g := NewNeighborGraph()
	// candX: 1 observer × 25 obs → single-source, demoted to 1/3 weight.
	seedAffinityFromObservers(g, anchor, "8aaaaaaaaaaa", "8a",
		[]string{"obs1"}, 25)
	// candY: 6 distinct observers × 5 obs each = 30 obs → full weight.
	seedAffinityFromObservers(g, anchor, "8abbbbbbbbbb", "8a",
		[]string{"obs1", "obs2", "obs3", "obs4", "obs5", "obs6"}, 5)

	pm := buildPrefixMap(nodes)
	r, method, score := pm.resolveWithContext("8a", []string{anchor}, g)
	if r == nil {
		t.Fatal("expected non-nil candidate")
	}
	if r.Name != "candY" {
		t.Fatalf("want candY (corroborated by 6 observers); got %s via %s score=%v",
			r.Name, method, score)
	}
	if method != "neighbor_affinity" {
		t.Fatalf("want method=neighbor_affinity (confidence-weighted tier 1); got %s", method)
	}
}

// Sanity gate on the source-diversity counter itself: repeated contributions
// from the same observer must NOT inflate the observer-set count, but
// contributions from new observers must increment it.
func TestNeighborEdge_ObserverSetIsDistinct(t *testing.T) {
	g := NewNeighborGraph()
	now := time.Now()
	// 10 contributions from obs1 — set size must stay 1.
	for i := 0; i < 10; i++ {
		g.upsertEdge("aa11", "bb22", "bb", "obs1", nil, now)
	}
	// 1 contribution each from obs2..obs4 — set size grows to 4.
	g.upsertEdge("aa11", "bb22", "bb", "obs2", nil, now)
	g.upsertEdge("aa11", "bb22", "bb", "obs3", nil, now)
	g.upsertEdge("aa11", "bb22", "bb", "obs4", nil, now)

	edges := g.Neighbors("aa11")
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge; got %d", len(edges))
	}
	e := edges[0]
	if len(e.Observers) != 4 {
		t.Fatalf("expected 4 distinct observers; got %d (%v)", len(e.Observers), e.Observers)
	}
	if e.Count != 13 {
		t.Fatalf("expected count=13 (10+3); got %d", e.Count)
	}
	if got := e.Confidence(); got != 1.0 {
		t.Fatalf("Confidence() with 4 observers: want 1.0 (saturated); got %v", got)
	}
	// Single-observer edge must report degraded confidence.
	g.upsertEdge("aa11", "cc33", "cc", "obs1", nil, now)
	for _, ee := range g.Neighbors("aa11") {
		if ee.NodeA == "cc33" || ee.NodeB == "cc33" {
			if got := ee.Confidence(); got >= 1.0 || got <= 0 {
				t.Fatalf("Confidence() single-observer: want in (0,1); got %v", got)
			}
		}
	}
}
