package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// GET /api/packets?nodes=... resolves every entry with a SQLite lookup while
// holding the packet store's read lock, so the list must be capped.
func TestMultiNodePacketsListCap(t *testing.T) {
	_, router := setupTestServerWithAPIKey(t, "")

	get := func(n int) *httptest.ResponseRecorder {
		keys := make([]string, n)
		for i := range keys {
			keys[i] = fmt.Sprintf("%064x", i+1)
		}
		req := httptest.NewRequest("GET", "/api/packets?nodes="+strings.Join(keys, ",")+"&limit=1", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	t.Run("at the cap is accepted", func(t *testing.T) {
		if w := get(maxMultiNodePubkeys); w.Code != http.StatusOK {
			t.Fatalf("expected 200 for %d nodes, got %d (body: %s)", maxMultiNodePubkeys, w.Code, w.Body.String())
		}
	})

	t.Run("one over the cap is rejected with 400", func(t *testing.T) {
		if w := get(maxMultiNodePubkeys + 1); w.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for %d nodes, got %d (body: %s)", maxMultiNodePubkeys+1, w.Code, w.Body.String())
		}
	})
}
