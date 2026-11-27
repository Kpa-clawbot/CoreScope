package main

import (
	"math"
	"testing"
)

// Expected values below were computed independently (python, same formulas
// as ducting_types.go), not by calling the code under test.

func ductApprox(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.6f, want %.6f ±%g", name, got, want, tol)
	}
}

func TestDuctRefractivityKnownAnswers(t *testing.T) {
	ductApprox(t, "Refractivity(1013.25,15,50)", Refractivity(1013.25, 15, 50), 311.19, 0.05)
	ductApprox(t, "vapourPressureHPa(15,100)", vapourPressureHPa(15, 100), 17.046, 0.005)
	ductApprox(t, "vapourPressureHPa(15,0)", vapourPressureHPa(15, 0), 0, 1e-12)
	ductApprox(t, "ModifiedRefractivity(300,1000)", ModifiedRefractivity(300, 1000), 457, 1e-9)
}

func TestDuctAnalyzeMKnownAnswers(t *testing.T) {
	cases := []struct {
		name  string
		h, m  []float64
		class uint8
		base  float64
		top   float64
		dm    float64
		fmin  float64
		ftol  float64
	}{
		{
			name: "surface-based",
			h:    []float64{0, 100, 200, 300}, m: []float64{350, 340, 345, 380},
			class: DuctClassSurface, base: 0, top: 100, dm: 10, fmin: 379.47, ftol: 0.1,
		},
		{
			name: "elevated",
			h:    []float64{0, 500, 700, 900, 1200}, m: []float64{330, 400, 380, 395, 440},
			class: DuctClassElev, base: 357.14, top: 700, dm: 20, fmin: 78.26, ftol: 0.05,
		},
		{
			// Run 1 surface-based (fmin 536.66), run 2 elevated (fmin 58.42): run 2 wins.
			name: "two runs, later stronger",
			h:    []float64{0, 100, 200, 300, 600, 700}, m: []float64{350, 345, 360, 400, 370, 420},
			class: DuctClassElev, base: 225, top: 600, dm: 30, fmin: 58.42, ftol: 0.05,
		},
		{
			// Run 1 surface-based (fmin 219.09), run 2 elevated (fmin 357.77): run 1 wins.
			name: "two runs, earlier stronger",
			h:    []float64{0, 100, 200, 300, 400}, m: []float64{400, 370, 380, 390, 385},
			class: DuctClassSurface, base: 0, top: 100, dm: 30, fmin: 219.09, ftol: 0.05,
		},
		{
			// Both runs d=100, ΔM=10 (fmin 379.47): tie goes to the lower bottom.
			name: "tie -> lower bottom",
			h:    []float64{0, 100, 200, 290}, m: []float64{350, 340, 440, 430},
			class: DuctClassSurface, base: 0, top: 100, dm: 10, fmin: 379.47, ftol: 0.1,
		},
		{
			// Elevated layer whose M crosses Mtop exactly at a level.
			name: "elevated, crossing at a level",
			h:    []float64{0, 200, 400, 600}, m: []float64{300, 320, 340, 320},
			class: DuctClassElev, base: 200, top: 600, dm: 20, fmin: 67.08, ftol: 0.05,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			n := make([]float64, len(c.m))
			for i := range c.m {
				n[i] = c.m[i] - 0.157*c.h[i]
			}
			r := analyzeM(c.h, c.m, n)
			if r.Class != c.class {
				t.Fatalf("Class = %d, want %d (%+v)", r.Class, c.class, r)
			}
			ductApprox(t, "BaseM", r.BaseM, c.base, 0.05)
			ductApprox(t, "TopM", r.TopM, c.top, 1e-9)
			ductApprox(t, "DeltaM", r.DeltaM, c.dm, 1e-9)
			ductApprox(t, "FminMHz", r.FminMHz, c.fmin, c.ftol)
		})
	}
}

func TestDuctAnalyzeMNoDuct(t *testing.T) {
	t.Run("super-refractive", func(t *testing.T) {
		// dN/dh = -100 N/km everywhere, dM/dh = +57 M/km: bends but does not trap.
		h := []float64{0, 200, 400, 600}
		n := []float64{350, 330, 310, 290}
		m := []float64{350, 361.4, 372.8, 384.2}
		r := analyzeM(h, m, n)
		if r.Class != DuctClassSuper {
			t.Fatalf("Class = %d, want %d", r.Class, DuctClassSuper)
		}
		ductApprox(t, "MinGradNkm", r.MinGradNkm, -100, 1e-9)
		if r.FminMHz != 0 || r.BaseM != 0 || r.TopM != 0 || r.DeltaM != 0 {
			t.Errorf("duct fields not zero: %+v", r)
		}
	})
	t.Run("dM/dh exactly zero is not a duct", func(t *testing.T) {
		// dN/dh = -157 N/km over the lowest layer: M constant there.
		h := []float64{0, 1000, 2000}
		n := []float64{350, 193, 160}
		m := []float64{350, 350, 474}
		r := analyzeM(h, m, n)
		if r.Class != DuctClassSuper {
			t.Fatalf("Class = %d, want %d (%+v)", r.Class, DuctClassSuper, r)
		}
		if r.FminMHz != 0 {
			t.Errorf("FminMHz = %v, want 0", r.FminMHz)
		}
		ductApprox(t, "MinGradNkm", r.MinGradNkm, -157, 1e-9)
	})
	t.Run("fewer than 2 levels", func(t *testing.T) {
		for _, h := range [][]float64{nil, {0}} {
			r := analyzeM(h, h, h)
			if r != (DuctResult{Class: DuctClassNoData}) {
				t.Errorf("len %d: got %+v, want NoData zero result", len(h), r)
			}
		}
		if r := AnalyzeDucting([]DuctLevel{{HeightAGL: 2, PressureHPa: 1000, TempC: 10, RHPct: 50}}); r.Class != DuctClassNoData {
			t.Errorf("AnalyzeDucting 1 level: Class = %d, want 0", r.Class)
		}
	})
}

// ISA standard atmosphere at 100 m elevation, RH 40 %. The 1000 hPa level
// is 10.9 m above ground and dropped (ductMinLevelGapM). Python gives
// min dN/dh = -36.813 N/km (between the 2 m point and the 975 hPa level)
// and every other layer between -26.6 and -35.4 N/km.
func TestDuctStandardAtmosphereIsNormal(t *testing.T) {
	isaT := func(z float64) float64 { return 15 - 6.5*z/1000 }
	isaZ := func(p float64) float64 { return (1 - math.Pow(p/1013.25, 1/5.25588)) / 2.25577e-5 }
	const elev = 100.0
	ps := 1013.25 * math.Pow(1-2.25577e-5*elev, 5.25588)
	levels := []float64{1000, 975, 950, 925, 900, 850, 800, 700}
	var temps, rhs, zs []float64
	for _, p := range levels {
		z := isaZ(p)
		zs = append(zs, z)
		temps = append(temps, isaT(z))
		rhs = append(rhs, 40)
	}
	prof := DuctProfile(elev, ps, isaT(elev), 40, levels, temps, rhs, zs)
	// 1000 hPa is 10.9 m above ground here: closer to the 2 m point than
	// ductMinLevelGapM, ignored.
	if len(prof) != 8 || prof[1].PressureHPa != 975 {
		t.Fatalf("profile %+v, want 8 levels, the 2 m point then 975 hPa", prof)
	}
	r := AnalyzeDucting(prof)
	if r.Class != DuctClassNormal {
		t.Fatalf("Class = %d, want %d (%+v)", r.Class, DuctClassNormal, r)
	}
	ductApprox(t, "MinGradNkm (≈-40)", r.MinGradNkm, -40, 10)
	ductApprox(t, "MinGradNkm (python)", r.MinGradNkm, -36.813, 0.01)
	if r.FminMHz != 0 {
		t.Errorf("FminMHz = %v, want 0", r.FminMHz)
	}
}

// A strong humidity lapse at the surface traps through AnalyzeDucting end to end.
func TestDuctAnalyzeDuctingSurfaceDuct(t *testing.T) {
	prof := []DuctLevel{
		{HeightAGL: 2, PressureHPa: 1013, TempC: 25, RHPct: 90},
		{HeightAGL: 100, PressureHPa: 1001.5, TempC: 27, RHPct: 20},
		{HeightAGL: 500, PressureHPa: 956.5, TempC: 24, RHPct: 20},
	}
	r := AnalyzeDucting(prof)
	if r.Class != DuctClassSurface {
		t.Fatalf("Class = %d, want %d (%+v)", r.Class, DuctClassSurface, r)
	}
	// Python: M = 383.652 (2 m), 304.165 (100 m), 353.508 (500 m).
	ductApprox(t, "BaseM", r.BaseM, 0, 1e-9)
	ductApprox(t, "TopM", r.TopM, 100, 1e-9)
	ductApprox(t, "DeltaM", r.DeltaM, 79.487, 0.005)
	ductApprox(t, "FminMHz", r.FminMHz, 134.60, 0.05)
}

func TestDuctProfile(t *testing.T) {
	nan := math.NaN()
	t.Run("filters and sorts", func(t *testing.T) {
		// elevation 200 m, surface 990 hPa.
		levels := []float64{850, 1000, 925, 950, 700, 900, 875}
		temps := []float64{8, 20, 12, nan, -5, 70, 9}
		rhs := []float64{60, 50, 120, 50, 40, 50, -5}
		zs := []float64{1450, 110, 760, 540, 3300, 990, 1220}
		p := DuctProfile(200, 990, 18, 55, levels, temps, rhs, zs)
		// 1000 hPa: below ground, dropped. 950: NaN T, skipped. 700: 3100 m AGL,
		// dropped. 900: T 70 °C, rejected. Remaining: surface, 925, 875, 850.
		wantH := []float64{2, 560, 1020, 1250}
		wantP := []float64{990, 925, 875, 850}
		wantRH := []float64{55, 100, 0, 60}
		if len(p) != len(wantH) {
			t.Fatalf("got %d levels %+v, want %d", len(p), p, len(wantH))
		}
		for i := range wantH {
			if p[i].HeightAGL != wantH[i] || p[i].PressureHPa != wantP[i] || p[i].RHPct != wantRH[i] {
				t.Errorf("level %d = %+v, want h=%v p=%v rh=%v", i, p[i], wantH[i], wantP[i], wantRH[i])
			}
		}
		if p[0].TempC != 18 {
			t.Errorf("surface TempC = %v, want 18", p[0].TempC)
		}
	})
	t.Run("mismatched lengths use the shortest", func(t *testing.T) {
		p := DuctProfile(0, 1013, 15, 50,
			[]float64{1000, 950, 900},
			[]float64{14, 11},
			[]float64{50, 50, 50, 50},
			[]float64{110, 540, 990})
		if len(p) != 3 || p[2].PressureHPa != 950 {
			t.Fatalf("got %+v, want surface + 1000 + 950", p)
		}
		if got := DuctProfile(0, 1013, 15, 50, nil, nil, nil, nil); len(got) != 1 {
			t.Fatalf("nil slices: got %+v, want surface only", got)
		}
	})
	t.Run("duplicate heights de-duplicated", func(t *testing.T) {
		p := DuctProfile(0, 1013, 15, 50,
			[]float64{1000, 999, 950},
			[]float64{14, 13, 11},
			[]float64{50, 50, 50},
			[]float64{110, 110, 540})
		if len(p) != 3 || p[1].PressureHPa != 1000 || p[2].HeightAGL != 540 {
			t.Fatalf("got %+v, want surface, 1000@110, 950@540", p)
		}
	})
	t.Run("bad surface point: no profile", func(t *testing.T) {
		levels := []float64{1000, 950}
		temps := []float64{14, 11}
		rhs := []float64{50, 50}
		zs := []float64{110, 540}
		for _, s := range []struct{ p, t, rh float64 }{{1013, nan, 50}, {1013, 15, nan}, {1013, 95, 50}, {1200, 15, 50}, {nan, 15, 50}} {
			if p := DuctProfile(0, s.p, s.t, s.rh, levels, temps, rhs, zs); len(p) != 0 {
				t.Errorf("surface %+v: got %+v, want no profile", s, p)
			}
		}
	})
	t.Run("level at the surface point height dropped", func(t *testing.T) {
		p := DuctProfile(100, 1013, 15, 50, []float64{1000}, []float64{14}, []float64{50}, []float64{102})
		if len(p) != 1 {
			t.Fatalf("got %+v, want surface only", p)
		}
	})
}

// Mean gradient over the lowest 1.5 km. Python, ITU-R P.453 N:
//
//	N(2 m,    1000 hPa, 15 C, 50 %) = 307.6179
//	N(500 m,   950 hPa, 12 C, 55 %) = 293.9286  (not a bracketing level)
//	N(1200 m,  870 hPa,  6 C, 60 %) = 268.7227
//	N(1800 m,  820 hPa,  3 C, 70 %) = 256.3955
//
// 1500 m lies halfway between 1200 and 1800 m:
// N(1500) = 268.7227 + (256.3955-268.7227)*300/600 = 262.5591
// MeanGradNkm = (262.5591 - 307.6179) / 1500 * 1000 = -30.04 N/km.
func TestDuctMeanGradientKnownAnswer(t *testing.T) {
	prof := []DuctLevel{
		{HeightAGL: 2, PressureHPa: 1000, TempC: 15, RHPct: 50},
		{HeightAGL: 500, PressureHPa: 950, TempC: 12, RHPct: 55},
		{HeightAGL: 1200, PressureHPa: 870, TempC: 6, RHPct: 60},
		{HeightAGL: 1800, PressureHPa: 820, TempC: 3, RHPct: 70},
	}
	r := AnalyzeDucting(prof)
	ductApprox(t, "MeanGradNkm", r.MeanGradNkm, -30.04, 0.01)
	// MinGradNkm is the steepest consecutive pair, unchanged:
	// (268.7227-293.9286)/0.7 = -36.01, (256.3955-268.7227)/0.6 = -20.55,
	// (293.9286-307.6179)/0.498 = -27.49.
	ductApprox(t, "MinGradNkm", r.MinGradNkm, -36.01, 0.01)
}

func TestDuctMeanGradientExactLevel(t *testing.T) {
	// A level exactly at 1500 m is used as is (no interpolation needed).
	prof := []DuctLevel{
		{HeightAGL: 2, PressureHPa: 1000, TempC: 15, RHPct: 50},
		{HeightAGL: 1500, PressureHPa: 850, TempC: 5, RHPct: 60},
	}
	want := (Refractivity(850, 5, 60) - Refractivity(1000, 15, 50)) / 1.5
	ductApprox(t, "MeanGradNkm", AnalyzeDucting(prof).MeanGradNkm, want, 1e-9)
}

func TestDuctMeanGradientTopBelowLayer(t *testing.T) {
	prof := []DuctLevel{
		{HeightAGL: 2, PressureHPa: 1000, TempC: 15, RHPct: 50},
		{HeightAGL: 500, PressureHPa: 950, TempC: 12, RHPct: 55},
		{HeightAGL: 900, PressureHPa: 905, TempC: 9, RHPct: 60},
	}
	r := AnalyzeDucting(prof)
	if r.MeanGradNkm != 0 {
		t.Errorf("MeanGradNkm = %v, want 0 (top below 1000 m)", r.MeanGradNkm)
	}
	if r.MinGradNkm == 0 {
		t.Error("MinGradNkm should still be computed")
	}
	if g := AnalyzeDucting(nil).MeanGradNkm; g != 0 {
		t.Errorf("no data: MeanGradNkm = %v, want 0", g)
	}
}

func TestDuctMeanGradientStandardAtmosphere(t *testing.T) {
	isaT := func(z float64) float64 { return 15 - 6.5*z/1000 }
	isaZ := func(p float64) float64 { return (1 - math.Pow(p/1013.25, 1/5.25588)) / 2.25577e-5 }
	const elev = 100.0
	ps := 1013.25 * math.Pow(1-2.25577e-5*elev, 5.25588)
	levels := []float64{1000, 925, 850, 800, 700}
	var temps, rhs, zs []float64
	for _, p := range levels {
		z := isaZ(p)
		zs = append(zs, z)
		temps = append(temps, isaT(z))
		rhs = append(rhs, 40)
	}
	r := AnalyzeDucting(DuctProfile(elev, ps, isaT(elev), 40, levels, temps, rhs, zs))
	if r.MeanGradNkm < -50 || r.MeanGradNkm > -30 {
		t.Errorf("MeanGradNkm = %.2f, want about -40 (-50..-30)", r.MeanGradNkm)
	}
}

// High terrain: the models stop at 700 hPa, ~1,400 m above a Denver-like
// surface, so no two points bracket 1500 m. The mean is taken over the
// depth there is: (N(top) - N(2 m)) / h_top.
func TestDuctMeanGradientHighTerrain(t *testing.T) {
	prof := []DuctLevel{
		{HeightAGL: 2, PressureHPa: 835, TempC: 20, RHPct: 30},
		{HeightAGL: 400, PressureHPa: 800, TempC: 17, RHPct: 30},
		{HeightAGL: 1400, PressureHPa: 700, TempC: 8, RHPct: 35},
	}
	want := (Refractivity(700, 8, 35) - Refractivity(835, 20, 30)) / 1400 * 1000
	r := AnalyzeDucting(prof)
	if want >= 0 || want < -100 {
		t.Fatalf("test profile gives an implausible %v N/km", want)
	}
	ductApprox(t, "MeanGradNkm", r.MeanGradNkm, want, 1e-9)

	// Just below 1500 m: still the available depth, not 0.
	prof[2].HeightAGL = 1499
	want = (Refractivity(700, 8, 35) - Refractivity(835, 20, 30)) / 1499 * 1000
	ductApprox(t, "MeanGradNkm 1499 m", AnalyzeDucting(prof).MeanGradNkm, want, 1e-9)

	// Exactly 1000 m is enough; just under is not.
	prof[2].HeightAGL = 1000
	want = (Refractivity(700, 8, 35) - Refractivity(835, 20, 30)) / 1000 * 1000
	ductApprox(t, "MeanGradNkm 1000 m", AnalyzeDucting(prof).MeanGradNkm, want, 1e-9)
	prof[2].HeightAGL = 999
	if g := AnalyzeDucting(prof).MeanGradNkm; g != 0 {
		t.Errorf("top 999 m: MeanGradNkm = %v, want 0", g)
	}
}

// Mexico City: ground ~2,240 m, surface pressure ~775 hPa, and 700 hPa (the
// models' top level) only ~860 m above it. Through DuctProfile with the GFS
// levels, everything at or below 775 hPa is dropped, the profile stops under
// the 1 km floor, and the cell has no field value (n = 0); the duct analysis
// still runs on the 860 m there are. A Denver-like site (ground ~1,600 m,
// 700 hPa ~1,450 m up) gets a value.
func TestDuctMeanGradientMexicoCityHasNoFieldValue(t *testing.T) {
	m, _ := ductingModel("gfs")
	// A standard-ish column: geopotential height of each level (m above sea
	// level), temperature and humidity; levels under ground are present, as
	// the API returns them, and must be ignored.
	geo := map[float64]float64{1000: 110, 975: 330, 950: 550, 925: 770, 900: 1000, 875: 1230, 850: 1460,
		825: 1700, 800: 1950, 775: 2210, 750: 2480, 725: 2790, 700: 3100}
	col := func(elev, surfP float64) DuctResult {
		temps := make([]float64, len(m.LevelsHPa))
		rhs := make([]float64, len(m.LevelsHPa))
		geos := make([]float64, len(m.LevelsHPa))
		for i, p := range m.LevelsHPa {
			geos[i] = geo[p]
			temps[i] = 24 - 6.5*geo[p]/1000
			rhs[i] = 45
		}
		prof := DuctProfile(elev, surfP, 24-6.5*elev/1000, 45, m.LevelsHPa, temps, rhs, geos)
		return AnalyzeDucting(prof)
	}
	mx := col(2240, 775)
	if mx.Class == DuctClassNoData {
		t.Fatalf("Mexico City: no data at all; want a profile to analyse")
	}
	if mx.MeanGradNkm != 0 {
		t.Errorf("Mexico City: MeanGradNkm %v, want 0 (700 hPa is only 860 m up, under the 1 km floor)", mx.MeanGradNkm)
	}
	den := col(1600, 840)
	if den.MeanGradNkm >= 0 || den.MeanGradNkm < -100 {
		t.Errorf("Denver: MeanGradNkm %v, want a plausible negative value", den.MeanGradNkm)
	}
}

// A pressure level only a few metres above the 2 m point is not a second
// measurement of the same column: over 6 m, the 2.3 N units between
//
//	N(2 m, 1001 hPa, 12 C, 95 %) = 333.55
//	N(8 m, 1000 hPa, 13 C, 88 %) = 331.25
//
// would read as -385 N/km and a surface-based duct with fmin ~12.8 GHz.
// The level is ignored; with N(540 m, 950 hPa, 9 C, 80 %) = 304.33 the
// profile is an ordinary -54 N/km.
func TestDuctLevelTooCloseToSurfaceIgnored(t *testing.T) {
	for _, c := range []struct {
		name string
		z    float64 // geopotential height of the 1000 hPa level; elevation 0
	}{{"8 m", 8}, {"5 m", 5}, {"just under 30 m above the 2 m point", 31.9}} {
		t.Run(c.name, func(t *testing.T) {
			prof := DuctProfile(0, 1001, 12, 95,
				[]float64{1000, 950}, []float64{13, 9}, []float64{88, 80}, []float64{c.z, 540})
			if len(prof) != 2 || prof[0].HeightAGL != 2 || prof[1].HeightAGL != 540 {
				t.Fatalf("profile %+v, want the 2 m point and the 540 m level only", prof)
			}
			r := AnalyzeDucting(prof)
			if r.Class != DuctClassNormal || r.FminMHz != 0 {
				t.Fatalf("Class = %d, FminMHz = %v; want normal, no duct (%+v)", r.Class, r.FminMHz, r)
			}
			ductApprox(t, "MinGradNkm", r.MinGradNkm, -54.3, 0.5)
		})
	}
	// Alone, the close level leaves no usable profile.
	if r := AnalyzeDucting(DuctProfile(0, 1001, 12, 95, []float64{1000}, []float64{13}, []float64{88}, []float64{8})); r.Class != DuctClassNoData {
		t.Errorf("only a level at 8 m: Class = %d, want no data", r.Class)
	}
	// 30 m above the 2 m point is kept.
	if prof := DuctProfile(0, 1001, 12, 95, []float64{997}, []float64{12}, []float64{90}, []float64{32}); len(prof) != 2 || prof[1].HeightAGL != 32 {
		t.Errorf("level at 32 m: profile %+v, want it kept", prof)
	}
}

// Without the 2 m point there is no surface to measure from: the first
// pressure level must not stand in for it.
func TestDuctMissingSurfacePointIsNoData(t *testing.T) {
	nan := math.NaN()
	// With a 2 m point this profile is a surface-based duct with a mean gradient.
	levels := []float64{1000, 950, 850, 800}
	temps := []float64{27, 24, 16, 12}
	rhs := []float64{20, 20, 30, 30}
	zs := []float64{100, 500, 1450, 1950}
	if r := AnalyzeDucting(DuctProfile(0, 1013, 25, 90, levels, temps, rhs, zs)); r.Class != DuctClassSurface || r.MeanGradNkm == 0 {
		t.Fatalf("with the 2 m point: %+v, want a surface duct and a mean gradient", r)
	}
	for _, s := range []struct {
		name     string
		p, t, rh float64
	}{
		{"temperature_2m null", 1013, nan, 90},
		{"relative_humidity_2m null", 1013, 25, nan},
		{"surface_pressure null", nan, 25, 90},
		{"all null", nan, nan, nan},
		{"temperature out of range", 1013, 95, 90},
	} {
		prof := DuctProfile(0, s.p, s.t, s.rh, levels, temps, rhs, zs)
		if len(prof) != 0 {
			t.Errorf("%s: profile %+v, want none", s.name, prof)
		}
		if r := AnalyzeDucting(prof); r != (DuctResult{Class: DuctClassNoData}) {
			t.Errorf("%s: %+v, want no data and every field 0", s.name, r)
		}
	}
	// A hand-built profile that does not start at the 2 m point is no data too.
	r := AnalyzeDucting([]DuctLevel{
		{HeightAGL: 100, PressureHPa: 1001.5, TempC: 27, RHPct: 20},
		{HeightAGL: 500, PressureHPa: 956.5, TempC: 24, RHPct: 20},
		{HeightAGL: 1600, PressureHPa: 840, TempC: 15, RHPct: 20},
	})
	if r != (DuctResult{Class: DuctClassNoData}) {
		t.Errorf("profile without the 2 m point: %+v, want no data", r)
	}
}
