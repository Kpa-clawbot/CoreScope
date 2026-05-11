package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestServerWithCORS creates a minimal Server with the given CORS config.
func newTestServerWithCORS(origins []string) *Server {
	cfg := &Config{CORSAllowedOrigins: origins}
	srv := &Server{cfg: cfg}
	return srv
}

// dummyHandler is a simple handler that writes 200 OK.
var dummyHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
})

func TestCORS_DefaultNoHeaders(t *testing.T) {
	srv := newTestServerWithCORS(nil)
	handler := srv.corsMiddleware(dummyHandler)

	req := httptest.NewRequest("GET", "/api/health", nil)
	req.Header.Set("Origin", "https://evil.example")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if v := rr.Header().Get("Access-Control-Allow-Origin"); v != "" {
		t.Fatalf("expected no ACAO header, got %q", v)
	}
}

func TestCORS_AllowlistMatch(t *testing.T) {
	srv := newTestServerWithCORS([]string{"https://good.example"})
	handler := srv.corsMiddleware(dummyHandler)

	req := httptest.NewRequest("GET", "/api/health", nil)
	req.Header.Set("Origin", "https://good.example")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if v := rr.Header().Get("Access-Control-Allow-Origin"); v != "https://good.example" {
		t.Fatalf("expected origin echo, got %q", v)
	}
	if v := rr.Header().Get("Access-Control-Allow-Methods"); v != "GET, POST, OPTIONS" {
		t.Fatalf("expected methods header, got %q", v)
	}
	if v := rr.Header().Get("Access-Control-Allow-Headers"); v != "Content-Type, X-API-Key" {
		t.Fatalf("expected headers header, got %q", v)
	}
	if v := rr.Header().Get("Vary"); v != "Origin" {
		t.Fatalf("expected Vary: Origin, got %q", v)
	}
}

func TestCORS_AllowlistNoMatch(t *testing.T) {
	srv := newTestServerWithCORS([]string{"https://good.example"})
	handler := srv.corsMiddleware(dummyHandler)

	req := httptest.NewRequest("GET", "/api/health", nil)
	req.Header.Set("Origin", "https://evil.example")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if v := rr.Header().Get("Access-Control-Allow-Origin"); v != "" {
		t.Fatalf("expected no ACAO header for non-matching origin, got %q", v)
	}
}

func TestCORS_PreflightAllowed(t *testing.T) {
	srv := newTestServerWithCORS([]string{"https://good.example"})
	handler := srv.corsMiddleware(dummyHandler)

	req := httptest.NewRequest("OPTIONS", "/api/health", nil)
	req.Header.Set("Origin", "https://good.example")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
	if v := rr.Header().Get("Access-Control-Allow-Origin"); v != "https://good.example" {
		t.Fatalf("expected origin echo, got %q", v)
	}
}

func TestCORS_PreflightRejected(t *testing.T) {
	srv := newTestServerWithCORS([]string{"https://good.example"})
	handler := srv.corsMiddleware(dummyHandler)

	req := httptest.NewRequest("OPTIONS", "/api/health", nil)
	req.Header.Set("Origin", "https://evil.example")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rr.Code)
	}
}

func TestCORS_Wildcard(t *testing.T) {
	srv := newTestServerWithCORS([]string{"*"})
	handler := srv.corsMiddleware(dummyHandler)

	req := httptest.NewRequest("GET", "/api/health", nil)
	req.Header.Set("Origin", "https://anything.example")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if v := rr.Header().Get("Access-Control-Allow-Origin"); v != "*" {
		t.Fatalf("expected *, got %q", v)
	}
	// Wildcard should NOT set Vary: Origin
	if v := rr.Header().Get("Vary"); v == "Origin" {
		t.Fatalf("wildcard should not set Vary: Origin")
	}
}

func TestCORS_NoOriginHeader(t *testing.T) {
	srv := newTestServerWithCORS([]string{"https://good.example"})
	handler := srv.corsMiddleware(dummyHandler)

	req := httptest.NewRequest("GET", "/api/health", nil)
	// No Origin header
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if v := rr.Header().Get("Access-Control-Allow-Origin"); v != "" {
		t.Fatalf("expected no ACAO without Origin header, got %q", v)
	}
}
