package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type elevClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *elevClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *elevClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// elevNodes is the injected node list. Tests register the points they use
// with register (or implicitly through elevTest.get).
type elevNodes struct {
	mu  sync.Mutex
	svc *elevationService
	ns  []nodeInfo
}

func (n *elevNodes) get() []nodeInfo {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]nodeInfo(nil), n.ns...)
}

// register adds nodes at the points and forces a node-set rebuild.
func (n *elevNodes) register(pts ...geoPoint) {
	n.mu.Lock()
	for _, p := range pts {
		n.ns = append(n.ns, nodeInfo{Lat: p.Lat, Lon: p.Lon, HasGPS: true})
	}
	n.mu.Unlock()
	n.svc.mu.Lock()
	n.svc.nodeSet = nil
	n.svc.mu.Unlock()
}

func (n *elevNodes) set(ns []nodeInfo) {
	n.mu.Lock()
	n.ns = ns
	n.mu.Unlock()
}

// elevTest is route LOS over a test SRTM source (newSRTMTest): tiles are
// preloaded with src.put, as a finished download leaves them, or served by
// the test upstream h (nil: every tile 404s).
type elevTest struct {
	srv   *Server
	svc   *elevationService
	src   *srtmSource
	up    *srtmUpstream
	clk   *elevClock
	nodes *elevNodes
}

func newElevTest(t testing.TB, h http.HandlerFunc) *elevTest {
	t.Helper()
	if h == nil {
		h = srtmServe(nil)
	}
	src, up, clk := newSRTMTest(t, 0, h)
	svc := newElevationService(src)
	svc.now = clk.now
	svc.wait = 10 * time.Second // downloads from the local upstream finish well within it
	nl := &elevNodes{svc: svc}
	svc.nodes = nl.get
	srv := &Server{cfg: &Config{LOS: &LOSConfig{Enabled: true}}, elevation: svc}
	return &elevTest{srv: srv, svc: svc, src: src, up: up, clk: clk, nodes: nl}
}

const elevTestClient = "192.0.2.1:5000"

func elevParsePts(pts string) []geoPoint {
	parts := strings.Split(pts, ",")
	var ps []geoPoint
	for i := 0; i+1 < len(parts); i += 2 {
		la, e1 := strconv.ParseFloat(parts[i], 64)
		lo, e2 := strconv.ParseFloat(parts[i+1], 64)
		if e1 == nil && e2 == nil {
			ps = append(ps, geoPoint{la, lo})
		}
	}
	return ps
}

// get registers the request's points as known nodes, then requests them.
func (et *elevTest) get(pts string) *httptest.ResponseRecorder {
	return et.getFrom(pts, elevTestClient, nil)
}

func (et *elevTest) getFrom(pts, remote string, hdr map[string]string) *httptest.ResponseRecorder {
	et.nodes.register(elevParsePts(pts)...)
	return et.raw(pts, remote, hdr)
}

// raw requests pts as they are, registering nothing.
func (et *elevTest) raw(pts, remote string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/elevation/profile?pts="+url.QueryEscape(pts), nil)
	req.RemoteAddr = remote
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	et.srv.handleElevationProfile(w, req)
	return w
}

// legs decodes a 200 body.
func elevLegs(t testing.TB, w *httptest.ResponseRecorder) ElevationProfileResponse {
	t.Helper()
	if w.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
	var resp ElevationProfileResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("body %q: %v", w.Body.String(), err)
	}
	return resp
}

func elevLegErrors(resp ElevationProfileResponse) []string {
	out := make([]string, len(resp.Legs))
	for i, l := range resp.Legs {
		out[i] = l.Error
		if (l.Error == "") != (len(l.ElevM) > 0) {
			out[i] += "!elev_m"
		}
	}
	return out
}

// withElevLimit gives the service a download limit of n tiles a minute.
func withElevLimit(svc *elevationService, clk *elevClock, n int) {
	svc.downloads = &clientLimiter{ipr: &wsLimiter{state: map[string]*wsIPState{}}, rl: newRateLimiter(n, time.Minute)}
	svc.downloads.rl.now = clk.now
}

func elevBucketTokens(svc *elevationService, ip string) float64 {
	return rlTokens(svc.downloads.rl, ip)
}

// rlTokens is key's tokens in l (its burst when it has no bucket yet).
func rlTokens(l *rateLimiter, key string) float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if b := l.buckets[key]; b != nil {
		return b.tokens
	}
	return l.burst
}

// elevFieldGrid is tile k with each sample set to f at its position, so
// tiles built from one f agree on their shared edges, as real SRTM tiles do.
func elevFieldGrid(k srtmKey, f func(lat, lon float64) float64) []int16 {
	g := make([]int16, srtmSide*srtmSide)
	for r := 0; r < srtmSide; r++ {
		lat := float64(k.lat+1) - float64(r)/srtmCells
		for c := 0; c < srtmSide; c++ {
			g[r*srtmSide+c] = int16(math.Round(f(lat, float64(k.lon)+float64(c)/srtmCells)))
		}
	}
	return g
}

func elevFlatGrid(v int16) []int16 {
	g := make([]int16, srtmSide*srtmSide)
	for i := range g {
		g[i] = v
	}
	return g
}

// elevLinear is linear in lat and lon and whole at every 3-arc-second grid
// point near 29-33 N, 99-95 W, so bilinear interpolation reproduces it
// exactly there.
func elevLinear(lat, lon float64) float64 { return 1200*(lat-29) + 1200*(lon+99) }

// elevWavy is rough terrain: ridges every few km in both directions.
func elevWavy(lat, lon float64) float64 {
	return 600 + 250*math.Sin(lat*9) + 180*math.Cos(lon*7) + 40*math.Sin((lat+lon)*310)
}

// ─── Request handling ──────────────────────────────────────────────────────

func TestElevationDisabled(t *testing.T) {
	for _, srv := range []*Server{
		{cfg: &Config{LOS: &LOSConfig{Enabled: false}}, elevation: newElevationService(nil)},
		{cfg: &Config{LOS: &LOSConfig{Enabled: true}}},
		{cfg: &Config{}},
	} {
		w := httptest.NewRecorder()
		srv.handleElevationProfile(w, httptest.NewRequest(http.MethodGet, "/api/elevation/profile?pts=1,1,2,2", nil))
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "los disabled") {
			t.Fatalf("code=%d body=%s want 404", w.Code, w.Body.String())
		}
	}
}

func TestElevationValidation(t *testing.T) {
	et := newElevTest(t, nil)
	many := make([]string, 0)
	for i := 0; i < (losMaxPoints+1)*2; i++ {
		many = append(many, "1")
	}
	cases := map[string]string{
		"missing":   "",
		"odd":       "1,2,3",
		"one point": "1,2",
		"17 points": strings.Join(many, ","),
		"lat range": "91,0,1,1",
		"lon range": "0,181,1,1",
		"nan":       "NaN,0,1,1",
		"inf":       "Inf,0,1,1",
		"garbage":   "a,b,c,d",
		"empty val": "1,,2,3",
	}
	for name, pts := range cases {
		if w := et.get(pts); w.Code != http.StatusBadRequest {
			t.Errorf("%s: code=%d want 400", name, w.Code)
		}
	}
	if n := et.up.total(); n != 0 {
		t.Fatalf("validation failures fetched %d tiles", n)
	}
}

func TestElevationQueryBounds(t *testing.T) {
	et := newElevTest(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/elevation/profile?pts=1,1,2,2&x="+strings.Repeat("a", 2100), nil)
	w := httptest.NewRecorder()
	et.srv.handleElevationProfile(w, req)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "pts too long") {
		t.Fatalf("raw query: %d %s", w.Code, w.Body.String())
	}
	if _, msg := parseElevationPts(strings.Repeat("1,", 500) + "1"); !strings.Contains(msg, "2 to 16") {
		t.Fatalf("comma count: %q", msg)
	}
	// 16 points (31 commas) is the maximum and still parses.
	if pts, msg := parseElevationPts(strings.TrimSuffix(strings.Repeat("1,1,", 16), ",")); msg != "" || len(pts) != 16 {
		t.Fatalf("16 points: %d %q", len(pts), msg)
	}
	if _, msg := parseElevationPts(strings.TrimSuffix(strings.Repeat("1,1,", 17), ",")); msg == "" {
		t.Fatal("17 points accepted")
	}
}

// Review #2391 point 9: route LOS and the planner read the same tiles and
// sample a leg the same way, so the same leg gets the same terrain, and so
// the same verdict, in both views. Both requests go through their real
// handlers over one shared source.
func TestElevationMatchesPlannerProfile(t *testing.T) {
	et := newElevTest(t, nil)
	for _, k := range []srtmKey{{30, -98}, {30, -97}, {31, -98}, {31, -97}} {
		et.src.put(k, elevFieldGrid(k, elevWavy))
	}
	// Points at 0.001° so the planner's quantisation keeps them as they are.
	const pts = "30.2,-97.8,30.85,-97.15,31.3,-96.6"
	cfg := &Config{LOS: &LOSConfig{Enabled: true}, Planner: &PlannerConfig{Enabled: true}}
	et.srv.cfg = cfg
	p := newPlannerService(*cfg.Planner, et.srv, et.src)
	t.Cleanup(p.Close)
	var ns []nodeInfo
	for i, pt := range elevParsePts(pts) {
		ns = append(ns, nodeInfo{PublicKey: fmt.Sprintf("ee%02d", i), Name: "N", Role: "repeater",
			Lat: pt.Lat, Lon: pt.Lon, HasGPS: true, LastSeen: time.Now()})
	}
	p.data = &fakePlannerData{serverPlannerData: serverPlannerData{et.srv}, ns: ns}
	p.fastWait = 10 * time.Second
	et.srv.planner = p

	los := elevLegs(t, et.get(pts))
	req := httptest.NewRequest(http.MethodGet, "/api/planner/profile?pts="+pts, nil)
	req.RemoteAddr = elevTestClient
	pw := httptest.NewRecorder()
	et.srv.handlePlannerProfile(pw, req)
	if pw.Code != http.StatusOK {
		t.Fatalf("planner: %d %s", pw.Code, pw.Body.String())
	}
	var plan PlannerProfileResponse
	if err := json.Unmarshal(pw.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}

	if los.Source != et.src.source || plan.Source != los.Source || los.Source != "SRTM (127.0.0.1)" {
		t.Fatalf("sources: LOS %q, planner %q, source %q", los.Source, plan.Source, et.src.source)
	}
	if len(los.Legs) != 2 || len(plan.Legs) != 2 {
		t.Fatalf("legs: LOS %d, planner %d", len(los.Legs), len(plan.Legs))
	}
	gp := elevParsePts(pts)
	for i := range los.Legs {
		l, pl := los.Legs[i], plan.Legs[i]
		if l.Error != "" || pl.Error != "" {
			t.Fatalf("leg %d errors: LOS %q, planner %q", i, l.Error, pl.Error)
		}
		a, b := gp[i], gp[i+1]
		if n := planProfileSamples(geoDistKm(a, b)); len(l.ElevM) != n {
			t.Fatalf("leg %d: %d samples, want %d", i, len(l.ElevM), n)
		}
		if l.DistanceKm != pl.DistanceKm || !slices.Equal(l.ElevM, pl.ElevM) {
			t.Fatalf("leg %d differs: LOS %v km, planner %v km, first elev %v vs %v",
				i, l.DistanceKm, pl.DistanceKm, l.ElevM[:3], pl.ElevM[:3])
		}
		// Not flat: the comparison means something.
		if slices.Min(l.ElevM) == slices.Max(l.ElevM) {
			t.Fatalf("leg %d is flat", i)
		}
	}
	if n := et.up.total(); n != 0 {
		t.Fatalf("resident tiles fetched again: %d", n)
	}
}

// Only the tiles a leg's samples fall in are loaded, not its bounding box.
func TestElevationLoadsOnlyLegTiles(t *testing.T) {
	a, b := geoPoint{30.5, -97.5}, geoPoint{32.5, -95.5}
	if d := geoDistKm(a, b); d > losMaxLegKm {
		t.Fatalf("test leg is %.0f km", d)
	}
	want := legTiles(a, b)
	box, _ := srtmSpanOf(geoBBox{S: a.Lat, W: a.Lon, N: b.Lat, E: b.Lon})
	if len(want) >= box.count() {
		t.Fatalf("the leg's samples touch %d tiles, its box %d: the test needs fewer", len(want), box.count())
	}
	for _, p := range legSamplePoints(a, b) {
		if !slices.Contains(want, srtmKeyOf(p.Lat, p.Lon)) {
			t.Fatalf("sample (%v, %v) is in no listed tile", p.Lat, p.Lon)
		}
	}
	tile := srtmConstTile(t, 250, srtmRawBytes, gzip.BestSpeed)
	tiles := map[string][]byte{}
	for i := 0; i < box.count(); i++ {
		tiles[srtmPath(box.key(i))] = tile
	}
	et := newElevTest(t, srtmServe(tiles))
	resp := elevLegs(t, et.get("30.5,-97.5,32.5,-95.5"))
	l := resp.Legs[0]
	if l.Error != "" || len(l.ElevM) != planProfileSamples(geoDistKm(a, b)) || slices.Min(l.ElevM) != 250 || slices.Max(l.ElevM) != 250 {
		t.Fatalf("leg %+v", l.Error)
	}
	for i := 0; i < box.count(); i++ {
		k := box.key(i)
		wantHits := 0
		if slices.Contains(want, k) {
			wantHits = 1
		}
		if got := et.up.hitsFor(srtmPath(k)); got != wantHits {
			t.Errorf("tile %s fetched %d times, want %d", k.name(), got, wantHits)
		}
	}
}

// Points on degree lines (a tile corner, a parallel, a meridian) read the
// tile north or east of the line, which shares its edge with the other
// side, and agree with the planner's rectangular view.
func TestElevationDegreeLinePoints(t *testing.T) {
	et := newElevTest(t, nil)
	for lat := 29; lat <= 31; lat++ {
		for lon := -98; lon <= -96; lon++ {
			k := srtmKey{lat, lon}
			et.src.put(k, elevFieldGrid(k, elevLinear))
		}
	}
	// Along the -97 meridian, to the 31,-97 corner, then along the 31st
	// parallel (the great circle bulges north of it).
	const pts = "30.2,-97,30.8,-97,31,-97,31,-96.5,31,-96"
	geo := elevParsePts(pts)
	if !slices.Contains(legTiles(geoPoint{31, -97}, geoPoint{31, -96.5}), srtmKey{31, -97}) {
		t.Fatal("the corner point does not load the tile north-east of it")
	}
	resp := elevLegs(t, et.get(pts))
	tmpl := make([]PlannerProfileLeg, len(geo)-1)
	for i := range tmpl {
		tmpl[i].DistanceKm = losRound(geoDistKm(geo[i], geo[i+1]), 1000)
	}
	v := srtmMustView(t, et.src, plannerProfileView(geoBBox{S: 30.2, W: -97, N: 31, E: -96}, plannerProfilePad(defaultPlannerMaxLegKm)))
	plan, err := planProfileLegs(t.Context(), v, geo, tmpl, func(float64) {})
	if err != nil {
		t.Fatal(err)
	}
	for i, l := range resp.Legs {
		if l.Error != "" {
			t.Fatalf("leg %d: %q", i, l.Error)
		}
		samples := legSamplePoints(geo[i], geo[i+1])
		for j, e := range l.ElevM {
			if want := elevLinear(samples[j].Lat, samples[j].Lon); math.Abs(e-want) > 0.05+1e-6 {
				t.Fatalf("leg %d sample %d at (%v, %v): %v, want %v", i, j, samples[j].Lat, samples[j].Lon, e, want)
			}
		}
		if !slices.Equal(l.ElevM, plan.Legs[i].ElevM) {
			t.Fatalf("leg %d differs from the planner view", i)
		}
	}
	if n := et.up.total(); n != 0 {
		t.Fatalf("fetched %d tiles", n)
	}
}

// Tiles that are not resident: the request waits, then answers 202 while
// the download goes on; polls are free and start nothing; once the tile is
// in, the same URL answers 200.
func TestElevationLoadingThenReady(t *testing.T) {
	gate := newSRTMGate()
	slow, ready := srtmKey{40, 10}, srtmKey{40, 11}
	tile := srtmConstTile(t, 300, srtmRawBytes, gzip.BestSpeed)
	serve := srtmServe(map[string][]byte{srtmPath(slow): tile})
	et := newElevTest(t, func(w http.ResponseWriter, r *http.Request) {
		if gate.wait(r) {
			serve(w, r)
		}
	})
	t.Cleanup(gate.open)
	et.svc.wait = 20 * time.Millisecond
	withElevLimit(et.svc, et.clk, 10)
	et.src.put(ready, elevFlatGrid(50))
	const ip = "192.0.2.30"
	const pts = "40.5,10.5,40.5,11.5"

	check202 := func(w *httptest.ResponseRecorder) {
		t.Helper()
		if w.Code != http.StatusAccepted || w.Header().Get("Retry-After") != "2" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("code=%d Retry-After=%q Cache-Control=%q body=%s", w.Code, w.Header().Get("Retry-After"),
				w.Header().Get("Cache-Control"), w.Body.String())
		}
		var st PlannerJobStatus
		if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil ||
			st != (PlannerJobStatus{State: "loading_terrain", Progress: 0.5, RetryAfterS: 2}) {
			t.Fatalf("status %s (%v)", w.Body.String(), err)
		}
	}
	check202(et.getFrom(pts, ip+":5000", nil))
	if got := elevBucketTokens(et.svc, ip); got != 9 {
		t.Fatalf("tokens=%v want 9 (one download started)", got)
	}
	srtmEventually(t, "the download to start", func() bool { return et.up.hitsFor(srtmPath(slow)) == 1 })
	// A poll while it downloads costs nothing and starts nothing.
	check202(et.raw(pts, ip+":5000", nil))
	if got := elevBucketTokens(et.svc, ip); got != 9 || et.up.total() != 1 {
		t.Fatalf("poll: tokens=%v, fetches=%d", got, et.up.total())
	}

	gate.open()
	srtmEventually(t, "the tile to load", func() bool { return et.src.Resident(srtmBox(slow)) })
	w := et.raw(pts, ip+":5000", nil)
	resp := elevLegs(t, w)
	l := resp.Legs[0]
	if l.Error != "" || l.ElevM[0] != 300 || l.ElevM[len(l.ElevM)-1] != 50 || w.Header().Get("Cache-Control") != elevCacheControl {
		t.Fatalf("after load: %q, ends %v..%v, Cache-Control %q", l.Error, l.ElevM[0], l.ElevM[len(l.ElevM)-1],
			w.Header().Get("Cache-Control"))
	}
	if got := elevBucketTokens(et.svc, ip); got != 9 || et.up.total() != 1 {
		t.Fatalf("after load: tokens=%v, fetches=%d", got, et.up.total())
	}
}

func TestElevationMissingTileIsNoTerrain(t *testing.T) {
	et := newElevTest(t, nil) // every download 404s: no such tile
	et.src.put(srtmKey{40, 10}, elevFlatGrid(70))
	const pts = "40.2,10.2,40.8,10.8,41.2,10.5"
	for round := 0; round < 2; round++ {
		w := et.get(pts)
		resp := elevLegs(t, w)
		if got := elevLegErrors(resp); !slices.Equal(got, []string{"", elevErrNoTerrain}) {
			t.Fatalf("round %d: errors %q", round, got)
		}
		if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
			t.Fatalf("round %d: Cache-Control %q", round, cc)
		}
	}
	// Known missing after the first: not asked again.
	if n := et.up.hitsFor(srtmPath(srtmKey{41, 10})); n != 1 || et.up.total() != 1 {
		t.Fatalf("missing tile fetched %d times (total %d)", n, et.up.total())
	}
}

// A request loads at most losMaxTiles distinct tiles. A leg whose new tiles
// would go past the cap is too_many_tiles and loads nothing; a later leg
// over tiles already in the request still fits.
func TestElevationTooManyTiles(t *testing.T) {
	et := newElevTest(t, nil)
	// About 280 km legs east along 30.5° N, then back west over the fifth.
	lons := []float64{-100, -97.1, -94.2, -91.3, -88.4, -85.5, -82.6, -85.5, -88.4}
	var parts []string
	var geo []geoPoint
	for _, lon := range lons {
		parts = append(parts, fmt.Sprintf("30.5,%g", lon))
		geo = append(geo, geoPoint{30.5, lon})
	}
	pts := strings.Join(parts, ",")
	seen := map[srtmKey]bool{}
	var cum []int
	for i := 0; i+1 < len(geo); i++ {
		if d := geoDistKm(geo[i], geo[i+1]); d > losMaxLegKm {
			t.Fatalf("leg %d is %.0f km", i, d)
		}
		for _, k := range legTiles(geo[i], geo[i+1]) {
			seen[k] = true
		}
		cum = append(cum, len(seen))
	}
	if cum[4] > losMaxTiles || cum[5] <= losMaxTiles {
		t.Fatalf("cumulative tiles %v: the test needs the 6th leg to cross the cap", cum)
	}
	// Preload what the request may load, so nothing is downloaded.
	et.nodes.register(elevParsePts(pts)...)
	_, keys := et.svc.plan(elevParsePts(pts))
	if len(keys) != cum[4] {
		t.Fatalf("request tiles %d, want %d", len(keys), cum[4])
	}
	flat := elevFlatGrid(10)
	for _, k := range keys {
		et.src.put(k, flat)
	}
	w := et.raw(pts, elevTestClient, nil)
	resp := elevLegs(t, w)
	tm := elevErrTooManyTiles
	if got, want := elevLegErrors(resp), []string{"", "", "", "", "", tm, tm, ""}; !slices.Equal(got, want) {
		t.Fatalf("errors %q, want %q", got, want)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" || et.up.total() != 0 {
		t.Fatalf("Cache-Control %q, fetches %d", cc, et.up.total())
	}
}

// Terrain that cannot load makes its legs "upstream" (retryable); the
// other legs are still answered.
func TestElevationTerrainUnavailable(t *testing.T) {
	near, far := srtmKey{40, 10}, srtmKey{40, 11}
	const pts = "40.2,10.2,40.8,10.8,40.5,11.5"
	tile := srtmConstTile(t, 300, srtmRawBytes, gzip.BestSpeed)

	// Downloads paused (cooldown): nothing is fetched or charged.
	et := newElevTest(t, srtmServe(map[string][]byte{srtmPath(far): tile}))
	withElevLimit(et.svc, et.clk, 10)
	et.src.put(near, elevFlatGrid(70))
	et.src.mu.Lock()
	et.src.coolTo = et.clk.now().Add(time.Minute)
	et.src.mu.Unlock()
	w := et.getFrom(pts, "192.0.2.40:5000", nil)
	resp := elevLegs(t, w)
	if got := elevLegErrors(resp); !slices.Equal(got, []string{"", elevErrUpstream}) || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cooldown: errors %q, Cache-Control %q", got, w.Header().Get("Cache-Control"))
	}
	if et.up.total() != 0 || elevBucketTokens(et.svc, "192.0.2.40") != 10 {
		t.Fatalf("cooldown: fetches %d, tokens %v", et.up.total(), elevBucketTokens(et.svc, "192.0.2.40"))
	}

	// Download budget used up.
	et.clk.add(time.Minute)
	et.src.mu.Lock()
	for i := 0; i < srtmPerMinute; i++ {
		et.src.budget.starts = append(et.src.budget.starts, et.clk.now())
	}
	et.src.mu.Unlock()
	if got := elevLegErrors(elevLegs(t, et.raw(pts, elevTestClient, nil))); !slices.Equal(got, []string{"", elevErrUpstream}) {
		t.Fatalf("budget: errors %q", got)
	}

	// The tile server failing opens a cooldown.
	et2 := newElevTest(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	if got := elevLegErrors(elevLegs(t, et2.get("40.5,11.2,40.5,11.8"))); !slices.Equal(got, []string{elevErrUpstream}) {
		t.Fatalf("502: errors %q", got)
	}
	if !et2.src.cooling() {
		t.Fatal("a failed download did not pause downloads")
	}
}

// ─── Per-client limit ──────────────────────────────────────────────────────

// A request is charged one token per tile download it starts, all or
// nothing; resident tiles cost nothing, so such a request is never limited.
func TestElevationChargedPerDownload(t *testing.T) {
	tile := srtmConstTile(t, 120, srtmRawBytes, gzip.BestSpeed)
	tiles := map[string][]byte{}
	for lon := 10; lon <= 18; lon++ {
		tiles[srtmPath(srtmKey{40, lon})] = tile
	}
	et := newElevTest(t, srtmServe(tiles))
	withElevLimit(et.svc, et.clk, 4)
	const a, b = "192.0.2.20", "192.0.2.21"
	ok := func(pts, ip string) {
		t.Helper()
		if w := et.getFrom(pts, ip+":5000", nil); w.Code != http.StatusOK {
			t.Fatalf("%s from %s: %d %s", pts, ip, w.Code, w.Body.String())
		}
	}
	tokens := func(ip string, want float64) {
		t.Helper()
		if got := elevBucketTokens(et.svc, ip); math.Abs(got-want) > 1e-6 {
			t.Fatalf("%s tokens=%v want %v", ip, got, want)
		}
	}

	ok("40.5,10.5,40.5,11.5", a) // 2 downloads
	tokens(a, 2)
	ok("40.5,10.5,40.5,11.5", a) // resident: free
	tokens(a, 2)

	// 3 downloads, 2 tokens: refused, nothing taken, nothing fetched.
	const three = "40.5,11.5,40.5,12.5,40.5,13.5,40.5,14.5"
	before := et.up.total()
	w := et.getFrom(three, a+":5000", nil)
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "rate limited") {
		t.Fatalf("code=%d body=%s want 429", w.Code, w.Body.String())
	}
	if ra, _ := strconv.Atoi(w.Header().Get("Retry-After")); ra < 1 || ra > 60 {
		t.Fatalf("Retry-After=%q", w.Header().Get("Retry-After"))
	}
	tokens(a, 2)
	if et.up.total() != before {
		t.Fatal("a refused request fetched tiles")
	}

	ok(three, b) // another client has its own bucket
	tokens(b, 1)
	ok("40.5,14.5,40.5,16.5", a) // 2 downloads: a is out of tokens
	tokens(a, 0)
	ok(three, a) // resident now: never limited
	tokens(a, 0)
	const more = "40.5,16.5,40.5,18.5" // 2 downloads
	if w := et.getFrom(more, a+":5000", nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("out of tokens: %d", w.Code)
	}
	et.clk.add(31 * time.Second) // 4 a minute: 2.07 tokens back
	ok(more, a)
	tokens(a, 31.0/60*4-2)
}

// Code review #1 (#2391): a request whose tiles the download budget only
// partly admits answered 202 while the admitted one loaded, and every poll
// counted the refused tile as a new start and charged for it again, until
// the client got 429 on a poll. The budget is now peeked first: a tile it
// has no room for is upstream and costs nothing, so polls stay free.
func TestElevationPollAfterRefusedIsFree(t *testing.T) {
	gate := newSRTMGate()
	slow, refused := srtmKey{40, 10}, srtmKey{40, 11}
	tile := srtmConstTile(t, 300, srtmRawBytes, gzip.BestSpeed)
	serve := srtmServe(map[string][]byte{srtmPath(slow): tile, srtmPath(refused): tile})
	et := newElevTest(t, func(w http.ResponseWriter, r *http.Request) {
		if gate.wait(r) {
			serve(w, r)
		}
	})
	t.Cleanup(gate.open)
	et.svc.wait = 20 * time.Millisecond
	withElevLimit(et.svc, et.clk, 10)
	// The download budget has room for one more start this minute.
	et.src.mu.Lock()
	for i := 0; i < srtmPerMinute-1; i++ {
		et.src.budget.starts = append(et.src.budget.starts, et.clk.now())
	}
	et.src.mu.Unlock()
	const ip = "192.0.2.31"
	// One leg in each tile, and one across both.
	const pts = "40.2,10.2,40.8,10.8,40.2,11.2,40.8,11.8"

	w := et.getFrom(pts, ip+":5000", nil)
	if w.Code != http.StatusAccepted {
		t.Fatalf("first request: %d %s", w.Code, w.Body.String())
	}
	if got := elevBucketTokens(et.svc, ip); got != 9 {
		t.Fatalf("tokens=%v want 9 (one download admitted, one refused)", got)
	}
	srtmEventually(t, "the admitted download", func() bool { return et.up.hitsFor(srtmPath(slow)) == 1 })
	// Spend the rest: a poll that charged anything would now be limited.
	if ok, _ := et.svc.downloads.rl.takeN(ip, 9); !ok {
		t.Fatal("could not drain the bucket")
	}
	for i := 0; i < 5; i++ {
		if w := et.raw(pts, ip+":5000", nil); w.Code != http.StatusAccepted {
			t.Fatalf("poll %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	if got := elevBucketTokens(et.svc, ip); got != 0 {
		t.Fatalf("polls took tokens: %v left", got)
	}
	gate.open()
	srtmEventually(t, "the tile to load", func() bool { return et.src.Resident(srtmBox(slow)) })
	w = et.raw(pts, ip+":5000", nil)
	if got := elevLegErrors(elevLegs(t, w)); !slices.Equal(got, []string{"", elevErrUpstream, elevErrUpstream}) {
		t.Fatalf("after load: errors %q", got)
	}
	if w.Header().Get("Cache-Control") != "no-store" || et.up.hitsFor(srtmPath(refused)) != 0 || elevBucketTokens(et.svc, ip) != 0 {
		t.Fatalf("Cache-Control %q, refused tile fetched %d times, tokens %v", w.Header().Get("Cache-Control"),
			et.up.hitsFor(srtmPath(refused)), elevBucketTokens(et.svc, ip))
	}
	// Once the budget has room again the refused tile downloads, charged
	// once: the bucket refilled to 10, less 1.
	et.clk.add(time.Minute)
	if got := elevLegErrors(elevLegs(t, et.raw(pts, ip+":5000", nil))); !slices.Equal(got, []string{"", "", ""}) {
		t.Fatalf("budget back: errors %q", got)
	}
	if got := elevBucketTokens(et.svc, ip); got != 9 || et.up.hitsFor(srtmPath(refused)) != 1 {
		t.Fatalf("budget back: tokens %v, refused tile fetched %d times", got, et.up.hitsFor(srtmPath(refused)))
	}
}

func TestElevationRateLimitIndistinctClientsShareBucket(t *testing.T) {
	tile := srtmConstTile(t, 120, srtmRawBytes, gzip.BestSpeed)
	tiles := map[string][]byte{}
	for lon := 20; lon <= 22; lon++ {
		tiles[srtmPath(srtmKey{40, lon})] = tile
	}
	et := newElevTest(t, srtmServe(tiles))
	withElevLimit(et.svc, et.clk, 2)
	// Loopback peer, no trusted proxy: X-Forwarded-For is not believed.
	for i, xff := range []string{"198.51.100.1", "198.51.100.2"} {
		pts := fmt.Sprintf("40.2,%d.2,40.8,%d.8", 20+i, 20+i)
		if w := et.getFrom(pts, "127.0.0.1:1", map[string]string{"X-Forwarded-For": xff}); w.Code != http.StatusOK {
			t.Fatalf("request %d: %d", i, w.Code)
		}
	}
	if w := et.getFrom("40.2,22.2,40.8,22.8", "127.0.0.1:1", map[string]string{"X-Forwarded-For": "198.51.100.3"}); w.Code != http.StatusTooManyRequests {
		t.Fatalf("shared bucket not enforced: %d", w.Code)
	}
}

// ─── Known-node rule ───────────────────────────────────────────────────────

func TestElevationUnknownPoint(t *testing.T) {
	et := newElevTest(t, nil)
	et.src.put(srtmKey{1, 1}, elevFlatGrid(5))
	et.nodes.set([]nodeInfo{
		{Lat: 1, Lon: 1, HasGPS: true},
		{Lat: 1.2, Lon: 1.2, HasGPS: true},
		{Lat: 0, Lon: 0, HasGPS: true},      // null island: excluded
		{Lat: 0.5, Lon: 0.5, HasGPS: false}, // no GPS: excluded
	})
	leg := func(pts string) ElevationLeg {
		t.Helper()
		return elevLegs(t, et.raw(pts, elevTestClient, nil)).Legs[0]
	}
	for name, pts := range map[string]string{
		"unknown":     "1.5,1.5,1,1",
		"unknown b":   "1,1,1.5,1.5",
		"null island": "0,0,1,1",
		"no gps":      "0.5,0.5,1,1",
	} {
		if l := leg(pts); l.Error != elevErrUnknownPoint || len(l.ElevM) != 0 {
			t.Errorf("%s: %+v", name, l)
		}
	}
	if n := et.up.total(); n != 0 {
		t.Fatalf("unknown points fetched %d tiles", n)
	}
	if l := leg("1,1,1.2,1.2"); l.Error != "" {
		t.Fatalf("known pair: %+v", l)
	}
	// too_long wins over unknown_point.
	if l := leg("10,10,10,20"); l.Error != elevErrTooLong {
		t.Fatalf("order: %+v", l)
	}
	// The set is rebuilt only after 60 s.
	et.nodes.set(append(et.nodes.get(), nodeInfo{Lat: 1.4, Lon: 1.4, HasGPS: true}))
	if l := leg("1,1,1.4,1.4"); l.Error != elevErrUnknownPoint {
		t.Fatalf("set refreshed early: %+v", l)
	}
	et.clk.add(elevNodeSetTTL + time.Second)
	if l := leg("1,1,1.4,1.4"); l.Error != "" {
		t.Fatalf("set not refreshed after TTL: %+v", l)
	}
}

// Review #2391 point 4: a node /api/nodes hides (blacklist, hidden name,
// outside the geo_filter) is no valid endpoint, and a rule change applies to
// the next request, not after elevNodeSetTTL.
func TestElevationHiddenNodesAreUnknown(t *testing.T) {
	et := newElevTest(t, nil)
	et.src.put(srtmKey{1, 1}, elevFlatGrid(5))
	srv, svc := et.srv, et.svc
	srv.cfg.SetNodeBlacklist([]string{"bad1"})
	srv.cfg.SetHiddenNamePrefixes([]string{"zz-"})
	inside := &GeoFilterConfig{Polygon: [][2]float64{{0.5, 0.5}, {0.5, 1.35}, {1.35, 1.35}, {1.35, 0.5}}}
	srv.setGeoFilter(inside)
	svc.visibility = srv
	et.nodes.set([]nodeInfo{
		{PublicKey: "ok1", Name: "A", Lat: 1, Lon: 1, HasGPS: true},
		{PublicKey: "ok2", Name: "B", Lat: 1.2, Lon: 1.2, HasGPS: true},
		{PublicKey: "BAD1", Name: "C", Lat: 1.1, Lon: 1.1, HasGPS: true},
		{PublicKey: "hid1", Name: "zz-x", Lat: 1.3, Lon: 1.3, HasGPS: true},
		{PublicKey: "far1", Name: "D", Lat: 1.4, Lon: 1.4, HasGPS: true},
	})
	leg := func(pts string) ElevationLeg {
		t.Helper()
		resp := elevLegs(t, et.raw(pts, elevTestClient, nil))
		if len(resp.Legs) != 1 {
			t.Fatalf("%s: %+v", pts, resp)
		}
		return resp.Legs[0]
	}
	for name, pts := range map[string]string{
		"blacklisted":        "1,1,1.1,1.1",
		"hidden name":        "1,1,1.3,1.3",
		"outside geo_filter": "1,1,1.4,1.4",
		"hidden first":       "1.1,1.1,1,1",
	} {
		if l := leg(pts); l.Error != elevErrUnknownPoint || len(l.ElevM) != 0 {
			t.Errorf("%s: %+v", name, l)
		}
	}
	if l := leg("1,1,1.2,1.2"); l.Error != "" {
		t.Fatalf("visible pair: %+v", l)
	}

	// Each rule change applies at once (the clock never moves), and terrain
	// is not served for an endpoint that is now hidden.
	steps := []struct {
		name   string
		change func()
		pts    string
		want   string
	}{
		{"blacklist ok2", func() { srv.cfg.SetNodeBlacklist([]string{"bad1", "OK2"}) }, "1,1,1.2,1.2", elevErrUnknownPoint},
		{"unblacklist ok2", func() { srv.cfg.SetNodeBlacklist([]string{"bad1"}) }, "1,1,1.2,1.2", ""},
		{"hide B", func() { srv.cfg.SetHiddenNamePrefixes([]string{"zz-", "B"}) }, "1,1,1.2,1.2", elevErrUnknownPoint},
		{"unhide B", func() { srv.cfg.SetHiddenNamePrefixes([]string{"zz-"}) }, "1,1,1.2,1.2", ""},
		{"shrink geo_filter", func() {
			srv.setGeoFilter(&GeoFilterConfig{Polygon: [][2]float64{{0.5, 0.5}, {0.5, 1.1}, {1.1, 1.1}, {1.1, 0.5}}})
		}, "1,1,1.2,1.2", elevErrUnknownPoint},
		{"drop geo_filter", func() { srv.setGeoFilter(nil) }, "1,1,1.4,1.4", ""},
	}
	for _, st := range steps {
		st.change()
		if l := leg(st.pts); l.Error != st.want {
			t.Fatalf("after %s: %s → %+v, want error %q", st.name, st.pts, l, st.want)
		}
	}
	if n := et.up.total(); n != 0 {
		t.Fatalf("fetched %d tiles", n)
	}
}

func TestElevationUnknownPointFailClosed(t *testing.T) {
	et := newElevTest(t, nil)
	et.svc.nodes = nil
	if l := elevLegs(t, et.raw("1,1,1.2,1.2", elevTestClient, nil)).Legs[0]; l.Error != elevErrUnknownPoint {
		t.Fatalf("nil nodes: %+v", l)
	}
	et.svc.nodes = func() []nodeInfo { return nil }
	if l := elevLegs(t, et.raw("1,1,1.2,1.2", elevTestClient, nil)).Legs[0]; l.Error != elevErrUnknownPoint || et.up.total() != 0 {
		t.Fatalf("empty nodes: %+v fetches=%d", l, et.up.total())
	}
}

func TestElevationCacheControl(t *testing.T) {
	et := newElevTest(t, nil)
	et.src.put(srtmKey{1, 1}, elevFlatGrid(5))
	cc := func(pts string, register bool, want string, wantErrs ...string) {
		t.Helper()
		var w *httptest.ResponseRecorder
		if register {
			w = et.get(pts)
		} else {
			w = et.raw(pts, elevTestClient, nil)
		}
		if got := elevLegErrors(elevLegs(t, w)); !slices.Equal(got, wantErrs) || w.Header().Get("Cache-Control") != want {
			t.Fatalf("%s: errors %q, Cache-Control %q; want %q, %q", pts, got, w.Header().Get("Cache-Control"), wantErrs, want)
		}
	}
	cc("1,1,1.2,1.2", true, elevCacheControl, "")
	cc("1,1,1.2,1.2,1,5", true, elevCacheControl, "", elevErrTooLong) // too_long only: still cacheable
	cc("60,60,60.1,60.1", false, "no-store", elevErrUnknownPoint)
	cc("1,1,1.2,1.2,1.5,2.5", true, "no-store", "", elevErrNoTerrain) // N01E002 404s
}

func TestRateLimiterTakeN(t *testing.T) {
	l := newRateLimiter(5, time.Minute)
	if ok, _ := l.takeN("k", 3); !ok {
		t.Fatal("3 of 5 refused")
	}
	if ok, wait := l.takeN("k", 3); ok || wait <= 0 {
		t.Fatalf("ok=%v wait=%v", ok, wait)
	}
	if ok, _ := l.takeN("k", 2); !ok {
		t.Fatal("refusal took tokens")
	}
}

// BenchmarkElevationServeResident: a full request (16 points, 15 legs of
// about 190 km, so 600 samples a leg) whose tiles are all resident: tile
// planning, sampling and encoding, no download.
func BenchmarkElevationServeResident(b *testing.B) {
	et := newElevTest(b, nil)
	flat := elevFlatGrid(100)
	for lat := 30; lat <= 31; lat++ {
		for lon := -98; lon <= -96; lon++ {
			et.src.put(srtmKey{lat, lon}, flat)
		}
	}
	var parts []string
	for i := 0; i < losMaxPoints; i++ {
		lat := 30.1 + 1.7*float64(i%2)
		parts = append(parts, fmt.Sprintf("%.3f,%.3f", lat, -97.9+0.18*float64(i)))
	}
	pts := strings.Join(parts, ",")
	et.nodes.register(elevParsePts(pts)...)
	if resp := elevLegs(b, et.raw(pts, elevTestClient, nil)); !slices.Equal(elevLegErrors(resp), make([]string, losMaxPoints-1)) {
		b.Fatalf("benchmark route: %q", elevLegErrors(resp))
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if w := et.raw(pts, elevTestClient, nil); w.Code != http.StatusOK {
			b.Fatal(w.Code)
		}
	}
}
