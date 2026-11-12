package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ─── Fakes ──────────────────────────────────────────────────────────────────

type plannerTestClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *plannerTestClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *plannerTestClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

var plannerTestStart = time.Unix(1_700_000_000, 0)

// fakeTerrain is an in-memory terrainSource: flat-ish terrain over whatever
// box is asked for, with a switchable resident flag, error and gate.
type fakeTerrain struct {
	mu       sync.Mutex
	resident bool
	err      error
	gate     chan struct{}             // non-nil: View blocks until it is closed (or ctx ends)
	gates    map[geoBBox]chan struct{} // per view: blocks, and is not resident, until released
	missing  map[[2]int]bool           // 1° tiles (south-west corner) without data
	tiles    func(geoBBox) int         // nil: real 1° tile count
	views    int
	lastView geoBBox
}

func (f *fakeTerrain) View(ctx context.Context, bb geoBBox) (terrainView, error) {
	f.mu.Lock()
	f.views++
	f.lastView = bb
	gate, err := f.gate, f.err
	if g, ok := f.gates[bb]; ok {
		gate = g
	}
	missing := f.missing
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	return fakeTerrainView{bb: bb, missing: missing}, nil
}

func (f *fakeTerrain) Resident(bb geoBBox) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, held := f.gates[bb]; held {
		return false
	}
	return f.resident
}

// hold makes View(bb) block, and bb not resident, until release(bb).
func (f *fakeTerrain) hold(bb geoBBox) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.gates == nil {
		f.gates = map[geoBBox]chan struct{}{}
	}
	f.gates[bb] = make(chan struct{})
}

func (f *fakeTerrain) release(bb geoBBox) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if g, ok := f.gates[bb]; ok {
		close(g)
		delete(f.gates, bb)
	}
}

func (f *fakeTerrain) releaseAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for bb, g := range f.gates {
		close(g)
		delete(f.gates, bb)
	}
}

// fakeTiles lists the 1° tiles covering bb, as srtmSpanOf counts them.
func fakeTiles(bb geoBBox) [][2]int {
	lat0, lon0 := int(math.Floor(bb.S)), int(math.Floor(bb.W))
	lat1, lon1 := int(math.Ceil(bb.N))-1, int(math.Ceil(bb.E))-1
	if lat1 < lat0 {
		lat1 = lat0
	}
	if lon1 < lon0 {
		lon1 = lon0
	}
	var out [][2]int
	for la := lat0; la <= lat1; la++ {
		for lo := lon0; lo <= lon1; lo++ {
			out = append(out, [2]int{la, lo})
		}
	}
	return out
}

func (f *fakeTerrain) TileCount(bb geoBBox) int {
	if f.tiles != nil {
		return f.tiles(bb)
	}
	return len(fakeTiles(bb))
}

func (f *fakeTerrain) set(fn func(f *fakeTerrain)) { f.mu.Lock(); fn(f); f.mu.Unlock() }
func (f *fakeTerrain) viewCount() int              { f.mu.Lock(); defer f.mu.Unlock(); return f.views }
func (f *fakeTerrain) lastBBox() geoBBox           { f.mu.Lock(); defer f.mu.Unlock(); return f.lastView }

// fakeTerrainView is terrain over bb; tiles in missing read 0 m and are
// reported through terrainCoverage, like srtmView's.
type fakeTerrainView struct {
	bb      geoBBox
	missing map[[2]int]bool
}

func (v fakeTerrainView) inside(lat, lon float64) bool {
	const tol = 1e-9
	return lat >= v.bb.S-tol && lat <= v.bb.N+tol && lon >= v.bb.W-tol && lon <= v.bb.E+tol
}

func (v fakeTerrainView) ElevationAt(lat, lon float64) (float64, bool) {
	if !v.inside(lat, lon) {
		return 0, false
	}
	if v.MissingAt(lat, lon) {
		return 0, true
	}
	return 100 + 3*lat, true
}

func (v fakeTerrainView) MissingTiles() (missing, total int) {
	tiles := fakeTiles(v.bb)
	for _, t := range tiles {
		if v.missing[t] {
			missing++
		}
	}
	return missing, len(tiles)
}

func (v fakeTerrainView) MissingAt(lat, lon float64) bool {
	return !v.inside(lat, lon) || v.missing[[2]int{int(math.Floor(lat)), int(math.Floor(lon))}]
}

// fakePlannerData serves fixed nodes and edges; the hiding rules are the
// real serverPlannerData ones, read from the test Server's config.
type fakePlannerData struct {
	serverPlannerData
	mu    sync.Mutex
	ns    []nodeInfo
	heard map[string]time.Time
	es    []NeighborEdge
}

func (d *fakePlannerData) nodes() []nodeInfo { d.mu.Lock(); defer d.mu.Unlock(); return d.ns }
func (d *fakePlannerData) lastHeard(pks []string) map[string]time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := map[string]time.Time{}
	for _, pk := range pks {
		if t, ok := d.heard[pk]; ok {
			out[pk] = t
		}
	}
	return out
}
func (d *fakePlannerData) edges([]string) []NeighborEdge {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.es)
}
func (d *fakePlannerData) setNodes(ns []nodeInfo) { d.mu.Lock(); d.ns = ns; d.mu.Unlock() }

// plannerTestNodes: a visible cluster near 30.15,-97.05, nodes /api/nodes
// hides (bad*, zz-*) both inside it and alone far away, and nodes the
// planner must skip for other reasons (companion role, no GPS).
func plannerTestNodes() []nodeInfo {
	return []nodeInfo{
		{PublicKey: "aa01", Name: "Hill", Role: "repeater", Lat: 30.10, Lon: -97.10, HasGPS: true, LastSeen: plannerTestStart},
		{PublicKey: "aa02", Name: "Mesa", Role: "repeater", Lat: 30.20, Lon: -97.00, HasGPS: true, LastSeen: plannerTestStart},
		{PublicKey: "aa03", Name: "Hall", Role: "room", Lat: 30.15, Lon: -96.95, HasGPS: true},
		{PublicKey: "cc01", Name: "Phone", Role: "companion", Lat: 30.30, Lon: -97.30, HasGPS: true},
		{PublicKey: "cc02", Name: "North phone", Role: "companion", Lat: 31.50, Lon: -97.10, HasGPS: true},
		{PublicKey: "bad1", Name: "Banned", Role: "repeater", Lat: 30.12, Lon: -97.05, HasGPS: true},
		{PublicKey: "hid1", Name: "zz-secret", Role: "repeater", Lat: 30.18, Lon: -97.08, HasGPS: true},
		{PublicKey: "bad2", Name: "Banned far", Role: "repeater", Lat: 40.00, Lon: -80.00, HasGPS: true},
		{PublicKey: "hid2", Name: "zz-far", Role: "repeater", Lat: 41.00, Lon: -81.00, HasGPS: true},
		{PublicKey: "nogps", Name: "Nowhere", Role: "repeater"},
	}
}

// plannerStubs stands in for the planner_search functions.
type plannerStubs struct {
	mu         sync.Mutex
	gate       chan struct{} // non-nil: searches block until closed (or ctx ends)
	entered    chan struct{}
	panicMsg   string
	progress   float64
	covCalls   int
	relayCalls int
	lastPlan   planInputs
	lastRelay  PlannerRelayParams
	relayNodes []plannerNode
	cause      error // context.Cause a cancelled search saw
}

func (st *plannerStubs) hold() {
	st.mu.Lock()
	st.gate = make(chan struct{})
	st.mu.Unlock()
}

func (st *plannerStubs) release() {
	st.mu.Lock()
	if st.gate != nil {
		close(st.gate)
		st.gate = nil
	}
	st.mu.Unlock()
}

func (st *plannerStubs) set(fn func(st *plannerStubs)) { st.mu.Lock(); fn(st); st.mu.Unlock() }

func (st *plannerStubs) calls() (cov, relay int) {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.covCalls, st.relayCalls
}

// block runs the common part of both stubs after their bookkeeping.
func (st *plannerStubs) block(ctx context.Context, progress func(float64)) error {
	st.mu.Lock()
	gate, pm, pr := st.gate, st.panicMsg, st.progress
	st.mu.Unlock()
	if pm != "" {
		panic(pm)
	}
	if pr > 0 {
		progress(pr)
	}
	select {
	case st.entered <- struct{}{}:
	default:
	}
	if gate == nil {
		return nil
	}
	select {
	case <-gate:
		return nil
	case <-ctx.Done():
		st.mu.Lock()
		st.cause = context.Cause(ctx)
		st.mu.Unlock()
		return ctx.Err()
	}
}

func (st *plannerStubs) coverage(ctx context.Context, v terrainView, in planInputs, progress func(float64)) (PlannerPlanResponse, error) {
	st.mu.Lock()
	st.covCalls++
	st.lastPlan = in
	st.mu.Unlock()
	if err := st.block(ctx, progress); err != nil {
		return PlannerPlanResponse{}, err
	}
	out := PlannerPlanResponse{Status: "connected"}
	for _, n := range in.Nodes {
		out.Nodes = append(out.Nodes, PlannerNodeOut{PubKey: n.PubKey, Name: n.Name, Role: n.Role,
			Lat: n.Lat, Lon: n.Lon, Stale: n.Stale, InArea: n.InArea})
	}
	return out, nil
}

func (st *plannerStubs) relay(ctx context.Context, v terrainView, nodes []plannerNode, p PlannerRelayParams, progress func(float64)) (PlannerRelayResponse, error) {
	st.mu.Lock()
	st.relayCalls++
	st.lastRelay = p
	st.relayNodes = nodes
	st.mu.Unlock()
	if err := st.block(ctx, progress); err != nil {
		return PlannerRelayResponse{}, err
	}
	out := PlannerRelayResponse{}
	for _, n := range nodes {
		out.Existing = append(out.Existing, PlannerRelayOption{Kind: "existing", PubKey: n.PubKey, Name: n.Name, Lat: n.Lat, Lon: n.Lon})
	}
	return out, nil
}

// ─── Harness ────────────────────────────────────────────────────────────────

type plannerHarness struct {
	srv   *Server
	p     *plannerService
	src   *fakeTerrain
	data  *fakePlannerData
	clk   *plannerTestClock
	stubs *plannerStubs
}

const (
	plannerCovPath   = "/api/planner/coverage?bbox=30.0,-97.2,30.35,-96.85"
	plannerRelayPath = "/api/planner/relay?a=30.1,-97.1&b=30.2,-97.0"
	plannerProfPath  = "/api/planner/profile?pts=30.1,-97.1,30.2,-97.0"
)

func newPlannerHarness(t *testing.T) *plannerHarness {
	t.Helper()
	return newPlannerHarnessWith(t, PlannerConfig{Enabled: true})
}

// newPlannerHarnessWith is newPlannerHarness with its own planner block.
func newPlannerHarnessWith(t *testing.T, pc PlannerConfig) *plannerHarness {
	t.Helper()
	cfg := &Config{
		Planner:            &pc,
		NodeBlacklist:      []string{"bad1", "bad2"},
		HiddenNamePrefixes: []string{"zz-"},
	}
	srv := &Server{cfg: cfg}
	p := newPlannerService(pc, srv)
	h := &plannerHarness{
		srv:   srv,
		p:     p,
		src:   &fakeTerrain{resident: true},
		clk:   &plannerTestClock{t: plannerTestStart},
		stubs: &plannerStubs{entered: make(chan struct{}, 64)},
	}
	h.data = &fakePlannerData{serverPlannerData: serverPlannerData{srv}, ns: plannerTestNodes()}
	p.src = h.src
	p.data = h.data
	p.coverage = h.stubs.coverage
	p.relay = h.stubs.relay
	p.now = h.clk.now
	p.limit.rl.now = h.clk.now
	p.global.now = h.clk.now
	p.fastWait = 0
	p.watchEvery = 2 * time.Millisecond
	srv.planner = p
	t.Cleanup(func() {
		h.stubs.release()
		h.src.releaseAll()
		p.Close()
	})
	return h
}

func (h *plannerHarness) get(path, ip string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = ip + ":40000"
	w := httptest.NewRecorder()
	switch {
	case strings.HasPrefix(path, "/api/planner/profile"):
		h.srv.handlePlannerProfile(w, req)
	case strings.HasPrefix(path, "/api/planner/relay"):
		h.srv.handlePlannerRelay(w, req)
	default:
		h.srv.handlePlannerCoverage(w, req)
	}
	return w
}

// pollUntil re-polls path until its status is want, failing after 5 s.
func (h *plannerHarness) pollUntil(t *testing.T, path, ip string, want int) *httptest.ResponseRecorder {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		w := h.get(path, ip)
		if w.Code == want {
			return w
		}
		if w.Code != http.StatusAccepted || time.Now().After(deadline) {
			t.Fatalf("%s: code=%d body=%s, want %d", path, w.Code, w.Body.String(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func (h *plannerHarness) waitEntered(t *testing.T) {
	t.Helper()
	select {
	case <-h.stubs.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("search never started")
	}
}

func (h *plannerHarness) liveJobs() int {
	h.p.mu.Lock()
	defer h.p.mu.Unlock()
	return len(h.p.jobs)
}

func (h *plannerHarness) tokens(ip string) float64 {
	rl := h.p.limit.rl
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if b := rl.buckets[ip]; b != nil {
		return b.tokens
	}
	return float64(plannerIPJobsPerMin)
}

func plannerWaitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

func decodeStatus(t *testing.T, w *httptest.ResponseRecorder) PlannerJobStatus {
	t.Helper()
	var st PlannerJobStatus
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatalf("status body %q: %v", w.Body.String(), err)
	}
	return st
}

// ─── Tests ──────────────────────────────────────────────────────────────────

func TestPlannerAPIDisabled(t *testing.T) {
	off := newPlannerService(PlannerConfig{}, nil)
	t.Cleanup(off.Close)
	for _, srv := range []*Server{
		{cfg: &Config{}},
		{cfg: &Config{Planner: &PlannerConfig{Enabled: false}}, planner: off},
		{cfg: &Config{Planner: &PlannerConfig{Enabled: true}}},
	} {
		h := &plannerHarness{srv: srv}
		for _, path := range []string{plannerProfPath, plannerRelayPath, plannerCovPath} {
			w := h.get(path, "198.51.100.1")
			if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "planner disabled") {
				t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
			}
		}
	}
}

func TestPlannerAPIValidation(t *testing.T) {
	seventeen := strings.TrimSuffix(strings.Repeat("30.1,-97.1,", 17), ",")
	long := plannerProfPath + "&pad=" + strings.Repeat("x", 520)
	manyTiles := func(n int) func(h *plannerHarness) {
		return func(h *plannerHarness) { h.src.set(func(f *fakeTerrain) { f.tiles = func(geoBBox) int { return n } }) }
	}
	cases := []struct {
		name, path, want string
		setup            func(h *plannerHarness)
	}{
		// common query rules
		{name: "long query", path: long, want: "query too long"},
		{name: "unknown key", path: plannerProfPath + "&x=1", want: "unknown parameter"},
		{name: "duplicate key", path: plannerProfPath + "&pts=30.1,-97.1,30.2,-97.0", want: "duplicate parameter"},
		{name: "semicolon", path: "/api/planner/profile?pts=30.1,-97.1;x=1", want: "malformed query"},
		{name: "relay unknown key", path: plannerRelayPath + "&he=5", want: "unknown parameter"},
		{name: "relay duplicate", path: plannerRelayPath + "&a=30.1,-97.1", want: "duplicate parameter"},
		{name: "coverage unknown", path: plannerCovPath + "&_=1", want: "unknown parameter"},
		{name: "coverage duplicate", path: plannerCovPath + "&sites=2&sites=3", want: "duplicate parameter"},
		{name: "coverage long", path: plannerCovPath + "&baseline=" + strings.Repeat("o", 520), want: "query too long"},

		// profile
		{name: "pts missing", path: "/api/planner/profile", want: "pts required"},
		{name: "pts NaN", path: "/api/planner/profile?pts=NaN,-97.1,30.2,-97.0", want: "invalid"},
		{name: "pts Inf", path: "/api/planner/profile?pts=30.1,Inf,30.2,-97.0", want: "invalid"},
		{name: "pts -Inf", path: "/api/planner/profile?pts=30.1,-97.1,-Inf,-97.0", want: "invalid"},
		{name: "pts lat range", path: "/api/planner/profile?pts=91,-97.1,30.2,-97.0", want: "out-of-range"},
		{name: "pts lon range", path: "/api/planner/profile?pts=30.1,-181,30.2,-97.0", want: "out-of-range"},
		{name: "pts empty number", path: "/api/planner/profile?pts=30.1,,30.2,-97.0", want: "invalid"},
		{name: "one point", path: "/api/planner/profile?pts=30.1,-97.1", want: "2 to 16"},
		{name: "odd count", path: "/api/planner/profile?pts=30.1,-97.1,30.2", want: "2 to 16"},
		{name: "17 points", path: "/api/planner/profile?pts=" + seventeen, want: "2 to 16"},
		{name: "profile tiles", path: plannerProfPath, want: plannerErrTooManyTiles, setup: manyTiles(13)},

		// relay
		{name: "a missing", path: "/api/planner/relay?b=30.2,-97.0", want: "a required"},
		{name: "b missing", path: "/api/planner/relay?a=30.2,-97.0", want: "b required"},
		{name: "a one number", path: "/api/planner/relay?a=30.1&b=30.2,-97.0", want: "a must be"},
		{name: "a three numbers", path: "/api/planner/relay?a=30.1,-97.1,4&b=30.2,-97.0", want: "a must be"},
		{name: "a NaN", path: "/api/planner/relay?a=NaN,-97.1&b=30.2,-97.0", want: "a must be"},
		{name: "b Inf", path: "/api/planner/relay?a=30.1,-97.1&b=30.2,+Inf", want: "b must be"},
		{name: "b range", path: "/api/planner/relay?a=30.1,-97.1&b=-90.5,-97.0", want: "b must be"},
		{name: "ha NaN", path: plannerRelayPath + "&ha=NaN", want: "ha must be"},
		{name: "hr Inf", path: plannerRelayPath + "&hr=Inf", want: "hr must be"},
		{name: "hb empty", path: plannerRelayPath + "&hb=", want: "hb must be"},
		{name: "relay range not in set", path: plannerRelayPath + "&range=25", want: "range must be one of"},
		{name: "relay range NaN", path: plannerRelayPath + "&range=NaN", want: "range must be one of"},
		{name: "relay too long", path: "/api/planner/relay?a=30.1,-97.1&b=31.5,-97.1", want: plannerErrTooLong},
		{name: "relay out of area", path: "/api/planner/relay?a=30.1,-97.1&b=30.9,-97.0", want: plannerErrOutOfArea},
		{name: "relay near hidden only", path: "/api/planner/relay?a=40.0,-80.05&b=40.05,-80.0", want: plannerErrOutOfArea},
		{name: "relay tiles", path: plannerRelayPath, want: plannerErrTooManyTiles, setup: manyTiles(13)},

		// coverage
		{name: "bbox missing", path: "/api/planner/coverage?sites=2", want: "bbox required"},
		{name: "bbox three", path: "/api/planner/coverage?bbox=30,-97.2,30.3", want: "bbox must be"},
		{name: "bbox NaN", path: "/api/planner/coverage?bbox=NaN,-97.2,30.3,-96.9", want: "bbox must be"},
		{name: "bbox Inf", path: "/api/planner/coverage?bbox=30,-97.2,Inf,-96.9", want: "bbox must be"},
		{name: "bbox lon range", path: "/api/planner/coverage?bbox=30,-181,30.3,-96.9", want: "bbox must be"},
		{name: "bbox inverted", path: "/api/planner/coverage?bbox=30.3,-97.2,30.0,-96.9", want: "south below north"},
		{name: "bbox empty", path: "/api/planner/coverage?bbox=30.3,-97.2,30.3,-96.9", want: "south below north"},
		{name: "bbox antimeridian", path: "/api/planner/coverage?bbox=50,179.8,50.3,-179.8", want: "antimeridian"},
		{name: "bbox north of srtm", path: "/api/planner/coverage?bbox=60.01,10,60.3,10.4", want: "latitudes"},
		{name: "bbox snaps north of srtm", path: "/api/planner/coverage?bbox=59.8,10,60.01,10.4", want: "latitudes"},
		{name: "bbox south of srtm", path: "/api/planner/coverage?bbox=-56.3,10,-56.01,10.4", want: "latitudes"},
		{name: "bbox too tall", path: "/api/planner/coverage?bbox=30.0,-97.2,31.0,-96.85", want: plannerErrAreaTooLarge},
		{name: "bbox too wide", path: "/api/planner/coverage?bbox=30.0,-97.6,30.35,-96.5", want: plannerErrAreaTooLarge},
		{name: "sites 0", path: plannerCovPath + "&sites=0", want: "sites must be"},
		{name: "sites 11", path: plannerCovPath + "&sites=11", want: "sites must be"},
		{name: "sites fraction", path: plannerCovPath + "&sites=2.5", want: "sites must be"},
		{name: "sites text", path: plannerCovPath + "&sites=x", want: "sites must be"},
		{name: "coverage range", path: plannerCovPath + "&range=25", want: "range must be one of"},
		{name: "coverage range Inf", path: plannerCovPath + "&range=Inf", want: "range must be one of"},
		{name: "hr NaN", path: plannerCovPath + "&hr=NaN", want: "hr and he"},
		{name: "he Inf", path: plannerCovPath + "&he=-Inf", want: "hr and he"},
		{name: "baseline", path: plannerCovPath + "&baseline=both", want: "baseline must be"},
		{name: "stale 2", path: plannerCovPath + "&stale=2", want: "stale must be"},
		{name: "stale true", path: plannerCovPath + "&stale=true", want: "stale must be"},
		{name: "no nodes", path: "/api/planner/coverage?bbox=10,10,10.3,10.3", want: plannerErrNoNodes},
		{name: "only hidden nodes", path: "/api/planner/coverage?bbox=39.9,-80.1,40.1,-79.9", want: plannerErrNoNodes},
		{name: "only blacklisted node", path: "/api/planner/coverage?bbox=40.9,-81.1,41.1,-80.9", want: plannerErrNoNodes},
		{name: "only companions", path: "/api/planner/coverage?bbox=31.4,-97.2,31.6,-97.0", want: plannerErrNoNodes},
		{name: "coverage tiles", path: plannerCovPath, want: plannerErrTooManyTiles, setup: manyTiles(13)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newPlannerHarness(t)
			if tc.setup != nil {
				tc.setup(h)
			}
			w := h.get(tc.path, "198.51.100.1")
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tc.want) {
				t.Fatalf("code=%d body=%s, want 400 containing %q", w.Code, w.Body.String(), tc.want)
			}
			if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
				t.Fatalf("Cache-Control=%q", cc)
			}
			if n := h.liveJobs(); n != 0 || h.tokens("198.51.100.1") != plannerIPJobsPerMin {
				t.Fatalf("a rejected request started a job (%d) or took a token", n)
			}
		})
	}
	// The exact tile limits are inclusive.
	for _, tc := range []struct {
		path string
		max  int
	}{{plannerProfPath, 12}, {plannerRelayPath, 12}, {plannerCovPath, 12}} {
		h := newPlannerHarness(t)
		h.src.set(func(f *fakeTerrain) { f.tiles = func(geoBBox) int { return tc.max } })
		if w := h.get(tc.path, "198.51.100.1"); w.Code != http.StatusAccepted && w.Code != http.StatusOK {
			t.Fatalf("%s at %d tiles: %d %s", tc.path, tc.max, w.Code, w.Body.String())
		}
	}
}

func TestPlannerAPIQuantisationOneJob(t *testing.T) {
	h := newPlannerHarness(t)
	h.stubs.hold()
	rng := rand.New(rand.NewSource(1))
	jitter := func(lo float64) float64 { return lo + 0.001 + rng.Float64()*0.048 }
	for i := 0; i < 100; i++ {
		path := fmt.Sprintf("/api/planner/coverage?bbox=%.6f,%.6f,%.6f,%.6f",
			jitter(30.0), jitter(-97.2), jitter(30.30), jitter(-96.90))
		if w := h.get(path, "198.51.100.1"); w.Code != http.StatusAccepted {
			t.Fatalf("request %d (%s): %d %s", i, path, w.Code, w.Body.String())
		}
	}
	if n := h.liveJobs(); n != 1 {
		t.Fatalf("100 jittered boxes made %d jobs, want 1", n)
	}
	if tok := h.tokens("198.51.100.1"); tok != plannerIPJobsPerMin-1 {
		t.Fatalf("tokens=%v: the client was charged more than once", tok)
	}
	h.waitEntered(t)
	h.stubs.mu.Lock()
	area := h.stubs.lastPlan.Area
	h.stubs.mu.Unlock()
	if area != (geoBBox{S: 30.0, W: -97.2, N: 30.35, E: -96.85}) {
		t.Fatalf("snapped area %+v", area)
	}
	// One cell further south is another key.
	if w := h.get("/api/planner/coverage?bbox=29.99,-97.19,30.31,-96.89", "198.51.100.1"); w.Code != http.StatusAccepted {
		t.Fatalf("neighbouring cell: %d", w.Code)
	}
	if n := h.liveJobs(); n != 2 {
		t.Fatalf("jobs=%d want 2", n)
	}
	// A value exactly on a grid line stays on it.
	if g, msg := plannerParseGridBox("30,-97.2,30.35,-96.85", defaultPlannerMaxAreaKm); msg != "" || g != (plannerGridBox{600, -1944, 607, -1937}) {
		t.Fatalf("on-grid box: %+v %q", g, msg)
	}
}

func TestPlannerAPIProtocol(t *testing.T) {
	h := newPlannerHarness(t)
	h.stubs.hold()
	h.stubs.set(func(st *plannerStubs) { st.progress = 0.5 })
	path := plannerCovPath + "&sites=2"
	ip := "198.51.100.7"

	w := h.get(path, ip)
	if w.Code != http.StatusAccepted {
		t.Fatalf("first: %d %s", w.Code, w.Body.String())
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("202 Cache-Control=%q", cc)
	}
	st := decodeStatus(t, w)
	if ra := w.Header().Get("Retry-After"); ra != strconv.Itoa(st.RetryAfterS) || st.RetryAfterS < 1 {
		t.Fatalf("Retry-After=%q body=%+v", ra, st)
	}
	if !slices.Contains([]string{"queued", "loading_terrain", "computing"}, st.State) {
		t.Fatalf("state %q", st.State)
	}

	h.waitEntered(t)
	w = h.get(path, ip)
	st = decodeStatus(t, w)
	if w.Code != http.StatusAccepted || st.State != "computing" || st.Progress != 0.5 || st.RetryAfterS != 1 ||
		w.Header().Get("Retry-After") != "1" {
		t.Fatalf("second: %d %+v RA=%q", w.Code, st, w.Header().Get("Retry-After"))
	}

	h.stubs.release()
	w = h.pollUntil(t, path, ip, http.StatusOK)
	if cc := w.Header().Get("Cache-Control"); cc != plannerCacheControl {
		t.Fatalf("200 Cache-Control=%q", cc)
	}
	var plan PlannerPlanResponse
	if err := json.Unmarshal(w.Body.Bytes(), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.Source != plannerSourceName || plan.Params.Sites != 2 || plan.ComputedAt.IsZero() ||
		plan.Area != (geoBBoxJSON{S: 30.0, W: -97.2, N: 30.35, E: -96.85}) {
		t.Fatalf("plan %+v", plan)
	}
	if !strings.Contains(w.Body.String(), `"warnings":[]`) || !strings.Contains(w.Body.String(), `"sites":[]`) {
		t.Fatalf("nil lists not normalised: %s", w.Body.String())
	}

	// Cache hits: same body, no new job, no token.
	before := w.Body.String()
	for i := 0; i < 10; i++ {
		w := h.get(path, ip)
		if w.Code != http.StatusOK || w.Body.String() != before {
			t.Fatalf("hit %d: %d", i, w.Code)
		}
	}
	if cov, _ := h.stubs.calls(); cov != 1 {
		t.Fatalf("searches=%d want 1", cov)
	}
	if tok := h.tokens(ip); tok != plannerIPJobsPerMin-1 {
		t.Fatalf("tokens=%v want %d", tok, plannerIPJobsPerMin-1)
	}
	// Results expire after the TTL; the next request is a new job.
	h.clk.add(plannerResultTTL)
	if w := h.get(path, ip); w.Code != http.StatusAccepted && w.Code != http.StatusOK {
		t.Fatalf("after TTL: %d", w.Code)
	}
	plannerWaitFor(t, "second search", func() bool { cov, _ := h.stubs.calls(); return cov == 2 })
}

func TestPlannerAPIFastPath(t *testing.T) {
	h := newPlannerHarness(t)
	h.p.fastWait = plannerFastWait
	ip := "198.51.100.8"

	w := h.get(plannerProfPath, ip)
	if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != plannerCacheControl {
		t.Fatalf("profile: %d %s", w.Code, w.Body.String())
	}
	var prof PlannerProfileResponse
	if err := json.Unmarshal(w.Body.Bytes(), &prof); err != nil {
		t.Fatal(err)
	}
	d := geoDistKm(geoPoint{30.1, -97.1}, geoPoint{30.2, -97.0})
	if prof.Source != plannerSourceName || len(prof.Legs) != 1 || prof.Legs[0].Error != "" ||
		len(prof.Legs[0].ElevM) != planProfileSamples(d) || prof.Legs[0].DistanceKm != losRound(d, 1000) {
		t.Fatalf("profile %+v", prof)
	}
	if e0 := prof.Legs[0].ElevM[0]; e0 != losRound(100+3*30.1, 10) {
		t.Fatalf("first sample %v", e0)
	}

	if w := h.get(plannerRelayPath, ip); w.Code != http.StatusOK {
		t.Fatalf("relay: %d %s", w.Code, w.Body.String())
	}
	if w := h.get(plannerCovPath, ip); w.Code != http.StatusOK {
		t.Fatalf("coverage: %d %s", w.Code, w.Body.String())
	}

	// Terrain still to download: no point waiting, answer 202 at once.
	gate := make(chan struct{})
	h.src.set(func(f *fakeTerrain) { f.resident = false; f.gate = gate })
	path := "/api/planner/profile?pts=30.15,-97.1,30.2,-97.0"
	start := time.Now()
	w = h.get(path, ip)
	if w.Code != http.StatusAccepted || time.Since(start) > time.Second {
		t.Fatalf("non-resident: %d after %v", w.Code, time.Since(start))
	}
	plannerWaitFor(t, "loading_terrain", func() bool {
		w := h.get(path, ip)
		return decodeStatus(t, w).State == "loading_terrain" && w.Header().Get("Retry-After") == "2"
	})
	close(gate)
	h.pollUntil(t, path, ip, http.StatusOK)
}

func TestPlannerAPIProfileLegs(t *testing.T) {
	h := newPlannerHarness(t)
	h.p.fastWait = plannerFastWait
	// ok, out_of_area (30.9,-97.0 is >50 km from every visible node),
	// out_of_area, too_long.
	path := "/api/planner/profile?pts=30.1,-97.1,30.2,-97.0,30.9,-97.0,30.15,-96.95,31.5,-97.1"
	w := h.get(path, "198.51.100.9")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var prof PlannerProfileResponse
	json.Unmarshal(w.Body.Bytes(), &prof)
	var codes []string
	for _, l := range prof.Legs {
		codes = append(codes, l.Error)
		if (l.Error == "") != (len(l.ElevM) > 0) {
			t.Fatalf("leg %+v", l)
		}
	}
	if want := []string{"", plannerErrOutOfArea, plannerErrOutOfArea, plannerErrTooLong}; !slices.Equal(codes, want) {
		t.Fatalf("codes %q want %q", codes, want)
	}
	// One view over the valid legs only, padded by 0.01°.
	bb := h.src.lastBBox()
	want := geoBBox{S: 30.09, W: -97.11, N: 30.21, E: -96.99}
	if math.Abs(bb.S-want.S) > 1e-9 || math.Abs(bb.W-want.W) > 1e-9 || math.Abs(bb.N-want.N) > 1e-9 || math.Abs(bb.E-want.E) > 1e-9 {
		t.Fatalf("view %+v want %+v", bb, want)
	}
	// No valid leg: answered in the request, no job, no terrain, no token.
	views := h.src.viewCount()
	w = h.get("/api/planner/profile?pts=30.1,-97.1,31.5,-97.1,40.0,-80.0,40.1,-80.0", "198.51.100.10")
	json.Unmarshal(w.Body.Bytes(), &prof)
	if w.Code != http.StatusOK || len(prof.Legs) != 3 || prof.Legs[0].Error != plannerErrTooLong ||
		prof.Legs[2].Error != plannerErrOutOfArea || h.src.viewCount() != views || h.tokens("198.51.100.10") != plannerIPJobsPerMin {
		t.Fatalf("all-error profile: %d %s", w.Code, w.Body.String())
	}
	// A leg the terrain cannot cover is no_terrain, not a failure.
	legs, err := planProfileLegs(context.Background(), fakeTerrainView{bb: geoBBox{S: 0, W: 0, N: 1, E: 1}},
		[]geoPoint{{30.1, -97.1}, {30.2, -97.0}}, []PlannerProfileLeg{{DistanceKm: 1}}, func(float64) {})
	if err != nil || legs.Legs[0].Error != plannerErrNoTerrain {
		t.Fatalf("no_terrain: %+v %v", legs, err)
	}
}

func TestPlannerAPIRelayParams(t *testing.T) {
	h := newPlannerHarness(t)
	h.p.fastWait = plannerFastWait
	w := h.get(plannerRelayPath+"&ha=2.6&hb=150&hr=-3&range=20", "198.51.100.11")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	h.stubs.mu.Lock()
	got, nodes := h.stubs.lastRelay, h.stubs.relayNodes
	h.stubs.mu.Unlock()
	radio, _ := (*LOSConfig)(nil).resolved()
	want := PlannerRelayParams{
		A: geoPointJSON{30.1, -97.1}, B: geoPointJSON{30.2, -97.0},
		AntennaAM: 3, AntennaBM: 100, RelayAntennaM: 1,
		ExistingAntennaM: defaultPlannerExistAntenna, RangeKm: 20,
		FrequencyMHz: radio.FrequencyMHz, KFactor: radio.KFactor,
	}
	if got != want {
		t.Fatalf("params %+v\nwant   %+v", got, want)
	}
	var resp PlannerRelayResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Params != want || resp.Source != plannerSourceName || resp.Open == nil || resp.Warnings == nil {
		t.Fatalf("response %+v", resp)
	}
	// Nodes: in the corridor, visible, repeaters/rooms only.
	var keys []string
	for _, n := range nodes {
		keys = append(keys, n.PubKey)
	}
	if !slices.Contains(keys, "aa01") || !slices.Contains(keys, "aa02") {
		t.Fatalf("relay nodes %v", keys)
	}
	for _, bad := range []string{"bad1", "hid1", "cc01", "nogps"} {
		if slices.Contains(keys, bad) || strings.Contains(w.Body.String(), bad) {
			t.Fatalf("relay exposed %s: %v", bad, keys)
		}
	}
	// Corridor: A,B ± max(0.3·D, 2 km).
	d := geoDistKm(geoPoint{30.1, -97.1}, geoPoint{30.2, -97.0})
	wantView := plannerExpandBBox(geoBBox{S: 30.1, W: -97.1, N: 30.2, E: -97.0}, 0.3*d)
	if h.src.lastBBox() != wantView {
		t.Fatalf("view %+v want %+v", h.src.lastBBox(), wantView)
	}
	// Defaults, and the 2 km floor on a short hop.
	w = h.get("/api/planner/relay?a=30.1,-97.1&b=30.101,-97.1", "198.51.100.11")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	h.stubs.mu.Lock()
	got = h.stubs.lastRelay
	h.stubs.mu.Unlock()
	if got.AntennaAM != defaultPlannerNewAntennaM || got.AntennaBM != defaultPlannerNewAntennaM ||
		got.RelayAntennaM != defaultPlannerNewAntennaM || got.RangeKm != defaultPlannerRangeKm {
		t.Fatalf("defaults %+v", got)
	}
	if v := h.src.lastBBox(); v != plannerExpandBBox(geoBBox{S: 30.1, W: -97.1, N: 30.101, E: -97.1}, plannerRelayPadMinKm) {
		t.Fatalf("short-hop view %+v", v)
	}
}

func TestPlannerAPICoverageInputs(t *testing.T) {
	h := newPlannerHarness(t)
	h.p.fastWait = plannerFastWait
	h.data.mu.Lock()
	h.data.es = []NeighborEdge{
		{NodeA: "aa01", NodeB: "aa02", Count: 100, LastSeen: plannerTestStart,
			Observers: map[string]bool{"o1": true, "o2": true, "o3": true}, CountsByMode: map[int]int{2: 100}},
		{NodeA: "aa01", NodeB: "bad1", Count: 100, LastSeen: plannerTestStart,
			Observers: map[string]bool{"o1": true, "o2": true, "o3": true}, CountsByMode: map[int]int{2: 100}},
	}
	h.data.mu.Unlock()
	w := h.get(plannerCovPath+"&sites=4&range=20&hr=12.4&he=7&baseline=observed&stale=1", "198.51.100.12")
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	h.stubs.mu.Lock()
	in := h.stubs.lastPlan
	h.stubs.mu.Unlock()
	radio, _ := (*LOSConfig)(nil).resolved()
	wantParams := PlannerPlanParams{Sites: 4, RangeKm: 20, NewAntennaM: 12, ExistingAntennaM: 7,
		Baseline: "observed", IncludeStale: true, FrequencyMHz: radio.FrequencyMHz, KFactor: radio.KFactor,
		MinClearance: plannerMinClearance}
	if in.Params != wantParams {
		t.Fatalf("params %+v\nwant   %+v", in.Params, wantParams)
	}
	area := geoBBox{S: 30.0, W: -97.2, N: 30.35, E: -96.85}
	if in.Area != area || in.Truncated {
		t.Fatalf("area %+v truncated %v", in.Area, in.Truncated)
	}
	var keys []string
	for _, n := range in.Nodes {
		keys = append(keys, n.PubKey)
	}
	if !slices.Equal(keys, []string{"aa01", "aa02", "aa03"}) {
		t.Fatalf("nodes %v", keys)
	}
	if len(in.Observed) != 1 || in.Observed[0].Class != "observed" {
		t.Fatalf("observed %+v", in.Observed)
	}
	if v := h.src.lastBBox(); v != plannerExpandBBox(area, defaultPlannerNodeMarginKm) {
		t.Fatalf("view %+v", v)
	}
	// Defaults.
	w = h.get(plannerCovPath, "198.51.100.12")
	if w.Code != http.StatusOK {
		t.Fatalf("%d", w.Code)
	}
	h.stubs.mu.Lock()
	in = h.stubs.lastPlan
	h.stubs.mu.Unlock()
	if p := in.Params; p.Sites != plannerDefaultSites || p.RangeKm != defaultPlannerRangeKm || p.Baseline != "predicted" ||
		p.IncludeStale || p.NewAntennaM != defaultPlannerNewAntennaM || p.ExistingAntennaM != defaultPlannerExistAntenna {
		t.Fatalf("defaults %+v", p)
	}
}

func TestPlannerAPIPerIPLimit(t *testing.T) {
	h := newPlannerHarness(t)
	h.p.fastWait = plannerFastWait
	ip := "198.51.100.20"
	path := func(i int) string {
		return fmt.Sprintf("/api/planner/profile?pts=%.3f,-97.1,30.2,-97.0", 30.1+float64(i)/1000)
	}
	for i := 0; i < plannerIPJobsPerMin; i++ {
		if w := h.get(path(i), ip); w.Code != http.StatusOK {
			t.Fatalf("job %d: %d %s", i, w.Code, w.Body.String())
		}
	}
	w := h.get(path(99), ip)
	if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "rate limited") {
		t.Fatalf("7th: %d %s", w.Code, w.Body.String())
	}
	if ra, _ := strconv.Atoi(w.Header().Get("Retry-After")); ra != 10 {
		t.Fatalf("Retry-After=%q want 10", w.Header().Get("Retry-After"))
	}
	if h.liveJobs() != 0 {
		t.Fatal("a limited request started a job")
	}
	// Cached results stay free; other clients have their own budget.
	if w := h.get(path(0), ip); w.Code != http.StatusOK {
		t.Fatalf("cache hit limited: %d", w.Code)
	}
	if w := h.get(path(99), "198.51.100.21"); w.Code != http.StatusOK {
		t.Fatalf("other client: %d", w.Code)
	}
	h.clk.add(10 * time.Second)
	if w := h.get(path(98), ip); w.Code != http.StatusOK {
		t.Fatalf("after refill: %d", w.Code)
	}
}

func TestPlannerAPIGlobalCap(t *testing.T) {
	h := newPlannerHarness(t)
	h.p.fastWait = plannerFastWait
	n := 0
	for c := 0; n < plannerGlobalJobsPerMin; c++ {
		ip := fmt.Sprintf("198.51.100.%d", 30+c)
		for i := 0; i < plannerIPJobsPerMin && n < plannerGlobalJobsPerMin; i++ {
			path := fmt.Sprintf("/api/planner/profile?pts=%.3f,-97.1,30.2,-97.0", 30.1+float64(n)/1000)
			if w := h.get(path, ip); w.Code != http.StatusOK {
				t.Fatalf("job %d: %d %s", n, w.Code, w.Body.String())
			}
			n++
		}
	}
	w := h.get("/api/planner/profile?pts=30.15,-97.15,30.2,-97.0", "198.51.100.99")
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "planner busy") {
		t.Fatalf("31st: %d %s", w.Code, w.Body.String())
	}
	if ra, _ := strconv.Atoi(w.Header().Get("Retry-After")); ra < 1 || ra > 60 {
		t.Fatalf("Retry-After=%q", w.Header().Get("Retry-After"))
	}
	// The server being busy is not the client's fault: its token comes back.
	if tok := h.tokens("198.51.100.99"); tok != plannerIPJobsPerMin {
		t.Fatalf("refused client was charged: tokens %v", tok)
	}
}

func TestPlannerAPICoverageQueueFull(t *testing.T) {
	h := newPlannerHarness(t)
	h.stubs.hold()
	ip := "198.51.100.40"
	cov := func(sites int) string { return fmt.Sprintf("%s&sites=%d", plannerCovPath, sites) }
	if w := h.get(cov(1), ip); w.Code != http.StatusAccepted {
		t.Fatalf("running job: %d", w.Code)
	}
	h.waitEntered(t)
	for s := 2; s <= 1+plannerMaxQueuedCoverage; s++ {
		w := h.get(cov(s), ip)
		if w.Code != http.StatusAccepted || decodeStatus(t, w).State != "queued" {
			t.Fatalf("queued job %d: %d %s", s, w.Code, w.Body.String())
		}
	}
	other := "198.51.100.41"
	w := h.get(cov(9), other)
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "5" {
		t.Fatalf("overflow: %d RA=%q %s", w.Code, w.Header().Get("Retry-After"), w.Body.String())
	}
	if h.tokens(other) != plannerIPJobsPerMin {
		t.Fatal("a refused job charged the client")
	}
	// Polling a queued job is still fine.
	if w := h.get(cov(3), ip); w.Code != http.StatusAccepted {
		t.Fatalf("poll of queued job: %d", w.Code)
	}
	// Cheap jobs are not stuck behind coverage.
	h.p.fastWait = plannerFastWait
	if w := h.get(plannerProfPath, other); w.Code != http.StatusOK {
		t.Fatalf("profile behind coverage: %d", w.Code)
	}
	// Draining the queue runs every job once.
	h.stubs.release()
	plannerWaitFor(t, "queue to drain", func() bool { return h.liveJobs() == 0 })
	if c, _ := h.stubs.calls(); c != 1+plannerMaxQueuedCoverage {
		t.Fatalf("searches=%d", c)
	}
	h.p.mu.Lock()
	q := h.p.covQueued
	h.p.mu.Unlock()
	if q != 0 {
		t.Fatalf("covQueued=%d after drain", q)
	}
}

func TestPlannerAPILiveJobCap(t *testing.T) {
	h := newPlannerHarness(t)
	h.stubs.hold()
	covIP, relayIP := "198.51.100.50", "198.51.100.51"
	if w := h.get(plannerCovPath+"&sites=1", covIP); w.Code != http.StatusAccepted {
		t.Fatalf("coverage: %d", w.Code)
	}
	h.waitEntered(t)
	for s := 2; s <= 5; s++ {
		if w := h.get(fmt.Sprintf("%s&sites=%d", plannerCovPath, s), covIP); w.Code != http.StatusAccepted {
			t.Fatalf("coverage %d: %d", s, w.Code)
		}
	}
	for hr := 1; hr <= plannerMaxLiveJobs-5; hr++ {
		if w := h.get(fmt.Sprintf("%s&hr=%d", plannerRelayPath, hr), relayIP); w.Code != http.StatusAccepted {
			t.Fatalf("relay %d: %d", hr, w.Code)
		}
	}
	if n := h.liveJobs(); n != plannerMaxLiveJobs {
		t.Fatalf("live=%d", n)
	}
	w := h.get(plannerRelayPath+"&hr=50", relayIP)
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "5" {
		t.Fatalf("9th job: %d RA=%q", w.Code, w.Header().Get("Retry-After"))
	}
}

func TestPlannerAPIAbandonedJobCancelled(t *testing.T) {
	h := newPlannerHarness(t)
	h.stubs.hold()
	// Two jobs an hour, so the clock moving below refills nothing useful.
	h.p.limit.rl = newRateLimiter(2, time.Hour)
	h.p.limit.rl.now = h.clk.now
	ip := "198.51.100.60"
	if w := h.get(plannerCovPath, ip); w.Code != http.StatusAccepted {
		t.Fatalf("%d", w.Code)
	}
	h.waitEntered(t)
	// Polls keep it alive.
	h.clk.add(10 * time.Second)
	if w := h.get(plannerCovPath, ip); w.Code != http.StatusAccepted {
		t.Fatalf("poll: %d", w.Code)
	}
	h.clk.add(10 * time.Second)
	time.Sleep(20 * time.Millisecond) // several watch ticks
	if h.liveJobs() != 1 {
		t.Fatal("a polled job was cancelled")
	}
	// 16 s without a poll: cancelled and removed.
	h.clk.add(6 * time.Second)
	plannerWaitFor(t, "abandoned job to go", func() bool { return h.liveJobs() == 0 })
	h.stubs.mu.Lock()
	cause := h.stubs.cause
	h.stubs.mu.Unlock()
	if !errors.Is(cause, errPlannerAbandoned) {
		t.Fatalf("search saw cause %v", cause)
	}
	if len(h.p.covSlots) != 0 {
		t.Fatal("slot not released")
	}
	// The next poll starts afresh, and pays for it.
	if w := h.get(plannerCovPath, ip); w.Code != http.StatusAccepted {
		t.Fatalf("restart: %d", w.Code)
	}
	plannerWaitFor(t, "second search", func() bool { c, _ := h.stubs.calls(); return c == 2 })
	if tok := h.tokens(ip); tok >= 1 {
		t.Fatalf("restart was free (tokens %v)", tok)
	}
}

func TestPlannerAPIPanicRecovered(t *testing.T) {
	h := newPlannerHarness(t)
	h.p.fastWait = plannerFastWait
	h.stubs.set(func(st *plannerStubs) { st.panicMsg = "boom" })
	for _, path := range []string{plannerCovPath, plannerRelayPath} {
		w := h.get(path, "198.51.100.70")
		if w.Code != http.StatusInternalServerError || !strings.Contains(w.Body.String(), "planner failed") ||
			strings.Contains(w.Body.String(), "boom") {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if h.liveJobs() != 0 || len(h.p.covSlots) != 0 || len(h.p.cheapSlots) != 0 {
		t.Fatal("panicked job left state behind")
	}
	// The crashing query is not replayed for plannerFailedFor...
	h.stubs.set(func(st *plannerStubs) { st.panicMsg = "" })
	for _, path := range []string{plannerCovPath, plannerRelayPath} {
		w := h.get(path, "198.51.100.70")
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), plannerMsgFailedRecent) {
			t.Fatalf("right after panic %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if cov, relay := h.stubs.calls(); cov != 1 || relay != 1 {
		t.Fatalf("searches re-ran: %d %d", cov, relay)
	}
	// ...the server still serves everything else...
	if w := h.get(plannerCovPath+"&sites=2", "198.51.100.70"); w.Code != http.StatusOK {
		t.Fatalf("other key after panic: %d %s", w.Code, w.Body.String())
	}
	// ...and the key itself once the window has passed.
	h.clk.add(plannerFailedFor)
	for _, path := range []string{plannerCovPath, plannerRelayPath} {
		if w := h.get(path, "198.51.100.71"); w.Code != http.StatusOK {
			t.Fatalf("after the window %s: %d %s", path, w.Code, w.Body.String())
		}
	}
}

func TestPlannerAPITerrainBusy(t *testing.T) {
	h := newPlannerHarness(t)
	h.src.set(func(f *fakeTerrain) { f.resident = false; f.err = errTerrainBusy })
	ip := "198.51.100.80"
	h.get(plannerCovPath, ip)
	w := h.pollUntil(t, plannerCovPath, ip, http.StatusServiceUnavailable)
	if w.Header().Get("Retry-After") != "30" || !strings.Contains(w.Body.String(), "terrain busy") {
		t.Fatalf("busy: RA=%q %s", w.Header().Get("Retry-After"), w.Body.String())
	}
	if v := h.src.viewCount(); v != 1 {
		t.Fatalf("views=%d", v)
	}
	// Within 30 s: the same answer without a new job or token.
	h.clk.add(20 * time.Second)
	w = h.get(plannerCovPath, ip)
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "10" || h.src.viewCount() != 1 ||
		h.liveJobs() != 0 {
		t.Fatalf("busy window: %d RA=%q views=%d", w.Code, w.Header().Get("Retry-After"), h.src.viewCount())
	}
	// After it, the next poll restarts.
	h.clk.add(11 * time.Second)
	h.src.set(func(f *fakeTerrain) { f.err = nil })
	h.pollUntil(t, plannerCovPath, ip, http.StatusOK)
	if v := h.src.viewCount(); v != 2 {
		t.Fatalf("views=%d want 2", v)
	}
	// Other terrain failures are remembered for plannerFailedFor instead.
	h.p.fastWait = plannerFastWait
	h.src.set(func(f *fakeTerrain) { f.resident = true; f.err = errTerrainUnavailable })
	path := plannerCovPath + "&sites=5"
	w = h.get(path, ip)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "terrain unavailable") ||
		w.Header().Get("Retry-After") != "300" {
		t.Fatalf("unavailable: %d RA=%q %s", w.Code, w.Header().Get("Retry-After"), w.Body.String())
	}
	h.src.set(func(f *fakeTerrain) { f.err = nil })
	views := h.src.viewCount()
	if w := h.get(path, ip); w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), plannerMsgFailedRecent) ||
		h.src.viewCount() != views {
		t.Fatalf("retry inside the window: %d %s", w.Code, w.Body.String())
	}
	h.clk.add(plannerFailedFor)
	if w := h.get(path, ip); w.Code != http.StatusOK {
		t.Fatalf("retry after the window: %d", w.Code)
	}
}

func TestPlannerAPIExposure(t *testing.T) {
	h := newPlannerHarness(t)
	h.p.fastWait = plannerFastWait
	ip := "198.51.100.90"
	w := h.get(plannerCovPath, ip)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	var plan PlannerPlanResponse
	json.Unmarshal(w.Body.Bytes(), &plan)
	var keys []string
	for _, n := range plan.Nodes {
		keys = append(keys, n.PubKey)
	}
	if !slices.Equal(keys, []string{"aa01", "aa02", "aa03"}) {
		t.Fatalf("nodes %v", keys)
	}
	for _, hidden := range []string{"bad1", "hid1", "Banned", "zz-secret"} {
		if strings.Contains(w.Body.String(), hidden) {
			t.Fatalf("response exposes %q", hidden)
		}
	}
	// A profile point near only hidden nodes is out of area.
	w = h.get("/api/planner/profile?pts=40.0,-80.05,40.05,-80.0", ip)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), plannerErrOutOfArea) {
		t.Fatalf("profile near hidden node: %d %s", w.Code, w.Body.String())
	}
	// Blacklisting at runtime: the cached plan is not served again.
	h.srv.cfg.SetNodeBlacklist([]string{"bad1", "bad2", "aa01"})
	w = h.get(plannerCovPath, ip)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "aa01") {
		t.Fatalf("after blacklisting aa01: %d %s", w.Code, w.Body.String())
	}
	if c, _ := h.stubs.calls(); c != 2 {
		t.Fatalf("searches=%d want 2", c)
	}
}

func TestPlannerAPIFingerprint(t *testing.T) {
	area := geoBBox{S: 30.0, W: -97.2, N: 30.35, E: -96.85}
	f := plannerNodeFilter{}
	fp := func(ns []nodeInfo) string {
		_, s := buildPlannerKnown(ns, f, "", plannerTestStart).scan(area, plannerAreaNodeKm, defaultPlannerNodeMarginKm)
		return s
	}
	base := plannerTestNodes()
	ref := fp(base)
	moved := plannerTestNodes()
	moved[0].Lat += 0.01
	if fp(moved) == ref {
		t.Fatal("moving an in-area repeater kept the fingerprint")
	}
	jitter := plannerTestNodes()
	jitter[0].Lat += 0.0001
	if fp(jitter) != ref {
		t.Fatal("sub-quantum jitter changed the fingerprint")
	}
	shuffled := plannerTestNodes()
	slices.Reverse(shuffled)
	if fp(shuffled) != ref {
		t.Fatal("node order changed the fingerprint")
	}
	far := append(plannerTestNodes(), nodeInfo{PublicKey: "far1", Role: "repeater", Lat: 35, Lon: -90, HasGPS: true})
	if fp(far) != ref {
		t.Fatal("a repeater far outside the area changed the fingerprint")
	}
	phone := append(plannerTestNodes(), nodeInfo{PublicKey: "cc09", Role: "companion", Lat: 30.1, Lon: -97.0, HasGPS: true})
	if fp(phone) != ref {
		t.Fatal("a companion changed the fingerprint")
	}

	// End to end: the known set refreshes after 60 s and the moved node
	// makes a new key.
	h := newPlannerHarness(t)
	h.p.fastWait = plannerFastWait
	ip := "198.51.100.95"
	if w := h.get(plannerCovPath, ip); w.Code != http.StatusOK {
		t.Fatalf("%d", w.Code)
	}
	h.data.setNodes(moved)
	if w := h.get(plannerCovPath, ip); w.Code != http.StatusOK {
		t.Fatalf("%d", w.Code)
	}
	if c, _ := h.stubs.calls(); c != 1 {
		t.Fatalf("known set rebuilt early (searches=%d)", c)
	}
	h.clk.add(plannerKnownTTL + time.Second)
	if w := h.get(plannerCovPath, ip); w.Code != http.StatusOK {
		t.Fatalf("%d", w.Code)
	}
	if c, _ := h.stubs.calls(); c != 2 {
		t.Fatalf("moved node did not change the key (searches=%d)", c)
	}
}

func TestPlannerAPIDefaultDataSource(t *testing.T) {
	cfg := &Config{Planner: &PlannerConfig{Enabled: true}, LOS: &LOSConfig{FrequencyMHz: 915, KFactor: 1.2}}
	srv := &Server{cfg: cfg}
	p := newPlannerService(PlannerConfig{Enabled: true}, srv)
	t.Cleanup(p.Close)
	srv.planner = p
	if p.freqMHz != 915 || p.kFactor != 1.2 {
		t.Fatalf("radio %v %v", p.freqMHz, p.kFactor)
	}
	if _, ok := p.src.(*srtmSource); !ok {
		t.Fatalf("terrain source %T", p.src)
	}
	if p.cfg.DefaultRangeKm != defaultPlannerRangeKm || p.cfg.NewAntennaM != defaultPlannerNewAntennaM {
		t.Fatalf("config not resolved: %+v", p.cfg)
	}
	h := &plannerHarness{srv: srv}
	// No store: no known nodes, so nothing reaches terrain or compute.
	if w := h.get(plannerCovPath, "198.51.100.96"); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), plannerErrNoNodes) {
		t.Fatalf("coverage: %d %s", w.Code, w.Body.String())
	}
	if w := h.get(plannerRelayPath, "198.51.100.96"); w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), plannerErrOutOfArea) {
		t.Fatalf("relay: %d %s", w.Code, w.Body.String())
	}
	if w := h.get(plannerProfPath, "198.51.100.96"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), plannerErrOutOfArea) {
		t.Fatalf("profile: %d %s", w.Code, w.Body.String())
	}
	d := serverPlannerData{srv}
	if d.nodes() != nil || d.lastHeard([]string{"x"}) != nil || len(d.edges([]string{"x"})) != 0 {
		t.Fatal("storeless data source returned data")
	}
	fl := d.filter()
	if fl.SilentAfter("repeater") != 72*time.Hour || fl.SilentAfter("companion") != 24*time.Hour {
		t.Fatalf("silent windows %v %v", fl.SilentAfter("repeater"), fl.SilentAfter("companion"))
	}
	gen := d.filterGen()
	cfg.SetHiddenNamePrefixes([]string{"x-"})
	if d.filterGen() == gen {
		t.Fatal("filterGen ignores hidden-prefix changes")
	}
}

func TestPlannerAPIResultCacheBounds(t *testing.T) {
	now := plannerTestStart
	body := func(n int) []byte { return make([]byte, n) }

	c := newPlannerLRU(3, 1<<20, time.Minute)
	for _, k := range []string{"a", "b", "c"} {
		c.put(k, body(10), now)
	}
	c.get("a", now) // a is now most recent; b is the eviction candidate
	c.put("d", body(10), now)
	if _, ok := c.get("b", now); ok || c.len() != 3 {
		t.Fatalf("item cap: len=%d", c.len())
	}
	if _, ok := c.get("a", now); !ok {
		t.Fatal("recently used entry evicted")
	}
	if _, ok := c.get("a", now.Add(time.Minute)); ok || c.len() != 2 {
		t.Fatal("expired entry served")
	}

	c = newPlannerLRU(100, 1000, time.Minute)
	for i := 0; i < 4; i++ {
		c.put(strconv.Itoa(i), body(240), now) // 241 bytes each
	}
	c.put("4", body(240), now)
	if _, ok := c.get("0", now); ok || c.len() != 4 || c.size > 1000 {
		t.Fatalf("byte cap: len=%d size=%d", c.len(), c.size)
	}
	c.put("big", body(300), now) // over a quarter of the budget
	if _, ok := c.get("big", now); ok {
		t.Fatal("oversized entry cached")
	}
	c.put("1", body(10), now) // replacing an entry adjusts the size
	if c.size != 241*3+11 {
		t.Fatalf("size=%d", c.size)
	}
}

func TestPlannerAPICloseStopsJobs(t *testing.T) {
	h := newPlannerHarness(t)
	h.stubs.hold()
	h.get(plannerCovPath, "198.51.100.97")
	h.waitEntered(t)
	done := make(chan struct{})
	go func() { h.p.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not stop the running job")
	}
	if w := h.get(plannerCovPath+"&sites=2", "198.51.100.97"); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("after Close: %d", w.Code)
	}
}

// ─── Shared limiter ─────────────────────────────────────────────────────────

func TestClientLimitPerClientAndShared(t *testing.T) {
	req := func(remote, xff string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}
	off := &clientLimiter{}
	for i := 0; i < 50; i++ {
		if ok, _ := off.take(req("203.0.113.1:1", "")); !ok {
			t.Fatal("limiter without buckets limited")
		}
	}

	clk := &plannerTestClock{t: plannerTestStart}
	c := &clientLimiter{ipr: &wsLimiter{state: map[string]*wsIPState{}}, rl: newRateLimiter(2, time.Minute), tag: "test", what: "test"}
	c.rl.now = clk.now
	for i := 0; i < 2; i++ {
		if !c.allow(httptest.NewRecorder(), req("203.0.113.1:1", "")) {
			t.Fatalf("request %d limited", i)
		}
	}
	w := httptest.NewRecorder()
	if c.allow(w, req("203.0.113.1:2", "")) || w.Code != http.StatusTooManyRequests ||
		w.Header().Get("Retry-After") != "30" || !strings.Contains(w.Body.String(), "rate limited") {
		t.Fatalf("3rd: %d RA=%q %s", w.Code, w.Header().Get("Retry-After"), w.Body.String())
	}
	if !c.allow(httptest.NewRecorder(), req("203.0.113.2:1", "")) {
		t.Fatal("another client shares the bucket")
	}
	// Behind an untrusted local proxy, X-Forwarded-For is ignored and
	// everyone shares one bucket.
	if k := c.clientKey(req("127.0.0.1:1", "198.51.100.1")); k != clientSharedKey {
		t.Fatalf("indistinct key %q", k)
	}
	c.ipr.trustedProxies = parseCIDRList([]string{"127.0.0.1"}, "test")
	if k := c.clientKey(req("127.0.0.1:1", "198.51.100.1")); k != "198.51.100.1" {
		t.Fatalf("trusted proxy key %q", k)
	}
	if retryAfterSecs(0) != 1 || retryAfterSecs(1500*time.Millisecond) != 2 {
		t.Fatal("retryAfterSecs")
	}
}

// ─── Edge snapshots ─────────────────────────────────────────────────────────

func TestSnapshotEdgesDeepCopy(t *testing.T) {
	g := NewNeighborGraph()
	snr := 5.0
	g.upsertEdge("aa", "bb", "aabb", "obs1", &snr, plannerTestStart)
	amb := &NeighborEdge{NodeA: "cc", Prefix: "dd", Ambiguous: true, Candidates: []string{"d1", "d2"},
		Observers: map[string]bool{"o": true}, CountsByMode: map[int]int{1: 2}}
	g.mu.Lock()
	g.edges[makeEdgeKey("cc", "dd?")] = amb
	g.mu.Unlock()

	snap := g.SnapshotEdges()
	if len(snap) != 2 {
		t.Fatalf("len=%d", len(snap))
	}
	var ab, cc *NeighborEdge
	for i := range snap {
		if snap[i].NodeA == "aa" {
			ab = &snap[i]
		} else {
			cc = &snap[i]
		}
	}
	// The graph moving on does not reach the copy.
	g.upsertEdge("aa", "bb", "aabb", "obs2", &snr, plannerTestStart.Add(time.Minute))
	if ab.Count != 1 || len(ab.Observers) != 1 || ab.CountsByMode[2] != 1 || !ab.LastSeen.Equal(plannerTestStart) {
		t.Fatalf("copy changed with the graph: %+v", ab)
	}
	// Changing the copy does not reach the graph.
	cc.Candidates[0] = "x"
	cc.Observers["evil"] = true
	cc.CountsByMode[9] = 9
	if amb.Candidates[0] != "d1" || len(amb.Observers) != 1 || len(amb.CountsByMode) != 1 {
		t.Fatalf("graph changed with the copy: %+v", amb)
	}
	// Among: resolved edges with both ends in the set, case-insensitively.
	among := g.snapshotEdgesAmong([]string{"AA", "bb", "cc"})
	if len(among) != 1 || among[0].NodeA != "aa" || among[0].Count != 2 {
		t.Fatalf("among %+v", among)
	}
	if len(g.snapshotEdgesAmong([]string{"aa"})) != 0 {
		t.Fatal("edge with one end outside the set")
	}
	var nilGraph *NeighborGraph
	if nilGraph.SnapshotEdges() != nil {
		t.Fatal("nil graph")
	}
}

func TestSnapshotEdgesConcurrentUpserts(t *testing.T) {
	g := NewNeighborGraph()
	var running atomic.Int32
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		running.Add(1)
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			defer running.Add(-1)
			snr := float64(w)
			for i := 0; i < 3000; i++ {
				g.upsertEdge(fmt.Sprintf("n%02d", i%13), fmt.Sprintf("m%02d", (i+w)%11), "aabb",
					fmt.Sprintf("obs%d", i%7), &snr, plannerTestStart.Add(time.Duration(i)*time.Second))
			}
		}(w)
	}
	reads := 0
	for running.Load() > 0 || reads == 0 {
		for _, list := range [][]NeighborEdge{g.SnapshotEdges(), g.snapshotEdgesAmong([]string{"n01", "m02", "n03", "m04"})} {
			for _, e := range list {
				for o := range e.Observers {
					_ = o
				}
				for m, c := range e.CountsByMode {
					_ = m + c
				}
				// Writing the copy must not race with ingest writing the graph.
				e.Observers["reader"] = true
				e.CountsByMode[0]++
				_ = e.Count + e.SNRCount
			}
		}
		reads++
	}
	wg.Wait()
	for _, e := range g.AllEdges() {
		if e.Observers["reader"] {
			t.Fatal("a snapshot write reached the graph")
		}
	}
}

// ─── Failure memory, terrain order, missing terrain ─────────────────────────

func TestPlannerAPIFailedJobRemembered(t *testing.T) {
	h := newPlannerHarness(t)
	h.p.computeTimeout = 30 * time.Millisecond
	var sleep atomic.Int64
	sleep.Store(int64(150 * time.Millisecond))
	var calls atomic.Int32
	// Ignores its context: sleeps past the deadline, then "succeeds".
	h.p.coverage = func(ctx context.Context, v terrainView, in planInputs, progress func(float64)) (PlannerPlanResponse, error) {
		calls.Add(1)
		time.Sleep(time.Duration(sleep.Load()))
		return PlannerPlanResponse{Status: "connected"}, nil
	}
	ip := "198.51.100.110"
	if w := h.get(plannerCovPath, ip); w.Code != http.StatusAccepted {
		t.Fatalf("first: %d", w.Code)
	}
	w := h.pollUntil(t, plannerCovPath, ip, http.StatusServiceUnavailable)
	if w.Header().Get("Retry-After") != "300" {
		t.Fatalf("late result: RA=%q %s", w.Header().Get("Retry-After"), w.Body.String())
	}
	// Every later request in the window: 503, no job, no token.
	for i := 0; i < 3; i++ {
		w := h.get(plannerCovPath, ip)
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), plannerMsgFailedRecent) ||
			w.Header().Get("Retry-After") != "300" {
			t.Fatalf("in the window: %d RA=%q %s", w.Code, w.Header().Get("Retry-After"), w.Body.String())
		}
	}
	if n := calls.Load(); n != 1 || h.liveJobs() != 0 || h.tokens(ip) != plannerIPJobsPerMin-1 {
		t.Fatalf("searches=%d live=%d tokens=%v", n, h.liveJobs(), h.tokens(ip))
	}
	h.clk.add(plannerFailedFor - time.Minute)
	if w := h.get(plannerCovPath, ip); w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "60" {
		t.Fatalf("a minute left: %d RA=%q", w.Code, w.Header().Get("Retry-After"))
	}
	h.clk.add(time.Minute)
	sleep.Store(0)
	h.pollUntil(t, plannerCovPath, ip, http.StatusOK)
	if n := calls.Load(); n != 2 {
		t.Fatalf("searches=%d want 2", n)
	}

	// A search that honours its deadline fails the same way; the request
	// waiting on it is told why.
	h.p.fastWait = plannerFastWait
	h.p.relay = func(ctx context.Context, v terrainView, nodes []plannerNode, p PlannerRelayParams, progress func(float64)) (PlannerRelayResponse, error) {
		<-ctx.Done()
		return PlannerRelayResponse{}, ctx.Err()
	}
	w = h.get(plannerRelayPath, ip)
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "planner timed out") {
		t.Fatalf("relay timeout: %d %s", w.Code, w.Body.String())
	}
	if w := h.get(plannerRelayPath, ip); !strings.Contains(w.Body.String(), plannerMsgFailedRecent) {
		t.Fatalf("relay retry: %d %s", w.Code, w.Body.String())
	}
}

// Jobs nobody polls are dropped, not held against the key.
func TestPlannerAPIAbandonedJobNotRemembered(t *testing.T) {
	h := newPlannerHarness(t)
	h.stubs.hold()
	ip := "198.51.100.111"
	h.get(plannerCovPath, ip)
	h.waitEntered(t)
	h.clk.add(plannerAbandonAfter + time.Second)
	plannerWaitFor(t, "abandoned job to go", func() bool { return h.liveJobs() == 0 })
	h.p.mu.Lock()
	n := len(h.p.recent)
	h.p.mu.Unlock()
	if n != 0 {
		t.Fatalf("abandoned job remembered (%d entries)", n)
	}
	if w := h.get(plannerCovPath, ip); w.Code != http.StatusAccepted {
		t.Fatalf("after abandonment: %d %s", w.Code, w.Body.String())
	}
}

func TestPlannerAPITerrainBeforeSlot(t *testing.T) {
	h := newPlannerHarness(t)
	view1 := plannerExpandBBox(geoBBox{S: 30.0, W: -97.2, N: 30.35, E: -96.85}, defaultPlannerNodeMarginKm)
	h.src.hold(view1) // the first job's terrain takes as long as we like
	ip := "198.51.100.112"
	if w := h.get(plannerCovPath, ip); w.Code != http.StatusAccepted {
		t.Fatalf("slow job: %d", w.Code)
	}
	plannerWaitFor(t, "loading_terrain", func() bool {
		return decodeStatus(t, h.get(plannerCovPath, ip)).State == "loading_terrain"
	})
	// A second area whose terrain is resident runs to completion meanwhile.
	h.p.fastWait = plannerFastWait
	other := "/api/planner/coverage?bbox=30.05,-97.15,30.3,-96.9"
	if w := h.get(other, ip); w.Code != http.StatusOK {
		t.Fatalf("resident job behind a download: %d %s", w.Code, w.Body.String())
	}
	if st := decodeStatus(t, h.get(plannerCovPath, ip)); st.State != "loading_terrain" {
		t.Fatalf("first job state %q", st.State)
	}
	h.p.mu.Lock()
	q := h.p.covQueued
	h.p.mu.Unlock()
	if q != 1 {
		t.Fatalf("covQueued=%d: a loading job must count as queued", q)
	}
	h.src.release(view1)
	h.pollUntil(t, plannerCovPath, ip, http.StatusOK)
}

func TestPlannerAPILoadingJobsCountAsQueued(t *testing.T) {
	h := newPlannerHarness(t)
	view := plannerExpandBBox(geoBBox{S: 30.0, W: -97.2, N: 30.35, E: -96.85}, defaultPlannerNodeMarginKm)
	h.src.hold(view)
	ip := "198.51.100.113"
	for s := 1; s <= plannerMaxQueuedCoverage; s++ {
		if w := h.get(fmt.Sprintf("%s&sites=%d", plannerCovPath, s), ip); w.Code != http.StatusAccepted {
			t.Fatalf("job %d: %d", s, w.Code)
		}
	}
	w := h.get(plannerCovPath+"&sites=9", ip)
	if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "5" {
		t.Fatalf("5th loading coverage job: %d RA=%q", w.Code, w.Header().Get("Retry-After"))
	}
	if len(h.p.covSlots) != 0 {
		t.Fatal("a job took the compute slot before its terrain loaded")
	}
	h.src.release(view)
	plannerWaitFor(t, "jobs to finish", func() bool { return h.liveJobs() == 0 })
	if c, _ := h.stubs.calls(); c != plannerMaxQueuedCoverage {
		t.Fatalf("searches=%d", c)
	}
}

func TestPlannerAPIMissingTerrainWarnings(t *testing.T) {
	h := newPlannerHarness(t)
	h.p.fastWait = plannerFastWait
	ip := "198.51.100.114"
	warning := func(view geoBBox) string {
		return fmt.Sprintf("Terrain missing for 1 of %d tiles (read as sea level): results there are unreliable",
			len(fakeTiles(view)))
	}
	setMissing := func(tiles ...[2]int) {
		h.src.set(func(f *fakeTerrain) {
			f.missing = map[[2]int]bool{}
			for _, t := range tiles {
				f.missing[t] = true
			}
		})
	}

	// Coverage: its view spans 29..30 N, 98..97 W; one corner has no data.
	setMissing([2]int{29, -98})
	w := h.get(plannerCovPath, ip)
	var plan PlannerPlanResponse
	json.Unmarshal(w.Body.Bytes(), &plan)
	if want := warning(h.src.lastBBox()); w.Code != http.StatusOK || !slices.Contains(plan.Warnings, want) {
		t.Fatalf("coverage: %d warnings %q, want %q", w.Code, plan.Warnings, want)
	}

	setMissing([2]int{30, -98})
	w = h.get(plannerRelayPath, ip)
	var relay PlannerRelayResponse
	json.Unmarshal(w.Body.Bytes(), &relay)
	if want := warning(h.src.lastBBox()); w.Code != http.StatusOK || !slices.Contains(relay.Warnings, want) {
		t.Fatalf("relay: %d warnings %q, want %q", w.Code, relay.Warnings, want)
	}

	// Profile: the first leg stays in 30N 98W; the second crosses into
	// 30N 97W, which has no data.
	setMissing([2]int{30, -97})
	w = h.get("/api/planner/profile?pts=30.1,-97.3,30.2,-97.2,30.15,-96.95", ip)
	var prof PlannerProfileResponse
	json.Unmarshal(w.Body.Bytes(), &prof)
	if w.Code != http.StatusOK || len(prof.Legs) != 2 {
		t.Fatalf("profile: %d %s", w.Code, w.Body.String())
	}
	if prof.Legs[0].Error != "" || len(prof.Legs[0].ElevM) == 0 {
		t.Fatalf("leg over good terrain: %+v", prof.Legs[0])
	}
	if prof.Legs[1].Error != plannerErrNoTerrain || prof.Legs[1].ElevM != nil {
		t.Fatalf("leg over missing terrain: %+v", prof.Legs[1])
	}
	if want := warning(h.src.lastBBox()); !slices.Equal(prof.Warnings, []string{want}) {
		t.Fatalf("profile warnings %q, want %q", prof.Warnings, want)
	}

	// Full terrain: no warning anywhere.
	setMissing()
	w = h.get("/api/planner/profile?pts=30.1,-97.3,30.2,-97.2,30.15,-96.94", ip)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "warnings") || strings.Contains(w.Body.String(), plannerErrNoTerrain) {
		t.Fatalf("complete terrain: %d %s", w.Code, w.Body.String())
	}
	w = h.get(plannerCovPath+"&sites=2", ip)
	json.Unmarshal(w.Body.Bytes(), &plan)
	if len(plan.Warnings) != 0 {
		t.Fatalf("complete terrain coverage warnings %q", plan.Warnings)
	}
}

func TestPlannerConfigLogRedactsTileURL(t *testing.T) {
	var buf bytes.Buffer
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(flags) })
	logPlannerConfig(&PlannerConfig{Enabled: true, TileURL: "https://user:s3cret@tiles.example.com/tok-123/skadi"})
	out := buf.String()
	if !strings.Contains(out, "tiles=https://tiles.example.com,") {
		t.Fatalf("log %q", out)
	}
	for _, leak := range []string{"user", "s3cret", "tok-123", "skadi"} {
		if strings.Contains(out, leak) {
			t.Fatalf("log leaks %q: %q", leak, out)
		}
	}
	if got := plannerTileHost("not a url"); got != "(invalid)" {
		t.Fatalf("plannerTileHost = %q", got)
	}
}
