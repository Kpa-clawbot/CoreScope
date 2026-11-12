package main

import (
	"bytes"
	"encoding/json"
	"log"
	"math"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func TestPlannerConfigSizes(t *testing.T) {
	// Unset: the defaults, no warning.
	for _, c := range []*PlannerConfig{nil, {}, {Enabled: true}} {
		r, warns := c.resolved()
		if r.MaxAreaKm != 100 || r.NodeMarginKm != 30 || r.MaxLegKm != 100 || len(warns) != 0 {
			t.Fatalf("%+v: resolved %+v, warnings %q", c, r, warns)
		}
	}
	// Each end of each range is kept.
	for _, c := range []PlannerConfig{
		{MaxAreaKm: 20, NodeMarginKm: 10, MaxLegKm: 10, DefaultRangeKm: 10},
		{MaxAreaKm: 250, NodeMarginKm: 60, MaxLegKm: 200},
		{MaxAreaKm: 137.5, NodeMarginKm: 12.5, MaxLegKm: 64},
	} {
		r, warns := c.resolved()
		if r.MaxAreaKm != c.MaxAreaKm || r.NodeMarginKm != c.NodeMarginKm || r.MaxLegKm != c.MaxLegKm || len(warns) != 0 {
			t.Fatalf("%+v: resolved %+v, warnings %q", c, r, warns)
		}
	}
	// Out of range (NaN included): the default and one warning each.
	for _, c := range []struct {
		cfg  PlannerConfig
		warn string
	}{
		{PlannerConfig{MaxAreaKm: 19.9}, "planner.maxAreaKm must be within 20-250; using the default"},
		{PlannerConfig{MaxAreaKm: 251}, "planner.maxAreaKm must be within 20-250; using the default"},
		{PlannerConfig{MaxAreaKm: -100}, "planner.maxAreaKm must be within 20-250; using the default"},
		{PlannerConfig{MaxAreaKm: math.NaN()}, "planner.maxAreaKm must be within 20-250; using the default"},
		{PlannerConfig{NodeMarginKm: 9.5}, "planner.nodeMarginKm must be within 10-60; using the default"},
		{PlannerConfig{NodeMarginKm: 61}, "planner.nodeMarginKm must be within 10-60; using the default"},
		{PlannerConfig{MaxLegKm: 9}, "planner.maxLegKm must be within 10-200; using the default"},
		{PlannerConfig{MaxLegKm: 200.5}, "planner.maxLegKm must be within 10-200; using the default"},
		{PlannerConfig{MaxLegKm: math.Inf(1)}, "planner.maxLegKm must be within 10-200; using the default"},
	} {
		r, warns := c.cfg.resolved()
		if r.MaxAreaKm != 100 || r.NodeMarginKm != 30 || r.MaxLegKm != 100 || !slices.Equal(warns, []string{c.warn}) {
			t.Errorf("%+v: resolved %+v, warnings %q", c.cfg, r, warns)
		}
	}
}

func TestPlannerConfigRangesFollowMaxLeg(t *testing.T) {
	for _, c := range []struct {
		maxLeg float64
		want   []float64
	}{
		{100, []float64{10, 15, 20, 30, 40, 50, 60}},
		{60, []float64{10, 15, 20, 30, 40, 50, 60}},
		{45, []float64{10, 15, 20, 30, 40}},
		{10, []float64{10}},
	} {
		r, _ := (&PlannerConfig{MaxLegKm: c.maxLeg}).resolved()
		if got := r.rangesKm(); !slices.Equal(got, c.want) {
			t.Errorf("maxLegKm %v: ranges %v, want %v", c.maxLeg, got, c.want)
		}
	}
	// The returned slice cannot grow into the shared list.
	r, _ := (&PlannerConfig{MaxLegKm: 25}).resolved()
	_ = append(r.rangesKm(), 999)
	if plannerRangesKm[3] != 30 {
		t.Fatal("append through rangesKm overwrote plannerRangesKm")
	}

	// A default range over maxLegKm becomes the largest allowed one.
	for _, c := range []struct {
		cfg       PlannerConfig
		wantRange float64
		warn      string
	}{
		{PlannerConfig{MaxLegKm: 25}, 20, "planner.defaultRangeKm 30 is over planner.maxLegKm 25; using 20"},
		{PlannerConfig{MaxLegKm: 10, DefaultRangeKm: 60}, 10, "planner.defaultRangeKm 60 is over planner.maxLegKm 10; using 10"},
	} {
		r, warns := c.cfg.resolved()
		if r.DefaultRangeKm != c.wantRange || !slices.Equal(warns, []string{c.warn}) {
			t.Errorf("%+v: range %v, warnings %q", c.cfg, r.DefaultRangeKm, warns)
		}
		// Resolving again is stable and quiet.
		if r2, warns := r.resolved(); r2 != r || len(warns) != 0 {
			t.Errorf("%+v: re-resolved %+v, warnings %q", c.cfg, r2, warns)
		}
	}
	if r, warns := (&PlannerConfig{MaxLegKm: 40, DefaultRangeKm: 40}).resolved(); r.DefaultRangeKm != 40 || len(warns) != 0 {
		t.Errorf("range equal to maxLegKm: %v, %q", r.DefaultRangeKm, warns)
	}
}

func TestPlannerConfigJSONAndClient(t *testing.T) {
	var cfg Config
	raw := `{"planner":{"enabled":true,"maxAreaKm":180,"nodeMarginKm":45,"maxLegKm":35}}`
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	if p := cfg.Planner; p.MaxAreaKm != 180 || p.NodeMarginKm != 45 || p.MaxLegKm != 35 {
		t.Fatalf("decoded %+v", p)
	}
	cc := cfg.Planner.clientConfig(nil)
	if cc.MaxAreaKm != 180 || cc.NodeMarginKm != 45 || cc.MaxLegKm != 35 || cc.DefaultRangeKm != 30 ||
		!slices.Equal(cc.RangesKm, []float64{10, 15, 20, 30}) {
		t.Fatalf("client config %+v", cc)
	}
	b, _ := json.Marshal(cc)
	for _, k := range []string{`"maxAreaKm":180`, `"nodeMarginKm":45`, `"maxLegKm":35`, `"rangesKm":[10,15,20,30]`} {
		if !strings.Contains(string(b), k) {
			t.Fatalf("client JSON %s lacks %s", b, k)
		}
	}
	// Defaults.
	cc = (&PlannerConfig{Enabled: true}).clientConfig(nil)
	if cc.MaxAreaKm != 100 || cc.NodeMarginKm != 30 || cc.MaxLegKm != 100 || len(cc.RangesKm) != len(plannerRangesKm) {
		t.Fatalf("default client config %+v", cc)
	}
}

func TestPlannerConfigLogSizes(t *testing.T) {
	var buf bytes.Buffer
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(flags) })
	logPlannerConfig(&PlannerConfig{Enabled: true, MaxAreaKm: 200, NodeMarginKm: 50, MaxLegKm: 150})
	out := buf.String()
	for _, want := range []string{
		"maxArea=200 km, nodeMargin=50 km, maxLeg=150 km",
		"terrain tiles per request: coverage 28, relay 24, profile 24",
		"one view takes up to 81 MB and the cache up to 81 MB (2.9 MB a tile)",
		"tileCacheTiles=24 is below one full view; keeping 28 tiles resident",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}

// The handlers apply the operator's sizes: the box limit, the node margin
// (view, fingerprint and node selection), the leg limit and its ranges.
func TestPlannerAPIConfiguredSizes(t *testing.T) {
	const ip = "198.51.100.210"
	// About 145 × 144 km around the test cluster.
	const big = "/api/planner/coverage?bbox=29.5,-97.8,30.8,-96.3"
	h := newPlannerHarness(t)
	if w := h.get(big, ip); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), plannerErrAreaTooLarge) {
		t.Fatalf("default size: %d %s", w.Code, w.Body.String())
	}

	h = newPlannerHarnessWith(t, PlannerConfig{Enabled: true, MaxAreaKm: 200, NodeMarginKm: 50})
	// A repeater 40 km north of the box: inside a 50 km margin, not a 30 km one.
	h.data.setNodes(append(plannerTestNodes(), nodeInfo{PublicKey: "aa09", Name: "Ridge", Role: "repeater",
		Lat: 30.8 + 40/plannerKmPerDeg, Lon: -97.0, HasGPS: true, LastSeen: plannerTestStart}))
	if w := h.get(big, ip); w.Code != http.StatusAccepted && w.Code != http.StatusOK {
		t.Fatalf("maxAreaKm 200: %d %s", w.Code, w.Body.String())
	}
	h.waitEntered(t)
	area := geoBBox{S: 29.5, W: -97.8, N: 30.8, E: -96.3}
	if v := h.src.lastBBox(); v != plannerExpandBBox(area, 50) {
		t.Fatalf("view %+v, want the box plus 50 km", v)
	}
	h.stubs.mu.Lock()
	plan := h.stubs.lastPlan
	h.stubs.mu.Unlock()
	if !slices.ContainsFunc(plan.Nodes, func(n plannerNode) bool { return n.PubKey == "aa09" && !n.InArea }) {
		t.Fatalf("plan nodes %+v lack the margin repeater", plan.Nodes)
	}

	// maxLegKm: a 120 km leg is too long by default and allowed at 150.
	const relay120 = "/api/planner/relay?a=30.1,-97.1&b=31.18,-97.1"
	const prof120 = "/api/planner/profile?pts=30.1,-97.1,31.18,-97.1"
	h = newPlannerHarness(t)
	if w := h.get(relay120, ip); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), plannerErrTooLong) {
		t.Fatalf("default relay 120 km: %d %s", w.Code, w.Body.String())
	}
	if w := h.get(prof120, ip); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), plannerErrTooLong) {
		t.Fatalf("default profile 120 km: %d %s", w.Code, w.Body.String())
	}
	h = newPlannerHarnessWith(t, PlannerConfig{Enabled: true, MaxLegKm: 150})
	h.p.fastWait = plannerFastWait
	if w := h.get(relay120, ip); w.Code != http.StatusOK {
		t.Fatalf("maxLegKm 150 relay: %d %s", w.Code, w.Body.String())
	}
	w := h.get(prof120, ip)
	var prof PlannerProfileResponse
	if err := json.Unmarshal(w.Body.Bytes(), &prof); err != nil || w.Code != http.StatusOK ||
		len(prof.Legs) != 1 || prof.Legs[0].Error != "" || len(prof.Legs[0].ElevM) == 0 {
		t.Fatalf("maxLegKm 150 profile: %d %s", w.Code, w.Body.String())
	}
	if v, want := h.src.lastBBox(), plannerProfileView(geoBBox{30.1, -97.1, 31.18, -97.1}, plannerProfilePad(150)); v != want {
		t.Fatalf("profile view %+v, want %+v", v, want)
	}

	// Ranges above maxLegKm are refused, and the default follows them.
	h = newPlannerHarnessWith(t, PlannerConfig{Enabled: true, MaxLegKm: 25})
	h.p.fastWait = plannerFastWait
	for _, path := range []string{plannerRelayPath + "&range=30", plannerCovPath + "&range=30"} {
		if w := h.get(path, ip); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "range must be one of 10,15,20\"") {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if w := h.get(plannerRelayPath+"&range=20", ip); w.Code != http.StatusOK {
		t.Fatalf("range 20: %d %s", w.Code, w.Body.String())
	}
	if w := h.get(plannerRelayPath, ip); w.Code != http.StatusOK {
		t.Fatalf("default range: %d %s", w.Code, w.Body.String())
	}
	h.stubs.mu.Lock()
	rangeKm := h.stubs.lastRelay.RangeKm
	h.stubs.mu.Unlock()
	if rangeKm != 20 {
		t.Fatalf("default range %v, want 20", rangeKm)
	}
}
