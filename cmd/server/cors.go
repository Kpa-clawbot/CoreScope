package main

import "net/http"

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
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, X-API-Key")

		// Handle preflight
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
