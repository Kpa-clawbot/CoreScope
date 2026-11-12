package main

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeElevUpstream struct {
	srv    *httptest.Server
	calls  int32
	status atomic.Int32
	// short, when > 0, returns that many values instead of losSamples.
	short atomic.Int32
	nulls atomic.Bool
	delay time.Duration

	mu    sync.Mutex
	query []url.Values
}

func newFakeElevUpstream(t *testing.T) *fakeElevUpstream {
	f := &fakeElevUpstream{}
	f.status.Store(200)
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.calls, 1)
		f.mu.Lock()
		f.query = append(f.query, r.URL.Query())
		f.mu.Unlock()
		if f.delay > 0 {
			time.Sleep(f.delay)
		}
		if st := int(f.status.Load()); st != 200 {
			w.WriteHeader(st)
			w.Write([]byte(`{"error":true,"reason":"nope"}`))
			return
		}
		n := losSamples
		if s := int(f.short.Load()); s > 0 {
			n = s
		}
		vals := make([]interface{}, n)
		for i := range vals {
			vals[i] = float64(i)
			if f.nulls.Load() && i%10 == 0 {
				vals[i] = nil
			}
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"elevation": vals})
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeElevUpstream) count() int { return int(atomic.LoadInt32(&f.calls)) }

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
// with register (or implicitly through getElev).
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
func (n *elevNodes) register(pts ...elevPoint) {
	n.mu.Lock()
	for _, p := range pts {
		n.ns = append(n.ns, nodeInfo{Lat: p.lat, Lon: p.lon, HasGPS: true})
	}
	n.mu.Unlock()
	n.svc.mu.Lock()
	n.svc.nodeSet = nil
	n.svc.mu.Unlock()
}

var elevNodeLists sync.Map // *elevationService -> *elevNodes

func nodesOf(svc *elevationService) *elevNodes {
	v, _ := elevNodeLists.Load(svc)
	return v.(*elevNodes)
}

// tleg registers both endpoints, then asks for the leg.
func tleg(svc *elevationService, a, b elevPoint) ElevationLeg {
	nodesOf(svc).register(a, b)
	return svc.leg(a, b)
}

func newElevTestServer(t *testing.T, f *fakeElevUpstream, query string) (*Server, *elevationService, *elevClock) {
	svc := newElevationService(LOSConfig{Enabled: true, ElevationURL: f.srv.URL + query})
	clk := &elevClock{t: time.Unix(1_700_000_000, 0)}
	svc.now = clk.now
	nl := &elevNodes{svc: svc}
	svc.nodes = nl.get
	elevNodeLists.Store(svc, nl)
	t.Cleanup(func() { elevNodeLists.Delete(svc) })
	return &Server{cfg: &Config{LOS: &LOSConfig{Enabled: true}}, elevation: svc}, svc, clk
}

// getElev registers the request's points as known nodes, then calls the
// handler.
func getElev(srv *Server, pts string) (int, ElevationProfileResponse, string) {
	if srv.elevation != nil {
		if v, ok := elevNodeLists.Load(srv.elevation); ok {
			parts := strings.Split(pts, ",")
			var ps []elevPoint
			for i := 0; i+1 < len(parts); i += 2 {
				la, e1 := strconv.ParseFloat(parts[i], 64)
				lo, e2 := strconv.ParseFloat(parts[i+1], 64)
				if e1 == nil && e2 == nil {
					ps = append(ps, elevPoint{la, lo})
				}
			}
			v.(*elevNodes).register(ps...)
		}
	}
	return getElevRaw(srv, pts)
}

func getElevRaw(srv *Server, pts string) (int, ElevationProfileResponse, string) {
	req := httptest.NewRequest(http.MethodGet, "/api/elevation/profile?pts="+url.QueryEscape(pts), nil)
	w := serveElev(srv, req)
	var resp ElevationProfileResponse
	if w.Code == 200 {
		json.Unmarshal(w.Body.Bytes(), &resp)
	}
	return w.Code, resp, w.Header().Get("Cache-Control")
}

func serveElev(srv *Server, req *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	srv.handleElevationProfile(w, req)
	return w
}

func TestElevationDisabled(t *testing.T) {
	for _, srv := range []*Server{
		{cfg: &Config{LOS: &LOSConfig{Enabled: false}}, elevation: newElevationService(LOSConfig{})},
		{cfg: &Config{LOS: &LOSConfig{Enabled: true}}},
		{cfg: &Config{}},
	} {
		if code, _, _ := getElev(srv, "1,1,2,2"); code != 404 {
			t.Fatalf("code=%d want 404", code)
		}
	}
}

func TestElevationValidation(t *testing.T) {
	f := newFakeElevUpstream(t)
	srv, _, _ := newElevTestServer(t, f, "")
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
		if code, _, _ := getElev(srv, pts); code != 400 {
			t.Errorf("%s: code=%d want 400", name, code)
		}
	}
	if f.count() != 0 {
		t.Fatalf("validation failures reached upstream: %d", f.count())
	}
}

func TestElevationHappyPath(t *testing.T) {
	f := newFakeElevUpstream(t)
	srv, _, _ := newElevTestServer(t, f, "")
	code, resp, cc := getElev(srv, "0,1,0,2,0,3")
	if code != 200 {
		t.Fatalf("code=%d", code)
	}
	if cc != "public, max-age=86400" {
		t.Fatalf("Cache-Control=%q", cc)
	}
	if resp.Samples != losSamples || len(resp.Legs) != 2 || resp.Source == "" {
		t.Fatalf("bad response: %+v", resp)
	}
	want := math.Round(haversineKm(0, 1, 0, 2)*1000) / 1000
	for i, l := range resp.Legs {
		if len(l.ElevM) != losSamples || l.Error != "" {
			t.Fatalf("leg %d: %+v", i, l)
		}
		if l.DistanceKm != want {
			t.Fatalf("leg %d distance=%v want %v", i, l.DistanceKm, want)
		}
	}
	if f.count() != 2 {
		t.Fatalf("calls=%d want 2", f.count())
	}
}

func TestElevationUpstreamQuery(t *testing.T) {
	f := newFakeElevUpstream(t)
	srv, _, _ := newElevTestServer(t, f, "?apikey=sekrit")
	if code, _, _ := getElev(srv, "10,20,10.5,20.5"); code != 200 {
		t.Fatalf("code=%d", code)
	}
	q := f.query[0]
	if q.Get("apikey") != "sekrit" {
		t.Fatalf("apikey lost: %v", q)
	}
	lats, lons := strings.Split(q.Get("latitude"), ","), strings.Split(q.Get("longitude"), ",")
	if len(lats) != losSamples || len(lons) != losSamples {
		t.Fatalf("got %d lats, %d lons", len(lats), len(lons))
	}
	if lats[0] != "10.00000" || lons[losSamples-1] != "20.50000" {
		t.Fatalf("endpoints: %s %s", lats[0], lons[losSamples-1])
	}
}

func TestElevationGreatCircleSampling(t *testing.T) {
	a, b := elevPoint{0, 0}, elevPoint{0, 90}
	s := greatCircleSamples(a, b, losSamples)
	if s[0] != a || s[losSamples-1] != b {
		t.Fatalf("endpoints %v %v", s[0], s[losSamples-1])
	}
	// 100 samples have no exact midpoint; use 101 for t=0.5.
	m := greatCircleSamples(a, b, 101)[50]
	if math.Abs(m.lat) > 1e-9 || math.Abs(m.lon-45) > 1e-9 {
		t.Fatalf("midpoint %v want (0,45)", m)
	}
	// Along a meridian-crossing great circle the path bows poleward.
	mid := greatCircleSamples(elevPoint{60, -60}, elevPoint{60, 60}, 101)[50]
	if mid.lat <= 60 {
		t.Fatalf("expected great-circle arc above 60N, got %v", mid)
	}
	same := greatCircleSamples(a, a, 5)
	for _, p := range same {
		if p != a {
			t.Fatalf("identical endpoints: %v", p)
		}
	}
}

func TestElevationCacheHit(t *testing.T) {
	f := newFakeElevUpstream(t)
	srv, _, _ := newElevTestServer(t, f, "")
	_, r1, _ := getElev(srv, "1,1,1.2,1.2")
	_, r2, _ := getElev(srv, "1,1,1.2,1.2")
	if f.count() != 1 {
		t.Fatalf("calls=%d want 1", f.count())
	}
	if fmt.Sprint(r1.Legs) != fmt.Sprint(r2.Legs) {
		t.Fatal("cached response differs")
	}
}

func TestElevationReversedLegFromCache(t *testing.T) {
	f := newFakeElevUpstream(t)
	srv, svc, _ := newElevTestServer(t, f, "")
	_, fwd, _ := getElev(srv, "1,1,1.2,1.2")
	_, rev, _ := getElev(srv, "1.2,1.2,1,1")
	if f.count() != 1 {
		t.Fatalf("calls=%d want 1", f.count())
	}
	n := losSamples
	for i := 0; i < n; i++ {
		if fwd.Legs[0].ElevM[i] != rev.Legs[0].ElevM[n-1-i] {
			t.Fatalf("not reversed at %d", i)
		}
	}
	if fwd.Legs[0].ElevM[0] == fwd.Legs[0].ElevM[n-1] {
		t.Fatal("test data should be asymmetric")
	}
	// Repeat in the original direction: cache must be unmodified.
	_, fwd2, _ := getElev(srv, "1,1,1.2,1.2")
	if fmt.Sprint(fwd.Legs) != fmt.Sprint(fwd2.Legs) {
		t.Fatal("cached data was mutated")
	}
	for _, el := range svc.items {
		for i, v := range el.Value.(*elevCacheEntry).elev {
			if v != float64(i) {
				t.Fatalf("cache entry mutated at %d", i)
			}
		}
	}
	// Mutating a returned slice must not touch the cache.
	l := tleg(svc, elevPoint{1, 1}, elevPoint{1.2, 1.2})
	l.ElevM[3] = -999
	l2 := svc.leg(elevPoint{1, 1}, elevPoint{1.2, 1.2})
	if l2.ElevM[3] == -999 {
		t.Fatal("cache slice handed out")
	}
}

func TestElevationTooLongNoCall(t *testing.T) {
	f := newFakeElevUpstream(t)
	srv, _, _ := newElevTestServer(t, f, "")
	code, resp, _ := getElev(srv, "0,0,0,5,0,5.01")
	if code != 200 {
		t.Fatalf("code=%d", code)
	}
	if resp.Legs[0].Error != elevErrTooLong || len(resp.Legs[0].ElevM) != 0 || resp.Legs[0].DistanceKm < losMaxLegKm {
		t.Fatalf("leg0: %+v", resp.Legs[0])
	}
	if resp.Legs[1].Error != "" {
		t.Fatalf("leg1: %+v", resp.Legs[1])
	}
	if f.count() != 1 {
		t.Fatalf("calls=%d want 1 (only the short leg)", f.count())
	}
}

func TestElevationUpstream500CooldownAndRecovery(t *testing.T) {
	f := newFakeElevUpstream(t)
	srv, svc, clk := newElevTestServer(t, f, "")
	f.status.Store(500)
	_, resp, _ := getElev(srv, "1,1,1.2,1.2")
	if resp.Legs[0].Error != elevErrUpstream || len(resp.Legs[0].ElevM) != 0 {
		t.Fatalf("leg: %+v", resp.Legs[0])
	}
	if svc.cacheLen() != 0 {
		t.Fatal("failure was cached")
	}
	f.status.Store(200)
	// Cooldown: no call, still an upstream error.
	_, resp, _ = getElev(srv, "2,2,2.2,2.2")
	if resp.Legs[0].Error != elevErrUpstream || f.count() != 1 {
		t.Fatalf("cooldown not enforced: %+v calls=%d", resp.Legs[0], f.count())
	}
	clk.add(elevBackoff[0] + time.Second)
	_, resp, _ = getElev(srv, "2,2,2.2,2.2")
	if resp.Legs[0].Error != "" || f.count() != 2 {
		t.Fatalf("after cooldown: %+v calls=%d", resp.Legs[0], f.count())
	}
}

func TestElevationUpstream429OpensCooldown(t *testing.T) {
	f := newFakeElevUpstream(t)
	srv, _, _ := newElevTestServer(t, f, "")
	f.status.Store(429)
	getElev(srv, "1,1,1.2,1.2")
	f.status.Store(200)
	_, resp, _ := getElev(srv, "3,3,3.2,3.2")
	if resp.Legs[0].Error != elevErrUpstream || f.count() != 1 {
		t.Fatalf("429 did not open cooldown: %+v calls=%d", resp.Legs[0], f.count())
	}
}

func TestElevationClientErrorStatusesOpenCooldown(t *testing.T) {
	for _, st := range []int{400, 401, 403} {
		f := newFakeElevUpstream(t)
		srv, svc, _ := newElevTestServer(t, f, "")
		f.status.Store(int32(st))
		_, resp, _ := getElev(srv, "1,1,1.2,1.2")
		if resp.Legs[0].Error != elevErrUpstream || svc.cacheLen() != 0 {
			t.Fatalf("%d: %+v", st, resp.Legs[0])
		}
		f.status.Store(200)
		_, resp, _ = getElev(srv, "1,1,1.3,1.3")
		if resp.Legs[0].Error != elevErrUpstream || f.count() != 1 {
			t.Fatalf("%d did not open cooldown: %+v calls=%d", st, resp.Legs[0], f.count())
		}
	}
}

func TestElevationLengthMismatchAndNulls(t *testing.T) {
	f := newFakeElevUpstream(t)
	srv, svc, _ := newElevTestServer(t, f, "")
	f.short.Store(50)
	_, resp, _ := getElev(srv, "1,1,1.2,1.2")
	if resp.Legs[0].Error != elevErrUpstream || svc.cacheLen() != 0 {
		t.Fatalf("mismatch: %+v", resp.Legs[0])
	}
	f.short.Store(0)
	f.nulls.Store(true)
	_, resp, _ = getElev(srv, "1,1,1.2,1.2")
	l := resp.Legs[0]
	if l.Error != "" || len(l.ElevM) != losSamples {
		t.Fatalf("nulls: %+v", l)
	}
	if l.ElevM[10] != 0 || l.ElevM[11] != 11 {
		t.Fatalf("null should be 0: %v %v", l.ElevM[10], l.ElevM[11])
	}
}

func TestElevationLRUEviction(t *testing.T) {
	f := newFakeElevUpstream(t)
	srv, svc, _ := newElevTestServer(t, f, "")
	svc.capacity = 2
	getElev(srv, "1,1,1.1,1.1")
	getElev(srv, "2,2,2.1,2.1")
	getElev(srv, "1,1,1.1,1.1") // refresh leg 1
	getElev(srv, "3,3,3.1,3.1") // evicts leg 2
	if svc.cacheLen() != 2 {
		t.Fatalf("size=%d want 2", svc.cacheLen())
	}
	before := f.count()
	getElev(srv, "1,1,1.1,1.1")
	if f.count() != before {
		t.Fatal("recently used leg was evicted")
	}
	getElev(srv, "2,2,2.1,2.1")
	if f.count() != before+1 {
		t.Fatal("evicted leg should refetch")
	}
	if svc.cacheLen() != 2 {
		t.Fatalf("size=%d want 2", svc.cacheLen())
	}
}

func TestElevationConcurrentSingleflight(t *testing.T) {
	f := newFakeElevUpstream(t)
	f.delay = 150 * time.Millisecond
	srv, svc, _ := newElevTestServer(t, f, "")
	nodesOf(svc).register(elevPoint{1, 1}, elevPoint{1.2, 1.2})
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			pts := "1,1,1.2,1.2"
			if i%2 == 1 {
				pts = "1.2,1.2,1,1"
			}
			if code, resp, _ := getElevRaw(srv, pts); code != 200 || resp.Legs[0].Error != "" {
				t.Errorf("code=%d leg=%+v", code, resp.Legs[0])
			}
		}(i)
	}
	wg.Wait()
	if f.count() != 1 {
		t.Fatalf("calls=%d want 1", f.count())
	}
}

func TestElevationConcurrencyBound(t *testing.T) {
	var cur, max int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&cur, 1)
		for {
			m := atomic.LoadInt32(&max)
			if n <= m || atomic.CompareAndSwapInt32(&max, m, n) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		atomic.AddInt32(&cur, -1)
		vals := make([]float64, losSamples)
		json.NewEncoder(w).Encode(map[string]interface{}{"elevation": vals})
	}))
	defer srv.Close()
	svc := newElevationService(LOSConfig{Enabled: true, ElevationURL: srv.URL})
	s := &Server{cfg: &Config{LOS: &LOSConfig{Enabled: true}}, elevation: svc}
	// 10 distinct legs in one request.
	var p []string
	var ns []nodeInfo
	for i := 0; i < 11; i++ {
		p = append(p, fmt.Sprintf("%d,1", i))
		ns = append(ns, nodeInfo{Lat: float64(i), Lon: 1, HasGPS: true})
	}
	svc.nodes = func() []nodeInfo { return ns }
	// Adjacent degrees are ~111 km, within the leg limit.
	if code, resp, _ := getElev(s, strings.Join(p, ",")); code != 200 || len(resp.Legs) != 10 {
		t.Fatalf("code=%d legs=%d", code, len(resp.Legs))
	}
	if max > elevMaxConcurrent || max < 2 {
		t.Fatalf("max concurrent upstream calls=%d", max)
	}
}

func TestElevationUnknownPoint(t *testing.T) {
	f := newFakeElevUpstream(t)
	srv, svc, clk := newElevTestServer(t, f, "")
	nl := nodesOf(svc)
	nl.mu.Lock()
	nl.ns = []nodeInfo{
		{Lat: 1, Lon: 1, HasGPS: true},
		{Lat: 1.2, Lon: 1.2, HasGPS: true},
		{Lat: 0, Lon: 0, HasGPS: true},      // null island: excluded
		{Lat: 0.5, Lon: 0.5, HasGPS: false}, // no GPS: excluded
	}
	nl.mu.Unlock()
	for name, pts := range map[string]string{
		"unknown":     "1.5,1.5,1,1",
		"unknown b":   "1,1,1.5,1.5",
		"null island": "0,0,1,1",
		"no gps":      "0.5,0.5,1,1",
	} {
		_, resp, _ := getElevRaw(srv, pts)
		if resp.Legs[0].Error != elevErrUnknownPoint || len(resp.Legs[0].ElevM) != 0 {
			t.Errorf("%s: %+v", name, resp.Legs[0])
		}
	}
	if f.count() != 0 {
		t.Fatalf("unknown points reached upstream: %d", f.count())
	}
	if _, resp, _ := getElevRaw(srv, "1,1,1.2,1.2"); resp.Legs[0].Error != "" {
		t.Fatalf("known pair: %+v", resp.Legs[0])
	}
	// too_long wins over unknown_point.
	if _, resp, _ := getElevRaw(srv, "10,10,10,20"); resp.Legs[0].Error != elevErrTooLong {
		t.Fatalf("order: %+v", resp.Legs[0])
	}
	// The set is rebuilt only after 60 s.
	nl.mu.Lock()
	nl.ns = append(nl.ns, nodeInfo{Lat: 1.4, Lon: 1.4, HasGPS: true})
	nl.mu.Unlock()
	if _, resp, _ := getElevRaw(srv, "1,1,1.4,1.4"); resp.Legs[0].Error != elevErrUnknownPoint {
		t.Fatalf("set refreshed early: %+v", resp.Legs[0])
	}
	clk.add(elevNodeSetTTL + time.Second)
	if _, resp, _ := getElevRaw(srv, "1,1,1.4,1.4"); resp.Legs[0].Error != "" {
		t.Fatalf("set not refreshed after TTL: %+v", resp.Legs[0])
	}
}

// Review #2391 point 4: a node /api/nodes hides (blacklist, hidden name,
// outside the geo_filter) is no valid endpoint, and a rule change applies to
// the next request, not after elevNodeSetTTL.
func TestElevationHiddenNodesAreUnknown(t *testing.T) {
	f := newFakeElevUpstream(t)
	srv, svc, _ := newElevTestServer(t, f, "")
	srv.cfg.SetNodeBlacklist([]string{"bad1"})
	srv.cfg.SetHiddenNamePrefixes([]string{"zz-"})
	inside := &GeoFilterConfig{Polygon: [][2]float64{{0.5, 0.5}, {0.5, 1.35}, {1.35, 1.35}, {1.35, 0.5}}}
	srv.setGeoFilter(inside)
	svc.visibility = srv
	nl := nodesOf(svc)
	nl.mu.Lock()
	nl.ns = []nodeInfo{
		{PublicKey: "ok1", Name: "A", Lat: 1, Lon: 1, HasGPS: true},
		{PublicKey: "ok2", Name: "B", Lat: 1.2, Lon: 1.2, HasGPS: true},
		{PublicKey: "BAD1", Name: "C", Lat: 1.1, Lon: 1.1, HasGPS: true},
		{PublicKey: "hid1", Name: "zz-x", Lat: 1.3, Lon: 1.3, HasGPS: true},
		{PublicKey: "far1", Name: "D", Lat: 1.4, Lon: 1.4, HasGPS: true},
	}
	nl.mu.Unlock()
	leg := func(pts string) ElevationLeg {
		t.Helper()
		code, resp, _ := getElevRaw(srv, pts)
		if code != http.StatusOK || len(resp.Legs) != 1 {
			t.Fatalf("%s: %d %+v", pts, code, resp)
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
	if f.count() != 0 {
		t.Fatalf("hidden endpoints reached upstream: %d", f.count())
	}
	if l := leg("1,1,1.2,1.2"); l.Error != "" {
		t.Fatalf("visible pair: %+v", l)
	}

	// Each rule change applies at once (the clock never moves), and a cached
	// profile is not served for an endpoint that is now hidden.
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
	if f.count() != 2 {
		t.Fatalf("upstream calls=%d want 2 (1,1→1.2,1.2 once, 1,1→1.4,1.4 once)", f.count())
	}
}

func TestElevationUnknownPointFailClosed(t *testing.T) {
	f := newFakeElevUpstream(t)
	srv, svc, _ := newElevTestServer(t, f, "")
	svc.nodes = nil
	_, resp, _ := getElevRaw(srv, "1,1,1.2,1.2")
	if resp.Legs[0].Error != elevErrUnknownPoint {
		t.Fatalf("nil nodes: %+v", resp.Legs[0])
	}
	svc.nodes = func() []nodeInfo { return nil }
	_, resp, _ = getElevRaw(srv, "1,1,1.2,1.2")
	if resp.Legs[0].Error != elevErrUnknownPoint || f.count() != 0 {
		t.Fatalf("empty nodes: %+v calls=%d", resp.Legs[0], f.count())
	}
}

func TestElevationQuantisedSampling(t *testing.T) {
	f := newFakeElevUpstream(t)
	srv, svc, _ := newElevTestServer(t, f, "")
	nodesOf(svc).register(elevPoint{1, 1}, elevPoint{1.2, 1.2})
	// Two spellings of the same 5-decimal endpoints.
	getElevRaw(srv, "1.0000004,1,1.2,1.2")
	getElevRaw(srv, "1,1,1.2,1.2000003")
	if f.count() != 1 {
		t.Fatalf("calls=%d want 1", f.count())
	}
	if q := f.query[0]; strings.Split(q.Get("latitude"), ",")[0] != "1.00000" {
		t.Fatalf("not sampled from the quantised endpoint: %v", q.Get("latitude")[:20])
	}
}

func TestElevationBudgetsExhaustIndependently(t *testing.T) {
	big := 1000000
	cases := []struct {
		name  string
		set   func(*elevationService)
		reset time.Duration // advance that must reopen the budget
		short time.Duration // advance that must NOT reopen it
	}{
		{"minute", func(s *elevationService) { s.minute.limit = 2; s.hour.limit, s.day.limit = big, big }, time.Minute + time.Second, 0},
		{"hour", func(s *elevationService) { s.hour.limit = 2; s.minute.limit, s.day.limit = big, big }, time.Hour + time.Second, time.Minute + time.Second},
		{"day", func(s *elevationService) { s.day.limit = 2; s.minute.limit, s.hour.limit = big, big }, 24*time.Hour + time.Second, time.Hour + time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFakeElevUpstream(t)
			_, svc, clk := newElevTestServer(t, f, "")
			c.set(svc)
			leg := func(i int) ElevationLeg {
				return tleg(svc, elevPoint{float64(i) + 0.5, 1}, elevPoint{float64(i) + 0.6, 1.1})
			}
			for i := 0; i < 2; i++ {
				if l := leg(i); l.Error != "" {
					t.Fatalf("call %d: %+v", i, l)
				}
			}
			if l := leg(2); l.Error != elevErrUpstream || f.count() != 2 {
				t.Fatalf("budget not enforced: %+v calls=%d", l, f.count())
			}
			// Cached legs are still served over budget.
			if l := leg(0); l.Error != "" {
				t.Fatalf("cached leg over budget: %+v", l)
			}
			if c.short > 0 {
				clk.add(c.short)
				if l := leg(2); l.Error != elevErrUpstream {
					t.Fatalf("reopened too early: %+v", l)
				}
				clk.add(c.reset - c.short)
			} else {
				clk.add(c.reset)
			}
			if l := leg(2); l.Error != "" {
				t.Fatalf("not reopened after window: %+v", l)
			}
		})
	}
}

func TestElevationSemaphoreTimeoutDoesNotSpendBudget(t *testing.T) {
	f := newFakeElevUpstream(t)
	_, svc, _ := newElevTestServer(t, f, "")
	svc.timeout = 50 * time.Millisecond
	for i := 0; i < elevMaxConcurrent; i++ {
		svc.sem <- struct{}{}
	}
	l := tleg(svc, elevPoint{1, 1}, elevPoint{1.2, 1.2})
	if l.Error != elevErrUpstream || f.count() != 0 {
		t.Fatalf("leg=%+v calls=%d", l, f.count())
	}
	svc.mu.Lock()
	spent := svc.minute.n + svc.hour.n + svc.day.n
	cool := svc.coolTo
	svc.mu.Unlock()
	if spent != 0 || !cool.IsZero() {
		t.Fatalf("budget spent=%d cooldown=%v on a semaphore timeout", spent, cool)
	}
}

func TestElevationBackoffLadderAndReset(t *testing.T) {
	f := newFakeElevUpstream(t)
	_, svc, clk := newElevTestServer(t, f, "")
	f.status.Store(503)
	n := 0
	try := func() ElevationLeg {
		n++
		return tleg(svc, elevPoint{1, 1}, elevPoint{1.2, 1.2})
	}
	for step, d := range elevBackoff {
		before := f.count()
		if l := try(); l.Error != elevErrUpstream || f.count() != before+1 {
			t.Fatalf("step %d: call not made (%+v)", step, l)
		}
		clk.add(d - time.Second)
		if try(); f.count() != before+1 {
			t.Fatalf("step %d: call made before %s elapsed", step, d)
		}
		clk.add(2 * time.Second)
	}
	// The ladder tops out at one hour.
	if try(); f.count() != len(elevBackoff)+1 {
		t.Fatalf("expected a call after the last step, calls=%d", f.count())
	}
	clk.add(time.Hour - time.Second)
	before := f.count()
	if try(); f.count() != before {
		t.Fatal("capped cooldown shorter than 1h")
	}
	clk.add(2 * time.Second)
	// A 200 resets the ladder.
	f.status.Store(200)
	if l := try(); l.Error != "" {
		t.Fatalf("success expected: %+v", l)
	}
	f.status.Store(500)
	tleg(svc, elevPoint{2, 2}, elevPoint{2.2, 2.2}) // fails: first rung again
	before = f.count()
	clk.add(61 * time.Second)
	tleg(svc, elevPoint{2, 2}, elevPoint{2.2, 2.2})
	if f.count() != before+1 {
		t.Fatalf("ladder not reset by 200: calls=%d want %d", f.count(), before+1)
	}
}

func TestElevationTransportErrorOpensCooldown(t *testing.T) {
	f := newFakeElevUpstream(t)
	_, svc, _ := newElevTestServer(t, f, "")
	f.srv.Close()
	if l := tleg(svc, elevPoint{1, 1}, elevPoint{1.2, 1.2}); l.Error != elevErrUpstream {
		t.Fatalf("%+v", l)
	}
	svc.mu.Lock()
	cool := svc.coolTo
	svc.mu.Unlock()
	if cool.IsZero() {
		t.Fatal("transport error did not open a cooldown")
	}
}

func getElevFrom(srv *Server, pts, remote string, hdr map[string]string) *httptest.ResponseRecorder {
	if v, ok := elevNodeLists.Load(srv.elevation); ok {
		parts := strings.Split(pts, ",")
		var ps []elevPoint
		for i := 0; i+1 < len(parts); i += 2 {
			la, _ := strconv.ParseFloat(parts[i], 64)
			lo, _ := strconv.ParseFloat(parts[i+1], 64)
			ps = append(ps, elevPoint{la, lo})
		}
		v.(*elevNodes).register(ps...)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/elevation/profile?pts="+url.QueryEscape(pts), nil)
	req.RemoteAddr = remote
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return serveElev(srv, req)
}

func withElevLimit(svc *elevationService, clk *elevClock, n int) {
	svc.ipr = &wsLimiter{state: map[string]*wsIPState{}}
	svc.rl = newRateLimiter(n, time.Minute)
	svc.rl.now = clk.now
}

func TestElevationPerIPRateLimit(t *testing.T) {
	f := newFakeElevUpstream(t)
	srv, svc, clk := newElevTestServer(t, f, "")
	withElevLimit(svc, clk, 2)
	ip := "192.0.2.10:5000"
	for i := 0; i < 2; i++ {
		w := getElevFrom(srv, fmt.Sprintf("%d,1,%d.1,1.1", i+1, i+1), ip, nil)
		if w.Code != 200 {
			t.Fatalf("request %d: %d", i, w.Code)
		}
	}
	w := getElevFrom(srv, "7,1,7.1,1.1", ip, nil)
	if w.Code != 429 {
		t.Fatalf("code=%d want 429", w.Code)
	}
	if ra, _ := strconv.Atoi(w.Header().Get("Retry-After")); ra < 1 || ra > 30 {
		t.Fatalf("Retry-After=%q", w.Header().Get("Retry-After"))
	}
	if !strings.Contains(w.Body.String(), "rate limited") {
		t.Fatalf("body=%s", w.Body.String())
	}
	before := f.count()
	// A fully cached request is never limited, and takes no token.
	for i := 0; i < 5; i++ {
		if w := getElevFrom(srv, "1,1,1.1,1.1", ip, nil); w.Code != 200 {
			t.Fatalf("cached request limited: %d", w.Code)
		}
	}
	if f.count() != before {
		t.Fatal("cached request reached upstream")
	}
	// Another client has its own bucket.
	if w := getElevFrom(srv, "7,1,7.1,1.1", "192.0.2.11:5000", nil); w.Code != 200 {
		t.Fatalf("other client: %d", w.Code)
	}
	// Tokens refill.
	clk.add(31 * time.Second)
	if w := getElevFrom(srv, "8,1,8.1,1.1", ip, nil); w.Code != 200 {
		t.Fatalf("after refill: %d", w.Code)
	}
}

func TestElevationRateLimitIndistinctClientsShareBucket(t *testing.T) {
	f := newFakeElevUpstream(t)
	srv, svc, clk := newElevTestServer(t, f, "")
	withElevLimit(svc, clk, 2)
	// Loopback peer, no trusted proxy: X-Forwarded-For is not believed.
	for i, xff := range []string{"198.51.100.1", "198.51.100.2"} {
		if w := getElevFrom(srv, fmt.Sprintf("%d,1,%d.1,1.1", i+1, i+1), "127.0.0.1:1", map[string]string{"X-Forwarded-For": xff}); w.Code != 200 {
			t.Fatalf("request %d: %d", i, w.Code)
		}
	}
	if w := getElevFrom(srv, "7,1,7.1,1.1", "127.0.0.1:1", map[string]string{"X-Forwarded-For": "198.51.100.3"}); w.Code != 429 {
		t.Fatalf("shared bucket not enforced: %d", w.Code)
	}
}

func TestElevationQueryBounds(t *testing.T) {
	f := newFakeElevUpstream(t)
	srv, _, _ := newElevTestServer(t, f, "")
	req := httptest.NewRequest(http.MethodGet, "/api/elevation/profile?pts=1,1,2,2&x="+strings.Repeat("a", 2100), nil)
	w := serveElev(srv, req)
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

func TestElevationCacheControl(t *testing.T) {
	f := newFakeElevUpstream(t)
	srv, _, clk := newElevTestServer(t, f, "")
	_ = clk
	if code, _, cc := getElev(srv, "1,1,1.2,1.2"); code != 200 || cc != "public, max-age=86400" {
		t.Fatalf("clean: %d %q", code, cc)
	}
	// too_long only: still cacheable.
	if code, resp, cc := getElev(srv, "0,1,0,5"); code != 200 || resp.Legs[0].Error != elevErrTooLong || cc != "public, max-age=86400" {
		t.Fatalf("too_long: %d %q %+v", code, cc, resp.Legs)
	}
	// unknown_point: not cacheable.
	if _, resp, cc := getElevRaw(srv, "60,60,60.1,60.1"); resp.Legs[0].Error != elevErrUnknownPoint || cc != "no-store" {
		t.Fatalf("unknown: %q %+v", cc, resp.Legs)
	}
	// upstream error mixed with a good leg: not cacheable.
	f.status.Store(500)
	if _, resp, cc := getElev(srv, "1,1,1.2,1.2,2,2"); resp.Legs[1].Error != elevErrUpstream || cc != "no-store" {
		t.Fatalf("upstream: %q %+v", cc, resp.Legs)
	}
}

func TestElevationRedirectNotFollowed(t *testing.T) {
	var hits int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
	}))
	defer target.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redir.Close()
	svc := newElevationService(LOSConfig{Enabled: true, ElevationURL: redir.URL})
	svc.nodes = func() []nodeInfo {
		return []nodeInfo{{Lat: 1, Lon: 1, HasGPS: true}, {Lat: 1.2, Lon: 1.2, HasGPS: true}}
	}
	if l := svc.leg(elevPoint{1, 1}, elevPoint{1.2, 1.2}); l.Error != elevErrUpstream {
		t.Fatalf("%+v", l)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatal("redirect was followed")
	}
}

func TestElevationUserAgent(t *testing.T) {
	old := Version
	defer func() { Version = old }()
	for _, v := range []string{"v9.9.9", ""} {
		Version = v
		var ua atomic.Value
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ua.Store(r.Header.Get("User-Agent"))
			vals := make([]float64, losSamples)
			json.NewEncoder(w).Encode(map[string]interface{}{"elevation": vals})
		}))
		svc := newElevationService(LOSConfig{Enabled: true, ElevationURL: up.URL})
		svc.nodes = func() []nodeInfo {
			return []nodeInfo{{Lat: 1, Lon: 1, HasGPS: true}, {Lat: 1.2, Lon: 1.2, HasGPS: true}}
		}
		svc.leg(elevPoint{1, 1}, elevPoint{1.2, 1.2})
		up.Close()
		want := "CoreScope"
		if v != "" {
			want += "/" + v
		}
		if got, _ := ua.Load().(string); got != want {
			t.Fatalf("User-Agent=%q want %q", got, want)
		}
	}
}

func elevBucketTokens(svc *elevationService, ip string) float64 {
	svc.rl.mu.Lock()
	defer svc.rl.mu.Unlock()
	return svc.rl.buckets[ip].tokens
}

func TestElevationBudgetChargedPerUncachedLeg(t *testing.T) {
	f := newFakeElevUpstream(t)
	srv, svc, clk := newElevTestServer(t, f, "")
	withElevLimit(svc, clk, 10)
	ip := "192.0.2.20"
	remote := ip + ":5000"
	// 4 points = 3 distinct uncached legs.
	if w := getElevFrom(srv, "1,1,1.1,1.1,1.2,1.2,1.3,1.3", remote, nil); w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	if got := elevBucketTokens(svc, ip); got != 7 {
		t.Fatalf("tokens=%v want 7 (3 legs charged)", got)
	}
	// Same route again: all cached, free. Extend by one leg: 1 charged.
	getElevFrom(srv, "1,1,1.1,1.1,1.2,1.2,1.3,1.3", remote, nil)
	if got := elevBucketTokens(svc, ip); got != 7 {
		t.Fatalf("cached legs charged: tokens=%v", got)
	}
	if w := getElevFrom(srv, "1,1,1.1,1.1,1.2,1.2,1.3,1.3,1.4,1.4", remote, nil); w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	if got := elevBucketTokens(svc, ip); got != 6 {
		t.Fatalf("tokens=%v want 6", got)
	}
	// Duplicate legs in one request (A-B-A-B) are one leg, one token.
	if w := getElevFrom(srv, "5,1,5.1,1.1,5,1,5.1,1.1", remote, nil); w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
	if got := elevBucketTokens(svc, ip); got != 5 {
		t.Fatalf("duplicate legs: tokens=%v want 5", got)
	}
}

func TestElevationBudgetAllOrNothing(t *testing.T) {
	f := newFakeElevUpstream(t)
	srv, svc, clk := newElevTestServer(t, f, "")
	withElevLimit(svc, clk, 3)
	ip := "192.0.2.21"
	remote := ip + ":5000"
	before := f.count()
	// 4 uncached legs, 3 tokens: refused, nothing taken, nothing fetched.
	w := getElevFrom(srv, "1,1,1.1,1.1,1.2,1.2,1.3,1.3,1.4,1.4", remote, nil)
	if w.Code != 429 {
		t.Fatalf("code=%d want 429", w.Code)
	}
	if ra, _ := strconv.Atoi(w.Header().Get("Retry-After")); ra < 1 {
		t.Fatalf("Retry-After=%q", w.Header().Get("Retry-After"))
	}
	if f.count() != before {
		t.Fatal("refused request reached upstream")
	}
	if got := elevBucketTokens(svc, ip); got != 3 {
		t.Fatalf("refused request consumed tokens: %v", got)
	}
	// A 3-leg request still fits.
	if w := getElevFrom(srv, "1,1,1.1,1.1,1.2,1.2,1.3,1.3", remote, nil); w.Code != 200 {
		t.Fatalf("code=%d", w.Code)
	}
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
