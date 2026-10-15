package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
)

func TestCORSBearerRoutes(t *testing.T) {
	f := newAuthFixture(t)
	f.srv.cfg.CORSAllowedOrigins = []string{rxOrigin}
	r := mux.NewRouter()
	r.Use(f.srv.corsMiddleware)
	f.srv.registerAuthRoutes(r)
	f.router = r

	preflight := func(path, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("OPTIONS", path, nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", "authorization, content-type")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	for _, p := range []string{"/api/auth/device-token", "/api/auth/me", "/api/auth/logout", "/api/account/settings",
		"/api/account/companions", "/api/account/companions/challenge", "/api/account/companions/" + strings.Repeat("ab", 32)} {
		w := preflight(p, rxOrigin)
		h := w.Header()
		if w.Code != http.StatusNoContent || h.Get("Access-Control-Allow-Origin") != rxOrigin ||
			h.Get("Access-Control-Allow-Methods") != "GET, HEAD, POST, PUT, DELETE, OPTIONS" ||
			h.Get("Access-Control-Allow-Headers") != "Authorization, Content-Type" ||
			h.Get("Access-Control-Allow-Credentials") != "" {
			t.Errorf("preflight %s = %d %v", p, w.Code, h)
		}
	}
	if w := preflight("/api/account/companions", "https://evil.example"); w.Code != http.StatusForbidden || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("foreign origin preflight = %d %v", w.Code, w.Header())
	}
	if m := preflight("/api/account/sessions", rxOrigin).Header().Get("Access-Control-Allow-Methods"); strings.Contains(m, "POST") {
		t.Errorf("non-bearer route allows writes cross-origin: %q", m)
	}

	// The actual cross-origin request carries the origin echo, never credentials.
	f.registerAndActivate(t, "alice@example.org", "Alice", pw)
	w := f.do("POST", "/api/auth/device-token", deviceTokenRequest{Email: "alice@example.org", Password: pw, DeviceName: "x"},
		header("Origin", rxOrigin))
	expectStatus(t, w, http.StatusOK)
	if w.Header().Get("Access-Control-Allow-Origin") != rxOrigin || w.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("device-token CORS headers = %v", w.Header())
	}
}
