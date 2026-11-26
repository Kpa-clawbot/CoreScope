package main

import (
	"fmt"
	"math/rand"
	"testing"
)

// benchKey is one well-formed node public key.
var benchKey = fmt.Sprintf("%064x", uint64(0xabcdef0123456789))

// BenchmarkLocationFuzzAnchored: the steady state, a node served again
// from its anchor cell (key decode, LRU lookup + bounds check, no HMAC).
func BenchmarkLocationFuzzAnchored(b *testing.B) {
	f := newTestLocFuzzer(b, 1000, testLocSecret)
	f.Fuzz(benchKey, 50.85, 4.35)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f.Fuzz(benchKey, 50.85, 4.35)
	}
}

// BenchmarkLocationFuzzReanchor: every call lands in a new cell (cell maths,
// two haversines, one HMAC, an LRU write; the grid comes from the anchor).
func BenchmarkLocationFuzzReanchor(b *testing.B) {
	f := newTestLocFuzzer(b, 1000, testLocSecret)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f.Fuzz(benchKey, 50.85+float64(i%1000)*0.01, 4.35)
	}
}

func benchNodes(n int) ([]string, [][2]float64) {
	rng := rand.New(rand.NewSource(1))
	pks := make([]string, n)
	pts := make([][2]float64, n)
	for i := range pks {
		pks[i] = fmt.Sprintf("%064x", rng.Uint64())
		pts[i] = [2]float64{rng.Float64()*120 - 60, rng.Float64()*360 - 180}
	}
	return pks, pts
}

// BenchmarkLocationFuzz10kNodes: one pass over 10,000 anchored nodes (a
// /api/nodes or map payload at scale).
func BenchmarkLocationFuzz10kNodes(b *testing.B) {
	f := newTestLocFuzzer(b, 1000, testLocSecret)
	pks, pts := benchNodes(10000)
	for i, pk := range pks {
		f.Fuzz(pk, pts[i][0], pts[i][1])
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j, pk := range pks {
			f.Fuzz(pk, pts[j][0], pts[j][1])
		}
	}
}

// BenchmarkLocationFuzz10kNodesCold: the same pass on a fresh fuzzer (first
// request after startup: every node anchors, two HMACs each).
func BenchmarkLocationFuzz10kNodesCold(b *testing.B) {
	pks, pts := benchNodes(10000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		f := newTestLocFuzzer(b, 1000, testLocSecret)
		b.StartTimer()
		for j, pk := range pks {
			f.Fuzz(pk, pts[j][0], pts[j][1])
		}
	}
}
