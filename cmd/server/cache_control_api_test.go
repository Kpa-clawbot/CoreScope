package main

// Issue #1551: /api/* responses must emit Cache-Control: no-store so
// CDNs (Cloudflare, nginx, Varnish) do not cache JSON. Static assets
// (app.js, /, etc.) intentionally remain CDN-cacheable.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gorilla/mux"
)

// TestAPIRoutesEmitNoStoreCacheControl asserts every covered /api/*
// endpoint sets Cache-Control: no-store. This is a black-box test
// against the real router, exercising whatever middleware chain is
// wired by RegisterRoutes.
func TestAPIRoutesEmitNoStoreCacheControl(t *testing.T) {
	_, router := setupTestServer(t)

	apiPaths := []string{
		"/api/stats",
		"/api/observers",
		"/api/packets?limit=10",
		"/api/nodes?limit=10",
	}

	for _, p := range apiPaths {
		t.Run(p, func(t *testing.T) {
			req := httptest.NewRequest("GET", p, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("%s: expected 200, got %d (body: %s)", p, w.Code, w.Body.String())
			}
			cc := w.Header().Get("Cache-Control")
			if cc != "no-store" {
				t.Errorf("%s: expected Cache-Control: no-store, got %q", p, cc)
			}
		})
	}
}

// TestStaticAssetsDoNotEmitNoStore guards against scope creep: the
// no-store middleware must be scoped to /api/* only. Static assets
// (HTML, JS, CSS) keep their existing browser-cache headers
// ("no-cache, no-store, must-revalidate" today via spaHandler) and
// must NOT be downgraded to bare "no-store" by the API middleware —
// i.e. the API middleware must not run on these paths. If a future
// change moves static assets behind no-store middleware, CDN caching
// of immutable hashed assets breaks; assert the contract explicitly.
func TestStaticAssetsDoNotEmitBareNoStore(t *testing.T) {
	// Build a temp public dir so spaHandler has real files to serve.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>SPA</html>"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.js"), []byte("console.log('app')"), 0644); err != nil {
		t.Fatal(err)
	}

	_, router := setupTestServer(t)
	// Wire the SPA handler exactly the way main.go does for non-/api paths.
	fs := http.FileServer(http.Dir(dir))
	router.PathPrefix("/").Handler(spaHandler(dir, fs))

	cases := []struct {
		path        string
		wantCacheCC string
	}{
		// spaHandler sets this exact value for HTML/JS/CSS.
		{"/app.js", "no-cache, no-store, must-revalidate"},
		{"/", "no-cache, no-store, must-revalidate"},
	}

	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			req := httptest.NewRequest("GET", c.path, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			cc := w.Header().Get("Cache-Control")
			if cc == "no-store" {
				t.Errorf("%s: API no-store middleware leaked onto static asset (got bare %q, expected %q)", c.path, cc, c.wantCacheCC)
			}
			if cc != c.wantCacheCC {
				t.Errorf("%s: expected Cache-Control %q, got %q", c.path, c.wantCacheCC, cc)
			}
		})
	}
}

// Ensure mux import used (test compiles even if setupTestServer signature
// changes).
var _ = mux.NewRouter
