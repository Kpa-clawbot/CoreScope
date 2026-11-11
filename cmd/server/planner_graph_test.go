package main

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"
)

// ─── backboneGraph.Stats: hand-computed cases ──────────────────────────────

func gEdges(pairs ...[2]int) []plannerEdge {
	out := make([]plannerEdge, len(pairs))
	for i, p := range pairs {
		out[i] = plannerEdge{A: p[0], B: p[1], Class: "solid"}
	}
	return out
}

func countedSet(idx ...int) func(int) bool {
	set := map[int]bool{}
	for _, i := range idx {
		set[i] = true
	}
	return func(i int) bool { return set[i] }
}

func assertStats(t *testing.T, got, want backboneStats) {
	t.Helper()
	if got.Components != want.Components || got.Largest != want.Largest || got.Isolated != want.Isolated ||
		got.CutVertices != want.CutVertices || got.Fragility != want.Fragility ||
		!slices.Equal(got.ComponentOf, want.ComponentOf) {
		t.Fatalf("stats mismatch\n got  %+v\n want %+v", got, want)
	}
}

func TestBackboneStatsTable(t *testing.T) {
	cases := []struct {
		name    string
		n       int
		active  []bool
		edges   []plannerEdge
		counted func(int) bool // nil = all
		want    backboneStats
	}{
		{
			// a-b-c, b is a cut vertex: removing it leaves {a},{c}: 3-1-1 = 1.
			name: "path a-b-c", n: 3, edges: gEdges([2]int{0, 1}, [2]int{1, 2}),
			want: backboneStats{Components: 1, Largest: 3, CutVertices: 1, Fragility: 1, ComponentOf: []int{0, 0, 0}},
		},
		{
			// Same path with b at index 0, so the cut vertex is the DFS root.
			name: "path with cut vertex as DFS root", n: 3, edges: gEdges([2]int{0, 1}, [2]int{0, 2}),
			want: backboneStats{Components: 1, Largest: 3, CutVertices: 1, Fragility: 1, ComponentOf: []int{0, 0, 0}},
		},
		{
			// Star, 4 leaves: centre fragility = 5-1-1 = 3 = leaves-1.
			name: "star centre root", n: 5,
			edges: gEdges([2]int{0, 1}, [2]int{0, 2}, [2]int{0, 3}, [2]int{0, 4}),
			want:  backboneStats{Components: 1, Largest: 5, CutVertices: 1, Fragility: 3, ComponentOf: []int{0, 0, 0, 0, 0}},
		},
		{
			// Star with the centre at the last index (non-root in the DFS).
			name: "star centre non-root", n: 6,
			edges: gEdges([2]int{5, 0}, [2]int{5, 1}, [2]int{5, 2}, [2]int{5, 3}, [2]int{5, 4}),
			want:  backboneStats{Components: 1, Largest: 6, CutVertices: 1, Fragility: 4, ComponentOf: []int{0, 0, 0, 0, 0, 0}},
		},
		{
			name: "cycle", n: 4, edges: gEdges([2]int{0, 1}, [2]int{1, 2}, [2]int{2, 3}, [2]int{3, 0}),
			want: backboneStats{Components: 1, Largest: 4, ComponentOf: []int{0, 0, 0, 0}},
		},
		{
			// Triangles {0,1,2} and {4,5,6} joined through bridge vertex 3.
			// v=2: parts {0,1}=2, {3,4,5,6}=4 → 7-1-4 = 2
			// v=3: parts {0,1,2}=3, {4,5,6}=3 → 7-1-3 = 3
			// v=4: parts {5,6}=2, {0,1,2,3}=4 → 7-1-4 = 2
			name: "two triangles joined by a bridge vertex", n: 7,
			edges: gEdges([2]int{0, 1}, [2]int{1, 2}, [2]int{0, 2}, [2]int{2, 3}, [2]int{3, 4},
				[2]int{4, 5}, [2]int{5, 6}, [2]int{4, 6}),
			want: backboneStats{Components: 1, Largest: 7, CutVertices: 3, Fragility: 7, ComponentOf: []int{0, 0, 0, 0, 0, 0, 0}},
		},
		{
			// Bow-tie: triangles share vertex 2. parts {0,1},{3,4} → 5-1-2 = 2.
			name: "bow-tie sharing one vertex", n: 5,
			edges: gEdges([2]int{0, 1}, [2]int{1, 2}, [2]int{0, 2}, [2]int{2, 3}, [2]int{3, 4}, [2]int{2, 4}),
			want:  backboneStats{Components: 1, Largest: 5, CutVertices: 1, Fragility: 2, ComponentOf: []int{0, 0, 0, 0, 0}},
		},
		{
			// 0 and 1 isolated, pair 2-3: the pair is the largest (id 0), the
			// isolated vertices follow in index order.
			name: "disconnected pair plus isolated vertices", n: 4, edges: gEdges([2]int{2, 3}),
			want: backboneStats{Components: 3, Largest: 2, Isolated: 2, ComponentOf: []int{1, 2, 0, 0}},
		},
		{
			// Equal counted sizes: the component with the smaller vertex wins.
			name: "tie broken by smallest vertex index", n: 4, edges: gEdges([2]int{1, 3}, [2]int{0, 2}),
			want: backboneStats{Components: 2, Largest: 2, ComponentOf: []int{0, 1, 0, 1}},
		},
		{
			// Counted {0,1,2}; margin vertices 3 and 5 uncounted; 4 is an
			// uncounted isolated vertex. Graph 0-3-1-2-5.
			// v=3 (uncounted AP): parts {0}=1, {1,2,5}=2 → 3-0-2 = 1
			// v=1: parts {0,3}=1, {2,5}=1 → 3-1-1 = 1
			// v=2: parts {0,3,1}=2, {5}=0 → 3-1-2 = 0
			// CutVertices counts 1 and 2 only; {4} is not a component.
			name: "counted/uncounted mix", n: 6,
			edges:   gEdges([2]int{0, 3}, [2]int{3, 1}, [2]int{1, 2}, [2]int{2, 5}),
			counted: countedSet(0, 1, 2),
			want:    backboneStats{Components: 1, Largest: 3, CutVertices: 2, Fragility: 2, ComponentOf: []int{0, 0, 0, 0, 1, 0}},
		},
		{
			// Uncounted-only component bigger than the counted one still
			// ranks after it.
			name: "uncounted component ranks last", n: 5,
			edges:   gEdges([2]int{0, 1}, [2]int{1, 2}, [2]int{3, 4}),
			counted: countedSet(3),
			want:    backboneStats{Components: 1, Largest: 1, CutVertices: 0, Fragility: 0, ComponentOf: []int{1, 1, 1, 0, 0}},
		},
		{
			// Path 0-1-2 with 1 inactive: its edges vanish, 0 and 2 isolated.
			name: "inactive vertex", n: 3, active: []bool{true, false, true},
			edges: gEdges([2]int{0, 1}, [2]int{1, 2}),
			want:  backboneStats{Components: 2, Largest: 1, Isolated: 2, ComponentOf: []int{0, -1, 1}},
		},
		{
			// Parallel edges, reversed duplicates, self-loops and out-of-range
			// endpoints collapse to the single edge 0-1; 0-1 is no cycle.
			name: "parallel edges and junk are deduped", n: 3,
			edges: gEdges([2]int{0, 1}, [2]int{1, 0}, [2]int{0, 1}, [2]int{1, 1}, [2]int{2, 2}, [2]int{0, 7}, [2]int{-1, 2}),
			want:  backboneStats{Components: 2, Largest: 2, Isolated: 1, ComponentOf: []int{0, 0, 1}},
		},
		{
			name: "empty graph", n: 0,
			want: backboneStats{ComponentOf: []int{}},
		},
		{
			name: "nothing counted", n: 3, edges: gEdges([2]int{0, 1}, [2]int{1, 2}),
			counted: func(int) bool { return false },
			want:    backboneStats{ComponentOf: []int{0, 0, 0}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newBackboneGraph(tc.n, tc.active, tc.edges)
			assertStats(t, g.Stats(tc.counted), tc.want)
		})
	}
}

func TestBackboneAddVertexBridgesComponents(t *testing.T) {
	g := newBackboneGraph(5, []bool{true, true, true, true, false}, gEdges([2]int{0, 1}, [2]int{2, 3}))
	counted := func(i int) bool { return i < 4 } // the site itself is not counted
	assertStats(t, g.Stats(counted), backboneStats{Components: 2, Largest: 2, ComponentOf: []int{0, 0, 1, 1, -1}})

	// Duplicates, out-of-range, negative and inactive peers are ignored.
	v := g.AddVertex([]int{1, 2, 1, 99, -1, 4})
	if v != 5 || g.n != 6 || len(g.adj[5]) != 2 || len(g.adj[4]) != 0 {
		t.Fatalf("AddVertex = %d, n=%d, adj[5]=%v adj[4]=%v", v, g.n, g.adj[5], g.adj[4])
	}
	// 0-1-5-2-3, counted {0..3}:
	// v=1: parts {0}=1, {5,2,3}=2 → 4-1-2 = 1
	// v=5: parts {0,1}=2, {2,3}=2 → 4-0-2 = 2 (not counted: no CutVertices)
	// v=2: parts {3}=1, {0,1,5}=2 → 4-1-2 = 1
	assertStats(t, g.Stats(counted), backboneStats{
		Components: 1, Largest: 4, CutVertices: 2, Fragility: 4, ComponentOf: []int{0, 0, 0, 0, -1, 0},
	})

	// A second site linked to 3 and 0 closes the cycle 0-1-5-2-3-6: a
	// redundancy pick leaves no articulation point at all.
	w := g.AddVertex([]int{3, 0})
	if w != 6 {
		t.Fatalf("second AddVertex = %d", w)
	}
	assertStats(t, g.Stats(counted), backboneStats{
		Components: 1, Largest: 4, ComponentOf: []int{0, 0, 0, 0, -1, 0, 0},
	})
}

func TestBackboneAddVertexNoPeers(t *testing.T) {
	g := newBackboneGraph(2, nil, gEdges([2]int{0, 1}))
	g.AddVertex(nil)
	assertStats(t, g.Stats(nil), backboneStats{Components: 2, Largest: 2, Isolated: 1, ComponentOf: []int{0, 0, 1}})
}

func TestBackboneCloneIndependence(t *testing.T) {
	g := newBackboneGraph(4, nil, gEdges([2]int{0, 1}, [2]int{2, 3}))
	before := g.Stats(nil)

	c := g.Clone()
	c.AddVertex([]int{1, 2})
	c.AddVertex([]int{0, 3})
	if g.n != 4 || len(g.adj) != 4 || len(g.active) != 4 {
		t.Fatalf("clone mutation leaked: n=%d len(adj)=%d", g.n, len(g.adj))
	}
	for v, want := range []int{1, 1, 1, 1} {
		if len(g.adj[v]) != want {
			t.Fatalf("original adj[%d]=%v after clone AddVertex", v, g.adj[v])
		}
	}
	assertStats(t, g.Stats(nil), before)
	assertStats(t, c.Stats(nil), backboneStats{Components: 1, Largest: 6, ComponentOf: []int{0, 0, 0, 0, 0, 0}})

	// And the other way: growing the original leaves the clone alone.
	cBefore := c.Stats(nil)
	g.AddVertex([]int{0, 1, 2, 3})
	assertStats(t, c.Stats(nil), cBefore)
	if len(c.adj[0]) != 2 {
		t.Fatalf("clone adj[0]=%v after original AddVertex", c.adj[0])
	}

	// A clone of a clone appends without overwriting its neighbour's list
	// in the shared flat backing array.
	cc := c.Clone()
	cc.AddVertex([]int{0})
	if !slices.Equal(cc.adj[1], c.adj[1]) || len(cc.adj[0]) != len(c.adj[0])+1 {
		t.Fatalf("flat backing overwrite: cc.adj[0]=%v cc.adj[1]=%v c.adj[1]=%v", cc.adj[0], cc.adj[1], c.adj[1])
	}
}

// Parallel edges cannot change vertex connectivity, so Stats alone cannot
// see a missed dedup; check the adjacency invariant directly.
func TestBackboneNewGraphDedupsAdjacency(t *testing.T) {
	g := newBackboneGraph(3, nil, gEdges([2]int{0, 1}, [2]int{1, 0}, [2]int{0, 1}, [2]int{1, 1}, [2]int{1, 2}))
	if !slices.Equal(g.adj[0], []int{1}) || !slices.Equal(g.adj[1], []int{0, 2}) || !slices.Equal(g.adj[2], []int{1}) {
		t.Fatalf("adj = %v", g.adj)
	}
}

func TestBackboneNewGraphKeepsCallerSlices(t *testing.T) {
	active := []bool{true, true}
	g := newBackboneGraph(2, active, gEdges([2]int{0, 1}))
	active[1] = false
	g.AddVertex([]int{1})
	if !g.active[1] || len(active) != 2 {
		t.Fatalf("graph aliases the caller's active slice")
	}
	// A short active slice: indices past it are active.
	h := newBackboneGraph(3, []bool{false}, gEdges([2]int{1, 2}, [2]int{0, 1}))
	assertStats(t, h.Stats(nil), backboneStats{Components: 1, Largest: 2, ComponentOf: []int{-1, 0, 0}})
}

// A long path proves the DFS is iterative and the fragility sum exact:
// internal vertex i leaves parts i and m-i (m = n-1), so it contributes
// min(i, m-i); for m = 2k the total is k².
func TestBackboneStatsDeepPath(t *testing.T) {
	const n = 200001
	edges := make([]plannerEdge, 0, n-1)
	for i := 0; i+1 < n; i++ {
		edges = append(edges, plannerEdge{A: i, B: i + 1})
	}
	st := newBackboneGraph(n, nil, edges).Stats(nil)
	k := (n - 1) / 2
	if st.Components != 1 || st.Largest != n || st.CutVertices != n-2 || st.Fragility != k*k {
		t.Fatalf("deep path: %+v (want cut %d frag %d)", backboneStats{
			Components: st.Components, Largest: st.Largest, CutVertices: st.CutVertices, Fragility: st.Fragility,
		}, n-2, k*k)
	}
}

// ─── backboneGraph.Stats: property test against brute force ────────────────

// bruteStats recomputes backboneStats from the raw edge list (not g.adj):
// components by BFS, and for each vertex, the parts left after deleting it.
func bruteStats(n int, active []bool, edges [][2]int, counted func(int) bool) backboneStats {
	isAct := func(i int) bool { return active == nil || i >= len(active) || active[i] }
	adj := make([]map[int]bool, n)
	for i := range adj {
		adj[i] = map[int]bool{}
	}
	for _, e := range edges {
		a, b := e[0], e[1]
		if a == b || a < 0 || b < 0 || a >= n || b >= n || !isAct(a) || !isAct(b) {
			continue
		}
		adj[a][b], adj[b][a] = true, true
	}
	cnt := func(i int) int {
		if isAct(i) && (counted == nil || counted(i)) {
			return 1
		}
		return 0
	}
	// components ignoring vertex skip (−1 = none); returns per-vertex label
	// and per-label counted size, labels in smallest-vertex order.
	label := func(skip int) ([]int, []int) {
		lab := make([]int, n)
		for i := range lab {
			lab[i] = -1
		}
		var sizes []int
		for r := 0; r < n; r++ {
			if r == skip || !isAct(r) || lab[r] >= 0 {
				continue
			}
			id := len(sizes)
			sizes = append(sizes, 0)
			queue := []int{r}
			lab[r] = id
			for len(queue) > 0 {
				v := queue[0]
				queue = queue[1:]
				sizes[id] += cnt(v)
				for w := range adj[v] {
					if w != skip && lab[w] < 0 {
						lab[w] = id
						queue = append(queue, w)
					}
				}
			}
		}
		return lab, sizes
	}
	lab, sizes := label(-1)
	var st backboneStats
	for _, s := range sizes {
		if s > 0 {
			st.Components++
		}
		st.Largest = max(st.Largest, s)
	}
	order := make([]int, len(sizes))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return sizes[b] - sizes[a] })
	rank := make([]int, len(sizes))
	for r, id := range order {
		rank[id] = r
	}
	st.ComponentOf = make([]int, n)
	for v := 0; v < n; v++ {
		st.ComponentOf[v] = -1
		if lab[v] < 0 {
			continue
		}
		st.ComponentOf[v] = rank[lab[v]]
		if cnt(v) == 1 && len(adj[v]) == 0 {
			st.Isolated++
		}
		// Parts of C(v) after removing v.
		lab2, sizes2 := label(v)
		parts := map[int]int{}
		for u := 0; u < n; u++ {
			if u != v && lab[u] == lab[v] {
				parts[lab2[u]] = sizes2[lab2[u]]
			}
		}
		if len(parts) < 2 {
			continue
		}
		if cnt(v) == 1 {
			st.CutVertices++
		}
		largest := 0
		for _, s := range parts {
			largest = max(largest, s)
		}
		st.Fragility += sizes[lab[v]] - cnt(v) - largest
	}
	return st
}

func TestBackboneStatsMatchesBruteForce(t *testing.T) {
	rng := rand.New(rand.NewPCG(2026, 11))
	for iter := 0; iter < 300; iter++ {
		n := 1 + rng.IntN(14)
		var active []bool
		if rng.IntN(3) > 0 {
			active = make([]bool, n)
			for i := range active {
				active[i] = rng.IntN(6) > 0
			}
		}
		p := 0.05 + 0.4*rng.Float64()
		var edges [][2]int
		for a := 0; a < n; a++ {
			for b := 0; b < n; b++ {
				if a != b && rng.Float64() < p/2 {
					edges = append(edges, [2]int{a, b}) // both directions possible: duplicates
				}
			}
		}
		if rng.IntN(4) == 0 && n > 0 {
			edges = append(edges, [2]int{0, 0}, [2]int{n - 1, n + 3}) // junk
		}
		cMask := make([]bool, n+3)
		for i := range cMask {
			cMask[i] = rng.IntN(4) > 0
		}
		counted := func(i int) bool { return cMask[i] }

		g := newBackboneGraph(n, active, gEdges(edges...))
		// Sometimes grow the graph with sites, mirroring the edges in the
		// brute force's list.
		bn, bActive := n, active
		if rng.IntN(2) == 0 {
			if bActive == nil {
				bActive = make([]bool, n)
				for i := range bActive {
					bActive[i] = true
				}
			} else {
				bActive = slices.Clone(bActive)
			}
			for s := 0; s < 1+rng.IntN(3); s++ {
				var peers []int
				for q := 0; q < rng.IntN(4); q++ {
					peers = append(peers, rng.IntN(bn+1)-1)
				}
				if rng.IntN(2) == 0 {
					g = g.Clone()
				}
				v := g.AddVertex(peers)
				for _, p := range peers {
					if p >= 0 && p < bn && bActive[p] {
						edges = append(edges, [2]int{v, p})
					}
				}
				bActive = append(bActive, true)
				bn++
			}
		}
		got := g.Stats(counted)
		want := bruteStats(bn, bActive, edges, counted)
		if got.Components != want.Components || got.Largest != want.Largest || got.Isolated != want.Isolated ||
			got.CutVertices != want.CutVertices || got.Fragility != want.Fragility ||
			!slices.Equal(got.ComponentOf, want.ComponentOf) {
			t.Fatalf("iter %d n=%d active=%v edges=%v counted=%v\n got  %+v\n want %+v",
				iter, bn, bActive, edges, cMask[:bn], got, want)
		}
	}
}

// BenchmarkBackboneStats: the greedy search calls Stats ~500×10 times per
// plan on graphs of up to ~800 vertices (300 nodes + 500 candidates).
func BenchmarkBackboneStats(b *testing.B) {
	const n, m = 800, 4000
	rng := rand.New(rand.NewPCG(1, 2))
	seen := map[[2]int]bool{}
	var edges []plannerEdge
	for len(edges) < m {
		a, c := rng.IntN(n), rng.IntN(n)
		if a == c || seen[[2]int{min(a, c), max(a, c)}] {
			continue
		}
		seen[[2]int{min(a, c), max(a, c)}] = true
		edges = append(edges, plannerEdge{A: a, B: c})
	}
	// A pendant chain gives the pass real articulation points to score.
	for i := 0; i < 50; i++ {
		edges = append(edges, plannerEdge{A: 700 + i, B: 701 + i})
	}
	g := newBackboneGraph(n, nil, edges)
	counted := func(i int) bool { return i < 300 || i >= 700 }
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_ = g.Stats(counted)
	}
}

// ─── selectPlannerNodes ────────────────────────────────────────────────────

// testArea is ~55 km N-S by ~39 km E-W around lat 45.25.
var testArea = geoBBox{S: 45.0, W: -122.0, N: 45.5, E: -121.5}

// lonOffsetKm returns lon moved km east at lat.
func lonOffsetKm(lat, lon, km float64) float64 {
	return lon + km/(plannerKmPerDeg*math.Cos(lat*math.Pi/180))
}

func latOffsetKm(lat, km float64) float64 { return lat + km/plannerKmPerDeg }

func rptr(pk string, lat, lon float64) nodeInfo {
	return nodeInfo{PublicKey: pk, Name: "node-" + pk, Role: "repeater", Lat: lat, Lon: lon, HasGPS: true}
}

func pubkeys(nodes []plannerNode) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.PubKey
	}
	return out
}

func TestSelectPlannerNodesFilters(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	cLat, cLon := 45.25, -121.75
	all := []nodeInfo{
		rptr("aa01", cLat, cLon),
		{PublicKey: "aa02", Name: "room", Role: "room", Lat: cLat, Lon: cLon, HasGPS: true},
		{PublicKey: "aa03", Name: "comp", Role: "companion", Lat: cLat, Lon: cLon, HasGPS: true},
		{PublicKey: "aa04", Name: "sens", Role: "sensor", Lat: cLat, Lon: cLon, HasGPS: true},
		{PublicKey: "aa05", Name: "room-server", Role: "room_server", Lat: cLat, Lon: cLon, HasGPS: true},
		{PublicKey: "aa06", Name: "nogps", Role: "repeater", Lat: cLat, Lon: cLon, HasGPS: false},
		rptr("aa07", 0, 0),
		rptr("aa08", math.NaN(), cLon),
		rptr("aa09", cLat, math.Inf(1)),
		rptr("aa10", cLat, cLon), // blacklisted
		{PublicKey: "aa11", Name: "Hidden-x", Role: "repeater", Lat: cLat, Lon: cLon, HasGPS: true},
		rptr("AA01", cLat, cLon), // same pubkey as aa01, different case
		rptr("", cLat, cLon),
	}
	f := plannerNodeFilter{
		IsBlacklisted: func(pk string) bool { return pk == "aa10" },
		IsNameHidden:  func(name string) bool { return strings.HasPrefix(name, "Hidden") },
	}
	got, trunc := selectPlannerNodes(all, nil, testArea, 30, f, now)
	if trunc {
		t.Fatal("unexpected truncation")
	}
	if want := []string{"aa01", "aa02"}; !slices.Equal(pubkeys(got), want) {
		t.Fatalf("got %v want %v", pubkeys(got), want)
	}
	if got[1].Role != "room" || got[1].Name != "room" || !got[0].InArea || got[0].Stale {
		t.Fatalf("fields: %+v", got)
	}

	// Nil filter funcs filter nothing.
	got, _ = selectPlannerNodes(all, nil, testArea, 30, plannerNodeFilter{}, now)
	if want := []string{"aa01", "aa02", "aa10", "aa11"}; !slices.Equal(pubkeys(got), want) {
		t.Fatalf("nil filters: got %v want %v", pubkeys(got), want)
	}

	// (0,0) is the "no fix" placeholder: dropped even when the area holds it.
	gulf := geoBBox{S: -0.5, W: -0.5, N: 0.5, E: 0.5}
	got, _ = selectPlannerNodes([]nodeInfo{rptr("null-island", 0, 0), rptr("near", 0.1, 0)}, nil, gulf, 0, plannerNodeFilter{}, now)
	if want := []string{"near"}; !slices.Equal(pubkeys(got), want) {
		t.Fatalf("(0,0): got %v want %v", pubkeys(got), want)
	}
}

func TestSelectPlannerNodesMarginAndInArea(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	midLat := 45.25
	all := []nodeInfo{
		rptr("b01-centre", midLat, -121.75),
		rptr("b02-on-north-edge", testArea.N, -121.75),
		rptr("b03-on-west-edge", midLat, testArea.W),
		rptr("b04-east-20km", midLat, lonOffsetKm(midLat, testArea.E, 20)),
		rptr("b05-east-40km", midLat, lonOffsetKm(midLat, testArea.E, 40)),
		rptr("b06-north-20km", latOffsetKm(testArea.N, 20), -121.75),
		rptr("b07-north-40km", latOffsetKm(testArea.N, 40), -121.75),
		rptr("b08-south-25km", latOffsetKm(testArea.S, -25), -121.75),
		rptr("b09-west-28km-at-north", testArea.N, lonOffsetKm(testArea.N, testArea.W, -28)),
		rptr("b10-west-33km", midLat, lonOffsetKm(midLat, testArea.W, -33)),
	}
	got, _ := selectPlannerNodes(all, nil, testArea, 30, plannerNodeFilter{}, now)
	in := map[string]bool{}
	for _, n := range got {
		in[n.PubKey] = n.InArea
	}
	want := map[string]bool{
		"b01-centre": true, "b02-on-north-edge": true, "b03-on-west-edge": true,
		"b04-east-20km": false, "b06-north-20km": false, "b08-south-25km": false, "b09-west-28km-at-north": false,
	}
	if len(in) != len(want) {
		t.Fatalf("selected %v, want %v", in, want)
	}
	for pk, w := range want {
		if g, ok := in[pk]; !ok || g != w {
			t.Fatalf("%s: selected=%v InArea=%v, want InArea=%v (all: %v)", pk, ok, g, w, in)
		}
	}
	// In-area first, then by pubkey.
	if wantOrder := []string{"b01-centre", "b02-on-north-edge", "b03-on-west-edge",
		"b04-east-20km", "b06-north-20km", "b08-south-25km", "b09-west-28km-at-north"}; !slices.Equal(pubkeys(got), wantOrder) {
		t.Fatalf("order %v want %v", pubkeys(got), wantOrder)
	}

	// marginKm 0: only the area itself.
	got, _ = selectPlannerNodes(all, nil, testArea, 0, plannerNodeFilter{}, now)
	if wantOrder := []string{"b01-centre", "b02-on-north-edge", "b03-on-west-edge"}; !slices.Equal(pubkeys(got), wantOrder) {
		t.Fatalf("margin 0: %v", pubkeys(got))
	}
}

func TestPlannerGraphExpandBBox(t *testing.T) {
	e := plannerExpandBBox(testArea, 30)
	// The east margin, measured along the parallels of the expanded box,
	// is ≥ 30 km everywhere and ≈ 30 km at its highest latitude.
	for _, lat := range []float64{e.S, testArea.S, 45.25, testArea.N, e.N} {
		if d := haversineKm(lat, testArea.E, lat, e.E); d < 30-1e-3 {
			t.Fatalf("east margin at lat %.3f = %.3f km < 30", lat, d)
		}
	}
	if d := haversineKm(e.N, testArea.E, e.N, e.E); math.Abs(d-30) > 0.05 {
		t.Fatalf("east margin at top = %.3f km, want ≈30", d)
	}
	if d := haversineKm(testArea.N, 0, e.N, 0); math.Abs(d-30) > 1e-6 {
		t.Fatalf("north margin = %.6f km", d)
	}
	// Antimeridian: a node just past 180 is inside a box expanded past it.
	area := geoBBox{S: -17.5, W: 179.5, N: -17.0, E: 179.9}
	if !plannerBBoxContains(plannerExpandBBox(area, 30), -17.2, -179.95) {
		t.Fatal("margin across the antimeridian missed a node")
	}
	// A pole-reaching box spans every longitude.
	if p := plannerExpandBBox(geoBBox{S: 89.9, W: 0, N: 89.95, E: 1}, 30); p.W != -180 || p.E != 180 || p.N != 90 {
		t.Fatalf("polar expansion %+v", p)
	}
	// NaN or negative margins expand nothing.
	if plannerExpandBBox(testArea, math.NaN()) != testArea || plannerExpandBBox(testArea, -5) != testArea {
		t.Fatal("bad margin changed the box")
	}
}

func TestSelectPlannerNodesStale(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	c := func(pk, role string, lastSeen time.Time) nodeInfo {
		n := rptr(pk, 45.25, -121.75)
		n.Role, n.LastSeen = role, lastSeen
		return n
	}
	all := []nodeInfo{
		c("c1-old-seen-recent-heard", "repeater", now.Add(-100*time.Hour)),
		c("c2-old-seen-no-heard", "repeater", now.Add(-100*time.Hour)),
		c("c3-recent-seen-old-heard", "repeater", now.Add(-time.Hour)),
		c("c4-never-seen", "repeater", time.Time{}),
		c("c5-room-50h", "room", now.Add(-50*time.Hour)),
		c("c6-repeater-50h", "repeater", now.Add(-50*time.Hour)),
		c("c7-exactly-silent", "repeater", now.Add(-72*time.Hour)),
		c("c8-future", "repeater", now.Add(time.Hour)),
	}
	heard := map[string]time.Time{
		"c1-old-seen-recent-heard": now.Add(-time.Hour),
		"c3-recent-seen-old-heard": now.Add(-200 * time.Hour),
		"c4-never-seen":            {},
	}
	f := plannerNodeFilter{SilentAfter: func(role string) time.Duration {
		if role == "room" {
			return 24 * time.Hour
		}
		return 72 * time.Hour
	}}
	got, _ := selectPlannerNodes(all, heard, testArea, 30, f, now)
	stale := map[string]bool{}
	for _, n := range got {
		stale[n.PubKey] = n.Stale
	}
	want := map[string]bool{
		"c1-old-seen-recent-heard": false, // lastHeard overrides an old LastSeen
		"c2-old-seen-no-heard":     true,
		"c3-recent-seen-old-heard": false, // the newer of the two wins
		"c4-never-seen":            true,
		"c5-room-50h":              true, // room window is 24h
		"c6-repeater-50h":          false,
		"c7-exactly-silent":        false, // strictly greater than the window
		"c8-future":                false,
	}
	for pk, w := range want {
		if g, ok := stale[pk]; !ok || g != w {
			t.Fatalf("%s: stale=%v present=%v, want %v", pk, g, ok, w)
		}
	}

	// Nil SilentAfter: nothing is stale.
	got, _ = selectPlannerNodes(all, heard, testArea, 30, plannerNodeFilter{}, now)
	for _, n := range got {
		if n.Stale {
			t.Fatalf("%s stale with nil SilentAfter", n.PubKey)
		}
	}
}

func TestSelectPlannerNodesCapAndTruncation(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	cLat, cLon := 45.25, -121.75
	var all []nodeInfo
	// 10 in-area nodes in the SW corner, ~34 km from the centre, and 300
	// margin nodes just east of the area, 20-30 km from it: every margin
	// node is nearer the centre than every in-area node, so only the
	// in-area-first rule keeps the corner nodes.
	for i := 0; i < 10; i++ {
		all = append(all, rptr(fmt.Sprintf("in-%02d", i), testArea.S+0.001*float64(i), testArea.W+0.001))
	}
	for i := 0; i < 300; i++ {
		lon := lonOffsetKm(cLat, testArea.E, 0.5+0.03*float64(i))
		all = append(all, rptr(fmt.Sprintf("m-%03d", i), cLat, lon))
	}
	rng := rand.New(rand.NewPCG(3, 4))
	rng.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })

	if far, near := haversineKm(cLat, cLon, testArea.S, testArea.W+0.001),
		haversineKm(cLat, cLon, cLat, lonOffsetKm(cLat, testArea.E, 0.5+0.03*299)); far <= near {
		t.Fatalf("fixture: corner %.1f km not beyond farthest margin node %.1f km", far, near)
	}
	got, trunc := selectPlannerNodes(all, nil, testArea, 30, plannerNodeFilter{}, now)
	if !trunc || len(got) != plannerMaxNodes {
		t.Fatalf("trunc=%v len=%d", trunc, len(got))
	}
	for i := 0; i < 10; i++ {
		if want := fmt.Sprintf("in-%02d", i); got[i].PubKey != want || !got[i].InArea {
			t.Fatalf("got[%d]=%s InArea=%v, want %s in area", i, got[i].PubKey, got[i].InArea, want)
		}
	}
	for i := 10; i < plannerMaxNodes; i++ {
		// The 290 nearest margin nodes, ordered by pubkey.
		if want := fmt.Sprintf("m-%03d", i-10); got[i].PubKey != want || got[i].InArea {
			t.Fatalf("got[%d]=%s, want %s", i, got[i].PubKey, want)
		}
	}

	// Exactly at the cap: nothing dropped, no truncation.
	got, trunc = selectPlannerNodes(all[:plannerMaxNodes], nil, testArea, 30, plannerNodeFilter{}, now)
	if trunc || len(got) != plannerMaxNodes {
		t.Fatalf("at cap: trunc=%v len=%d", trunc, len(got))
	}

	// More in-area nodes than the cap: the ones nearest the centre stay.
	all = all[:0]
	for i := 0; i < plannerMaxNodes+5; i++ {
		all = append(all, rptr(fmt.Sprintf("z-%03d", i), cLat, cLon+0.0005*float64(i)))
	}
	got, trunc = selectPlannerNodes(all, nil, testArea, 30, plannerNodeFilter{}, now)
	if !trunc || len(got) != plannerMaxNodes || got[len(got)-1].PubKey != fmt.Sprintf("z-%03d", plannerMaxNodes-1) {
		t.Fatalf("in-area cap: trunc=%v len=%d last=%s", trunc, len(got), got[len(got)-1].PubKey)
	}
}

// ─── observedPlannerEdges ──────────────────────────────────────────────────

func TestObservedPlannerEdges(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	nodes := []plannerNode{
		{PubKey: "aaaa", Lat: 45.0, Lon: -122.0},
		{PubKey: "bbbb", Lat: 45.1, Lon: -122.0},
		{PubKey: "cccc", Lat: 45.2, Lon: -122.0},
		{PubKey: "dddd", Lat: 45.3, Lon: -122.0},
		{PubKey: "EEEE", Lat: 45.4, Lon: -122.0},
		{PubKey: "ffff", Lat: 45.5, Lon: -122.0},
		{PubKey: "gggg", Lat: 45.6, Lon: -122.0},
	}
	three := map[string]bool{"o1": true, "o2": true, "o3": true}
	strong := func(a, b string, modes map[int]int) NeighborEdge {
		return NeighborEdge{NodeA: a, NodeB: b, Count: 100, LastSeen: now, Observers: three, CountsByMode: modes}
	}
	ambiguous := strong("aaaa", "gggg", map[int]int{2: 100})
	ambiguous.Ambiguous = true
	unresolved := strong("aaaa", "", map[int]int{2: 100})
	weak := strong("cccc", "gggg", map[int]int{2: 100})
	weak.Count = 4 // score 0.04 < 0.05
	oneObserver := strong("dddd", "gggg", map[int]int{2: 100})
	oneObserver.Observers = map[string]bool{"o1": true} // 1.0 × 1/3 < 0.5
	decayed := strong("ffff", "gggg", map[int]int{3: 100})
	decayed.LastSeen = now.Add(-30 * 24 * time.Hour) // ~0.05 after 4+ half-lives
	edges := []NeighborEdge{
		ambiguous,
		unresolved,
		strong("aaaa", "bbbb", map[int]int{1: 100}),           // 1-byte only: dropped
		strong("bbbb", "cccc", map[int]int{2: 100}),           // 2-byte: kept
		strong("cccc", "bbbb", map[int]int{2: 50, 1: 50}),     // duplicate of the above, reversed
		strong("CCCC", "dddd", map[int]int{0: 100}),           // legacy: kept
		strong("dddd", "eeee", nil),                           // no tally: kept (and case-insensitive)
		strong("aaaa", "cccc", map[int]int{1: 99, 3: 1}),      // any 3-byte evidence: kept
		strong("aaaa", "dddd", map[int]int{0: 50, 1: 50}),     // legacy + 1-byte, not legacy-only: dropped
		strong("aaaa", "zzzz", map[int]int{3: 100}),           // endpoint not in nodes
		strong("bbbb", "bbbb", map[int]int{3: 100}),           // self
		strong("eeee", "ffff", map[int]int{0: 0, 1: 0, 2: 7}), // zero buckets ignored
		weak,
		oneObserver,
		decayed,
	}
	got := observedPlannerEdges(nodes, edges, now, 0.5)
	type pair struct{ a, b int }
	var gotPairs []pair
	for _, e := range got {
		if e.Class != "observed" || e.ClearancePct != 0 || e.A >= e.B {
			t.Fatalf("bad edge %+v", e)
		}
		if want := haversineKm(nodes[e.A].Lat, nodes[e.A].Lon, nodes[e.B].Lat, nodes[e.B].Lon); e.DistanceKm != want {
			t.Fatalf("distance %v want %v", e.DistanceKm, want)
		}
		gotPairs = append(gotPairs, pair{e.A, e.B})
	}
	want := []pair{{0, 2}, {1, 2}, {2, 3}, {3, 4}, {4, 5}}
	if !slices.Equal(gotPairs, want) {
		t.Fatalf("pairs %v want %v", gotPairs, want)
	}
	if math.Abs(got[0].DistanceKm-22.24) > 0.01 {
		t.Fatalf("0.2° of latitude = %.3f km", got[0].DistanceKm)
	}

	// minScore gates on Score×Confidence: a low threshold admits the weak,
	// single-observer and decayed edges too.
	got = observedPlannerEdges(nodes, edges, now, 0.01)
	if len(got) != 8 {
		t.Fatalf("minScore 0.01: %d edges %+v", len(got), got)
	}

	// The threshold is inclusive: the strong edges score exactly 1.0.
	if got = observedPlannerEdges(nodes, edges, now, 1.0); len(got) != 5 {
		t.Fatalf("minScore 1.0: %d edges %+v", len(got), got)
	}

	if out := observedPlannerEdges(nil, edges, now, 0); len(out) != 0 {
		t.Fatalf("no nodes: %+v", out)
	}
}

func TestPlannerGraphEdgeEvidence(t *testing.T) {
	cases := []struct {
		modes     map[int]int
		ambiguous bool
		resolved  bool
		want      bool
	}{
		{nil, false, false, true},
		{map[int]int{}, false, false, true},
		{map[int]int{0: 5}, false, false, true},
		{map[int]int{0: 5}, true, true, true}, // Jaccard-resolved legacy
		{map[int]int{0: 5}, true, false, false},
		{map[int]int{1: 5}, false, true, false},
		{map[int]int{0: 5, 1: 1}, false, false, false},
		{map[int]int{2: 1}, true, false, true}, // evidence only; ambiguity is the caller's rule
		{map[int]int{1: 9, 3: 1}, false, false, true},
	}
	for i, tc := range cases {
		e := &NeighborEdge{CountsByMode: tc.modes, Ambiguous: tc.ambiguous, Resolved: tc.resolved}
		if got := plannerEdgeEvidenceOK(e); got != tc.want {
			t.Fatalf("case %d %+v: got %v", i, tc, got)
		}
	}
}

// The operator geo_filter /api/nodes applies must also hide nodes from the
// planner (security R21: no node the API hides may surface here).
func TestSelectPlannerNodesHonoursGeoFilter(t *testing.T) {
	now := time.Now()
	all := []nodeInfo{
		{PublicKey: "aa01", Name: "Inside", Role: "repeater", Lat: 30.30, Lon: -97.80, HasGPS: true, LastSeen: now},
		{PublicKey: "bb02", Name: "Filtered", Role: "repeater", Lat: 30.32, Lon: -97.70, HasGPS: true, LastSeen: now},
	}
	area := geoBBox{S: 30.2, W: -97.9, N: 30.4, E: -97.6}
	f := plannerNodeFilter{PassesGeo: func(lat, lon float64) bool { return lon < -97.75 }}
	nodes, _ := selectPlannerNodes(all, nil, area, 0, f, now)
	if len(nodes) != 1 || nodes[0].PubKey != "aa01" {
		t.Fatalf("geo-filtered node leaked into planner nodes: %+v", nodes)
	}
}
