package main

// Per-client token bucket for public endpoints whose requests cost terrain
// tile downloads or heavy compute (/api/elevation/profile, /api/planner/*).
// Clients are told apart by wsLimiter.clientIPRightmost: X-Forwarded-For is
// believed only from webSocket.trustedProxies, and is read from the right, so
// the client is the last hop no trusted proxy vouches for and a forged
// leftmost entry buys nothing (these limits protect an upstream quota).
// Operators behind a CDN must list the CDN's ranges in
// webSocket.trustedProxies, or every client behind it looks like the CDN.
// When clients cannot be told apart (a local reverse proxy that is not in
// webSocket.trustedProxies, the shipped default), the per-client limit is
// skipped, as /ws and the login limiter skip theirs: one bucket for the whole
// site would refuse real visitors. The global budgets still apply (terrain
// downloads: srtmBudgetFor; planner jobs: plannerGlobalJobsPerMin), and a
// one-time warning tells the operator what to configure.

import (
	"log"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// clientLimiter charges tokens to the caller's bucket. A nil limiter, ipr or
// rl disables the limit (tests).
type clientLimiter struct {
	ipr  *wsLimiter
	rl   *rateLimiter
	warn sync.Once
	// tag is the log prefix ("los"); what names the limit in the warning
	// ("terrain tile").
	tag, what string
}

// clientSharedKey names, in logs, the clients that cannot be told apart. It
// is never a bucket: those clients are not charged.
const clientSharedKey = "shared"

// clientKey is r's bucket, the client IP, and whether it identifies a
// client. When it does not, the key is clientSharedKey, nothing is charged
// to it and the one-time warning is logged.
func (c *clientLimiter) clientKey(r *http.Request) (key string, distinct bool) {
	if c.ipr == nil {
		return clientSharedKey, false
	}
	ip, distinct := c.ipr.clientIPRightmost(r)
	if distinct && ip != nil {
		return ip.String(), true
	}
	c.warn.Do(func() {
		log.Printf("[%s] WARNING: clients cannot be told apart (requests arrive from %v), "+
			"so the per-client %s rate limit is off and only the global budget applies. "+
			"Set webSocket.trustedProxies to your reverse proxy's address for per-client limits.", c.tag, ip, c.what)
	})
	return clientSharedKey, false
}

// take consumes one token from r's bucket. When none is left it reports how
// long until the next one. It writes nothing and takes no lock but the
// bucket map's, so callers may hold their own locks around it.
func (c *clientLimiter) take(r *http.Request) (bool, time.Duration) {
	return c.takeN(r, 1)
}

// takeN consumes n tokens from r's bucket, all or nothing; on refusal it
// reports how long until n are available and takes none. n < 1 always
// succeeds, and leaves no bucket behind, as does a client that cannot be
// told apart from the others (clientKey).
func (c *clientLimiter) takeN(r *http.Request, n int) (bool, time.Duration) {
	if c.off() || n < 1 {
		return true, 0
	}
	key, distinct := c.clientKey(r)
	if !distinct {
		return true, 0
	}
	return c.rl.takeN(key, n)
}

// refundN gives back n tokens takeN just took from r's bucket.
func (c *clientLimiter) refundN(r *http.Request, n int) {
	if c.off() || n < 1 {
		return
	}
	if key, distinct := c.clientKey(r); distinct {
		c.rl.refundN(key, n)
	}
}

func (c *clientLimiter) off() bool { return c == nil || c.ipr == nil || c.rl == nil }

// retryAfterSecs rounds wait up to whole seconds, at least 1.
func retryAfterSecs(wait time.Duration) int {
	secs := int(math.Ceil(wait.Seconds()))
	if secs < 1 {
		secs = 1
	}
	return secs
}

// writeRateLimited answers 429 "rate limited" with a Retry-After of at least
// 1 s.
func writeRateLimited(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(retryAfterSecs(wait)))
	writeError(w, http.StatusTooManyRequests, "rate limited")
}
