package main

import (
	"log"
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

// wildcardOrigin splits a wildcard allowlist entry, "scheme://*.host[:port]",
// into its scheme and the host[:port] the wildcard label sits on. One entry
// such as "https://*.myproject.pages.dev" then covers every deployment and
// branch preview of a static host project (85868e50.myproject.pages.dev).
// ok is false for anything else, including a wildcard over fewer than three
// labels: "https://*.pages.dev" would admit every Pages site on the internet.
// The label count is a sanity floor, not a Public Suffix List check. It keeps
// two-label shared suffixes (pages.dev, github.io, co.uk) out, but
// "https://*.s3.amazonaws.com" passes and would admit every S3 website
// endpoint, so a wildcard belongs only over a host the operator controls.
func wildcardOrigin(entry string) (scheme, rest string, ok bool) {
	scheme, rest, ok = strings.Cut(entry, "://*.")
	if !ok {
		return "", "", false
	}
	if s := strings.ToLower(scheme); s != "http" && s != "https" {
		return "", "", false
	}
	host, port, hasPort := strings.Cut(rest, ":")
	if hasPort && (port == "" || strings.Trim(port, "0123456789") != "") {
		return "", "", false
	}
	if strings.Count(host, ".") < 2 || strings.Contains(host, "..") ||
		strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") ||
		strings.ContainsAny(host, "*/?#@ ") {
		return "", "", false
	}
	return scheme, rest, true
}

// originMatches reports whether a browser Origin matches one allowlist entry:
// the same origin ignoring case, or a wildcard entry (see wildcardOrigin)
// with exactly one more label in front. The wildcard does not match the host
// it sits on, so "https://myproject.pages.dev" is listed on its own. "*"
// matches nothing here: the CORS middleware handles it and /ws refuses it.
func originMatches(entry, origin string) bool {
	if strings.EqualFold(entry, origin) {
		return true
	}
	scheme, rest, ok := wildcardOrigin(entry)
	if !ok {
		return false
	}
	oScheme, oRest, ok := strings.Cut(origin, "://")
	if !ok || !strings.EqualFold(scheme, oScheme) {
		return false
	}
	label, tail, ok := strings.Cut(oRest, ".")
	return ok && label != "" && !strings.ContainsAny(label, ":/@") && strings.EqualFold(tail, rest)
}

// sanitizeCORSOrigins drops each entry that has a wildcard but is neither "*"
// nor a valid wildcard label (see wildcardOrigin), and logs it, so a typo
// fails closed instead of matching more than the operator meant.
func sanitizeCORSOrigins(origins []string) []string {
	var out []string
	for _, o := range origins {
		if o != "*" && strings.Contains(o, "*") {
			if _, _, ok := wildcardOrigin(o); !ok {
				log.Printf(`[config] corsAllowedOrigins: ignoring %q: a wildcard must be the whole leftmost label of "scheme://*.host", with at least three labels after "*.", e.g. "https://*.myproject.pages.dev"`, o)
				continue
			}
		}
		out = append(out, o)
	}
	return out
}

// corsExposedHeaders are the response headers the UI reads besides the
// CORS-safelisted ones: Retry-After (api() on a warm-up 503, node-reach.js on
// a 429) and X-CoreScope-Load-Status (warmup-banner.js). A UI served from an
// allowed origin reads both as null unless they are listed here.
const corsExposedHeaders = "Retry-After, X-CoreScope-Load-Status"

// corsMiddleware returns a middleware that sets CORS headers based on the
// configured allowed origins. When CORSAllowedOrigins is empty (default),
// no Access-Control-* headers are added, preserving browser same-origin policy.
// An entry is an exact origin, a wildcard label (originMatches) or "*".
//
// Embed contract (issue #1369): no cross-origin writes. The middleware
// advertises only GET, HEAD, and OPTIONS in Access-Control-Allow-Methods so
// iframes cannot opt into POST/PUT/DELETE via CORS. That header only governs
// requests with a preflight: a CORS simple request (a POST with a text/plain
// body) has none, which is how a listed origin calls the three anonymous POSTs
// that compute an answer and write nothing (/api/decode,
// /api/packets/observations, /api/paths/inspect; see simple_post_test.go).
// Same-origin writes (admin UI, API-key holders on the canonical origin) are
// unaffected — they never go through the preflight path.
// The exception is the bearer routes of companion linking (bearerCORSPath,
// only with user management on), which also allow POST, PUT, DELETE and the
// Authorization header, for every listed origin: each deployment under a
// wildcard entry included.
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
			if originMatches(o, reqOrigin) {
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
		w.Header().Set("Access-Control-Expose-Headers", corsExposedHeaders)
		if s.auth != nil && bearerCORSPath(r.URL.Path) {
			// Companion linking: CoreDrive RX on an allowlisted origin logs in
			// and writes with a device token in Authorization. Still no
			// credentials: nothing rides on cookies.
			w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		} else {
			// Embed contract — see comment above.
			w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
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
