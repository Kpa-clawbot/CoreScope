package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"unicode/utf16"
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

// locKey is a well-formed node public key (64 hex digits), the same for the
// same parts.
func locKey(parts ...any) string {
	sum := sha256.Sum256([]byte(fmt.Sprint(parts...)))
	return hex.EncodeToString(sum[:])
}

// locSlot is the hysteresis slot of a well-formed pubkey.
func locSlot(t testing.TB, purpose locPurpose, pubkey string) locAnchorKey {
	t.Helper()
	pk, ok := parseLocPubkey(pubkey)
	if !ok {
		t.Fatalf("test pubkey %q is not 64 hex digits", pubkey)
	}
	return locAnchorKey{purpose: purpose, pk: pk}
}

// locTestGrid is the slot and grid of a well-formed pubkey.
func locTestGrid(t testing.TB, f *locationFuzzer, purpose locPurpose, pubkey string) (*locAnchorKey, locGrid) {
	t.Helper()
	k := locSlot(t, purpose, pubkey)
	return &k, f.gridFor(&k)
}

// setAnchorCap replaces the hysteresis cache with an empty one of the given
// capacity.
func setAnchorCap(f *locationFuzzer, n int) {
	f.mu.Lock()
	f.anchors = newLRU[locAnchorKey, locAnchor](n, 0, nil)
	f.mu.Unlock()
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
	la, oa, _ := a.Fuzz(locKey("ab"), 50.85, 4.35)
	lb, ob, _ := b.Fuzz(locKey("ab"), 50.85, 4.35)
	if la == lb && oa == ob {
		t.Fatal("env secret did not replace the config secret")
	}
}

func TestLocationFuzzNilIsIdentity(t *testing.T) {
	var f *locationFuzzer
	if lat, lon, _ := f.Fuzz(locKey("ab"), 12.5, -3.25); lat != 12.5 || lon != -3.25 {
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
	if lat, lon, _ := f.Fuzz(locKey("ab"), 0, 0); lat != 0 || lon != 0 {
		t.Fatalf("no-fix (0,0) became (%v,%v)", lat, lon)
	}
	// A non-finite coordinate is no position: neither half is served.
	var db DB
	db.setLocationFuzzer(f)
	for _, p := range [][2]float64{
		{math.NaN(), 4}, {50.85, math.NaN()}, {math.Inf(1), 4}, {50.85, math.Inf(-1)}, {math.NaN(), math.Inf(1)},
	} {
		for name, fuzz := range map[string]func(string, float64, float64) (float64, float64, bool){"Fuzz": f.Fuzz, "FuzzEstimate": f.FuzzEstimate} {
			if lat, lon, ok := fuzz(locKey("ab"), p[0], p[1]); ok || lat != 0 || lon != 0 {
				t.Errorf("%s(%v,%v) = %v,%v,%v, want refused", name, p[0], p[1], lat, lon, ok)
			}
		}
		la, lo := db.servedNodePos(locKey("ab"), sql.NullFloat64{Float64: p[0], Valid: true}, sql.NullFloat64{Float64: p[1], Valid: true})
		if la.Valid || lo.Valid {
			t.Errorf("servedNodePos(%v,%v) = %v,%v, want both NULL", p[0], p[1], la, lo)
		}
	}
	if f.anchorCount() != 0 {
		t.Fatal("a call that serves nothing stored an anchor")
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
				sLat, sLon, _ := f.Fuzz(pk, lat, lon)
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
				pk := locKey("edge", i, k)
				sLat, sLon, _ := f.Fuzz(pk, p[0], p[1])
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
			pk := locKey("node", node)
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
				sLat, sLon, _ := f.Fuzz(pk, lat, lon)
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
		pk := locKey("pk", i)
		la, oa, _ := a.Fuzz(pk, lat, lon)
		lb, ob, _ := b.Fuzz(pk, lat, lon)
		if la != lb || oa != ob {
			t.Fatalf("instances disagree for %s at (%v,%v): (%v,%v) vs (%v,%v)", pk, lat, lon, la, oa, lb, ob)
		}
		if lc, oc, _ := other.Fuzz(pk, lat, lon); lc != la || oc != oa {
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
	pk := locKey("case")
	la, oa, _ := a.Fuzz(strings.ToUpper(pk), 50.85, 4.35)
	lb, ob, _ := b.Fuzz(pk, 50.85, 4.35)
	if la != lb || oa != ob {
		t.Fatal("pubkey case changed the output")
	}
	// One anchor for both spellings.
	a.Fuzz(pk, 50.85, 4.35)
	if a.anchorCount() != 1 {
		t.Fatalf("anchors %d, want 1", a.anchorCount())
	}
}

// randLocKey is a random well-formed node public key.
func randLocKey(rng *rand.Rand) string {
	var b [locPubkeyLen]byte
	rng.Read(b[:])
	return hex.EncodeToString(b[:])
}

// mustFuzz is Fuzz for a key the fuzzer must accept.
func mustFuzz(t testing.TB, f *locationFuzzer, pk string, lat, lon float64) (float64, float64) {
	t.Helper()
	sLat, sLon, ok := f.Fuzz(pk, lat, lon)
	if !ok {
		t.Fatalf("Fuzz refused the well-formed key %q", pk)
	}
	return sLat, sLon
}

// hasAnchor reports whether the slot is cached, without touching the
// eviction order.
func hasAnchor(t testing.TB, f *locationFuzzer, purpose locPurpose, pubkey string) bool {
	t.Helper()
	k := locSlot(t, purpose, pubkey)
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.anchors.Peek(k)
	return ok
}

// Grid and offsets are per node: nodes at one exact spot get unrelated
// points, spread like one node's point is (cell centre uniform over ±s/2,
// plus the disk of radius ≈ 0.568·R: about 306 m per axis at R = 1000).
func TestLocationFuzzColocatedNodesGetDistinctPoints(t *testing.T) {
	f := newTestLocFuzzer(t, 1000, testLocSecret)
	const lat, lon, n = 50.85, 4.35, 1000
	seen := map[[2]float64]int{}
	var north, east []float64
	for i := 0; i < n; i++ {
		sLat, sLon := mustFuzz(t, f, locKey("colocated", i), lat, lon)
		checkServed(t, f, lat, lon, sLat, sLon)
		if j, dup := seen[[2]float64{sLat, sLon}]; dup {
			t.Fatalf("nodes %d and %d at one spot share the served point (%v,%v)", j, i, sLat, sLon)
		}
		seen[[2]float64{sLat, sLon}] = i
		const mPerDeg = earthRadiusKm * 1000 * math.Pi / 180
		north = append(north, (sLat-lat)*mPerDeg)
		east = append(east, (sLon-lon)*mPerDeg*math.Cos(lat*math.Pi/180))
	}
	for name, v := range map[string][]float64{"north": north, "east": east} {
		var sum, sq float64
		for _, x := range v {
			sum += x
		}
		mean := sum / n
		for _, x := range v {
			sq += (x - mean) * (x - mean)
		}
		if sd := math.Sqrt(sq / (n - 1)); sd < 250 || sd > 360 {
			t.Errorf("%s: std dev %.1f m of co-located served points, want 250..360", name, sd)
		}
	}
}

// The probe attack: nodes with fresh keys placed around a target's served
// point. Under a shared grid the probe in the target's cell was served the
// target's point; with per-node keying no probe is, and none lands closer to
// it than chance allows.
func TestLocationFuzzProbeNeverSharesTargetPoint(t *testing.T) {
	f := newTestLocFuzzer(t, 1000, testLocSecret)
	tLat, tLon := mustFuzz(t, f, locKey("target"), 50.85, 4.35)
	rng := rand.New(rand.NewSource(20261126))
	const probes = 10000
	near := 0
	for i := 0; i < probes; i++ {
		pLat, pLon := destinationPoint(tLat, tLon, rng.Float64()*2*math.Pi, f.RadiusM()*math.Sqrt(rng.Float64()))
		sLat, sLon := mustFuzz(t, f, randLocKey(rng), pLat, pLon)
		if sLat == tLat && sLon == tLon {
			t.Fatalf("probe %d at (%v,%v) is served the target's point", i, pLat, pLon)
		}
		if distM(sLat, sLon, tLat, tLon) < 1 {
			near++
		}
	}
	// A probe's point is spread over several km²: within 1 m of a given
	// point about once in a million probes.
	if near > 1 {
		t.Fatalf("%d of %d probes are served within 1 m of the target's point", near, probes)
	}
}

// chiSquareUniform is the chi-square statistic of hist against the uniform.
func chiSquareUniform(hist []int) float64 {
	n := 0
	for _, c := range hist {
		n += c
	}
	want := float64(n) / float64(len(hist))
	var chi2 float64
	for _, c := range hist {
		chi2 += (float64(c) - want) * (float64(c) - want) / want
	}
	return chi2
}

// chi2Crit9 is the 0.1% point of chi-square with 9 degrees of freedom (10
// bins). A shared grid puts every key in one bin: χ² = 9 × the sample size.
const chi2Crit9 = 27.88

// One key's grid says nothing about another's: the place where a node's
// served point first changes on a 1 m-step walk (its cell edge plus the
// hysteresis margin), taken modulo the cell size, is uniform over keys, for
// rows (northward walk) and for columns (eastward walk).
func TestLocationFuzzGridEdgesUniformAcrossKeys(t *testing.T) {
	f := newTestLocFuzzer(t, 1000, testLocSecret)
	const lat0, lon0, keys, bins = 40.0, 4.35, 2000, 10
	side := int(f.cellM)
	// walk returns the number of 1 m steps until the served point changes.
	walk := func(pk string, at func(step int) (float64, float64)) int {
		lat, lon := at(0)
		l0, o0 := mustFuzz(t, f, pk, lat, lon)
		for step := 1; step <= 2*side; step++ {
			lat, lon = at(step)
			if l, o := mustFuzz(t, f, pk, lat, lon); l != l0 || o != o0 {
				return step
			}
		}
		t.Fatalf("key %s: no cell edge within %d m", pk[:8], 2*side)
		return 0
	}
	north := make([]int, bins)
	east := make([]int, bins)
	mPerDegLon := locMetresPerDeg * math.Cos(lat0*math.Pi/180)
	for i := 0; i < keys; i++ {
		pk := locKey("sweep-north", i)
		step := walk(pk, func(step int) (float64, float64) { return lat0 + float64(step)/locMetresPerDeg, lon0 })
		north[(step%side)*bins/side]++

		// Columns: the width depends on the row, so read it off the anchor.
		pk = locKey("sweep-east", i)
		mustFuzz(t, f, pk, lat0, lon0)
		a, ok := f.anchorFor(locSlot(t, locNodeSlot, pk))
		if !ok {
			t.Fatal("no anchor after a served call")
		}
		widthM := a.halfLonX / (0.5 + locMarginFrac) * mPerDegLon
		step = walk(pk, func(step int) (float64, float64) { return lat0, lon0 + float64(step)/mPerDegLon })
		_, frac := math.Modf(float64(step) / widthM)
		east[int(frac*bins)]++
	}
	if chi2 := chiSquareUniform(north); chi2 > chi2Crit9 {
		t.Errorf("row edges modulo the cell side are not uniform: χ² = %.1f, bins %v", chi2, north)
	}
	if chi2 := chiSquareUniform(east); chi2 > chi2Crit9 {
		t.Errorf("column edges modulo the column width are not uniform: χ² = %.1f, bins %v", chi2, east)
	}
}

// An estimate is computed from unsigned reports, so whoever controls it must
// learn nothing about the node's own grid: for one key at one position the
// two served points differ, and over 2,000 keys the estimate grid's origin
// is uniformly spread relative to the node grid's (rows and columns).
func TestLocationFuzzEstimateGridIndependentOfNodeGrid(t *testing.T) {
	f := newTestLocFuzzer(t, 1000, testLocSecret)
	const keys, bins = 2000, 10
	dRow := make([]int, bins)
	dCol := make([]int, bins)
	for i := 0; i < keys; i++ {
		pk := locKey("purpose", i)
		nLat, nLon := mustFuzz(t, f, pk, 50.85, 4.35)
		eLat, eLon, ok := f.FuzzEstimate(pk, 50.85, 4.35)
		if !ok {
			t.Fatal("well-formed key refused")
		}
		if nLat == eLat && nLon == eLon {
			t.Fatalf("key %d: the estimate is served the node's point (%v,%v)", i, nLat, nLon)
		}
		checkServed(t, f, 50.85, 4.35, eLat, eLon)
		_, ng := locTestGrid(t, f, locNodeSlot, pk)
		_, eg := locTestGrid(t, f, locEstimateSlot, pk)
		if ng.fLat == eg.fLat || ng.fLon == eg.fLon {
			t.Fatalf("key %d: node and estimate grids share an origin fraction", i)
		}
		dRow[int(math.Mod(eg.fLat-ng.fLat+1, 1)*bins)]++
		dCol[int(math.Mod(eg.fLon-ng.fLon+1, 1)*bins)]++
	}
	if chi2 := chiSquareUniform(dRow); chi2 > chi2Crit9 {
		t.Errorf("estimate row origins follow the node grid: χ² = %.1f, bins %v", chi2, dRow)
	}
	if chi2 := chiSquareUniform(dCol); chi2 > chi2Crit9 {
		t.Errorf("estimate column origins follow the node grid: χ² = %.1f, bins %v", chi2, dCol)
	}
}

// Known answers: the labels, the field order and widths of the HMAC messages
// and the geometry are a served-data format. A change here moves every
// served position; it must be deliberate.
func TestLocationFuzzKnownAnswer(t *testing.T) {
	f := newTestLocFuzzer(t, 1000, testLocSecret)
	const pk = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	nLat, nLon := mustFuzz(t, f, pk, 50.85, 4.35)
	eLat, eLon, ok := f.FuzzEstimate(pk, 50.85, 4.35)
	if !ok {
		t.Fatal("well-formed key refused")
	}
	const wantNLat, wantNLon, wantELat, wantELon = 50.848184734340, 4.349924936189, 50.850149236687, 4.356043293426
	if math.Abs(nLat-wantNLat) > 1e-9 || math.Abs(nLon-wantNLon) > 1e-9 {
		t.Errorf("Fuzz = %.12f, %.12f, want %.12f, %.12f", nLat, nLon, wantNLat, wantNLon)
	}
	if math.Abs(eLat-wantELat) > 1e-9 || math.Abs(eLon-wantELon) > 1e-9 {
		t.Errorf("FuzzEstimate = %.12f, %.12f, want %.12f, %.12f", eLat, eLon, wantELat, wantELon)
	}
}

// R2 with a fresh random key on every call, node and estimate slots alike:
// the bound holds in every per-node grid, poles and antimeridian included.
func TestLocationFuzzBoundRandomKeys(t *testing.T) {
	edge := [][2]float64{
		{90, 0}, {-90, 0}, {89.9, 179.999}, {-89.9, -179.999}, {85, 10}, {-85.0001, -10},
		{0, 179.999}, {0, -179.999}, {45, 180}, {-33.9, -180}, {1e-7, -1e-7},
	}
	for _, radius := range []float64{100, 1000, 50000} {
		f := newTestLocFuzzer(t, radius, testLocSecret)
		rng := rand.New(rand.NewSource(int64(radius)))
		for i := 0; i < 30000; i++ {
			lat, lon := randomGlobePoint(rng)
			if i < 50*len(edge) {
				lat, lon = edge[i%len(edge)][0], edge[i%len(edge)][1]
			}
			fuzz := f.Fuzz
			if i%2 == 1 {
				fuzz = f.FuzzEstimate
			}
			sLat, sLon, ok := fuzz(randLocKey(rng), lat, lon)
			if !ok {
				t.Fatal("well-formed key refused")
			}
			checkServed(t, f, lat, wrapLon(lon), sLat, sLon)
		}
	}
}

// Spellings of one key are one node; anything that is not 64 hex digits is
// refused, never served from a shared grid.
func TestLocationFuzzPubkeyForms(t *testing.T) {
	f := newTestLocFuzzer(t, 1000, testLocSecret)
	pk := locKey("forms")
	mixed := strings.ToUpper(pk[:32]) + pk[32:]
	wLat, wLon := mustFuzz(t, f, pk, 50.85, 4.35)
	weLat, weLon, ok := f.FuzzEstimate(pk, 50.85, 4.35)
	if !ok {
		t.Fatal("well-formed key refused")
	}
	for _, v := range []string{strings.ToUpper(pk), mixed, "  " + pk + "\n", "\t" + mixed + " "} {
		if la, lo, ok := f.Fuzz(v, 50.85, 4.35); !ok || la != wLat || lo != wLon {
			t.Errorf("Fuzz(%q) = %v,%v,%v, want %v,%v", v, la, lo, ok, wLat, wLon)
		}
		if la, lo, ok := f.FuzzEstimate(v, 50.85, 4.35); !ok || la != weLat || lo != weLon {
			t.Errorf("FuzzEstimate(%q) = %v,%v,%v, want %v,%v", v, la, lo, ok, weLat, weLon)
		}
	}
	if n := f.anchorCount(); n != 2 {
		t.Fatalf("%d anchors for one key, want 2 (node and estimate)", n)
	}

	var db DB
	db.setLocationFuzzer(f)
	valid := func(v float64) sql.NullFloat64 { return sql.NullFloat64{Float64: v, Valid: true} }
	if la, lo := db.servedNodePos(" "+mixed, valid(50.85), valid(4.35)); !la.Valid || !lo.Valid || la.Float64 != wLat || lo.Float64 != wLon {
		t.Fatalf("servedNodePos of a well-formed key: %v %v", la, lo)
	}
	bad := []string{"", "abc", pk[:63], pk + "0", "zz" + pk[2:], pk[:62] + "g0", pk[:31] + " " + pk[32:],
		"0x" + pk[2:], pk[:32], pk + pk, strings.Repeat("é", 32)}
	for _, v := range bad {
		if la, lo, ok := f.Fuzz(v, 50.85, 4.35); ok || la != 0 || lo != 0 {
			t.Errorf("Fuzz(%q) = %v,%v,%v, want refused", v, la, lo, ok)
		}
		if la, lo, ok := f.FuzzEstimate(v, 50.85, 4.35); ok || la != 0 || lo != 0 {
			t.Errorf("FuzzEstimate(%q) = %v,%v,%v, want refused", v, la, lo, ok)
		}
		if la, lo := db.servedNodePos(v, valid(50.85), valid(4.35)); la.Valid || lo.Valid {
			t.Errorf("servedNodePos(%q) = %v,%v, want both NULL", v, la, lo)
		}
		est := PositionEstimate{Lat: 50.85, Lon: 4.35, RadiusM: 300}
		if fuzzEstimate(f, v, &est) || est != (PositionEstimate{Lat: 50.85, Lon: 4.35, RadiusM: 300}) {
			t.Errorf("fuzzEstimate(%q) kept the estimate (%+v)", v, est)
		}
		// No fix stays no fix, and the feature off stays the identity.
		if la, lo, ok := f.Fuzz(v, 0, 0); !ok || la != 0 || lo != 0 {
			t.Errorf("Fuzz(%q, 0, 0) = %v,%v,%v", v, la, lo, ok)
		}
		var off *locationFuzzer
		if la, lo, ok := off.Fuzz(v, 50.85, 4.35); !ok || la != 50.85 || lo != 4.35 {
			t.Errorf("nil fuzzer, %q: %v,%v,%v", v, la, lo, ok)
		}
	}
	if n := f.anchorCount(); n != 2 {
		t.Fatalf("refused keys stored anchors: %d, want 2", n)
	}
}

// The served point of a slot depends on the secret, the slot and that slot's
// own sequence of positions (hysteresis) only, never on what else the fuzzer
// served in between: two instances fed the same per-slot sequences,
// interleaved differently across slots, agree on every output.
func TestLocationFuzzDeterministicAcrossCallOrders(t *testing.T) {
	a := newTestLocFuzzer(t, 1000, testLocSecret)
	b := newTestLocFuzzer(t, 1000, testLocSecret)
	rng := rand.New(rand.NewSource(17))
	type slot struct {
		pk  string
		est bool
		pts [][2]float64 // visited in this order
	}
	const visits = 6
	slots := make([]slot, 600)
	for i := range slots {
		// Every third key has an estimate slot too, moving independently.
		s := slot{pk: randLocKey(rng), est: i%3 == 1}
		if i%3 == 2 {
			s.pk = slots[i-1].pk
		}
		lat, lon := randomGlobePoint(rng)
		for v := 0; v < visits; v++ {
			// Small steps stay in the anchor cell's margin, large ones leave it.
			stepM := 60.0
			if rng.Intn(3) == 0 {
				stepM = 3000
			}
			lat, lon = destinationPoint(lat, lon, rng.Float64()*2*math.Pi, stepM*rng.Float64())
			s.pts = append(s.pts, [2]float64{lat, lon})
		}
		slots[i] = s
	}
	serve := func(f *locationFuzzer, s slot, v int) [2]float64 {
		fuzz := f.Fuzz
		if s.est {
			fuzz = f.FuzzEstimate
		}
		la, lo, ok := fuzz(s.pk, s.pts[v][0], s.pts[v][1])
		if !ok {
			t.Fatal("well-formed key refused")
		}
		return [2]float64{la, lo}
	}
	// a: slot after slot. b: slots interleaved at random, each slot's own
	// visits still in order.
	want := make([][visits][2]float64, len(slots))
	for i, s := range slots {
		for v := 0; v < visits; v++ {
			want[i][v] = serve(a, s, v)
		}
	}
	var order []int
	for i := range slots {
		for v := 0; v < visits; v++ {
			order = append(order, i)
		}
	}
	rng.Shuffle(len(order), func(x, y int) { order[x], order[y] = order[y], order[x] })
	next := make([]int, len(slots))
	moved := 0
	for _, i := range order {
		v := next[i]
		next[i]++
		got := serve(b, slots[i], v)
		if got != want[i][v] {
			t.Fatalf("slot %d visit %d: %v interleaved, %v in sequence", i, v, got, want[i][v])
		}
		if v > 0 && got != want[i][v-1] {
			moved++
		}
	}
	if moved == 0 || moved == len(order)-len(slots) {
		t.Fatalf("fixture: %d of %d later visits re-anchored; want some but not all", moved, len(order)-len(slots))
	}
}

// A node's stored position and the estimate of it keep separate hysteresis:
// each sits astride a row edge of its own grid, inside the margin, while the
// other is served from 3 km away. Sharing one slot would re-anchor both on
// every call.
func TestLocationFuzzNodeAndEstimateDoNotThrash(t *testing.T) {
	f := newTestLocFuzzer(t, 1000, testLocSecret)
	pk := locKey("both")
	edgeAbove := func(purpose locPurpose, lat float64) float64 {
		_, grid := locTestGrid(t, f, purpose, pk)
		return (math.Floor(lat/f.dLat+grid.fLat) + 1 - grid.fLat) * f.dLat
	}
	nEdge, eEdge := edgeAbove(locNodeSlot, 40), edgeAbove(locEstimateSlot, 40+3000/locMetresPerDeg)
	const lon = 4.35
	n0Lat, n0Lon := mustFuzz(t, f, pk, nEdge-0.01*f.dLat, lon)
	e0Lat, e0Lon, ok := f.FuzzEstimate(pk, eEdge-0.01*f.dLat, lon)
	if !ok || (n0Lat == e0Lat && n0Lon == e0Lon) {
		t.Fatalf("fixture: node and estimate share a point (%v)", ok)
	}
	for i := 0; i < 100; i++ {
		// Odd rounds step north across the edge, inside the margin.
		d := -0.01
		if i%2 == 1 {
			d = 0.2
		}
		if la, lo := mustFuzz(t, f, pk, nEdge+d*f.dLat, lon); la != n0Lat || lo != n0Lon {
			t.Fatalf("round %d: the node's point moved to (%v,%v)", i, la, lo)
		}
		if la, lo, _ := f.FuzzEstimate(pk, eEdge+d*f.dLat, lon); la != e0Lat || lo != e0Lon {
			t.Fatalf("round %d: the estimate's point moved to (%v,%v)", i, la, lo)
		}
	}
	if n := f.anchorCount(); n != 2 {
		t.Fatalf("%d anchors, want one per slot", n)
	}
	// Without the stored anchors the stepped positions do re-anchor.
	g := newTestLocFuzzer(t, 1000, testLocSecret)
	if la, lo := mustFuzz(t, g, pk, nEdge+0.2*f.dLat, lon); la == n0Lat && lo == n0Lon {
		t.Fatal("fixture: the node's step does not cross a cell edge")
	}
	if la, lo, _ := g.FuzzEstimate(pk, eEdge+0.2*f.dLat, lon); la == e0Lat && lo == e0Lon {
		t.Fatal("fixture: the estimate's step does not cross a cell edge")
	}
}

// The price of per-node points, pinned from both sides: averaging co-located
// nodes narrows their position by about √N, no more and no less. One node is
// a median ≈ 400 m off at R = 1000; the mean of four ≈ 180 m (per-axis
// σ ≈ 306 m / √4, median ≈ 1.18 σ). A grid shared between nodes would leave
// the mean of four where one node is.
func TestLocationFuzzAveragingColocatedNodes(t *testing.T) {
	f := newTestLocFuzzer(t, 1000, testLocSecret)
	for _, tc := range []struct {
		group  int
		lo, hi float64
	}{{1, 370, 440}, {4, 150, 220}} {
		rng := rand.New(rand.NewSource(4))
		const trials = 2000
		errs := make([]float64, trials)
		for i := range errs {
			lat, lon := rng.Float64()*120-60, rng.Float64()*340-170
			var sLat, sLon float64
			for k := 0; k < tc.group; k++ {
				la, lo := mustFuzz(t, f, randLocKey(rng), lat, lon)
				sLat, sLon = sLat+la/float64(tc.group), sLon+lo/float64(tc.group)
			}
			errs[i] = distM(lat, lon, sLat, sLon)
		}
		sort.Float64s(errs)
		if med := errs[trials/2]; med < tc.lo || med > tc.hi {
			t.Errorf("median error of the mean of %d co-located node(s) is %.1f m, want %v..%v", tc.group, med, tc.lo, tc.hi)
		}
	}
}

// A full cache evicts the least recently used slot, and a served node counts
// as used.
func TestLocationFuzzAnchorLRUEviction(t *testing.T) {
	f := newTestLocFuzzer(t, 1000, testLocSecret)
	setAnchorCap(f, 3)
	a, b, c, d := locKey("lru", "a"), locKey("lru", "b"), locKey("lru", "c"), locKey("lru", "d")
	for _, pk := range []string{a, b, c} {
		mustFuzz(t, f, pk, 50.85, 4.35)
	}
	mustFuzz(t, f, a, 50.85, 4.35) // a warm hit: a is now the most recent
	mustFuzz(t, f, d, 50.85, 4.35) // evicts b, the least recently used
	for pk, want := range map[string]bool{a: true, b: false, c: true, d: true} {
		if got := hasAnchor(t, f, locNodeSlot, pk); got != want {
			t.Errorf("after the fourth node: anchor of %s… cached = %v, want %v", pk[:8], got, want)
		}
	}
	// An estimate slot takes part in the same order: it evicts c.
	if _, _, ok := f.FuzzEstimate(a, 50.85, 4.35); !ok {
		t.Fatal("well-formed key refused")
	}
	if !hasAnchor(t, f, locEstimateSlot, a) || !hasAnchor(t, f, locNodeSlot, a) || !hasAnchor(t, f, locNodeSlot, d) || hasAnchor(t, f, locNodeSlot, c) {
		t.Error("the estimate slot did not evict the least recently used node slot")
	}
	if n := f.anchorCount(); n != 3 {
		t.Fatalf("%d anchors, want 3", n)
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
		pk := locKey("h", node)
		lat := rng.Float64()*120 - 60
		lon := rng.Float64()*360 - 180
		l0, o0, _ := f.Fuzz(pk, lat, lon)
		// Jitter: any move under 0.25·s per axis (minus a little for the
		// cos change across the row) stays inside the expanded anchor cell.
		for k := 0; k < 20; k++ {
			jl, jo := metresOffset(lat, lon, (rng.Float64()*2-1)*0.24*s, (rng.Float64()*2-1)*0.23*s)
			if l, o, _ := f.Fuzz(pk, jl, jo); l != l0 || o != o0 {
				t.Fatalf("%s: jitter inside the margin changed the output", pk)
			}
		}
		// A move of 5·R re-anchors: a different cell, a different point.
		fl, fo := metresOffset(lat, lon, 5*f.RadiusM(), 0)
		l1, o1, _ := f.Fuzz(pk, fl, fo)
		if l1 == l0 && o1 == o0 {
			t.Fatalf("%s: moving 5 km did not re-anchor", pk)
		}
		checkServed(t, f, fl, fo, l1, o1)
		// Back home re-anchors to the home cell and yields its point again,
		// unless home sat in the margin of the old cell, which it does not
		// after a 5·R excursion.
		if l, o, _ := f.Fuzz(pk, lat, lon); l != l0 || o != o0 {
			t.Fatalf("%s: returning home did not restore the home point", pk)
		}
	}
}

func TestLocationFuzzHysteresisKeepsAnchorAcrossCellEdge(t *testing.T) {
	f := newTestLocFuzzer(t, 1000, testLocSecret)
	// Find a row boundary and sit just south of it, then step just north
	// (inside the margin): same output. Then step past the margin: new cell.
	edgeKey := locKey("edge")
	_, grid := locTestGrid(t, f, locNodeSlot, edgeKey)
	row := math.Floor(40/f.dLat + grid.fLat)
	edge := (row + 1 - grid.fLat) * f.dLat // northern edge of the row
	lon := 4.35
	south := edge - 0.01*f.dLat
	l0, o0, _ := f.Fuzz(edgeKey, south, lon)
	if l, o, _ := f.Fuzz(edgeKey, edge+0.2*f.dLat, lon); l != l0 || o != o0 {
		t.Fatal("crossing into the margin re-anchored")
	}
	l2, o2, _ := f.Fuzz(edgeKey, edge+0.3*f.dLat, lon)
	if l2 == l0 && o2 == o0 {
		t.Fatal("leaving the margin did not re-anchor")
	}
	// A fresh instance at the same northern point picks the northern cell
	// directly, so the re-anchored point is the stateless answer.
	g := newTestLocFuzzer(t, 1000, testLocSecret)
	if l, o, _ := g.Fuzz(edgeKey, edge+0.3*f.dLat, lon); l != l2 || o != o2 {
		t.Fatal("re-anchored point differs from the stateless one")
	}
}

func TestLocationFuzzAntimeridian(t *testing.T) {
	f := newTestLocFuzzer(t, 1000, testLocSecret)
	for _, lat := range []float64{-45, 0.5, 30, 60} {
		pk := locKey("am", lat)
		l0, o0, _ := f.Fuzz(pk, lat, 179.9995)
		checkServed(t, f, lat, 179.9995, l0, o0)
		// ~100 m east, across the antimeridian: inside the margin of
		// whatever cell holds 179.9995 only if close enough; check the
		// bound always, and identity when the step is under 0.2·s.
		step := 0.001 * locMetresPerDeg * math.Cos(lat*math.Pi/180)
		l1, o1, _ := f.Fuzz(pk, lat, -179.9995)
		checkServed(t, f, lat, -179.9995, l1, o1)
		if step < 0.2*f.cellM && (l1 != l0 || o1 != o0) {
			t.Fatalf("lat %v: a %.0f m step across the antimeridian re-anchored", lat, step)
		}
	}
	// A cell spans the antimeridian seamlessly: the column containing
	// -180 and the one containing 179.999... are adjacent, not a gap.
	amPK, amGrid := locTestGrid(t, f, locNodeSlot, locKey("seam"))
	for _, lat := range []float64{0, 40, 84} {
		a := f.cellAt(amPK, amGrid, lat, 179.99999)
		b := f.cellAt(amPK, amGrid, lat, -179.99999)
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
			sLat, sLon, _ := f.Fuzz(locKey("p", k), lat, lon)
			checkServed(t, f, lat, lon, sLat, sLon)
		}
	}
	// The cell is chosen at ±85: two polar latitudes on one meridian share
	// the anchor (and the offset from the exact point).
	a := newTestLocFuzzer(t, 1000, testLocSecret)
	polar := locKey("polar")
	l1, o1, _ := a.Fuzz(polar, 88, 10)
	c1, _ := a.anchorFor(locSlot(t, locNodeSlot, polar))
	l2, o2, _ := a.Fuzz(polar, 89.5, 10)
	c2, _ := a.anchorFor(locSlot(t, locNodeSlot, polar))
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
		sLat, sLon, _ := f.Fuzz(locKey("n", i), lat, lon)
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
	pk, grid := locTestGrid(t, f, locNodeSlot, locKey("geometry"))
	if grid.fLat < 0 || grid.fLat >= 1 || grid.fLon < 0 || grid.fLon >= 1 || (grid.fLat == 0 && grid.fLon == 0) {
		t.Fatalf("grid offsets %v %v", grid.fLat, grid.fLon)
	}
	if again := f.gridFor(pk); again != grid {
		t.Fatalf("grid of one key changed between calls: %+v vs %+v", again, grid)
	}
	g := newTestLocFuzzer(t, 1000, testLocSecret+"other")
	if o := g.gridFor(pk); o.fLat == grid.fLat || o.fLon == grid.fLon {
		t.Fatal("grid origin does not depend on the secret")
	}
	if _, o := locTestGrid(t, f, locNodeSlot, locKey("geometry2")); o.fLat == grid.fLat || o.fLon == grid.fLon {
		t.Fatal("grid origin does not depend on the pubkey")
	}
	// A cell is about s wide in metres at its centre latitude, never wider.
	for _, lat := range []float64{0, 30, 60, 84.9} {
		a := f.cellAt(pk, grid, lat, 10)
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
	if f.anchors.maxItems != 200000 {
		t.Fatalf("anchor cap %d, want 200000", f.anchors.maxItems)
	}
	setAnchorCap(f, 100)
	rng := rand.New(rand.NewSource(9))
	for i := 0; i < 5000; i++ {
		lat, lon := randomGlobePoint(rng)
		sLat, sLon, _ := f.Fuzz(locKey("cap", i), lat, lon)
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
	l1, o1, _ := f.Fuzz(locKey("cap", 0), 10, 10)
	l2, o2, _ := g.Fuzz(locKey("cap", 0), 10, 10)
	if l1 != l2 || o1 != o2 {
		t.Fatal("eviction changed the stateless output")
	}
}

func TestLocationFuzzConcurrent(t *testing.T) {
	f := newTestLocFuzzer(t, 1000, testLocSecret)
	setAnchorCap(f, 150) // exercise eviction under contention too
	var wg sync.WaitGroup
	errs := make(chan string, 32)
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(seed))
			for i := 0; i < 2000; i++ {
				pk := locKey("c", rng.Intn(200))
				lat, lon := randomGlobePoint(rng)
				sLat, sLon, _ := f.Fuzz(pk, lat, lon)
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
	// #2458 review: neither may start with exact positions.
	quotedTrue := write("quoted.json", `{"locationPrivacy": {"enabled": "true", "secret": "`+testLocSecret+`"}}`)
	braceInComment := write("brace.json", `{"locationPrivacy": {"_comment": "see {docs}", "enabled": true}, "port": 3000,}`)
	// Go keeps the last of two keys, and matches keys and escapes as below.
	dupEnabled := write("dup.json", `{"locationPrivacy": {"enabled": false, "enabled": true}, "port": 3000,}`)
	dupBlock := write("dupblock.json", `{"locationPrivacy": {"enabled": false}, "locationPrivacy": {"enabled": true}, "port": 3000,}`)
	escapedKey := write("escaped.json", `{"location\u0050rivacy": {"\u0065nabled": true}, "port": 3000,}`)
	// An unescaped quote earlier in the file pairs the strings up wrongly.
	strayQuote := write("stray.json", `{"_c": "say "hi" there", "locationPrivacy": {"enabled": true}}`)
	unterminated := write("unterminated.json", `{"locationPrivacy": {"enabled": false}, "_c": "oops, "locationPrivacy": {"enabled": true}}`)
	unterminatedNoMention := write("unterminated2.json", `{"locationPrivacy": {"enabled": false}, "_c": "oops}`)
	// A clean block is read exactly: braces in strings, duplicates resolved to false.
	falseBraceComment := write("falsebrace.json", `{"locationPrivacy": {"_comment": "see {docs} \"quoted\"", "enabled": false}, "port": 3000,}`)
	mentionOnly := write("mention.json", `{"_c": "set locationPrivacy to hide positions", "port": 3000,}`)
	// #2458 review: a key that lost its colon or closing quote, or is
	// followed by something else, is still the operator's block.
	blockOn := `{"enabled":true,"secret":"` + testLocSecret + `"}}`
	noColon := write("nocolon.json", `{"locationPrivacy" `+blockOn)
	noClosingQuote := write("noclosingquote.json", `{"locationPrivacy: `+blockOn)
	equalsSign := write("equals.json", `{"locationPrivacy" = `+blockOn)
	commaAfterKey := write("commakey.json", `{"locationPrivacy", `+blockOn)
	semicolon := write("semicolon.json", `{"locationPrivacy"; `+blockOn)
	misspeltKey := write("misspelt.json", `{"locationPrivacyX": {"enabled": false}, "port": 3000,}`)
	paddedKey := write("padded.json", `{"locationPrivacy ": {"enabled": false}, "port": 3000,}`)
	valueThenBrace := write("valuebrace.json", `{"_c": "locationPrivacy" {"enabled": true}}`)
	nameAtEnd := write("atend.json", `{"port": 3000, "_c": "locationPrivacy"`)
	nameAfterComma := write("aftercomma.json", `{"port": 3000, "locationPrivacy"}`)
	// A string value naming the feature is not a block.
	commentValue := write("commentvalue.json", `{"_comment": "set locationPrivacy.enabled to true", "port": 3000,}`)
	notesArray := write("notes.json", `{"_notes": ["locationPrivacy docs", "x"], "port": 3000,}`)
	lastInArray := write("lastinarray.json", `{"_notes": ["locationPrivacy docs"], "_c": "LOCATIONPRIVACY"  ,}`)
	falseWithComment := write("falsecomment.json", `{"locationPrivacy": {"_comment": "locationPrivacy is off", "enabled": false}, "port": 3000,}`)
	// A UTF-16 file (PowerShell 5.1's default) has a NUL after every character.
	utf16Body := []byte{0xFF, 0xFE}
	for _, u := range utf16.Encode([]rune(`{"locationPrivacy": {"enabled": true}}`)) {
		utf16Body = append(utf16Body, byte(u), byte(u>>8))
	}
	utf16LE := filepath.Join(dir, "utf16.json")
	if err := os.WriteFile(utf16LE, utf16Body, 0600); err != nil {
		t.Fatal(err)
	}
	// An escaped name is still the name when its separator is broken.
	escNoColon := write("escnocolon.json", `{"location\u0050rivacy" {"enabled": true}}`)
	escNoClosingQuote := write("escnoquote.json", `{"location\u0050rivacy: {"enabled": true}}`)
	escEquals := write("escequals.json", `{"\u004cocationPrivac\u0079" = {"enabled": true}}`)
	escStrayQuote := write("escstray.json", `{"a": "x", "b": "y, "location\u0050rivacy": {"enabled": true}}`)
	escMisspeltKey := write("escmisspelt.json", `{"location\u0050rivacyX": {"enabled": false}, "port": 3000,}`)
	// A block closed too early leaves its fields outside it.
	closedEarly := write("closedearly.json", "{\"locationPrivacy\": {}\n  \"enabled\": true,\n  \"secret\": \"x\"\n}}")
	closedEarlyFalse := write("closedearlyfalse.json", `{"locationPrivacy": {"enabled": false} "enabled": true}}`)
	blockAtEnd := write("blockatend.json", `{"locationPrivacy": {"enabled": false}`)
	// Escapes that do not spell the name change nothing.
	escOther := write("escother.json", `{"_c": "C:\\u006cogs \u00e9 \ud83d\ude00 \ud800 \q", "locationPrivacy": {"enabled": false}, "port": 3000,}`)
	escValue := write("escvalue.json", `{"_c": "see location\u0050rivacy", "port": 3000,}`)
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
		{"parse error, enabled quoted \"true\"", parseErr, quotedTrue, false, true},
		{"parse error, brace in a string before enabled", parseErr, braceInComment, false, true},
		{"parse error, enabled false then true", parseErr, dupEnabled, false, true},
		{"parse error, second block enabled", parseErr, dupBlock, false, true},
		{"parse error, escaped keys", parseErr, escapedKey, false, true},
		{"parse error, stray quote before the block", parseErr, strayQuote, false, true},
		{"parse error, unread tail names the block again", parseErr, unterminated, false, true},
		{"parse error, enabled false, unread tail without the name", parseErr, unterminatedNoMention, false, false},
		{"parse error, enabled false, braces and quotes in a string", parseErr, falseBraceComment, false, false},
		{"parse error, name only mentioned in a string", parseErr, mentionOnly, false, false},
		{"parse error, key without a colon", parseErr, noColon, false, true},
		{"parse error, key without a closing quote", parseErr, noClosingQuote, false, true},
		{"parse error, key then =", parseErr, equalsSign, false, true},
		{"parse error, key then comma", parseErr, commaAfterKey, false, true},
		{"parse error, key then semicolon", parseErr, semicolon, false, true},
		{"parse error, misspelt key holding the name, enabled false", parseErr, misspeltKey, false, true},
		{"parse error, key with a trailing space, enabled false", parseErr, paddedKey, false, true},
		{"parse error, value string then a brace", parseErr, valueThenBrace, false, true},
		{"parse error, name-bearing string at end of input", parseErr, nameAtEnd, false, true},
		{"parse error, name-bearing string after a comma", parseErr, nameAfterComma, false, true},
		{"parse error, comment value names enabled, no block", parseErr, commentValue, false, false},
		{"parse error, name in an array value", parseErr, notesArray, false, false},
		{"parse error, name in a one-string array and a last value", parseErr, lastInArray, false, false},
		{"parse error, enabled false, name in a string inside the block", parseErr, falseWithComment, false, false},
		{"parse error, UTF-16 file", parseErr, utf16LE, false, true},
		{"parse error, escaped key without a colon", parseErr, escNoColon, false, true},
		{"parse error, escaped key without a closing quote", parseErr, escNoClosingQuote, false, true},
		{"parse error, escaped key then =", parseErr, escEquals, false, true},
		{"parse error, stray quote before an escaped key", parseErr, escStrayQuote, false, true},
		{"parse error, escaped misspelt key, enabled false", parseErr, escMisspeltKey, false, true},
		{"parse error, block closed before enabled true", parseErr, closedEarly, false, true},
		{"parse error, enabled false, block closed before enabled true", parseErr, closedEarlyFalse, false, true},
		{"parse error, enabled false, block ends the file", parseErr, blockAtEnd, false, true},
		{"parse error, enabled false, other escapes in a string", parseErr, escOther, false, false},
		{"parse error, escaped name only mentioned in a string", parseErr, escValue, false, false},
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
