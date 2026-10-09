package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"os"
	"runtime"
	"testing"
)

// The subpath index holds every contiguous run of 2-8 hops of every stored
// transmission's path. On a production instance's 7-day window (41k
// transmissions with 2+ hops) that is 1.2M distinct keys, 79% of them seen
// once, and 3.07M entries; a heap profile put 186 MB of a 805 MB live heap in
// addTxToSubpathIndexFull.
//
// subpathFixture builds transmissions with that shape from synthetic data: path
// lengths follow the production hop-count histogram, and hops are random walks
// over a 400-node graph with 3-byte (6 hex) prefixes, which gives the same
// key-reuse profile (about 80% of keys seen once).
func subpathFixture(numTx int, seed int64) []*StoreTx {
	rng := rand.New(rand.NewSource(seed))
	const nodes = 400
	prefixes := make([]string, nodes)
	neighbours := make([][]int, nodes)
	for i := range prefixes {
		prefixes[i] = fmt.Sprintf("%06X", rng.Intn(1<<24))
		for k := 0; k < 6; k++ {
			neighbours[i] = append(neighbours[i], rng.Intn(nodes))
		}
	}
	// Hop-count histogram from production (lengths 2..19, then 20 = "20 or more").
	weights := []int{2086, 1394, 1142, 1181, 1396, 2287, 3256, 2595, 2686, 2590, 2079, 2035, 1831, 1413, 1294, 1148, 978, 850, 9107}
	total := 0
	for _, w := range weights {
		total += w
	}
	pickLen := func() int {
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
	txs := make([]*StoreTx, numTx)
	for i := range txs {
		n := pickLen()
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

// buildSubpathIndexForTest builds the index the way Load does and returns it.
func buildSubpathIndexForTest(txs []*StoreTx) *PacketStore {
	s := &PacketStore{packets: txs}
	s.buildSubpathIndex()
	return s
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

// BenchmarkBuildSubpathIndex reports the live heap the index keeps for a
// production-shaped 7-day window (41k transmissions with paths).
//
//	go test -run '^$' -bench BenchmarkBuildSubpathIndex -count 10 ./cmd/server
func BenchmarkBuildSubpathIndex(b *testing.B) {
	log.SetOutput(io.Discard)
	b.Cleanup(func() { log.SetOutput(os.Stderr) })
	txs := subpathFixture(41000, 1)
	for _, tx := range txs {
		txGetParsedPath(tx) // parse once, outside the measurement
	}
	b.ReportAllocs()
	b.ResetTimer()
	var mb float64
	for i := 0; i < b.N; i++ {
		mb = retainedHeapMB(func() interface{} { return buildSubpathIndexForTest(txs) })
	}
	b.ReportMetric(mb, "retained-MB")
}

// TestSubpathIndexRetainedHeap bounds the live heap the subpath index keeps for
// the production-shaped fixture. It kept 250 MB when a separate key → count
// map duplicated every key of spTxIndex; with counts read as len(txs) it keeps
// about 197 MB. The bound sits between the two.
func TestSubpathIndexRetainedHeap(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 1.5M-key index")
	}
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)
	txs := subpathFixture(41000, 1)
	for _, tx := range txs {
		txGetParsedPath(tx)
	}
	const limitMB = 225
	mb := retainedHeapMB(func() interface{} { return buildSubpathIndexForTest(txs) })
	t.Logf("subpath index for 41k production-shaped transmissions keeps %.1f MB", mb)
	if mb > limitMB {
		t.Fatalf("subpath index keeps %.1f MB, limit %d MB", mb, limitMB)
	}
}
