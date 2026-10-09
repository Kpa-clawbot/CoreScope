package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// /api/nodes builds every node row into a map, enriches it and encodes the
// page on every request: on one production instance that was 95 GB of
// allocation over 45 hours (about 6.7 MB per request), a quarter of all bytes
// allocated. The same few queries repeat (three paginated pages of one query
// were 10,000 of 14,000 requests in a day), so identical queries inside
// nodesResponseTTL share one built and encoded response.

func getNodes(t *testing.T, router http.Handler, url string) string {
	t.Helper()
	req := httptest.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("GET %s: %d %s", url, w.Code, w.Body.String())
	}
	return w.Body.String()
}

func countNodeBuilds(srv *Server) *int32 {
	var n int32
	srv.nodesComputeHook = func() { atomic.AddInt32(&n, 1) }
	return &n
}

func TestHandleNodes_IdenticalQueryBuiltOnce(t *testing.T) {
	srv, router := setupTestServer(t)
	builds := countNodeBuilds(srv)
	a := getNodes(t, router, "/api/nodes?limit=50")
	b := getNodes(t, router, "/api/nodes?limit=50")
	if got := atomic.LoadInt32(builds); got != 1 {
		t.Fatalf("expected one build for two identical queries, got %d", got)
	}
	if a != b {
		t.Fatal("cached response differs from the built one")
	}
}

func TestHandleNodes_ParameterOrderSharesEntry(t *testing.T) {
	srv, router := setupTestServer(t)
	builds := countNodeBuilds(srv)
	getNodes(t, router, "/api/nodes?limit=50&offset=0")
	getNodes(t, router, "/api/nodes?offset=0&limit=50")
	if got := atomic.LoadInt32(builds); got != 1 {
		t.Fatalf("expected reordered parameters to share one build, got %d", got)
	}
}

func TestHandleNodes_DistinctQueriesBuiltSeparately(t *testing.T) {
	srv, router := setupTestServer(t)
	builds := countNodeBuilds(srv)
	a := getNodes(t, router, "/api/nodes?limit=50")
	b := getNodes(t, router, "/api/nodes?limit=1")
	if got := atomic.LoadInt32(builds); got != 2 {
		t.Fatalf("expected two builds for two different queries, got %d", got)
	}
	if a == b {
		t.Fatal("different limits returned the same body")
	}
}

func TestHandleNodes_ConcurrentMissesShareOneBuild(t *testing.T) {
	srv, router := setupTestServer(t)
	var n int32
	srv.nodesComputeHook = func() {
		atomic.AddInt32(&n, 1)
		time.Sleep(50 * time.Millisecond) // make sure the callers overlap
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); getNodes(t, router, "/api/nodes?limit=50") }()
	}
	wg.Wait()
	if got := atomic.LoadInt32(&n); got != 1 {
		t.Fatalf("expected 10 concurrent identical requests to share one build, got %d", got)
	}
}

func TestHandleNodes_RebuiltAfterTTL(t *testing.T) {
	srv, router := setupTestServer(t)
	builds := countNodeBuilds(srv)
	getNodes(t, router, "/api/nodes?limit=50")
	srv.expireNodesCacheForTest()
	getNodes(t, router, "/api/nodes?limit=50")
	if got := atomic.LoadInt32(builds); got != 2 {
		t.Fatalf("expected a rebuild once the cached response expired, got %d builds", got)
	}
}

func TestHandleNodes_GeoFilterChangeDropsCache(t *testing.T) {
	srv, router := setupTestServer(t)
	builds := countNodeBuilds(srv)
	getNodes(t, router, "/api/nodes?limit=50")
	srv.setGeoFilter(&GeoFilterConfig{Polygon: [][2]float64{{0, 0}, {0, 1}, {1, 1}}})
	getNodes(t, router, "/api/nodes?limit=50")
	if got := atomic.LoadInt32(builds); got != 2 {
		t.Fatalf("expected a geo-filter change to drop cached node pages, got %d builds", got)
	}
}

// expireNodesCacheForTest drops every cached /api/nodes response.
func (s *Server) expireNodesCacheForTest() { s.invalidateNodesCache() }

// nodesBenchServer serves 1,100 nodes (a production instance lists about
// 1,085), half of them repeaters.
func nodesBenchServer(b *testing.B) http.Handler {
	b.Helper()
	log.SetOutput(io.Discard)
	b.Cleanup(func() { log.SetOutput(os.Stderr) })
	db := setupTestDB(b)
	now := time.Now().UTC()
	for i := 0; i < 1100; i++ {
		role := "companion"
		if i%2 == 0 {
			role = "repeater"
		}
		seen := now.Add(-time.Duration(i%720) * time.Hour).Format(time.RFC3339)
		if _, err := db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			fmt.Sprintf("%064x", i+1000), fmt.Sprintf("Bench node %d", i), role,
			55+float64(i%100)/50, -4+float64(i%80)/40, seen, seen, i%50); err != nil {
			b.Fatal(err)
		}
	}
	srv := NewServer(db, &Config{Port: 3000}, NewHub())
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		b.Fatal(err)
	}
	store.WaitIndexesReady(5 * time.Second)
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)
	return router
}

func benchGet(b *testing.B, router http.Handler, url string) {
	req := httptest.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		b.Fatalf("GET %s: %d", url, w.Code)
	}
}

// BenchmarkHandleNodesRepeated: the production pattern, one query asked over
// and over (the top query was 3,378 of 14,053 requests in a day).
func BenchmarkHandleNodesRepeated(b *testing.B) {
	router := nodesBenchServer(b)
	const url = "/api/nodes?limit=500&offset=0&lastHeard=30d"
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchGet(b, router, url)
	}
}

// BenchmarkHandleNodesUnique: every request a different query, so nothing is
// served from cache; shows the miss path does not get slower.
func BenchmarkHandleNodesUnique(b *testing.B) {
	router := nodesBenchServer(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchGet(b, router, fmt.Sprintf("/api/nodes?limit=500&offset=0&lastHeard=30d&search=&n=%d", i))
	}
}

func TestHandleNodes_FilterListChangesMissCache(t *testing.T) {
	srv, router := setupTestServer(t)
	builds := countNodeBuilds(srv)
	getNodes(t, router, "/api/nodes?limit=50")
	srv.cfg.SetHiddenNamePrefixes([]string{"zz-hidden"})
	getNodes(t, router, "/api/nodes?limit=50")
	srv.cfg.SetNodeBlacklist([]string{"0000000000000000"})
	getNodes(t, router, "/api/nodes?limit=50")
	if got := atomic.LoadInt32(builds); got != 3 {
		t.Fatalf("expected hidden-prefix and blacklist changes to miss the cache, got %d builds", got)
	}
}

func TestNodesCache_BoundedByBytes(t *testing.T) {
	srv := &Server{}
	body := make([]byte, nodesCacheMaxBytes/5) // under the per-entry limit
	for i := 0; i < 20; i++ {
		srv.storeNodesResponse(fmt.Sprintf("q%d", i), body)
		if srv.nodesCacheBytes > nodesCacheMaxBytes {
			t.Fatalf("cache holds %d bytes after %d inserts, limit %d", srv.nodesCacheBytes, i+1, nodesCacheMaxBytes)
		}
	}
	srv.storeNodesResponse("huge", make([]byte, nodesCacheMaxBytes/4+1))
	if _, ok := srv.cachedNodesResponse("huge"); ok {
		t.Fatal("expected an oversized response not to be cached")
	}
}
