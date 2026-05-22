package main

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompressionConfigDefaults(t *testing.T) {
	cfg := &Config{}
	if cfg.GZipEnabled() {
		t.Error("GZipEnabled should be false when compression is nil")
	}
	if cfg.WSCompressionEnabled() {
		t.Error("WSCompressionEnabled should be false when compression is nil")
	}
}

func TestCompressionConfigExplicitFalse(t *testing.T) {
	cfg := &Config{Compression: &CompressionConfig{GZip: false, Websocket: false}}
	if cfg.GZipEnabled() {
		t.Error("GZipEnabled should be false")
	}
	if cfg.WSCompressionEnabled() {
		t.Error("WSCompressionEnabled should be false")
	}
}

func TestCompressionConfigEnabled(t *testing.T) {
	cfg := &Config{Compression: &CompressionConfig{GZip: true, Websocket: true}}
	if !cfg.GZipEnabled() {
		t.Error("GZipEnabled should be true")
	}
	if !cfg.WSCompressionEnabled() {
		t.Error("WSCompressionEnabled should be true")
	}
}

func TestGZipMiddlewareCompresses(t *testing.T) {
	body := `{"nodes":[{"id":"abc"}]}`
	handler := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(body))
	}))

	req := httptest.NewRequest("GET", "/api/nodes", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Header().Get("Content-Encoding") != "gzip" {
		t.Errorf("expected Content-Encoding: gzip, got %q", rr.Header().Get("Content-Encoding"))
	}
	if rr.Header().Get("Vary") != "Accept-Encoding" {
		t.Errorf("expected Vary: Accept-Encoding, got %q", rr.Header().Get("Vary"))
	}
	gz, err := gzip.NewReader(rr.Body)
	if err != nil {
		t.Fatalf("response is not valid gzip: %v", err)
	}
	defer gz.Close()
	decoded, err := io.ReadAll(gz)
	if err != nil {
		t.Fatalf("reading gzip: %v", err)
	}
	if string(decoded) != body {
		t.Errorf("decompressed body = %q, want %q", string(decoded), body)
	}
}

func TestGZipMiddlewareSkipsNoAcceptEncoding(t *testing.T) {
	handler := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello"))
	}))

	req := httptest.NewRequest("GET", "/api/nodes", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Header().Get("Content-Encoding") != "" {
		t.Errorf("expected no Content-Encoding, got %q", rr.Header().Get("Content-Encoding"))
	}
	if rr.Body.String() != "hello" {
		t.Errorf("expected plain body, got %q", rr.Body.String())
	}
}

func TestGZipMiddlewareSkipsWebSocket(t *testing.T) {
	called := false
	handler := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.Write([]byte("ws"))
	}))

	req := httptest.NewRequest("GET", "/ws", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Upgrade", "websocket")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if !called {
		t.Error("expected next handler to be called")
	}
	if rr.Header().Get("Content-Encoding") != "" {
		t.Errorf("WebSocket should not be gzip-encoded, got %q", rr.Header().Get("Content-Encoding"))
	}
}
