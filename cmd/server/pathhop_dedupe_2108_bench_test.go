package main

import (
	"fmt"
	"io"
	"log"
	"slices"
	"testing"
	"time"
)

// phdBenchStore: n non-advert transmissions one second apart, each with 3
// raw one-byte hops of which 2 resolve to one of 4000 relays (the shape of
// a relay-heavy store), indexed once as by their first observation.
// resolve returns the keys a further observation of transmission j resolves
// to, freshly allocated as every ingest path allocates them.
func phdBenchStore(n int) (store *PacketStore, resolve func(j int) []string) {
	start := time.Now().UTC().Add(-time.Duration(n+10) * time.Second)
	store = makeTestStore(0, start, 0)
	store.byPathHop = make(map[string][]*StoreTx)
	hopsSeen := map[string]bool{}
	relays := func(i int) (int, int) { return i % 2000, 2000 + (5000+i*31)%2000 }
	resolve = func(j int) []string {
		r1, r2 := relays(j)
		return []string{fmt.Sprintf("%02x%062x", r1%256, r1), fmt.Sprintf("%02x%062x", r2%256, r2)}
	}
	for i := 0; i < n; i++ {
		r1, r2 := relays(i)
		pt := 2
		tx := &StoreTx{
			ID:          i + 1,
			Hash:        fmt.Sprintf("late%07d", i),
			FirstSeen:   start.Add(time.Duration(i) * time.Second).Format(time.RFC3339),
			PathJSON:    fmt.Sprintf(`["%02x","%02x","%02x"]`, r1%256, r2%256, (i*13+5)%256),
			PayloadType: &pt,
		}
		store.packets = append(store.packets, tx)
		store.byHash[tx.Hash] = tx
		store.byTxID[tx.ID] = tx
		store.byPayloadType[pt] = append(store.byPayloadType[pt], tx)
		addTxToPathHopIndex(store.byPathHop, tx)
		store.indexResolvedPathHops(tx, resolve(i), hopsSeen)
	}
	return store, resolve
}

func phdEntriesPerTx(store *PacketStore) float64 {
	entries := 0
	for _, list := range store.byPathHop {
		entries += len(list)
	}
	return float64(entries) / float64(len(store.packets))
}

// BenchmarkLateObservationIndex_2108 measures the store side of
// IngestNewObservations for one late observation through the same relays
// (indexResolvedPathHops under the write lock, as the ingest loop calls it;
// the keys are resolved, so allocated, outside the timer as the resolver
// allocates them before this call) and reports how many byPathHop entries
// a transmission ends up with. Without the dedupe that number grows with
// every observation; with it, it stays at 3 raw + 2 resolved.
func BenchmarkLateObservationIndex_2108(b *testing.B) {
	for _, n := range []int{20000, 100000} {
		b.Run(fmt.Sprintf("txs=%d", n), func(b *testing.B) {
			store, resolve := phdBenchStore(n)
			hopsSeen := map[string]bool{}
			const batch = 1024
			pks := make([][]string, batch)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				j := (i * 7919) % n // spread late observations over the store
				if i%batch == 0 {
					b.StopTimer()
					for k := range pks {
						pks[k] = resolve(((i + k) * 7919) % n)
					}
					b.StartTimer()
				}
				store.mu.Lock()
				store.indexResolvedPathHops(store.packets[j], pks[i%batch], hopsSeen)
				store.mu.Unlock()
			}
			b.StopTimer()
			b.ReportMetric(phdEntriesPerTx(store), "pathhop-entries/tx")
		})
	}
}

// BenchmarkIngestNewObservations_2108 drives the real late-observation
// ingest (SQL scan + resolve + index) for a batch of one new observation per
// transmission through the same relays.
func BenchmarkIngestNewObservations_2108(b *testing.B) {
	prev := log.Writer()
	log.SetOutput(io.Discard)
	defer log.SetOutput(prev)
	db := setupTestDB(b)
	defer db.conn.Close()
	const observers = 512
	phdSeed(b, db, observers)
	base := time.Now().UTC().Add(-30 * time.Minute)
	for i := range phdPaths {
		phdInsertTx(b, db, i, base.Add(time.Duration(i)*time.Second))
		phdInsertObs(b, db, i, 1, base.Add(time.Duration(i*1000+1)*time.Second), false)
	}
	store := phdLoadedStore(b, db)
	store.IngestNewFromDB(0, 1000)
	b.ReportAllocs()
	b.ResetTimer()
	for it := 0; it < b.N; it++ {
		b.StopTimer()
		o := 2 + it%(observers-1)
		since := phdMaxObsID(b, db)
		for i := range phdPaths {
			phdInsertObs(b, db, i, o, base.Add(time.Duration(i*1000+o)*time.Second), false)
		}
		b.StartTimer()
		store.IngestNewObservations(since, 10000)
	}
}

// BenchmarkTrafficShareScoreMap_2108 measures the bulk traffic-share pass
// (cache-miss path of GetRepeaterUsefulnessScoreMap) on realistic sizes.
// obs is how many observations of each transmission were indexed through
// the real indexResolvedPathHops: with obs=1 the index is clean on any
// build; with obs=11 it is what a store holds after 11 observers heard each
// transmission (clean with the dedupe, 10 extra entries per resolved key
// without it). order=reversed reverses every bucket, as when background
// chunks load older transmissions after newer ones: no bucket is in
// ascending ID order, so every full-key bucket takes the sort path.
func BenchmarkTrafficShareScoreMap_2108(b *testing.B) {
	for _, c := range []struct {
		n, obs   int
		reversed bool
	}{{20000, 1, false}, {100000, 1, false}, {20000, 1, true}, {100000, 1, true}, {20000, 11, false}, {100000, 11, false}} {
		order := "ingest"
		if c.reversed {
			order = "reversed"
		}
		b.Run(fmt.Sprintf("txs=%d/obs=%d/order=%s", c.n, c.obs, order), func(b *testing.B) {
			store, resolve := phdBenchStore(c.n)
			hopsSeen := map[string]bool{}
			for o := 1; o < c.obs; o++ {
				for j, tx := range store.packets {
					store.indexResolvedPathHops(tx, resolve(j), hopsSeen)
				}
			}
			if c.reversed {
				for _, list := range store.byPathHop {
					slices.Reverse(list)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				store.computeRepeaterUsefulnessScoreMap()
			}
			b.StopTimer()
			b.ReportMetric(phdEntriesPerTx(store), "pathhop-entries/tx")
		})
	}
}
