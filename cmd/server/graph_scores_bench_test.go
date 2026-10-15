package main

import (
	"fmt"
	"math/rand"
	"testing"
)

// graphScoreFixture makes connected mesh components with realistic 64-byte
// pubkeys, duplicate-free edges and reproducible affinity weights.
func graphScoreFixture(n, edgeCount, components int, seed int64) []BridgeEdge {
	rng := rand.New(rand.NewSource(seed))
	keys := make([]string, n)
	for i := range keys {
		keys[i] = fmt.Sprintf("%064x", i+1)
	}
	edges := make([]BridgeEdge, 0, edgeCount)
	seen := make(map[[2]int]bool, edgeCount)
	add := func(a, b int) {
		if a > b {
			a, b = b, a
		}
		pair := [2]int{a, b}
		if a == b || seen[pair] {
			return
		}
		seen[pair] = true
		edges = append(edges, BridgeEdge{A: keys[a], B: keys[b], Weight: 1 / float64(1+rng.Intn(9))})
	}
	size := n / components
	for i := 0; i < n; i++ {
		start := i / size * size
		add(i, start+(i-start+1)%size)
	}
	for len(edges) < edgeCount {
		a := rng.Intn(n)
		add(a, a/size*size+rng.Intn(size))
	}
	return edges
}

func BenchmarkGraphScores(b *testing.B) {
	for _, shape := range []struct {
		name                 string
		n, edges, components int
	}{
		{"mesh600", 600, 2000, 1},
		{"mesh2000", 2000, 10000, 1},
		{"mesh5000", 5000, 30000, 1},
		{"fragmented2000", 2000, 8000, 20},
	} {
		edges := graphScoreFixture(shape.n, shape.edges, shape.components, 42)
		for _, axis := range []struct {
			name    string
			compute func([]BridgeEdge) map[string]float64
		}{{"bridge", ComputeBridgeScores}, {"coverage", ComputeCoverageScores}} {
			b.Run(shape.name+"/"+axis.name, func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					if got := axis.compute(edges); len(got) != shape.n {
						b.Fatalf("got %d nodes, want %d", len(got), shape.n)
					}
				}
			})
		}
	}
}
