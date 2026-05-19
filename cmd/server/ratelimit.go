package main

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Per-IP token-bucket rate limiting for the HTTP API. Hand-rolled — no external
// dependency. The goal is DoS protection; the default limits are generous
// enough that normal dashboard usage never trips them.
//
// Two limiters are applied:
//   - a general bucket on every /api/ route
//   - a stricter bucket on the expensive endpoints (/api/analytics/*,
//     /api/decode, /api/paths/inspect) which do heavy compute or DB work.
//
// Defaults (overridable via config.RateLimit):
//   general:   50 req/s sustained, burst 100
//   expensive:  5 req/s sustained, burst 15

const (
	defaultGeneralRPS     = 50.0
	defaultGeneralBurst   = 100
	defaultExpensiveRPS   = 5.0
	defaultExpensiveBurst = 15

	// idleBucketTTL bounds memory: an IP's bucket is dropped after this long
	// with no requests. Bounds the per-IP map under a churning client set.
	idleBucketTTL = 10 * time.Minute
)

// tokenBucket is a classic token bucket: tokens refill at `rate` per second up
// to `burst`, and each request consumes one token.
type tokenBucket struct {
	tokens   float64
	lastSeen time.Time
}

// ipRateLimiter holds one tokenBucket per client IP for a single rate class.
type ipRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	rate    float64 // tokens added per second
	burst   float64 // maximum tokens
}

func newIPRateLimiter(rate float64, burst int) *ipRateLimiter {
	return &ipRateLimiter{
		buckets: make(map[string]*tokenBucket),
		rate:    rate,
		burst:   float64(burst),
	}
}

// allow reports whether a request from `ip` may proceed, consuming a token if
// so. It also lazily evicts buckets idle longer than idleBucketTTL.
func (l *ipRateLimiter) allow(ip string) bool {
	now := time.Now()

	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[ip]
	if !ok {
		// New IP: start with a full bucket, consume one token.
		l.buckets[ip] = &tokenBucket{tokens: l.burst - 1, lastSeen: now}
		l.evictIdleLocked(now)
		return true
	}

	// Refill based on elapsed time, capped at burst.
	elapsed := now.Sub(b.lastSeen).Seconds()
	b.tokens += elapsed * l.rate
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.lastSeen = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// evictIdleLocked drops buckets that have been idle past the TTL. Called with
// l.mu held. O(n) but only runs when a brand-new IP is seen, so amortized cost
// is low and the map cannot grow without bound.
func (l *ipRateLimiter) evictIdleLocked(now time.Time) {
	for ip, b := range l.buckets {
		if now.Sub(b.lastSeen) > idleBucketTTL {
			delete(l.buckets, ip)
		}
	}
}

// rateLimiters bundles the general and expensive-endpoint limiters.
type rateLimiters struct {
	general   *ipRateLimiter
	expensive *ipRateLimiter
}

// newRateLimiters builds the limiter pair from config, applying defaults for
// any zero/omitted field.
func newRateLimiters(cfg *RateLimitConfig) *rateLimiters {
	genRPS, genBurst := defaultGeneralRPS, defaultGeneralBurst
	expRPS, expBurst := defaultExpensiveRPS, defaultExpensiveBurst
	if cfg != nil {
		if cfg.GeneralRPS > 0 {
			genRPS = cfg.GeneralRPS
		}
		if cfg.GeneralBurst > 0 {
			genBurst = cfg.GeneralBurst
		}
		if cfg.ExpensiveRPS > 0 {
			expRPS = cfg.ExpensiveRPS
		}
		if cfg.ExpensiveBurst > 0 {
			expBurst = cfg.ExpensiveBurst
		}
	}
	return &rateLimiters{
		general:   newIPRateLimiter(genRPS, genBurst),
		expensive: newIPRateLimiter(expRPS, expBurst),
	}
}

// clientIP extracts the best-effort client IP for rate-limiting purposes. It
// trusts the first X-Forwarded-For entry when present (the analyzer typically
// runs behind a reverse proxy), falling back to the connection RemoteAddr.
// NOTE: X-Forwarded-For is trusted unconditionally; the deployment must be
// behind a trusted reverse proxy that strips or overwrites this header before
// forwarding, otherwise clients can spoof their IP and bypass rate limiting.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// First entry is the original client.
		if i := strings.IndexByte(xff, ','); i >= 0 {
			xff = xff[:i]
		}
		if ip := strings.TrimSpace(xff); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// isExpensivePath reports whether the request targets an endpoint subject to
// the stricter expensive-bucket limit.
func isExpensivePath(path string) bool {
	return strings.HasPrefix(path, "/api/analytics/") ||
		path == "/api/decode" ||
		path == "/api/paths/inspect"
}

// rateLimitMiddleware enforces per-IP rate limits on /api/ routes. Non-/api/
// requests (static files, WebSocket upgrades) are passed through untouched.
func (s *Server) rateLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.limiters == nil || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		ip := clientIP(r)

		// Expensive endpoints must satisfy BOTH buckets so the strict limit
		// is real even within the general budget.
		if isExpensivePath(r.URL.Path) {
			if !s.limiters.expensive.allow(ip) {
				writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
		}
		if !s.limiters.general.allow(ip) {
			writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}
