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

// ISA standard atmosphere at 100 m elevation, RH 40 %. Python gives
// min dN/dh = -45.398 N/km (between the 2 m point and the 1000 hPa level)
// and every other layer between -26.6 and -36.5 N/km.
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
	if len(prof) != 9 {
		t.Fatalf("profile has %d levels, want 9", len(prof))
	}
	r := AnalyzeDucting(prof)
	if r.Class != DuctClassNormal {
		t.Fatalf("Class = %d, want %d (%+v)", r.Class, DuctClassNormal, r)
	}
	ductApprox(t, "MinGradNkm (≈-40)", r.MinGradNkm, -40, 10)
	ductApprox(t, "MinGradNkm (python)", r.MinGradNkm, -45.398, 0.01)
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
	t.Run("bad surface point omitted", func(t *testing.T) {
		levels := []float64{1000, 950}
		temps := []float64{14, 11}
		rhs := []float64{50, 50}
		zs := []float64{110, 540}
		for _, s := range []struct{ p, t, rh float64 }{{1013, nan, 50}, {1013, 15, nan}, {1013, 95, 50}, {1200, 15, 50}} {
			p := DuctProfile(0, s.p, s.t, s.rh, levels, temps, rhs, zs)
			if len(p) == 0 || p[0].HeightAGL == ductSurfaceHeightM {
				t.Errorf("surface %+v: got %+v, want no surface point", s, p)
			}
		}
		// Surface pressure missing: levels still kept on the height check.
		if p := DuctProfile(0, nan, 15, 50, levels, temps, rhs, zs); len(p) != 2 {
			t.Errorf("NaN surface pressure: got %+v, want 2 levels", p)
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
		{HeightAGL: 1499, PressureHPa: 850, TempC: 5, RHPct: 60},
	}
	r := AnalyzeDucting(prof)
	if r.MeanGradNkm != 0 {
		t.Errorf("MeanGradNkm = %v, want 0 (top below 1500 m)", r.MeanGradNkm)
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
