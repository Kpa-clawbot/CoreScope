package main

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// Issue #2237: HTTP readers read StoreTx state that ingest rewrites under
// s.mu.Lock (Observations, LatestSeen, the header observer/path fields,
// the parsed-path cache, trackedBytes, and the hash migration's tx.Hash)
// without holding s.mu. Each test runs an ingest-style writer concurrently
// with one reader; they only fail under `go test -race`.

// ingestObs2237 applies one ingest step to tx the way IngestNewObservations
// does: append an observation, advance LatestSeen, re-pick the best
// observation (the path grows each step, so the header path changes) and
// charge trackedBytes, all under s.mu.Lock.
func ingestObs2237(s *PacketStore, tx *StoreTx, n int) {
	hops := make([]string, n%8+4)
	for i := range hops {
		hops[i] = fmt.Sprintf(`"%02x"`, (n+i)%256)
	}
	obs := &StoreObs{
		ID:             10_000_000 + n,
		TransmissionID: tx.ID,
		ObserverID:     fmt.Sprintf("race-obs-%d", n),
		ObserverName:   "race",
		ObserverIATA:   fmt.Sprintf("I%02d", n%50),
		PathJSON:       "[" + strings.Join(hops, ",") + "]",
		Timestamp:      time.Now().UTC().Add(time.Duration(n) * time.Millisecond).Format(time.RFC3339Nano),
	}
	s.mu.Lock()
	tx.addObservation(obs)
	if obs.Timestamp > tx.LatestSeen {
		tx.LatestSeen = obs.Timestamp
	}
	pickBestObservation(tx)
	s.trackedBytes += estimateStoreObsBytes(obs)
	s.mu.Unlock()
}

// runIngestAgainst2237 runs steps ingest steps over txs in a goroutine and
// calls read until the writer is done.
func runIngestAgainst2237(t *testing.T, s *PacketStore, txs []*StoreTx, steps int, read func(i int)) {
	t.Helper()
	runAgainst2237(steps, func(i int) { ingestObs2237(s, txs[i%len(txs)], i) }, read)
}

// runAgainst2237 calls write(0..steps-1) in a goroutine and read in the
// caller until the writer is done. The writer starts after the first read,
// and read runs at least minReads2237 times, so the two always overlap.
func runAgainst2237(steps int, write, read func(i int)) {
	read(0)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < steps; i++ {
			write(i)
		}
	}()
	for i := 1; ; i++ {
		if i > minReads2237 {
			select {
			case <-done:
				return
			default:
			}
		}
		read(i)
	}
}

const minReads2237 = 20

func makeRaceStore2237(n int) *PacketStore {
	s := makeTestStore(n, time.Now().Add(-time.Hour), 1)
	for _, tx := range s.packets {
		tx.LatestSeen = tx.FirstSeen
		tx.RawHex = "0102030405"
		tx.ObserverID = tx.Observations[0].ObserverID
	}
	return s
}

func TestQueryGroupedPacketsIngestRace2237(t *testing.T) {
	s := makeRaceStore2237(20)
	runIngestAgainst2237(t, s, s.packets, 300, func(i int) {
		if i%2 == 0 {
			// Alternate the cold path (filter + sort) with the warm cache hit.
			s.groupedCacheMu.Lock()
			s.groupedCacheTxs = nil
			s.groupedCacheMu.Unlock()
		}
		res := s.QueryGroupedPackets(PacketQuery{Limit: 50})
		if res.Total != 20 {
			t.Errorf("total = %d, want 20", res.Total)
		}
	})
}

// TestGroupedPageReflectsHeaderAfterIngest2237 pins that a row still shows
// the header fields pickBestObservation chose, and the distinct IATAs of
// every observation, after the snapshot refactor.
func TestGroupedPageReflectsHeaderAfterIngest2237(t *testing.T) {
	s := makeRaceStore2237(3)
	tx := s.packets[2]
	ingestObs2237(s, tx, 5) // 9 hops, I05
	res := s.QueryGroupedPackets(PacketQuery{Limit: 50})
	var row map[string]interface{}
	for _, p := range res.Packets {
		if p["hash"] == tx.Hash {
			row = p
		}
	}
	if row == nil {
		t.Fatalf("tx %s missing from page", tx.Hash)
	}
	if row["observer_id"] != "race-obs-5" {
		t.Errorf("observer_id = %v, want race-obs-5", row["observer_id"])
	}
	if row["path_json"] != tx.PathJSON || row["observation_count"] != 3 || row["latest"] != tx.LatestSeen {
		t.Errorf("row = %v, want path %s, 3 observations, latest %s", row, tx.PathJSON, tx.LatestSeen)
	}
	if got := fmt.Sprint(row["distinct_iatas"]); got != "[I05]" {
		t.Errorf("distinct_iatas = %s, want [I05]", got)
	}
	if res.Packets[0]["hash"] != tx.Hash {
		t.Errorf("first row = %v, want the most recently heard tx %s", res.Packets[0]["hash"], tx.Hash)
	}
}

// Two readers under s.mu.RLock call txGetParsedPath while ingest changes
// the best path: a lazy parse there is two readers writing tx.parsedPath.
func TestTxGetParsedPathReadersRace2237(t *testing.T) {
	s := makeRaceStore2237(1)
	tx := s.packets[0]
	readParsed := func() []string {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return txGetParsedPath(tx)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			readParsed()
		}
	}()
	runIngestAgainst2237(t, s, s.packets, 300, func(int) { readParsed() })
	close(stop)
	wg.Wait()
	if got, want := fmt.Sprint(readParsed()), fmt.Sprint(parsePathJSON(tx.PathJSON)); got != want {
		t.Fatalf("parsed path %s, want %s", got, want)
	}
}

func TestBuildFromStoreIngestRace2237(t *testing.T) {
	var txs []*StoreTx
	for i := 0; i < 10; i++ {
		txs = append(txs, ngMakeTx(i+1, 4, ngFromNodeJSON("aaaa1111"), []*StoreObs{
			ngMakeObs("obs00001", `["bbbb"]`, time.Now().UTC().Format(time.RFC3339), ngFloatPtr(-10)),
		}))
	}
	s := ngTestStore(nil, txs)
	runIngestAgainst2237(t, s, txs, 300, func(int) {
		BuildFromStoreWithOptions(s, BuildOptions{MaxEdgeKm: DefaultMaxEdgeKm})
	})
}

func TestAudioLabBucketsIngestRace2237(t *testing.T) {
	s := makeRaceStore2237(20)
	srv := &Server{store: s}
	runIngestAgainst2237(t, s, s.packets, 300, func(int) {
		w := httptest.NewRecorder()
		srv.handleAudioLabBuckets(w, httptest.NewRequest("GET", "/api/audio-lab/buckets", nil))
		if w.Code != 200 {
			t.Errorf("status %d", w.Code)
		}
	})
}

func TestPerfStoreStatsTrackedBytesRace2237(t *testing.T) {
	s := makeRaceStore2237(5)
	runIngestAgainst2237(t, s, s.packets, 300, func(int) {
		_ = s.GetPerfStoreStats()
		_ = s.GetPerfStoreStatsTyped()
	})
}

func TestStatsTrackedBytesRace2237(t *testing.T) {
	srv, router := setupTestServer(t)
	s := srv.store
	s.mu.RLock()
	txs := append([]*StoreTx(nil), s.packets...)
	s.mu.RUnlock()
	runIngestAgainst2237(t, s, txs, 300, func(int) {
		srv.statsMu.Lock()
		srv.statsCache = nil
		srv.statsMu.Unlock()
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/api/stats", nil))
		if w.Code != 200 {
			t.Errorf("status %d", w.Code)
		}
	})
}

// The content-hash migration (hash_migrate.go) rewrites tx.Hash under
// s.mu.Lock while the observer analytics handler builds its response.
func TestObserverAnalyticsHashMigrationRace2237(t *testing.T) {
	s := makeRaceStore2237(20)
	srv := &Server{store: s}
	runAgainst2237(300, func(i int) {
		s.mu.Lock()
		s.packets[i%len(s.packets)].Hash = fmt.Sprintf("migrated%04d", i)
		s.mu.Unlock()
	}, func(int) {
		req := mux.SetURLVars(httptest.NewRequest("GET", "/api/observers/obs0/analytics", nil), map[string]string{"id": "obs0"})
		w := httptest.NewRecorder()
		srv.handleObserverAnalytics(w, req)
		if w.Code != 200 {
			t.Errorf("status %d", w.Code)
		}
	})
}

// BenchmarkQueryGroupedPackets2237 measures the grouped packets page on a
// 30K-transmission store: cold (filter + sort + page) and warm (3s cache hit,
// page only).
func BenchmarkQueryGroupedPackets2237(b *testing.B) {
	s := makeRaceStore2237(30000)
	for _, limit := range []int{50, 1000} {
		q := PacketQuery{Limit: limit}
		b.Run(fmt.Sprintf("cold/limit=%d", limit), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				s.groupedCacheMu.Lock()
				s.groupedCacheTxs = nil
				s.groupedCacheMu.Unlock()
				s.QueryGroupedPackets(q)
			}
		})
		b.Run(fmt.Sprintf("warm/limit=%d", limit), func(b *testing.B) {
			b.ReportAllocs()
			s.QueryGroupedPackets(q)
			for i := 0; i < b.N; i++ {
				s.groupedCacheMu.Lock()
				s.groupedCacheExp = time.Now().Add(time.Hour)
				s.groupedCacheMu.Unlock()
				s.QueryGroupedPackets(q)
			}
		})
	}
}
