package main

import (
	"math"
	"math/rand"
	"sort"
	"testing"
)

// Enumerating every simple path is an independent oracle for tiny graphs.
// It deliberately does not use Dijkstra, Brandes, or production graph helpers.
func simplePathScores(edges []BridgeEdge) (map[string]float64, map[string]float64) {
	keySet := make(map[string]bool)
	for _, e := range edges {
		keySet[e.A], keySet[e.B] = true, true
	}
	keys := make([]string, 0, len(keySet))
	for k := range keySet {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	index := make(map[string]int, len(keys))
	cost := make([][]float64, len(keys))
	for i, k := range keys {
		index[k] = i
		cost[i] = make([]float64, len(keys))
	}
	for _, e := range edges {
		a, b := index[e.A], index[e.B]
		cost[a][b], cost[b][a] = 1/e.Weight, 1/e.Weight
	}
	bridge, coverage := make([]float64, len(keys)), make([]float64, len(keys))
	for source := range keys {
		for target := source + 1; target < len(keys); target++ {
			best, count := math.Inf(1), 0
			via := make([]int, len(keys))
			path := []int{source}
			visited := make([]bool, len(keys))
			visited[source] = true
			var walk func(int, float64)
			walk = func(node int, distance float64) {
				if distance > best+1e-12 {
					return
				}
				if node == target {
					if distance < best-1e-12 {
						best, count = distance, 0
						clear(via)
					}
					count++
					for _, mid := range path[1 : len(path)-1] {
						via[mid]++
					}
					return
				}
				for next, length := range cost[node] {
					if length == 0 || visited[next] {
						continue
					}
					visited[next] = true
					path = append(path, next)
					walk(next, distance+length)
					path = path[:len(path)-1]
					visited[next] = false
				}
			}
			walk(source, 0)
			if count == 0 {
				continue
			}
			coverage[source] += 1 / best
			coverage[target] += 1 / best
			for i, paths := range via {
				bridge[i] += float64(paths) / float64(count)
			}
		}
	}
	normalize := func(values []float64) map[string]float64 {
		maxValue := 0.0
		for _, v := range values {
			maxValue = math.Max(maxValue, v)
		}
		out := make(map[string]float64, len(keys))
		for i, k := range keys {
			out[k] = 0
			if maxValue > 0 {
				out[k] = values[i] / maxValue
			}
		}
		return out
	}
	return normalize(bridge), normalize(coverage)
}

func assertGraphScores(t *testing.T, got, want map[string]float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d nodes, want %d", len(got), len(want))
	}
	for key, expected := range want {
		value, ok := got[key]
		if !ok || math.IsNaN(value) || math.Abs(value-expected) > 1e-9 {
			t.Errorf("score[%s] = %v (present %v), want %v", key, value, ok, expected)
		}
	}
}

func TestGraphScoresMatchSimplePaths(t *testing.T) {
	for seed := int64(0); seed < 20; seed++ {
		for _, edges := range [][]BridgeEdge{
			graphScoreFixture(7, 7+int(seed)%10, 1, seed),
			graphScoreFixture(8, 10, 2, seed),
		} {
			bridge, coverage := simplePathScores(edges)
			assertGraphScores(t, ComputeBridgeScores(edges), bridge)
			assertGraphScores(t, ComputeCoverageScores(edges), coverage)
		}
	}
}

func TestGraphScoresNormalizeAndFilterEdges(t *testing.T) {
	clean := []BridgeEdge{{"a", "b", 1}, {"b", "c", .5}, {"c", "d", 1}, {"b", "d", .25}}
	dirty := []BridgeEdge{
		{" A ", "B", 1}, {"b", "c ", .5}, {"C", "d", 1}, {"B", " D", .25},
		{"b", "a", .1}, {"A", "B", .5}, {"a", "a", 1}, {"", "z", 1},
		{"x", "y", math.NaN()}, {"x", "y", math.Inf(1)}, {"x", "y", math.Inf(-1)},
		{"x", "y", 0}, {"x", "y", -1}, {"x", "y", bridgeMinWeightEpsilon / 2},
	}
	bridge, coverage := simplePathScores(clean)
	for seed := int64(0); seed < 10; seed++ {
		rand.New(rand.NewSource(seed)).Shuffle(len(dirty), func(i, j int) { dirty[i], dirty[j] = dirty[j], dirty[i] })
		assertGraphScores(t, ComputeBridgeScores(dirty), bridge)
		assertGraphScores(t, ComputeCoverageScores(dirty), coverage)
	}
}

func TestBridgeScoresPreserveDistanceTieTolerance(t *testing.T) {
	for _, delta := range []float64{0, 5e-13, 2e-12} {
		edges := []BridgeEdge{{"a", "b", 1}, {"b", "d", 1}, {"a", "c", 1}, {"c", "d", 1}, {"a", "d", 1 / (2 + delta)}}
		middle := 2.0 / 3
		if delta > 1e-12 {
			middle = 1
		}
		want := map[string]float64{"a": 1, "b": middle, "c": middle, "d": 1}
		assertGraphScores(t, ComputeBridgeScores(edges), want)
	}
}

func TestGraphScoresAllocationBudget(t *testing.T) {
	edges := graphScoreFixture(600, 2000, 1, 42)
	// String maps allocated ~1.5M objects; indexed state with boxed heap
	// values still allocated ~1.05M. Bound queue storage by graph size instead.
	for _, axis := range []struct {
		name    string
		compute func([]BridgeEdge) map[string]float64
	}{{"bridge", ComputeBridgeScores}, {"coverage", ComputeCoverageScores}} {
		t.Run(axis.name, func(t *testing.T) {
			allocs := testing.AllocsPerRun(1, func() { axis.compute(edges) })
			if allocs > 25000 {
				t.Fatalf("allocated %.0f objects, budget is 25000", allocs)
			}
		})
	}
}
