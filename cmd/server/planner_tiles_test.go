package main

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// plannerTestDest is the point km from a on the initial bearing (degrees
// clockwise from north), on haversineKm's sphere.
func plannerTestDest(a geoPoint, bearingDeg, km float64) geoPoint {
	const rad = math.Pi / 180
	d := km / earthRadiusKm
	p1, l1, th := a.Lat*rad, a.Lon*rad, bearingDeg*rad
	p2 := math.Asin(math.Sin(p1)*math.Cos(d) + math.Cos(p1)*math.Sin(d)*math.Cos(th))
	l2 := l1 + math.Atan2(math.Sin(th)*math.Sin(d)*math.Cos(p1), math.Cos(d)-math.Sin(p1)*math.Sin(p2))
	return geoPoint{p2 / rad, l2 / rad}
}

// plannerTestTiles is the real source's tile count.
func plannerTestTiles(bb geoBBox) int {
	sp, ok := srtmSpanOf(bb)
	if !ok {
		return 0
	}
	return sp.count()
}

func TestPlannerWorstCaseTiles(t *testing.T) {
	cases := []struct {
		name           string
		sideKm, maxLat float64
		want           int
	}{
		// 50 km is 0.45° of latitude and, at the equator, of longitude.
		{"small at the equator", 50, 0, 2 * 2},
		// At 60° a degree of longitude is half as long: 0.9° → still 2.
		{"small at 60", 50, 60, 2 * 2},
		// 160 km: 1.44° of latitude (3 tiles), 2.9° of longitude at 60.3° (4).
		{"default view at the band edge", 160, 60.3, 3 * 4},
		// 160 km at 30°: 1.66° of longitude → 3 tiles.
		{"default view at 30", 160, 30, 3 * 3},
	}
	for _, c := range cases {
		if got := plannerWorstCaseTiles(c.sideKm, c.maxLat); got != c.want {
			t.Errorf("%s: plannerWorstCaseTiles(%v, %v) = %d, want %d", c.name, c.sideKm, c.maxLat, got, c.want)
		}
	}
	// Longitude never shrinks with latitude, and a span never needs fewer
	// tiles than a smaller one.
	for lat := 0.0; lat <= 85; lat += 5 {
		if a, b := plannerWorstCaseTiles(160, lat), plannerWorstCaseTiles(160, lat+5); a > b {
			t.Errorf("tiles at %v° (%d) > at %v° (%d)", lat, a, lat+5, b)
		}
	}
	if got := plannerWorstCaseTiles(100, 89.99); got < 180 {
		t.Errorf("near the pole: %d tiles, want the whole circle of longitude counted", got)
	}
}

// plannerTestSizes are the size limits the property tests run at: the
// defaults, the smallest and largest an operator can set, and one between.
var plannerTestSizes = []struct{ area, margin, leg float64 }{
	{defaultPlannerMaxAreaKm, defaultPlannerNodeMarginKm, defaultPlannerMaxLegKm},
	{200, 50, 150},
	{plannerAreaKmMax, plannerMarginKmMax, plannerLegKmMax},
	{plannerAreaKmMin, plannerMarginKmMin, plannerLegKmMin},
}

func TestPlannerTileCapsDefaults(t *testing.T) {
	got := plannerTileCaps(defaultPlannerMaxAreaKm, defaultPlannerNodeMarginKm, defaultPlannerMaxLegKm)
	// 100 km box + 30 km margin = 160 km; 100 km leg + 2×30 km pad = 160 km:
	// 3 × 4 tiles at the north edge of the SRTM band.
	if want := (plannerTileLimits{Profile: 12, Relay: 12, Coverage: 12}); got != want {
		t.Fatalf("caps %+v, want %+v", got, want)
	}
	if got.max() != 12 {
		t.Fatalf("max %d", got.max())
	}
	// Short legs keep the old floor of 6.
	if c := plannerTileCaps(defaultPlannerMaxAreaKm, defaultPlannerNodeMarginKm, 10); c.Relay != 6 || c.Profile != 6 {
		t.Fatalf("10 km legs: %+v, want relay/profile 6", c)
	}
	if n := terrainViewTiles(nil, &PlannerConfig{Enabled: true}); n != got.max() {
		t.Fatalf("a planner-only source accepts %d tiles per view, want %d", n, got.max())
	}
	p := newPlannerService(PlannerConfig{Enabled: true}, nil, nil)
	t.Cleanup(p.Close)
	if p.tiles != got {
		t.Fatalf("service caps %+v, want %+v", p.tiles, got)
	}
}

// The caps, the source's per-view cap and the cache floor follow the
// operator's sizes.
func TestPlannerTileCapsFromConfig(t *testing.T) {
	cfg := PlannerConfig{Enabled: true, MaxAreaKm: 200, NodeMarginKm: 50, MaxLegKm: 150}
	r, warns := cfg.resolved()
	if len(warns) != 0 {
		t.Fatalf("warnings %q", warns)
	}
	// 300 km: 2.7° (4 tiles) by 6.1° at 60.5° (8 tiles); 150 + 2×45 km:
	// 2.2° (4) by 4.4° (6).
	want := plannerTileLimits{Profile: 24, Relay: 24, Coverage: 28}
	if got := r.tileLimits(); got != want {
		t.Fatalf("caps %+v, want %+v", got, want)
	}
	p := newPlannerService(cfg, nil, nil)
	t.Cleanup(p.Close)
	src := p.src.(*srtmSource)
	if p.tiles != want || src.maxTiles != 28 || src.cache.maxItems != 28 {
		t.Fatalf("service caps %+v, source max %d, cache %d", p.tiles, src.maxTiles, src.cache.maxItems)
	}
	if n := terrainViewTiles(nil, &cfg); n != 28 {
		t.Fatalf("shared source view cap %d, want 28", n)
	}
	if c := (TerrainConfig{TileCacheTiles: 100}).cacheTiles(28); c != 100 {
		t.Fatalf("a larger configured cache: %d", c)
	}
}

// Review #2391 point 1: an 89 × 84 km box at 51° N needs 3 × 4 = 12 tiles
// once the 30 km node margin is added, and was refused at a cap of 9.
func TestPlannerCoverageReviewBoxAccepted(t *testing.T) {
	const bbox = "50.2,3.4,51.0,4.6"
	g, msg := plannerParseGridBox(bbox, defaultPlannerMaxAreaKm)
	if msg != "" {
		t.Fatalf("parse: %s", msg)
	}
	view := plannerExpandBBox(g.bbox(), defaultPlannerNodeMarginKm)
	caps := PlannerConfig{}.tileLimits()
	src := newSRTMSource(TerrainConfig{}, caps.max())
	if n := src.TileCount(view); n != 12 || n > caps.Coverage || n > src.maxTiles {
		t.Fatalf("view %+v: %d tiles, coverage cap %d, source cap %d", view, n, caps.Coverage, src.maxTiles)
	}

	h := newPlannerHarness(t)
	h.data.setNodes(append(plannerTestNodes(), nodeInfo{PublicKey: "be01", Name: "Brussels", Role: "repeater",
		Lat: 50.6, Lon: 4.0, HasGPS: true, LastSeen: plannerTestStart}))
	w := h.get("/api/planner/coverage?bbox="+bbox, "198.51.100.200")
	if w.Code != http.StatusAccepted && w.Code != http.StatusOK {
		t.Fatalf("review box: %d %s", w.Code, w.Body.String())
	}
	plannerWaitFor(t, "the job's terrain request", func() bool { return h.src.viewCount() == 1 })
	if v := h.src.lastBBox(); v != view {
		t.Fatalf("job view %+v, want %+v", v, view)
	}
}

// plannerTestCoverageBoxes calls fn with the coverage boxes at the size
// limit that plannerParseGridBox accepts: south edges on the 0.05° grid
// within the band, every sStride-th (a stride prime to 20 still meets every
// alignment to the degree lines), each height, the widest box the width
// check allows at that height, and every 0.05° west-edge alignment within a
// degree. A smaller box at the same corner lies inside one of these, so they
// are the worst case for the tile count.
func plannerTestCoverageBoxes(t *testing.T, maxAreaKm float64, sStride int, fn func(area geoBBox)) {
	t.Helper()
	parse := func(s, w, n, e int) bool {
		raw := fmt.Sprintf("%g,%g,%g,%g", float64(s)/plannerGridPerDeg, float64(w)/plannerGridPerDeg,
			float64(n)/plannerGridPerDeg, float64(e)/plannerGridPerDeg)
		g, msg := plannerParseGridBox(raw, maxAreaKm)
		if msg == "" && g != (plannerGridBox{s, w, n, e}) {
			t.Fatalf("%s snapped to %+v", raw, g)
		}
		return msg == ""
	}
	sMin := int(plannerCoverageMinLat * plannerGridPerDeg)
	nMax := int(plannerCoverageMaxLat * plannerGridPerDeg)
	hMax := int(maxAreaKm / plannerKmPerDeg * plannerGridPerDeg)
	boxes := 0
	for s := sMin; s < nMax; s += sStride {
		for h := 1; h <= hMax && s+h <= nMax; h++ {
			n := s + h
			mid := (float64(s) + float64(n)) / 2 / plannerGridPerDeg
			w0 := 20 // 1° E: any meridian will do, only the alignment matters
			wd := int(maxAreaKm/(plannerKmPerDeg*math.Cos(mid*math.Pi/180))*plannerGridPerDeg) + 1
			for wd > 0 && !parse(s, w0, n, w0+wd) {
				wd--
			}
			if wd == 0 || parse(s, w0, n, w0+wd+1) {
				t.Fatalf("no widest box at s=%d h=%d (width %d)", s, h, wd)
			}
			for off := 0; off < plannerGridPerDeg; off++ {
				if !parse(s, w0+off, n, w0+off+wd) {
					t.Fatalf("box s=%d h=%d w=%d at offset %d refused", s, h, wd, off)
				}
				fn(plannerGridBox{s, w0 + off, n, w0 + off + wd}.bbox())
				boxes++
			}
		}
	}
	if boxes == 0 {
		t.Fatal("no boxes")
	}
}

// Every coverage box the parser accepts fits the coverage cap, at every
// tested size, and the cap is reached: it is the worst case, not a guess.
func TestPlannerCoverageTileCapHoldsEverywhere(t *testing.T) {
	for i, sz := range plannerTestSizes {
		caps := plannerTileCaps(sz.area, sz.margin, sz.leg)
		stride := 1
		if i > 0 {
			stride = 3
		}
		worst := 0
		plannerTestCoverageBoxes(t, sz.area, stride, func(area geoBBox) {
			n := plannerTestTiles(plannerExpandBBox(area, sz.margin))
			if n > caps.Coverage {
				t.Fatalf("%+v: box %+v: view needs %d tiles, cap %d", sz, area, n, caps.Coverage)
			}
			worst = max(worst, n)
		})
		if worst != caps.Coverage {
			t.Errorf("%+v: largest view needs %d tiles, cap %d: the cap is not the worst case", sz, worst, caps.Coverage)
		}
	}
}

// Every relay corridor and single profile leg of at most the maximum length,
// with both ends in the band, fits its cap, at every tested size.
func TestPlannerRelayProfileTileCapsHoldEverywhere(t *testing.T) {
	for i, sz := range plannerTestSizes {
		caps := plannerTileCaps(sz.area, sz.margin, sz.leg)
		pad := plannerProfilePad(sz.leg)
		step := 0.25
		if i > 0 {
			step = 0.65
		}
		legs := 0
		for lat := plannerCoverageMinLat; lat <= plannerCoverageMaxLat; lat += step {
			for off := 0.0; off < 1; off += 0.05 {
				a := geoPoint{lat, 20 + off}
				for bearing := 0.0; bearing < 360; bearing += 15 {
					for _, km := range []float64{sz.leg, sz.leg / 2} {
						b := plannerTestDest(a, bearing, km)
						qa, ok1 := plannerLatLon(fmt.Sprint(a.Lat), fmt.Sprint(a.Lon))
						qb, ok2 := plannerLatLon(fmt.Sprint(b.Lat), fmt.Sprint(b.Lon))
						if !ok1 || !ok2 || qb.Lat < plannerCoverageMinLat || qb.Lat > plannerCoverageMaxLat {
							continue
						}
						if d := geoDistKm(qa, qb); d > sz.leg {
							// Rounding to 0.001° lengthened it: the handler says too_long.
							continue
						}
						legs++
						if n := plannerTestTiles(plannerRelayView(qa, qb)); n > caps.Relay {
							t.Fatalf("%+v: relay %+v → %+v: %d tiles, cap %d", sz, qa, qb, n, caps.Relay)
						}
						pv := plannerProfileView(plannerBBoxAdd(geoBBox{qa.Lat, qa.Lon, qa.Lat, qa.Lon}, qb), pad)
						if n := plannerTestTiles(pv); n > caps.Profile {
							t.Fatalf("%+v: profile %+v → %+v: %d tiles, cap %d", sz, qa, qb, n, caps.Profile)
						}
					}
				}
			}
		}
		if legs < 30000 {
			t.Fatalf("%+v: only %d legs checked", sz, legs)
		}
	}
}

// The profile pad covers the great-circle bulge of the longest leg: every
// point the profile samples lies inside its view.
func TestPlannerProfilePadCoversBulge(t *testing.T) {
	for _, c := range []struct{ leg, want float64 }{{10, 0.01}, {100, 0.01}, {150, 0.0225}, {200, 0.04}} {
		if got := plannerProfilePad(c.leg); math.Abs(got-c.want) > 1e-12 {
			t.Errorf("plannerProfilePad(%v) = %v, want %v", c.leg, got, c.want)
		}
	}
	for _, sz := range plannerTestSizes {
		pad := plannerProfilePad(sz.leg)
		for lat := plannerCoverageMinLat; lat <= plannerCoverageMaxLat; lat += 1 {
			for bearing := 0.0; bearing < 180; bearing += 15 {
				a := geoPoint{lat, 20.5}
				b := plannerTestDest(a, bearing, sz.leg)
				view := plannerProfileView(plannerBBoxAdd(geoBBox{a.Lat, a.Lon, a.Lat, a.Lon}, b), pad)
				for _, pt := range greatCircleSamples(a, b, planProfileSamples(sz.leg)) {
					if !plannerBBoxContains(view, pt.Lat, pt.Lon) {
						t.Fatalf("leg %v km from %+v on %v°: sample %+v outside %+v", sz.leg, a, bearing, pt, view)
					}
				}
			}
		}
	}
}

// A view over its endpoint's cap is too_many_tiles, not area_too_large, and
// refused before any job or token; area_too_large stays for a box side over
// the size limit.
func TestPlannerTooManyTiles(t *testing.T) {
	for _, tc := range []struct {
		name, path string
		set        func(l *plannerTileLimits, n int)
	}{
		{"profile", plannerProfPath, func(l *plannerTileLimits, n int) { l.Profile = n }},
		{"relay", plannerRelayPath, func(l *plannerTileLimits, n int) { l.Relay = n }},
		{"coverage", plannerCovPath, func(l *plannerTileLimits, n int) { l.Coverage = n }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newPlannerHarness(t)
			// The test views are 2×2 tiles (29-30 N × 98-97 W) or smaller.
			need := 0
			h.src.set(func(f *fakeTerrain) {
				f.tiles = func(bb geoBBox) int { need = len(fakeTiles(bb)); return need }
			})
			tc.set(&h.p.tiles, 0)
			w := h.get(tc.path, "198.51.100.201")
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), plannerErrTooManyTiles) ||
				strings.Contains(w.Body.String(), plannerErrAreaTooLarge) {
				t.Fatalf("over the cap: %d %s", w.Code, w.Body.String())
			}
			if h.liveJobs() != 0 || h.tokens("198.51.100.201") != plannerIPJobsPerMin {
				t.Fatal("a refused request started a job or took a token")
			}
			// At exactly the cap it runs.
			tc.set(&h.p.tiles, need)
			if w := h.get(tc.path, "198.51.100.201"); w.Code != http.StatusAccepted && w.Code != http.StatusOK {
				t.Fatalf("at the cap (%d): %d %s", need, w.Code, w.Body.String())
			}
		})
	}

	// A terrain source that refuses the view fails the job the same way.
	h := newPlannerHarness(t)
	h.p.fastWait = plannerFastWait
	h.src.set(func(f *fakeTerrain) { f.err = errTerrainTooManyTiles })
	w := h.get(plannerCovPath, "198.51.100.202")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), plannerErrTooManyTiles) {
		t.Fatalf("source refusal: %d %s", w.Code, w.Body.String())
	}
	if got := plannerOutcome(fmt.Errorf("wrapped: %w", errTerrainTooManyTiles)); got != "too_many_tiles" {
		t.Fatalf("outcome %q", got)
	}

	// The size limit keeps its own code.
	h = newPlannerHarness(t)
	w = h.get("/api/planner/coverage?bbox=30.0,-97.2,31.0,-96.85", "198.51.100.203")
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), plannerErrAreaTooLarge) {
		t.Fatalf("oversized box: %d %s", w.Code, w.Body.String())
	}
}

// The source enforces its own per-view cap: one tile more is refused
// without a request, and is never resident.
func TestSRTMSourceMaxTiles(t *testing.T) {
	s, u, _ := newSRTMTest(t, 0, srtmServe(nil))
	strip := func(n int) geoBBox { return geoBBox{S: 30.2, W: -99.5, N: 30.8, E: -100 + float64(n)} }
	if got := s.TileCount(strip(s.maxTiles + 1)); got != s.maxTiles+1 {
		t.Fatalf("strip of %d: %d tiles", s.maxTiles+1, got)
	}
	if _, err := s.View(context.Background(), strip(s.maxTiles+1)); err != errTerrainTooManyTiles {
		t.Fatalf("View over the cap: %v", err)
	}
	if s.Resident(strip(s.maxTiles + 1)) {
		t.Fatal("a view over the cap is resident")
	}
	if n := u.total(); n != 0 {
		t.Fatalf("refused view made %d requests", n)
	}
}

// terrainBoth is route LOS and the planner over one test source and one
// per-client download budget, wired as newTerrainServices wires them, on the
// source's clock.
type terrainBoth struct {
	*elevTest
	p    *plannerService
	data *fakePlannerData
}

func newTerrainBoth(t *testing.T, h http.HandlerFunc) *terrainBoth {
	t.Helper()
	et := newElevTest(t, h)
	cfg := &Config{LOS: &LOSConfig{Enabled: true}, Planner: &PlannerConfig{Enabled: true}}
	et.srv.cfg = cfg
	p := newPlannerService(*cfg.Planner, et.srv, et.src)
	t.Cleanup(p.Close)
	data := &fakePlannerData{serverPlannerData: serverPlannerData{et.srv}}
	p.data = data
	p.now = et.clk.now
	p.limit.rl.now = et.clk.now
	p.global.now = et.clk.now
	et.svc.downloads = newTerrainClientLimiter(nil, terrainViewTiles(cfg.LOS, cfg.Planner))
	et.svc.downloads.rl.now = et.clk.now
	p.downloads = et.svc.downloads
	et.srv.planner = p
	return &terrainBoth{elevTest: et, p: p, data: data}
}

// known makes the points of each pts list repeaters the planner knows. Call
// it before the first planner request: the planner rebuilds its node set
// only every plannerKnownTTL.
func (tb *terrainBoth) known(ptsLists ...string) {
	var ns []nodeInfo
	for _, pts := range ptsLists {
		for _, pt := range elevParsePts(pts) {
			ns = append(ns, nodeInfo{PublicKey: fmt.Sprintf("kn%04d", len(ns)), Name: "N", Role: "repeater",
				Lat: pt.Lat, Lon: pt.Lon, HasGPS: true, LastSeen: tb.clk.now()})
		}
	}
	tb.data.setNodes(append(tb.data.nodes(), ns...))
}

// profile asks the planner for a profile of pts from ip, once.
func (tb *terrainBoth) profile(pts, ip string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/planner/profile?pts="+url.QueryEscape(pts), nil)
	req.RemoteAddr = ip + ":5000"
	w := httptest.NewRecorder()
	tb.srv.handlePlannerProfile(w, req)
	return w
}

// profileDone polls the planner profile until it answers 200.
func (tb *terrainBoth) profileDone(t *testing.T, pts, ip string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		w := tb.profile(pts, ip)
		if w.Code == http.StatusOK {
			return
		}
		if w.Code != http.StatusAccepted || time.Now().After(deadline) {
			t.Fatalf("planner %s from %s: %d %s", pts, ip, w.Code, w.Body.String())
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (tb *terrainBoth) tokens(ip string) float64 { return rlTokens(tb.p.downloads.rl, ip) }

// Security review (#2391): a planner job cost one token however many tiles
// its view downloaded. It now also takes one token per download its view
// starts, from the budget route LOS charges, all or nothing; tiles already
// in memory or already downloading cost nothing.
func TestPlannerChargesTileDownloads(t *testing.T) {
	gate := newSRTMGate()
	tb := newTerrainBoth(t, func(w http.ResponseWriter, r *http.Request) {
		if gate.wait(r) {
			http.NotFound(w, r)
		}
	})
	t.Cleanup(gate.open)
	const (
		cold    = "30.8,-97.2,31.2,-96.8"     // N30-N31 × W098-W097: 4 tiles
		overlap = "30.85,-97.15,31.15,-96.85" // the same 4 tiles, another key
		here    = "40.3,10.3,40.6,10.6"       // N40E010 only
		refused = "50.8,5.8,51.2,6.2"         // N50-N51 × E005-E006: 4 tiles
		busy    = "52.8,5.8,53.2,6.2"         // N52-N53 × E005-E006: 4 tiles
	)
	tb.known(cold, overlap, here, refused, busy)
	tb.src.put(srtmKey{31, -97}, elevFlatGrid(80))
	tb.src.put(srtmKey{40, 10}, elevFlatGrid(80))
	const ip = "198.51.100.150"
	burst := tb.p.downloads.rl.burst

	// 3 of the 4 tiles are not here: 3 tokens, and one job token.
	if w := tb.profile(cold, ip); w.Code != http.StatusAccepted {
		t.Fatalf("cold view: %d %s", w.Code, w.Body.String())
	}
	if got := tb.tokens(ip); got != burst-3 {
		t.Fatalf("cold view: %v download tokens left, want %v", got, burst-3)
	}
	srtmEventually(t, "the 3 downloads to start", func() bool {
		tb.src.mu.Lock()
		defer tb.src.mu.Unlock()
		return len(tb.src.loading) == 3
	})
	jobs := rlTokens(tb.p.limit.rl, ip)

	// Polling the job, a view over the same tiles while they download, and a
	// view in memory cost no download token.
	if w := tb.profile(cold, ip); w.Code != http.StatusAccepted {
		t.Fatalf("poll: %d", w.Code)
	}
	if w := tb.profile(overlap, ip); w.Code != http.StatusAccepted {
		t.Fatalf("overlapping view: %d %s", w.Code, w.Body.String())
	}
	tb.p.fastWait = 10 * time.Second
	if w := tb.profile(here, ip); w.Code != http.StatusOK {
		t.Fatalf("resident view: %d %s", w.Code, w.Body.String())
	}
	if got := tb.tokens(ip); got != burst-3 || rlTokens(tb.p.limit.rl, ip) != jobs-2 {
		t.Fatalf("free requests: %v download tokens, %v job tokens (want %v, %v)", got, rlTokens(tb.p.limit.rl, ip), burst-3, jobs-2)
	}

	// Too few download tokens: 429, the job token back, nothing started.
	tb.p.downloads.rl.takeN(ip, int(burst)-3-2) // 2 left, the view needs 4
	jobs = rlTokens(tb.p.limit.rl, ip)
	w := tb.profile(refused, ip)
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "rate limited") || w.Header().Get("Retry-After") == "" {
		t.Fatalf("over the download budget: %d %s", w.Code, w.Body.String())
	}
	if rlTokens(tb.p.limit.rl, ip) != jobs || tb.tokens(ip) != 2 || tb.p.src.Starts(plannerProfileView(geoBBox{S: 50.8, W: 5.8, N: 51.2, E: 6.2},
		plannerProfilePad(defaultPlannerMaxLegKm))) != 4 {
		t.Fatalf("refused job: %v job tokens (want %v), %v download tokens", rlTokens(tb.p.limit.rl, ip), jobs, tb.tokens(ip))
	}

	// The global job budget refusing gives both tokens back.
	tb.clk.add(time.Minute) // 2 + 10 download tokens
	tb.p.global = newRateLimiter(1, time.Hour)
	tb.p.global.now = tb.clk.now
	tb.p.global.take("all")
	if w := tb.profile(busy, ip); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("global budget out: %d %s", w.Code, w.Body.String())
	}
	if ok, _ := tb.p.downloads.rl.takeN(ip, 12); !ok || rlTokens(tb.p.limit.rl, ip) != plannerIPJobsPerMin {
		t.Fatalf("not refunded: %v download tokens, %v job tokens", tb.tokens(ip), rlTokens(tb.p.limit.rl, ip))
	}
	if tb.liveJobs() != 2 {
		t.Fatalf("%d live jobs, want the 2 loading ones", tb.liveJobs())
	}
}

func (tb *terrainBoth) liveJobs() int {
	tb.p.mu.Lock()
	defer tb.p.mu.Unlock()
	return len(tb.p.jobs)
}

// Code review #2 (#2391): at the largest sizes an operator may set, one cold
// coverage view spans 40 tiles, twice the old 20-a-minute download budget,
// so its first request always failed busy and the key was refused for 30 s.
// The budget now fits the largest view, and such a view loads in one go.
func TestPlannerLargestViewLoadsCold(t *testing.T) {
	terrainQuietLog(t)
	cfg := &PlannerConfig{Enabled: true, MaxAreaKm: plannerAreaKmMax, NodeMarginKm: plannerMarginKmMax, MaxLegKm: plannerLegKmMax}
	r, warns := cfg.resolved()
	if len(warns) != 0 {
		t.Fatalf("warnings %q", warns)
	}
	g, msg := plannerParseGridBox("55.3,1.95,57.5,6", r.MaxAreaKm)
	if msg != "" {
		t.Fatal(msg)
	}
	view := plannerExpandBBox(g.bbox(), r.NodeMarginKm)
	caps := r.tileLimits()
	if n := plannerTestTiles(view); n != 40 || n != caps.Coverage || n <= srtmPerMinute {
		t.Fatalf("view %+v: %d tiles, coverage cap %d", view, n, caps.Coverage)
	}
	// The shared source, as newTerrainServices builds it, budgets for it.
	both := &Config{LOS: &LOSConfig{Enabled: true}, Planner: cfg}
	_, p := newTerrainServices(both, &Server{cfg: both})
	t.Cleanup(p.Close)
	if b := p.src.(*srtmSource).budget; b.perMinute != 40 || b.perHour != 200 {
		t.Fatalf("budget %d/min, %d/hour for 40-tile views", b.perMinute, b.perHour)
	}

	s, u, _ := newSRTMTestViews(t, 0, terrainViewTiles(both.LOS, cfg), srtmServe(nil)) // every tile 404s
	if n := s.Starts(view); n != 40 {
		t.Fatalf("Starts = %d, want all 40", n)
	}
	if _, err := s.View(context.Background(), view); err != nil {
		t.Fatalf("cold 40-tile view: %v", err)
	}
	if u.total() != 40 || !s.Resident(view) {
		t.Fatalf("%d downloads, resident %v", u.total(), s.Resident(view))
	}
}
