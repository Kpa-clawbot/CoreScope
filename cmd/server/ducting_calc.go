package main

// Tropospheric ducting physics: one vertical profile in, one DuctResult out.
// Pure functions, no I/O. Formulas and limits are in ducting_types.go.

import (
	"math"
	"slices"
)

// vapourPressureHPa returns the water-vapour partial pressure (hPa) for an
// air temperature tC (°C) and relative humidity rhPct (0..100).
func vapourPressureHPa(tC, rhPct float64) float64 {
	return rhPct / 100 * 6.1121 * math.Exp(17.502*tC/(240.97+tC))
}

// Refractivity returns the radio refractivity N (ITU-R P.453) for pressure
// pHPa, temperature tC (°C) and relative humidity rhPct.
func Refractivity(pHPa, tC, rhPct float64) float64 {
	t := tC + 273.15
	e := vapourPressureHPa(tC, rhPct)
	return 77.6 / t * (pHPa + 4810*e/t)
}

// ModifiedRefractivity returns M = N + 0.157*h for h metres above ground.
func ModifiedRefractivity(n, hAGL float64) float64 {
	return n + 0.157*hAGL
}

func ductFinite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }

// ductSaneLevel reports whether p, t and rh are finite and inside the
// accepted physical ranges (RH is clamped later, so only finiteness matters).
func ductSaneLevel(pHPa, tC, rhPct float64) bool {
	return ductFinite(pHPa) && ductFinite(tC) && ductFinite(rhPct) &&
		pHPa >= 100 && pHPa <= 1100 && tC >= -90 && tC <= 60
}

// ductShortest returns the smallest of the given lengths. (The package's
// test files shadow the builtin min, so it is not used here.)
func ductShortest(lens ...int) int {
	s := lens[0]
	for _, l := range lens[1:] {
		if l < s {
			s = l
		}
	}
	return s
}

func ductClampRH(rh float64) float64 {
	return math.Min(100, math.Max(0, rh))
}

// DuctProfile builds the ascending profile from a surface point (2 m values)
// and pressure-level data. Slices may differ in length (the shortest wins)
// and may hold NaN for missing values; unusable levels are skipped. Levels
// below ground, less than ductMinLevelGapM above the surface point, or above
// ductMaxHeightAGL are dropped.
//
// The profile starts at the 2 m point or is empty: when a 2 m value is
// missing or out of range there is no surface to measure a gradient or a
// duct from, and the lowest pressure level must not stand in for it (that
// would call an elevated layer surface-based and take the mean gradient
// from the wrong height). AnalyzeDucting reports an empty profile as no data.
func DuctProfile(elevation, surfPHPa, t2mC, rh2mPct float64, levelsHPa, tempsC, rhsPct, geoHeightsM []float64) []DuctLevel {
	if !ductSaneLevel(surfPHPa, t2mC, rh2mPct) {
		return nil
	}
	n := ductShortest(len(levelsHPa), len(tempsC), len(rhsPct), len(geoHeightsM))
	out := make([]DuctLevel, 0, n+1)
	out = append(out, DuctLevel{
		HeightAGL:   ductSurfaceHeightM,
		PressureHPa: surfPHPa,
		TempC:       t2mC,
		RHPct:       ductClampRH(rh2mPct),
	})
	for i := 0; i < n; i++ {
		p, t, rh, z := levelsHPa[i], tempsC[i], rhsPct[i], geoHeightsM[i]
		if !ductSaneLevel(p, t, rh) || !ductFinite(z) || !ductFinite(elevation) {
			continue
		}
		if p >= surfPHPa {
			continue
		}
		h := z - elevation
		if h < ductSurfaceHeightM+ductMinLevelGapM || h > ductMaxHeightAGL {
			continue
		}
		out = append(out, DuctLevel{HeightAGL: h, PressureHPa: p, TempC: t, RHPct: ductClampRH(rh)})
	}
	// Stable so that, among levels at one height, the first given is the one
	// kept by the de-duplication below.
	slices.SortStableFunc(out, func(a, b DuctLevel) int {
		switch {
		case a.HeightAGL < b.HeightAGL:
			return -1
		case a.HeightAGL > b.HeightAGL:
			return 1
		}
		return 0
	})
	dedup := out[:0]
	for i, l := range out {
		if i > 0 && l.HeightAGL == dedup[len(dedup)-1].HeightAGL {
			continue
		}
		dedup = append(dedup, l)
	}
	return dedup
}

// AnalyzeDucting computes N and M for each level of an ascending profile and
// classifies it (see analyzeM). A profile that does not start at the 2 m
// surface point (DuctProfile's are empty then) is no data: every height and
// gradient below is measured from that point.
func AnalyzeDucting(profile []DuctLevel) DuctResult {
	if len(profile) == 0 || profile[0].HeightAGL != ductSurfaceHeightM {
		return DuctResult{Class: DuctClassNoData}
	}
	h := make([]float64, len(profile))
	m := make([]float64, len(profile))
	n := make([]float64, len(profile))
	for i, l := range profile {
		h[i] = l.HeightAGL
		n[i] = Refractivity(l.PressureHPa, l.TempC, l.RHPct)
		m[i] = ModifiedRefractivity(n[i], l.HeightAGL)
	}
	return analyzeM(h, m, n)
}

// ductMeanMinLayerM is the shallowest layer ductMeanGradient still averages
// over when the profile stops below ductMeanLayerM (high terrain: the models'
// top level, 700 hPa, is only ~1.4 km above Denver).
const ductMeanMinLayerM = 1000.0

// ductMeanGradient returns (N at ductMeanLayerM - N at the first point) /
// ductMeanLayerM in N/km, with N at ductMeanLayerM linearly interpolated
// between the two points that bracket it. When none do but the highest point
// is at least ductMeanMinLayerM above ground, it returns the mean over that
// shallower depth instead: (N at the top - N at the first point) / its
// height. 0 when the profile is shallower still. h ascends; h[0] is the 2 m
// surface point.
func ductMeanGradient(h, n []float64) float64 {
	top := -1
	for i := 0; i+1 < len(h); i++ {
		if top < 0 || h[i+1] > h[top] {
			top = i + 1
		}
		if h[i] > ductMeanLayerM || h[i+1] < ductMeanLayerM || h[i+1] <= h[i] {
			continue
		}
		n1500 := n[i] + (n[i+1]-n[i])*(ductMeanLayerM-h[i])/(h[i+1]-h[i])
		return (n1500 - n[0]) / ductMeanLayerM * 1000
	}
	if top < 0 || h[top] < ductMeanMinLayerM {
		return 0
	}
	return (n[top] - n[0]) / h[top] * 1000
}

// analyzeM finds trapping layers in an M profile (heights ascending) and
// returns the strongest duct (lowest fmin) plus the most negative dN/dh.
func analyzeM(h, m, n []float64) DuctResult {
	cnt := ductShortest(len(h), len(m), len(n))
	if cnt < 2 {
		return DuctResult{Class: DuctClassNoData}
	}

	var res DuctResult
	haveGrad := false
	for i := 0; i+1 < cnt; i++ {
		dh := h[i+1] - h[i]
		if dh <= 0 {
			continue
		}
		g := (n[i+1] - n[i]) / (dh / 1000)
		if !haveGrad || g < res.MinGradNkm {
			res.MinGradNkm = g
			haveGrad = true
		}
	}

	res.MeanGradNkm = ductMeanGradient(h[:cnt], n[:cnt])

	found, surface := false, false
	var bestF, bestBase, bestTop, bestDM float64
	for a := 0; a+1 < cnt; {
		if !(m[a+1] < m[a]) {
			a++
			continue
		}
		b := a + 1
		for b+1 < cnt && m[b+1] < m[b] {
			b++
		}
		mTop, top := m[b], h[b]

		j := -1
		for k := a - 1; k >= 0; k-- {
			if m[k] <= mTop {
				j = k
				break
			}
		}
		base, from := 0.0, 0
		if j >= 0 {
			// m[j] <= mTop < m[j+1] since j is the highest such index.
			base = h[j] + (h[j+1]-h[j])*(mTop-m[j])/(m[j+1]-m[j])
			from = j
		}
		mMax := m[from]
		for k := from + 1; k <= b; k++ {
			mMax = math.Max(mMax, m[k])
		}
		d, dm := top-base, mMax-mTop
		if d > 0 && dm > 0 {
			// One constant for surface and elevated ducts: see ductFminConstMHz.
			f := ductFminConstMHz / (d * math.Sqrt(dm))
			if !found || f < bestF || (f == bestF && base < bestBase) {
				found, surface = true, j < 0
				bestF, bestBase, bestTop, bestDM = f, base, top, dm
			}
		}
		a = b
	}

	switch {
	case found && surface:
		res.Class = DuctClassSurface
	case found:
		res.Class = DuctClassElev
	case haveGrad && res.MinGradNkm <= ductSuperGradientNk:
		res.Class = DuctClassSuper
	default:
		res.Class = DuctClassNormal
	}
	if found {
		res.FminMHz, res.BaseM, res.TopM, res.DeltaM = bestF, bestBase, bestTop, bestDM
	}
	return res
}
