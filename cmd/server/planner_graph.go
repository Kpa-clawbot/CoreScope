package main

// Backbone graph for the planner: which repeaters count, which observed RF
// links are trustworthy, and the connectivity objective the greedy site
// search optimises (components, articulation points, fragility).

import (
	"cmp"
	"math"
	"slices"
	"strings"
	"time"
)

// plannerNode is a repeater/room with GPS considered by a plan.
type plannerNode struct {
	PubKey, Name, Role string
	Lat, Lon           float64
	Stale              bool // no certain activity within the role's silent window
	InArea             bool // inside the (snapped) bbox, not just the margin
}

// plannerEdge: A/B index the plannerNode slice. Class: observed | solid | marginal.
type plannerEdge struct {
	A, B         int
	Class        string
	DistanceKm   float64
	ClearancePct float64
}

// plannerNodeFilter carries the same filters /api/nodes applies.
type plannerNodeFilter struct {
	// Visible is /api/nodes' hiding rule for one node (node_visibility.go);
	// nil hides nothing.
	Visible     func(n *nodeInfo) bool
	SilentAfter func(role string) time.Duration
}

// plannerKmPerDeg is one degree of latitude on haversineKm's sphere.
const plannerKmPerDeg = 6371.0 * math.Pi / 180

// selectPlannerNodes picks repeaters/rooms with GPS (not 0,0) that f.Visible
// keeps, inside area expanded by marginKm, capped at
// plannerMaxNodes (in-area first, then nearest the centre). Stale is set from
// max(LastSeen, lastHeard[pk]) vs SilentAfter(role). A nil Visible hides
// nothing; a nil SilentAfter marks nothing stale. A pubkey listed twice
// (case-insensitively) is kept once, so pubkeys identify nodes downstream.
// Output: in-area first, then by pubkey.
func selectPlannerNodes(all []nodeInfo, lastHeard map[string]time.Time, area geoBBox, marginKm float64,
	f plannerNodeFilter, now time.Time) (nodes []plannerNode, truncated bool) {
	outer := plannerExpandBBox(area, marginKm)
	type candidate struct {
		node   plannerNode
		distKm float64 // to the area centre; filled only when capping
	}
	var cands []candidate
	seen := make(map[string]struct{})
	for i := range all {
		ni := &all[i]
		if ni.Role != "repeater" && ni.Role != "room" {
			continue
		}
		if !ni.HasGPS || !plannerFiniteCoord(ni.Lat, ni.Lon) || (ni.Lat == 0 && ni.Lon == 0) {
			continue
		}
		if f.Visible != nil && !f.Visible(ni) {
			continue
		}
		if !plannerBBoxContains(outer, ni.Lat, ni.Lon) {
			continue
		}
		key := strings.ToLower(ni.PublicKey)
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		cands = append(cands, candidate{node: plannerNode{
			PubKey: ni.PublicKey, Name: ni.Name, Role: ni.Role,
			Lat: ni.Lat, Lon: ni.Lon,
			Stale:  plannerNodeStale(ni, lastHeard[ni.PublicKey], f.SilentAfter, now),
			InArea: plannerBBoxContains(area, ni.Lat, ni.Lon),
		}})
	}
	if len(cands) > plannerMaxNodes {
		cLat, cLon := (area.S+area.N)/2, (area.W+area.E)/2
		for i := range cands {
			cands[i].distKm = haversineKm(cLat, cLon, cands[i].node.Lat, cands[i].node.Lon)
		}
		slices.SortFunc(cands, func(a, b candidate) int {
			if a.node.InArea != b.node.InArea {
				if a.node.InArea {
					return -1
				}
				return 1
			}
			if c := cmp.Compare(a.distKm, b.distKm); c != 0 {
				return c
			}
			return strings.Compare(a.node.PubKey, b.node.PubKey)
		})
		cands = cands[:plannerMaxNodes]
		truncated = true
	}
	nodes = make([]plannerNode, len(cands))
	for i := range cands {
		nodes[i] = cands[i].node
	}
	slices.SortFunc(nodes, func(a, b plannerNode) int {
		if a.InArea != b.InArea {
			if a.InArea {
				return -1
			}
			return 1
		}
		return strings.Compare(a.PubKey, b.PubKey)
	})
	return nodes, truncated
}

func plannerFiniteCoord(lat, lon float64) bool {
	return !math.IsNaN(lat) && !math.IsInf(lat, 0) && !math.IsNaN(lon) && !math.IsInf(lon, 0)
}

// plannerNodeStale: liveness is the newer of the DB last_seen and the
// certain last heard (AGENTS.md "last_heard vs last_seen"). Never seen at all
// means stale: now.Sub(zero time) saturates at the maximum Duration.
func plannerNodeStale(ni *nodeInfo, heard time.Time, silentAfter func(role string) time.Duration, now time.Time) bool {
	if silentAfter == nil {
		return false
	}
	last := ni.LastSeen
	if heard.After(last) {
		last = heard
	}
	return now.Sub(last) > silentAfter(ni.Role)
}

// plannerExpandBBox grows b by km on every side. Longitude degrees shrink
// with cos(lat); the expanded box's highest |lat| is used so the east/west
// margin is at least km at every latitude in the box (slightly more nearer
// the equator). A box reaching a pole spans every longitude.
func plannerExpandBBox(b geoBBox, km float64) geoBBox {
	if !(km > 0) {
		return b
	}
	dLat := km / plannerKmPerDeg
	s, n := math.Max(b.S-dLat, -90), math.Min(b.N+dLat, 90)
	cosLat := math.Cos(math.Max(math.Abs(s), math.Abs(n)) * math.Pi / 180)
	if cosLat < 1e-9 {
		return geoBBox{S: s, W: -180, N: n, E: 180}
	}
	dLon := km / (plannerKmPerDeg * cosLat)
	if dLon >= 180 {
		return geoBBox{S: s, W: -180, N: n, E: 180}
	}
	return geoBBox{S: s, W: b.W - dLon, N: n, E: b.E + dLon}
}

// plannerBBoxContains is inclusive on every edge. lon is also tried ±360 so
// an expanded box that runs past ±180 still catches nodes on the far side
// of the antimeridian.
func plannerBBoxContains(b geoBBox, lat, lon float64) bool {
	if lat < b.S || lat > b.N {
		return false
	}
	for _, l := range [3]float64{lon, lon + 360, lon - 360} {
		if l >= b.W && l <= b.E {
			return true
		}
	}
	return false
}

// observedPlannerEdges turns observed neighbor edges into Class "observed"
// edges between nodes (rules: non-ambiguous, not 1-byte-only evidence,
// Score*Confidence ≥ minScore). Endpoints match nodes by full pubkey
// (case-insensitive; the neighbor graph lower-cases its keys). (A,B) and
// (B,A) collapse into one edge. Output has A < B, sorted by (A, B).
// DistanceKm is the great-circle distance; ClearancePct is 0 because observed
// edges are not terrain-checked.
func observedPlannerEdges(nodes []plannerNode, edges []NeighborEdge, now time.Time, minScore float64) []plannerEdge {
	index := make(map[string]int, len(nodes))
	for i := range nodes {
		k := strings.ToLower(nodes[i].PubKey)
		if _, dup := index[k]; !dup {
			index[k] = i
		}
	}
	seen := make(map[[2]int]struct{})
	var out []plannerEdge
	for i := range edges {
		e := &edges[i]
		if e.Ambiguous || e.NodeB == "" || !plannerEdgeEvidenceOK(e) {
			continue
		}
		a, okA := index[strings.ToLower(e.NodeA)]
		b, okB := index[strings.ToLower(e.NodeB)]
		if !okA || !okB || a == b {
			continue
		}
		if !(e.Score(now)*e.Confidence() >= minScore) {
			continue
		}
		if a > b {
			a, b = b, a
		}
		if _, dup := seen[[2]int{a, b}]; dup {
			continue
		}
		seen[[2]int{a, b}] = struct{}{}
		out = append(out, plannerEdge{
			A: a, B: b, Class: "observed",
			DistanceKm: haversineKm(nodes[a].Lat, nodes[a].Lon, nodes[b].Lat, nodes[b].Lon),
		})
	}
	slices.SortFunc(out, func(x, y plannerEdge) int {
		if c := cmp.Compare(x.A, y.A); c != 0 {
			return c
		}
		return cmp.Compare(x.B, y.B)
	})
	return out
}

// plannerEdgeEvidenceOK rejects edges whose only per-mode evidence is 1-byte
// hop prefixes, which collide ~8-way across a mesh (CountsByMode doc in
// neighbor_graph.go). 2- and 3-byte sightings are evidence. An edge with no
// per-mode tally or only the legacy bucket 0 (persisted snapshot counts)
// is accepted when it is resolved or was never ambiguous. Legacy counts
// mixed with 1-byte sightings and nothing longer are not "legacy-only", so
// that edge is rejected.
func plannerEdgeEvidenceOK(e *NeighborEdge) bool {
	if e.CountsByMode[2]+e.CountsByMode[3] > 0 {
		return true
	}
	return e.CountsByMode[1] <= 0 && (e.Resolved || !e.Ambiguous)
}

// backboneGraph is an undirected graph over the active plannerNode indices
// plus any extra (site) vertices appended after them. Invariants: len(active)
// == len(adj) == n; adj holds only active vertices, never v itself, and no
// neighbour twice. Stats and Clone only read g, so they may run concurrently;
// AddVertex must not.
type backboneGraph struct {
	n      int
	active []bool
	adj    [][]int
}

// newBackboneGraph: vertices 0..n-1; active[i]=false removes vertex i
// (e.g. stale nodes when stale=0). active == nil means all active; indices
// past len(active) are active too. Edges touching an inactive or
// out-of-range vertex, self-loops and parallel edges are dropped.
func newBackboneGraph(n int, active []bool, edges []plannerEdge) *backboneGraph {
	n = max(n, 0)
	act := make([]bool, n, n+1)
	for i := range act {
		act[i] = i >= len(active) || active[i]
	}
	pairs := make([]uint64, 0, len(edges))
	for _, e := range edges {
		a, b := e.A, e.B
		if a == b || a < 0 || b < 0 || a >= n || b >= n || !act[a] || !act[b] {
			continue
		}
		if a > b {
			a, b = b, a
		}
		pairs = append(pairs, uint64(a)<<32|uint64(b))
	}
	slices.Sort(pairs)
	pairs = slices.Compact(pairs)
	deg := make([]int, n)
	for _, p := range pairs {
		deg[p>>32]++
		deg[uint32(p)]++
	}
	// One backing array; each list is capped at its degree so a later
	// AddVertex append reallocates instead of overwriting the next list.
	flat := make([]int, 2*len(pairs))
	adj := make([][]int, n, n+1)
	off := 0
	for v := range adj {
		adj[v] = flat[off : off : off+deg[v]]
		off += deg[v]
	}
	for _, p := range pairs {
		a, b := int(p>>32), int(uint32(p))
		adj[a] = append(adj[a], b)
		adj[b] = append(adj[b], a)
	}
	return &backboneGraph{n: n, active: act, adj: adj}
}

// AddVertex appends a vertex (a candidate site) linked to peers; returns its index.
// Peers that are inactive, out of range or repeated are ignored.
func (g *backboneGraph) AddVertex(peers []int) int {
	v := g.n
	nb := make([]int, 0, len(peers))
	for _, p := range peers {
		if p >= 0 && p < v && g.active[p] {
			nb = append(nb, p)
		}
	}
	slices.Sort(nb)
	nb = slices.Compact(nb)
	for _, p := range nb {
		g.adj[p] = append(g.adj[p], v)
	}
	g.adj = append(g.adj, nb)
	g.active = append(g.active, true)
	g.n++
	return v
}

// Clone returns an independent copy (greedy evaluates candidates on copies).
func (g *backboneGraph) Clone() *backboneGraph {
	total := 0
	for _, nb := range g.adj {
		total += len(nb)
	}
	flat := make([]int, total)
	adj := make([][]int, len(g.adj), len(g.adj)+1)
	off := 0
	for v, nb := range g.adj {
		copy(flat[off:], nb)
		adj[v] = flat[off : off+len(nb) : off+len(nb)]
		off += len(nb)
	}
	act := make([]bool, len(g.active), len(g.active)+1)
	copy(act, g.active)
	return &backboneGraph{n: g.n, active: act, adj: adj}
}

// backboneStats is the objective state of a graph.
//   - Components: components containing ≥ 1 counted vertex (isolated counted
//     vertices are components); Largest = counted vertices in the largest.
//   - CutVertices: articulation points that are counted vertices.
//   - Fragility: Σ over articulation points v of (|C(v)| − 1 − size of the
//     largest part left when v is removed): nodes cut off from the main part
//     if v fails.
//   - ComponentOf: component id per vertex (−1 inactive); ids ordered by
//     counted size descending, so 0 is the largest.
type backboneStats struct {
	Components, Largest, Isolated, CutVertices, Fragility int
	ComponentOf                                           []int
}

// Stats computes backboneStats; counted(i) says whether vertex i counts
// (in-area nodes; margin nodes carry connectivity but are not counted).
// counted is asked only about active vertices; nil counts every vertex.
//
// Sizes are counted sizes throughout: Fragility sums, over every
// articulation point v (counted or not), counted(C(v)) − counted(v) − the
// largest counted part left when v is removed. Ties in ComponentOf order go
// to the component with the smallest vertex index; components with no
// counted vertex get the ids after Components.
//
// One iterative Tarjan pass, O(V+E), no recursion. The parts of v come from
// the same pass: a DFS child c with low[c] ≥ disc[v] is cut off with its
// subtree; for the root every child is, and a non-root keeps one more part,
// the rest of the component (its ancestors plus the child subtrees with a
// back edge above v).
func (g *backboneGraph) Stats(counted func(i int) bool) backboneStats {
	n := g.n
	st := backboneStats{ComponentOf: make([]int, n)}
	if n == 0 {
		return st
	}
	slab := make([]int32, 11*n)
	disc := slab[0*n : 1*n] // DFS discovery time; 0 = unvisited
	low := slab[1*n : 2*n]
	parent := slab[2*n : 3*n] // −1 for a DFS root
	next := slab[3*n : 4*n]   // next adjacency index to scan
	sub := slab[4*n : 5*n]    // counted vertices in the DFS subtree
	sepN := slab[5*n : 6*n]   // children cut off when v is removed
	sepSum := slab[6*n : 7*n] // their counted subtree sizes, summed
	sepMax := slab[7*n : 8*n] // and the largest
	cnt := slab[8*n : 9*n]    // 1 if counted
	stack := slab[9*n : 10*n]
	compCnt := slab[10*n : 11*n] // counted size per discovery-order component
	comp := st.ComponentOf       // discovery-order id, remapped at the end

	for v := 0; v < n; v++ {
		comp[v] = -1
		if g.active[v] && (counted == nil || counted(v)) {
			cnt[v] = 1
		}
	}

	var clock int32
	nComp := 0
	for r := 0; r < n; r++ {
		if !g.active[r] || disc[r] != 0 {
			continue
		}
		id := nComp
		nComp++
		clock++
		disc[r], low[r], parent[r], sub[r] = clock, clock, -1, cnt[r]
		comp[r] = id
		stack[0] = int32(r)
		sp := 1
		for sp > 0 {
			v := stack[sp-1]
			if nb := g.adj[v]; int(next[v]) < len(nb) {
				w := nb[next[v]]
				next[v]++
				if disc[w] == 0 {
					clock++
					disc[w], low[w], parent[w], sub[w] = clock, clock, v, cnt[w]
					comp[w] = id
					stack[sp] = int32(w)
					sp++
				} else if int32(w) != parent[v] && disc[w] < low[v] {
					// The usual tree-edge skip. The articulation test below
					// (low ≥ disc) holds either way; only bridges need it.
					low[v] = disc[w]
				}
				continue
			}
			sp--
			if p := parent[v]; p >= 0 {
				// Not the min builtin: routes_test.go declares a package
				// min(int, int) that shadows it in test builds.
				if low[v] < low[p] {
					low[p] = low[v]
				}
				sub[p] += sub[v]
				if low[v] >= disc[p] {
					sepN[p]++
					sepSum[p] += sub[v]
					sepMax[p] = max(sepMax[p], sub[v])
				}
			}
		}
		compCnt[id] = sub[r]
	}

	for v := 0; v < n; v++ {
		if comp[v] < 0 {
			continue
		}
		if cnt[v] == 1 && len(g.adj[v]) == 0 {
			st.Isolated++
		}
		isRoot := parent[v] < 0
		if sepN[v] < 1 || (isRoot && sepN[v] < 2) {
			continue // not an articulation point
		}
		if cnt[v] == 1 {
			st.CutVertices++
		}
		total := compCnt[comp[v]]
		rest := total - cnt[v] - sepSum[v] // 0 for a root
		st.Fragility += int(total - cnt[v] - max(sepMax[v], rest))
	}

	// Rank components by counted size, descending, with a stable counting
	// sort (discovery order = smallest vertex index first). The DFS arrays
	// are dead now, so the sort reuses the slab: maxC+1 ≤ n+1 ≤ 2n.
	var maxC int32
	for id := 0; id < nComp; id++ {
		if compCnt[id] > 0 {
			st.Components++
		}
		maxC = max(maxC, compCnt[id])
	}
	st.Largest = int(maxC)
	firstRank := slab[0 : maxC+1] // per counted size
	clear(firstRank)
	for id := 0; id < nComp; id++ {
		firstRank[compCnt[id]]++
	}
	var acc int32
	for s := maxC; s >= 0; s-- {
		c := firstRank[s]
		firstRank[s] = acc
		acc += c
	}
	rank := slab[2*n : 2*n+nComp]
	for id := 0; id < nComp; id++ {
		rank[id] = firstRank[compCnt[id]]
		firstRank[compCnt[id]]++
	}
	for v := range comp {
		if comp[v] >= 0 {
			comp[v] = int(rank[comp[v]])
		}
	}
	return st
}
