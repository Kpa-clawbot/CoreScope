package main

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const testLocSecret = "0123456789abcdef0123456789abcdef-test-only"

// newTestLocFuzzer builds an enabled fuzzer with the test secret, with the
// environment override cleared.
func newTestLocFuzzer(t testing.TB, radiusM float64, secret string) *locationFuzzer {
	t.Helper()
	if tt, ok := t.(*testing.T); ok {
		tt.Setenv(locationSecretEnv, "")
	} else {
		os.Unsetenv(locationSecretEnv)
	}
	f, err := newLocationFuzzer(&LocationPrivacyConfig{Enabled: true, RadiusM: radiusM, Secret: secret})
	if err != nil {
		t.Fatalf("newLocationFuzzer: %v", err)
	}
	return f
}

// distM is the great-circle distance CoreScope uses everywhere, in metres.
func distM(lat1, lon1, lat2, lon2 float64) float64 {
	return haversineKm(lat1, lon1, lat2, lon2) * 1000
}

// checkServed asserts R2 for one served point.
func checkServed(t *testing.T, f *locationFuzzer, lat, lon, sLat, sLon float64) {
	t.Helper()
	if sLat == 0 && sLon == 0 {
		t.Fatalf("served (0,0) for (%v,%v)", lat, lon)
	}
	if sLat < -90 || sLat > 90 || sLon < -180 || sLon >= 180 || math.IsNaN(sLat) || math.IsNaN(sLon) {
		t.Fatalf("served (%v,%v) out of range for (%v,%v)", sLat, sLon, lat, lon)
	}
	// ≤ R with the ellipsoid slack to spare: on the sphere the served point
	// is never farther than R·(1 − locRelSlack).
	if d := distM(lat, lon, sLat, sLon); d > f.RadiusM()*(1-locRelSlack) {
		t.Fatalf("served (%v,%v) is %.3f m from (%v,%v), radius %v", sLat, sLon, d, lat, lon, f.RadiusM())
	}
}

// randomGlobePoint is uniform on the sphere, with a share of points forced
// near the poles, the ±85 clamp, the antimeridian and null island.
func randomGlobePoint(rng *rand.Rand) (float64, float64) {
	lat := math.Asin(2*rng.Float64()-1) * 180 / math.Pi
	lon := rng.Float64()*360 - 180
	switch rng.Intn(10) {
	case 0:
		lat = math.Copysign(85+rng.Float64()*5, rng.Float64()-0.5)
	case 1:
		lat = math.Copysign(84.5+rng.Float64(), rng.Float64()-0.5)
	case 2:
		lon = math.Copysign(180-rng.Float64()*0.01, rng.Float64()-0.5)
	case 3:
		lat, lon = (rng.Float64()-0.5)*0.02, (rng.Float64()-0.5)*0.02
	}
	if lat == 0 && lon == 0 {
		lat = 1e-9
	}
	return lat, lon
}

func TestLocationPrivacyConfigValidation(t *testing.T) {
	long := strings.Repeat("s", 32)
	cases := []struct {
		name    string
		cfg     *LocationPrivacyConfig
		env     string
		wantNil bool
		wantErr bool
		radius  float64
	}{
		{name: "nil block", cfg: nil, wantNil: true},
		{name: "disabled", cfg: &LocationPrivacyConfig{Enabled: false, Secret: "x", RadiusM: 5}, wantNil: true},
		{name: "missing secret", cfg: &LocationPrivacyConfig{Enabled: true}, wantErr: true},
		{name: "31-byte secret", cfg: &LocationPrivacyConfig{Enabled: true, Secret: long[:31]}, wantErr: true},
		{name: "32-byte secret, default radius", cfg: &LocationPrivacyConfig{Enabled: true, Secret: long}, radius: 1000},
		{name: "radius 99", cfg: &LocationPrivacyConfig{Enabled: true, Secret: long, RadiusM: 99}, wantErr: true},
		{name: "radius 100", cfg: &LocationPrivacyConfig{Enabled: true, Secret: long, RadiusM: 100}, radius: 100},
		{name: "radius 50000", cfg: &LocationPrivacyConfig{Enabled: true, Secret: long, RadiusM: 50000}, radius: 50000},
		{name: "radius 50001", cfg: &LocationPrivacyConfig{Enabled: true, Secret: long, RadiusM: 50001}, wantErr: true},
		{name: "negative radius", cfg: &LocationPrivacyConfig{Enabled: true, Secret: long, RadiusM: -1000}, wantErr: true},
		{name: "NaN radius", cfg: &LocationPrivacyConfig{Enabled: true, Secret: long, RadiusM: math.NaN()}, wantErr: true},
		{name: "env supplies the secret", cfg: &LocationPrivacyConfig{Enabled: true}, env: long, radius: 1000},
		{name: "env overrides a short config secret", cfg: &LocationPrivacyConfig{Enabled: true, Secret: "short"}, env: long, radius: 1000},
		{name: "short env overrides a good config secret", cfg: &LocationPrivacyConfig{Enabled: true, Secret: long}, env: "short-env-secret", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(locationSecretEnv, tc.env)
			f, err := newLocationFuzzer(tc.cfg)
			switch {
			case tc.wantErr:
				if err == nil || f != nil {
					t.Fatalf("want error, got f=%v err=%v", f != nil, err)
				}
				for _, s := range []string{tc.env, tc.cfg.Secret} {
					if len(s) >= 4 && strings.Contains(err.Error(), s) {
						t.Fatalf("error leaks the secret: %v", err)
					}
				}
			case tc.wantNil:
				if err != nil || f != nil {
					t.Fatalf("want (nil, nil), got f=%v err=%v", f != nil, err)
				}
			default:
				if err != nil || f == nil {
					t.Fatalf("want fuzzer, got err=%v", err)
				}
				if f.RadiusM() != tc.radius || !f.Enabled() {
					t.Fatalf("radius %v enabled %v, want %v", f.RadiusM(), f.Enabled(), tc.radius)
				}
			}
		})
	}
}

func TestLocationPrivacyEnvSecretChangesOutput(t *testing.T) {
	a := newTestLocFuzzer(t, 1000, testLocSecret)
	t.Setenv(locationSecretEnv, strings.Repeat("e", 40))
	b, err := newLocationFuzzer(&LocationPrivacyConfig{Enabled: true, Secret: testLocSecret})
	if err != nil {
		t.Fatal(err)
	}
	la, oa := a.Fuzz("ab", 50.85, 4.35)
	lb, ob := b.Fuzz("ab", 50.85, 4.35)
	if la == lb && oa == ob {
		t.Fatal("env secret did not replace the config secret")
	}
}

func TestLocationFuzzNilIsIdentity(t *testing.T) {
	var f *locationFuzzer
	if lat, lon := f.Fuzz("ab", 12.5, -3.25); lat != 12.5 || lon != -3.25 {
		t.Fatalf("nil fuzzer changed the position: %v,%v", lat, lon)
	}
	if f.Enabled() || f.RadiusM() != 0 {
		t.Fatal("nil fuzzer reports enabled")
	}
	var c *Config
	if c.LocationFuzzer() != nil {
		t.Fatal("nil config has a fuzzer")
	}
	c.setLocationFuzzer(newTestLocFuzzer(t, 1000, testLocSecret)) // must not panic
}

func TestLocationFuzzNoFixPassthrough(t *testing.T) {
	f := newTestLocFuzzer(t, 1000, testLocSecret)
	if lat, lon := f.Fuzz("ab", 0, 0); lat != 0 || lon != 0 {
		t.Fatalf("no-fix (0,0) became (%v,%v)", lat, lon)
	}
	lat, lon := f.Fuzz("ab", math.NaN(), 4)
	if !math.IsNaN(lat) || lon != 4 {
		t.Fatalf("non-finite input not passed through: %v,%v", lat, lon)
	}
	if f.anchorCount() != 0 {
		t.Fatal("passthrough stored an anchor")
	}
}

// R2 property: ≥ 100k random points per radius, fixed seed.
func TestLocationFuzzBoundProperty(t *testing.T) {
	for _, radius := range []float64{100, 1000, 50000} {
		t.Run(fmt.Sprint(radius), func(t *testing.T) {
			f := newTestLocFuzzer(t, radius, testLocSecret)
			rng := rand.New(rand.NewSource(20261123))
			n := 100000
			if radius != 1000 {
				n = 30000
			}
			var sum float64
			for i := 0; i < n; i++ {
				lat, lon := randomGlobePoint(rng)
				pk := fmt.Sprintf("%064x", i%5000)
				sLat, sLon := f.Fuzz(pk, lat, lon)
				checkServed(t, f, lat, lon, sLat, sLon)
				sum += distM(lat, lon, sLat, sLon)
			}
			// The offset must actually move points: mean distance well
			// above zero (cell centre ± disk gives about half the radius).
			if mean := sum / float64(n); mean < 0.2*radius {
				t.Fatalf("mean displacement %.1f m is too small for radius %v", mean, radius)
			}
		})
	}
}

func TestLocationFuzzBoundEdgeCases(t *testing.T) {
	pts := [][2]float64{
		{90, 0}, {-90, 0}, {90, 179.999}, {-90, -179.999},
		{89.9, 0}, {-89.9, 123.4}, {89.9, 179.999}, {-89.9, -179.999},
		{85, 10}, {-85, 10}, {85.0001, 10}, {84.9999, 10}, {-85.0001, -10},
		{0, 179.999}, {0, -179.999}, {45, 179.999}, {45, -179.999}, {-33.9, 180}, {-33.9, -180},
		{10, 540}, {10, -540}, {95, 0}, {-95, 0},
		{1e-9, 0}, {0, 1e-9}, {-1e-7, 1e-7}, {1e-4, -1e-4},
	}
	for _, radius := range []float64{100, 1000, 50000} {
		f := newTestLocFuzzer(t, radius, testLocSecret)
		for i, p := range pts {
			for k := 0; k < 50; k++ {
				pk := fmt.Sprintf("%02x%02x", i, k)
				sLat, sLon := f.Fuzz(pk, p[0], p[1])
				// Out-of-range input is measured from its clamped/wrapped form.
				lat := math.Max(-90, math.Min(90, p[0]))
				checkServed(t, f, lat, wrapLon(p[1]), sLat, sLon)
			}
		}
	}
}

// R2 under hysteresis: random walks with small and large steps.
func TestLocationFuzzBoundUnderHysteresis(t *testing.T) {
	for _, radius := range []float64{100, 1000, 50000} {
		f := newTestLocFuzzer(t, radius, testLocSecret)
		rng := rand.New(rand.NewSource(7))
		for node := 0; node < 200; node++ {
			pk := fmt.Sprintf("node%d", node)
			lat, lon := randomGlobePoint(rng)
			for step := 0; step < 200; step++ {
				stepM := radius * 0.05
				if rng.Intn(10) == 0 {
					stepM = radius * 2
				}
				lat, lon = destinationPoint(lat, lon, rng.Float64()*2*math.Pi, stepM*rng.Float64())
				if lat == 0 && lon == 0 {
					continue
				}
				sLat, sLon := f.Fuzz(pk, lat, lon)
				checkServed(t, f, lat, lon, sLat, sLon)
			}
		}
	}
}

func TestLocationFuzzDeterministicAcrossInstances(t *testing.T) {
	a := newTestLocFuzzer(t, 1000, testLocSecret)
	b := newTestLocFuzzer(t, 1000, testLocSecret)
	other := newTestLocFuzzer(t, 1000, testLocSecret+"x")
	rng := rand.New(rand.NewSource(3))
	differ := 0
	for i := 0; i < 2000; i++ {
		lat, lon := randomGlobePoint(rng)
		pk := fmt.Sprintf("pk%d", i)
		la, oa := a.Fuzz(pk, lat, lon)
		lb, ob := b.Fuzz(pk, lat, lon)
		if la != lb || oa != ob {
			t.Fatalf("instances disagree for %s at (%v,%v): (%v,%v) vs (%v,%v)", pk, lat, lon, la, oa, lb, ob)
		}
		if lc, oc := other.Fuzz(pk, lat, lon); lc != la || oc != oa {
			differ++
		}
	}
	if differ < 1990 {
		t.Fatalf("a different secret changed only %d of 2000 outputs", differ)
	}
}

func TestLocationFuzzPubkeyCaseInsensitive(t *testing.T) {
	a := newTestLocFuzzer(t, 1000, testLocSecret)
	b := newTestLocFuzzer(t, 1000, testLocSecret)
	la, oa := a.Fuzz("ABCDEF0123", 50.85, 4.35)
	lb, ob := b.Fuzz("abcdef0123", 50.85, 4.35)
	if la != lb || oa != ob {
		t.Fatal("pubkey case changed the output")
	}
	// One anchor for both spellings.
	a.Fuzz("abcdef0123", 50.85, 4.35)
	if a.anchorCount() != 1 {
		t.Fatalf("anchors %d, want 1", a.anchorCount())
	}
}

// Offsets derive from the cell only: nodes in one cell share one served
// point, so averaging co-located nodes cannot converge on the cell centre.
func TestLocationFuzzColocatedNodesShareOnePoint(t *testing.T) {
	f := newTestLocFuzzer(t, 1000, testLocSecret)
	l0, o0 := f.Fuzz("pk0", 50.85, 4.35)
	for i := 1; i < 100; i++ {
		if l, o := f.Fuzz(fmt.Sprintf("pk%d", i), 50.85, 4.35); l != l0 || o != o0 {
			t.Fatalf("pk%d at the same exact spot got (%v,%v), want (%v,%v)", i, l, o, l0, o0)
		}
	}
}

func TestLocationFuzzSameCellSharesOnePoint(t *testing.T) {
	f := newTestLocFuzzer(t, 1000, testLocSecret)
	rng := rand.New(rand.NewSource(13))
	for k := 0; k < 200; k++ {
		lat, lon := rng.Float64()*120-60, rng.Float64()*360-180
		c := f.cellAt(lat, lon)
		// A second spot strictly inside the same cell (not its margin).
		latC := (c.latLo + c.latHi) / 2
		halfLat := (c.latHi - c.latLo) / 2 / (1 + 2*locMarginFrac)
		halfLon := c.halfLonX / (1 + 2*locMarginFrac)
		lat2 := latC + (rng.Float64()*2-1)*0.9*halfLat
		lon2 := wrapLon(c.lonC + (rng.Float64()*2-1)*0.9*halfLon)
		if c2 := f.cellAt(lat2, lon2); c2.row != c.row || c2.col != c.col {
			t.Fatalf("test spot left the cell")
		}
		la, oa := f.Fuzz(fmt.Sprintf("a%d", k), lat, lon)
		lb, ob := f.Fuzz(fmt.Sprintf("b%d", k), lat2, lon2)
		if la != lb || oa != ob {
			t.Fatalf("two nodes in one cell got (%v,%v) and (%v,%v)", la, oa, lb, ob)
		}
		checkServed(t, f, lat, lon, la, oa)
		checkServed(t, f, lat2, lon2, lb, ob)
	}
}

// metresOffset moves (lat, lon) by north/east metres in the grid's degree
// frame.
func metresOffset(lat, lon, northM, eastM float64) (float64, float64) {
	return lat + northM/locMetresPerDeg, wrapLon(lon + eastM/(locMetresPerDeg*math.Cos(lat*math.Pi/180)))
}

func TestLocationFuzzHysteresis(t *testing.T) {
	f := newTestLocFuzzer(t, 1000, testLocSecret)
	s := f.cellM
	rng := rand.New(rand.NewSource(11))
	for node := 0; node < 300; node++ {
		pk := fmt.Sprintf("h%d", node)
		lat := rng.Float64()*120 - 60
		lon := rng.Float64()*360 - 180
		l0, o0 := f.Fuzz(pk, lat, lon)
		// Jitter: any move under 0.25·s per axis (minus a little for the
		// cos change across the row) stays inside the expanded anchor cell.
		for k := 0; k < 20; k++ {
			jl, jo := metresOffset(lat, lon, (rng.Float64()*2-1)*0.24*s, (rng.Float64()*2-1)*0.23*s)
			if l, o := f.Fuzz(pk, jl, jo); l != l0 || o != o0 {
				t.Fatalf("%s: jitter inside the margin changed the output", pk)
			}
		}
		// A move of 5·R re-anchors: a different cell, a different point.
		fl, fo := metresOffset(lat, lon, 5*f.RadiusM(), 0)
		l1, o1 := f.Fuzz(pk, fl, fo)
		if l1 == l0 && o1 == o0 {
			t.Fatalf("%s: moving 5 km did not re-anchor", pk)
		}
		checkServed(t, f, fl, fo, l1, o1)
		// Back home re-anchors to the home cell and yields its point again,
		// unless home sat in the margin of the old cell, which it does not
		// after a 5·R excursion.
		if l, o := f.Fuzz(pk, lat, lon); l != l0 || o != o0 {
			t.Fatalf("%s: returning home did not restore the home point", pk)
		}
	}
}

func TestLocationFuzzHysteresisKeepsAnchorAcrossCellEdge(t *testing.T) {
	f := newTestLocFuzzer(t, 1000, testLocSecret)
	// Find a row boundary and sit just south of it, then step just north
	// (inside the margin): same output. Then step past the margin: new cell.
	row := math.Floor(40/f.dLat + f.fLat)
	edge := (row + 1 - f.fLat) * f.dLat // northern edge of the row
	lon := 4.35
	south := edge - 0.01*f.dLat
	l0, o0 := f.Fuzz("edge", south, lon)
	if l, o := f.Fuzz("edge", edge+0.2*f.dLat, lon); l != l0 || o != o0 {
		t.Fatal("crossing into the margin re-anchored")
	}
	l2, o2 := f.Fuzz("edge", edge+0.3*f.dLat, lon)
	if l2 == l0 && o2 == o0 {
		t.Fatal("leaving the margin did not re-anchor")
	}
	// A fresh instance at the same northern point picks the northern cell
	// directly, so the re-anchored point is the stateless answer.
	g := newTestLocFuzzer(t, 1000, testLocSecret)
	if l, o := g.Fuzz("edge", edge+0.3*f.dLat, lon); l != l2 || o != o2 {
		t.Fatal("re-anchored point differs from the stateless one")
	}
}

func TestLocationFuzzAntimeridian(t *testing.T) {
	f := newTestLocFuzzer(t, 1000, testLocSecret)
	for _, lat := range []float64{-45, 0.5, 30, 60} {
		pk := fmt.Sprintf("am%v", lat)
		l0, o0 := f.Fuzz(pk, lat, 179.9995)
		checkServed(t, f, lat, 179.9995, l0, o0)
		// ~100 m east, across the antimeridian: inside the margin of
		// whatever cell holds 179.9995 only if close enough; check the
		// bound always, and identity when the step is under 0.2·s.
		step := 0.001 * locMetresPerDeg * math.Cos(lat*math.Pi/180)
		l1, o1 := f.Fuzz(pk, lat, -179.9995)
		checkServed(t, f, lat, -179.9995, l1, o1)
		if step < 0.2*f.cellM && (l1 != l0 || o1 != o0) {
			t.Fatalf("lat %v: a %.0f m step across the antimeridian re-anchored", lat, step)
		}
	}
	// A cell spans the antimeridian seamlessly: the column containing
	// -180 and the one containing 179.999... are adjacent, not a gap.
	for _, lat := range []float64{0, 40, 84} {
		a := f.cellAt(lat, 179.99999)
		b := f.cellAt(lat, -179.99999)
		ncolA := int64(math.Round(360 / (2 * a.halfLonX / (1 + 2*locMarginFrac))))
		if d := (b.col - a.col + ncolA) % ncolA; d != 0 && d != 1 {
			t.Fatalf("lat %v: columns %d and %d are not adjacent across the antimeridian (ncol %d)", lat, a.col, b.col, ncolA)
		}
	}
}

func TestLocationFuzzPolarClamp(t *testing.T) {
	f := newTestLocFuzzer(t, 1000, testLocSecret)
	for _, lat := range []float64{85.5, 89.9, 90, -89.9, -87} {
		for k := 0; k < 200; k++ {
			lon := float64(k)*1.8 - 180
			sLat, sLon := f.Fuzz(fmt.Sprintf("p%d", k), lat, lon)
			checkServed(t, f, lat, lon, sLat, sLon)
		}
	}
	// The cell is chosen at ±85: two polar latitudes on one meridian share
	// the anchor (and the offset from the exact point).
	a := newTestLocFuzzer(t, 1000, testLocSecret)
	l1, o1 := a.Fuzz("polar", 88, 10)
	c1, _ := a.anchorFor("polar")
	l2, o2 := a.Fuzz("polar", 89.5, 10)
	c2, _ := a.anchorFor("polar")
	if c1.row != c2.row || c1.col != c2.col {
		t.Fatal("polar latitudes on one meridian got different anchors")
	}
	if d1, d2 := distM(88, 10, l1, o1), distM(89.5, 10, l2, o2); math.Abs(d1-d2) > 1e-3 {
		t.Fatalf("polar offset distance changed with latitude: %.4f vs %.4f m", d1, d2)
	}
}

func TestLocationFuzzNeverNullIsland(t *testing.T) {
	f := newTestLocFuzzer(t, 100, testLocSecret)
	rng := rand.New(rand.NewSource(5))
	for i := 0; i < 20000; i++ {
		lat, lon := (rng.Float64()-0.5)*0.002, (rng.Float64()-0.5)*0.002
		if lat == 0 && lon == 0 {
			continue
		}
		sLat, sLon := f.Fuzz(fmt.Sprintf("n%d", i), lat, lon)
		checkServed(t, f, lat, lon, sLat, sLon)
		if math.Abs(sLat) < locNullEps && math.Abs(sLon) < locNullEps {
			t.Fatalf("served (%v,%v) reads as no fix", sLat, sLon)
		}
	}
	cases := [][4]float64{
		{0, 0, locNullEps, 0},
		{3e-6, -4e-6, locNullEps, -4e-6},
		{-3e-6, 4e-6, -locNullEps, 4e-6},
		{2e-5, 0, 2e-5, 0},
		{0, 2e-5, 0, 2e-5},
	}
	for _, c := range cases {
		if lat, lon := avoidNullIsland(c[0], c[1]); lat != c[2] || lon != c[3] {
			t.Errorf("avoidNullIsland(%v,%v) = (%v,%v), want (%v,%v)", c[0], c[1], lat, lon, c[2], c[3])
		}
	}
}

func TestLocationFuzzWrapLon(t *testing.T) {
	cases := [][2]float64{{0, 0}, {179.5, 179.5}, {180, -180}, {-180, -180}, {540, -180}, {181, -179}, {-181, 179}, {-540, -180}, {359.5, -0.5}}
	for _, c := range cases {
		if got := wrapLon(c[0]); math.Abs(got-c[1]) > 1e-9 {
			t.Errorf("wrapLon(%v) = %v, want %v", c[0], got, c[1])
		}
	}
}

func TestLocationFuzzGridGeometry(t *testing.T) {
	f := newTestLocFuzzer(t, 1000, testLocSecret)
	if f.cellM != 400 || math.Abs(f.dLat-400/locMetresPerDeg) > 1e-15 {
		t.Fatalf("cell %v m, row %v deg", f.cellM, f.dLat)
	}
	if f.fLat < 0 || f.fLat >= 1 || f.fLon < 0 || f.fLon >= 1 || (f.fLat == 0 && f.fLon == 0) {
		t.Fatalf("grid offsets %v %v", f.fLat, f.fLon)
	}
	g := newTestLocFuzzer(t, 1000, testLocSecret+"other")
	if g.fLat == f.fLat || g.fLon == f.fLon {
		t.Fatal("grid origin does not depend on the secret")
	}
	// A cell is about s wide in metres at its centre latitude, never wider.
	for _, lat := range []float64{0, 30, 60, 84.9} {
		a := f.cellAt(lat, 10)
		latC := (a.latLo + a.latHi) / 2
		wM := 2 * a.halfLonX / (1 + 2*locMarginFrac) * locMetresPerDeg * math.Cos(latC*math.Pi/180)
		if wM > f.cellM*(1+1e-9) || wM < f.cellM*0.98 {
			t.Fatalf("lat %v: cell width %.2f m, want about %v", lat, wM, f.cellM)
		}
		if !a.contains(lat, 10) {
			t.Fatalf("lat %v: cell does not contain its own point", lat)
		}
	}
}

func TestLocationFuzzAnchorCap(t *testing.T) {
	f := newTestLocFuzzer(t, 1000, testLocSecret)
	if f.anchorCap != 200000 {
		t.Fatalf("anchor cap %d, want 200000", f.anchorCap)
	}
	f.anchorCap = 100
	rng := rand.New(rand.NewSource(9))
	for i := 0; i < 5000; i++ {
		lat, lon := randomGlobePoint(rng)
		sLat, sLon := f.Fuzz(fmt.Sprintf("cap%d", i), lat, lon)
		checkServed(t, f, lat, lon, sLat, sLon)
		if n := f.anchorCount(); n > 100 {
			t.Fatalf("anchor map grew to %d past its cap of 100", n)
		}
	}
	if f.anchorCount() != 100 {
		t.Fatalf("anchor map %d, want full at 100", f.anchorCount())
	}
	// An evicted node re-anchors to the same stateless answer.
	g := newTestLocFuzzer(t, 1000, testLocSecret)
	l1, o1 := f.Fuzz("cap0", 10, 10)
	l2, o2 := g.Fuzz("cap0", 10, 10)
	if l1 != l2 || o1 != o2 {
		t.Fatal("eviction changed the stateless output")
	}
}

func TestLocationFuzzConcurrent(t *testing.T) {
	f := newTestLocFuzzer(t, 1000, testLocSecret)
	f.anchorCap = 150 // exercise eviction under contention too
	var wg sync.WaitGroup
	errs := make(chan string, 32)
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))
			for i := 0; i < 2000; i++ {
				pk := fmt.Sprintf("c%d", rng.Intn(200))
				lat, lon := randomGlobePoint(rng)
				sLat, sLon := f.Fuzz(pk, lat, lon)
				if d := distM(lat, lon, sLat, sLon); d > f.RadiusM() || (sLat == 0 && sLon == 0) {
					errs <- fmt.Sprintf("%s (%v,%v) -> (%v,%v) %.1f m", pk, lat, lon, sLat, sLon, d)
					return
				}
			}
		}(int64(g))
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

func TestLocationPrivacyClientView(t *testing.T) {
	var off *locationFuzzer
	if c := off.Client(); c.Enabled || c.RadiusM != 0 {
		t.Fatalf("disabled client view %+v", c)
	}
	on := newTestLocFuzzer(t, 0, testLocSecret)
	if c := on.Client(); !c.Enabled || c.RadiusM != 1000 {
		t.Fatalf("enabled client view %+v", c)
	}
}

func TestConfigClientLocationPrivacy(t *testing.T) {
	srv, router := setupTestServer(t)
	get := func() map[string]json.RawMessage {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/api/config/client", nil))
		if w.Code != 200 {
			t.Fatalf("status %d", w.Code)
		}
		if strings.Contains(w.Body.String(), testLocSecret) {
			t.Fatal("client config leaks the secret")
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	// Off: the key is absent, so the payload is the upstream one.
	if raw, ok := get()["locationPrivacy"]; ok {
		t.Fatalf("disabled: locationPrivacy present: %s", raw)
	}
	srv.cfg.LocationPrivacy = &LocationPrivacyConfig{Enabled: true, RadiusM: 2500, Secret: testLocSecret}
	srv.cfg.setLocationFuzzer(newTestLocFuzzer(t, 2500, testLocSecret))
	if raw := string(get()["locationPrivacy"]); raw != `{"enabled":true,"radiusM":2500}` {
		t.Fatalf("enabled: locationPrivacy %s", raw)
	}
	if clientLocationPrivacy(nil) != nil {
		t.Fatal("clientLocationPrivacy(nil) is not nil")
	}
}

// R13: the secret is read from config.json but no serialisation of the
// config (JSON, fmt) carries it.
func TestLocationPrivacySecretNeverSerialised(t *testing.T) {
	t.Setenv(locationSecretEnv, "")
	dir := t.TempDir()
	raw := `{"port": 3000, "locationPrivacy": {"enabled": true, "radiusM": 750, "secret": "` + testLocSecret + `"}}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LocationPrivacy == nil || cfg.LocationPrivacy.Secret != testLocSecret {
		t.Fatal("secret not read from config.json")
	}
	f, err := newLocationFuzzer(cfg.LocationPrivacy)
	if err != nil || f.RadiusM() != 750 {
		t.Fatalf("fuzzer from loaded config: %v", err)
	}
	cfg.setLocationFuzzer(f)

	outputs := map[string]string{}
	for name, v := range map[string]interface{}{"config": cfg, "block": cfg.LocationPrivacy, "blockValue": *cfg.LocationPrivacy} {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal %s: %v", name, err)
		}
		outputs["json "+name] = string(b)
		ib, _ := json.MarshalIndent(v, "", "  ")
		outputs["indent "+name] = string(ib)
		outputs["fmt %v "+name] = fmt.Sprintf("%v", v)
		outputs["fmt %+v "+name] = fmt.Sprintf("%+v", v)
		outputs["fmt %s "+name] = fmt.Sprintf("%s", v)
	}
	outputs["client"] = func() string { b, _ := json.Marshal(f.Client()); return string(b) }()
	for name, out := range outputs {
		if strings.Contains(out, testLocSecret) || strings.Contains(out, "0123456789abcdef") {
			t.Errorf("%s leaks the secret: %s", name, out)
		}
	}
	// The block still serialises its public fields.
	b, _ := json.Marshal(cfg.LocationPrivacy)
	if string(b) != `{"enabled":true,"radiusM":750}` {
		t.Errorf("block JSON %s", b)
	}
}

func locTestEnv(set bool) func(string) string {
	return func(k string) string {
		if set && k == locationSecretEnv {
			return strings.Repeat("e", 40)
		}
		return ""
	}
}

// checkLocRefusal asserts a refusal (or none) and that its message carries
// no secret and no file contents.
func checkLocRefusal(t *testing.T, err error, want bool) {
	t.Helper()
	if (err != nil) != want {
		t.Fatalf("got %v, want refusal=%v", err, want)
	}
	if err != nil {
		for _, leak := range []string{testLocSecret, "eeee", `"locationPrivacy"`} {
			if strings.Contains(err.Error(), leak) {
				t.Fatalf("message leaks %q: %q", leak, err)
			}
		}
	}
}

// Rule b: a config.json that fails to parse stops the server only when the
// env secret is set or the raw file has a locationPrivacy block with
// "enabled": true.
func TestLocationPrivacyFailClosed(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	enabledTrue := write("true.json", `{"locationPrivacy": {"radiusM": 500, "enabled": true, "secret": "`+testLocSecret+`"},}`)
	enabledTrueCase := write("case.json", `{"LocationPRIVACY":{"Enabled" : TRUE},`)
	enabledTrueSpaced := write("spaced.json", "{\"locationPrivacy\"\n :\n {\n \"enabled\"\t:\n true\n }\n,}")
	enabledFalse := write("false.json", `{"locationPrivacy": {"enabled": false, "radiusM": 1000, "secret": ""},}`)
	noEnabled := write("noenabled.json", `{"locationPrivacy": {"radiusM": 1000},}`)
	without := write("without.json", `{"port": 3000,`)
	missing := filepath.Join(dir, "missing.json")
	parseErr := json.Unmarshal([]byte(`{"port": 3000,`), &Config{})
	cases := []struct {
		name    string
		loadErr error
		path    string
		env     bool
		want    bool
	}{
		{"parse error, enabled true", parseErr, enabledTrue, false, true},
		{"parse error, enabled true, any key case", parseErr, enabledTrueCase, false, true},
		{"parse error, enabled true across lines", parseErr, enabledTrueSpaced, false, true},
		{"parse error, enabled false", parseErr, enabledFalse, false, false},
		{"parse error, block without enabled", parseErr, noEnabled, false, false},
		{"parse error, no block", parseErr, without, false, false},
		{"parse error, file missing", parseErr, missing, false, false},
		{"parse error, env set, enabled false", parseErr, enabledFalse, true, true},
		{"parse error, env set, no block", parseErr, without, true, true},
		{"parse error, env set, file missing", parseErr, missing, true, true},
		{"no parse error, enabled true", nil, enabledTrue, false, false},
		{"no parse error, env set", nil, without, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := locationPrivacyFailClosed(tc.loadErr, tc.path, locTestEnv(tc.env))
			checkLocRefusal(t, err, tc.want)
			if err != nil && !strings.Contains(err.Error(), "refusing to start with exact positions") {
				t.Fatalf("message %q", err)
			}
		})
	}
}

// The Docker entrypoint copies config.example.json, which carries the stock
// disabled block: a typo elsewhere in it must not stop the server.
func TestLocationPrivacyStockExampleWithSyntaxErrorStarts(t *testing.T) {
	stock, err := os.ReadFile(filepath.Join("..", "..", "config.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stock), `"locationPrivacy"`) {
		t.Fatal("config.example.json has no locationPrivacy block")
	}
	// Inject a trailing comma before the final brace.
	i := strings.LastIndex(string(stock), "}")
	broken := string(stock[:i]) + ",}" + string(stock[i+1:])
	dir := t.TempDir()
	p := filepath.Join(dir, "config.json")
	if err := os.WriteFile(p, []byte(broken), 0600); err != nil {
		t.Fatal(err)
	}
	_, failures := loadConfigReport(dir)
	if len(failures) != 1 {
		t.Fatalf("broken stock config not reported: %+v", failures)
	}
	checkLocRefusal(t, locationPrivacyFailClosed(failures[0].Err, failures[0].Path, locTestEnv(false)), false)
	// The stock file as shipped does not decode into Config either (its
	// areas._comment string), so it takes the same path and must start too.
	os.WriteFile(p, stock, 0600)
	if _, failures = loadConfigReport(dir); len(failures) == 1 {
		checkLocRefusal(t, locationPrivacyFailClosed(failures[0].Err, failures[0].Path, locTestEnv(false)), false)
	}
	// Its block says "enabled": false explicitly.
	var only struct {
		LocationPrivacy *LocationPrivacyConfig `json:"locationPrivacy"`
	}
	if err := json.Unmarshal(stock, &only); err != nil || !only.LocationPrivacy.explicitlyDisabled() {
		t.Fatalf("stock block: %+v err=%v", only.LocationPrivacy, err)
	}
}

// Rule a: with the env secret set, a config that loaded must turn the
// feature on or say "enabled": false explicitly.
func TestLocationPrivacyStartupCheck(t *testing.T) {
	load := func(body string) *LocationPrivacyConfig {
		var c Config
		if err := json.Unmarshal([]byte(body), &c); err != nil {
			t.Fatal(err)
		}
		return c.LocationPrivacy
	}
	on := newTestLocFuzzer(t, 1000, testLocSecret)
	cases := []struct {
		name string
		c    *LocationPrivacyConfig
		f    *locationFuzzer
		env  bool
		want bool
	}{
		{"env set, block missing", load(`{"port": 3000}`), nil, true, true},
		{"env set, key misspelt", load(`{"locationPrivcy": {"enabled": true}}`), nil, true, true},
		{"env set, enabled missing", load(`{"locationPrivacy": {"radiusM": 1000}}`), nil, true, true},
		{"env set, enabled null", load(`{"locationPrivacy": {"enabled": null}}`), nil, true, true},
		{"env set, enabled false explicitly", load(`{"locationPrivacy": {"enabled": false}}`), nil, true, false},
		{"env set, Enabled false, key case", load(`{"locationPrivacy": {"ENABLED": false}}`), nil, true, false},
		{"env set, enabled true and valid", load(`{"locationPrivacy": {"enabled": true}}`), on, true, false},
		{"env set, nil block", nil, nil, true, true},
		{"env set, literal struct without enabledSet", &LocationPrivacyConfig{}, nil, true, true},
		{"env unset, block missing", load(`{"port": 3000}`), nil, false, false},
		{"env unset, enabled missing", load(`{"locationPrivacy": {}}`), nil, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			checkLocRefusal(t, locationPrivacyStartupCheck(tc.c, tc.f, locTestEnv(tc.env)), tc.want)
		})
	}
	// "enabled": true with an invalid block never reaches this check:
	// newLocationFuzzer refuses it first, whatever the env.
	t.Setenv(locationSecretEnv, "")
	if _, err := newLocationFuzzer(load(`{"locationPrivacy": {"enabled": true, "secret": "short"}}`)); err == nil {
		t.Fatal("enabled with a short secret was accepted")
	}
}

func TestLocationPrivacyUnmarshalKeepsFields(t *testing.T) {
	var c Config
	if err := json.Unmarshal([]byte(`{"locationPrivacy": {"enabled": true, "radiusM": 750, "secret": "`+testLocSecret+`"}}`), &c); err != nil {
		t.Fatal(err)
	}
	lp := c.LocationPrivacy
	if !lp.Enabled || !lp.enabledSet || lp.RadiusM != 750 || lp.Secret != testLocSecret || lp.explicitlyDisabled() {
		t.Fatalf("decoded %+v set=%v", lp, lp.enabledSet)
	}
	if err := json.Unmarshal([]byte(`{"locationPrivacy": {"enabled": "yes"}}`), &c); err == nil {
		t.Fatal("a non-bool enabled was accepted")
	}
}

// One owner for the fuzzer: NewServer copies whichever side is set to the
// other, never replaces a fuzzer with nil, and panics on two different ones.
func TestNewServerReconcilesLocationFuzzer(t *testing.T) {
	f := newTestLocFuzzer(t, 1000, testLocSecret)
	g := newTestLocFuzzer(t, 1000, testLocSecret)

	t.Run("config only", func(t *testing.T) {
		db := setupTestDB(t)
		cfg := &Config{Port: 3000}
		cfg.setLocationFuzzer(f)
		NewServer(db, cfg, NewHub())
		if db.locationFuzzer() != f || cfg.LocationFuzzer() != f {
			t.Fatal("config fuzzer not copied to the DB")
		}
	})
	t.Run("db only", func(t *testing.T) {
		db := setupTestDB(t)
		db.setLocationFuzzer(f)
		cfg := &Config{Port: 3000}
		NewServer(db, cfg, NewHub())
		if db.locationFuzzer() != f || cfg.LocationFuzzer() != f {
			t.Fatal("a nil config fuzzer turned DB fuzzing off, or was not filled in")
		}
	})
	t.Run("same on both", func(t *testing.T) {
		db := setupTestDB(t)
		db.setLocationFuzzer(f)
		cfg := &Config{Port: 3000}
		cfg.setLocationFuzzer(f)
		NewServer(db, cfg, NewHub())
		if db.locationFuzzer() != f || cfg.LocationFuzzer() != f {
			t.Fatal("same fuzzer changed")
		}
	})
	t.Run("neither", func(t *testing.T) {
		db := setupTestDB(t)
		cfg := &Config{Port: 3000}
		NewServer(db, cfg, NewHub())
		if db.locationFuzzer() != nil || cfg.LocationFuzzer() != nil {
			t.Fatal("a fuzzer appeared")
		}
	})
	t.Run("different on each side", func(t *testing.T) {
		db := setupTestDB(t)
		db.setLocationFuzzer(f)
		cfg := &Config{Port: 3000}
		cfg.setLocationFuzzer(g)
		defer func() {
			if r := recover(); r == nil || !strings.Contains(fmt.Sprint(r), "different location fuzzers") {
				t.Fatalf("want a panic about different fuzzers, got %v", r)
			}
		}()
		NewServer(db, cfg, NewHub())
	})
}

// loadConfigReport reports the broken files LoadConfig skips, including one
// skipped in favour of data/config.json, so main can fail closed on them.
func TestLocationPrivacyLoadConfigReportsParseFailures(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0700); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(dir, "config.json")
	os.WriteFile(broken, []byte(`{"locationPrivacy": {"enabled": true,}`), 0600)
	os.WriteFile(filepath.Join(dir, "data", "config.json"), []byte(`{"port": 4321}`), 0600)
	cfg, failures := loadConfigReport(dir)
	if cfg.Port != 4321 || cfg.LocationPrivacy != nil {
		t.Fatalf("fallback config not used: port %d", cfg.Port)
	}
	if len(failures) != 1 || failures[0].Path != broken || failures[0].Err == nil {
		t.Fatalf("failures %+v", failures)
	}
	if err := locationPrivacyFailClosed(failures[0].Err, failures[0].Path, func(string) string { return "" }); err == nil {
		t.Fatal("broken config with a locationPrivacy block did not refuse")
	}
	// LoadConfig keeps its old contract: no error, same config.
	if c, err := LoadConfig(dir); err != nil || c.Port != 4321 {
		t.Fatalf("LoadConfig: %v port %d", err, c.Port)
	}
	if got := configCandidatePaths(dir); len(got) != 2 || got[0] != broken {
		t.Fatalf("candidate paths %v", got)
	}
	// A clean directory reports nothing.
	if _, f := loadConfigReport(t.TempDir()); len(f) != 0 {
		t.Fatalf("failures for no config: %+v", f)
	}
}
