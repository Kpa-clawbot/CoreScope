package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// GET /api/nodes/{pubkey}/reach is unauthenticated and a cold-cache request
// starts a scan. Only reachColdBuildLimit scans may run at once; a cold
// request that finds every slot taken queues for at most the limiter's wait,
// then gets 429 with Retry-After instead of queueing on the shared SQLite
// pool. Goes through the production router.
func TestNodeReachConcurrentBuildCap(t *testing.T) {
	srv, router := setupTestServerWithAPIKey(t, "")
	srv.reach.cold.configure(0, 5*time.Millisecond, nil) // default cap, short wait
	const key = "ab00000000000000000000000000000000000000000000000000000000000001"

	get := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/nodes/"+key+"/reach?days=7", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	// Fill every build slot as if scans were in flight.
	for i := 0; i < reachColdBuildLimit; i++ {
		if err := srv.reach.cold.acquire(context.Background()); err != nil {
			t.Fatalf("slot %d should be free: %v", i, err)
		}
	}
	if w := get(); w.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 while all build slots are busy, got %d (body: %s)", w.Code, w.Body.String())
	} else if w.Header().Get("Retry-After") == "" {
		t.Fatalf("429 must carry Retry-After")
	}

	// Free the slots: the same request now runs the scan. The key is not a
	// known node, so the normal answer is 404 — the point is that it is no
	// longer 429.
	for i := 0; i < reachColdBuildLimit; i++ {
		srv.reach.cold.release()
	}
	if w := get(); w.Code == http.StatusTooManyRequests {
		t.Fatalf("still 429 after slots were released (body: %s)", w.Body.String())
	}
}
