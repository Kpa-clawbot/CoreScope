package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// A frontend served from another origin (a static host listed in
// CORS_ALLOWED_ORIGINS) reads two response headers besides Content-Type:
// Retry-After (api() on a warm-up 503, node-reach.js on a 429) and
// X-CoreScope-Load-Status (warmup-banner.js). Cross-origin, fetch exposes only
// the CORS-safelisted headers unless the server lists the rest in
// Access-Control-Expose-Headers; without it both read as null.

func exposedHeaders(t *testing.T, origins []string, origin string) map[string]bool {
	t.Helper()
	handler := newTestServerWithCORS(origins).corsMiddleware(dummyHandler)
	req := httptest.NewRequest("GET", "/api/stats", nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	got := map[string]bool{}
	for _, v := range rr.Header().Values("Access-Control-Expose-Headers") {
		for _, name := range strings.Split(v, ",") {
			if name = strings.TrimSpace(name); name != "" {
				got[strings.ToLower(name)] = true
			}
		}
	}
	return got
}

func TestCORS_ExposesRetryAfterAndLoadStatus(t *testing.T) {
	for _, tc := range []struct {
		name    string
		origins []string
	}{
		{"listed origin", []string{"https://beta.example"}},
		{"wildcard", []string{"*"}},
	} {
		got := exposedHeaders(t, tc.origins, "https://beta.example")
		for _, want := range []string{"retry-after", "x-corescope-load-status"} {
			if !got[want] {
				t.Errorf("%s: Access-Control-Expose-Headers lacks %s, got %v", tc.name, want, got)
			}
		}
	}
}

func TestCORS_NoExposeHeadersWithoutAnAllowedOrigin(t *testing.T) {
	for _, tc := range []struct {
		name    string
		origins []string
		origin  string
	}{
		{"no CORS list", nil, "https://beta.example"},
		{"unlisted origin", []string{"https://beta.example"}, "https://evil.example"},
		{"same-origin request (no Origin header)", []string{"https://beta.example"}, ""},
	} {
		if got := exposedHeaders(t, tc.origins, tc.origin); len(got) != 0 {
			t.Errorf("%s: expected no Access-Control-Expose-Headers, got %v", tc.name, got)
		}
	}
}
