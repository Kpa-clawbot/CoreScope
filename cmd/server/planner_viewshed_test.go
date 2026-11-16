package main

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"sort"
	"testing"
)

// flatTerrainView is level ground everywhere.
type flatTerrainView struct{ elevM float64 }

func (v flatTerrainView) ElevationAt(lat, lon float64) (float64, bool) { return v.elevM, true }

// ridgeTerrainView is level ground with a north-south ridge between two
// longitudes.
type ridgeTerrainView struct {
	baseM, ridgeM float64
	west, east    float64
}

func (v ridgeTerrainView) ElevationAt(lat, lon float64) (float64, bool) {
	if lon >= v.west && lon <= v.east {
		return v.baseM + v.ridgeM, true
	}
	return v.baseM, true
}

var viewshedTestSite = geoPoint{Lat: 30.5, Lon: -97.5}

func viewshedTestParams(rangeKm float64) viewshedParams {
	return viewshedParams{
		Site: viewshedTestSite, SiteM: 10, RxM: 5, RangeKm: rangeKm,
		FreqMHz: 869.525, KFactor: 4.0 / 3.0,
		Reach: PlannerUserReach{D50Km: defaultPlannerUserD50Km, Slope: defaultPlannerUserSlope},
	}
}

// viewshedCellCenter mirrors the documented grid layout: row 0 north,
// row-major, cell centres at the middle of equal bbox divisions.
func viewshedCellCenter(g viewshedGrid, r, c int) geoPoint {
	return geoPoint{
		Lat: g.BBox.N - (float64(r)+0.5)*(g.BBox.N-g.BBox.S)/float64(g.Rows),
		Lon: g.BBox.W + (float64(c)+0.5)*(g.BBox.E-g.BBox.W)/float64(g.Cols),
	}
}

func mustViewshed(t testing.TB, v terrainView, p viewshedParams) viewshedGrid {
	t.Helper()
	g, err := computeViewshed(context.Background(), v, p, nil)
	if err != nil {
		t.Fatalf("computeViewshed: %v", err)
	}
	if len(g.Cells) != g.Rows*g.Cols || g.Rows == 0 || g.Cols == 0 {
		t.Fatalf("grid %dx%d with %d cells", g.Rows, g.Cols, len(g.Cells))
	}
	return g
}

func TestUserReachP(t *testing.T) {
	r := PlannerUserReach{D50Km: 2.7, Slope: 0.30}
	if p := userReachP(2.7, r); math.Abs(p-0.5) > 1e-9 {
		t.Errorf("P(2.7) = %v, want 0.5", p)
	}
	if p := userReachP(1.0, r); !(p > 0.95) {
		t.Errorf("P(1) = %v, want > 0.95", p)
	}
	if p := userReachP(10, r); !(p < 0.02) {
		t.Errorf("P(10) = %v, want < 0.02", p)
	}
}

func TestUserReachClassBounds(t *testing.T) {
	for _, tc := range []struct {
		p    float64
		want byte
	}{{0.0999, 1}, {0.10, 2}, {0.2499, 2}, {0.25, 3}, {0.50, 4}, {0.75, 5}} {
		if got := userReachClass(tc.p); got != tc.want {
			t.Errorf("userReachClass(%v) = %d, want %d", tc.p, got, tc.want)
		}
	}
}

func TestVerdictClass(t *testing.T) {
	for v, want := range map[string]byte{"blocked": 1, "marginal": 2, "clear60": 3, "clear": 4} {
		if got := verdictClass(v); got != want {
			t.Errorf("verdictClass(%q) = %d, want %d", v, got, want)
		}
	}
}

func TestViewshedCellKm(t *testing.T) {
	if got := viewshedCellKm(10); got != 0.2 {
		t.Errorf("viewshedCellKm(10) = %v, want 0.2", got)
	}
	if got := viewshedCellKm(100); math.Abs(got-100.0/150) > 1e-12 {
		t.Errorf("viewshedCellKm(100) = %v, want %v", got, 100.0/150)
	}
}

// On flat ground the Repeaters verdict depends on distance only. With the
// planner's default heights (10 m site, 5 m receiver) the first Fresnel zone
// already grazes the ground well inside 3 km, so the "clear within 3 km"
// check runs with masts high enough for it (30 m / 15 m); both cases check
// the analyzeLOS equality, the plain Users curve and monotonic classes.
func TestViewshedFlatTerrainIsPlainCurve(t *testing.T) {
	for _, tc := range []struct {
		name          string
		siteM, rxM    float64
		clearWithinKm float64 // 0: not asserted
	}{
		{"masts 30m/15m", 30, 15, 3},
		{"defaults 10m/5m", 10, 5, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := viewshedTestParams(30)
			p.SiteM, p.RxM = tc.siteM, tc.rxM
			checkFlatViewshed(t, p, tc.clearWithinKm)
		})
	}
}

func checkFlatViewshed(t *testing.T, p viewshedParams, clearWithinKm float64) {
	const elev = 50.0
	v := flatTerrainView{elevM: elev}
	g := mustViewshed(t, v, p)
	if g.Rows > 305 || g.Cols > 305 {
		t.Errorf("grid %dx%d exceeds ~300x300", g.Rows, g.Cols)
	}
	lp := losParams{FreqMHz: p.FreqMHz, KFactor: p.KFactor, AntennaAM: p.SiteM, AntennaBM: p.RxM}
	type cell struct {
		d   float64
		rep byte
	}
	var cells []cell
	for r := 0; r < g.Rows; r++ {
		for c := 0; c < g.Cols; c++ {
			pt := viewshedCellCenter(g, r, c)
			d := geoDistKm(p.Site, pt)
			b := g.Cells[r*g.Cols+c]
			if d > p.RangeKm {
				continue
			}
			users, rep := b&0x0f, b>>4
			if want := userReachClass(userReachP(d, p.Reach)); users != want {
				t.Fatalf("cell (%d,%d) at %.2f km: users %d, want plain curve %d", r, c, d, users, want)
			}
			profile := make([]float64, planProfileSamples(d))
			for i := range profile {
				profile[i] = elev
			}
			res, ok := analyzeLOS(profile, d, lp)
			if !ok {
				t.Fatalf("analyzeLOS failed at %.2f km", d)
			}
			if want := verdictClass(res.Verdict); rep != want {
				t.Fatalf("cell (%d,%d) at %.2f km: repeaters %d, want %d (%s)", r, c, d, rep, want, res.Verdict)
			}
			if d <= clearWithinKm && rep != 4 {
				t.Fatalf("cell (%d,%d) at %.2f km: repeaters %d, want 4 within %.0f km", r, c, d, rep, clearWithinKm)
			}
			cells = append(cells, cell{d, rep})
		}
	}
	if len(cells) == 0 {
		t.Fatal("no cell in range")
	}
	// Flat terrain: the class depends on distance only, so along every ray
	// (and across all of them) it never increases with distance.
	sort.Slice(cells, func(i, j int) bool { return cells[i].d < cells[j].d })
	for i := 1; i < len(cells); i++ {
		if cells[i].rep > cells[i-1].rep {
			t.Fatalf("repeaters class rises from %d at %.3f km to %d at %.3f km",
				cells[i-1].rep, cells[i-1].d, cells[i].rep, cells[i].d)
		}
	}
	// First distance at which each class appears, for the report.
	first := map[byte]float64{}
	for _, c := range cells {
		if _, seen := first[c.rep]; !seen {
			first[c.rep] = c.d
		}
	}
	t.Logf("site %.0f m / rx %.0f m: repeaters class first seen at km %v (4 clear, 3 clear60, 2 marginal, 1 blocked)",
		p.SiteM, p.RxM, first)

	// R2: where earth curvature alone puts the handheld path into deep
	// shadow on flat terrain, up to the largest range the planner offers.
	e := newPlanLinkEval(context.Background(), v, p.FreqMHz, p.KFactor)
	for d := 0.1; d <= defaultPlannerMaxLegKm; d += 0.1 {
		pt := geoPoint{Lat: p.Site.Lat, Lon: p.Site.Lon + d/(plannerKmPerDeg*math.Cos(p.Site.Lat*math.Pi/180))}
		dd := geoDistKm(p.Site, pt)
		if c, _ := e.clearance(p.Site, pt, dd, p.SiteM, viewshedUserRxM, math.Inf(-1)); c < viewshedShadowClearance {
			t.Logf("site %.0f m: users-layer curvature shadow starts at %.1f km (plain curve class there %d)",
				p.SiteM, dd, userReachClass(userReachP(dd, p.Reach)))
			return
		}
	}
	t.Logf("site %.0f m: no users-layer curvature shadow within %.0f km", p.SiteM, defaultPlannerMaxLegKm)
}

func TestViewshedRidgeShadow(t *testing.T) {
	p := viewshedTestParams(30)
	// A wider handheld reach so the curve is still high 10 km out and the
	// shadow drop is visible above the class-1 floor.
	p.Reach = PlannerUserReach{D50Km: 20, Slope: 0.30}
	cosLat := math.Cos(p.Site.Lat * math.Pi / 180)
	kmToLon := func(km float64) float64 { return km / (plannerKmPerDeg * cosLat) }
	ridge := ridgeTerrainView{
		baseM: 50, ridgeM: 300,
		west: p.Site.Lon + kmToLon(4.8), east: p.Site.Lon + kmToLon(5.2),
	}
	flat := mustViewshed(t, flatTerrainView{elevM: 50}, p)
	g := mustViewshed(t, ridge, p)
	if flat.Rows != g.Rows || flat.Cols != g.Cols {
		t.Fatalf("grid shape differs: %dx%d vs %dx%d", flat.Rows, flat.Cols, g.Rows, g.Cols)
	}
	east, west := 0, 0
	for r := 0; r < g.Rows; r++ {
		for c := 0; c < g.Cols; c++ {
			pt := viewshedCellCenter(g, r, c)
			if math.Abs(pt.Lat-p.Site.Lat)*plannerKmPerDeg > 1 {
				continue
			}
			dx := (pt.Lon - p.Site.Lon) * plannerKmPerDeg * cosLat
			i := r*g.Cols + c
			fu, gu, gr := flat.Cells[i]&0x0f, g.Cells[i]&0x0f, g.Cells[i]>>4
			switch {
			case math.Abs(dx-10) <= 0.5:
				east++
				if gr != 1 {
					t.Errorf("east cell (%d,%d): repeaters %d, want 1 (blocked)", r, c, gr)
				}
				if fu < 1+viewshedShadowDrop {
					t.Fatalf("east cell (%d,%d): flat users %d leaves no room for the drop", r, c, fu)
				}
				if gu > fu-viewshedShadowDrop {
					t.Errorf("east cell (%d,%d): users %d, want at most %d (flat %d)", r, c, gu, fu-viewshedShadowDrop, fu)
				}
			case math.Abs(dx+10) <= 0.5:
				west++
				if g.Cells[i] != flat.Cells[i] {
					t.Errorf("west cell (%d,%d): %#x, want flat %#x", r, c, g.Cells[i], flat.Cells[i])
				}
			}
		}
	}
	if east == 0 || west == 0 {
		t.Fatalf("checked %d east and %d west cells", east, west)
	}
}

// Runs at the default heights and at mast heights so every verdict class
// is in the sample: 200 cells each.
func TestViewshedAgreesWithAnalyzeLOS(t *testing.T) {
	verdicts := map[string]int{}
	for _, h := range [][2]float64{{10, 5}, {30, 15}} {
		p := viewshedTestParams(30)
		p.SiteM, p.RxM = h[0], h[1]
		checkViewshedAgainstLOS(t, p, verdicts)
	}
	if len(verdicts) < 4 {
		t.Errorf("sample covers only verdicts %v; want all four", verdicts)
	}
	t.Logf("verdicts sampled: %v", verdicts)
}

func checkViewshedAgainstLOS(t *testing.T, p viewshedParams, verdicts map[string]int) {
	v := fakeTerrainView{bb: plannerExpandBBox(viewshedBBox(p.Site, p.RangeKm), 1)}
	g := mustViewshed(t, v, p)
	lp := losParams{FreqMHz: p.FreqMHz, KFactor: p.KFactor, AntennaAM: p.SiteM, AntennaBM: p.RxM}
	rng := rand.New(rand.NewSource(2391))
	for checked := 0; checked < 200; {
		r, c := rng.Intn(g.Rows), rng.Intn(g.Cols)
		pt := viewshedCellCenter(g, r, c)
		d := geoDistKm(p.Site, pt)
		if d > p.RangeKm {
			continue
		}
		checked++
		prof, ok := sampleTerrainProfile(v, p.Site, pt, planProfileSamples(d))
		if !ok {
			t.Fatalf("no profile to cell (%d,%d)", r, c)
		}
		res, ok := analyzeLOS(prof, d, lp)
		if !ok {
			t.Fatalf("analyzeLOS failed for cell (%d,%d)", r, c)
		}
		verdicts[res.Verdict]++
		if got, want := g.Cells[r*g.Cols+c]>>4, verdictClass(res.Verdict); got != want {
			t.Errorf("cell (%d,%d) at %.2f km: repeaters %d, analyzeLOS %d (%s, clearance %.4f)",
				r, c, d, got, want, res.Verdict, res.Clearance)
		}
	}
}

func TestViewshedMissingTerrainIsNoData(t *testing.T) {
	p := viewshedTestParams(30)
	p.Site = geoPoint{Lat: 30.9, Lon: -97.5}
	bb := plannerExpandBBox(viewshedBBox(p.Site, p.RangeKm), 1)
	v := fakeTerrainView{bb: bb, missing: map[[2]int]bool{{31, -98}: true}}
	g := mustViewshed(t, v, p)
	missing, classed := 0, 0
	for r := 0; r < g.Rows; r++ {
		for c := 0; c < g.Cols; c++ {
			pt := viewshedCellCenter(g, r, c)
			b := g.Cells[r*g.Cols+c]
			if v.MissingAt(pt.Lat, pt.Lon) {
				missing++
				if b != 0 {
					t.Fatalf("cell (%d,%d) on missing terrain has %#x, want 0", r, c, b)
				}
			} else if b != 0 {
				classed++
			}
		}
	}
	if missing == 0 || classed == 0 {
		t.Fatalf("%d cells on missing terrain, %d classed elsewhere", missing, classed)
	}
}

func TestViewshedMissingSiteIsAllNoData(t *testing.T) {
	p := viewshedTestParams(10)
	v := fakeTerrainView{bb: plannerExpandBBox(viewshedBBox(p.Site, p.RangeKm), 1),
		missing: map[[2]int]bool{{30, -98}: true}}
	g := mustViewshed(t, v, p)
	for i, b := range g.Cells {
		if b != 0 {
			t.Fatalf("cell %d = %#x with the site on missing terrain", i, b)
		}
	}
}

func TestViewshedOutsideRangeIsNoData(t *testing.T) {
	p := viewshedTestParams(20)
	g := mustViewshed(t, flatTerrainView{elevM: 50}, p)
	for _, rc := range [][2]int{{0, 0}, {0, g.Cols - 1}, {g.Rows - 1, 0}, {g.Rows - 1, g.Cols - 1}} {
		if b := g.Cells[rc[0]*g.Cols+rc[1]]; b != 0 {
			t.Errorf("corner cell %v = %#x, want 0", rc, b)
		}
	}
	mid := g.Cells[(g.Rows/2)*g.Cols+g.Cols/2]
	if mid&0x0f == 0 || mid>>4 == 0 {
		t.Errorf("centre cell = %#x, want both layers classed", mid)
	}
}

func TestViewshedHonoursContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := computeViewshed(ctx, flatTerrainView{elevM: 50}, viewshedTestParams(30), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestViewshedReportsProgress(t *testing.T) {
	var last float64
	calls := 0
	_, err := computeViewshed(context.Background(), flatTerrainView{elevM: 50}, viewshedTestParams(10),
		func(f float64) {
			if f < last {
				t.Errorf("progress went back from %v to %v", last, f)
			}
			last = f
			calls++
		})
	if err != nil {
		t.Fatal(err)
	}
	if calls == 0 || last != 1 {
		t.Errorf("progress: %d calls, last %v; want calls ending at 1", calls, last)
	}
}

func benchmarkViewshed(b *testing.B, rangeKm float64) {
	p := viewshedTestParams(rangeKm)
	v := fakeTerrainView{bb: plannerExpandBBox(viewshedBBox(p.Site, p.RangeKm), 1)}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := computeViewshed(context.Background(), v, p, nil); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkViewshed30km(b *testing.B) { benchmarkViewshed(b, 30) }

// BenchmarkViewshedMaxLeg: the largest range the planner offers (60 km).
func BenchmarkViewshedMaxLeg(b *testing.B) { benchmarkViewshed(b, 60) }
