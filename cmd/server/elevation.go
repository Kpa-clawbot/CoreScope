package main

// GET /api/elevation/profile — terrain elevation along consecutive legs of a
// route, for the map's line-of-sight view (los_config.go).
//
// CONTRACT (frozen; public/route-los.js depends on it):
//
//	GET /api/elevation/profile?pts=lat1,lon1,lat2,lon2[,lat3,lon3...]
//
//	pts      flat comma list of 2..losMaxPoints points (even count of
//	         numbers), lat in [-90,90], lon in [-180,180].
//	200      ElevationProfileResponse; legs[i] is pts[i] → pts[i+1].
//	400      writeError: bad/missing pts, odd count, out of range, too many,
//	         query over 2048 bytes.
//	404      writeError "los disabled" when los.enabled is false.
//	429      writeError "rate limited" + Retry-After (seconds): this client
//	         needs upstream fetches and used up its per-IP tokens. Fully
//	         cached requests are never limited.
//
// Points rule: every point must be the position of a known node (GPS set,
// not 0,0), matched at 5 decimals. This bounds the cache key space to real
// node pairs so the endpoint cannot be used to drain the elevation quota.
//
// Per-leg failures never fail the request: the leg carries an error code
// and no elev_m. Codes: "too_long" (leg > losMaxLegKm), "unknown_point"
// (an endpoint is not a known node), "upstream" (elevation service failed,
// cooling down, or over budget). Cache-Control is public, max-age=86400
// unless a leg has an error other than too_long, then no-store.

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// ElevationProfileResponse is the /api/elevation/profile body.
type ElevationProfileResponse struct {
	// Samples per leg, both endpoints included (losSamples).
	Samples int `json:"samples"`
	// Source is the elevation host, for attribution (e.g.
	// "api.open-meteo.com").
	Source string         `json:"source"`
	Legs   []ElevationLeg `json:"legs"`
}

// ElevationLeg is one leg of the requested route.
type ElevationLeg struct {
	// Great-circle (haversine, R=6371 km) length of the leg.
	DistanceKm float64 `json:"distance_km"`
	// ElevM holds Samples terrain heights in metres, from the leg's first
	// point to its second, at evenly spaced great-circle fractions
	// t = i/(Samples-1). Omitted when Error is set.
	ElevM []float64 `json:"elev_m,omitempty"`
	// Error codes: "too_long" (leg > losMaxLegKm), "unknown_point" (an
	// endpoint is not a known node), "upstream" (elevation service failed or
	// rate-limited).
	Error string `json:"error,omitempty"`
}

// Leg error codes.
const (
	elevErrTooLong  = "too_long"
	elevErrUpstream = "upstream"
	// elevErrUnknownPoint: a leg endpoint is not the position of a known node.
	elevErrUnknownPoint = "unknown_point"
)

const (
	elevCacheCapacity    = 4096
	elevMaxConcurrent    = 4
	elevUpstreamTimeout  = 10 * time.Second
	elevMaxBodyBytes     = 256 << 10
	elevErrBodyDrain     = 4096
	elevEarthRadiusKm    = 6371.0
	elevSamePointRadians = 1e-9
	elevMaxRawQuery      = 2048
	elevNodeSetTTL       = 60 * time.Second
	// elevIPTokens is the per-client budget in legs per minute; it must cover
	// one full route (losMaxPoints-1 = 15 legs).
	elevIPTokens = 30
)

// elevBackoff is the cooldown after the 1st, 2nd, 3rd... consecutive upstream
// failure; the last step repeats.
var elevBackoff = []time.Duration{60 * time.Second, 5 * time.Minute, 30 * time.Minute, time.Hour}

func (s *Server) handleElevationProfile(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.LOS.enabled() || s.elevation == nil {
		writeError(w, http.StatusNotFound, "los disabled")
		return
	}
	if len(r.URL.RawQuery) > elevMaxRawQuery {
		writeError(w, http.StatusBadRequest, "pts too long")
		return
	}
	pts, msg := parseElevationPts(r.URL.Query().Get("pts"))
	if msg != "" {
		writeError(w, http.StatusBadRequest, msg)
		return
	}
	e := s.elevation
	plans := make([]elevPlan, len(pts)-1)
	// The client is charged one token per distinct uncached leg (a leg and
	// its reverse share a key), all or nothing, so a route cannot spend the
	// upstream budget at one token per request.
	uncached := map[string]struct{}{}
	for i := range plans {
		plans[i] = e.plan(pts[i], pts[i+1])
		if plans[i].res.Error == "" && !e.cached(plans[i].key) {
			uncached[plans[i].key] = struct{}{}
		}
	}
	if len(uncached) > 0 && !e.allowN(w, r, len(uncached)) {
		return
	}
	legs := make([]ElevationLeg, len(plans))
	var wg sync.WaitGroup
	for i := range plans {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			legs[i] = e.run(plans[i])
		}(i)
	}
	wg.Wait()
	cc := "public, max-age=86400"
	for _, l := range legs {
		if l.Error != "" && l.Error != elevErrTooLong {
			cc = "no-store"
			break
		}
	}
	w.Header().Set("Cache-Control", cc)
	writeJSON(w, ElevationProfileResponse{Samples: losSamples, Source: e.source, Legs: legs})
}

type elevPoint struct{ lat, lon float64 }

// parseElevationPts validates the flat "lat,lon,lat,lon..." list. The second
// return value is a 400 message, empty when valid.
func parseElevationPts(raw string) ([]elevPoint, string) {
	if raw == "" {
		return nil, "pts required"
	}
	tooMany := fmt.Sprintf("pts must hold 2 to %d points", losMaxPoints)
	if strings.Count(raw, ",") > 2*losMaxPoints-1 {
		return nil, tooMany
	}
	parts := strings.SplitN(raw, ",", 2*losMaxPoints+1)
	if len(parts)%2 != 0 {
		return nil, "pts must hold lat,lon pairs"
	}
	n := len(parts) / 2
	if n < 2 || n > losMaxPoints {
		return nil, tooMany
	}
	pts := make([]elevPoint, n)
	for i := 0; i < n; i++ {
		lat, err1 := strconv.ParseFloat(strings.TrimSpace(parts[2*i]), 64)
		lon, err2 := strconv.ParseFloat(strings.TrimSpace(parts[2*i+1]), 64)
		if err1 != nil || err2 != nil || math.IsNaN(lat) || math.IsInf(lat, 0) || math.IsNaN(lon) || math.IsInf(lon, 0) {
			return nil, "pts contains an invalid number"
		}
		if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
			return nil, "pts out of range"
		}
		pts[i] = elevPoint{lat, lon}
	}
	return pts, ""
}

func legKm(a, b elevPoint) float64 { return haversineKm(a.lat, a.lon, b.lat, b.lon) }

// greatCircleSamples returns n points from a to b (inclusive) along the great
// circle, using the spherical intermediate-point formula.
func greatCircleSamples(a, b elevPoint, n int) []elevPoint {
	out := make([]elevPoint, n)
	p1, l1 := a.lat*math.Pi/180, a.lon*math.Pi/180
	p2, l2 := b.lat*math.Pi/180, b.lon*math.Pi/180
	d := legKm(a, b) / elevEarthRadiusKm
	if d < elevSamePointRadians {
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
		out[i] = elevPoint{
			lat: math.Atan2(z, math.Hypot(x, y)) * 180 / math.Pi,
			lon: math.Atan2(y, x) * 180 / math.Pi,
		}
	}
	// Endpoints are exact, not trig round-trips.
	out[0], out[n-1] = a, b
	return out
}

// elevHTTPDoer is the slice of *http.Client the service needs (test seam).
type elevHTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

type elevCacheEntry struct {
	key  string
	elev []float64 // canonical direction; never handed out
}

// elevWindow is a fixed-window counter.
type elevWindow struct {
	every time.Duration
	limit int
	at    time.Time
	n     int
}

// roll starts a new window when the current one has expired.
func (w *elevWindow) roll(now time.Time) {
	if w.at.IsZero() || now.Sub(w.at) >= w.every {
		w.at, w.n = now, 0
	}
}

// elevationService fetches and caches leg profiles. The cache is in-memory
// only (the server never writes the DB).
type elevationService struct {
	baseURL   string
	source    string
	userAgent string
	client    elevHTTPDoer
	now       func() time.Time
	// capacity is the max number of cached legs.
	capacity int
	// timeout bounds one upstream fetch, semaphore wait included.
	timeout time.Duration
	// nodes lists the known nodes; only their positions are valid leg
	// endpoints. Nil means none (every leg is unknown_point).
	nodes func() []nodeInfo
	// clientLimiter (ipr/rl) is the per-client limit in legs, charged one
	// token per distinct uncached leg; nil ipr or rl disables it.
	clientLimiter

	sf  singleflight.Group
	sem chan struct{}

	refreshMu sync.Mutex // serialises node-set rebuilds

	mu        sync.Mutex
	ll        *list.List
	items     map[string]*list.Element
	nodeSet   map[string]struct{}
	nodeSetAt time.Time
	fails     int       // consecutive upstream failures
	coolTo    time.Time // upstream calls blocked until then
	minute    elevWindow
	hour      elevWindow
	day       elevWindow
}

// newElevationService builds the service from the resolved config.
func newElevationService(cfg LOSConfig) *elevationService {
	src := ""
	if u, err := url.Parse(cfg.ElevationURL); err == nil {
		src = u.Host
	}
	ua := "CoreScope"
	if Version != "" {
		ua += "/" + Version
	}
	perMin, perHour, perDay := cfg.LegsPerMinute, cfg.LegsPerHour, cfg.LegsPerDay
	if perMin <= 0 {
		perMin = defaultLOSLegsPerMinute
	}
	if perHour <= 0 {
		perHour = defaultLOSLegsPerHour
	}
	if perDay <= 0 {
		perDay = defaultLOSLegsPerDay
	}
	return &elevationService{
		baseURL:   cfg.ElevationURL,
		source:    src,
		userAgent: ua,
		client: &http.Client{
			Timeout: elevUpstreamTimeout,
			// An elevation host has no business redirecting us elsewhere.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		now:      time.Now,
		capacity: elevCacheCapacity,
		timeout:  elevUpstreamTimeout,
		sem:      make(chan struct{}, elevMaxConcurrent),
		ll:       list.New(),
		items:    make(map[string]*list.Element),
		minute:   elevWindow{every: time.Minute, limit: perMin},
		hour:     elevWindow{every: time.Hour, limit: perHour},
		day:      elevWindow{every: 24 * time.Hour, limit: perDay},

		clientLimiter: clientLimiter{tag: "los", what: "elevation"},
	}
}

func elevKey(p elevPoint) string { return fmt.Sprintf("%.5f,%.5f", p.lat, p.lon) }

// parseElevKey is the inverse of elevKey.
func parseElevKey(k string) elevPoint {
	la, lo, _ := strings.Cut(k, ",")
	lat, _ := strconv.ParseFloat(la, 64)
	lon, _ := strconv.ParseFloat(lo, 64)
	return elevPoint{lat, lon}
}

func reversedCopy(in []float64) []float64 {
	out := make([]float64, len(in))
	for i, v := range in {
		out[len(in)-1-i] = v
	}
	return out
}

// known reports whether key is the position of a known node. The set is
// rebuilt at most every elevNodeSetTTL, outside e.mu.
func (e *elevationService) known(key string) bool {
	if e.nodes == nil {
		return false
	}
	e.mu.Lock()
	due := e.nodeSet == nil || e.now().Sub(e.nodeSetAt) >= elevNodeSetTTL
	e.mu.Unlock()
	if due {
		e.refreshMu.Lock()
		e.mu.Lock()
		due = e.nodeSet == nil || e.now().Sub(e.nodeSetAt) >= elevNodeSetTTL
		e.mu.Unlock()
		if due {
			ns := e.nodes()
			set := make(map[string]struct{}, len(ns))
			for _, n := range ns {
				if n.HasGPS && !(n.Lat == 0 && n.Lon == 0) {
					set[elevKey(elevPoint{n.Lat, n.Lon})] = struct{}{}
				}
			}
			e.mu.Lock()
			e.nodeSet, e.nodeSetAt = set, e.now()
			e.mu.Unlock()
		}
		e.refreshMu.Unlock()
	}
	e.mu.Lock()
	_, ok := e.nodeSet[key]
	e.mu.Unlock()
	return ok
}

// elevPlan is a leg after the checks that need no upstream: res carries the
// distance and, when the leg is already decided, its error.
type elevPlan struct {
	res      ElevationLeg
	key      string
	a, b     elevPoint // canonical order, quantised to the key's 5 decimals
	reversed bool
}

// plan runs the cheap checks, in order: too_long, unknown_point.
func (e *elevationService) plan(a, b elevPoint) elevPlan {
	p := elevPlan{res: ElevationLeg{DistanceKm: math.Round(legKm(a, b)*1000) / 1000}}
	if p.res.DistanceKm > losMaxLegKm {
		p.res.Error = elevErrTooLong
		return p
	}
	ka, kb := elevKey(a), elevKey(b)
	if !e.known(ka) || !e.known(kb) {
		p.res.Error = elevErrUnknownPoint
		return p
	}
	if kb < ka {
		ka, kb = kb, ka
		p.reversed = true
	}
	p.key = ka + "|" + kb
	// Sample from the quantised endpoints so the cached profile does not
	// depend on which spelling of the coordinates asked first.
	p.a, p.b = parseElevKey(ka), parseElevKey(kb)
	return p
}

// leg builds one ElevationLeg; failures are reported on the leg.
func (e *elevationService) leg(a, b elevPoint) ElevationLeg { return e.run(e.plan(a, b)) }

// run serves a planned leg from the cache or one shared upstream fetch.
func (e *elevationService) run(p elevPlan) ElevationLeg {
	res := p.res
	if res.Error != "" {
		return res
	}
	elev, ok := e.cacheGet(p.key)
	if !ok {
		v, err, _ := e.sf.Do(p.key, func() (interface{}, error) {
			if cached, ok := e.cacheGet(p.key); ok {
				return cached, nil
			}
			got, err := e.fetch(p.a, p.b)
			if err != nil {
				return nil, err
			}
			e.cachePut(p.key, got)
			return got, nil
		})
		if err != nil {
			res.Error = elevErrUpstream
			return res
		}
		elev = v.([]float64)
	}
	// elev may be shared with other callers or the cache: always copy.
	if p.reversed {
		res.ElevM = reversedCopy(elev)
	} else {
		res.ElevM = append([]float64(nil), elev...)
	}
	return res
}

// cached reports whether key is cached, without touching LRU order.
func (e *elevationService) cached(key string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.items[key]
	return ok
}

// cacheGet returns a private copy of the cached canonical profile.
func (e *elevationService) cacheGet(key string) ([]float64, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	el, ok := e.items[key]
	if !ok {
		return nil, false
	}
	e.ll.MoveToFront(el)
	return append([]float64(nil), el.Value.(*elevCacheEntry).elev...), true
}

func (e *elevationService) cachePut(key string, elev []float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	stored := append([]float64(nil), elev...)
	if el, ok := e.items[key]; ok {
		el.Value.(*elevCacheEntry).elev = stored
		e.ll.MoveToFront(el)
		return
	}
	e.items[key] = e.ll.PushFront(&elevCacheEntry{key: key, elev: stored})
	for e.capacity > 0 && e.ll.Len() > e.capacity {
		last := e.ll.Back()
		e.ll.Remove(last)
		delete(e.items, last.Value.(*elevCacheEntry).key)
	}
}

func (e *elevationService) cacheLen() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ll.Len()
}

var errElevUpstream = errors.New("elevation upstream unavailable")

// admit applies the cooldown and the minute/hour/day budgets. A call is
// counted against all three only when every one allows it.
func (e *elevationService) admit() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	if now.Before(e.coolTo) {
		return false
	}
	ws := [...]*elevWindow{&e.minute, &e.hour, &e.day}
	for _, w := range ws {
		w.roll(now)
		if w.n >= w.limit {
			return false
		}
	}
	for _, w := range ws {
		w.n++
	}
	return true
}

// openCooldown records an upstream failure and blocks calls for the next
// step of the back-off ladder. Failures that land while a cooldown is already
// running (calls admitted just before it opened) do not escalate it.
func (e *elevationService) openCooldown() {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	if now.Before(e.coolTo) {
		return
	}
	step := e.fails
	if step >= len(elevBackoff) {
		step = len(elevBackoff) - 1
	}
	e.fails++
	e.coolTo = now.Add(elevBackoff[step])
}

func (e *elevationService) resetFailures() {
	e.mu.Lock()
	e.fails = 0
	e.mu.Unlock()
}

type elevUpstreamBody struct {
	Elevation []*float64 `json:"elevation"`
}

// fetch samples the leg a→b and queries the upstream once.
func (e *elevationService) fetch(a, b elevPoint) ([]float64, error) {
	// Detached from any request context: a shared singleflight call must not
	// fail because one client went away.
	ctx, cancel := context.WithTimeout(context.Background(), e.timeout)
	defer cancel()
	select {
	case e.sem <- struct{}{}:
		defer func() { <-e.sem }()
	case <-ctx.Done():
		return nil, errElevUpstream
	}
	// Budget is spent only once a slot is held, so a wait that times out
	// costs nothing.
	if !e.admit() {
		return nil, errElevUpstream
	}

	samples := greatCircleSamples(a, b, losSamples)
	lats := make([]string, len(samples))
	lons := make([]string, len(samples))
	for i, p := range samples {
		lats[i] = strconv.FormatFloat(p.lat, 'f', 5, 64)
		lons[i] = strconv.FormatFloat(p.lon, 'f', 5, 64)
	}
	u, err := url.Parse(e.baseURL)
	if err != nil {
		return nil, errElevUpstream
	}
	q := u.Query()
	q.Set("latitude", strings.Join(lats, ","))
	q.Set("longitude", strings.Join(lons, ","))
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errElevUpstream
	}
	req.Header.Set("User-Agent", e.userAgent)
	resp, err := e.client.Do(req)
	if err != nil {
		// Unwrap: url.Error text embeds the URL, which may carry an apikey.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		log.Printf("[los] elevation request failed: %v", err)
		e.openCooldown()
		return nil, errElevUpstream
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, elevErrBodyDrain))
		switch {
		case resp.StatusCode == http.StatusTooManyRequests, resp.StatusCode >= 500,
			resp.StatusCode == http.StatusBadRequest, resp.StatusCode == http.StatusUnauthorized,
			resp.StatusCode == http.StatusForbidden:
			e.openCooldown()
			log.Printf("[los] elevation upstream status %d; backing off", resp.StatusCode)
		default:
			log.Printf("[los] elevation upstream status %d", resp.StatusCode)
		}
		return nil, errElevUpstream
	}
	e.resetFailures()
	var body elevUpstreamBody
	if err := json.NewDecoder(io.LimitReader(resp.Body, elevMaxBodyBytes)).Decode(&body); err != nil {
		return nil, errElevUpstream
	}
	if len(body.Elevation) != losSamples {
		log.Printf("[los] elevation upstream returned %d values, want %d", len(body.Elevation), losSamples)
		return nil, errElevUpstream
	}
	out := make([]float64, losSamples)
	for i, v := range body.Elevation {
		if v != nil && !math.IsNaN(*v) && !math.IsInf(*v, 0) {
			out[i] = *v
		}
	}
	return out, nil
}
