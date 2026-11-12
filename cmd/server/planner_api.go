package main

// /api/planner/* — the repeater planner's public endpoints. Wire types,
// constants and the 200/202 job protocol are in planner_types.go.
//
// Abuse model. Every request is quantised before anything else happens
// (points to 0.001°, coverage boxes outward to a 0.05° grid, antenna heights
// to 1 m, ranges to a fixed set), so the cache-key space is small and compute
// only ever sees quantised values. Work runs as background jobs, one per
// key: a request waits at most plannerFastWait for its job and otherwise gets
// 202 + PlannerJobStatus to re-poll. Starting a job costs the client a per-IP
// token and the server a global one; cache hits and polls of a live job are
// free. Compute is kept away from the live app by slot semaphores (coverage
// 1, relay + profile 2), caps on live and queued jobs, per-stage timeouts, and
// cancellation of any job nobody has polled for plannerAbandonAfter.
//
// Invariants:
//   - At most one live job per key. A successful job caches its result before
//     it leaves p.jobs (both under p.mu), so no request can see neither and
//     start a duplicate.
//   - Every job, however it ends (result, error, panic, cancel), gives back
//     its slot, leaves p.jobs and the queue count, and closes done once.
//   - A job loads its terrain before it takes a compute slot, so a download
//     never holds up jobs whose terrain is already here. It counts as queued
//     from creation until it holds the slot.
//   - A key whose job failed (anything but being abandoned or shut down) is
//     answered 503 for plannerFailedFor without a new job or a token, so a
//     query that times out or crashes cannot be replayed into the compute
//     slot.
//   - Node data is read outside the store's long-held locks: the node list
//     is the store's shared cache (read only), LastHeardMap holds the read
//     lock for one map lookup per pubkey, edges are copied under the graph's
//     read lock. Nothing /api/nodes hides (blacklist, hidden names) reaches
//     a response, a geo check or a cache key.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"runtime/debug"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	plannerCacheControl = "public, max-age=300"

	plannerMaxRawQuery    = 512
	plannerCoordScale     = 1000.0 // points quantised to 0.001°
	plannerGridPerDeg     = 20     // coverage boxes snapped to 0.05°
	plannerGridEps        = 1e-9   // in grid cells: a value on a grid line stays there
	plannerCoverageMinLat = -56.0  // SRTM coverage
	plannerCoverageMaxLat = 60.0
	plannerMinAntennaM    = 1.0
	plannerMaxAntennaM    = 100.0
	plannerDefaultSites   = 3

	plannerProfilePadDeg = 0.01 // covers the great-circle bulge of a 100 km leg (plannerProfilePad)
	plannerRelayPadFrac  = 0.3  // relay corridor: A,B ± max(0.3·D, 2 km)
	plannerRelayPadMinKm = 2.0
	plannerMinLegTiles   = 6    // profile/relay tile caps never go below this
	plannerAreaNodeKm    = 10.0 // coverage needs a repeater/room within bbox + this
	// plannerObservedMinScore is the Score×Confidence an observed neighbor
	// edge needs to count as a backbone link (same floor as the neighbor
	// graph view's default minScore).
	plannerObservedMinScore = 0.1

	plannerKnownTTL        = 60 * time.Second
	plannerResultTTL       = 10 * time.Minute
	plannerCovCacheItems   = 128
	plannerCovCacheBytes   = 32 << 20
	plannerCheapCacheItems = 1024
	plannerCheapCacheBytes = 32 << 20

	plannerCovSlots          = 1
	plannerCheapSlots        = 2
	plannerMaxQueuedCoverage = 4
	plannerMaxLiveJobs       = 8
	plannerIPJobsPerMin      = 6
	plannerGlobalJobsPerMin  = 30
	plannerFullRetryS        = 5

	plannerFastWait       = 1500 * time.Millisecond
	plannerTerrainTimeout = 60 * time.Second
	plannerComputeTimeout = 20 * time.Second
	plannerAbandonAfter   = 15 * time.Second
	plannerBusyFor        = 30 * time.Second
	plannerFailedFor      = 5 * time.Minute
	plannerWatchEvery     = time.Second
)

// Endpoints, as they appear in cache keys and log lines.
const (
	plannerProfile  = "profile"
	plannerRelay    = "relay"
	plannerCoverage = "coverage"
)

// 400 codes the UI matches on (leg errors use the first three too).
// area_too_large: a coverage box side is over the size limit.
// too_many_tiles: the request's terrain view spans more 1° tiles than its
// endpoint's cap (plannerTileLimits).
const (
	plannerErrTooLong      = "too_long"
	plannerErrOutOfArea    = "out_of_area"
	plannerErrNoTerrain    = "no_terrain"
	plannerErrAreaTooLarge = "area_too_large"
	plannerErrTooManyTiles = "too_many_tiles"
	plannerErrNoNodes      = "no_nodes_in_area"
)

var (
	errPlannerAbandoned = errors.New("planner: job not polled")
	errPlannerClosed    = errors.New("planner: shutting down")
	errPlannerPanic     = errors.New("planner: job panicked")
)

// Answers for a key that failed recently.
const (
	plannerMsgTerrainBusy  = "terrain busy"
	plannerMsgFailedRecent = "planner job failed recently"
)

// ─── Data the planner reads ─────────────────────────────────────────────────

// plannerData is everything the planner reads from the rest of the server.
// serverPlannerData is the real one; tests inject fixed data.
type plannerData interface {
	// nodes is the node list. It is shared with other readers: never modify it.
	nodes() []nodeInfo
	// lastHeard is the certain last-heard time of each pubkey that has one.
	lastHeard(pubkeys []string) map[string]time.Time
	// edges is a private copy of the neighbor edges between pubkeys.
	edges(pubkeys []string) []NeighborEdge
	// filter is what /api/nodes hides, plus each role's silent window.
	filter() plannerNodeFilter
	// filterGen changes whenever the hiding rules change.
	filterGen() string
}

type serverPlannerData struct{ s *Server }

func (d serverPlannerData) nodes() []nodeInfo {
	if d.s == nil || d.s.store == nil {
		return nil
	}
	ns, _ := d.s.store.getCachedNodesAndPM()
	return ns
}

func (d serverPlannerData) lastHeard(pubkeys []string) map[string]time.Time {
	if d.s == nil || d.s.store == nil || len(pubkeys) == 0 {
		return nil
	}
	m, _ := d.s.store.LastHeardMap(pubkeys)
	return m
}

func (d serverPlannerData) edges(pubkeys []string) []NeighborEdge {
	if d.s == nil || len(pubkeys) == 0 {
		return nil
	}
	return d.s.getNeighborGraph().snapshotEdgesAmong(pubkeys)
}

func (d serverPlannerData) filter() plannerNodeFilter {
	var cfg *Config
	if d.s != nil {
		cfg = d.s.cfg
	}
	if cfg == nil {
		return plannerNodeFilter{}
	}
	h := cfg.GetHealthThresholds()
	return plannerNodeFilter{
		// Exactly what /api/nodes hides (node_visibility.go): a node the
		// operator filtered out of the API must not surface through the
		// planner, and a foreign one it shows is usable.
		Visible: d.s.visibleNodeInfo(),
		SilentAfter: func(role string) time.Duration {
			_, silentMs := h.GetHealthMs(role)
			return time.Duration(silentMs) * time.Millisecond
		},
	}
}

func (d serverPlannerData) filterGen() string { return d.s.nodeVisibilityGen() }

// ─── Service ────────────────────────────────────────────────────────────────

// plannerService holds terrain, caches, limiters and the job runner.
type plannerService struct {
	cfg     PlannerConfig // resolved
	freqMHz float64       // radio defaults from the los block, like the LOS tool
	kFactor float64
	tiles   plannerTileLimits
	source  string // terrain attribution in every response (terrainSourceName)

	src      terrainSource
	data     plannerData
	coverage func(ctx context.Context, v terrainView, in planInputs, progress func(float64)) (PlannerPlanResponse, error)
	relay    func(ctx context.Context, v terrainView, nodes []plannerNode, p PlannerRelayParams, progress func(float64)) (PlannerRelayResponse, error)
	now      func() time.Time

	// limit charges the client for each job it starts; global caps new jobs
	// for everyone.
	limit  clientLimiter
	global *rateLimiter

	// Timings (fields so tests can shorten them).
	fastWait, terrainTimeout, computeTimeout time.Duration
	abandonAfter, busyFor, failedFor         time.Duration
	watchEvery                               time.Duration

	knownMu sync.Mutex // one rebuild at a time
	known   atomic.Pointer[plannerKnown]

	covCache, cheapCache *plannerLRU
	covSlots, cheapSlots chan struct{}

	epoch  time.Time       // origin of plannerJob.polledAt
	ctx    context.Context // parent of every job; Close cancels it
	cancel context.CancelCauseFunc
	wg     sync.WaitGroup

	mu        sync.Mutex
	closed    bool
	jobs      map[string]*plannerJob
	covQueued int                      // coverage jobs not yet holding a slot
	recent    map[string]plannerRecent // keys answered 503 without a new job
}

// plannerRecent is a key's recent failure: requests for it get msg until
// the entry lapses.
type plannerRecent struct {
	until time.Time
	msg   string
}

// newPlannerService builds the service from the resolved config over terrain,
// the SRTM source it shares with route LOS (newTerrainServices); nil builds a
// private default one sized for this planner (tests). It starts no
// goroutine; each job brings its own and Close stops them.
func newPlannerService(cfg PlannerConfig, s *Server, terrain *srtmSource) *plannerService {
	rc, _ := cfg.resolved()
	if terrain == nil {
		terrain = newSRTMSource(TerrainConfig{}, rc.tileLimits().max())
	}
	var los *LOSConfig
	var trusted []string
	if s != nil && s.cfg != nil {
		los = s.cfg.LOS
		trusted = s.cfg.WSTrustedProxies()
	}
	radio, _ := los.resolved()
	ctx, cancel := context.WithCancelCause(context.Background())
	return &plannerService{
		cfg:      rc,
		freqMHz:  radio.FrequencyMHz,
		kFactor:  radio.KFactor,
		tiles:    rc.tileLimits(),
		source:   terrain.source,
		src:      terrain,
		data:     serverPlannerData{s},
		coverage: runCoveragePlan,
		relay:    runRelaySearch,
		now:      time.Now,
		limit: clientLimiter{
			ipr: &wsLimiter{state: map[string]*wsIPState{},
				trustedProxies: parseCIDRList(trusted, "webSocket.trustedProxies")},
			rl:  newRateLimiter(plannerIPJobsPerMin, time.Minute),
			tag: "planner", what: "planner",
		},
		global:         newRateLimiter(plannerGlobalJobsPerMin, time.Minute),
		fastWait:       plannerFastWait,
		terrainTimeout: plannerTerrainTimeout,
		computeTimeout: plannerComputeTimeout,
		abandonAfter:   plannerAbandonAfter,
		busyFor:        plannerBusyFor,
		failedFor:      plannerFailedFor,
		watchEvery:     plannerWatchEvery,
		covCache:       newPlannerLRU(plannerCovCacheItems, plannerCovCacheBytes, plannerResultTTL),
		cheapCache:     newPlannerLRU(plannerCheapCacheItems, plannerCheapCacheBytes, plannerResultTTL),
		covSlots:       make(chan struct{}, plannerCovSlots),
		cheapSlots:     make(chan struct{}, plannerCheapSlots),
		epoch:          time.Now(),
		ctx:            ctx,
		cancel:         cancel,
		jobs:           map[string]*plannerJob{},
		recent:         map[string]plannerRecent{},
	}
}

// Close cancels every job, refuses new ones and waits for the job
// goroutines to return.
func (p *plannerService) Close() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.cancel(errPlannerClosed)
	p.wg.Wait()
}

func (s *Server) plannerDisabled(w http.ResponseWriter) bool {
	if !s.cfg.Planner.enabled() || s.planner == nil {
		writeError(w, http.StatusNotFound, "planner disabled")
		return true
	}
	return false
}

func (s *Server) handlePlannerProfile(w http.ResponseWriter, r *http.Request) {
	if s.plannerDisabled(w) {
		return
	}
	s.planner.handleProfile(w, r)
}

func (s *Server) handlePlannerRelay(w http.ResponseWriter, r *http.Request) {
	if s.plannerDisabled(w) {
		return
	}
	s.planner.handleRelay(w, r)
}

func (s *Server) handlePlannerCoverage(w http.ResponseWriter, r *http.Request) {
	if s.plannerDisabled(w) {
		return
	}
	s.planner.handleCoverage(w, r)
}

// ─── /api/planner/profile ───────────────────────────────────────────────────

func (p *plannerService) handleProfile(w http.ResponseWriter, r *http.Request) {
	q, rej := plannerParseQuery(r.URL.RawQuery, "pts")
	if rej != nil {
		rej.write(w)
		return
	}
	raw, ok := q["pts"]
	if !ok {
		plannerBadRequest(w, "pts required")
		return
	}
	pts, msg := plannerParsePts(raw)
	if msg != "" {
		plannerBadRequest(w, msg)
		return
	}
	known := p.knownSet()
	far := make([]bool, len(pts))
	for i, pt := range pts {
		far[i] = !known.near(pt)
	}
	legs := make([]PlannerProfileLeg, len(pts)-1)
	var key strings.Builder
	key.WriteString(plannerProfile)
	for _, pt := range pts {
		fmt.Fprintf(&key, "|%d,%d", plannerMilli(pt.Lat), plannerMilli(pt.Lon))
	}
	key.WriteByte('|')
	var bb geoBBox
	valid := 0
	for i := range legs {
		d := geoDistKm(pts[i], pts[i+1])
		legs[i].DistanceKm = losRound(d, 1000)
		switch {
		case d > p.cfg.MaxLegKm:
			legs[i].Error = plannerErrTooLong
			key.WriteByte('L')
		case far[i] || far[i+1]:
			legs[i].Error = plannerErrOutOfArea
			key.WriteByte('A')
		default:
			key.WriteByte('-')
			if valid == 0 {
				bb = geoBBox{S: pts[i].Lat, W: pts[i].Lon, N: pts[i].Lat, E: pts[i].Lon}
			}
			bb = plannerBBoxAdd(plannerBBoxAdd(bb, pts[i]), pts[i+1])
			valid++
		}
	}
	if valid == 0 {
		// Nothing to sample: the answer is fully decided here, no job.
		body, err := plannerEncode(PlannerProfileResponse{Source: p.source, Legs: legs})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "planner failed")
			return
		}
		writePlannerResult(w, body)
		return
	}
	bb = plannerProfileView(bb, plannerProfilePad(p.cfg.MaxLegKm))
	if p.src.TileCount(bb) > p.tiles.Profile {
		plannerBadRequest(w, plannerErrTooManyTiles)
		return
	}
	p.serve(w, r, plannerJobSpec{
		endpoint: plannerProfile,
		key:      key.String(),
		view:     bb,
		compute: func(ctx context.Context, v terrainView, progress func(float64)) (any, error) {
			res, err := planProfileLegs(ctx, v, pts, legs, progress)
			res.Source = p.source
			return res, err
		},
	})
}

// plannerProfilePad is the profile view's margin in degrees for legs up to
// maxLegKm. A great circle bulges poleward of its endpoints by about
// tan(lat)·(d/R)²/8, so the 0.01° that covers a 100 km leg (0.003° at 60°,
// 0.01° at 80°) scales with the square of the leg.
func plannerProfilePad(maxLegKm float64) float64 {
	scale := maxLegKm / defaultPlannerMaxLegKm
	return plannerProfilePadDeg * math.Max(1, scale*scale)
}

// plannerProfileView is the terrain a profile over legs (the bbox of its
// valid legs' endpoints) reads: grown by padDeg on every side for the
// great-circle bulge.
func plannerProfileView(legs geoBBox, padDeg float64) geoBBox {
	return geoBBox{S: legs.S - padDeg, W: legs.W - padDeg, N: legs.N + padDeg, E: legs.E + padDeg}
}

// planProfileLegs samples every leg without an error (sampleLegProfile, as
// route LOS does); legs is the request's pre-checked template and is not
// modified. A leg with any sample on a tile without data is no_terrain
// (terrain has no elevation there, never a 0 m valley), and the response
// carries the terrain warning. The caller sets Source.
func planProfileLegs(ctx context.Context, v terrainView, pts []geoPoint, legs []PlannerProfileLeg,
	progress func(float64)) (PlannerProfileResponse, error) {
	out := PlannerProfileResponse{Legs: slices.Clone(legs)}
	if w := plannerTerrainWarning(v); w != "" {
		out.Warnings = []string{w}
	}
	for i := range out.Legs {
		if err := ctx.Err(); err != nil {
			return PlannerProfileResponse{}, err
		}
		if leg := &out.Legs[i]; leg.Error == "" {
			if elev, ok := sampleLegProfile(v, pts[i], pts[i+1]); ok {
				leg.ElevM = elev
			} else {
				leg.Error = plannerErrNoTerrain
			}
		}
		progress(float64(i+1) / float64(len(out.Legs)))
	}
	return out, nil
}

// plannerTerrainWarning is the warning to show when some of v's tiles have
// no data, which every endpoint skips; "" when it has none or cannot tell.
func plannerTerrainWarning(v terrainView) string {
	tc, ok := v.(terrainCoverage)
	if !ok {
		return ""
	}
	missing, total := tc.MissingTiles()
	if missing == 0 {
		return ""
	}
	return fmt.Sprintf("Terrain missing for %d of %d tiles (open sea or unavailable): sites and links there were skipped",
		missing, total)
}

// ─── /api/planner/relay ─────────────────────────────────────────────────────

func (p *plannerService) handleRelay(w http.ResponseWriter, r *http.Request) {
	q, rej := plannerParseQuery(r.URL.RawQuery, "a", "b", "ha", "hb", "hr", "range")
	if rej != nil {
		rej.write(w)
		return
	}
	var ends [2]geoPoint
	for i, k := range [2]string{"a", "b"} {
		raw, ok := q[k]
		if !ok {
			plannerBadRequest(w, k+" required")
			return
		}
		pt, ok := plannerParsePoint(raw)
		if !ok {
			plannerBadRequest(w, k+" must be lat,lon within range")
			return
		}
		ends[i] = pt
	}
	a, b := ends[0], ends[1]
	var heights [3]float64
	for i, k := range [3]string{"ha", "hb", "hr"} {
		h, ok := plannerOptHeight(q, k, p.cfg.NewAntennaM)
		if !ok {
			plannerBadRequest(w, k+" must be a number of metres")
			return
		}
		heights[i] = h
	}
	rangeKm, ok := plannerOptRange(q, p.cfg.DefaultRangeKm, p.cfg.rangesKm())
	if !ok {
		plannerBadRequest(w, plannerRangeMsg(p.cfg.rangesKm()))
		return
	}
	d := geoDistKm(a, b)
	if d > p.cfg.MaxLegKm {
		plannerBadRequest(w, plannerErrTooLong)
		return
	}
	known := p.knownSet()
	if !known.near(a) || !known.near(b) {
		plannerBadRequest(w, plannerErrOutOfArea)
		return
	}
	view := plannerRelayView(a, b)
	if p.src.TileCount(view) > p.tiles.Relay {
		plannerBadRequest(w, plannerErrTooManyTiles)
		return
	}
	params := PlannerRelayParams{
		A:                geoPointJSON{Lat: a.Lat, Lon: a.Lon},
		B:                geoPointJSON{Lat: b.Lat, Lon: b.Lon},
		AntennaAM:        heights[0],
		AntennaBM:        heights[1],
		RelayAntennaM:    heights[2],
		ExistingAntennaM: p.cfg.ExistingAntennaM,
		RangeKm:          rangeKm,
		FrequencyMHz:     p.freqMHz,
		KFactor:          p.kFactor,
	}
	_, fp := known.scan(view, 0, 0)
	key := fmt.Sprintf("%s|%d,%d|%d,%d|%g,%g,%g|%g|%s|%s", plannerRelay,
		plannerMilli(a.Lat), plannerMilli(a.Lon), plannerMilli(b.Lat), plannerMilli(b.Lon),
		heights[0], heights[1], heights[2], rangeKm, fp, known.gen)
	p.serve(w, r, plannerJobSpec{
		endpoint: plannerRelay,
		key:      key,
		view:     view,
		compute: func(ctx context.Context, v terrainView, progress func(float64)) (any, error) {
			nodes, _ := p.snapshotNodes(view, 0, p.now())
			res, err := p.relay(ctx, v, nodes, params, progress)
			if err != nil {
				return nil, err
			}
			res.Source = p.source
			if res.Params == (PlannerRelayParams{}) {
				res.Params = params
			}
			res.Existing = plannerNonNil(res.Existing)
			res.Open = plannerNonNil(res.Open)
			if w := plannerTerrainWarning(v); w != "" {
				res.Warnings = append(res.Warnings, w)
			}
			res.Warnings = plannerNonNil(res.Warnings)
			return res, nil
		},
	})
}

// plannerRelayPad is the corridor margin around a leg of dKm.
func plannerRelayPad(dKm float64) float64 {
	return math.Max(plannerRelayPadFrac*dKm, plannerRelayPadMinKm)
}

// plannerRelayView is the terrain a relay search for a→b reads: the
// endpoints' bbox grown by plannerRelayPad on every side.
func plannerRelayView(a, b geoPoint) geoBBox {
	ends := plannerBBoxAdd(geoBBox{S: a.Lat, W: a.Lon, N: a.Lat, E: a.Lon}, b)
	return plannerExpandBBox(ends, plannerRelayPad(geoDistKm(a, b)))
}

// ─── /api/planner/coverage ──────────────────────────────────────────────────

func (p *plannerService) handleCoverage(w http.ResponseWriter, r *http.Request) {
	q, rej := plannerParseQuery(r.URL.RawQuery, "bbox", "sites", "range", "hr", "he", "baseline", "stale")
	if rej != nil {
		rej.write(w)
		return
	}
	raw, ok := q["bbox"]
	if !ok {
		plannerBadRequest(w, "bbox required")
		return
	}
	grid, msg := plannerParseGridBox(raw, p.cfg.MaxAreaKm)
	if msg != "" {
		plannerBadRequest(w, msg)
		return
	}
	sites := plannerDefaultSites
	if s, ok := q["sites"]; ok {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > plannerMaxSites {
			plannerBadRequest(w, fmt.Sprintf("sites must be an integer 1-%d", plannerMaxSites))
			return
		}
		sites = n
	}
	rangeKm, ok := plannerOptRange(q, p.cfg.DefaultRangeKm, p.cfg.rangesKm())
	if !ok {
		plannerBadRequest(w, plannerRangeMsg(p.cfg.rangesKm()))
		return
	}
	hr, ok1 := plannerOptHeight(q, "hr", p.cfg.NewAntennaM)
	he, ok2 := plannerOptHeight(q, "he", p.cfg.ExistingAntennaM)
	if !ok1 || !ok2 {
		plannerBadRequest(w, "hr and he must be numbers of metres")
		return
	}
	baseline := "predicted"
	if s, ok := q["baseline"]; ok {
		if s != "predicted" && s != "observed" {
			plannerBadRequest(w, "baseline must be predicted or observed")
			return
		}
		baseline = s
	}
	stale := false
	if s, ok := q["stale"]; ok {
		if s != "0" && s != "1" {
			plannerBadRequest(w, "stale must be 0 or 1")
			return
		}
		stale = s == "1"
	}
	area := grid.bbox()
	known := p.knownSet()
	margin := p.cfg.NodeMarginKm
	hasNodes, fp := known.scan(area, plannerAreaNodeKm, margin)
	if !hasNodes {
		plannerBadRequest(w, plannerErrNoNodes)
		return
	}
	view := plannerExpandBBox(area, margin)
	if p.src.TileCount(view) > p.tiles.Coverage {
		plannerBadRequest(w, plannerErrTooManyTiles)
		return
	}
	params := PlannerPlanParams{
		Sites:            sites,
		RangeKm:          rangeKm,
		NewAntennaM:      hr,
		ExistingAntennaM: he,
		Baseline:         baseline,
		IncludeStale:     stale,
		FrequencyMHz:     p.freqMHz,
		KFactor:          p.kFactor,
		MinClearance:     plannerMinClearance,
	}
	key := fmt.Sprintf("%s|%d,%d,%d,%d|%d|%g|%g,%g|%s|%t|%s|%s", plannerCoverage,
		grid.s, grid.w, grid.n, grid.e, sites, rangeKm, hr, he, baseline, stale, fp, known.gen)
	p.serve(w, r, plannerJobSpec{
		endpoint: plannerCoverage,
		key:      key,
		view:     view,
		compute: func(ctx context.Context, v terrainView, progress func(float64)) (any, error) {
			now := p.now()
			nodes, truncated := p.snapshotNodes(area, margin, now)
			pubkeys := make([]string, len(nodes))
			for i := range nodes {
				pubkeys[i] = nodes[i].PubKey
			}
			observed := observedPlannerEdges(nodes, p.data.edges(pubkeys), now, plannerObservedMinScore)
			res, err := p.coverage(ctx, v, planInputs{
				Area: area, Nodes: nodes, Observed: observed, Truncated: truncated, Params: params,
			}, progress)
			if err != nil {
				return nil, err
			}
			if res.Area == (geoBBoxJSON{}) {
				res.Area = geoBBoxJSON{S: area.S, W: area.W, N: area.N, E: area.E}
			}
			if res.Params == (PlannerPlanParams{}) {
				res.Params = params
			}
			res.Source = p.source
			if res.ComputedAt.IsZero() {
				res.ComputedAt = now.UTC()
			}
			res.Truncated = res.Truncated || truncated
			res.Nodes = plannerNonNil(res.Nodes)
			res.Edges = plannerNonNil(res.Edges)
			res.Sites = plannerNonNil(res.Sites)
			if w := plannerTerrainWarning(v); w != "" {
				res.Warnings = append(res.Warnings, w)
			}
			res.Warnings = plannerNonNil(res.Warnings)
			return res, nil
		},
	})
}

// snapshotNodes selects a job's repeaters/rooms from one read of the node
// cache. Only candidates' pubkeys go to LastHeardMap, keeping its store
// read lock short.
func (p *plannerService) snapshotNodes(area geoBBox, marginKm float64, now time.Time) ([]plannerNode, bool) {
	all := p.data.nodes()
	outer := plannerExpandBBox(area, marginKm)
	var pubkeys []string
	for i := range all {
		n := &all[i]
		if (n.Role == "repeater" || n.Role == "room") && n.HasGPS && plannerBBoxContains(outer, n.Lat, n.Lon) {
			pubkeys = append(pubkeys, n.PublicKey)
		}
	}
	return selectPlannerNodes(all, p.data.lastHeard(pubkeys), area, marginKm, p.data.filter(), now)
}

// ─── Parsing ────────────────────────────────────────────────────────────────

// plannerParseQuery applies the checks common to every endpoint: a bounded
// query, only the allowed keys, none repeated.
func plannerParseQuery(raw string, allowed ...string) (map[string]string, *plannerReject) {
	if len(raw) > plannerMaxRawQuery {
		return nil, &plannerReject{code: http.StatusBadRequest, msg: "query too long"}
	}
	vals, err := url.ParseQuery(raw)
	if err != nil {
		return nil, &plannerReject{code: http.StatusBadRequest, msg: "malformed query"}
	}
	out := make(map[string]string, len(vals))
	for k, v := range vals {
		if !slices.Contains(allowed, k) {
			return nil, &plannerReject{code: http.StatusBadRequest, msg: fmt.Sprintf("unknown parameter %q", k)}
		}
		if len(v) != 1 {
			return nil, &plannerReject{code: http.StatusBadRequest, msg: fmt.Sprintf("duplicate parameter %q", k)}
		}
		out[k] = v[0]
	}
	return out, nil
}

// plannerFloat parses a finite float.
func plannerFloat(s string) (float64, bool) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// plannerMilli is v in thousandths of a degree, the planner's point quantum.
func plannerMilli(v float64) int64 { return int64(math.Round(v * plannerCoordScale)) }

// plannerLatLon range-checks and quantises one point.
func plannerLatLon(latS, lonS string) (geoPoint, bool) {
	lat, ok1 := plannerFloat(latS)
	lon, ok2 := plannerFloat(lonS)
	if !ok1 || !ok2 || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return geoPoint{}, false
	}
	return geoPoint{
		Lat: float64(plannerMilli(lat)) / plannerCoordScale,
		Lon: float64(plannerMilli(lon)) / plannerCoordScale,
	}, true
}

// plannerParsePoint parses "lat,lon".
func plannerParsePoint(raw string) (geoPoint, bool) {
	if strings.Count(raw, ",") != 1 {
		return geoPoint{}, false
	}
	la, lo, _ := strings.Cut(raw, ",")
	return plannerLatLon(la, lo)
}

// plannerParsePts parses the flat "lat,lon,lat,lon..." list; the second
// return value is a 400 message, empty when valid. Separators are counted
// before splitting, so an oversized list costs no allocation.
func plannerParsePts(raw string) ([]geoPoint, string) {
	c := strings.Count(raw, ",")
	if c%2 == 0 || c < 3 || c > 2*plannerMaxPoints-1 {
		return nil, fmt.Sprintf("pts must hold 2 to %d lat,lon points", plannerMaxPoints)
	}
	parts := strings.Split(raw, ",")
	pts := make([]geoPoint, len(parts)/2)
	for i := range pts {
		pt, ok := plannerLatLon(parts[2*i], parts[2*i+1])
		if !ok {
			return nil, "pts contains an invalid or out-of-range number"
		}
		pts[i] = pt
	}
	return pts, ""
}

// plannerHeight rounds an antenna height to 1 m and clamps it to 1..100.
func plannerHeight(v float64) float64 {
	v = math.Round(v)
	if v < plannerMinAntennaM {
		return plannerMinAntennaM
	}
	if v > plannerMaxAntennaM {
		return plannerMaxAntennaM
	}
	return v
}

func plannerOptHeight(q map[string]string, key string, def float64) (float64, bool) {
	s, ok := q[key]
	if !ok {
		return plannerHeight(def), true
	}
	v, ok := plannerFloat(s)
	if !ok {
		return 0, false
	}
	return plannerHeight(v), true
}

// plannerOptRange is the "range" parameter, def when absent; it must be
// one of allowed.
func plannerOptRange(q map[string]string, def float64, allowed []float64) (float64, bool) {
	s, ok := q["range"]
	if !ok {
		return def, true
	}
	v, ok := plannerFloat(s)
	if !ok || !slices.Contains(allowed, v) {
		return 0, false
	}
	return v, true
}

func plannerRangeMsg(allowed []float64) string {
	s := make([]string, len(allowed))
	for i, r := range allowed {
		s[i] = strconv.FormatFloat(r, 'f', -1, 64)
	}
	return "range must be one of " + strings.Join(s, ",")
}

// plannerGridBox is a coverage area in 0.05° grid cells.
type plannerGridBox struct{ s, w, n, e int }

func (g plannerGridBox) bbox() geoBBox {
	return geoBBox{
		S: float64(g.s) / plannerGridPerDeg, W: float64(g.w) / plannerGridPerDeg,
		N: float64(g.n) / plannerGridPerDeg, E: float64(g.e) / plannerGridPerDeg,
	}
}

// plannerParseGridBox parses "s,w,n,e" and snaps it outward to the grid;
// neither side of the snapped box may exceed maxAreaKm. The second return
// value is a 400 message, empty when valid.
func plannerParseGridBox(raw string, maxAreaKm float64) (plannerGridBox, string) {
	const bad = "bbox must be s,w,n,e in degrees"
	if strings.Count(raw, ",") != 3 {
		return plannerGridBox{}, bad
	}
	parts := strings.Split(raw, ",")
	var v [4]float64
	for i, s := range parts {
		f, ok := plannerFloat(s)
		if !ok {
			return plannerGridBox{}, bad
		}
		v[i] = f
	}
	s, w, n, e := v[0], v[1], v[2], v[3]
	if s < -90 || n > 90 || w < -180 || e > 180 {
		return plannerGridBox{}, bad
	}
	if !(s < n) || !(w < e) {
		return plannerGridBox{}, "bbox needs south below north and west below east (no antimeridian crossing)"
	}
	g := plannerGridBox{
		s: int(math.Floor(s*plannerGridPerDeg + plannerGridEps)),
		w: int(math.Floor(w*plannerGridPerDeg + plannerGridEps)),
		n: int(math.Ceil(n*plannerGridPerDeg - plannerGridEps)),
		e: int(math.Ceil(e*plannerGridPerDeg - plannerGridEps)),
	}
	if g.n <= g.s {
		g.n = g.s + 1
	}
	if g.e <= g.w {
		g.e = g.w + 1
	}
	bb := g.bbox()
	if bb.S < plannerCoverageMinLat || bb.N > plannerCoverageMaxLat {
		return plannerGridBox{}, "bbox must lie within latitudes -56 to 60 (terrain coverage)"
	}
	hKm := (bb.N - bb.S) * plannerKmPerDeg
	wKm := (bb.E - bb.W) * plannerKmPerDeg * math.Cos((bb.S+bb.N)/2*math.Pi/180)
	if hKm > maxAreaKm || wKm > maxAreaKm {
		return plannerGridBox{}, plannerErrAreaTooLarge
	}
	return g, ""
}

// plannerBBoxAdd grows bb to include pt.
func plannerBBoxAdd(bb geoBBox, pt geoPoint) geoBBox {
	bb.S, bb.N = math.Min(bb.S, pt.Lat), math.Max(bb.N, pt.Lat)
	bb.W, bb.E = math.Min(bb.W, pt.Lon), math.Max(bb.E, pt.Lon)
	return bb
}

// plannerNonNil turns a nil slice into an empty one so it encodes as [].
func plannerNonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// ─── Terrain tile caps ──────────────────────────────────────────────────────

// plannerTileLimits is the most 1° terrain tiles one request's view may span,
// per endpoint. Each is the worst case of the largest request the size
// limits accept anywhere in the SRTM latitude band, so such a request is
// never refused for its tiles. A view over its cap (a profile chain of many
// long legs, a leg beyond the band) is too_many_tiles.
type plannerTileLimits struct{ Profile, Relay, Coverage int }

// max is the most tiles one view may need: what the terrain source accepts
// in one View.
func (l plannerTileLimits) max() int { return max(l.Profile, l.Relay, l.Coverage) }

// plannerTileCaps derives the caps from the size limits.
//
//   - Coverage: the parser checks the snapped box, so its sides are at most
//     maxAreaKm (the width at the box's middle latitude) and its edges within
//     the band; the view adds nodeMarginKm on every side. Every point of the
//     view lies within the band's edge plus the margin (plus one grid cell,
//     to spare), and its extent is at most maxAreaKm + 2·nodeMarginKm.
//   - Relay: A and B are at most maxLegKm apart and the corridor adds
//     plannerRelayPad of that on every side, so its extent is at most
//     maxLegKm + 2·plannerRelayPad(maxLegKm), with A and B in the band.
//   - Profile: one leg's view (its pad is far below the relay corridor's)
//     fits the relay's; longer chains may not.
//
// Profile and relay never go below plannerMinLegTiles.
func plannerTileCaps(maxAreaKm, nodeMarginKm, maxLegKm float64) plannerTileLimits {
	band := math.Max(plannerCoverageMaxLat, -plannerCoverageMinLat)
	cov := plannerWorstCaseTiles(maxAreaKm+2*nodeMarginKm,
		band+nodeMarginKm/plannerKmPerDeg+1.0/plannerGridPerDeg)
	pad := plannerRelayPad(maxLegKm)
	leg := max(plannerWorstCaseTiles(maxLegKm+2*pad, band+pad/plannerKmPerDeg), plannerMinLegTiles)
	return plannerTileLimits{Profile: leg, Relay: leg, Coverage: cov}
}

// plannerWorstCaseTiles is the most 1° SRTM tiles a view can span when it
// extends at most sideKm north-south and east-west and no point of it lies
// beyond maxAbsLat. A span of x degrees touches at most ceil(x)+1 tiles,
// when it starts just short of a degree line; the 1e-9° keeps a span that
// rounding nudged past a whole number from being counted one short.
func plannerWorstCaseTiles(sideKm, maxAbsLat float64) int {
	lat := math.Ceil(sideKm/plannerKmPerDeg+1e-9) + 1
	lon := math.Ceil(plannerLonSpanDeg(sideKm, maxAbsLat)+1e-9) + 1
	return int(math.Min(lat, 180) * math.Min(lon, 360))
}

// plannerLonSpanDeg bounds the degrees of longitude km can span at
// |lat| ≤ maxAbsLat: the longitude between two points on that parallel km
// apart (haversine). It is at least km/(plannerKmPerDeg·cos maxAbsLat), the
// linear conversion plannerExpandBBox and the width check use, and it grows
// at least that fast, so it also covers a leg's endpoints (no further apart
// in longitude than two points on the parallel) plus linear padding.
func plannerLonSpanDeg(km, maxAbsLat float64) float64 {
	c := math.Cos(math.Min(math.Abs(maxAbsLat), 90) * math.Pi / 180)
	q := math.Sin(km/(2*earthRadiusKm)) / c
	if !(q < 1) {
		return 360
	}
	return 2 * math.Asin(q) * 180 / math.Pi
}

// ─── Known positions ────────────────────────────────────────────────────────

// plannerKnown is the visible nodes with GPS, rebuilt at most every
// plannerKnownTTL (and at once when the hiding rules change). The geo checks
// and cache-key fingerprints read it; it is immutable once published.
type plannerKnown struct {
	builtAt time.Time
	gen     string
	byLat   []geoPoint          // every visible node with GPS, any role, by latitude
	infra   []plannerKnownInfra // visible repeaters/rooms, by pubkey then position
}

type plannerKnownInfra struct {
	key        string // lower-case pubkey
	lat, lon   float64
	qlat, qlon int64 // plannerMilli
}

// knownSet returns the current set. A set that is only too old keeps
// serving while one caller rebuilds it; a missing one, or one built under
// other hiding rules, is rebuilt before anyone uses it.
func (p *plannerService) knownSet() *plannerKnown {
	gen := p.data.filterGen()
	now := p.now()
	fresh := func(k *plannerKnown) bool {
		return k != nil && k.gen == gen && now.Sub(k.builtAt) < plannerKnownTTL
	}
	k := p.known.Load()
	if fresh(k) {
		return k
	}
	if k != nil && k.gen == gen {
		if !p.knownMu.TryLock() {
			return k
		}
	} else {
		p.knownMu.Lock()
	}
	defer p.knownMu.Unlock()
	if k := p.known.Load(); fresh(k) {
		return k
	}
	k = buildPlannerKnown(p.data.nodes(), p.data.filter(), gen, now)
	p.known.Store(k)
	return k
}

func buildPlannerKnown(nodes []nodeInfo, f plannerNodeFilter, gen string, now time.Time) *plannerKnown {
	k := &plannerKnown{builtAt: now, gen: gen}
	for i := range nodes {
		n := &nodes[i]
		if !n.HasGPS || !plannerFiniteCoord(n.Lat, n.Lon) || (n.Lat == 0 && n.Lon == 0) {
			continue
		}
		if f.Visible != nil && !f.Visible(n) {
			continue
		}
		k.byLat = append(k.byLat, geoPoint{Lat: n.Lat, Lon: n.Lon})
		if (n.Role == "repeater" || n.Role == "room") && n.PublicKey != "" {
			k.infra = append(k.infra, plannerKnownInfra{
				key: strings.ToLower(n.PublicKey), lat: n.Lat, lon: n.Lon,
				qlat: plannerMilli(n.Lat), qlon: plannerMilli(n.Lon),
			})
		}
	}
	sort.Slice(k.byLat, func(i, j int) bool { return k.byLat[i].Lat < k.byLat[j].Lat })
	slices.SortFunc(k.infra, func(a, b plannerKnownInfra) int {
		if c := strings.Compare(a.key, b.key); c != 0 {
			return c
		}
		if a.qlat != b.qlat {
			return compareInt64(a.qlat, b.qlat)
		}
		return compareInt64(a.qlon, b.qlon)
	})
	return k
}

func compareInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// near reports whether pt is within plannerGeoRadiusKm of a visible node.
// Only the latitude band that can hold such a node is scanned.
func (k *plannerKnown) near(pt geoPoint) bool {
	band := plannerGeoRadiusKm/plannerKmPerDeg + 1e-9
	i := sort.Search(len(k.byLat), func(i int) bool { return k.byLat[i].Lat >= pt.Lat-band })
	for ; i < len(k.byLat) && k.byLat[i].Lat <= pt.Lat+band; i++ {
		if geoDistKm(pt, k.byLat[i]) <= plannerGeoRadiusKm {
			return true
		}
	}
	return false
}

// scan reports whether a visible repeater/room lies within area + nearKm,
// and fingerprints (pubkey + quantised position, in sorted order) those
// within area + fpKm: the nodes a job on area would select.
func (k *plannerKnown) scan(area geoBBox, nearKm, fpKm float64) (bool, string) {
	nb, fb := plannerExpandBBox(area, nearKm), plannerExpandBBox(area, fpKm)
	h := sha256.New()
	var buf []byte
	found := false
	for i := range k.infra {
		n := &k.infra[i]
		if !found && plannerBBoxContains(nb, n.lat, n.lon) {
			found = true
		}
		if plannerBBoxContains(fb, n.lat, n.lon) {
			buf = append(buf[:0], n.key...)
			buf = append(buf, '@')
			buf = strconv.AppendInt(buf, n.qlat, 10)
			buf = append(buf, ',')
			buf = strconv.AppendInt(buf, n.qlon, 10)
			buf = append(buf, ';')
			h.Write(buf)
		}
	}
	return found, hex.EncodeToString(h.Sum(nil)[:8])
}

// ─── Job runner ─────────────────────────────────────────────────────────────

const (
	plannerStateQueued int32 = iota
	plannerStateTerrain
	plannerStateComputing
)

var plannerStateNames = [...]string{"queued", "loading_terrain", "computing"}

// plannerJobSpec is one endpoint's job: its key, the terrain it needs and
// the compute to run over that terrain.
type plannerJobSpec struct {
	endpoint string
	key      string
	view     geoBBox
	compute  func(ctx context.Context, v terrainView, progress func(float64)) (any, error)
}

type plannerJob struct {
	key      string
	endpoint string
	heavy    bool   // coverage: its own slot and queue limit
	client   string // clientLogID, for the log line only; never the IP
	ctx      context.Context
	cancel   context.CancelCauseFunc
	done     chan struct{}

	state    atomic.Int32
	progress atomic.Uint64 // math.Float64bits
	polledAt atomic.Int64  // p.since(now) of the last request for key

	queued bool // waiting for a slot; guarded by p.mu

	// Set before done is closed; read only after.
	body []byte
	err  error
}

// since is now as a duration from p.epoch. With the real clock both carry a
// monotonic reading, so a wall-clock jump cannot abandon every job at once.
func (p *plannerService) since(now time.Time) int64 { return int64(now.Sub(p.epoch)) }

func (j *plannerJob) setProgress(f float64) {
	if math.IsNaN(f) {
		return
	}
	if f < 0 {
		f = 0
	} else if f > 1 {
		f = 1
	}
	j.progress.Store(math.Float64bits(f))
}

func (j *plannerJob) status() PlannerJobStatus {
	st := j.state.Load()
	retry := 2
	if st == plannerStateComputing {
		retry = 1
	}
	return PlannerJobStatus{
		State:       plannerStateNames[st],
		Progress:    math.Round(math.Float64frombits(j.progress.Load())*100) / 100,
		RetryAfterS: retry,
	}
}

// plannerReject is a request answered without a result: 4xx/5xx + message
// and, when set, Retry-After.
type plannerReject struct {
	code       int
	msg        string
	retryAfter int
}

func (rj *plannerReject) write(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	if rj.retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(rj.retryAfter))
	}
	writeError(w, rj.code, rj.msg)
}

func plannerBadRequest(w http.ResponseWriter, msg string) {
	(&plannerReject{code: http.StatusBadRequest, msg: msg}).write(w)
}

func (p *plannerService) cacheFor(heavy bool) *plannerLRU {
	if heavy {
		return p.covCache
	}
	return p.cheapCache
}

// serve answers a validated request: the cached result, or the status of
// the key's job (starting one if needed). It waits at most p.fastWait, and
// only when the terrain is already resident.
func (p *plannerService) serve(w http.ResponseWriter, r *http.Request, spec plannerJobSpec) {
	heavy := spec.endpoint == plannerCoverage
	now := p.now()
	if body, ok := p.cacheFor(heavy).get(spec.key, now); ok {
		writePlannerResult(w, body)
		return
	}
	j, body, rej := p.attach(r, spec, heavy, now)
	switch {
	case rej != nil:
		rej.write(w)
		return
	case body != nil:
		writePlannerResult(w, body)
		return
	}
	if p.fastWait > 0 && p.src.Resident(spec.view) {
		t := time.NewTimer(p.fastWait)
		select {
		case <-j.done:
		case <-t.C:
		case <-r.Context().Done():
		}
		t.Stop()
	}
	select {
	case <-j.done:
		if j.err != nil {
			p.failure(j.err).write(w)
			return
		}
		writePlannerResult(w, j.body)
	default:
		writePlannerStatus(w, j.status())
	}
}

// attach returns the live job for spec.key, or the result that landed since
// the caller's cache miss, or starts a job. Starting one checks capacity
// first (free), then charges the client, then the global budget: a client
// over its own limit never spends everyone else's.
func (p *plannerService) attach(r *http.Request, spec plannerJobSpec, heavy bool, now time.Time) (*plannerJob, []byte, *plannerReject) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if j := p.jobs[spec.key]; j != nil {
		j.polledAt.Store(p.since(now))
		return j, nil, nil
	}
	if body, ok := p.cacheFor(heavy).get(spec.key, now); ok {
		return nil, body, nil
	}
	if rec, ok := p.recent[spec.key]; ok {
		if now.Before(rec.until) {
			return nil, nil, &plannerReject{http.StatusServiceUnavailable, rec.msg, retryAfterSecs(rec.until.Sub(now))}
		}
		delete(p.recent, spec.key)
	}
	if p.closed {
		return nil, nil, &plannerReject{http.StatusServiceUnavailable, "planner unavailable", plannerFullRetryS}
	}
	if len(p.jobs) >= plannerMaxLiveJobs || (heavy && p.covQueued >= plannerMaxQueuedCoverage) {
		return nil, nil, &plannerReject{http.StatusServiceUnavailable, "planner busy", plannerFullRetryS}
	}
	client := p.limit.clientKey(r)
	if ok, wait := p.limit.take(r); !ok {
		return nil, nil, &plannerReject{http.StatusTooManyRequests, "rate limited", retryAfterSecs(wait)}
	}
	if ok, wait := p.global.take("all"); !ok {
		p.refundClient(client)
		return nil, nil, &plannerReject{http.StatusServiceUnavailable, "planner busy", retryAfterSecs(wait)}
	}
	ctx, cancel := context.WithCancelCause(p.ctx)
	j := &plannerJob{
		key: spec.key, endpoint: spec.endpoint, heavy: heavy, client: clientLogID(client),
		ctx: ctx, cancel: cancel, done: make(chan struct{}), queued: true,
	}
	j.polledAt.Store(p.since(now))
	p.jobs[spec.key] = j
	if heavy {
		p.covQueued++
	}
	p.wg.Add(1)
	go p.run(j, spec)
	return j, nil, nil
}

// refundClient gives back the token p.limit.take just charged key, when a
// later check refused the job after all. Every take and refund on
// p.limit.rl happens under p.mu, so no other charge slips in between.
func (p *plannerService) refundClient(key string) {
	if p.limit.ipr == nil || p.limit.rl == nil {
		return // take charged nothing
	}
	p.limit.rl.refund(key)
}

func (p *plannerService) run(j *plannerJob, spec plannerJobSpec) {
	defer p.wg.Done()
	start := time.Now()
	stop := make(chan struct{})
	p.wg.Add(1)
	go p.watch(j, stop)
	body, err := p.execute(j, spec)
	close(stop)
	p.finish(j, body, err)
	log.Printf("[planner] %s client=%s key=%s took=%s outcome=%s", j.endpoint, j.client,
		plannerKeyHash(j.key), time.Since(start).Round(time.Millisecond), plannerOutcome(err))
}

// execute runs one job: load terrain, wait for a slot, compute, encode.
// Terrain comes first, outside the slot, so a slow download only delays its
// own job. A panic anywhere in it fails the job, not the server; the slot is
// released on every path.
func (p *plannerService) execute(j *plannerJob, spec plannerJobSpec) (body []byte, err error) {
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("[planner] %s job %s panicked: %v\n%s", j.endpoint, plannerKeyHash(j.key), rec, debug.Stack())
			body, err = nil, errPlannerPanic
		}
	}()
	if !p.src.Resident(spec.view) {
		j.state.Store(plannerStateTerrain)
	}
	tctx, cancel := context.WithTimeout(j.ctx, p.terrainTimeout)
	v, err := p.src.View(tctx, spec.view)
	cancel()
	if err != nil {
		return nil, j.failure(err)
	}
	j.state.Store(plannerStateQueued)

	slots := p.cheapSlots
	if j.heavy {
		slots = p.covSlots
	}
	select {
	case slots <- struct{}{}:
	case <-j.ctx.Done():
		return nil, context.Cause(j.ctx)
	}
	defer func() { <-slots }()
	p.mu.Lock()
	p.dequeueLocked(j)
	p.mu.Unlock()

	j.state.Store(plannerStateComputing)
	cctx, cancel := context.WithTimeout(j.ctx, p.computeTimeout)
	defer cancel()
	res, err := spec.compute(cctx, v, j.setProgress)
	if err == nil && cctx.Err() != nil {
		// A search that ignored its deadline does not get to finish late.
		err = cctx.Err()
	}
	if err != nil {
		return nil, j.failure(err)
	}
	return plannerEncode(res)
}

// failure reports why the job's own context ended, when it did, rather than
// the bare context.Canceled the compute saw.
func (j *plannerJob) failure(err error) error {
	if j.ctx.Err() != nil {
		return context.Cause(j.ctx)
	}
	return err
}

// watch cancels j once no request has asked for its key for abandonAfter.
func (p *plannerService) watch(j *plannerJob, stop <-chan struct{}) {
	defer p.wg.Done()
	t := time.NewTicker(p.watchEvery)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if time.Duration(p.since(p.now())-j.polledAt.Load()) > p.abandonAfter {
				j.cancel(errPlannerAbandoned)
				return
			}
		}
	}
}

func (p *plannerService) dequeueLocked(j *plannerJob) {
	if !j.queued {
		return
	}
	j.queued = false
	if j.heavy {
		p.covQueued--
	}
}

// finish publishes the outcome: a result goes to the cache, a failure to
// p.recent (terrain busy for busyFor, anything else for failedFor) unless
// nobody wanted the job any more, so the next poll starts afresh. The job
// leaves p.jobs in the same critical section.
func (p *plannerService) finish(j *plannerJob, body []byte, err error) {
	j.body, j.err = body, err
	now := p.now()
	p.mu.Lock()
	switch {
	case err == nil:
		p.cacheFor(j.heavy).put(j.key, body, now)
	case errors.Is(err, errTerrainBusy):
		p.rememberLocked(j.key, now, p.busyFor, plannerMsgTerrainBusy)
	case !errors.Is(err, errPlannerAbandoned) && !errors.Is(err, errPlannerClosed):
		p.rememberLocked(j.key, now, p.failedFor, plannerMsgFailedRecent)
	}
	if p.jobs[j.key] == j {
		delete(p.jobs, j.key)
	}
	p.dequeueLocked(j)
	p.mu.Unlock()
	j.cancel(nil)
	close(j.done)
}

// rememberLocked answers key with msg for d, sweeping lapsed entries first
// (the map holds at most the jobs started in the last failedFor). The caller
// holds p.mu.
func (p *plannerService) rememberLocked(key string, now time.Time, d time.Duration, msg string) {
	for k, rec := range p.recent {
		if !now.Before(rec.until) {
			delete(p.recent, k)
		}
	}
	p.recent[key] = plannerRecent{until: now.Add(d), msg: msg}
}

// failure maps a failed job to the answer its waiting request gets; its
// Retry-After matches how long finish remembers the failure.
func (p *plannerService) failure(err error) *plannerReject {
	unavailable := http.StatusServiceUnavailable
	busyS, failedS := retryAfterSecs(p.busyFor), retryAfterSecs(p.failedFor)
	switch {
	case errors.Is(err, errTerrainBusy):
		return &plannerReject{unavailable, plannerMsgTerrainBusy, busyS}
	case errors.Is(err, errPlannerAbandoned), errors.Is(err, errPlannerClosed):
		return &plannerReject{unavailable, "planner job cancelled", 1}
	case errors.Is(err, errTerrainUnavailable):
		return &plannerReject{unavailable, "terrain unavailable", failedS}
	case errors.Is(err, errTerrainTooManyTiles):
		return &plannerReject{code: http.StatusBadRequest, msg: plannerErrTooManyTiles}
	case errors.Is(err, context.DeadlineExceeded):
		return &plannerReject{unavailable, "planner timed out", failedS}
	}
	return &plannerReject{http.StatusInternalServerError, "planner failed", failedS}
}

// plannerOutcome is the log line's outcome.
func plannerOutcome(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, errTerrainBusy):
		return "terrain_busy"
	case errors.Is(err, errTerrainUnavailable):
		return "terrain_unavailable"
	case errors.Is(err, errTerrainTooManyTiles):
		return "too_many_tiles"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, errPlannerAbandoned):
		return "abandoned"
	case errors.Is(err, errPlannerClosed):
		return "closed"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, errPlannerPanic):
		return "panic"
	}
	return "error: " + err.Error()
}

// plannerKeyHash names a key in logs without revealing the coordinates in it.
func plannerKeyHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:6])
}

func plannerEncode(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil // byte-identical to json.Encoder output
}

func writePlannerResult(w http.ResponseWriter, body []byte) {
	w.Header().Set("Cache-Control", plannerCacheControl)
	writeJSONBytes(w, body)
}

func writePlannerStatus(w http.ResponseWriter, st PlannerJobStatus) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Retry-After", strconv.Itoa(st.RetryAfterS))
	writeJSONStatus(w, http.StatusAccepted, st)
}

// ─── Result cache ───────────────────────────────────────────────────────────

// plannerLRU holds encoded results, bounded by entry count and total bytes;
// entries expire ttl after they were stored. Bodies are never modified after
// put, so get hands out the stored slice.
type plannerLRU struct {
	mu       sync.Mutex
	ttl      time.Duration
	maxEntry int // bytes; put skips a bigger entry
	cache    *lru[string, plannerLRUEntry]
}

type plannerLRUEntry struct {
	body []byte
	exp  time.Time
}

func newPlannerLRU(maxItems, maxBytes int, ttl time.Duration) *plannerLRU {
	return &plannerLRU{ttl: ttl, maxEntry: maxBytes / 4,
		cache: newLRU(maxItems, maxBytes, plannerEntrySize)}
}

func plannerEntrySize(key string, e plannerLRUEntry) int { return len(key) + len(e.body) }

func (c *plannerLRU) get(key string, now time.Time) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache.Get(key)
	if !ok {
		return nil, false
	}
	if !now.Before(e.exp) {
		c.cache.Remove(key)
		return nil, false
	}
	return e.body, true
}

// put stores body; one entry over a quarter of the byte budget is not
// cached, so it cannot flush everything else.
func (c *plannerLRU) put(key string, body []byte, now time.Time) {
	e := plannerLRUEntry{body: body, exp: now.Add(c.ttl)}
	if plannerEntrySize(key, e) > c.maxEntry {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache.Put(key, e)
}

func (c *plannerLRU) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cache.Len()
}
