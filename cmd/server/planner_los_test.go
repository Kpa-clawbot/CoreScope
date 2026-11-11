package main

import (
	"encoding/json"
	"math"
	"os"
	"testing"
)

type losVectorFile struct {
	Name  string `json:"name"`
	Input struct {
		ElevM      []float64 `json:"elevM"`
		DistanceKm float64   `json:"distanceKm"`
		FreqMHz    float64   `json:"freqMHz"`
		KFactor    float64   `json:"kFactor"`
		AntennaAM  float64   `json:"antennaAM"`
		AntennaBM  float64   `json:"antennaBM"`
	} `json:"input"`
	Expect struct {
		Clearance    *float64 `json:"clearance"` // null = +Inf
		WorstIdx     int      `json:"worstIdx"`
		ObstructionM *float64 `json:"obstructionM"` // null = -Inf
		WorstKm      *float64 `json:"worstKm"`
		DistanceKm   float64  `json:"distanceKm"`
		Verdict      string   `json:"verdict"`
	} `json:"expect"`
}

func loadLOSVectors(t *testing.T) []losVectorFile {
	t.Helper()
	raw, err := os.ReadFile("../../tests/fixtures/planner-los-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vs []losVectorFile
	if err := json.Unmarshal(raw, &vs); err != nil {
		t.Fatal(err)
	}
	if len(vs) < 6 {
		t.Fatalf("want >= 6 vectors, got %d", len(vs))
	}
	return vs
}

func near(a, b float64) bool { return math.Abs(a-b) <= 1e-9 }

func TestPlannerLOSVectorsMatchJS(t *testing.T) {
	for _, v := range loadLOSVectors(t) {
		t.Run(v.Name, func(t *testing.T) {
			p := losParams{v.Input.FreqMHz, v.Input.KFactor, v.Input.AntennaAM, v.Input.AntennaBM}
			r, ok := analyzeLOS(v.Input.ElevM, v.Input.DistanceKm, p)
			if !ok {
				t.Fatal("ok=false")
			}
			e := v.Expect
			if e.Clearance == nil {
				if !math.IsInf(r.Clearance, 1) {
					t.Errorf("clearance %v, want +Inf", r.Clearance)
				}
			} else if !near(r.Clearance, *e.Clearance) {
				t.Errorf("clearance %v, want %v", r.Clearance, *e.Clearance)
			}
			if e.ObstructionM == nil {
				if !math.IsInf(r.ObstructionM, -1) {
					t.Errorf("obstruction %v, want -Inf", r.ObstructionM)
				}
			} else if !near(r.ObstructionM, *e.ObstructionM) {
				t.Errorf("obstruction %v, want %v", r.ObstructionM, *e.ObstructionM)
			}
			if e.WorstKm != nil && !near(r.WorstKm, *e.WorstKm) {
				t.Errorf("worstKm %v, want %v", r.WorstKm, *e.WorstKm)
			}
			if r.WorstIdx != e.WorstIdx {
				t.Errorf("worstIdx %d, want %d", r.WorstIdx, e.WorstIdx)
			}
			if r.Verdict != e.Verdict {
				t.Errorf("verdict %s, want %s", r.Verdict, e.Verdict)
			}
			if !near(r.DistanceKm, e.DistanceKm) {
				t.Errorf("distanceKm %v, want %v", r.DistanceKm, e.DistanceKm)
			}
		})
	}
}

func TestPlannerLOSVectorsCoverAllVerdicts(t *testing.T) {
	seen := map[string]bool{}
	for _, v := range loadLOSVectors(t) {
		seen[v.Expect.Verdict] = true
	}
	for _, w := range []string{"clear", "clear60", "marginal", "blocked"} {
		if !seen[w] {
			t.Errorf("no vector with verdict %s", w)
		}
	}
}

func TestPlannerLOSInvalidInput(t *testing.T) {
	ok := losParams{FreqMHz: 869.6}
	cases := map[string]struct {
		e []float64
		d float64
		p losParams
	}{
		"one sample": {[]float64{1}, 1, ok},
		"nan elev":   {[]float64{1, math.NaN(), 2}, 1, ok},
		"inf elev":   {[]float64{1, math.Inf(1), 2}, 1, ok},
		"freq zero":  {[]float64{1, 2, 3}, 1, losParams{}},
		"freq nan":   {[]float64{1, 2, 3}, 1, losParams{FreqMHz: math.NaN()}},
		"k negative": {[]float64{1, 2, 3}, 1, losParams{FreqMHz: 869.6, KFactor: -1}},
	}
	for name, c := range cases {
		if _, got := analyzeLOS(c.e, c.d, c.p); got {
			t.Errorf("%s: ok=true", name)
		}
	}
	// k=0 defaults to 4/3.
	a, _ := analyzeLOS([]float64{0, 0, 0}, 10, losParams{FreqMHz: 869.6})
	b, _ := analyzeLOS([]float64{0, 0, 0}, 10, losParams{FreqMHz: 869.6, KFactor: 4.0 / 3.0})
	if a.Clearance != b.Clearance {
		t.Errorf("k default mismatch %v vs %v", a.Clearance, b.Clearance)
	}
}

func TestPlannerLOSLinkFromProfile(t *testing.T) {
	p := losParams{FreqMHz: 869.618, KFactor: 4.0 / 3.0}
	l := linkFromProfile([]float64{120, 340}, 5.00049, p, true)
	if l.ClearancePct != 999 || l.Verdict != "clear" || l.ObstructionM != 0 || l.DistanceKm != 5.000 {
		t.Errorf("n=2 link: %+v", l)
	}
	if _, err := json.Marshal(l); err != nil {
		t.Errorf("link not JSON-encodable: %v", err)
	}
	elev := make([]float64, 41)
	for i := range elev {
		elev[i] = 100.04
	}
	l = linkFromProfile(elev, 3, losParams{FreqMHz: 869.618, KFactor: 4.0 / 3.0, AntennaAM: 25, AntennaBM: 25}, true)
	if l.Verdict != "clear" || l.ClearancePct != 154.6 || l.WorstKm != 1.5 || len(l.ElevM) != 41 || l.ElevM[0] != 100 {
		t.Errorf("flat link: %+v", l)
	}
	if l = linkFromProfile(elev, 3, p, false); l.ElevM != nil {
		t.Error("ElevM set without withElev")
	}
	if l = linkFromProfile(nil, 3, p, true); l.Verdict != "blocked" {
		t.Errorf("invalid input verdict %q", l.Verdict)
	}
}

func TestPlannerLOSProfileSamples(t *testing.T) {
	cases := []struct {
		d    float64
		want int
	}{{0, 64}, {1, 64}, {5.67, 64}, {5.76, 65}, {5.8, 66}, {9, 101}, {53.8, 599}, {54, 600}, {200, 600}, {math.NaN(), 64}}
	for _, c := range cases {
		if got := planProfileSamples(c.d); got != c.want {
			t.Errorf("planProfileSamples(%v) = %d, want %d", c.d, got, c.want)
		}
	}
}

func TestPlannerLOSDecimate(t *testing.T) {
	in := make([]float64, 1000)
	for i := range in {
		in[i] = float64(i % 37)
	}
	in[500] = 9999 // lone ridge
	in[999] = 7
	out := decimateProfile(in, 200)
	if len(out) != 200 || out[0] != in[0] || out[199] != 7 {
		t.Fatalf("len=%d first=%v last=%v", len(out), out[0], out[len(out)-1])
	}
	mx := 0.0
	for _, v := range out {
		mx = math.Max(mx, v)
	}
	if mx != 9999 {
		t.Errorf("ridge lost: max %v", mx)
	}
	short := []float64{1, 2, 3}
	if got := decimateProfile(short, 3); len(got) != 3 {
		t.Error("short input changed")
	}
	if got := decimateProfile(in[:201], 200); len(got) != 200 {
		t.Errorf("201->200 len %d", len(got))
	}
	if got := decimateProfile(in, 3); len(got) != 3 {
		t.Errorf("max=3 len %d", len(got))
	}
}

type rampView struct{ minLon float64 }

func (r rampView) ElevationAt(lat, lon float64) (float64, bool) {
	if lon < r.minLon {
		return 0, false
	}
	return lon * 10, true // metres per degree of longitude
}

func TestPlannerLOSTerrainProfile(t *testing.T) {
	a, b := geoPoint{0, 0}, geoPoint{0, 1}
	prof, ok := sampleTerrainProfile(rampView{-1}, a, b, 11)
	if !ok || len(prof) != 11 {
		t.Fatalf("ok=%v len=%d", ok, len(prof))
	}
	for i, v := range prof {
		if want := float64(i); math.Abs(v-want) > 1e-6 { // 10 m/deg over 1 deg, 0.1 steps
			t.Errorf("prof[%d]=%v want %v", i, v, want)
		}
	}
	if _, ok := sampleTerrainProfile(rampView{0.5}, a, b, 11); ok {
		t.Error("out-of-view returned ok")
	}
	if _, ok := sampleTerrainProfile(rampView{-1}, a, b, 1); ok {
		t.Error("n=1 returned ok")
	}
}

func TestPlannerLOSGeoHelpers(t *testing.T) {
	m := geoIntermediate(geoPoint{0, 0}, geoPoint{0, 90}, 0.5)
	if math.Abs(m.Lat) > 1e-9 || math.Abs(m.Lon-45) > 1e-9 {
		t.Errorf("midpoint %+v", m)
	}
	if got := geoIntermediate(geoPoint{3, 4}, geoPoint{3, 4}, 0.5); got != (geoPoint{3, 4}) {
		t.Errorf("same point %+v", got)
	}
	if d := geoDistKm(geoPoint{0, 0}, geoPoint{0, 90}); math.Abs(d-losEarthKm*math.Pi/2) > 1e-6 {
		t.Errorf("dist %v", d)
	}
}
