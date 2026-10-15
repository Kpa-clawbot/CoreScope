package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"runtime"
	"testing"
)

// The subpath index used to hold every contiguous run of 2-8 hops of every
// stored transmission's path: on a production instance's 7-day window (41k
// transmissions with 2+ hops) 1.2M distinct keys, 79% of them seen once, and
// 186 MB of an 805 MB live heap. Subpaths are now counted per request, and
// TestSubpathRequestAllocations bounds what one request allocates over the
// same shape of data.
//
// subpathFixture builds transmissions with that shape from synthetic data: path
// lengths follow the production hop-count histogram, and hops are random walks
// over a 400-node graph with 3-byte (6 hex) prefixes, which gives the same
// key-reuse profile (about 80% of keys seen once).
func subpathFixture(numTx int, seed int64) []*StoreTx {
	rng := rand.New(rand.NewSource(seed))
	const nodes = 400
	prefixes, neighbours := subpathGraph(rng, nodes)
	// Hop-count histogram from production (lengths 2..19, then 20 = "20 or more").
	weights := []int{2086, 1394, 1142, 1181, 1396, 2287, 3256, 2595, 2686, 2590, 2079, 2035, 1831, 1413, 1294, 1148, 978, 850, 9107}
	txs := make([]*StoreTx, numTx)
	for i := range txs {
		n := weightedHopCount(rng, weights)
		at := rng.Intn(nodes)
		hops := make([]string, n)
		for h := 0; h < n; h++ {
			hops[h] = prefixes[at]
			at = neighbours[at][rng.Intn(len(neighbours[at]))]
		}
		pj, _ := json.Marshal(hops)
		txs[i] = &StoreTx{ID: i + 1, PathJSON: string(pj)}
	}
	return txs
}

// subpathGraph is a random graph of n nodes with 3-byte prefixes and six
// neighbours each.
func subpathGraph(rng *rand.Rand, n int) ([]string, [][]int) {
	prefixes := make([]string, n)
	neighbours := make([][]int, n)
	for i := range prefixes {
		prefixes[i] = fmt.Sprintf("%06X", rng.Intn(1<<24))
		for k := 0; k < 6; k++ {
			neighbours[i] = append(neighbours[i], rng.Intn(n))
		}
	}
	return prefixes, neighbours
}

// weightedHopCount draws a path length from the histogram: weights[i] is the
// weight of length i+2, and the last bucket stands for 20-44 hops.
func weightedHopCount(rng *rand.Rand, weights []int) int {
	total := 0
	for _, w := range weights {
		total += w
	}
	r := rng.Intn(total)
	for i, w := range weights {
		if r < w {
			if i == len(weights)-1 {
				return 20 + rng.Intn(25) // long floods: 20-44 hops
			}
			return i + 2
		}
		r -= w
	}
	return 2
}

// retainedHeapMB reports how much live heap build() keeps after a full GC.
func retainedHeapMB(build func() interface{}) float64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	keep := build()
	runtime.GC()
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(keep)
	return float64(int64(after.HeapAlloc)-int64(before.HeapAlloc)) / (1 << 20)
}

func TestSubpathFixtureShape(t *testing.T) {
	txs := subpathFixture(20000, 1)
	keys := map[string]int{}
	for _, tx := range txs {
		hops := txGetParsedPath(tx)
		for l := 2; l <= min(8, len(hops)); l++ {
			for s := 0; s+l <= len(hops); s++ {
				keys[fmt.Sprint(hops[s:s+l])]++
			}
		}
	}
	once := 0
	for _, c := range keys {
		if c == 1 {
			once++
		}
	}
	share := float64(once) / float64(len(keys))
	t.Logf("fixture: %d txs, %d distinct subpath keys, %.0f%% seen once", len(txs), len(keys), share*100)
	if share < 0.6 || share > 0.95 {
		t.Fatalf("fixture no longer resembles production key reuse (%.0f%% seen once, production 79%%)", share*100)
	}
}

// TestSubpathRequestAllocations bounds what one analytics request allocates
// to count the production-shaped fixture's subpaths. Counting with a map from
// each named key string to a heap-allocated accumulator allocated about
// 336 MB for its 1.5M keys: on a small instance that is a spike the size of
// the whole store, every time the cache expires. Pointer-free tallies under
// a hash of the name allocate about 110 MB, nearly all of it the one map's
// growth.
func TestSubpathRequestAllocations(t *testing.T) {
	if testing.Short() {
		t.Skip("counts 1.5M subpaths")
	}
	txs := subpathFixture(41000, 1)
	for _, tx := range txs {
		txGetParsedPath(tx)
	}
	s := &PacketStore{packets: txs}
	groups := []subpathGroup{{2, 2, 50}, {3, 3, 50}, {4, 4, 50}, {5, 8, 50}}
	identity := func(h string) string { return h }
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	res, _ := s.topNamedSubpaths(groups, identity)
	runtime.ReadMemStats(&after)
	runtime.KeepAlive(res)
	mb := float64(after.TotalAlloc-before.TotalAlloc) / (1 << 20)
	t.Logf("one subpaths request over 41k production-shaped transmissions allocates %.1f MB", mb)
	const limitMB = 150
	if mb > limitMB {
		t.Fatalf("one subpaths request allocates %.1f MB, limit %d MB", mb, limitMB)
	}
	if got := len(res[0]); got != 50 {
		t.Fatalf("group 2-2 returned %d subpaths, want 50", got)
	}
}
