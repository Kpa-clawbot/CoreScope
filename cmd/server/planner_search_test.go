package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// ─── Synthetic terrain ─────────────────────────────────────────────────────

// Test geometry is in km east (x) / north (y) of a fixed origin.
const (
	ptLat0 = 37.0
	ptLon0 = -100.0
)

var ptCos0 = math.Cos(ptLat0 * math.Pi / 180)

func ptGeo(x, y float64) geoPoint {
	return geoPoint{Lat: ptLat0 + y/plannerKmPerDeg, Lon: ptLon0 + x/(plannerKmPerDeg*ptCos0)}
}

func ptXY(p geoPoint) (x, y float64) {
	return (p.Lon - ptLon0) * plannerKmPerDeg * ptCos0, (p.Lat - ptLat0) * plannerKmPerDeg
}

// ptBumps is a flat base plus Gaussian bumps (h may be negative: a
// pit). sy = 1e6 makes a ridge running north–south.
type ptBumps struct {
	base  float64
	bumps []ptBump
}

type ptBump struct{ x, y, h, sx, sy float64 }

func (t *ptBumps) ElevationAt(lat, lon float64) (float64, bool) {
	x, y := ptXY(geoPoint{lat, lon})
	e := t.base
	for _, b := range t.bumps {
		dx, dy := (x-b.x)/b.sx, (y-b.y)/b.sy
		e += b.h * math.Exp(-(dx*dx+dy*dy)/2)
	}
	return e, true
}

// ptWaves: cheap, hilly, many local maxima.
type ptWaves struct{}

func (ptWaves) ElevationAt(lat, lon float64) (float64, bool) {
	x, y := ptXY(geoPoint{lat, lon})
	return 300 + 140*math.Sin(x/3.1)*math.Cos(y/2.3) + 60*math.Sin(x/1.3+y/0.9) + 20*math.Cos(x/0.45-y/0.6), true
}

func ptNode(name string, x, y float64) plannerNode {
	p := ptGeo(x, y)
	return plannerNode{PubKey: "pk-" + name, Name: name, Role: "repeater", Lat: p.Lat, Lon: p.Lon, InArea: true}
}

func ptArea(x0, y0, x1, y1 float64) geoBBox {
	sw, ne := ptGeo(x0, y0), ptGeo(x1, y1)
	return geoBBox{S: sw.Lat, W: sw.Lon, N: ne.Lat, E: ne.Lon}
}

func ptParams() PlannerPlanParams {
	return PlannerPlanParams{
		Sites: 3, RangeKm: 30, NewAntennaM: 10, ExistingAntennaM: 30, Baseline: "predicted",
		FrequencyMHz: 869.525, KFactor: 4.0 / 3.0, MinClearance: plannerMinClearance,
	}
}

func ptPlan(t *testing.T, v terrainView, in planInputs) PlannerPlanResponse {
	t.Helper()
	resp, err := runCoveragePlan(context.Background(), v, in, nil)
	if err != nil {
		t.Fatalf("runCoveragePlan: %v", err)
	}
	return resp
}

// ─── Scenarios ─────────────────────────────────────────────────────────────

// ptTwoClusterCase: two 3-node clusters 30+ km apart behind two parallel
// north–south ridges (x = ±4 km, 500 m). Only the summit between them
// (0, 0, 800 m) sees over both ridges.
func ptTwoClusterCase() (terrainView, planInputs) {
	v := &ptBumps{base: 100, bumps: []ptBump{
		{x: -4, h: 400, sx: 0.8, sy: 1e6},
		{x: 4, h: 400, sx: 0.8, sy: 1e6},
		{x: 0, y: 0, h: 700, sx: 1.2, sy: 1.2},
	}}
	nodes := []plannerNode{
		ptNode("w1", -16, -3), ptNode("w2", -15, 3), ptNode("w3", -19, 0),
		ptNode("e1", 16, -3), ptNode("e2", 15, 3), ptNode("e3", 19, 0),
	}
	p := ptParams()
	p.RangeKm = 40
	return v, planInputs{Area: ptArea(-22, -8, 22, 8), Nodes: nodes, Params: p}
}

func TestCoveragePlanJoinsClustersAtSummit(t *testing.T) {
	v, in := ptTwoClusterCase()
	resp := ptPlan(t, v, in)
	if resp.Baseline.Components != 2 || resp.Baseline.Repeaters != 6 {
		t.Fatalf("baseline %+v, want 2 components of 6 repeaters", resp.Baseline)
	}
	if resp.Status != "improved" {
		t.Errorf("status %q, want improved", resp.Status)
	}
	if len(resp.Sites) == 0 {
		t.Fatal("no sites")
	}
	s := resp.Sites[0]
	if d := geoDistKm(geoPoint{s.Lat, s.Lon}, ptGeo(0, 0)); d > 1 {
		t.Errorf("first site %.2f km from the summit (%v,%v)", d, s.Lat, s.Lon)
	}
	if s.Step != 1 || s.Reason != "joins" || s.Gain.ComponentsJoined != 1 || s.After.Components != 1 {
		t.Errorf("site 1 = step %d reason %q gain %+v after %+v", s.Step, s.Reason, s.Gain, s.After)
	}
	if !slices.Equal(s.JoinsComponents, []int{0, 1}) {
		t.Errorf("joins_components %v, want [0 1]", s.JoinsComponents)
	}
	if s.AntennaM != in.Params.NewAntennaM || s.ElevM < 700 {
		t.Errorf("antenna %v elev %v", s.AntennaM, s.ElevM)
	}
	if len(s.Links) == 0 || len(s.Links) > planSiteLinksOut {
		t.Fatalf("links %d", len(s.Links))
	}
	sides := map[byte]bool{}
	for i, l := range s.Links {
		if l.Class == "solid" && l.Node >= 0 {
			sides[resp.Nodes[l.Node].Name[0]] = true
		}
		if i > 0 && l.ClearancePct > s.Links[i-1].ClearancePct {
			t.Errorf("links not sorted by clearance: %+v", s.Links)
		}
	}
	if !sides['w'] || !sides['e'] {
		t.Errorf("summit links do not reach both clusters: %+v", s.Links)
	}
	// The summit is the only joining site; nothing else adds anything.
	if len(resp.Sites) != 1 || resp.Final.Components != 1 {
		t.Errorf("sites %d final %+v, want exactly the summit", len(resp.Sites), resp.Final)
	}
	// Baseline components: west (indices 0-2) is id 0, east id 1.
	for i, n := range resp.Nodes {
		if want := i / 3; n.Component != want {
			t.Errorf("node %s component %d, want %d", n.Name, n.Component, want)
		}
	}
	for _, e := range resp.Edges {
		if e.Class == "solid" && resp.Nodes[e.A].Name[0] != resp.Nodes[e.B].Name[0] {
			t.Errorf("solid edge across the ridges: %+v", e)
		}
	}
}

// ptChainCase: a-b-c with a hill between a and c, so a–c is blocked and b is
// a cut vertex; the hilltop sees all three.
func ptChainCase() (terrainView, planInputs) {
	v := &ptBumps{base: 100, bumps: []ptBump{{x: 0, y: 0, h: 250, sx: 1, sy: 1}}}
	nodes := []plannerNode{ptNode("a", -6, 0), ptNode("b", 0, 6), ptNode("c", 6, 0)}
	p := ptParams()
	p.ExistingAntennaM, p.RangeKm = 25, 20
	return v, planInputs{Area: ptArea(-10, -10, 10, 10), Nodes: nodes, Params: p}
}

func TestCoveragePlanConnectedChainGetsRedundancy(t *testing.T) {
	v, in := ptChainCase()
	resp := ptPlan(t, v, in)
	if resp.Status != "connected" || resp.Baseline.Components != 1 || resp.Baseline.CutVertices != 1 {
		t.Fatalf("status %q baseline %+v, want connected with 1 cut vertex", resp.Status, resp.Baseline)
	}
	if len(resp.Sites) == 0 {
		t.Fatal("no redundancy pick")
	}
	s := resp.Sites[0]
	if s.Reason != "redundancy" || s.Gain.CutVerticesRemoved != 1 || s.After.CutVertices != 0 || s.Gain.ComponentsJoined != 0 {
		t.Errorf("site 1 reason %q gain %+v after %+v", s.Reason, s.Gain, s.After)
	}
	// Fragility is not in the summary; recompute it from the graph.
	g := newBackboneGraph(3, nil, gEdges([2]int{0, 1}, [2]int{1, 2}))
	before := g.Stats(nil).Fragility
	var peers []int
	for _, l := range s.Links {
		if l.Class == "solid" && l.Node >= 0 {
			peers = append(peers, l.Node)
		}
	}
	g.AddVertex(peers)
	if after := g.Stats(func(i int) bool { return i < 3 }).Fragility; after >= before {
		t.Errorf("fragility %d -> %d, want a decrease", before, after)
	}
	if len(resp.Sites) != 1 {
		t.Errorf("%d sites; after the cycle closes nothing should gain", len(resp.Sites))
	}
}

func ptFlatClusterCase() (terrainView, planInputs) {
	nodes := []plannerNode{
		ptNode("a", 0, 0), ptNode("b", 3, 0), ptNode("c", 0, 3), ptNode("d", 3, 3), ptNode("e", 1.5, 1.5),
	}
	p := ptParams()
	p.RangeKm = 20
	return &ptBumps{base: 200}, planInputs{Area: ptArea(-10, -10, 13, 13), Nodes: nodes, Params: p}
}

func TestCoveragePlanFullMeshNeedsNothing(t *testing.T) {
	v, in := ptFlatClusterCase()
	resp := ptPlan(t, v, in)
	if resp.Status != "connected" || resp.Baseline.Components != 1 || resp.Baseline.CutVertices != 0 {
		t.Fatalf("status %q baseline %+v", resp.Status, resp.Baseline)
	}
	if n := len(resp.Edges); n != 10 {
		t.Errorf("%d edges, want all 10 pairs solid", n)
	}
	if len(resp.Sites) != 0 {
		t.Errorf("sites %+v, want none", resp.Sites)
	}
	// Candidates did exist; they just add nothing.
	for _, w := range resp.Warnings {
		if strings.Contains(w, "candidate") {
			t.Errorf("unexpected warning %q", w)
		}
	}
	if resp.Final != resp.Baseline {
		t.Errorf("final %+v != baseline %+v", resp.Final, resp.Baseline)
	}
}

func TestCoveragePlanBaselineObservedVsPredicted(t *testing.T) {
	v := &ptBumps{base: 200}
	nodes := []plannerNode{ptNode("a", 0, 0), ptNode("b", 3, 0), ptNode("c", 6, 0)}
	in := planInputs{Area: ptArea(-1, -1, 7, 1), Nodes: nodes, Params: ptParams(),
		Observed: []plannerEdge{{A: 1, B: 0, Class: "observed"}}}
	in.Params.Sites = 0

	pred := ptPlan(t, v, in)
	in.Params.Baseline = "observed"
	obs := ptPlan(t, v, in)

	if pred.Baseline.Components != 1 || obs.Baseline.Components != 2 {
		t.Errorf("components predicted %d observed %d, want 1 and 2", pred.Baseline.Components, obs.Baseline.Components)
	}
	if len(obs.Edges) != 1 || obs.Edges[0].Class != "observed" || obs.Edges[0].A != 0 || obs.Edges[0].B != 1 {
		t.Errorf("observed-mode edges %+v", obs.Edges)
	}
	if obs.Edges[0].ClearancePct < 60 {
		t.Errorf("observed edge terrain clearance %v not recorded", obs.Edges[0].ClearancePct)
	}
	var classes []string
	for _, e := range pred.Edges {
		classes = append(classes, e.Class)
	}
	if !slices.Equal(classes, []string{"observed", "solid", "solid"}) {
		t.Errorf("predicted-mode classes %v", classes)
	}
	if pred.Edges[1].DistanceKm > pred.Edges[2].DistanceKm {
		t.Errorf("solid edges not by distance: %+v", pred.Edges)
	}
	if obs.Nodes[2].Component != 1 || pred.Nodes[2].Component != 0 {
		t.Errorf("node c component observed %d predicted %d", obs.Nodes[2].Component, pred.Nodes[2].Component)
	}
}

func TestCoveragePlanStaleExclusion(t *testing.T) {
	v := &ptBumps{base: 200}
	stale, margin := ptNode("s", 0, 3), ptNode("m", 9, 0)
	stale.Stale, margin.Stale, margin.InArea = true, true, false
	in := planInputs{
		Area:     ptArea(-5, -5, 5, 5),
		Nodes:    []plannerNode{ptNode("a", 0, 0), ptNode("b", 3, 0), stale, margin},
		Observed: []plannerEdge{{A: 0, B: 2, Class: "observed"}},
		Params:   ptParams(),
	}
	in.Params.Sites = 0

	resp := ptPlan(t, v, in)
	if resp.Baseline.Repeaters != 2 || resp.Baseline.StaleExcluded != 1 || resp.Final.StaleExcluded != 1 {
		t.Errorf("baseline %+v, want 2 repeaters and 1 stale excluded", resp.Baseline)
	}
	if !slices.Contains(resp.Warnings, "1 stale repeater excluded") {
		t.Errorf("warnings %q", resp.Warnings)
	}
	if resp.Nodes[2].Component != -1 || resp.Nodes[3].Component != -1 || !resp.Nodes[2].Stale {
		t.Errorf("stale nodes %+v %+v", resp.Nodes[2], resp.Nodes[3])
	}
	for _, e := range resp.Edges {
		if e.A >= 2 || e.B >= 2 {
			t.Errorf("edge touches an excluded node: %+v", e)
		}
	}

	in.Params.IncludeStale = true
	resp = ptPlan(t, v, in)
	if resp.Baseline.Repeaters != 3 || resp.Baseline.StaleExcluded != 0 || resp.Baseline.Components != 1 {
		t.Errorf("with stale: baseline %+v", resp.Baseline)
	}
	for _, w := range resp.Warnings {
		if strings.Contains(w, "stale") {
			t.Errorf("unexpected warning %q", w)
		}
	}
	if resp.Nodes[2].Component != 0 || resp.Nodes[3].Component < 0 {
		t.Errorf("components %d %d", resp.Nodes[2].Component, resp.Nodes[3].Component)
	}
}

func TestCoveragePlanEdgesCapAndTruncation(t *testing.T) {
	var nodes []plannerNode
	for r := 0; r < 10; r++ {
		for c := 0; c < 7; c++ {
			nodes = append(nodes, ptNode("n", float64(c)*0.5, float64(r)*0.5))
		}
	}
	in := planInputs{Area: ptArea(-1, -1, 4, 5.5), Nodes: nodes, Params: ptParams()}
	in.Params.Sites, in.Params.ExistingAntennaM, in.Params.RangeKm = 1, 40, 10
	for i := 0; i < 5; i++ {
		in.Observed = append(in.Observed, plannerEdge{A: 2 * i, B: 60 + i, Class: "observed"})
	}
	resp := ptPlan(t, &ptBumps{base: 200}, in)
	if len(resp.Edges) != plannerMaxEdgesOut || !resp.Truncated {
		t.Fatalf("edges %d truncated %v, want %d and true", len(resp.Edges), resp.Truncated, plannerMaxEdgesOut)
	}
	for i, e := range resp.Edges {
		want := "solid"
		if i < 5 {
			want = "observed"
		}
		if e.Class != want {
			t.Fatalf("edge %d class %q, want %q", i, e.Class, want)
		}
		if i > 0 && e.Class == resp.Edges[i-1].Class && e.DistanceKm < resp.Edges[i-1].DistanceKm {
			t.Fatalf("edge %d out of distance order", i)
		}
	}
	if !slices.Contains(resp.Warnings, "Area has no candidate high ground with a usable link") {
		t.Errorf("warnings %q", resp.Warnings)
	}
	if resp.Status != "connected" || len(resp.Sites) != 0 {
		t.Errorf("status %q sites %d", resp.Status, len(resp.Sites))
	}

	// A small plan is not cut, but the caller's node cap still shows.
	v, small := ptFlatClusterCase()
	small.Truncated = true
	if resp := ptPlan(t, v, small); !resp.Truncated || len(resp.Edges) != 10 {
		t.Errorf("caller truncation lost: truncated %v edges %d", resp.Truncated, len(resp.Edges))
	}
}

func ptJSON(t *testing.T, resp PlannerPlanResponse) string {
	t.Helper()
	resp.ComputedAt = time.Time{}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCoveragePlanDeterministic(t *testing.T) {
	for name, mk := range map[string]func() (terrainView, planInputs){
		"two clusters": ptTwoClusterCase, "chain": ptChainCase, "hilly": func() (terrainView, planInputs) { return ptHillyCase(40, 20) },
	} {
		t.Run(name, func(t *testing.T) {
			v, in := mk()
			first := ptJSON(t, ptPlan(t, v, in))
			if again := ptJSON(t, ptPlan(t, v, in)); again != first {
				t.Errorf("runs differ:\n%s\n%s", first, again)
			}
			if !strings.Contains(first, `"warnings":[`) || !strings.Contains(first, `"sites":[`) {
				t.Errorf("slices must encode as arrays: %s", first)
			}
		})
	}
}

// ptHillyCase: n seeded random nodes over a side×side km area (plus a 10 km
// margin) on ptWaves, 10 sites asked.
func ptHillyCase(n int, side float64) (terrainView, planInputs) {
	rng := rand.New(rand.NewPCG(7, 11))
	var nodes []plannerNode
	for i := 0; i < n; i++ {
		x, y := rng.Float64()*(side+20)-10, rng.Float64()*(side+20)-10
		nd := ptNode(string(rune('a'+i%26))+string(rune('a'+i/26)), x, y)
		nd.InArea = x >= 0 && x <= side && y >= 0 && y <= side
		nd.Stale = i%10 == 9
		nodes = append(nodes, nd)
	}
	p := ptParams()
	p.Sites, p.ExistingAntennaM, p.NewAntennaM, p.RangeKm = 10, 10, 15, 30
	return ptWaves{}, planInputs{Area: ptArea(0, 0, side, side), Nodes: nodes, Params: p}
}

// ptHeavyCase: 300 random nodes (220 in a 100×100 km area) on flat ground
// with 40 m / 20 m masts and a 60 km range, so every stage has real work
// (~0.1 s each without -race): long predicted links stay solid and need
// full profiles. With the observed baseline (150 links) the backbone is in
// pieces and the greedy runs.
func ptHeavyCase(baseline string) (terrainView, planInputs) {
	rng := rand.New(rand.NewPCG(5, 6))
	var nodes []plannerNode
	for i := 0; i < plannerMaxNodes; i++ {
		x, y := rng.Float64()*100, rng.Float64()*100
		if i >= 220 { // the 30 km margin
			x, y = rng.Float64()*160-30, rng.Float64()*160-30
			for x >= 0 && x <= 100 && y >= 0 && y <= 100 {
				x, y = rng.Float64()*160-30, rng.Float64()*160-30
			}
		}
		nd := ptNode("h", x, y)
		nd.InArea = i < 220
		nodes = append(nodes, nd)
	}
	var observed []plannerEdge
	for len(observed) < 150 {
		a, b := rng.IntN(len(nodes)), rng.IntN(len(nodes))
		if a != b {
			observed = append(observed, plannerEdge{A: a, B: b, Class: "observed"})
		}
	}
	p := ptParams()
	p.Sites, p.RangeKm, p.NewAntennaM, p.ExistingAntennaM, p.Baseline = 10, 60, 40, 20, baseline
	return &ptBumps{base: 0}, planInputs{Area: ptArea(0, 0, 100, 100), Nodes: nodes, Observed: observed, Params: p}
}

// Cancelling from inside each stage (baseline edges, candidates, greedy)
// returns ctx.Err() within 50 ms, though each stage has far more work left.
func TestCoveragePlanCancelPromptly(t *testing.T) {
	for _, tc := range []struct {
		baseline string
		at       float64
	}{{"predicted", 0.05}, {"predicted", 0.45}, {"observed", 0.8}} {
		v, in := ptHeavyCase(tc.baseline)
		ctx, cancel := context.WithCancel(context.Background())
		var cancelled time.Time
		_, err := runCoveragePlan(ctx, v, in, func(f float64) {
			if f >= tc.at && cancelled.IsZero() {
				cancel()
				cancelled = time.Now()
			}
		})
		took := time.Since(cancelled)
		cancel()
		if cancelled.IsZero() {
			t.Fatalf("%s: progress never reached %.2f (err %v)", tc.baseline, tc.at, err)
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("cancel at %.2f: err %v, want context.Canceled", tc.at, err)
		}
		if took > 50*time.Millisecond {
			t.Errorf("cancel at %.2f: returned %v after cancel", tc.at, took)
		}
		t.Logf("%s, cancel at %.2f: returned %v later", tc.baseline, tc.at, took)
	}
	v, in := ptHillyCase(40, 20)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runCoveragePlan(ctx, v, in, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("pre-cancelled: %v", err)
	}
}

func TestCoveragePlanProgressMonotonic(t *testing.T) {
	v, in := ptHillyCase(40, 20)
	last := -1.0
	resp, err := runCoveragePlan(context.Background(), v, in, func(f float64) {
		if f < last || f < 0 || f > 1 {
			t.Errorf("progress %v after %v", f, last)
		}
		last = f
	})
	if err != nil || last != 1 {
		t.Fatalf("err %v last progress %v", err, last)
	}
	if len(resp.Sites) == 0 {
		t.Errorf("hilly case picked nothing: %+v", resp.Baseline)
	}
	for i, s := range resp.Sites {
		if s.Step != i+1 || s.Lat != losRound(s.Lat, 1e5) || s.ElevM != losRound(s.ElevM, 10) {
			t.Errorf("site %+v", s)
		}
		for _, l := range s.Links {
			if (l.Node < 0) != (l.PeerStep > 0) || (l.PeerStep > 0 && l.PeerStep >= s.Step) {
				t.Errorf("site %d link %+v", s.Step, l)
			}
		}
		for _, o := range resp.Sites[:i] {
			if d := geoDistKm(geoPoint{s.Lat, s.Lon}, geoPoint{o.Lat, o.Lon}); d <= planSiteSpacingKm {
				t.Errorf("sites %d and %d only %.2f km apart", o.Step, s.Step, d)
			}
		}
	}
}

func TestCoveragePlanRejectsBadInput(t *testing.T) {
	v, good := ptFlatClusterCase()
	cases := map[string]func(*planInputs){
		"sites":     func(in *planInputs) { in.Params.Sites = plannerMaxSites + 1 },
		"range":     func(in *planInputs) { in.Params.RangeKm = math.NaN() },
		"antenna":   func(in *planInputs) { in.Params.NewAntennaM = -1 },
		"frequency": func(in *planInputs) { in.Params.FrequencyMHz = 0 },
		"k":         func(in *planInputs) { in.Params.KFactor = -1 },
		"baseline":  func(in *planInputs) { in.Params.Baseline = "guess" },
		"area":      func(in *planInputs) { in.Area.S, in.Area.N = in.Area.N, in.Area.S },
		"node":      func(in *planInputs) { in.Nodes[0].Lat = math.Inf(1) },
		"nodes":     func(in *planInputs) { in.Nodes = make([]plannerNode, plannerMaxNodes+1) },
	}
	for name, mutate := range cases {
		in := good
		in.Nodes = slices.Clone(good.Nodes)
		mutate(&in)
		if _, err := runCoveragePlan(context.Background(), v, in, nil); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := runCoveragePlan(context.Background(), nil, good, nil); err == nil {
		t.Error("nil view: no error")
	}
	// Out-of-range observed indices are ignored, not trusted.
	good.Observed = []plannerEdge{{A: -1, B: 2}, {A: 0, B: 99}, {A: 3, B: 3}}
	if _, err := runCoveragePlan(context.Background(), v, good, nil); err != nil {
		t.Errorf("bad observed edges: %v", err)
	}
}

// ─── Shared machinery ──────────────────────────────────────────────────────

// planLinkEval must reproduce sampleTerrainProfile + analyzeLOS exactly,
// and its early exit must agree with the full value.
func TestPlannerSearchLinkEvalMatchesAnalyzeLOS(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	views := map[string]terrainView{"bumps": ptTwoClusterView(), "waves": ptWaves{}, "grid": ptBenchTerrain()}
	for name, v := range views {
		var origin geoPoint
		if name == "grid" {
			origin = geoPoint{45, -120.5}
		} else {
			origin = ptGeo(0, 0)
		}
		ev := newPlanLinkEval(context.Background(), v, 869.525, 0)
		for i := 0; i < 300; i++ {
			a := geoPoint{origin.Lat + rng.Float64()*0.6 - 0.3, origin.Lon + rng.Float64()*0.8 - 0.4}
			b := geoPoint{origin.Lat + rng.Float64()*0.6 - 0.3, origin.Lon + rng.Float64()*0.8 - 0.4}
			if i%50 == 0 {
				b = a // zero-length leg
			}
			d := geoDistKm(a, b)
			ha, hb := rng.Float64()*30, rng.Float64()*30
			prof, ok := sampleTerrainProfile(v, a, b, planProfileSamples(d))
			if !ok {
				t.Fatalf("%s: profile not ok", name)
			}
			want, ok := analyzeLOS(prof, d, losParams{FreqMHz: 869.525, AntennaAM: ha, AntennaBM: hb})
			if !ok {
				t.Fatalf("%s: analyzeLOS not ok", name)
			}
			// Equal up to rounding: a sample may sit an ulp (~1 nm) away
			// where a multiply-add fuses differently.
			tol := 1e-9 * math.Max(1, math.Abs(want.Clearance))
			got, ok := ev.clearance(a, b, d, ha, hb, math.Inf(-1))
			if !ok || math.Abs(got-want.Clearance) > tol {
				t.Fatalf("%s pair %d: eval %v (ok %v), analyzeLOS %v", name, i, got, ok, want.Clearance)
			}
			for _, floor := range []float64{0, plannerMinClearance, want.Clearance - 1e-6, want.Clearance + 1e-6} {
				c, ok := ev.clearance(a, b, d, ha, hb, floor)
				if ok != (want.Clearance >= floor) || (ok && math.Abs(c-want.Clearance) > tol) {
					t.Fatalf("%s pair %d floor %v: got %v ok %v, full %v", name, i, floor, c, ok, want.Clearance)
				}
			}
		}
	}
	// A profile that leaves the view is no link, like analyzeLOS's reject.
	ev := newPlanLinkEval(context.Background(), rampView{0.5}, 869.525, 0)
	if _, ok := ev.clearance(geoPoint{0, 0}, geoPoint{0, 1}, geoDistKm(geoPoint{0, 0}, geoPoint{0, 1}), 5, 5, math.Inf(-1)); ok {
		t.Error("out-of-view profile ok")
	}
}

func ptTwoClusterView() terrainView { v, _ := ptTwoClusterCase(); return v }

// planNear must never reject a pair within range.
func TestPlannerSearchNearIsConservative(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	for _, lat := range []float64{0, 37, 60, 75, -55} {
		for _, km := range []float64{2, 10, 60, 100} {
			p := newPlanNear(km, math.Abs(lat)+1)
			for i := 0; i < 2000; i++ {
				a := geoPoint{lat + rng.Float64()*2 - 1, rng.Float64()*360 - 180}
				brg, dist := rng.Float64()*2*math.Pi, km*(0.98+rng.Float64()*0.04)
				dLat := dist * math.Cos(brg) / plannerKmPerDeg
				dLon := dist * math.Sin(brg) / (plannerKmPerDeg * math.Cos(a.Lat*math.Pi/180))
				b := geoPoint{a.Lat + dLat, math.Remainder(a.Lon+dLon, 360)}
				if math.Abs(b.Lat) > math.Abs(lat)+1 {
					continue
				}
				if geoDistKm(a, b) <= km && !p.maybe(a, b) {
					t.Fatalf("lat %v km %v: rejected %v-%v at %.6f km", lat, km, a, b, geoDistKm(a, b))
				}
			}
		}
	}
	if p := newPlanNear(10, 45); p.maybe(geoPoint{45, 0}, geoPoint{45.2, 0}) {
		t.Error("22 km north accepted for 10 km")
	}
}

// Plateau cells are all local maxima; thinning keeps one per 3×3 block
// (radius 2) and never touches an isolated peak.
func TestPlannerSearchGridPeaksThinPlateaus(t *testing.T) {
	ctx := context.Background()
	// Half a step of slack: the grid's cos(latitude) is the box middle's,
	// not ptGeo's origin's.
	box := ptArea(0, 0, 20.5, 11.5)
	flat, err := newPlanGrid(ctx, &ptBumps{base: 10}, box, 1)
	if err != nil {
		t.Fatal(err)
	}
	if flat.rows != 12 || flat.cols != 21 {
		t.Fatalf("grid %dx%d, want 12x21", flat.rows, flat.cols)
	}
	if n := len(flat.peaks(2)); n != 12*21 {
		t.Errorf("plateau peaks %d, want every cell", n)
	}
	thin := flat.thin(flat.peaks(2), 2)
	if len(thin) != 4*7 {
		t.Errorf("thinned plateau peaks %d, want a 3-cell lattice of 28", len(thin))
	}
	for _, i := range thin {
		if r, c := i/flat.cols, i%flat.cols; r%3 != 0 || c%3 != 0 {
			t.Errorf("thinned peak at %d,%d off the lattice", r, c)
		}
	}
	hills := &ptBumps{base: 10, bumps: []ptBump{{x: 5, y: 5, h: 100, sx: 1, sy: 1}, {x: 15, y: 5, h: 50, sx: 1, sy: 1}}}
	g, err := newPlanGrid(ctx, hills, box, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, thin := range []bool{false, true} {
		peaks := g.peaks(2)
		if thin {
			peaks = g.thin(peaks, 2)
		}
		var tops []geoPoint
		for _, i := range peaks {
			if g.elev[i] > 20 {
				tops = append(tops, g.point(i))
			}
		}
		if len(tops) != 2 || geoDistKm(tops[0], ptGeo(5, 5)) > 0.8 || geoDistKm(tops[1], ptGeo(15, 5)) > 0.8 {
			t.Errorf("thin=%v: hilltops %v", thin, tops)
		}
	}
	// Cells outside the view are skipped, not peaks.
	if g, err := newPlanGrid(ctx, rampView{ptLon0 + 0.1}, box, 1); err != nil {
		t.Fatal(err)
	} else {
		for _, i := range g.peaks(1) {
			if !g.ok[i] || g.point(i).Lon < ptLon0+0.1 {
				t.Fatalf("peak %v outside the view", g.point(i))
			}
		}
	}
	if _, err := newPlanGrid(ctx, &ptBumps{}, geoBBox{S: 0, W: 0, N: 10, E: 10}, 1); err == nil {
		t.Error("1100 km grid accepted")
	}
}

// ─── Relay ─────────────────────────────────────────────────────────────────

// ptRelayCase: A and B 20 km apart across a ridge (350 m); a sharp hill
// (~725 m) 5 km beside the line sees both more steeply. Existing nodes: one
// on the hill, six on the ridge, and four that break a rule. Existing masts
// are 20 m: a short mast on a rounded crown is shadowed by the crown itself
// when looking 3° down.
func ptRelayCase() (terrainView, []plannerNode, PlannerRelayParams) {
	v := &ptBumps{base: 50, bumps: []ptBump{
		{x: 0, y: 0, h: 300, sx: 0.7, sy: 3},
		{x: 0, y: 5, h: 600, sx: 0.8, sy: 0.8},
	}}
	stale := ptNode("stale-hill", 0.3, 5)
	stale.Stale = true
	nodes := []plannerNode{
		ptNode("crest-3", 0, -3), ptNode("crest-2", 0, -2), ptNode("crest-1", 0, -1),
		ptNode("crest0", 0, 0), ptNode("crest1", 0, 1), ptNode("crest2", 0, 2),
		ptNode("detour", 0, 8.5),      // legs 13.1 km ≤ range, detour 26.2 > 1.3·20
		ptNode("out-of-range", -6, 0), // detour 20, but 16 km from B
		ptNode("at-a", -10, 0),        // A itself
		stale,
		ptNode("hill", 0, 5),
	}
	a, b := ptGeo(-10, 0), ptGeo(10, 0)
	return v, nodes, PlannerRelayParams{
		A: geoPointJSON{a.Lat, a.Lon}, B: geoPointJSON{b.Lat, b.Lon},
		AntennaAM: 2, AntennaBM: 2, RelayAntennaM: 10, ExistingAntennaM: 20, RangeKm: 15,
		FrequencyMHz: 869.525, KFactor: 4.0 / 3.0,
	}
}

func TestRelaySearchRidgeAndHill(t *testing.T) {
	v, nodes, p := ptRelayCase()
	resp, err := runRelaySearch(context.Background(), v, nodes, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	a, b := geoPoint{p.A.Lat, p.A.Lon}, geoPoint{p.B.Lat, p.B.Lon}
	if resp.Direct.Verdict != "blocked" || len(resp.Direct.ElevM) == 0 {
		t.Errorf("direct %+v, want blocked with a profile", resp.Direct)
	}
	if len(resp.Existing) != relayMaxOptions || len(resp.Open) == 0 || len(resp.Open) > relayMaxOptions {
		t.Fatalf("existing %d open %d", len(resp.Existing), len(resp.Open))
	}
	if resp.Existing[0].Name != "hill" || resp.Existing[0].PubKey != "pk-hill" {
		t.Errorf("existing[0] = %q, want the hill", resp.Existing[0].Name)
	}
	if d := geoDistKm(geoPoint{resp.Open[0].Lat, resp.Open[0].Lon}, ptGeo(0, 5)); d > 1 {
		t.Errorf("best open option %.2f km from the hilltop", d)
	}
	for _, list := range [][]PlannerRelayOption{resp.Existing, resp.Open} {
		for i, o := range list {
			r := geoPoint{o.Lat, o.Lon}
			dA, dB := geoDistKm(a, r), geoDistKm(r, b)
			if dA > p.RangeKm || dB > p.RangeKm || dA+dB > 1.3*geoDistKm(a, b) {
				t.Errorf("%s %q breaks the corridor/range rule: %.2f + %.2f", o.Kind, o.Name, dA, dB)
			}
			if len(o.LegA.ElevM) == 0 || len(o.LegB.ElevM) == 0 || len(o.LegA.ElevM) > 200 {
				t.Errorf("%s %q legs lack profiles", o.Kind, o.Name)
			}
			if o.BottleneckPct != math.Min(o.LegA.ClearancePct, o.LegB.ClearancePct) || o.BottleneckPct < 0 {
				t.Errorf("%s %q bottleneck %v legs %v/%v", o.Kind, o.Name, o.BottleneckPct, o.LegA.ClearancePct, o.LegB.ClearancePct)
			}
			if i > 0 && o.BottleneckPct > list[i-1].BottleneckPct {
				t.Errorf("%s list not ranked: %v after %v", o.Kind, o.BottleneckPct, list[i-1].BottleneckPct)
			}
			if o.ElevM <= 50 {
				t.Errorf("%s %q elev %v", o.Kind, o.Name, o.ElevM)
			}
		}
	}
	for _, o := range resp.Existing {
		if o.Kind != "existing" || o.AntennaM != p.ExistingAntennaM {
			t.Errorf("existing option %+v", o)
		}
		switch o.Name {
		case "detour", "out-of-range", "at-a", "stale-hill":
			t.Errorf("%q should be excluded", o.Name)
		}
	}
	for i, o := range resp.Open {
		if o.Kind != "open" || o.AntennaM != p.RelayAntennaM || o.PubKey != "" {
			t.Errorf("open option %+v", o)
		}
		for _, q := range resp.Open[:i] {
			if geoDistKm(geoPoint{o.Lat, o.Lon}, geoPoint{q.Lat, q.Lon}) <= relaySpacingKm {
				t.Errorf("open options within %v km", relaySpacingKm)
			}
		}
	}
	if len(resp.Warnings) != 0 {
		t.Errorf("warnings %q", resp.Warnings)
	}
	if resp.Source != planSearchSource || resp.Params != p {
		t.Errorf("source %q params %+v", resp.Source, resp.Params)
	}

	// Each rule-breaker alone is still excluded (not just ranked out).
	for _, i := range []int{6, 7, 8, 9} {
		resp2, err := runRelaySearch(context.Background(), v, nodes[i:i+1], p, nil)
		if err != nil || len(resp2.Existing) != 0 {
			t.Errorf("%q listed alone: %+v (err %v)", nodes[i].Name, resp2.Existing, err)
		}
	}
}

func TestRelaySearchBlockedOptionsOnlyWhenNothingClears(t *testing.T) {
	v, nodes, p := ptRelayCase()
	pit := ptNode("pit", 2, -4)
	terr := v.(*ptBumps)
	terr.bumps = append(terr.bumps, ptBump{x: 2, y: -4, h: -300, sx: 0.4, sy: 0.4})
	resp, err := runRelaySearch(context.Background(), terr, []plannerNode{pit, nodes[10]}, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Existing) != 1 || resp.Existing[0].Name != "hill" {
		t.Errorf("existing %+v, want only the hill (the pit is blocked)", resp.Existing)
	}

	// A sits in a pit: no relay clears, so blocked options are still listed.
	a := ptGeo(-10, 0)
	pitA := &ptBumps{base: 50, bumps: []ptBump{{x: -10, y: 0, h: -45, sx: 0.4, sy: 0.4}}}
	resp, err = runRelaySearch(context.Background(), pitA, []plannerNode{ptNode("mid", 0, 1)}, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Existing) != 1 || len(resp.Open) == 0 {
		t.Fatalf("existing %d open %d, want blocked options kept", len(resp.Existing), len(resp.Open))
	}
	for _, o := range append(resp.Existing, resp.Open...) {
		if o.BottleneckPct >= 0 {
			t.Errorf("option at %v,%v clears from inside the pit at %v", o.Lat, o.Lon, a)
		}
	}
	if !slices.Contains(resp.Warnings, "No relay gives a clear first Fresnel zone") {
		t.Errorf("warnings %q", resp.Warnings)
	}
}

func TestRelaySearchDeterministicAndCancellable(t *testing.T) {
	v, nodes, p := ptRelayCase()
	r1, err1 := runRelaySearch(context.Background(), v, nodes, p, nil)
	r2, err2 := runRelaySearch(context.Background(), v, nodes, p, nil)
	j1, _ := json.Marshal(r1)
	j2, _ := json.Marshal(r2)
	if err1 != nil || err2 != nil || string(j1) != string(j2) {
		t.Errorf("relay runs differ (%v, %v)", err1, err2)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runRelaySearch(ctx, v, nodes, p, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("pre-cancelled: %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	var at time.Time
	_, err := runRelaySearch(ctx, v, nodes, p, func(f float64) {
		if f >= relayGridEnd && at.IsZero() { // before the first profile
			cancel()
			at = time.Now()
		}
	})
	cancel()
	if at.IsZero() || !errors.Is(err, context.Canceled) || time.Since(at) > 50*time.Millisecond {
		t.Errorf("mid-run cancel: err %v after %v", err, time.Since(at))
	}
	bad := p
	bad.B = geoPointJSON{p.A.Lat + 1, p.A.Lon} // 111 km
	if _, err := runRelaySearch(context.Background(), v, nodes, bad, nil); err == nil {
		t.Error("over-long leg accepted")
	}
	bad = p
	bad.RangeKm = 0
	if _, err := runRelaySearch(context.Background(), v, nodes, bad, nil); err == nil {
		t.Error("zero range accepted")
	}
}

// ─── Benchmarks (AGENTS.md rule 0) ─────────────────────────────────────────

// ptGridTerrain is SRTM-like: int16 metres every 3 arc-seconds (1200 per
// degree), bilinear like srtmView, row 0 = south.
type ptGridTerrain struct {
	lat0, lon0 float64
	rows, cols int
	g          []int16
}

func (t *ptGridTerrain) ElevationAt(lat, lon float64) (float64, bool) {
	fy, fx := (lat-t.lat0)*1200, (lon-t.lon0)*1200
	if !(fy >= 0 && fx >= 0 && fy <= float64(t.rows-1) && fx <= float64(t.cols-1)) {
		return 0, false
	}
	r, c := int(fy), int(fx)
	if r > t.rows-2 {
		r = t.rows - 2
	}
	if c > t.cols-2 {
		c = t.cols - 2
	}
	dy, dx := fy-float64(r), fx-float64(c)
	i := r*t.cols + c
	s := float64(t.g[i])*(1-dx) + float64(t.g[i+1])*dx
	n := float64(t.g[i+t.cols])*(1-dx) + float64(t.g[i+t.cols+1])*dx
	return s*(1-dy) + n*dy, true
}

var (
	ptBenchOnce sync.Once
	ptBenchView *ptGridTerrain
)

// ptBenchTerrain: 2°×3° (44–46 N, 122–119 W, fictional hills) of value noise,
// 20/6/1.6/0.6 km octaves, 0–~1250 m: rough enough that the 1 km grid has
// more than plannerMaxCands candidates. Built once, ~17 MB.
func ptBenchTerrain() *ptGridTerrain {
	ptBenchOnce.Do(func() {
		t := &ptGridTerrain{lat0: 44, lon0: -122, rows: 2*1200 + 1, cols: 3*1200 + 1}
		t.g = make([]int16, t.rows*t.cols)
		rng := rand.New(rand.NewPCG(42, 42))
		type octave struct {
			cellKm, amp float64
			w           int
			lat         []float64
		}
		kmPerPx := plannerKmPerDeg / 1200
		octs := []octave{{cellKm: 20, amp: 500}, {cellKm: 6, amp: 300}, {cellKm: 1.6, amp: 300}, {cellKm: 0.6, amp: 150}}
		for k := range octs {
			o := &octs[k]
			o.w = int(float64(t.cols)*kmPerPx/o.cellKm) + 3
			h := int(float64(t.rows)*kmPerPx/o.cellKm) + 3
			o.lat = make([]float64, o.w*h)
			for i := range o.lat {
				o.lat[i] = rng.Float64()
			}
		}
		smooth := func(f float64) float64 { return f * f * (3 - 2*f) }
		for r := 0; r < t.rows; r++ {
			for c := 0; c < t.cols; c++ {
				e := 0.0
				for k := range octs {
					o := &octs[k]
					y, x := float64(r)*kmPerPx/o.cellKm, float64(c)*kmPerPx*0.7/o.cellKm
					iy, ix := int(y), int(x)
					fy, fx := smooth(y-float64(iy)), smooth(x-float64(ix))
					l := o.lat[iy*o.w+ix:]
					top := l[0]*(1-fx) + l[1]*fx
					bot := l[o.w]*(1-fx) + l[o.w+1]*fx
					e += o.amp * (top*(1-fy) + bot*fy)
				}
				t.g[r*t.cols+c] = int16(e)
			}
		}
		ptBenchView = t
	})
	return ptBenchView
}

// ptBenchPlanInputs: the largest plan the API allows. 100×100 km area at
// 45 N, 300 nodes (220 in area, 80 in the 30 km margin) each on the highest
// of 8 random spots within 1.5 km, 10 % stale, 150 observed links.
func ptBenchPlanInputs() planInputs {
	v := ptBenchTerrain()
	rng := rand.New(rand.NewPCG(9, 9))
	area := geoBBox{S: 44.55, W: -121.136, N: 45.45, E: -119.864}
	outer := plannerExpandBBox(area, plannerNodeMarginKm)
	var nodes []plannerNode
	for len(nodes) < plannerMaxNodes {
		in := len(nodes) < 220
		b := outer
		if in {
			b = area
		}
		p := geoPoint{b.S + rng.Float64()*(b.N-b.S), b.W + rng.Float64()*(b.E-b.W)}
		if !in && plannerBBoxContains(area, p.Lat, p.Lon) {
			continue
		}
		best, bestE := p, -1e9
		for k := 0; k < 8; k++ {
			q := geoPoint{p.Lat + (rng.Float64()-0.5)*0.027, p.Lon + (rng.Float64()-0.5)*0.038}
			if e, ok := v.ElevationAt(q.Lat, q.Lon); ok && e > bestE {
				best, bestE = q, e
			}
		}
		i := len(nodes)
		nodes = append(nodes, plannerNode{
			PubKey: string(rune('A'+i%26)) + string(rune('A'+i/26%26)) + string(rune('0'+i/676)),
			Role:   "repeater", Lat: best.Lat, Lon: best.Lon, InArea: in, Stale: i%10 == 7,
		})
	}
	var observed []plannerEdge
	for len(observed) < 150 {
		a, b := rng.IntN(len(nodes)), rng.IntN(len(nodes))
		if a != b && geoDistKm(geoPoint{nodes[a].Lat, nodes[a].Lon}, geoPoint{nodes[b].Lat, nodes[b].Lon}) < 40 {
			observed = append(observed, plannerEdge{A: a, B: b, Class: "observed"})
		}
	}
	return planInputs{Area: area, Nodes: nodes, Observed: observed, Params: PlannerPlanParams{
		Sites: plannerMaxSites, RangeKm: 60, NewAntennaM: 10, ExistingAntennaM: 5, Baseline: "predicted",
		FrequencyMHz: 869.525, KFactor: 4.0 / 3.0, MinClearance: plannerMinClearance,
	}}
}

// ptBenchFlat: the same grid, all 0 m (sea, or a missing tile). Every cell
// of a plateau is a local maximum, so this is the candidate-count worst case.
func ptBenchFlat() *ptGridTerrain {
	t := ptBenchTerrain()
	return &ptGridTerrain{lat0: t.lat0, lon0: t.lon0, rows: t.rows, cols: t.cols, g: make([]int16, len(t.g))}
}

// BenchmarkCoveragePlanMax: the largest plan the API allows (100×100 km,
// 300 nodes, 60 km range, 10 sites), single-threaded, with the time split
// per stage. "hilly" is the realistic case; "flat" maximises grid peaks
// (every plateau cell is one, before thinning); "flat-tall" (40 m / 20 m
// masts, observed baseline of 150 links) also gives the peaks solid links
// and leaves components to join, so the greedy runs 10 rounds on
// plannerMaxCands; "flat-100m" (100 m masts, the config maximum) makes every
// link in range solid, so no profile exits early.
func BenchmarkCoveragePlanMax(b *testing.B) {
	base := ptBenchPlanInputs()
	tall, tallest := base, base
	tall.Params.NewAntennaM, tall.Params.ExistingAntennaM, tall.Params.Baseline = 40, 20, "observed"
	tallest.Params.NewAntennaM, tallest.Params.ExistingAntennaM = 100, 100
	for _, tc := range []struct {
		name string
		v    terrainView
		in   planInputs
	}{
		{"hilly", ptBenchTerrain(), base}, {"flat", ptBenchFlat(), base},
		{"flat-tall", ptBenchFlat(), tall}, {"flat-100m", ptBenchFlat(), tallest},
	} {
		b.Run(tc.name, func(b *testing.B) {
			var st planStageStats
			var resp PlannerPlanResponse
			for i := 0; i < b.N; i++ {
				var err error
				if resp, err = planCoverage(context.Background(), tc.v, tc.in, nil, &st); err != nil {
					b.Fatal(err)
				}
			}
			ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
			b.ReportMetric(ms(st.EdgesTime), "edges-ms")
			b.ReportMetric(ms(st.CandsTime), "cands-ms")
			b.ReportMetric(ms(st.GreedyTime), "greedy-ms")
			b.ReportMetric(ms(st.OutputTime), "output-ms")
			b.ReportMetric(float64(st.Profiles), "profiles")
			b.ReportMetric(float64(st.Peaks), "peaks")
			b.ReportMetric(float64(st.Candidates), "cands")
			b.ReportMetric(float64(st.Sites), "sites")
			b.Logf("edges %d baseline %+v final %+v status %s", len(resp.Edges), resp.Baseline, resp.Final, resp.Status)
		})
	}
}

// BenchmarkRelayMax: a 99 km A–B, 60 km range, 300 nodes. "flat" makes
// every 500 m cell a 3×3 peak (~100k cells to filter and sort).
func BenchmarkRelayMax(b *testing.B) {
	in := ptBenchPlanInputs()
	a, c := geoPoint{45.0, -121.13}, geoPoint{45.0, -119.87}
	p := PlannerRelayParams{
		A: geoPointJSON{a.Lat, a.Lon}, B: geoPointJSON{c.Lat, c.Lon},
		AntennaAM: 5, AntennaBM: 5, RelayAntennaM: 10, ExistingAntennaM: 5, RangeKm: 60,
		FrequencyMHz: 869.525, KFactor: 4.0 / 3.0,
	}
	for _, tc := range []struct {
		name string
		v    terrainView
	}{{"hilly", ptBenchTerrain()}, {"flat", ptBenchFlat()}} {
		b.Run(tc.name, func(b *testing.B) {
			var resp PlannerRelayResponse
			for i := 0; i < b.N; i++ {
				var err error
				if resp, err = runRelaySearch(context.Background(), tc.v, in.Nodes, p, nil); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(geoDistKm(a, c), "km")
			b.ReportMetric(float64(len(resp.Existing)), "existing")
			b.ReportMetric(float64(len(resp.Open)), "open")
		})
	}
}

// BenchmarkPlannerSearchProfile: one 40 km leg, shared path
// (sampleTerrainProfile + analyzeLOS) vs planLinkEval, full and with the
// solid-only early exit.
func BenchmarkPlannerSearchProfile(b *testing.B) {
	v := ptBenchTerrain()
	a, c := geoPoint{44.8, -121.0}, geoPoint{45.1, -120.65}
	d := geoDistKm(a, c)
	lp := losParams{FreqMHz: 869.525, AntennaAM: 10, AntennaBM: 5}
	b.Run("shared", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			prof, _ := sampleTerrainProfile(v, a, c, planProfileSamples(d))
			analyzeLOS(prof, d, lp)
		}
	})
	ev := newPlanLinkEval(context.Background(), v, 869.525, 0)
	b.Run("eval", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			ev.clearance(a, c, d, 10, 5, math.Inf(-1))
		}
	})
	b.Run("eval-solid", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			ev.clearance(a, c, d, 10, 5, plannerMinClearance)
		}
	})
}
