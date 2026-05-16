package main

import (
	"testing"
	"time"
)

// Regression coverage for the hop disambiguator's tier-1 (neighbor affinity)
// path of pm.resolveWithContext. Issue #1201: tier 1 is the strongest
// disambiguation signal but was untested by any test we shipped — only
// upstream tests (that predate the context-plumbing fix in #1198) exercised
// it. These tests pin tier-1 behavior so any future refactor that disables
// tier 1, reorders priorities, or drops the Ambiguous-edge guard will fail.
//
// Naming convention for fixture pubkeys: lowercase hex placeholders only;
// no real observer/operator handles (per AGENTS.md PII rules).

// ─── helpers ───────────────────────────────────────────────────────────────────

// seedAffinity adds n observations of an edge between obsPK and candPK at
// recent timestamps. Count ≥ affinityMinObservations is required for tier 1
// to consider an edge.
func seedAffinity(g *NeighborGraph, obsPK, candPK, prefix, observer string, n int) {
	now := time.Now()
	for i := 0; i < n; i++ {
		g.upsertEdge(obsPK, candPK, prefix, observer, nil, now.Add(-time.Duration(i)*time.Minute))
	}
}

// ─── sub-task 1: tier-1 explicit tests ─────────────────────────────────────────

// TestResolveWithContext_Tier1_StrongAffinityPicksX seeds a strong edge to X
// and a weak (below threshold) edge to Y. Tier 1 must pick X.
func TestResolveWithContext_Tier1_StrongAffinityPicksX(t *testing.T) {
	nodes := []nodeInfo{
		{PublicKey: "72aaaaaaaaaa", Role: "repeater", Name: "candX", HasGPS: true, Lat: 35.3, Lon: -120.7},
		{PublicKey: "72bbbbbbbbbb", Role: "repeater", Name: "candY", HasGPS: true, Lat: 34.0, Lon: -118.2},
	}
	pm := buildPrefixMap(nodes)

	ctxPK := "ccccccccccc1" // anchor / context pubkey
	g := NewNeighborGraph()
	seedAffinity(g, ctxPK, "72aaaaaaaaaa", "72", "obs1", 100) // strong X
	seedAffinity(g, ctxPK, "72bbbbbbbbbb", "72", "obs1", 1)   // below affinityMinObservations

	r, method, score := pm.resolveWithContext("72", []string{ctxPK}, g)
	if r == nil {
		t.Fatal("expected non-nil candidate")
	}
	if r.Name != "candX" {
		t.Fatalf("tier-1 should pick candX (strong affinity); got %s via %s score=%f", r.Name, method, score)
	}
	if method != "neighbor_affinity" {
		t.Fatalf("expected neighbor_affinity, got %s", method)
	}
}

// TestResolveWithContext_Tier1_StrongAffinityPicksY reverses the weights to
// prove the score is actually consulted (not a constant returning X).
func TestResolveWithContext_Tier1_StrongAffinityPicksY(t *testing.T) {
	nodes := []nodeInfo{
		{PublicKey: "72aaaaaaaaaa", Role: "repeater", Name: "candX", HasGPS: true, Lat: 35.3, Lon: -120.7},
		{PublicKey: "72bbbbbbbbbb", Role: "repeater", Name: "candY", HasGPS: true, Lat: 34.0, Lon: -118.2},
	}
	pm := buildPrefixMap(nodes)

	ctxPK := "ccccccccccc1"
	g := NewNeighborGraph()
	seedAffinity(g, ctxPK, "72aaaaaaaaaa", "72", "obs1", 1)   // weak X
	seedAffinity(g, ctxPK, "72bbbbbbbbbb", "72", "obs1", 100) // strong Y

	r, method, _ := pm.resolveWithContext("72", []string{ctxPK}, g)
	if r == nil {
		t.Fatal("expected non-nil candidate")
	}
	if r.Name != "candY" {
		t.Fatalf("tier-1 should pick candY (strong affinity); got %s via %s", r.Name, method)
	}
	if method != "neighbor_affinity" {
		t.Fatalf("expected neighbor_affinity, got %s", method)
	}
}

// TestResolveWithContext_Tier1_AmbiguousEdgeSkipsToTier2 verifies that
// ambiguous edges are skipped in the tier-1 scan of resolveWithContext
// (the `if e.Ambiguous { continue }` guard inside the tier-1 candidate
// loop) and the resolver falls through to tier 2.
func TestResolveWithContext_Tier1_AmbiguousEdgeSkipsToTier2(t *testing.T) {
	// Two candidates for "72". Geo proximity will pick candY (close to anchor).
	nodes := []nodeInfo{
		{PublicKey: "72aaaaaaaaaa", Role: "repeater", Name: "candX", HasGPS: true, Lat: 47.6, Lon: -122.3}, // Seattle (far)
		{PublicKey: "72bbbbbbbbbb", Role: "repeater", Name: "candY", HasGPS: true, Lat: 34.05, Lon: -118.25}, // LA (near anchor)
		{PublicKey: "ffeeeeeeeeee", Role: "repeater", Name: "anchor", HasGPS: true, Lat: 34.1, Lon: -118.3}, // anchor in LA
	}
	pm := buildPrefixMap(nodes)

	ctxPK := "ffeeeeeeeeee"
	g := NewNeighborGraph()
	// Seed a strong edge that WOULD pick candX (Seattle) under tier 1, but
	// mark it Ambiguous → tier 1 must skip it.
	seedAffinity(g, ctxPK, "72aaaaaaaaaa", "72", "obs1", 100)
	for _, e := range g.AllEdges() {
		e.Ambiguous = true
	}

	r, method, _ := pm.resolveWithContext("72", []string{ctxPK}, g)
	if r == nil {
		t.Fatal("expected non-nil candidate")
	}
	if method == "neighbor_affinity" {
		t.Fatalf("tier 1 must skip ambiguous edges; got method=%s pubkey=%s", method, r.PublicKey)
	}
	if r.Name != "candY" {
		t.Fatalf("expected tier-2 to pick candY (geo-near anchor); got %s via %s", r.Name, method)
	}
	if method != "geo_proximity" {
		t.Fatalf("expected fall-through to geo_proximity, got %s", method)
	}
}

// ─── sub-task 2: tier ordering ─────────────────────────────────────────────────

// TestResolveWithContext_Tier1_BeatsTier2WhenBothSignal constructs a
// scenario where tier 1 (affinity) and tier 2 (geo) point at DIFFERENT
// candidates. Tier 1 must win. Catches any refactor that reorders priorities
// or accidentally hits the geo branch first.
func TestResolveWithContext_Tier1_BeatsTier2WhenBothSignal(t *testing.T) {
	// candX is far from the anchor but has strong graph affinity.
	// candY is geographically close to the anchor but has no graph affinity.
	// Tier 1 must pick candX.
	nodes := []nodeInfo{
		{PublicKey: "72aaaaaaaaaa", Role: "repeater", Name: "candX", HasGPS: true, Lat: 47.6, Lon: -122.3},  // Seattle
		{PublicKey: "72bbbbbbbbbb", Role: "repeater", Name: "candY", HasGPS: true, Lat: 34.05, Lon: -118.25}, // LA (near anchor)
		{PublicKey: "ffeeeeeeeeee", Role: "repeater", Name: "anchor", HasGPS: true, Lat: 34.1, Lon: -118.3},  // LA
	}
	pm := buildPrefixMap(nodes)

	ctxPK := "ffeeeeeeeeee"
	g := NewNeighborGraph()
	seedAffinity(g, ctxPK, "72aaaaaaaaaa", "72", "obs1", 100) // strong affinity to Seattle candidate

	r, method, _ := pm.resolveWithContext("72", []string{ctxPK}, g)
	if r == nil {
		t.Fatal("expected non-nil candidate")
	}
	if r.Name != "candX" {
		t.Fatalf("tier 1 must beat tier 2 when both signal; expected candX (affinity), got %s via %s", r.Name, method)
	}
	if method != "neighbor_affinity" {
		t.Fatalf("expected neighbor_affinity, got %s", method)
	}
}

// ─── sub-task 3: tier-1 fallbacks ──────────────────────────────────────────────

// TestResolveWithContext_Tier1_EmptyGraphFallsThrough: s.graph is non-nil
// but has NO edges involving the context pubkey. Tier 1 must quietly skip
// and tier 2 (geo) must decide.
func TestResolveWithContext_Tier1_EmptyGraphFallsThrough(t *testing.T) {
	nodes := []nodeInfo{
		{PublicKey: "72aaaaaaaaaa", Role: "repeater", Name: "candX", HasGPS: true, Lat: 47.6, Lon: -122.3},  // Seattle (far)
		{PublicKey: "72bbbbbbbbbb", Role: "repeater", Name: "candY", HasGPS: true, Lat: 34.05, Lon: -118.25}, // LA (near anchor)
		{PublicKey: "ffeeeeeeeeee", Role: "repeater", Name: "anchor", HasGPS: true, Lat: 34.1, Lon: -118.3},
	}
	pm := buildPrefixMap(nodes)

	ctxPK := "ffeeeeeeeeee"
	g := NewNeighborGraph() // empty
	// Add an unrelated edge so the graph isn't strictly empty, but no edge
	// for the context pubkey.
	seedAffinity(g, "aaaaaaaaaaa1", "aaaaaaaaaaa2", "aa", "obs1", 10)

	r, method, _ := pm.resolveWithContext("72", []string{ctxPK}, g)
	if r == nil {
		t.Fatal("expected non-nil candidate")
	}
	if method == "neighbor_affinity" {
		t.Fatalf("tier 1 must skip when graph has no edges for context; got method=%s", method)
	}
	if r.Name != "candY" {
		t.Fatalf("expected tier-2 geo to pick candY; got %s via %s", r.Name, method)
	}
}

// TestResolveWithContext_Tier1_NilGraphFallsThrough: graph is nil entirely.
// Tier 1 is short-circuited (`if graph != nil`) and tier 2 decides.
func TestResolveWithContext_Tier1_NilGraphFallsThrough(t *testing.T) {
	nodes := []nodeInfo{
		{PublicKey: "72aaaaaaaaaa", Role: "repeater", Name: "candX", HasGPS: true, Lat: 47.6, Lon: -122.3},
		{PublicKey: "72bbbbbbbbbb", Role: "repeater", Name: "candY", HasGPS: true, Lat: 34.05, Lon: -118.25},
		{PublicKey: "ffeeeeeeeeee", Role: "repeater", Name: "anchor", HasGPS: true, Lat: 34.1, Lon: -118.3},
	}
	pm := buildPrefixMap(nodes)

	r, method, _ := pm.resolveWithContext("72", []string{"ffeeeeeeeeee"}, nil)
	if r == nil {
		t.Fatal("expected non-nil candidate")
	}
	if method == "neighbor_affinity" {
		t.Fatalf("tier 1 must skip when graph is nil; got method=%s", method)
	}
	if r.Name != "candY" {
		t.Fatalf("expected tier-2 geo to pick candY; got %s via %s", r.Name, method)
	}
}

// TestResolveWithContext_Tier1_ScoresTooCloseFallsThrough: best.score is
// below affinityConfidenceRatio × runner-up.score (the ratio guard at the
// end of the tier-1 block in resolveWithContext).
// Resolver must fall through to tier 2.
func TestResolveWithContext_Tier1_ScoresTooCloseFallsThrough(t *testing.T) {
	nodes := []nodeInfo{
		{PublicKey: "72aaaaaaaaaa", Role: "repeater", Name: "candX", HasGPS: true, Lat: 47.6, Lon: -122.3},   // Seattle
		{PublicKey: "72bbbbbbbbbb", Role: "repeater", Name: "candY", HasGPS: true, Lat: 34.05, Lon: -118.25}, // LA (near anchor)
		{PublicKey: "ffeeeeeeeeee", Role: "repeater", Name: "anchor", HasGPS: true, Lat: 34.1, Lon: -118.3},
	}
	pm := buildPrefixMap(nodes)

	ctxPK := "ffeeeeeeeeee"
	g := NewNeighborGraph()
	// Both above affinityMinObservations, but within 3× of each other →
	// ratio guard fails, fall-through expected.
	seedAffinity(g, ctxPK, "72aaaaaaaaaa", "72", "obs1", 10)
	seedAffinity(g, ctxPK, "72bbbbbbbbbb", "72", "obs1", 8)

	r, method, _ := pm.resolveWithContext("72", []string{ctxPK}, g)
	if r == nil {
		t.Fatal("expected non-nil candidate")
	}
	if method == "neighbor_affinity" {
		t.Fatalf("tier 1 must fall through when scores are too close (< %v ratio); got method=%s",
			affinityConfidenceRatio, method)
	}
	if r.Name != "candY" {
		t.Fatalf("expected tier-2 geo to pick candY; got %s via %s", r.Name, method)
	}
}
