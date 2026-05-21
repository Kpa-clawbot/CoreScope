package main

// Tests added in response to PR #934 review findings. These tests demonstrate
// the four behaviors the original implementation lacked:
//
//   1. gzipResponseWriter must implement http.Flusher (SSE / streaming).
//   2. gzipResponseWriter must implement http.Hijacker (WebSocket / raw conn).
//   3. gzip.Writer instances must be pooled (sync.Pool) to avoid the
//      ~256KB window allocation per request.
//   4. A content-type allow-list must skip already-compressed payloads
//      (images, video, application/x-gzip, …) and must skip responses
//      whose handler already set its own Content-Encoding header.

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

func TestGZipResponseWriter_ImplementsFlusher(t *testing.T) {
	seen := false
	handler := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Flusher); ok {
			seen = true
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	req := httptest.NewRequest("GET", "/api/events", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	handler.ServeHTTP(httptest.NewRecorder(), req)
	if !seen {
		t.Error("gzipResponseWriter must implement http.Flusher (required for SSE / streaming endpoints)")
	}
}

func TestGZipResponseWriter_ImplementsHijacker(t *testing.T) {
	seen := false
	handler := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Hijacker); ok {
			seen = true
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	srv := httptest.NewServer(handler)
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/api/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if !seen {
		t.Error("gzipResponseWriter must implement http.Hijacker (required for raw conn / WebSocket upgrade)")
	}
}

func TestGZipMiddleware_SkipsImageContentType(t *testing.T) {
	payload := strings.Repeat("\x89PNGfakebinary", 64)
	handler := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte(payload))
	}))
	req := httptest.NewRequest("GET", "/tiles/1.png", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if got := rr.Header().Get("Content-Encoding"); got == "gzip" {
		t.Errorf("image/png responses must NOT be gzip-encoded, got Content-Encoding=%q", got)
	}
	if rr.Body.String() != payload {
		t.Errorf("image body was mutated; expected pass-through")
	}
}

func TestGZipMiddleware_SkipsAlreadyEncodedResponses(t *testing.T) {
	handler := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "br")
		w.Write([]byte("alreadybrotlied"))
	}))
	req := httptest.NewRequest("GET", "/api/x", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if got := rr.Header().Get("Content-Encoding"); got != "br" {
		t.Errorf("handler-set Content-Encoding must be preserved, got %q (gzip middleware double-wrapped)", got)
	}
}

func TestGZipMiddleware_AllowsJSON(t *testing.T) {
	body := `{"nodes":[{"id":"abc"}]}`
	handler := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Write([]byte(body))
	}))
	req := httptest.NewRequest("GET", "/api/nodes", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("application/json must still be compressed, got %q", rr.Header().Get("Content-Encoding"))
	}
	gz, err := gzip.NewReader(rr.Body)
	if err != nil {
		t.Fatalf("invalid gzip: %v", err)
	}
	defer gz.Close()
	decoded, _ := io.ReadAll(gz)
	if string(decoded) != body {
		t.Errorf("decoded=%q, want %q", string(decoded), body)
	}
}

func TestGZipMiddleware_PoolReusesWriters(t *testing.T) {
	body := strings.Repeat("x", 1024)
	handler := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))
	// Warm the pool: first N requests pay the one-time allocation cost.
	for i := 0; i < 16; i++ {
		req := httptest.NewRequest("GET", "/api", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	const N = 200
	for i := 0; i < N; i++ {
		req := httptest.NewRequest("GET", "/api", nil)
		req.Header.Set("Accept-Encoding", "gzip")
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	allocBytes := after.TotalAlloc - before.TotalAlloc

	// Each gzip.Writer carries a ~256KB sliding window. Without a sync.Pool,
	// N=200 requests allocate roughly N * 256KB = 50MB. With a pool the
	// per-request alloc footprint should be a tiny fraction of that.
	// 10MB ceiling gives generous headroom for testing.AllocsPerRun noise
	// while still catching a regression to the unpooled implementation.
	if allocBytes > 10*1024*1024 {
		t.Errorf("gzip.Writer not pooled: %d bytes allocated across %d requests (expected ≤10MB)", allocBytes, N)
	}
}
