package main

// LOS maths for the planner: a Go port of public/los.js analyze().
// The server and browser must reach identical verdicts on identical terrain;
// tests/fixtures/planner-los-vectors.json is the shared proof (generated from
// the real los.js, checked by both Go and Node).

import "math"

const losEarthKm = 6371.0

// losParams: antenna heights are metres above ground at each end.
// KFactor 0 means the default 4/3; negative is invalid.
type losParams struct {
	FreqMHz, KFactor, AntennaAM, AntennaBM float64
}

// losResult mirrors MeshLOS.analyze: Clearance is the min over interior
// samples of (LOS − terrain)/F1 (+Inf when fewer than 3 samples), Verdict is
// clear | clear60 | marginal | blocked. With no interior sample WorstIdx is
// -1, WorstT/WorstKm are 0 and ObstructionM is -Inf.
type losResult struct {
	DistanceKm   float64
	Clearance    float64
	WorstIdx     int
	WorstT       float64
	WorstKm      float64
	ObstructionM float64
	Verdict      string
}

func losFinite(x float64) bool { return !math.IsNaN(x) && !math.IsInf(x, 0) }

func losVerdict(c float64) string {
	switch {
	case c >= 1:
		return "clear"
	case c >= 0.6:
		return "clear60"
	case c >= 0:
		return "marginal"
	}
	return "blocked"
}

// analyzeLOS: ok=false on invalid input (fewer than 2 samples, NaN/Inf,
// freq ≤ 0, k < 0).
func analyzeLOS(elevM []float64, distKm float64, p losParams) (losResult, bool) {
	n := len(elevM)
	if n < 2 {
		return losResult{}, false
	}
	for _, v := range elevM {
		if !losFinite(v) {
			return losResult{}, false
		}
	}
	if !(p.FreqMHz > 0) || !losFinite(p.FreqMHz) {
		return losResult{}, false
	}
	k := p.KFactor
	if k == 0 {
		k = 4.0 / 3.0
	}
	if !(k > 0) || !losFinite(k) {
		return losResult{}, false
	}
	D := distKm
	if !losFinite(D) || D < 0.001 {
		D = 0.001
	}
	aA, aB := p.AntennaAM, p.AntennaBM
	if !losFinite(aA) {
		aA = 0
	}
	if !losFinite(aB) {
		aB = 0
	}

	last := float64(n - 1)
	terrain := func(i int) float64 {
		d := float64(i) / last * D
		return elevM[i] + d*(D-d)/(2*k*losEarthKm)*1000
	}
	yA, yB := terrain(0)+aA, terrain(n-1)+aB
	res := losResult{DistanceKm: D, Clearance: math.Inf(1), WorstIdx: -1, ObstructionM: math.Inf(-1)}
	for i := 1; i <= n-2; i++ {
		t := float64(i) / last
		los := yA + (yB-yA)*t
		d := t * D
		f1 := 17.32 * math.Sqrt(math.Max(d*(D-d), 1e-9)/(D*p.FreqMHz/1000))
		tm := terrain(i)
		if c := (los - tm) / f1; c < res.Clearance {
			res.Clearance, res.WorstIdx = c, i
		}
		if ob := tm - los; ob > res.ObstructionM {
			res.ObstructionM = ob
		}
	}
	if res.WorstIdx >= 0 {
		res.WorstT = float64(res.WorstIdx) / last
		res.WorstKm = res.WorstT * D
	}
	res.Verdict = losVerdict(res.Clearance)
	return res, true
}

// planProfileSamples: samples for a leg, ~90 m spacing, clamped to [64, 600].
func planProfileSamples(distKm float64) int {
	if !losFinite(distKm) || distKm < 0 {
		return 64
	}
	n := math.Ceil(distKm*1000/90) + 1
	if n < 64 {
		return 64
	}
	if n > 600 {
		return 600
	}
	return int(n)
}

// sampleTerrainProfile returns n elevations from a to b along the great
// circle (t = i/(n-1)); ok=false if any sample is outside the view.
func sampleTerrainProfile(v terrainView, a, b geoPoint, n int) ([]float64, bool) {
	if v == nil || n < 2 {
		return nil, false
	}
	pts := greatCircleSamples(elevPoint{a.Lat, a.Lon}, elevPoint{b.Lat, b.Lon}, n)
	out := make([]float64, n)
	for i, p := range pts {
		e, ok := v.ElevationAt(p.lat, p.lon)
		if !ok {
			return nil, false
		}
		out[i] = e
	}
	return out, true
}

func geoDistKm(a, b geoPoint) float64 { return haversineKm(a.Lat, a.Lon, b.Lat, b.Lon) }

// geoIntermediate is the spherical intermediate point at fraction t of the
// great circle a→b (same formula as MeshLOS.intermediatePoint).
func geoIntermediate(a, b geoPoint, t float64) geoPoint {
	rad := math.Pi / 180
	p1, l1, p2, l2 := a.Lat*rad, a.Lon*rad, b.Lat*rad, b.Lon*rad
	h := math.Pow(math.Sin((p2-p1)/2), 2) + math.Cos(p1)*math.Cos(p2)*math.Pow(math.Sin((l2-l1)/2), 2)
	d := 2 * math.Asin(math.Min(1, math.Sqrt(h)))
	if d < 1e-12 {
		return a
	}
	A, B := math.Sin((1-t)*d)/math.Sin(d), math.Sin(t*d)/math.Sin(d)
	x := A*math.Cos(p1)*math.Cos(l1) + B*math.Cos(p2)*math.Cos(l2)
	y := A*math.Cos(p1)*math.Sin(l1) + B*math.Cos(p2)*math.Sin(l2)
	z := A*math.Sin(p1) + B*math.Sin(p2)
	return geoPoint{Lat: math.Atan2(z, math.Hypot(x, y)) / rad, Lon: math.Atan2(y, x) / rad}
}

func losRound(x float64, scale float64) float64 { return math.Round(x*scale) / scale }

// linkFromProfile summarises a profile as a PlannerLink; withElev attaches
// the profile decimated to ≤ 200 samples. Invalid input yields a zero link
// with a "blocked" verdict so it can never read as a usable path.
func linkFromProfile(elev []float64, distKm float64, p losParams, withElev bool) PlannerLink {
	r, ok := analyzeLOS(elev, distKm, p)
	if !ok {
		return PlannerLink{DistanceKm: losRound(math.Max(distKm, 0), 1000), Verdict: "blocked"}
	}
	pct := 999.0
	if !math.IsInf(r.Clearance, 1) {
		pct = losRound(r.Clearance*100, 10)
	}
	obs := r.ObstructionM
	if math.IsInf(obs, -1) {
		obs = 0 // no interior samples; -Inf is not JSON-encodable
	}
	l := PlannerLink{
		DistanceKm:   losRound(r.DistanceKm, 1000),
		ClearancePct: pct,
		Verdict:      r.Verdict,
		WorstKm:      losRound(r.WorstKm, 10),
		ObstructionM: losRound(obs, 10),
	}
	if withElev {
		d := decimateProfile(elev, 200)
		l.ElevM = make([]float64, len(d))
		for i, v := range d {
			l.ElevM[i] = losRound(v, 10)
		}
	}
	return l
}

// decimateProfile keeps both ends and at most max samples, preserving the
// per-bucket maximum so ridges survive. The result has exactly max samples
// when len(elev) > max (max < 3 returns the input unchanged).
func decimateProfile(elev []float64, max int) []float64 {
	n := len(elev)
	if n <= max || max < 3 {
		return elev
	}
	interior, buckets := n-2, max-2
	out := make([]float64, 0, max)
	out = append(out, elev[0])
	for j := 0; j < buckets; j++ {
		lo := 1 + j*interior/buckets
		hi := 1 + (j+1)*interior/buckets
		m := elev[lo]
		for _, v := range elev[lo+1 : hi] {
			if v > m {
				m = v
			}
		}
		out = append(out, m)
	}
	return append(out, elev[n-1])
}
