package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"sync/atomic"
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

func TestRepeaterClass(t *testing.T) {
	for _, tc := range []struct {
		c    float64
		want byte
	}{
		{1.5, 3}, {1, 3}, {0.6, 3}, {0, 3},
		{-0.0001, 2}, {-0.2, 2},
		{-0.2001, 1}, {-1, 1}, {-6, 1},
	} {
		if got := repeaterClass(tc.c); got != tc.want {
			t.Errorf("repeaterClass(%v) = %d, want %d", tc.c, got, tc.want)
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
			if want := repeaterClass(res.Clearance); rep != want {
				t.Fatalf("cell (%d,%d) at %.2f km: repeaters %d, want %d (%s)", r, c, d, rep, want, res.Verdict)
			}
			if d <= clearWithinKm && rep != 3 {
				t.Fatalf("cell (%d,%d) at %.2f km: repeaters %d, want 3 within %.0f km", r, c, d, rep, clearWithinKm)
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
		if got, want := g.Cells[r*g.Cols+c]>>4, repeaterClass(res.Clearance); got != want {
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

// computeViewshedRef is the original single-goroutine, two-walk
// computeViewshed, kept as the oracle the optimised one must match byte for
// byte. computeViewshed profiles site→cell for every cell in range: two
// clearance walks per cell (receiver at RxM for Repeaters, at
// viewshedUserRxM for the Users shadow test). progress gets the fraction of
// rows done. Errors: ctx.Err() only.
func computeViewshedRef(ctx context.Context, v terrainView, p viewshedParams, progress func(float64)) (viewshedGrid, error) {
	cellKm := viewshedCellKm(p.RangeKm)
	bb := viewshedBBox(p.Site, p.RangeKm)
	cosLat := math.Cos(p.Site.Lat * math.Pi / 180)
	rows := max(1, int(math.Ceil((bb.N-bb.S)*plannerKmPerDeg/cellKm)))
	cols := max(1, int(math.Ceil((bb.E-bb.W)*plannerKmPerDeg*cosLat/cellKm)))
	g := viewshedGrid{BBox: bb, Rows: rows, Cols: cols, CellKm: cellKm, Cells: make([]byte, rows*cols)}
	if err := ctx.Err(); err != nil {
		return viewshedGrid{}, err
	}
	if _, ok := v.ElevationAt(p.Site.Lat, p.Site.Lon); !ok {
		return g, nil // no terrain at the site: every cell is no data
	}
	e := newPlanLinkEval(ctx, v, p.FreqMHz, p.KFactor)
	dLat := (bb.N - bb.S) / float64(rows)
	dLon := (bb.E - bb.W) / float64(cols)
	for r := 0; r < rows; r++ {
		lat := bb.N - (float64(r)+0.5)*dLat
		for c := 0; c < cols; c++ {
			cell := geoPoint{Lat: lat, Lon: bb.W + (float64(c)+0.5)*dLon}
			d := geoDistKm(p.Site, cell)
			if d > p.RangeKm {
				continue
			}
			cr, ok := e.clearance(p.Site, cell, d, p.SiteM, p.RxM, math.Inf(-1))
			if !ok {
				continue // no terrain on the path (or ctx cancelled: checked below)
			}
			users := userReachClass(userReachP(d, p.Reach))
			// Same path as above, so terrain is present; the walk stops at
			// the first sample below the shadow threshold.
			if cu, ok := e.clearance(p.Site, cell, d, p.SiteM, viewshedUserRxM, viewshedShadowClearance); !ok && cu < viewshedShadowClearance {
				if users > viewshedShadowDrop {
					users -= viewshedShadowDrop
				} else {
					users = 1
				}
			}
			g.Cells[r*cols+c] = users | repeaterClass(cr)<<4
		}
		if e.err != nil {
			return viewshedGrid{}, e.err
		}
		if progress != nil {
			progress(float64(r+1) / float64(rows))
		}
	}
	if err := ctx.Err(); err != nil {
		return viewshedGrid{}, err
	}
	return g, nil
}

// roughSRTMView is a real srtmView (bilinear lookups over int16 tiles) over
// bb (relief scaled by scale: 1 rough, 0.1 gentle), with deterministic hills and a few sharp ridges, so the paths see
// shadow, partial blockage and clear ground.
func roughSRTMView(bb geoBBox, scale float64) *srtmView {
	sp, _ := srtmSpanOf(bb)
	v := &srtmView{span: sp, grids: make([][]int16, sp.count())}
	for i := range v.grids {
		k := sp.key(i)
		g := make([]int16, srtmSide*srtmSide)
		for r := 0; r < srtmSide; r++ {
			lat := float64(k.lat+1) - float64(r)/srtmCells
			for c := 0; c < srtmSide; c++ {
				lon := float64(k.lon) + float64(c)/srtmCells
				e := 300 + scale*(120*math.Sin(lat*37)*math.Cos(lon*29)+60*math.Sin(lat*151+lon*113))
				if math.Mod(lon*40, 1) < 0.08 { // N-S ridges ~2 km wide every ~2.5 km
					e += 90 * scale
				}
				g[r*srtmSide+c] = int16(e)
			}
		}
		v.grids[i] = g
	}
	return v
}

// viewshedEquivalenceCases are the terrains and parameters the optimised
// code is compared with computeViewshedRef on.
func viewshedEquivalenceCases() []struct {
	name string
	v    terrainView
	p    viewshedParams
} {
	var cs []struct {
		name string
		v    terrainView
		p    viewshedParams
	}
	sites := []geoPoint{viewshedTestSite, {Lat: 45.2, Lon: 7.1}, {Lat: -33.9, Lon: 151.2}}
	for _, site := range sites {
		for _, rng := range []float64{3, 12, 30} {
			if rng == 30 && site != viewshedTestSite {
				continue // the slow case once is enough
			}
			p := viewshedTestParams(rng)
			p.Site = site
			bb := plannerExpandBBox(viewshedBBox(site, rng), 1)
			ridge := ridgeTerrainView{baseM: 100, ridgeM: 80, west: site.Lon + 0.05, east: site.Lon + 0.07}
			cs = append(cs,
				struct {
					name string
					v    terrainView
					p    viewshedParams
				}{fmt.Sprintf("fake/%.1f,%.1f/%gkm", site.Lat, site.Lon, rng), fakeTerrainView{bb: bb}, p},
				struct {
					name string
					v    terrainView
					p    viewshedParams
				}{fmt.Sprintf("flat/%.1f,%.1f/%gkm", site.Lat, site.Lon, rng), flatTerrainView{elevM: 50}, p},
				struct {
					name string
					v    terrainView
					p    viewshedParams
				}{fmt.Sprintf("ridge/%.1f,%.1f/%gkm", site.Lat, site.Lon, rng), ridgeTerrainView{ridge.baseM, ridge.ridgeM, ridge.west, ridge.east}, p},
			)
		}
	}
	return cs
}

func TestViewshedMatchesReference(t *testing.T) {
	check := func(name string, v terrainView, p viewshedParams) {
		want, err := computeViewshedRef(context.Background(), v, p, nil)
		if err != nil {
			t.Fatalf("%s: ref: %v", name, err)
		}
		for _, workers := range []int{1, 3, 7} {
			got, err := computeViewshedN(context.Background(), v, p, workers, nil)
			if err != nil {
				t.Fatalf("%s: workers=%d: %v", name, workers, err)
			}
			if got.Rows != want.Rows || got.Cols != want.Cols || got.BBox != want.BBox || got.CellKm != want.CellKm ||
				!bytes.Equal(got.Cells, want.Cells) {
				diff := 0
				for i := range want.Cells {
					if got.Cells[i] != want.Cells[i] {
						diff++
					}
				}
				t.Fatalf("%s: workers=%d: %d of %d cells differ from the reference", name, workers, diff, len(want.Cells))
			}
		}
	}
	for _, c := range viewshedEquivalenceCases() {
		check(c.name, c.v, c.p)
	}
	// Real tile lookups with ridges and shadow, and a tile without data.
	for _, rng := range []float64{8, 20} {
		p := viewshedTestParams(rng)
		bb := viewshedBBox(p.Site, rng)
		v := roughSRTMView(bb, 1)
		check(fmt.Sprintf("srtm/%gkm", rng), v, p)
		holed := &srtmView{span: v.span, grids: append([][]int16(nil), v.grids...)}
		holed.grids[0] = nil
		check(fmt.Sprintf("srtm-hole/%gkm", rng), holed, p)
	}
	check("missing-tile", fakeTerrainView{bb: plannerExpandBBox(viewshedBBox(viewshedTestSite, 30), 1),
		missing: map[[2]int]bool{{31, -98}: true}}, viewshedTestParams(30))
}

// TestViewshedRoughTerrainHasEveryClass guards the equivalence test above:
// it is only worth something if the terrain exercises shadow and every
// Repeaters class.
func TestViewshedRoughTerrainHasEveryClass(t *testing.T) {
	p := viewshedTestParams(25)
	g := mustViewshed(t, roughSRTMView(viewshedBBox(p.Site, p.RangeKm), 1), p)
	var rep [4]int
	for _, b := range g.Cells {
		rep[b>>4]++
	}
	if rep[1] == 0 || rep[2] == 0 || rep[3] == 0 {
		t.Fatalf("repeater class counts %v: terrain too tame", rep)
	}
}

// TestClearancePairMatchesClearance pins clearancePair to the two
// e.clearance calls it replaces, sample for sample, on rough terrain.
func TestClearancePairMatchesClearance(t *testing.T) {
	p := viewshedTestParams(40)
	v := roughSRTMView(viewshedBBox(p.Site, p.RangeKm), 1)
	rng := rand.New(rand.NewSource(1))
	e := newPlanLinkEval(context.Background(), v, p.FreqMHz, p.KFactor)
	ref := newPlanLinkEval(context.Background(), v, p.FreqMHz, p.KFactor)
	for i := 0; i < 400; i++ {
		b := geoPoint{Lat: p.Site.Lat + (rng.Float64()-0.5)*0.6, Lon: p.Site.Lon + (rng.Float64()-0.5)*0.6}
		d := geoDistKm(p.Site, b)
		cr, ok, shadow := e.clearancePair(p.Site, b, d, p.SiteM, p.RxM, viewshedUserRxM, viewshedShadowClearance)
		wantCr, wantOK := ref.clearance(p.Site, b, d, p.SiteM, p.RxM, math.Inf(-1))
		cu, uok := ref.clearance(p.Site, b, d, p.SiteM, viewshedUserRxM, viewshedShadowClearance)
		wantShadow := !uok && cu < viewshedShadowClearance
		if ok != wantOK || (ok && math.Float64bits(cr) != math.Float64bits(wantCr)) || shadow != wantShadow {
			t.Fatalf("b=%v d=%.2f: pair (%v,%v,%v) want (%v,%v,%v)", b, d, cr, ok, shadow, wantCr, wantOK, wantShadow)
		}
	}
}

func TestViewshedParallelProgress(t *testing.T) {
	for _, workers := range []int{2, 4} {
		var last float64
		calls := 0
		_, err := computeViewshedN(context.Background(), flatTerrainView{elevM: 50}, viewshedTestParams(10), workers,
			func(f float64) { // would race under -race if not serialised
				if f < last {
					t.Errorf("workers=%d: progress went back from %v to %v", workers, last, f)
				}
				last = f
				calls++
			})
		if err != nil {
			t.Fatal(err)
		}
		if calls == 0 || last != 1 {
			t.Errorf("workers=%d: %d calls, last %v; want calls ending at exactly 1", workers, calls, last)
		}
	}
}

// cancelAfterView cancels its context after n lookups.
type cancelAfterView struct {
	terrainView
	n      int64
	seen   atomic.Int64
	cancel context.CancelFunc
}

func (v *cancelAfterView) ElevationAt(lat, lon float64) (float64, bool) {
	if v.seen.Add(1) == v.n {
		v.cancel()
	}
	return v.terrainView.ElevationAt(lat, lon)
}

func TestViewshedParallelCancelsPromptly(t *testing.T) {
	p := viewshedTestParams(12)
	total := countingElevations(t, p)
	for _, workers := range []int{1, 4} {
		ctx, cancel := context.WithCancel(context.Background())
		v := &cancelAfterView{terrainView: flatTerrainView{elevM: 50}, n: 1000, cancel: cancel}
		_, err := computeViewshedN(ctx, v, p, workers, nil)
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("workers=%d: err = %v, want context.Canceled", workers, err)
		}
		// Each worker may finish its current 16-cell ctx window plus its row.
		if got := v.seen.Load(); got > total/20 {
			t.Errorf("workers=%d: %d lookups after cancel at 1000 (full run %d): not prompt", workers, got, total)
		}
	}
}

// A panic in a worker must surface on the caller's goroutine, where the job
// runner recovers it, not kill the process.
func TestViewshedWorkerPanicReachesCaller(t *testing.T) {
	// The site lookup in computeViewshedN is on the caller; let it pass.
	v := &panicAfterView{terrainView: flatTerrainView{elevM: 50}, after: 1}
	defer func() {
		if r := recover(); r != "boom" {
			t.Fatalf("recovered %v, want boom", r)
		}
	}()
	_, _ = computeViewshedN(context.Background(), v, viewshedTestParams(10), 4, nil)
	t.Fatal("no panic")
}

type panicAfterView struct {
	terrainView
	after int64
	seen  atomic.Int64
}

func (v *panicAfterView) ElevationAt(lat, lon float64) (float64, bool) {
	if v.seen.Add(1) > v.after {
		panic("boom")
	}
	return v.terrainView.ElevationAt(lat, lon)
}

// countView counts ElevationAt calls.
type countView struct {
	terrainView
	n atomic.Int64
}

func (v *countView) ElevationAt(lat, lon float64) (float64, bool) {
	v.n.Add(1)
	return v.terrainView.ElevationAt(lat, lon)
}

func countingElevations(t testing.TB, p viewshedParams) int64 {
	v := &countView{terrainView: flatTerrainView{elevM: 50}}
	if _, err := computeViewshedRef(context.Background(), v, p, nil); err != nil {
		t.Fatal(err)
	}
	return v.n.Load()
}

// TestViewshedWorkCount logs the work a run does (go test -v).
func TestViewshedWorkCount(t *testing.T) {
	for _, rng := range []float64{30, 60} {
		p := viewshedTestParams(rng)
		g := mustViewshed(t, flatTerrainView{elevM: 50}, p)
		cells := 0
		for _, b := range g.Cells {
			if b != 0 {
				cells++
			}
		}
		ref := countingElevations(t, p)
		cv := &countView{terrainView: flatTerrainView{elevM: 50}}
		if _, err := computeViewshedN(context.Background(), cv, p, 1, nil); err != nil {
			t.Fatal(err)
		}
		t.Logf("%gkm: grid %dx%d, cell %.3f km, %d cells in range; ElevationAt calls: reference %d, single pass %d",
			rng, g.Rows, g.Cols, g.CellKm, cells, ref, cv.n.Load())
	}
}

func benchmarkViewshed(b *testing.B, rangeKm float64, relief float64, workers int) {
	p := viewshedTestParams(rangeKm)
	var v terrainView = fakeTerrainView{bb: plannerExpandBBox(viewshedBBox(p.Site, p.RangeKm), 1)}
	if relief > 0 {
		v = roughSRTMView(viewshedBBox(p.Site, p.RangeKm), relief)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var err error
		if workers == 0 {
			_, err = computeViewshedRef(context.Background(), v, p, nil)
		} else {
			_, err = computeViewshedN(context.Background(), v, p, workers, nil)
		}
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkViewshed30km(b *testing.B) { benchmarkViewshed(b, 30, 0, viewshedWorkers()) }

// BenchmarkViewshedMaxLeg: the largest range the planner offers (60 km).
func BenchmarkViewshedMaxLeg(b *testing.B) { benchmarkViewshed(b, 60, 0, viewshedWorkers()) }

// BenchmarkViewshedVariants: workers=0 is the reference implementation.
// fake is the plane fakeTerrainView; srtm and srtm-gentle are real tile
// lookups over rough and gentle relief.
func BenchmarkViewshedVariants(b *testing.B) {
	for _, rangeKm := range []float64{30, 60} {
		for _, terrain := range []struct {
			name   string
			relief float64
		}{{"fake", 0}, {"srtm", 1}, {"srtm-gentle", 0.1}} {
			for _, w := range []int{0, 1, 2, 4, 6} {
				b.Run(fmt.Sprintf("%gkm/%s/w%d", rangeKm, terrain.name, w), func(b *testing.B) { benchmarkViewshed(b, rangeKm, terrain.relief, w) })
			}
		}
	}
}
