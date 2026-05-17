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

// Standard fixture shared by most tier-1 tests: two "72" candidates and
// (when needed) an anchor pubkey co-located with candY. candX is far
// (Seattle), candY is near LA — so geo proximity to anchor picks candY
// unless tier-1 fires for candX.
var tier1StdNodes = []nodeInfo{
	{PublicKey: "72aaaaaaaaaa", Role: "repeater", Name: "candX", HasGPS: true, Lat: 47.6, Lon: -122.3},   // Seattle (far)
	{PublicKey: "72bbbbbbbbbb", Role: "repeater", Name: "candY", HasGPS: true, Lat: 34.05, Lon: -118.25}, // LA (near anchor)
	{PublicKey: "ffeeeeeeeeee", Role: "repeater", Name: "anchor", HasGPS: true, Lat: 34.1, Lon: -118.3},
}

const tier1Anchor = "ffeeeeeeeeee"

// ─── sub-task 1: tier-1 explicit tests (table-driven) ──────────────────────────

// TestResolveWithContext_Tier1 collapses what were five near-identical
// per-branch functions into one table-driven test. Each row exercises
// exactly one tier-1 branch (strong-pick X, strong-pick Y, ambiguous-skip,
// tier-1-beats-tier-2, fall-throughs). Adding a new tier-1 case is a
// one-line addition.
//
// Mirror-pair rows (StrongAffinityPicksX / PicksY) prevent a "tier-1 always
// returns first candidate" tautology — the score MUST be consulted because
// flipping the weights flips the winner.
func TestResolveWithContext_Tier1(t *testing.T) {
	type seed struct {
		obsPK, candPK, prefix string
		count                 int
	}
	cases := []struct {
		name           string
		nodes          []nodeInfo
		ctxPK          string
		useNilGraph    bool      // skip graph entirely (tests `graph != nil` guard)
		seeds          []seed    // tier-1 affinity seeds
		markAmbiguous  [2]string // if non-empty pair, mark that edge ambiguous
		extraGraphSeed *seed     // seed unrelated to ctxPK (empty-for-context fixture)
		wantName       string
		wantMethod     string
	}{
		{
			name:       "StrongAffinityPicksX",
			nodes:      []nodeInfo{{PublicKey: "72aaaaaaaaaa", Role: "repeater", Name: "candX", HasGPS: true, Lat: 35.3, Lon: -120.7}, {PublicKey: "72bbbbbbbbbb", Role: "repeater", Name: "candY", HasGPS: true, Lat: 34.0, Lon: -118.2}},
			ctxPK:      "ccccccccccc1",
			seeds:      []seed{{"ccccccccccc1", "72aaaaaaaaaa", "72", 100}, {"ccccccccccc1", "72bbbbbbbbbb", "72", 1}},
			wantName:   "candX",
			wantMethod: "neighbor_affinity",
		},
		{
			name:       "StrongAffinityPicksY",
			nodes:      []nodeInfo{{PublicKey: "72aaaaaaaaaa", Role: "repeater", Name: "candX", HasGPS: true, Lat: 35.3, Lon: -120.7}, {PublicKey: "72bbbbbbbbbb", Role: "repeater", Name: "candY", HasGPS: true, Lat: 34.0, Lon: -118.2}},
			ctxPK:      "ccccccccccc1",
			seeds:      []seed{{"ccccccccccc1", "72aaaaaaaaaa", "72", 1}, {"ccccccccccc1", "72bbbbbbbbbb", "72", 100}},
			wantName:   "candY",
			wantMethod: "neighbor_affinity",
		},
		{
			// Strong edge to candX exists but is flagged Ambiguous → tier 1
			// must skip it and tier 2 (geo) picks candY (near anchor).
			name:          "AmbiguousEdgeSkipsToTier2",
			nodes:         tier1StdNodes,
			ctxPK:         tier1Anchor,
			seeds:         []seed{{tier1Anchor, "72aaaaaaaaaa", "72", 100}},
			markAmbiguous: [2]string{tier1Anchor, "72aaaaaaaaaa"},
			wantName:      "candY",
			wantMethod:    "geo_proximity",
		},
		{
			// candX is far (affinity), candY is geo-close. Tier 1 firing
			// → candX wins. Sentinel for "geo branch hit first" regressions.
			name:       "BeatsTier2WhenBothSignal",
			nodes:      tier1StdNodes,
			ctxPK:      tier1Anchor,
			seeds:      []seed{{tier1Anchor, "72aaaaaaaaaa", "72", 100}},
			wantName:   "candX",
			wantMethod: "neighbor_affinity",
		},
		{
			// Graph is non-nil but has no edges involving the context.
			// Tier 1 must short-circuit; tier 2 picks candY.
			name:           "EmptyGraphFallsThrough",
			nodes:          tier1StdNodes,
			ctxPK:          tier1Anchor,
			extraGraphSeed: &seed{"aaaaaaaaaaa1", "aaaaaaaaaaa2", "aa", 10},
			wantName:       "candY",
			wantMethod:     "geo_proximity",
		},
		{
			// Graph is nil — `graph != nil` short-circuit; tier 2 decides.
			name:        "NilGraphFallsThrough",
			nodes:       tier1StdNodes,
			ctxPK:       tier1Anchor,
			useNilGraph: true,
			wantName:    "candY",
			wantMethod:  "geo_proximity",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			pm := buildPrefixMap(tc.nodes)
			var g *NeighborGraph
			if !tc.useNilGraph {
				g = NewNeighborGraph()
				for _, s := range tc.seeds {
					seedAffinity(g, s.obsPK, s.candPK, s.prefix, "obs1", s.count)
				}
				if tc.extraGraphSeed != nil {
					s := *tc.extraGraphSeed
					seedAffinity(g, s.obsPK, s.candPK, s.prefix, "obs1", s.count)
				}
				if tc.markAmbiguous[0] != "" {
					// Use the public helper rather than mutating
					// *NeighborEdge fields returned from AllEdges() —
					// hardens the test against any future change that
					// makes AllEdges() return copies.
					if !g.MarkAmbiguous(tc.markAmbiguous[0], tc.markAmbiguous[1], true) {
						t.Fatalf("MarkAmbiguous(%s,%s): edge not found", tc.markAmbiguous[0], tc.markAmbiguous[1])
					}
				}
			}

			r, method, _ := pm.resolveWithContext("72", []string{tc.ctxPK}, g)
			if r == nil {
				t.Fatal("expected non-nil candidate")
			}
			if r.Name != tc.wantName {
				t.Fatalf("name: want %s got %s (method=%s)", tc.wantName, r.Name, method)
			}
			if method != tc.wantMethod {
				t.Fatalf("method: want %s got %s", tc.wantMethod, method)
			}
		})
	}
}

// TestResolveWithContext_Tier1_ScoresTooCloseFallsThrough: best.score is
// below affinityConfidenceRatio × runner-up.score (the ratio guard at the
// end of the tier-1 block in resolveWithContext). Resolver must fall
// through to tier 2.
//
// This case is kept SEPARATE from the table above because it asserts an
// extra invariant the others don't: the returned `score` field MUST be 0
// (tier-2 geo path returns score=0 in store.go). Pinning score==0 makes
// the test fail loudly if affinityConfidenceRatio is ever lowered to a
// value (≤1.25) where the 10/8 count ratio would actually clear tier 1 —
// at that point the resolver would return a non-zero affinity score and
// this assertion catches it, even before the wantMethod string check.
func TestResolveWithContext_Tier1_ScoresTooCloseFallsThrough(t *testing.T) {
	pm := buildPrefixMap(tier1StdNodes)
	g := NewNeighborGraph()
	// Both above affinityMinObservations, but within 3× of each other →
	// ratio guard fails, fall-through expected.
	seedAffinity(g, tier1Anchor, "72aaaaaaaaaa", "72", "obs1", 10)
	seedAffinity(g, tier1Anchor, "72bbbbbbbbbb", "72", "obs1", 8)

	r, method, score := pm.resolveWithContext("72", []string{tier1Anchor}, g)
	if r == nil {
		t.Fatal("expected non-nil candidate")
	}
	// Direct pin on score==0: catches a lowered affinityConfidenceRatio
	// constant that would let 10/8 clear the ratio guard and return a
	// non-zero affinity score.
	if score != 0 {
		t.Fatalf("expected tier-2 fall-through (score==0); got score=%f via %s — affinityConfidenceRatio (%v) may have been lowered to admit a 1.25× ratio",
			score, method, affinityConfidenceRatio)
	}
	if method == "neighbor_affinity" {
		t.Fatalf("tier 1 must fall through when scores are too close (< %v ratio); got method=%s",
			affinityConfidenceRatio, method)
	}
	if r.Name != "candY" {
		t.Fatalf("expected tier-2 geo to pick candY; got %s via %s", r.Name, method)
	}
}
