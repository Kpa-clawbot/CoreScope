package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestHiddenNamePrefix_1181_NodeHealth asserts that /api/nodes/{pk}/health
// returns 404 for a node whose name starts with a hidden prefix — mirroring
// the existing blacklist guard at the top of handleNodeHealth.
//
// Anti-tautology: this test FAILS if the IsNameHidden guard is removed from
// handleNodeHealth (the handler would 200 with health data instead of 404).
func TestHiddenNamePrefix_1181_NodeHealth(t *testing.T) {
	srv, router := setupTestServer(t)

	pk := "deadbeef00001184"
	if _, err := srv.db.conn.Exec(`INSERT INTO nodes
		(public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, ?, ?, 0, 0, '2026-06-01T00:00:00Z', '2026-06-01T00:00:00Z', 1)`,
		pk, "🚫 health me", "companion"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	get := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/nodes/"+pk+"/health", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	srv.cfg.HiddenNamePrefixes = []string{"🚫"}
	w := get()
	if w.Code != http.StatusNotFound {
		t.Fatalf("hidden: expected 404 from /api/nodes/%s/health, got %d body=%s", pk, w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "health me") {
		t.Fatalf("hidden: name leaked in /health 404 body: %s", w.Body.String())
	}
}

// TestHiddenNamePrefix_1181_BulkHealth asserts /api/nodes/bulk-health filters
// out nodes whose name starts with a hidden prefix — same shape as the
// existing blacklist filter inside handleBulkHealth.
//
// Anti-tautology: remove the IsNameHidden branch from handleBulkHealth and
// the hidden node leaks back into the response array; this assertion fails.
func TestHiddenNamePrefix_1181_BulkHealth(t *testing.T) {
	srv, router := setupTestServer(t)

	pk := "deadbeef00001185"
	if _, err := srv.db.conn.Exec(`INSERT INTO nodes
		(public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, ?, ?, 0, 0, '2026-06-01T00:00:00Z', '2026-06-01T00:00:00Z', 1)`,
		pk, "🚫 bulk me", "companion"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	srv.cfg.HiddenNamePrefixes = []string{"🚫"}
	srv.cfg.NodeBlacklist = []string{"force-filter-branch"} // force the existing blacklist branch on so results-array path is taken
	srv.cfg.SetNodeBlacklist(srv.cfg.NodeBlacklist)

	req := httptest.NewRequest("GET", "/api/nodes/bulk-health?limit=2000", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var arr []map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &arr); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, w.Body.String())
	}
	for _, e := range arr {
		if got, _ := e["public_key"].(string); strings.EqualFold(got, pk) {
			t.Fatalf("hidden node %s leaked through /api/nodes/bulk-health", pk)
		}
	}
}

// TestHiddenNamePrefix_1181_Paths asserts /api/nodes/{pk}/paths returns 404
// for a hidden-prefix node, mirroring blacklist behaviour.
func TestHiddenNamePrefix_1181_Paths(t *testing.T) {
	srv, router := setupTestServer(t)

	pk := "deadbeef00001186"
	if _, err := srv.db.conn.Exec(`INSERT INTO nodes
		(public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, ?, ?, 0, 0, '2026-06-01T00:00:00Z', '2026-06-01T00:00:00Z', 1)`,
		pk, "🚫 paths me", "companion"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	srv.cfg.HiddenNamePrefixes = []string{"🚫"}
	req := httptest.NewRequest("GET", "/api/nodes/"+pk+"/paths", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("hidden: expected 404 from /api/nodes/%s/paths, got %d body=%s", pk, w.Code, w.Body.String())
	}
}

// TestHiddenNamePrefix_1181_Analytics asserts /api/nodes/{pk}/analytics 404s
// for hidden-prefix nodes.
func TestHiddenNamePrefix_1181_Analytics(t *testing.T) {
	srv, router := setupTestServer(t)

	pk := "deadbeef00001187"
	if _, err := srv.db.conn.Exec(`INSERT INTO nodes
		(public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, ?, ?, 0, 0, '2026-06-01T00:00:00Z', '2026-06-01T00:00:00Z', 1)`,
		pk, "🚫 analytics me", "companion"); err != nil {
		t.Fatalf("insert: %v", err)
	}

	srv.cfg.HiddenNamePrefixes = []string{"🚫"}
	req := httptest.NewRequest("GET", "/api/nodes/"+pk+"/analytics", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("hidden: expected 404 from /api/nodes/%s/analytics, got %d body=%s", pk, w.Code, w.Body.String())
	}
}

// TestHiddenNamePrefixesGeneration_Increments asserts the per-source
// generation counter bumps on every Set call — mirrors
// TestConfig_BlacklistGenerationIncrements behaviour. Cache wiring lives in
// a follow-up; the counter is the prerequisite primitive.
func TestHiddenNamePrefixesGeneration_Increments(t *testing.T) {
	cfg := &Config{}
	g0 := cfg.HiddenNamePrefixesGeneration()
	cfg.SetHiddenNamePrefixes([]string{"🚫"})
	g1 := cfg.HiddenNamePrefixesGeneration()
	if g1 != g0+1 {
		t.Fatalf("first SetHiddenNamePrefixes: gen %d -> %d (want +1)", g0, g1)
	}
	cfg.SetHiddenNamePrefixes([]string{"🚫"})
	g2 := cfg.HiddenNamePrefixesGeneration()
	if g2 != g1+1 {
		t.Fatalf("second SetHiddenNamePrefixes: gen %d -> %d (want +1)", g1, g2)
	}
	cfg.SetHiddenNamePrefixes(nil)
	g3 := cfg.HiddenNamePrefixesGeneration()
	if g3 != g2+1 {
		t.Fatalf("nil SetHiddenNamePrefixes: gen %d -> %d (want +1)", g2, g3)
	}
}

// TestHiddenNamePrefixes_ConcurrentAccess hammers Set + IsNameHidden from
// multiple goroutines. Doesn't assert anything beyond "doesn't panic" —
// atomic.Pointer correctness is what we're verifying, race detector is not
// in scope for this PR's CI (see PR scope).
func TestHiddenNamePrefixes_ConcurrentAccess(t *testing.T) {
	cfg := &Config{}
	cfg.SetHiddenNamePrefixes([]string{"🚫"})

	var stop atomic.Bool
	var wg sync.WaitGroup

	// Writer
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; !stop.Load(); i++ {
			if i%2 == 0 {
				cfg.SetHiddenNamePrefixes([]string{"🚫", "test"})
			} else {
				cfg.SetHiddenNamePrefixes([]string{"🚫"})
			}
		}
	}()

	// Readers
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				_ = cfg.IsNameHidden("🚫 something")
				_ = cfg.IsNameHidden("normal name")
			}
		}()
	}

	time.Sleep(250 * time.Millisecond)
	stop.Store(true)
	wg.Wait()
}
