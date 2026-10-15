package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func packetFilterLockStore(t *testing.T) *PacketStore {
	t.Helper()
	db := setupTestDB(t)
	mustExecDB(t, db, `INSERT INTO observers (id, iata) VALUES ('obs-sjc', 'SJC'), ('obs-lax', 'LAX')`)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, lat, lon) VALUES ('inside-node', 50.85, 4.35), ('outside-node', 48.0, 4.35)`)
	s := newTestStoreWithDB(t, db, &Config{Areas: map[string]AreaEntry{
		"BEL":   {Polygon: [][2]float64{{50, 2.5}, {51.5, 2.5}, {51.5, 6.4}, {50, 6.4}}},
		"EMPTY": {Polygon: [][2]float64{{0, 0}, {1, 0}, {1, 1}, {0, 1}}},
	}})
	pt := PayloadADVERT
	s.packets = []*StoreTx{
		{ID: 1, Hash: "inside", FirstSeen: "2026-01-01T00:00:01Z", LatestSeen: "2026-01-01T00:00:01Z", PayloadType: &pt,
			DecodedJSON: `{"public_key":"inside-node"}`, Observations: []*StoreObs{{ObserverID: "obs-sjc"}}},
		{ID: 2, Hash: "other-region", FirstSeen: "2026-01-01T00:00:02Z", LatestSeen: "2026-01-01T00:00:02Z", PayloadType: &pt,
			DecodedJSON: `{"public_key":"inside-node"}`, Observations: []*StoreObs{{ObserverID: "obs-lax"}}},
		{ID: 3, Hash: "outside-area", FirstSeen: "2026-01-01T00:00:03Z", LatestSeen: "2026-01-01T00:00:03Z", PayloadType: &pt,
			DecodedJSON: `{"public_key":"outside-node"}`, Observations: []*StoreObs{{ObserverID: "obs-sjc"}}},
	}
	return s
}

func packetFilterHashes(result *PacketResult) []string {
	hashes := make([]string, 0, len(result.Packets))
	for _, packet := range result.Packets {
		hashes = append(hashes, packet["hash"].(string))
	}
	return hashes
}

func assertColdFilterAllowsStoreReaders(t *testing.T, s *PacketStore, kind string, query func()) {
	t.Helper()
	s.clockSkew = &ClockSkewEngine{}
	s.signalStartupLoadDone()
	hook, _, entered, release := blockingLoadHook(kind)
	s.cacheLoadHook = hook
	var workers sync.WaitGroup
	var releaseOnce sync.Once
	releaseSQL := func() { releaseOnce.Do(func() { close(release) }) }
	defer func() { releaseSQL(); workers.Wait() }()
	oldReadiness := readiness.Swap(1)
	t.Cleanup(func() { readiness.Store(oldReadiness) })
	t.Setenv("CORESCOPE_INGESTOR_STATS", filepath.Join(t.TempDir(), "missing.json"))

	queryDone := make(chan struct{})
	workers.Add(1)
	go func() { defer workers.Done(); query(); close(queryDone) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("query did not reach the cold filter lookup")
	}

	// Queue the poller writer while the filter's database work is blocked.
	// Both the writer and healthz must complete before SQL is released.
	writerStarted, writerDone := make(chan struct{}), make(chan struct{})
	workers.Add(1)
	go func() {
		defer workers.Done()
		close(writerStarted)
		s.mu.Lock()
		s.mu.Unlock()
		close(writerDone)
	}()
	<-writerStarted
	healthDone := make(chan int, 1)
	workers.Add(1)
	go func() {
		defer workers.Done()
		w := httptest.NewRecorder()
		(&Server{store: s}).handleHealthz(w, httptest.NewRequest(http.MethodGet, "/api/healthz", nil))
		healthDone <- w.Code
	}()
	select {
	case <-writerDone:
	case <-time.After(time.Second):
		t.Error("store writer blocked behind cold filter SQL")
	}
	select {
	case status := <-healthDone:
		if status != http.StatusOK {
			t.Errorf("healthz status = %d, want 200", status)
		}
	case <-time.After(time.Second):
		t.Error("healthz blocked behind cold filter SQL and the queued writer")
	}
	releaseSQL()
	select {
	case <-queryDone:
	case <-time.After(time.Second):
		t.Fatal("query did not finish after releasing filter SQL")
	}
}

func TestColdPacketFiltersDoNotHoldStoreLock(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		for _, filter := range []struct {
			name, kind string
			q          PacketQuery
			want       []string
		}{
			{"region", "regionObs", PacketQuery{Region: "SJC"}, []string{"outside-area", "inside"}},
			{"area", "areaNodes", PacketQuery{Area: "BEL"}, []string{"other-region", "inside"}},
		} {
			name := "raw/" + filter.name
			if grouped {
				name = "grouped/" + filter.name
			}
			t.Run(name, func(t *testing.T) {
				s := packetFilterLockStore(t)
				query := s.QueryPackets
				if grouped {
					query = s.QueryGroupedPackets
				}
				var got *PacketResult
				assertColdFilterAllowsStoreReaders(t, s, filter.kind, func() { got = query(filter.q) })
				if hashes := packetFilterHashes(got); !reflect.DeepEqual(hashes, filter.want) || got.Total != len(filter.want) {
					t.Errorf("filtered packets = %v, total %d; want %v", hashes, got.Total, filter.want)
				}
			})
		}
	}
}

func TestColdRegionConsumersDoNotHoldStoreLock(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query func(*PacketStore)
	}{
		{"channels", func(s *PacketStore) { s.GetChannels("SJC") }},
		{"encrypted-channels", func(s *PacketStore) { s.GetEncryptedChannels("SJC") }},
		{"channel-messages", func(s *PacketStore) { s.GetChannelMessages("public", 10, 0, "SJC") }},
		{"analytics-channels", func(s *PacketStore) { s.computeAnalyticsChannels("SJC", "", TimeWindow{}) }},
		{"analytics-rf", func(s *PacketStore) { s.computeAnalyticsRF("SJC", "", TimeWindow{}) }},
		{"windowed-subpaths", func(s *PacketStore) { s.computeAnalyticsSubpathsWindowed("SJC", 2, 3, 10, TimeWindow{}) }},
		{"subpaths", func(s *PacketStore) { s.computeAnalyticsSubpaths("SJC", 2, 3, 10) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := packetFilterLockStore(t)
			s.nodePM, s.nodeCacheTime = buildPrefixMap(nil), time.Now()
			assertColdFilterAllowsStoreReaders(t, s, "regionObs", func() { tc.query(s) })
		})
	}
}

func TestPacketFilterEmptyAndUnknownSets(t *testing.T) {
	all := []string{"outside-area", "other-region", "inside"}
	for _, grouped := range []bool{false, true} {
		for _, tc := range []struct {
			name string
			q    PacketQuery
			want []string
		}{
			{"no-filters", PacketQuery{}, all},
			{"unknown-region", PacketQuery{Region: "UNKNOWN"}, []string{}},
			{"unknown-region-with-area", PacketQuery{Region: "UNKNOWN", Area: "BEL"}, []string{}},
			{"unknown-area", PacketQuery{Area: "UNKNOWN"}, all},
			{"empty-area", PacketQuery{Area: "EMPTY"}, all},
			{"combined", PacketQuery{Region: "SJC", Area: "BEL"}, []string{"inside"}},
		} {
			name := "raw/" + tc.name
			if grouped {
				name = "grouped/" + tc.name
			}
			t.Run(name, func(t *testing.T) {
				s := packetFilterLockStore(t)
				if tc.q.Region == "UNKNOWN" {
					s.cacheLoadHook = func(kind string) {
						if kind == "areaNodes" {
							t.Error("empty region should short-circuit the area lookup")
						}
					}
				}
				query := s.QueryPackets
				if grouped {
					query = s.QueryGroupedPackets
				}
				got := query(tc.q)
				if hashes := packetFilterHashes(got); !reflect.DeepEqual(hashes, tc.want) || got.Total != len(tc.want) {
					t.Errorf("filtered packets = %v, total %d; want %v", hashes, got.Total, tc.want)
				}
			})
		}
	}
}

func TestPacketFilterUsesResolvedSnapshotAfterCacheEviction(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		name := "raw"
		if grouped {
			name = "grouped"
		}
		t.Run(name, func(t *testing.T) {
			s := packetFilterLockStore(t)
			query := s.QueryPackets
			if grouped {
				query = s.QueryGroupedPackets
			}
			hook, _, entered, release := blockingLoadHook("areaNodes")
			var regionLoads atomic.Int32
			s.cacheLoadHook = func(kind string) {
				if kind == "regionObs" {
					regionLoads.Add(1)
				}
				hook(kind)
			}
			var releaseOnce sync.Once
			releaseSQL := func() { releaseOnce.Do(func() { close(release) }) }
			var worker sync.WaitGroup
			defer func() { releaseSQL(); worker.Wait() }()
			result := make(chan *PacketResult, 1)
			worker.Add(1)
			go func() {
				defer worker.Done()
				result <- query(PacketQuery{Region: "SJC", Area: "BEL"})
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("query did not resolve its region before the area lookup")
			}
			// The refresh may evict a region between its lookup and the
			// locked packet scan. Retain the set; do not query it again.
			s.regionObsMu.Lock()
			delete(s.regionObsCache, "SJC")
			s.regionObsMu.Unlock()
			releaseSQL()
			got := <-result
			if hashes := packetFilterHashes(got); !reflect.DeepEqual(hashes, []string{"inside"}) {
				t.Errorf("snapshot-filtered packets = %v, want [inside]", hashes)
			}
			if loads := regionLoads.Load(); loads != 1 {
				t.Errorf("region loaded %d times; query must retain its first snapshot", loads)
			}
		})
	}
}

func TestPacketFiltersPreserveWarmAndStaleCaches(t *testing.T) {
	for _, grouped := range []bool{false, true} {
		for _, stale := range []bool{false, true} {
			for _, kind := range []string{"regionObs", "areaNodes"} {
				name := "raw/warm/" + kind
				if grouped {
					name = "grouped/warm/" + kind
				}
				if stale {
					name += "/stale"
				}
				t.Run(name, func(t *testing.T) {
					s := packetFilterLockStore(t)
					query := s.QueryPackets
					if grouped {
						query = s.QueryGroupedPackets
					}
					cachedAt := time.Now()
					if stale {
						cachedAt = cachedAt.Add(-time.Hour)
					}
					q, want, refreshKey := PacketQuery{Region: "SJC"}, "other-region", "regionObs"
					// Deliberately differ from the database, proving the cached
					// set is served while the refresh is blocked.
					s.regionObsCache["SJC"] = map[string]bool{"obs-lax": true}
					s.regionObsCacheTime = cachedAt
					if kind == "areaNodes" {
						q, want, refreshKey = PacketQuery{Area: "BEL"}, "outside-area", "areaNodes:BEL"
						s.areaNodeCache["BEL"] = map[string]bool{"outside-node": true}
						s.areaNodeCacheTimes["BEL"] = cachedAt
					}
					hook, calls, entered, release := blockingLoadHook(kind)
					s.cacheLoadHook = hook
					defer func() {
						close(release)
						waitUntil(t, "filter refresh to finish", func() bool { return !s.cacheRefresh.running(refreshKey) })
					}()
					var got *PacketResult
					returnsWithin(t, time.Second, "cached packet filter", func() { got = query(q) })
					if hashes := packetFilterHashes(got); !reflect.DeepEqual(hashes, []string{want}) {
						t.Errorf("cached packets = %v, want [%s]", hashes, want)
					}
					if stale {
						select {
						case <-entered:
						case <-time.After(time.Second):
							t.Fatal("stale filter did not start a background refresh")
						}
					} else if atomic.LoadInt64(calls) != 0 {
						t.Fatal("warm filter queried the database")
					}
				})
			}
		}
	}
}
