package main

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// #2219: a region or area filter seen for the first time loads its set from
// SQLite. That lookup must run before s.mu is taken. Otherwise the ingest
// writer queues behind it, and every later reader (/api/healthz) behind the
// writer, until the query returns.

// newColdLookupStore loads the seedTestData fixture with one configured area
// and leaves the region and area caches cold.
func newColdLookupStore(t *testing.T) *PacketStore {
	t.Helper()
	db := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedTestData(t, db)
	s := NewPacketStore(db, nil)
	s.config = &Config{Areas: map[string]AreaEntry{
		"BAY": {Label: "Bay", Polygon: [][2]float64{{37.0, -122.5}, {38.0, -122.5}, {38.0, -121.5}, {37.0, -121.5}}},
	}}
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestColdRegionAreaLookupDoesNotStallHealthz(t *testing.T) {
	cases := []struct {
		name string
		kind string
		call func(s *PacketStore)
	}{
		{"QueryPackets region", "regionObs", func(s *PacketStore) { s.QueryPackets(PacketQuery{Region: "SJC", Limit: 10}) }},
		{"QueryPackets area", "areaNodes", func(s *PacketStore) { s.QueryPackets(PacketQuery{Area: "BAY", Limit: 10}) }},
		{"QueryGroupedPackets region", "regionObs", func(s *PacketStore) { s.QueryGroupedPackets(PacketQuery{Region: "SJC", Limit: 10}) }},
		{"QueryGroupedPackets area", "areaNodes", func(s *PacketStore) { s.QueryGroupedPackets(PacketQuery{Area: "BAY", Limit: 10}) }},
		{"GetChannels", "regionObs", func(s *PacketStore) { s.GetChannels("SJC") }},
		{"GetEncryptedChannels", "regionObs", func(s *PacketStore) { s.GetEncryptedChannels("SJC") }},
		{"GetChannelMessages", "regionObs", func(s *PacketStore) { s.GetChannelMessages("#test", 10, 0, "SJC") }},
		{"computeAnalyticsChannels", "regionObs", func(s *PacketStore) { s.computeAnalyticsChannels("SJC", "", TimeWindow{}) }},
		{"computeAnalyticsRF", "regionObs", func(s *PacketStore) { s.computeAnalyticsRF("SJC", "", TimeWindow{}) }},
		{"computeAnalyticsSubpathsWindowed", "regionObs", func(s *PacketStore) {
			s.computeAnalyticsSubpathsWindowed("SJC", 2, 8, 10, TimeWindow{Since: "2000-01-01T00:00:00Z"})
		}},
		{"computeAnalyticsSubpaths", "regionObs", func(s *PacketStore) { s.computeAnalyticsSubpaths("SJC", 2, 8, 10) }},
	}

	readiness.Store(1)
	defer readiness.Store(0)

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newColdLookupStore(t)
			hook, _, entered, release := blockingLoadHook(tc.kind)
			s.cacheLoadHook = hook
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()

			done := make(chan struct{})
			go func() { tc.call(s); close(done) }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatalf("%s never reached the cold %s lookup", tc.name, tc.kind)
			}

			writerDone := make(chan struct{})
			go func() { // the ingest poller
				s.mu.Lock()
				s.mu.Unlock()
				close(writerDone)
			}()
			time.Sleep(10 * time.Millisecond) // let the writer queue up

			srv := &Server{store: s}
			w := httptest.NewRecorder()
			returnsWithin(t, time.Second, "/api/healthz during a cold "+tc.kind+" lookup", func() {
				srv.handleHealthz(w, httptest.NewRequest("GET", "/api/healthz", nil))
			})
			if w.Code != http.StatusOK {
				t.Errorf("healthz status %d", w.Code)
			}
			select {
			case <-writerDone:
			case <-time.After(time.Second):
				t.Fatalf("the store writer waited for the cold %s lookup", tc.kind)
			}

			unblock()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatalf("%s did not finish after the lookup was released", tc.name)
			}
		})
	}
}

// The scan uses the set resolved before the lock. A refresh or eviction that
// replaces the cache entry while the caller waits for s.mu must not change
// the result; re-resolving under the lock would pick up the replaced set.
func TestRegionSetResolvedBeforeLockSurvivesEviction(t *testing.T) {
	s := newColdLookupStore(t)
	_, want := s.GetChannelMessages("#test", 10, 0, "SJC")
	if want == 0 {
		t.Fatal("fixture: want at least one #test message heard in SJC")
	}
	s.regionObsMu.Lock()
	s.regionObsCache = nil
	s.regionObsMu.Unlock()

	s.mu.Lock()
	var once sync.Once
	unlock := func() { once.Do(s.mu.Unlock) }
	defer unlock()

	got := make(chan int, 1)
	go func() {
		_, n := s.GetChannelMessages("#test", 10, 0, "SJC")
		got <- n
	}()
	waitUntil(t, "the cold SJC lookup to finish outside s.mu", func() bool {
		s.regionObsMu.Lock()
		defer s.regionObsMu.Unlock()
		_, ok := s.regionObsCache["SJC"]
		return ok
	})

	// A refresh replaces the entry before the caller gets s.mu.
	s.regionObsMu.Lock()
	s.regionObsCache = map[string]map[string]bool{"SJC": {"no-such-observer": true}}
	s.regionObsMu.Unlock()
	unlock()

	select {
	case n := <-got:
		if n != want {
			t.Fatalf("GetChannelMessages total = %d after eviction, want %d from the set resolved before the lock", n, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("GetChannelMessages did not return")
	}
}
