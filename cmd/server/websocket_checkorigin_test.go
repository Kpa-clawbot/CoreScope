package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

// dialWS attempts a WebSocket upgrade against srv with the given Origin
// header. It returns the HTTP status code received (101 on success, 403 on
// rejection) and any dial error. Connection is closed if it succeeds.
func dialWS(t *testing.T, srv *httptest.Server, origin string) (int, error) {
	t.Helper()
	wsURL := "ws" + srv.URL[4:]
	headers := http.Header{}
	if origin != "" {
		headers.Set("Origin", origin)
	}
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if conn != nil {
		defer conn.Close()
	}
	status := 0
	if resp != nil {
		status = resp.StatusCode
	}
	return status, err
}

func newCheckOriginServer(t *testing.T, allowed []string) (*httptest.Server, *Hub) {
	t.Helper()
	hub := NewHub()
	hub.SetAllowedOrigins(allowed)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hub.ServeWS(w, r)
	}))
	return srv, hub
}

// TestCheckOriginRejectsForeignOrigin: the RED test that proves a
// cross-origin browser cannot upgrade /ws when not allowlisted.
// (#1793)
func TestCheckOriginRejectsForeignOrigin(t *testing.T) {
	srv, _ := newCheckOriginServer(t, nil)
	defer srv.Close()

	status, err := dialWS(t, srv, "https://evil.example.com")
	if err == nil {
		t.Fatalf("expected upgrade rejection for foreign origin, got success (status=%d)", status)
	}
	if status != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for foreign origin, got status=%d err=%v", status, err)
	}
}

func TestCheckOriginAllowsEmptyOrigin(t *testing.T) {
	srv, _ := newCheckOriginServer(t, nil)
	defer srv.Close()

	status, err := dialWS(t, srv, "")
	if err != nil {
		t.Fatalf("expected upgrade success for empty Origin (non-browser client), got status=%d err=%v", status, err)
	}
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("expected 101 Switching Protocols, got %d", status)
	}
}

func TestCheckOriginAllowsSameHost(t *testing.T) {
	srv, _ := newCheckOriginServer(t, nil)
	defer srv.Close()

	// httptest.NewServer uses srv.URL host, e.g. 127.0.0.1:PORT.
	hostURL := "http" + srv.URL[4:] // strip "ws" was N/A — srv.URL is http://
	if !strings.HasPrefix(srv.URL, "http://") {
		t.Fatalf("unexpected srv URL: %s", srv.URL)
	}
	sameOrigin := hostURL[:len(hostURL)] // same scheme+host as the dial target
	status, err := dialWS(t, srv, sameOrigin)
	if err != nil {
		t.Fatalf("expected upgrade success for same-host origin %s, got status=%d err=%v", sameOrigin, status, err)
	}
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("expected 101 Switching Protocols, got %d", status)
	}
}

func TestCheckOriginAllowsAllowlistedOrigin(t *testing.T) {
	allow := []string{"https://embed.example.com"}
	srv, _ := newCheckOriginServer(t, allow)
	defer srv.Close()

	status, err := dialWS(t, srv, "https://embed.example.com")
	if err != nil {
		t.Fatalf("expected upgrade success for allowlisted origin, got status=%d err=%v", status, err)
	}
	if status != http.StatusSwitchingProtocols {
		t.Fatalf("expected 101 Switching Protocols, got %d", status)
	}
}

// TestCheckOriginWildcardDoesNotAllowForeignOrigin asserts the deliberate
// divergence from CORS: "*" in the allowlist does NOT permit cross-origin
// WebSocket upgrades. OWASP WebSocket Security Cheat Sheet — avoid wildcards
// in CSWSH defense. See PR for citation.
func TestCheckOriginWildcardDoesNotAllowForeignOrigin(t *testing.T) {
	srv, _ := newCheckOriginServer(t, []string{"*"})
	defer srv.Close()

	status, err := dialWS(t, srv, "https://evil.example.com")
	if err == nil {
		t.Fatalf("expected upgrade rejection for foreign origin even when allowlist=[\"*\"], got success (status=%d)", status)
	}
	if status != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden, got status=%d err=%v", status, err)
	}
}
