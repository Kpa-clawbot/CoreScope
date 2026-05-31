package main

import (
	"net/http"
	"os"
	"strings"
)

// applyCORSEnv overlays cfg.CORSAllowedOrigins from the CORS_ALLOWED_ORIGINS
// env var when it is set and non-empty. Tokens are comma-separated, trimmed,
// and empties dropped. The env var is the ops-friendly override; it lets
// operators add cross-domain embed origins without editing config.json
// (issue #1369). An unset or empty env var leaves cfg untouched, so
// per-deployment config.json values still apply.
func applyCORSEnv(cfg *Config) {
	raw, ok := os.LookupEnv("CORS_ALLOWED_ORIGINS")
	if !ok {
		return
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		s := strings.TrimSpace(p)
		if s != "" {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		// Env var present but only whitespace — treat as unset, do not clobber.
		return
	}
	cfg.CORSAllowedOrigins = out
}

// corsMiddleware returns a middleware that sets CORS headers based on the
// configured allowed origins. When CORSAllowedOrigins is empty (default),
// no Access-Control-* headers are added, preserving browser same-origin policy.
//
// Embed contract (issue #1369): the cross-domain surface is read-only. The
// middleware advertises only GET, HEAD, and OPTIONS in Access-Control-Allow-
// Methods so iframes / server-side fetchers cannot opt into POST/PUT/DELETE
// via CORS. Same-origin writes (admin UI, API-key holders on the canonical
// origin) are unaffected — they never go through the preflight path.
// Credentialed CORS is intentionally NOT enabled.
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
		allowed := false
		wildcard := false
		for _, o := range origins {
			if o == "*" {
				allowed = true
				wildcard = true
				break
			}
			if o == reqOrigin {
				allowed = true
				break
			}
		}

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
		// Read-only embed contract — see comment above.
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key")

		// Handle preflight
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
