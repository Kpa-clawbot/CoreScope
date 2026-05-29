package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// Issue #1369: CORS_ALLOWED_ORIGINS env override + embed support.
//
// Red commit: these tests fail until LoadConfig honors the env var and the
// CORS middleware advertises GET/HEAD/OPTIONS (the embed contract is
// read-only cross-origin access).

// TestCORS_EnvOverridesConfig — env var CORS_ALLOWED_ORIGINS replaces config.
func TestCORS_EnvOverridesConfig_1369(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://blog.example.com,https://embed.example.com")
	cfg, err := LoadConfig("/nonexistent")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if len(cfg.CORSAllowedOrigins) != 2 {
		t.Fatalf("expected 2 origins from env, got %v", cfg.CORSAllowedOrigins)
	}
	if cfg.CORSAllowedOrigins[0] != "https://blog.example.com" ||
		cfg.CORSAllowedOrigins[1] != "https://embed.example.com" {
		t.Fatalf("env parse wrong: %v", cfg.CORSAllowedOrigins)
	}
}

// TestCORS_EnvEmptyKeepsConfig — empty env var does not clobber file config.
func TestCORS_EnvEmptyKeepsConfig_1369(t *testing.T) {
	os.Unsetenv("CORS_ALLOWED_ORIGINS")
	cfg := &Config{CORSAllowedOrigins: []string{"https://example.com"}}
	applyCORSEnv(cfg)
	if len(cfg.CORSAllowedOrigins) != 1 || cfg.CORSAllowedOrigins[0] != "https://example.com" {
		t.Fatalf("unset env should not clobber config; got %v", cfg.CORSAllowedOrigins)
	}
}

// TestCORS_EnvTrimsWhitespace — comma-separated env tokens are trimmed.
func TestCORS_EnvTrimsWhitespace_1369(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "  https://a.example  , https://b.example ")
	cfg := &Config{}
	applyCORSEnv(cfg)
	if len(cfg.CORSAllowedOrigins) != 2 {
		t.Fatalf("expected 2, got %v", cfg.CORSAllowedOrigins)
	}
	if cfg.CORSAllowedOrigins[0] != "https://a.example" || cfg.CORSAllowedOrigins[1] != "https://b.example" {
		t.Fatalf("not trimmed: %v", cfg.CORSAllowedOrigins)
	}
}

// TestCORS_EmbedContractGETHEAD — embed contract is read-only; the
// Access-Control-Allow-Methods header must advertise GET, HEAD, OPTIONS only
// (no POST/PUT/DELETE) so iframes/server-side fetchers know writes are not
// CORS-permitted. DJB hardening: minimum surface.
func TestCORS_EmbedContractGETHEAD_1369(t *testing.T) {
	srv := newTestServerWithCORS([]string{"https://embed.example.com"})
	handler := srv.corsMiddleware(dummyHandler)

	req := httptest.NewRequest("GET", "/api/health", nil)
	req.Header.Set("Origin", "https://embed.example.com")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	methods := rr.Header().Get("Access-Control-Allow-Methods")
	if methods != "GET, HEAD, OPTIONS" {
		t.Fatalf("expected read-only methods 'GET, HEAD, OPTIONS', got %q", methods)
	}
}

// TestCORS_PreflightPOSTRejected — preflight asking for POST from an allowed
// origin must NOT echo POST in Allow-Methods. The middleware advertises only
// the read-only set; preflight succeeds (browser then blocks the POST).
func TestCORS_PreflightPOSTRejected_1369(t *testing.T) {
	srv := newTestServerWithCORS([]string{"https://embed.example.com"})
	handler := srv.corsMiddleware(dummyHandler)

	req := httptest.NewRequest("OPTIONS", "/api/anything", nil)
	req.Header.Set("Origin", "https://embed.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("preflight expected 204, got %d", rr.Code)
	}
	if got := rr.Header().Get("Access-Control-Allow-Methods"); got != "GET, HEAD, OPTIONS" {
		t.Fatalf("preflight must advertise read-only methods only, got %q", got)
	}
}
