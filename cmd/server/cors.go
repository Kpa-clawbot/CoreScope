package main

import (
	"net/http"
	"net/url"
	"strings"
)

// originAllowed reports whether reqOrigin is permitted by the configured
// allowlist. It also returns whether the match was via a "*" wildcard entry.
// Shared by corsMiddleware and the WebSocket CheckOrigin check so both use a
// single allowlist policy. An empty allowlist means default-deny for
// cross-origin requests (callers handle the same-origin case separately).
func originAllowed(origins []string, reqOrigin string) (allowed, wildcard bool) {
	for _, o := range origins {
		if o == "*" {
			return true, true
		}
		if o == reqOrigin {
			return true, false
		}
	}
	return false, false
}

// isSameOriginWS reports whether a WebSocket upgrade request originates from the
// same host it is connecting to. A missing Origin header (non-browser client)
// or an Origin whose host matches the request Host is treated as same-origin.
func isSameOriginWS(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		// No Origin header — not a browser cross-site request. Allow.
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// corsMiddleware returns a middleware that sets CORS headers based on the
// configured allowed origins. When CORSAllowedOrigins is empty (default),
// no Access-Control-* headers are added, preserving browser same-origin policy.
func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origins := s.cfg.CORSAllowedOrigins
		if len(origins) == 0 {
			next.ServeHTTP(w, r)
			return
		}

		reqOrigin := r.Header.Get("Origin")
		if reqOrigin == "" {
			next.ServeHTTP(w, r)
			return
		}

		// Check if origin is allowed
		allowed, wildcard := originAllowed(origins, reqOrigin)

		if !allowed {
			// Origin not in allowlist — don't add CORS headers
			if r.Method == http.MethodOptions {
				// Still reject preflight with 403
				w.WriteHeader(http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
			return
		}

		// Set CORS headers
		if wildcard {
			w.Header().Set("Access-Control-Allow-Origin", "*")
		} else {
			w.Header().Set("Access-Control-Allow-Origin", reqOrigin)
			w.Header().Set("Vary", "Origin")
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		// When the allowlist is a wildcard ("*"), never advertise the
		// X-API-Key request header: doing so would let any website send
		// authenticated cross-origin requests with the operator's API key.
		// Wildcard origins only get the non-credential header set.
		if wildcard {
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		} else {
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key")
		}

		// Handle preflight
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
