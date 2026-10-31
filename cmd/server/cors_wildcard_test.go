package main

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// A static host such as Cloudflare Pages serves each deployment and branch
// preview on its own subdomain of the project (85868e50.myproject.pages.dev,
// pr-12.myproject.pages.dev). One allowlist entry, "https://*.myproject.pages.dev",
// covers them for both CORS and /ws. The wildcard is one leftmost label and
// matches exactly one label; the project's own origin is listed separately.

func TestOriginMatches(t *testing.T) {
	for _, tc := range []struct {
		pattern, origin string
		want            bool
	}{
		{"https://beta.example", "https://beta.example", true},
		{"https://Beta.Example", "https://beta.example", true},
		{"https://beta.example", "https://beta.example:8443", false},
		{"https://*.myproject.pages.dev", "https://85868e50.myproject.pages.dev", true},
		{"https://*.myproject.pages.dev", "https://pr-12.myproject.pages.dev", true},
		{"https://*.myproject.pages.dev", "https://PR-12.MyProject.pages.dev", true},
		{"https://*.myproject.pages.dev", "https://myproject.pages.dev", false},
		{"https://*.myproject.pages.dev", "https://a.b.myproject.pages.dev", false},
		{"https://*.myproject.pages.dev", "https://evilmyproject.pages.dev", false},
		{"https://*.myproject.pages.dev", "https://x.myproject.pages.dev.evil.example", false},
		{"https://*.myproject.pages.dev", "http://x.myproject.pages.dev", false},
		{"https://*.myproject.pages.dev", "https://x.myproject.pages.dev:8443", false},
		{"https://*.myproject.pages.dev", "https://.myproject.pages.dev", false},
		{"https://*.myproject.pages.dev", "null", false},
		{"http://*.pr.preview.localhost:8788", "http://pr-1.pr.preview.localhost:8788", true},
		{"http://*.pr.preview.localhost:8788", "http://pr-1.pr.preview.localhost", false},
		// Patterns refused at config load never match either.
		{"https://*.pages.dev", "https://anyone.pages.dev", false},
		{"https://*.example.com", "https://www.example.com", false},
		{"*", "https://beta.example", false},
	} {
		if got := originMatches(tc.pattern, tc.origin); got != tc.want {
			t.Errorf("originMatches(%q, %q) = %v, want %v", tc.pattern, tc.origin, got, tc.want)
		}
	}
}

func TestSanitizeCORSOriginsDropsUnsafeWildcards(t *testing.T) {
	in := []string{
		"https://beta.example",
		"*",
		"https://myproject.pages.dev",
		"https://*.myproject.pages.dev",
		"http://*.pr.preview.localhost:8788",
		"https://*.pages.dev",              // every Pages site on the internet
		"https://*.example.com",            // fewer than three labels after "*."
		"*.myproject.pages.dev",            // no scheme
		"https://a.*.pages.dev",            // not the leftmost label
		"https://*abc.myproject.pages.dev", // part of a label
		"https://*.*.myproject.pages.dev",
		"https://*.myproject.pages.dev/path",
		"https://*.myproject.pages.dev:port",
		"ftp://*.a.b.example",
		"https://*.a..example",
	}
	want := []string{
		"https://beta.example",
		"*",
		"https://myproject.pages.dev",
		"https://*.myproject.pages.dev",
		"http://*.pr.preview.localhost:8788",
	}
	if got := sanitizeCORSOrigins(in); !reflect.DeepEqual(got, want) {
		t.Fatalf("sanitizeCORSOrigins:\n got %q\nwant %q", got, want)
	}
}

func TestLoadConfigDropsUnsafeWildcardFromEnv(t *testing.T) {
	t.Setenv("CORS_ALLOWED_ORIGINS", "https://myproject.pages.dev,https://*.myproject.pages.dev,https://*.pages.dev")
	cfg, err := LoadConfig("/nonexistent")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	want := []string{"https://myproject.pages.dev", "https://*.myproject.pages.dev"}
	if !reflect.DeepEqual(cfg.CORSAllowedOrigins, want) {
		t.Fatalf("CORSAllowedOrigins = %q, want %q", cfg.CORSAllowedOrigins, want)
	}
}

func TestCORS_WildcardEntryEchoesTheMatchingOrigin(t *testing.T) {
	handler := newTestServerWithCORS([]string{"https://*.myproject.pages.dev"}).corsMiddleware(dummyHandler)
	for _, tc := range []struct {
		origin string
		allow  bool
	}{
		{"https://85868e50.myproject.pages.dev", true},
		{"https://myproject.pages.dev", false},
		{"https://a.b.myproject.pages.dev", false},
	} {
		req := httptest.NewRequest("GET", "/api/stats", nil)
		req.Header.Set("Origin", tc.origin)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		acao := rr.Header().Get("Access-Control-Allow-Origin")
		if tc.allow {
			if acao != tc.origin || rr.Header().Get("Vary") != "Origin" {
				t.Errorf("%s: ACAO %q, Vary %q; want the origin echoed with Vary: Origin", tc.origin, acao, rr.Header().Get("Vary"))
			}
		} else if acao != "" {
			t.Errorf("%s: expected no ACAO, got %q", tc.origin, acao)
		}
	}
}

func TestCheckOriginHonorsWildcardEntry(t *testing.T) {
	srv, _ := newCheckOriginServer(t, []string{"https://myproject.pages.dev", "https://*.myproject.pages.dev"})
	defer srv.Close()
	for _, tc := range []struct {
		origin string
		want   int
	}{
		{"https://pr-12.myproject.pages.dev", http.StatusSwitchingProtocols},
		{"https://myproject.pages.dev", http.StatusSwitchingProtocols},
		{"https://a.b.myproject.pages.dev", http.StatusForbidden},
		{"https://evil.example", http.StatusForbidden},
	} {
		if status, _ := dialWS(t, srv, tc.origin); status != tc.want {
			t.Errorf("%s: status %d, want %d", tc.origin, status, tc.want)
		}
	}
}
