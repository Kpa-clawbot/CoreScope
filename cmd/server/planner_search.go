package main

// Planner search: the repeater backbone plan (runCoveragePlan) and the
// single-relay search (runRelaySearch).
//
// Both are deterministic: single-threaded, no map iteration, and every sort
// is a total order (ties fall to an index). Every terrain link goes through
// planLinkEval, which samples the points sampleTerrainProfile samples and
// applies analyzeLOS's formulas in the same operand order, so a link the plan
// calls solid reads the same on /api/planner/profile. (Equal up to rounding:
// the compiler may fuse a multiply-add differently in the two copies, moving
// a sample by an ulp, ~1 nm.)

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"
)

// planInputs is everything a coverage plan needs, snapshotted by the API
// layer outside any store lock.
type planInputs struct {
	Area      geoBBox // snapped
	Nodes     []plannerNode
	Observed  []plannerEdge
	Truncated bool
	Params    PlannerPlanParams
}

const (
	planGridKm        = 1.0  // coverage candidate grid spacing
	planPeakRadius    = 2    // candidate = local maximum over a 5×5 window
	planSiteSpacingKm = 2.0  // candidates keep this far from active nodes and chosen sites
	planEdgesEnd      = 0.4  // progress when the baseline edges are done
	planCandsEnd      = 0.75 // progress when the candidates are done
	planSiteLinksOut  = 12   // PlannerSite.Links cap

	relayGridKm      = 0.5
	relayPeakRadius  = 1   // 3×3 window
	relayMaxOpen     = 400 // open cells evaluated, highest first
	relayMaxOptions  = 5   // per list
	relayDetour      = 1.3 // d(A,R)+d(R,B) ≤ 1.3·max(D, relayMinBaseKm)
	relayMinBaseKm   = 1.0
	relayExpand      = 0.3 // open grid: bbox(A,B) grown by 0.3·D ...
	relayMinExpandKm = 2.0 // ... but at least 2 km
	relaySpacingKm   = 1.0 // open options keep this far from a better one
	relayMinLegKm    = 0.1 // a relay this close to A or B is the endpoint itself
	relayGridEnd     = 0.1 // progress when the open grid is sampled

	// relayWarnDirectNoTerrain: a sample of the direct path has no terrain,
	// so Direct is not measured (Error no_terrain).
	relayWarnDirectNoTerrain = "Direct link crosses missing terrain: not measured"

	// planGridMaxSide caps grid rows/cols. A 250 km coverage area (the
	// largest maxAreaKm) at 1 km fits, and so does a 200 km relay leg's
	// corridor (the largest maxLegKm: 320 km at 500 m is 641 cells, a little
	// more east-west at high latitude).
	planGridMaxSide = 720
	planCtxEvery    = 16 // profiles between ctx checks (≤ ~1 ms of work)
	planStride0     = 16 // first stride of the coarse-to-fine profile walk
)

// ─── Backbone plan ─────────────────────────────────────────────────────────

// runCoveragePlan computes the backbone plan. progress(frac) is called
// with 0..1 as stages complete. Must check ctx in its hot loops.
//
// Stages: baseline edges (observed, plus predicted pairs within range when
// Baseline is "predicted") → baseline graph and stats → candidate sites
// (1 km grid local maxima, plateaus thinned to one per 3 km, with ≥ 1
// solid link) → greedy picks → response.
// Terrain without data (ElevationAt ok=false) is skipped, never read as 0 m:
// no candidate stands on it and no predicted edge or candidate link crosses
// it. An observed edge is radio evidence and stays, its clearance unknown (0).
// A cancelled ctx returns ctx.Err() within one ctx-check interval; invalid
// inputs return an error before any work.
func runCoveragePlan(ctx context.Context, v terrainView, in planInputs, progress func(frac float64)) (PlannerPlanResponse, error) {
	return planCoverage(ctx, v, in, progress, nil)
}

// planStageStats is what planCoverage spent per stage (benchmarks).
type planStageStats struct {
	EdgesTime, CandsTime, GreedyTime, OutputTime time.Duration
	Profiles, Peaks, Candidates, Sites           int
}

func planCoverage(ctx context.Context, v terrainView, in planInputs, progress func(float64), stats *planStageStats) (PlannerPlanResponse, error) {
	if err := checkPlanInputs(v, in); err != nil {
		return PlannerPlanResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return PlannerPlanResponse{}, err
	}
	if progress == nil {
		progress = func(float64) {}
	}
	s := newCoverageSearch(ctx, v, in, progress)
	var st planStageStats
	t := time.Now()
	if err := s.baselineEdges(); err != nil {
		return PlannerPlanResponse{}, err
	}
	s.baselineGraph()
	st.EdgesTime = time.Since(t)
	t = time.Now()
	peaks, err := s.findCandidates()
	if err != nil {
		return PlannerPlanResponse{}, err
	}
	st.CandsTime = time.Since(t)
	t = time.Now()
	if err := s.greedy(); err != nil {
		return PlannerPlanResponse{}, err
	}
	st.GreedyTime = time.Since(t)
	t = time.Now()
	resp, err := s.response()
	if err != nil {
		return PlannerPlanResponse{}, err
	}
	st.OutputTime = time.Since(t)
	st.Profiles, st.Peaks, st.Candidates, st.Sites = s.ev.profiles, peaks, len(s.cands), len(s.picks)
	if stats != nil {
		*stats = st
	}
	progress(1)
	return resp, nil
}

// checkPlanInputs rejects what the API layer should never pass; the error
// names the field, never a value. Ranges are checked against the largest
// planner.maxLegKm an operator can set: the API layer enforces the one set.
func checkPlanInputs(v terrainView, in planInputs) error {
	if v == nil {
		return errors.New("planner: no terrain view")
	}
	a := in.Area
	if !planCoordOK(a.S, a.W) || !planCoordOK(a.N, a.E) || a.S > a.N || a.W > a.E {
		return errors.New("planner: invalid area")
	}
	if len(in.Nodes) > plannerMaxNodes {
		return errors.New("planner: too many nodes")
	}
	for i := range in.Nodes {
		if !planCoordOK(in.Nodes[i].Lat, in.Nodes[i].Lon) {
			return errors.New("planner: invalid node position")
		}
	}
	p := in.Params
	switch {
	case p.Sites < 0 || p.Sites > plannerMaxSites:
		return errors.New("planner: invalid sites")
	case !(p.RangeKm > 0 && p.RangeKm <= plannerLegKmMax):
		return errors.New("planner: invalid range")
	case !planHeightOK(p.NewAntennaM) || !planHeightOK(p.ExistingAntennaM):
		return errors.New("planner: invalid antenna height")
	case !planRadioOK(p.FrequencyMHz, p.KFactor):
		return errors.New("planner: invalid radio parameters")
	case !losFinite(p.MinClearance):
		return errors.New("planner: invalid min clearance")
	case p.Baseline != "predicted" && p.Baseline != "observed":
		return errors.New("planner: invalid baseline")
	}
	return nil
}

func planCoordOK(lat, lon float64) bool {
	return plannerFiniteCoord(lat, lon) && lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}

func planHeightOK(h float64) bool { return h >= 0 && !math.IsInf(h, 1) }

// planRadioOK: the inputs analyzeLOS accepts (k = 0 means 4/3).
func planRadioOK(freqMHz, k float64) bool {
	return freqMHz > 0 && losFinite(freqMHz) && k >= 0 && losFinite(k)
}

// coverageSearch is one plan computation. Node vertices are 0..n-1 in the
// backbone graph; the site picked at step s is vertex n+s-1.
type coverageSearch struct {
	ctx      context.Context
	in       planInputs
	p        PlannerPlanParams
	progress func(float64)
	ev       *planLinkEval
	pts      []geoPoint
	active   []bool // !Stale || IncludeStale
	counted  []bool // active && InArea
	countFn  func(int) bool
	inRange  planNear // RangeKm prefilter
	spacing  planNear // planSiteSpacingKm prefilter

	repeaters, staleExcluded int

	observed, solid, marginal []plannerEdge
	g                         *backboneGraph // baseline, then + each pick
	s0, cur                   backboneStats
	// linked: the node has an edge in g (observed, solid or to a site). A
	// counted node without one is what a pick can newly "reach".
	linked []bool
	cands  []planCand
	picks  []planPick
}

// planCand is a candidate site. links are its solid links to active nodes
// (peer = node index), siteLinks its solid links to chosen sites
// (peer = step); minClear is the lowest clearance over both.
type planCand struct {
	idx       int // grid index: stable identity and the last tie-break
	pt        geoPoint
	elev      float64
	links     []planCandLink
	siteLinks []planCandLink
	minClear  float64
	gone      bool // picked, or within planSiteSpacingKm of a pick
}

type planCandLink struct {
	peer        int
	dist, clear float64
}

type planPick struct {
	cand   int // index into cands
	gain   planGain
	reason string
	before backboneStats
	after  backboneStats
}

// planGain is a pick's lexicographic objective: components joined, then
// largest-component growth, then fragility removed, then repeaters reached.
type planGain struct{ comps, largest, frag, reach int }

func (a planGain) compare(b planGain) int {
	if c := cmp.Compare(a.comps, b.comps); c != 0 {
		return c
	}
	if c := cmp.Compare(a.largest, b.largest); c != 0 {
		return c
	}
	if c := cmp.Compare(a.frag, b.frag); c != 0 {
		return c
	}
	return cmp.Compare(a.reach, b.reach)
}

func newCoverageSearch(ctx context.Context, v terrainView, in planInputs, progress func(float64)) *coverageSearch {
	n := len(in.Nodes)
	p := in.Params
	s := &coverageSearch{
		ctx: ctx, in: in, p: p, progress: progress,
		ev:      newPlanLinkEval(ctx, v, p.FrequencyMHz, p.KFactor),
		pts:     make([]geoPoint, n),
		active:  make([]bool, n),
		counted: make([]bool, n),
		linked:  make([]bool, n),
	}
	maxAbsLat := math.Max(math.Abs(in.Area.S), math.Abs(in.Area.N))
	for i, nd := range in.Nodes {
		s.pts[i] = geoPoint{nd.Lat, nd.Lon}
		s.active[i] = !nd.Stale || p.IncludeStale
		s.counted[i] = s.active[i] && nd.InArea
		if s.counted[i] {
			s.repeaters++
		}
		if nd.InArea && !s.active[i] {
			s.staleExcluded++
		}
		maxAbsLat = math.Max(maxAbsLat, math.Abs(nd.Lat))
	}
	s.countFn = func(i int) bool { return i < n && s.counted[i] }
	s.inRange = newPlanNear(p.RangeKm, maxAbsLat)
	s.spacing = newPlanNear(planSiteSpacingKm, maxAbsLat)
	return s
}

// baselineEdges: observed edges between active nodes (with their terrain
// clearance), then, for Baseline "predicted", every other active pair
// within range as solid or marginal. Pairs are canonical i<j, profiled i→j.
func (s *coverageSearch) baselineEdges() error {
	n := len(s.pts)
	he := s.p.ExistingAntennaM
	predicted := s.p.Baseline == "predicted"
	total := len(s.in.Observed)
	if predicted {
		total += n * (n - 1) / 2
	}
	done := 0
	tick := func() error {
		if done++; done%256 != 0 {
			return nil
		}
		s.progress(planEdgesEnd * float64(done) / float64(total))
		return s.ctx.Err()
	}
	isObserved := make([]bool, n*n) // n ≤ plannerMaxNodes
	for _, e := range s.in.Observed {
		if err := tick(); err != nil {
			return err
		}
		a, b := e.A, e.B
		if a > b {
			a, b = b, a
		}
		if a < 0 || b >= n || a == b || !s.active[a] || !s.active[b] || isObserved[a*n+b] {
			continue
		}
		isObserved[a*n+b] = true
		d := geoDistKm(s.pts[a], s.pts[b])
		pct := 0.0 // terrain unknown: a sample has no terrain
		if c, ok := s.ev.clearance(s.pts[a], s.pts[b], d, he, he, math.Inf(-1)); ok {
			pct = planPct(c)
		}
		if s.ev.err != nil {
			return s.ev.err
		}
		s.observed = append(s.observed, plannerEdge{A: a, B: b, Class: "observed", DistanceKm: d, ClearancePct: pct})
	}
	if !predicted {
		return nil
	}
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if err := tick(); err != nil {
				return err
			}
			if !s.active[i] || !s.active[j] || isObserved[i*n+j] || !s.inRange.maybe(s.pts[i], s.pts[j]) {
				continue
			}
			d := geoDistKm(s.pts[i], s.pts[j])
			if d > s.p.RangeKm {
				continue
			}
			c, ok := s.ev.clearance(s.pts[i], s.pts[j], d, he, he, 0)
			if s.ev.err != nil {
				return s.ev.err
			}
			switch {
			case !ok:
			case c >= s.p.MinClearance:
				s.solid = append(s.solid, plannerEdge{A: i, B: j, Class: "solid", DistanceKm: d, ClearancePct: planPct(c)})
			default:
				s.marginal = append(s.marginal, plannerEdge{A: i, B: j, Class: "marginal", DistanceKm: d, ClearancePct: planPct(c)})
			}
		}
	}
	return nil
}

// baselineGraph: only observed and solid edges connect the backbone.
func (s *coverageSearch) baselineGraph() {
	edges := make([]plannerEdge, 0, len(s.observed)+len(s.solid))
	edges = append(append(edges, s.observed...), s.solid...)
	for _, e := range edges {
		s.linked[e.A], s.linked[e.B] = true, true
	}
	s.g = newBackboneGraph(len(s.pts), s.active, edges)
	s.s0 = s.g.Stats(s.countFn)
	s.cur = s.s0
}

// findCandidates fills s.cands (grid order) and returns the number of grid
// cells profiled: local maxima more than planSiteSpacingKm from every active
// node, plateaus thinned.
func (s *coverageSearch) findCandidates() (int, error) {
	grid, err := newPlanGrid(s.ctx, s.ev.v, s.in.Area, planGridKm)
	if err != nil {
		return 0, err
	}
	peaks := grid.peaks(planPeakRadius)
	eligible := peaks[:0]
	for k, gi := range peaks {
		if k%256 == 0 {
			if err := s.ctx.Err(); err != nil {
				return 0, err
			}
		}
		if !s.nearActiveNode(grid.point(gi)) {
			eligible = append(eligible, gi)
		}
	}
	peaks = grid.thin(eligible, planPeakRadius)
	hr, he := s.p.NewAntennaM, s.p.ExistingAntennaM
	for k, gi := range peaks {
		if err := s.ctx.Err(); err != nil {
			return 0, err
		}
		s.progress(planEdgesEnd + (planCandsEnd-planEdgesEnd)*float64(k)/float64(len(peaks)))
		pt := grid.point(gi)
		var links []planCandLink
		for j, q := range s.pts {
			if !s.active[j] || !s.inRange.maybe(pt, q) {
				continue
			}
			d := geoDistKm(pt, q)
			if d > s.p.RangeKm {
				continue
			}
			c, ok := s.ev.clearance(pt, q, d, hr, he, s.p.MinClearance)
			if s.ev.err != nil {
				return 0, s.ev.err
			}
			if ok {
				links = append(links, planCandLink{peer: j, dist: d, clear: c})
			}
		}
		if len(links) == 0 {
			continue
		}
		minC := math.Inf(1)
		for _, l := range links {
			minC = math.Min(minC, l.clear)
		}
		s.cands = append(s.cands, planCand{idx: gi, pt: pt, elev: grid.elev[gi], links: links, minClear: minC})
	}
	s.capCandidates()
	return len(peaks), nil
}

func (s *coverageSearch) nearActiveNode(pt geoPoint) bool {
	for j, q := range s.pts {
		if s.active[j] && s.spacing.maybe(pt, q) && geoDistKm(pt, q) <= planSiteSpacingKm {
			return true
		}
	}
	return false
}

// capCandidates keeps the plannerMaxCands best by (distinct baseline
// components linked, solid links, elevation), then restores grid order.
func (s *coverageSearch) capCandidates() {
	if len(s.cands) <= plannerMaxCands {
		return
	}
	comps := make([]int, len(s.cands))
	seen := make([]int, len(s.pts)) // ComponentOf ids are < n; stamp = k+1
	for k := range s.cands {
		for _, l := range s.cands[k].links {
			if c := s.s0.ComponentOf[l.peer]; c >= 0 && seen[c] != k+1 {
				seen[c] = k + 1
				comps[k]++
			}
		}
	}
	order := make([]int, len(s.cands))
	for k := range order {
		order[k] = k
	}
	slices.SortFunc(order, func(x, y int) int {
		a, b := &s.cands[x], &s.cands[y]
		if c := cmp.Compare(comps[y], comps[x]); c != 0 {
			return c
		}
		if c := cmp.Compare(len(b.links), len(a.links)); c != 0 {
			return c
		}
		if c := cmp.Compare(b.elev, a.elev); c != 0 {
			return c
		}
		return cmp.Compare(a.idx, b.idx)
	})
	order = order[:plannerMaxCands]
	slices.Sort(order) // grid order again
	kept := make([]planCand, len(order))
	for k, o := range order {
		kept[k] = s.cands[o]
	}
	s.cands = kept
}

// greedy picks up to Params.Sites sites, each the candidate with the best
// planGain against the current graph (ties: higher min clearance, higher
// elevation, lower grid index), stopping when the best gain is not positive.
func (s *coverageSearch) greedy() error {
	var peers []int
	for step := 1; step <= s.p.Sites; step++ {
		best := -1
		var bestGain planGain
		var bestStats backboneStats
		for k := range s.cands {
			if k%32 == 0 {
				if err := s.ctx.Err(); err != nil {
					return err
				}
				frac := (float64(step-1) + float64(k)/float64(len(s.cands))) / float64(s.p.Sites)
				s.progress(planCandsEnd + (1-planCandsEnd)*frac)
			}
			c := &s.cands[k]
			if c.gone {
				continue
			}
			peers = s.candPeers(c, peers[:0])
			gc := s.g.Clone()
			gc.AddVertex(peers)
			st := gc.Stats(s.countFn)
			gain := planGain{
				comps:   s.cur.Components - st.Components,
				largest: st.Largest - s.cur.Largest,
				frag:    s.cur.Fragility - st.Fragility,
				reach:   s.reach(c),
			}
			if best < 0 || planCandBetter(gain, c, bestGain, &s.cands[best]) {
				best, bestGain, bestStats = k, gain, st
			}
		}
		if best < 0 || bestGain.compare(planGain{}) <= 0 {
			break
		}
		if err := s.pick(best, step, bestGain, bestStats); err != nil {
			return err
		}
	}
	return nil
}

func (s *coverageSearch) candPeers(c *planCand, peers []int) []int {
	for _, l := range c.links {
		peers = append(peers, l.peer)
	}
	for _, l := range c.siteLinks {
		peers = append(peers, len(s.pts)+l.peer-1)
	}
	return peers
}

// reach: counted nodes c links solidly that have no edge in the current
// graph. Nodes already on the backbone are not "reached" again, so a
// connected, redundant backbone yields no reach-only picks.
func (s *coverageSearch) reach(c *planCand) int {
	r := 0
	for _, l := range c.links {
		if s.counted[l.peer] && !s.linked[l.peer] {
			r++
		}
	}
	return r
}

func planCandBetter(g planGain, c *planCand, bg planGain, b *planCand) bool {
	if x := g.compare(bg); x != 0 {
		return x > 0
	}
	if c.minClear != b.minClear {
		return c.minClear > b.minClear
	}
	if c.elev != b.elev {
		return c.elev > b.elev
	}
	return c.idx < b.idx
}

// pick commits candidate k as site step: adds it to g, removes it and every
// candidate within planSiteSpacingKm, and links it to the rest in range.
func (s *coverageSearch) pick(k, step int, gain planGain, after backboneStats) error {
	c := &s.cands[k]
	s.g.AddVertex(s.candPeers(c, nil))
	reason := "reach"
	switch {
	case gain.comps > 0:
		reason = "joins"
	case gain.frag > 0 || after.CutVertices < s.cur.CutVertices:
		reason = "redundancy"
	}
	s.picks = append(s.picks, planPick{cand: k, gain: gain, reason: reason, before: s.cur, after: after})
	s.cur = after
	for _, l := range c.links {
		s.linked[l.peer] = true
	}
	hr := s.p.NewAntennaM
	for k2 := range s.cands {
		o := &s.cands[k2]
		if o.gone || k2 == k {
			continue
		}
		if s.spacing.maybe(c.pt, o.pt) && geoDistKm(c.pt, o.pt) <= planSiteSpacingKm {
			o.gone = true
			continue
		}
		if !s.inRange.maybe(c.pt, o.pt) {
			continue
		}
		d := geoDistKm(c.pt, o.pt)
		if d > s.p.RangeKm {
			continue
		}
		cl, ok := s.ev.clearance(c.pt, o.pt, d, hr, hr, s.p.MinClearance)
		if s.ev.err != nil {
			return s.ev.err
		}
		if ok {
			o.siteLinks = append(o.siteLinks, planCandLink{peer: step, dist: d, clear: cl})
			o.minClear = math.Min(o.minClear, cl)
		}
	}
	c.gone = true
	return nil
}

func (s *coverageSearch) summary(st backboneStats) PlannerSummary {
	return PlannerSummary{
		Repeaters:        s.repeaters,
		Components:       st.Components,
		LargestComponent: st.Largest,
		Isolated:         st.Isolated,
		CutVertices:      st.CutVertices,
		StaleExcluded:    s.staleExcluded,
	}
}

func (s *coverageSearch) response() (PlannerPlanResponse, error) {
	in := s.in
	resp := PlannerPlanResponse{
		Area:       geoBBoxJSON{S: in.Area.S, W: in.Area.W, N: in.Area.N, E: in.Area.E},
		Params:     s.p,
		Baseline:   s.summary(s.s0),
		Final:      s.summary(s.cur),
		Nodes:      make([]PlannerNodeOut, len(in.Nodes)),
		Sites:      make([]PlannerSite, 0, len(s.picks)),
		Warnings:   []string{},
		Truncated:  in.Truncated,
		ComputedAt: time.Now().UTC(),
	}
	switch {
	case s.s0.Components <= 1:
		resp.Status = "connected"
	case len(s.picks) > 0:
		resp.Status = "improved"
	default:
		resp.Status = "no_gain"
	}
	for i, nd := range in.Nodes {
		resp.Nodes[i] = PlannerNodeOut{
			PubKey: nd.PubKey, Name: nd.Name, Role: nd.Role, Lat: nd.Lat, Lon: nd.Lon,
			Stale: nd.Stale, InArea: nd.InArea, Component: s.s0.ComponentOf[i],
		}
	}
	var cut bool
	resp.Edges, cut = planEdgesOut(s.observed, s.solid, s.marginal)
	resp.Truncated = resp.Truncated || cut
	for step, pk := range s.picks {
		site, err := s.siteOut(step+1, pk)
		if err != nil {
			return PlannerPlanResponse{}, err
		}
		resp.Sites = append(resp.Sites, site)
	}
	if s.staleExcluded > 0 {
		resp.Warnings = append(resp.Warnings, plannerCount(s.staleExcluded, "stale repeater", "stale repeaters")+" excluded")
	}
	if len(s.cands) == 0 {
		resp.Warnings = append(resp.Warnings, "Area has no candidate high ground with a usable link")
	}
	return resp, nil
}

// planEdgesOut: observed, then solid, then marginal, each by distance
// (ties by A, B), capped at plannerMaxEdgesOut.
func planEdgesOut(classes ...[]plannerEdge) ([]PlannerEdgeOut, bool) {
	total := 0
	for _, es := range classes {
		total += len(es)
	}
	out := make([]PlannerEdgeOut, 0, int(math.Min(float64(total), plannerMaxEdgesOut)))
	for _, es := range classes {
		es = slices.Clone(es)
		slices.SortFunc(es, func(x, y plannerEdge) int {
			if c := cmp.Compare(x.DistanceKm, y.DistanceKm); c != 0 {
				return c
			}
			if c := cmp.Compare(x.A, y.A); c != 0 {
				return c
			}
			return cmp.Compare(x.B, y.B)
		})
		for _, e := range es {
			if len(out) == plannerMaxEdgesOut {
				return out, true
			}
			out = append(out, PlannerEdgeOut{
				A: e.A, B: e.B, Class: e.Class,
				DistanceKm: losRound(e.DistanceKm, 1000), ClearancePct: e.ClearancePct,
			})
		}
	}
	return out, false
}

// siteOut renders a pick. Links: its solid node and earlier-site links plus
// marginal node links (profiled again here, ≤ n per site), best first.
func (s *coverageSearch) siteOut(step int, pk planPick) (PlannerSite, error) {
	c := &s.cands[pk.cand]
	type ranked struct {
		link  PlannerSiteLink
		clear float64
	}
	var links []ranked
	isSolid := make([]bool, len(s.pts))
	var comps []int
	for _, l := range c.links {
		isSolid[l.peer] = true
		comps = append(comps, s.s0.ComponentOf[l.peer])
		links = append(links, ranked{PlannerSiteLink{Node: l.peer, DistanceKm: losRound(l.dist, 1000),
			ClearancePct: planPct(l.clear), Class: "solid"}, l.clear})
	}
	for _, l := range c.siteLinks {
		links = append(links, ranked{PlannerSiteLink{Node: -1, PeerStep: l.peer, DistanceKm: losRound(l.dist, 1000),
			ClearancePct: planPct(l.clear), Class: "solid"}, l.clear})
	}
	hr, he := s.p.NewAntennaM, s.p.ExistingAntennaM
	for j, q := range s.pts {
		if !s.active[j] || isSolid[j] || !s.inRange.maybe(c.pt, q) {
			continue
		}
		d := geoDistKm(c.pt, q)
		if d > s.p.RangeKm {
			continue
		}
		cl, ok := s.ev.clearance(c.pt, q, d, hr, he, 0)
		if s.ev.err != nil {
			return PlannerSite{}, s.ev.err
		}
		if ok && cl < s.p.MinClearance {
			links = append(links, ranked{PlannerSiteLink{Node: j, DistanceKm: losRound(d, 1000),
				ClearancePct: planPct(cl), Class: "marginal"}, cl})
		}
	}
	slices.SortFunc(links, func(x, y ranked) int {
		if c := cmp.Compare(y.clear, x.clear); c != 0 {
			return c
		}
		if c := cmp.Compare(x.link.DistanceKm, y.link.DistanceKm); c != 0 {
			return c
		}
		if c := cmp.Compare(x.link.Node, y.link.Node); c != 0 {
			return c
		}
		return cmp.Compare(x.link.PeerStep, y.link.PeerStep)
	})
	if len(links) > planSiteLinksOut {
		links = links[:planSiteLinksOut]
	}
	out := make([]PlannerSiteLink, len(links))
	for i, l := range links {
		out[i] = l.link
	}
	slices.Sort(comps)
	comps = slices.Compact(comps)
	if comps == nil {
		comps = []int{}
	}
	return PlannerSite{
		Step: step, Lat: c.pt.Lat, Lon: c.pt.Lon, ElevM: losRound(c.elev, 10), AntennaM: hr,
		Reason: pk.reason,
		Gain: PlannerGain{
			ComponentsJoined:   pk.before.Components - pk.after.Components,
			CutVerticesRemoved: pk.before.CutVertices - pk.after.CutVertices,
			RepeatersReached:   pk.gain.reach,
		},
		After:           s.summary(pk.after),
		Links:           out,
		JoinsComponents: comps,
	}, nil
}

// ─── Relay search ──────────────────────────────────────────────────────────

// runRelaySearch finds the best single relay for A→B among known nodes and
// open terrain in the corridor.
//
// A relay R qualifies when both legs are within range, the detour
// d(A,R)+d(R,B) is at most 1.3·max(D, 1 km), and R is not within 100 m of
// A or B. Options rank by bottleneck clearance (the worse leg, as reported),
// then total distance, then node/grid index; up to 5 per list. Open cells
// are 500 m grid local maxima (3×3), the 400 highest evaluated, and none
// within 1 km of a better one. Options with a negative bottleneck are listed
// only when no option in either list reaches 0.
//
// Terrain without data is skipped, never read as 0 m: no relay stands on it
// and no option has a leg across it. A direct path across it is not
// measured (Error no_terrain, no verdict or profile) and gets
// relayWarnDirectNoTerrain.
func runRelaySearch(ctx context.Context, v terrainView, nodes []plannerNode, p PlannerRelayParams, progress func(frac float64)) (PlannerRelayResponse, error) {
	if err := checkRelayParams(v, p); err != nil {
		return PlannerRelayResponse{}, err
	}
	if err := ctx.Err(); err != nil {
		return PlannerRelayResponse{}, err
	}
	if progress == nil {
		progress = func(float64) {}
	}
	a, b := geoPoint{p.A.Lat, p.A.Lon}, geoPoint{p.B.Lat, p.B.Lon}
	D := geoDistKm(a, b)
	corridor := relayDetour * math.Max(D, relayMinBaseKm)
	box := plannerExpandBBox(geoBBox{
		S: math.Min(a.Lat, b.Lat), W: math.Min(a.Lon, b.Lon),
		N: math.Max(a.Lat, b.Lat), E: math.Max(a.Lon, b.Lon),
	}, math.Max(relayExpand*D, relayMinExpandKm))
	grid, err := newPlanGrid(ctx, v, box, relayGridKm)
	if err != nil {
		return PlannerRelayResponse{}, err
	}
	progress(relayGridEnd)

	maxAbsLat := math.Max(math.Abs(box.S), math.Abs(box.N))
	for i := range nodes {
		maxAbsLat = math.Max(maxAbsLat, math.Abs(nodes[i].Lat))
	}
	near := newPlanNear(p.RangeKm, maxAbsLat)
	fits := func(pt geoPoint) (dA, dB float64, ok bool) {
		if !near.maybe(a, pt) || !near.maybe(pt, b) {
			return 0, 0, false
		}
		dA, dB = geoDistKm(a, pt), geoDistKm(pt, b)
		ok = dA <= p.RangeKm && dB <= p.RangeKm && dA+dB <= corridor && dA >= relayMinLegKm && dB >= relayMinLegKm
		return dA, dB, ok
	}

	var existing, open []relayCand
	for i := range nodes {
		nd := &nodes[i]
		if nd.Stale || !planCoordOK(nd.Lat, nd.Lon) {
			continue
		}
		pt := geoPoint{nd.Lat, nd.Lon}
		dA, dB, ok := fits(pt)
		if !ok {
			continue
		}
		if e, okE := v.ElevationAt(pt.Lat, pt.Lon); okE && losFinite(e) {
			existing = append(existing, relayCand{order: i, node: i, pt: pt, elev: e, dA: dA, dB: dB})
		}
	}
	for _, gi := range grid.peaks(relayPeakRadius) {
		pt := grid.point(gi)
		if dA, dB, ok := fits(pt); ok {
			open = append(open, relayCand{order: gi, node: -1, pt: pt, elev: grid.elev[gi], dA: dA, dB: dB})
		}
	}
	// Highest first; on a plateau, the shortest detour.
	slices.SortFunc(open, func(x, y relayCand) int {
		if c := cmp.Compare(y.elev, x.elev); c != 0 {
			return c
		}
		if c := cmp.Compare(x.dA+x.dB, y.dA+y.dB); c != 0 {
			return c
		}
		return cmp.Compare(x.order, y.order)
	})
	if len(open) > relayMaxOpen {
		open = open[:relayMaxOpen]
	}

	ev := newPlanLinkEval(ctx, v, p.FrequencyMHz, p.KFactor)
	total, done := 2*(len(existing)+len(open)), 0
	rank := func(list []relayCand, hR float64) ([]relayCand, error) {
		kept := list[:0]
		for _, c := range list {
			ca, okA := ev.clearance(a, c.pt, c.dA, p.AntennaAM, hR, math.Inf(-1))
			cb, okB := ev.clearance(c.pt, b, c.dB, hR, p.AntennaBM, math.Inf(-1))
			if ev.err != nil {
				return nil, ev.err
			}
			if done += 2; done%64 == 0 {
				progress(relayGridEnd + (1-relayGridEnd)*float64(done)/float64(total))
			}
			if okA && okB { // a leg over missing terrain says nothing
				c.pct = math.Min(planPct(ca), planPct(cb))
				kept = append(kept, c)
			}
		}
		slices.SortFunc(kept, relayCandCompare)
		return kept, nil
	}
	if existing, err = rank(existing, p.ExistingAntennaM); err != nil {
		return PlannerRelayResponse{}, err
	}
	if open, err = rank(open, p.RelayAntennaM); err != nil {
		return PlannerRelayResponse{}, err
	}
	if len(existing) > relayMaxOptions {
		existing = existing[:relayMaxOptions]
	}
	open = relaySpread(open)
	// Lists are sorted, so a list has a usable option iff its head does.
	if (len(existing) > 0 && existing[0].pct >= 0) || (len(open) > 0 && open[0].pct >= 0) {
		existing, open = relayUsable(existing), relayUsable(open)
	}

	lp := losParams{FreqMHz: p.FrequencyMHz, KFactor: p.KFactor}
	resp := PlannerRelayResponse{
		Direct:   relayLink(v, a, b, D, lp, p.AntennaAM, p.AntennaBM),
		Existing: relayOptions(v, nodes, existing, a, b, lp, p, "existing", p.ExistingAntennaM),
		Open:     relayOptions(v, nodes, open, a, b, lp, p, "open", p.RelayAntennaM),
		Params:   p,
		Warnings: []string{},
	}
	if resp.Direct.Error != "" {
		resp.Warnings = append(resp.Warnings, relayWarnDirectNoTerrain)
	}
	best := math.Inf(-1)
	for _, list := range [][]PlannerRelayOption{resp.Existing, resp.Open} {
		if len(list) > 0 {
			best = math.Max(best, list[0].BottleneckPct)
		}
	}
	if best < plannerMinClearance*100 {
		resp.Warnings = append(resp.Warnings, "No relay gives a clear first Fresnel zone")
	}
	progress(1)
	return resp, nil
}

// checkRelayParams rejects what the API layer should never pass. The leg and
// range are checked against the largest planner.maxLegKm an operator can
// set (planGridMaxSide fits its corridor): the API layer enforces the one
// set.
func checkRelayParams(v terrainView, p PlannerRelayParams) error {
	if v == nil {
		return errors.New("planner: no terrain view")
	}
	switch {
	case !planCoordOK(p.A.Lat, p.A.Lon) || !planCoordOK(p.B.Lat, p.B.Lon):
		return errors.New("planner: invalid endpoint")
	case !(haversineKm(p.A.Lat, p.A.Lon, p.B.Lat, p.B.Lon) <= plannerLegKmMax):
		return errors.New("planner: leg too long")
	case !(p.RangeKm > 0 && p.RangeKm <= plannerLegKmMax):
		return errors.New("planner: invalid range")
	case !planHeightOK(p.AntennaAM) || !planHeightOK(p.AntennaBM) ||
		!planHeightOK(p.RelayAntennaM) || !planHeightOK(p.ExistingAntennaM):
		return errors.New("planner: invalid antenna height")
	case !planRadioOK(p.FrequencyMHz, p.KFactor):
		return errors.New("planner: invalid radio parameters")
	}
	return nil
}

// relayCand: order is the node index (existing) or grid index (open);
// pct is the bottleneck as PlannerRelayOption.BottleneckPct reports it.
type relayCand struct {
	order, node int // node −1 for open terrain
	pt          geoPoint
	elev        float64
	dA, dB      float64
	pct         float64
}

func relayCandCompare(x, y relayCand) int {
	if c := cmp.Compare(y.pct, x.pct); c != 0 {
		return c
	}
	if c := cmp.Compare(x.dA+x.dB, y.dA+y.dB); c != 0 {
		return c
	}
	return cmp.Compare(x.order, y.order)
}

// relaySpread walks ranked open cells and keeps those more than
// relaySpacingKm from every option already kept, up to relayMaxOptions.
func relaySpread(ranked []relayCand) []relayCand {
	var out []relayCand
	for _, c := range ranked {
		clash := false
		for _, k := range out {
			if geoDistKm(c.pt, k.pt) <= relaySpacingKm {
				clash = true
				break
			}
		}
		if !clash {
			if out = append(out, c); len(out) == relayMaxOptions {
				break
			}
		}
	}
	return out
}

func relayUsable(list []relayCand) []relayCand {
	return slices.DeleteFunc(list, func(c relayCand) bool { return c.pct < 0 })
}

// relayLink is a reported leg: the shared LOS summary with its profile. A
// leg with a sample that has no terrain is not measured: Error no_terrain,
// no verdict and no profile, never a verdict read from 0 m.
func relayLink(v terrainView, a, b geoPoint, d float64, lp losParams, ha, hb float64) PlannerLink {
	lp.AntennaAM, lp.AntennaBM = ha, hb
	elev, ok := sampleTerrainProfile(v, a, b, planProfileSamples(d))
	if !ok {
		return PlannerLink{DistanceKm: losRound(math.Max(d, 0), 1000), Error: plannerErrNoTerrain}
	}
	return linkFromProfile(elev, d, lp, true)
}

func relayOptions(v terrainView, nodes []plannerNode, list []relayCand, a, b geoPoint, lp losParams,
	p PlannerRelayParams, kind string, hR float64) []PlannerRelayOption {
	out := make([]PlannerRelayOption, 0, len(list))
	for _, c := range list {
		legA := relayLink(v, a, c.pt, c.dA, lp, p.AntennaAM, hR)
		legB := relayLink(v, c.pt, b, c.dB, lp, hR, p.AntennaBM)
		if legA.Error != "" || legB.Error != "" {
			// The search measured both legs, so only an interior sample an
			// ulp across the edge of a tile without data gets here (planArc
			// vs greatCircleSamples). An unmeasured leg is never offered.
			continue
		}
		o := PlannerRelayOption{
			Kind: kind, Lat: c.pt.Lat, Lon: c.pt.Lon, ElevM: losRound(c.elev, 10), AntennaM: hR,
			LegA: legA, LegB: legB,
		}
		o.BottleneckPct = math.Min(o.LegA.ClearancePct, o.LegB.ClearancePct)
		if c.node >= 0 {
			o.PubKey, o.Name = nodes[c.node].PubKey, nodes[c.node].Name
		}
		out = append(out, o)
	}
	// The legs are recomputed from the same samples, so this is a no-op
	// unless rounding disagrees; it keeps the list ordered as reported.
	slices.SortStableFunc(out, func(x, y PlannerRelayOption) int { return cmp.Compare(y.BottleneckPct, x.BottleneckPct) })
	return out
}

// ─── Shared terrain machinery ──────────────────────────────────────────────

// planPct is PlannerLink.ClearancePct for clearance c (linkFromProfile's
// rounding; +Inf, no interior sample, reads 999).
func planPct(c float64) float64 {
	if math.IsInf(c, 1) {
		return 999
	}
	return losRound(c*100, 10)
}

// planLinkEval profiles links without allocating. Not safe for concurrent
// use; each search owns one.
type planLinkEval struct {
	ctx      context.Context
	v        terrainView
	freq     float64
	bulgeDen float64 // 2·k·R with k defaulted as analyzeLOS does
	profiles int
	err      error // ctx.Err() once seen; every later call fails fast
}

func newPlanLinkEval(ctx context.Context, v terrainView, freqMHz, kFactor float64) *planLinkEval {
	k := kFactor
	if k == 0 {
		k = 4.0 / 3.0
	}
	return &planLinkEval{ctx: ctx, v: v, freq: freqMHz, bulgeDen: 2 * k * earthRadiusKm}
}

// clearance returns the worst first-Fresnel clearance of a→b with antennas
// ha, hb metres above ground: analyzeLOS(sampleTerrainProfile(v, a, b,
// planProfileSamples(distKm)), distKm, …).Clearance, with distKm =
// geoDistKm(a, b). Interior samples are visited coarse-to-fine (stride 16,
// then the midpoints between) so an obstruction is usually met early, and
// the walk stops at the first sample below floor: ok=false. floor = −Inf
// evaluates every sample. ok=false also means a sample has no terrain
// (outside the view, or a tile without data: never read as 0 m) or ctx was
// cancelled (e.err is then set).
func (e *planLinkEval) clearance(a, b geoPoint, distKm, ha, hb, floor float64) (float64, bool) {
	if e.err != nil {
		return 0, false
	}
	if e.profiles%planCtxEvery == 0 {
		if err := e.ctx.Err(); err != nil {
			e.err = err
			return 0, false
		}
	}
	e.profiles++
	eA, okA := e.v.ElevationAt(a.Lat, a.Lon)
	eB, okB := e.v.ElevationAt(b.Lat, b.Lon)
	if !okA || !okB || !losFinite(eA) || !losFinite(eB) {
		return 0, false
	}
	n := planProfileSamples(distKm)
	arc := newPlanArc(a, b, distKm)
	D := distKm
	if !losFinite(D) || D < 0.001 {
		D = 0.001
	}
	// Below, every expression keeps analyzeLOS's operand order; only loop
	// invariants are hoisted.
	last := float64(n - 1)
	fDen := D * e.freq / 1000
	yA, yB := eA+ha, eB+hb
	worst := math.Inf(1)
	for s := planStride0; s >= 1; s /= 2 {
		i, step := s, 2*s // odd multiples of s: the points the coarser passes skipped
		if s == planStride0 {
			step = s
		}
		for ; i <= n-2; i += step {
			t := float64(i) / last
			lat, lon := arc.at(t)
			el, ok := e.v.ElevationAt(lat, lon)
			if !ok || !losFinite(el) {
				return 0, false
			}
			d := t * D
			tm := el + d*(D-d)/e.bulgeDen*1000
			los := yA + (yB-yA)*t
			f1 := 17.32 * math.Sqrt(math.Max(d*(D-d), 1e-9)/fDen)
			if c := (los - tm) / f1; c < worst {
				if worst = c; worst < floor {
					return worst, false
				}
			}
		}
	}
	return worst, true
}

// planArc is greatCircleSamples for one point at a time: the same formula
// and operand order, with the per-leg trigonometry hoisted (equal to an ulp).
type planArc struct {
	a                                      geoPoint
	same                                   bool
	d, sd                                  float64
	cp1, sp1, cl1, sl1, cp2, sp2, cl2, sl2 float64
}

func newPlanArc(a, b geoPoint, distKm float64) planArc {
	p1, l1 := a.Lat*math.Pi/180, a.Lon*math.Pi/180
	p2, l2 := b.Lat*math.Pi/180, b.Lon*math.Pi/180
	d := distKm / earthRadiusKm
	if d < geoSamePointRadians {
		return planArc{a: a, same: true}
	}
	return planArc{
		a: a, d: d, sd: math.Sin(d),
		cp1: math.Cos(p1), sp1: math.Sin(p1), cl1: math.Cos(l1), sl1: math.Sin(l1),
		cp2: math.Cos(p2), sp2: math.Sin(p2), cl2: math.Cos(l2), sl2: math.Sin(l2),
	}
}

// at is the point at fraction t (0 < t < 1; the ends are exact elsewhere).
func (r *planArc) at(t float64) (lat, lon float64) {
	if r.same {
		return r.a.Lat, r.a.Lon
	}
	A := math.Sin((1-t)*r.d) / r.sd
	B := math.Sin(t*r.d) / r.sd
	x := A*r.cp1*r.cl1 + B*r.cp2*r.cl2
	y := A*r.cp1*r.sl1 + B*r.cp2*r.sl2
	z := A*r.sp1 + B*r.sp2
	return math.Atan2(z, math.Hypot(x, y)) * 180 / math.Pi, math.Atan2(y, x) * 180 / math.Pi
}

// planNear is a conservative degree-box prefilter for "within km": it
// never rejects a pair whose haversine distance is ≤ km when neither point
// is further from the equator than maxAbsLat. From the haversine formula,
// hav(d) ≥ hav(Δφ) and hav(d) ≥ cos²(maxAbsLat)·hav(Δλ).
type planNear struct{ latLim, lonLim float64 }

func newPlanNear(km, maxAbsLat float64) planNear {
	const slack = 1 + 1e-9
	r := km / earthRadiusKm // radians; haversineKm uses the same radius
	p := planNear{latLim: r * 180 / math.Pi * slack, lonLim: 360}
	if c := math.Cos(maxAbsLat * math.Pi / 180); c > 0 {
		if q := math.Sin(r/2) / c; q < 1 {
			p.lonLim = 2 * math.Asin(q) * 180 / math.Pi * slack
		}
	}
	return p
}

func (p planNear) maybe(a, b geoPoint) bool {
	if math.Abs(a.Lat-b.Lat) > p.latLim {
		return false
	}
	dl := math.Abs(a.Lon - b.Lon)
	if dl > 180 {
		dl = 360 - dl
	}
	return dl <= p.lonLim
}

// planGrid samples terrain on a regular grid over a bbox: stepKm apart
// north–south and, at the box's middle latitude, east–west; centred in the
// box. Points are rounded to 5 decimals (what the API reports) and sampled
// there, so a reported site reproduces exactly.
type planGrid struct {
	rows, cols int
	lat, lon   []float64 // per row, per column
	elev       []float64 // row-major
	ok         []bool    // the view had terrain there
}

func newPlanGrid(ctx context.Context, v terrainView, b geoBBox, stepKm float64) (*planGrid, error) {
	dLat := stepKm / plannerKmPerDeg
	dLon := dLat / math.Max(math.Cos((b.S+b.N)/2*math.Pi/180), 0.01)
	rows := int(math.Floor((b.N-b.S)/dLat+1e-9)) + 1
	cols := int(math.Floor((b.E-b.W)/dLon+1e-9)) + 1
	if rows < 1 || cols < 1 || rows > planGridMaxSide || cols > planGridMaxSide {
		return nil, errors.New("planner: search area too large")
	}
	g := &planGrid{
		rows: rows, cols: cols,
		lat: make([]float64, rows), lon: make([]float64, cols),
		elev: make([]float64, rows*cols), ok: make([]bool, rows*cols),
	}
	lat0 := b.S + ((b.N-b.S)-float64(rows-1)*dLat)/2
	lon0 := b.W + ((b.E-b.W)-float64(cols-1)*dLon)/2
	for r := range g.lat {
		g.lat[r] = losRound(lat0+float64(r)*dLat, 1e5)
	}
	for c := range g.lon {
		g.lon[c] = losRound(lon0+float64(c)*dLon, 1e5)
	}
	for r := 0; r < rows; r++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for c := 0; c < cols; c++ {
			if e, ok := v.ElevationAt(g.lat[r], g.lon[c]); ok && losFinite(e) {
				g.elev[r*cols+c], g.ok[r*cols+c] = e, true
			}
		}
	}
	return g, nil
}

func (g *planGrid) point(i int) geoPoint { return geoPoint{g.lat[i/g.cols], g.lon[i%g.cols]} }

// peaks returns, in grid order, the valid cells at least as high as every
// valid cell within radius (a (2·radius+1)² window, clipped at the edges).
// Every cell of a plateau qualifies.
func (g *planGrid) peaks(radius int) []int {
	var out []int
	for r := 0; r < g.rows; r++ {
		for c := 0; c < g.cols; c++ {
			i := r*g.cols + c
			if g.ok[i] && !g.window(r, c, radius, func(j int) bool { return g.ok[j] && g.elev[j] > g.elev[i] }) {
				out = append(out, i)
			}
		}
	}
	return out
}

// thin walks peaks (grid order) and drops each one that has an already kept
// peak in its window. Two peaks in each other's window are necessarily equal,
// so this thins plateaus only, to one cell per (radius+1)² (every 3 km for
// the plan), and never drops an isolated peak. It bounds the plan on flat
// ground or sea, where every 1 km cell would otherwise be profiled to every
// node in range (14.9 s → 1.9 s with 100 m masts, BenchmarkCoveragePlanMax).
func (g *planGrid) thin(peaks []int, radius int) []int {
	kept := make([]bool, len(g.elev))
	out := peaks[:0:0]
	for _, i := range peaks {
		if !g.window(i/g.cols, i%g.cols, radius, func(j int) bool { return kept[j] }) {
			kept[i] = true
			out = append(out, i)
		}
	}
	return out
}

// window reports whether hit holds for any cell within radius of (r, c).
func (g *planGrid) window(r, c, radius int, hit func(j int) bool) bool {
	for rr := r - radius; rr <= r+radius; rr++ {
		if rr < 0 || rr >= g.rows {
			continue
		}
		for cc := c - radius; cc <= c+radius; cc++ {
			if cc >= 0 && cc < g.cols && hit(rr*g.cols+cc) {
				return true
			}
		}
	}
	return false
}

// plannerCount renders "1 stale repeater" / "3 stale repeaters".
func plannerCount(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
