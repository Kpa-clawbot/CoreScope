package main

// GET /api/elevation/profile — terrain elevation along consecutive legs of a
// route, for the map's line-of-sight view (los_config.go). The terrain is
// the planner's: the same SRTM source (terrain_config.go) and the same
// sampling (sampleLegProfile), so a leg gets the same verdict in both views.
//
// CONTRACT (public/route-los.js depends on it):
//
//	GET /api/elevation/profile?pts=lat1,lon1,lat2,lon2[,lat3,lon3...]
//
//	pts      flat comma list of 2..losMaxPoints points (even count of
//	         numbers), lat in [-90,90], lon in [-180,180].
//	200      ElevationProfileResponse; legs[i] is pts[i] → pts[i+1].
//	202      PlannerJobStatus{"loading_terrain", progress, retry_after_s}
//	         + Retry-After, no-store: some of the request's tiles are still
//	         downloading after losTileWait. The downloads carry on; the
//	         client re-polls the same URL.
//	400      writeError: bad/missing pts, odd count, out of range, too many,
//	         query over 2048 bytes.
//	404      writeError "los disabled" when los.enabled is false.
//	429      writeError "rate limited" + Retry-After (seconds): the request
//	         would start more tile downloads than this client has tokens
//	         left. A request that starts none is never limited.
//
// Points rule: every point must be the position of a known node (GPS set,
// not 0,0) that /api/nodes shows (node_visibility.go), matched at 5
// decimals. This bounds the tiles a client can make the server download to
// those under real node pairs.
//
// Per-leg failures never fail the request: the leg carries an error code
// and no elev_m. Codes, checked in this order: "too_long" (leg >
// losMaxLegKm), "unknown_point" (an endpoint is not a known node),
// "too_many_tiles" (the leg's tiles would take the request past
// losMaxTiles), "upstream" (a tile could not be loaded: downloads paused,
// over budget or failed), "no_terrain" (a sample lies in a tile without
// data). Cache-Control is public, max-age=86400 when every leg is OK or
// too_long, else no-store.

import (
	"fmt"
	"maps"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ElevationProfileResponse is the /api/elevation/profile body.
type ElevationProfileResponse struct {
	// Source attributes the terrain: "SRTM (AWS Open Data)" for the default
	// tiles, else "SRTM (<tile host>)" (terrainSourceName). The planner
	// reports the same string.
	Source string         `json:"source"`
	Legs   []ElevationLeg `json:"legs"`
}

// ElevationLeg is one leg of the requested route.
type ElevationLeg struct {
	// Great-circle (haversine, R=6371 km) length of the leg.
	DistanceKm float64 `json:"distance_km"`
	// ElevM holds terrain heights in metres (0.1 m steps), from the leg's
	// first point to its second, at evenly spaced great-circle fractions
	// t = i/(len-1). The count grows with the leg: planProfileSamples, about
	// one sample per 90 m, 64 to 600. Omitted when Error is set.
	ElevM []float64 `json:"elev_m,omitempty"`
	// Error codes: see the file comment.
	Error string `json:"error,omitempty"`
}

// Leg error codes. The ones the planner profile shares mean the same there.
const (
	elevErrTooLong      = plannerErrTooLong
	elevErrUnknownPoint = "unknown_point"
	elevErrTooManyTiles = plannerErrTooManyTiles
	elevErrUpstream     = "upstream"
	elevErrNoTerrain    = plannerErrNoTerrain
)

const (
	elevEarthRadiusKm    = 6371.0
	elevSamePointRadians = 1e-9
	elevMaxRawQuery      = 2048
	elevNodeSetTTL       = 60 * time.Second
	elevCacheControl     = "public, max-age=86400"
)

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
	s.elevation.serve(w, r, pts)
}

// serve answers a validated request. It waits up to e.wait for the tiles it
// needs, then answers 200, or 202 while some are still downloading. The
// client is charged one token per download the request starts, all or
// nothing, before any starts; resident tiles, tiles already downloading
// (another request's, or the planner's) and polls cost nothing.
func (e *elevationService) serve(w http.ResponseWriter, r *http.Request, pts []elevPoint) {
	legs, keys := e.plan(pts)
	ts := e.src.tiles(keys)
	failed := map[srtmKey]bool{}
	switch {
	case len(ts.need) == 0:
	case ts.cooling:
		// Downloads are paused: the legs on these tiles are upstream, the
		// rest are answered.
		for _, k := range ts.need {
			failed[k] = true
		}
	default:
		if ts.starts > 0 && !e.allowN(w, r, ts.starts) {
			return
		}
		got, bad := e.src.await(r.Context(), ts.need, e.wait)
		maps.Copy(ts.grids, got)
		maps.Copy(failed, bad)
		if len(ts.grids)+len(failed) < len(keys) {
			writePlannerStatus(w, PlannerJobStatus{
				State:       plannerStateNames[plannerStateTerrain],
				Progress:    math.Round(float64(len(ts.grids))/float64(len(keys))*100) / 100,
				RetryAfterS: losPollAfterS,
			})
			return
		}
	}
	view := srtmTileView(ts.grids)
	out := ElevationProfileResponse{Source: e.src.source, Legs: make([]ElevationLeg, len(legs))}
	cc := elevCacheControl
	for i, l := range legs {
		out.Legs[i] = l.res
		res := &out.Legs[i]
		if res.Error == "" {
			if anyTileIn(l.tiles, failed) {
				res.Error = elevErrUpstream
			} else if elev, ok := sampleLegProfile(view, l.a, l.b); ok {
				res.ElevM = elev
			} else {
				res.Error = elevErrNoTerrain
			}
		}
		if res.Error != "" && res.Error != elevErrTooLong {
			cc = "no-store"
		}
	}
	w.Header().Set("Cache-Control", cc)
	writeJSON(w, out)
}

// anyTileIn reports whether any of keys is in set.
func anyTileIn(keys []srtmKey, set map[srtmKey]bool) bool {
	for _, k := range keys {
		if set[k] {
			return true
		}
	}
	return false
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

// legTiles lists, first seen first, the tiles sampleLegProfile reads for
// leg a→b: the one each of legSamplePoints falls in, as srtmTileView looks
// it up (srtmKeyOf). Only these load, not the leg's bounding box: a 300 km
// diagonal's box holds dozens of tiles, its samples about 10.
func legTiles(a, b geoPoint) []srtmKey {
	var out []srtmKey
	seen := map[srtmKey]bool{}
	for _, p := range legSamplePoints(a, b) {
		if k := srtmKeyOf(p.lat, p.lon); !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	return out
}

// elevVisibility is the node-hiding rule /api/nodes applies; *Server
// implements it (node_visibility.go).
type elevVisibility interface {
	// nodeVisibilityGen changes whenever the rule does.
	nodeVisibilityGen() string
	// visibleNodeInfo returns the current rule; called once per set rebuild.
	visibleNodeInfo() func(n *nodeInfo) bool
}

// elevationService serves route LOS profiles from the shared terrain source.
// It keeps no terrain of its own: tiles live in the source's cache, in
// memory only (the server never writes the DB).
type elevationService struct {
	src *srtmSource
	now func() time.Time
	// wait is the longest a request waits for its tiles before 202.
	wait time.Duration
	// nodes lists the known nodes; only their positions are valid leg
	// endpoints. Nil means none (every leg is unknown_point).
	nodes func() []nodeInfo
	// visibility narrows nodes to those /api/nodes shows
	// (node_visibility.go). Nil (unit tests) hides nothing.
	visibility elevVisibility
	// clientLimiter (ipr/rl) is the per-client limit in tile downloads,
	// charged one token per download a request starts; nil ipr or rl
	// disables it.
	clientLimiter

	refreshMu sync.Mutex // serialises node-set rebuilds

	mu         sync.Mutex
	nodeSet    map[string]struct{}
	nodeSetAt  time.Time
	nodeSetGen string // visibility generation nodeSet was built under
}

// newElevationService builds the service over src, the SRTM source it
// shares with the planner (newTerrainServices); nil builds a private default
// one sized for route LOS (tests).
func newElevationService(src *srtmSource) *elevationService {
	if src == nil {
		src = newSRTMSource(TerrainConfig{}, losMaxTiles)
	}
	return &elevationService{
		src:  src,
		now:  time.Now,
		wait: losTileWait,

		clientLimiter: clientLimiter{tag: "los", what: "terrain tile"},
	}
}

func elevKey(p elevPoint) string { return fmt.Sprintf("%.5f,%.5f", p.lat, p.lon) }

// known reports whether key is the position of a known node that /api/nodes
// shows. The set is rebuilt outside e.mu every elevNodeSetTTL, and at once
// when the hiding rules change, so a node the operator hides stops being a
// valid endpoint on the next request.
func (e *elevationService) known(key string) bool {
	if e.nodes == nil {
		return false
	}
	gen := e.visibilityGen()
	e.mu.Lock()
	due := e.nodeSetDue(gen)
	e.mu.Unlock()
	if due {
		e.refreshMu.Lock()
		// Read the generation before the rule and the nodes: a rule change
		// during the build then leaves a stale generation behind, which the
		// next call rebuilds.
		gen = e.visibilityGen()
		e.mu.Lock()
		due = e.nodeSetDue(gen)
		e.mu.Unlock()
		if due {
			set := e.buildNodeSet()
			e.mu.Lock()
			e.nodeSet, e.nodeSetAt, e.nodeSetGen = set, e.now(), gen
			e.mu.Unlock()
		}
		e.refreshMu.Unlock()
	}
	e.mu.Lock()
	_, ok := e.nodeSet[key]
	e.mu.Unlock()
	return ok
}

func (e *elevationService) visibilityGen() string {
	if e.visibility == nil {
		return ""
	}
	return e.visibility.nodeVisibilityGen()
}

// nodeSetDue reports whether the set must be rebuilt. Caller holds e.mu.
func (e *elevationService) nodeSetDue(gen string) bool {
	return e.nodeSet == nil || e.nodeSetGen != gen || e.now().Sub(e.nodeSetAt) >= elevNodeSetTTL
}

// buildNodeSet keys the positions of the visible nodes with GPS (not 0,0).
func (e *elevationService) buildNodeSet() map[string]struct{} {
	var visible func(n *nodeInfo) bool
	if e.visibility != nil {
		visible = e.visibility.visibleNodeInfo()
	}
	ns := e.nodes()
	set := make(map[string]struct{}, len(ns))
	for i := range ns {
		n := &ns[i]
		if !n.HasGPS || (n.Lat == 0 && n.Lon == 0) {
			continue
		}
		if visible != nil && !visible(n) {
			continue
		}
		set[elevKey(elevPoint{n.Lat, n.Lon})] = struct{}{}
	}
	return set
}

// elevLeg is one requested leg after the checks that need no terrain.
type elevLeg struct {
	res   ElevationLeg // the distance, and the error once decided
	a, b  geoPoint
	tiles []srtmKey // legTiles; nil once res.Error is set
}

// plan runs the checks that need no terrain, in order: too_long,
// unknown_point, too_many_tiles. It returns the legs and the request's
// distinct tiles, first seen first. A leg whose new tiles would take the
// request past losMaxTiles is too_many_tiles and adds none; a later leg may
// still fit.
func (e *elevationService) plan(pts []elevPoint) ([]elevLeg, []srtmKey) {
	legs := make([]elevLeg, len(pts)-1)
	seen := map[srtmKey]bool{}
	var keys []srtmKey
	for i := range legs {
		l := &legs[i]
		pa, pb := pts[i], pts[i+1]
		l.a, l.b = geoPoint{pa.lat, pa.lon}, geoPoint{pb.lat, pb.lon}
		l.res.DistanceKm = losRound(legKm(pa, pb), 1000)
		if l.res.DistanceKm > losMaxLegKm {
			l.res.Error = elevErrTooLong
			continue
		}
		if !e.known(elevKey(pa)) || !e.known(elevKey(pb)) {
			l.res.Error = elevErrUnknownPoint
			continue
		}
		tiles := legTiles(l.a, l.b)
		fresh := 0
		for _, k := range tiles {
			if !seen[k] {
				fresh++
			}
		}
		if len(keys)+fresh > losMaxTiles {
			l.res.Error = elevErrTooManyTiles
			continue
		}
		for _, k := range tiles {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
		l.tiles = tiles
	}
	return legs, keys
}
