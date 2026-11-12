package main

// LOS maths for the planner: a Go port of public/los.js analyze().
// The server and browser must reach identical verdicts on identical terrain;
// tests/fixtures/planner-los-vectors.json is the shared proof (generated from
// the real los.js, checked by both Go and Node).

import "math"

// geoSamePointRadians: a leg shorter than this is one point to the samplers.
const geoSamePointRadians = 1e-9

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
		return elevM[i] + d*(D-d)/(2*k*earthRadiusKm)*1000
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
// circle (t = i/(n-1)); ok=false if any sample has no terrain (outside the
// view, or on a tile without data).
func sampleTerrainProfile(v terrainView, a, b geoPoint, n int) ([]float64, bool) {
	if n < 2 {
		return nil, false
	}
	return sampleTerrainAt(v, greatCircleSamples(a, b, n))
}

// sampleTerrainAt returns the elevation at each point; ok=false if any has
// no terrain.
func sampleTerrainAt(v terrainView, pts []geoPoint) ([]float64, bool) {
	if v == nil {
		return nil, false
	}
	out := make([]float64, len(pts))
	for i, p := range pts {
		e, ok := v.ElevationAt(p.Lat, p.Lon)
		if !ok {
			return nil, false
		}
		out[i] = e
	}
	return out, true
}

// legSamplePoints is where a profile reads the terrain of leg a→b:
// planProfileSamples(km) points along the great circle, both ends included.
func legSamplePoints(a, b geoPoint) []geoPoint {
	return greatCircleSamples(a, b, planProfileSamples(geoDistKm(a, b)))
}

// sampleLegProfile is leg a→b's terrain as both profile endpoints serve it
// (/api/planner/profile and /api/elevation/profile): the elevation at each
// of legSamplePoints, rounded to 0.1 m. One helper, so the same leg over the
// same tiles gets the same heights, and so the same verdict, in both views.
// ok=false if any sample has no terrain.
func sampleLegProfile(v terrainView, a, b geoPoint) ([]float64, bool) {
	elev, ok := sampleTerrainAt(v, legSamplePoints(a, b))
	if !ok {
		return nil, false
	}
	for i := range elev {
		elev[i] = losRound(elev[i], 10)
	}
	return elev, true
}

func geoDistKm(a, b geoPoint) float64 { return haversineKm(a.Lat, a.Lon, b.Lat, b.Lon) }

// greatCircleSamples returns n points from a to b (inclusive) along the great
// circle, at fractions t = i/(n-1), using the spherical intermediate-point
// formula. It is the one sampler of point slices; planArc is its one-point
// form for the search's hot loop.
func greatCircleSamples(a, b geoPoint, n int) []geoPoint {
	out := make([]geoPoint, n)
	p1, l1 := a.Lat*math.Pi/180, a.Lon*math.Pi/180
	p2, l2 := b.Lat*math.Pi/180, b.Lon*math.Pi/180
	d := geoDistKm(a, b) / earthRadiusKm
	if d < geoSamePointRadians {
		for i := range out {
			out[i] = a
		}
		return out
	}
	sd := math.Sin(d)
	for i := range out {
		f := float64(i) / float64(n-1)
		A := math.Sin((1-f)*d) / sd
		B := math.Sin(f*d) / sd
		x := A*math.Cos(p1)*math.Cos(l1) + B*math.Cos(p2)*math.Cos(l2)
		y := A*math.Cos(p1)*math.Sin(l1) + B*math.Cos(p2)*math.Sin(l2)
		z := A*math.Sin(p1) + B*math.Sin(p2)
		out[i] = geoPoint{
			Lat: math.Atan2(z, math.Hypot(x, y)) * 180 / math.Pi,
			Lon: math.Atan2(y, x) * 180 / math.Pi,
		}
	}
	// Endpoints are exact, not trig round-trips.
	out[0], out[n-1] = a, b
	return out
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
