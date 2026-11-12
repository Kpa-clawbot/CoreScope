package main

// Per-client token bucket for public endpoints whose requests cost terrain
// tile downloads or heavy compute (/api/elevation/profile, /api/planner/*).
// Clients are told apart by wsLimiter.clientIPRightmost: X-Forwarded-For is
// believed only from webSocket.trustedProxies, and is read from the right, so
// the client is the last hop no trusted proxy vouches for and a forged
// leftmost entry buys nothing (these limits protect an upstream quota).
// Operators behind a CDN must list the CDN's ranges in
// webSocket.trustedProxies, or every client behind it looks like the CDN.
// When clients cannot be told apart, every request shares one bucket and a
// one-time warning tells the operator what to configure.

import (
	"log"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// clientLimiter charges one token per call to the caller's bucket. A nil ipr
// or rl disables the limit (tests).
type clientLimiter struct {
	ipr  *wsLimiter
	rl   *rateLimiter
	warn sync.Once
	// tag is the log prefix ("los"); what names the limit in the warning
	// ("terrain tile").
	tag, what string
}

// clientSharedKey is the bucket every request shares when clients cannot be
// told apart.
const clientSharedKey = "shared"

// clientKey is r's bucket: the client IP when it identifies a client, else
// clientSharedKey (logging the one-time warning).
func (c *clientLimiter) clientKey(r *http.Request) string {
	if c.ipr == nil {
		return clientSharedKey
	}
	ip, distinct := c.ipr.clientIPRightmost(r)
	if distinct && ip != nil {
		return ip.String()
	}
	c.warn.Do(func() {
		log.Printf("[%s] WARNING: clients cannot be told apart (requests arrive from %v), "+
			"so the %s rate limit is shared by everyone. "+
			"Set webSocket.trustedProxies to your reverse proxy's address for per-client limits.", c.tag, ip, c.what)
	})
	return clientSharedKey
}

// take consumes one token from r's bucket. When none is left it reports how
// long until the next one. It writes nothing, so callers may hold their own
// locks around it.
func (c *clientLimiter) take(r *http.Request) (bool, time.Duration) {
	return c.takeN(r, 1)
}

// takeN consumes n tokens from r's bucket, all or nothing; on refusal it
// reports how long until n are available and takes none.
func (c *clientLimiter) takeN(r *http.Request, n int) (bool, time.Duration) {
	if c.ipr == nil || c.rl == nil {
		return true, 0
	}
	return c.rl.takeN(c.clientKey(r), n)
}

// allow is take that answers 429 "rate limited" + Retry-After itself.
func (c *clientLimiter) allow(w http.ResponseWriter, r *http.Request) bool {
	return c.allowN(w, r, 1)
}

// allowN is takeN that answers 429 "rate limited" + Retry-After itself.
func (c *clientLimiter) allowN(w http.ResponseWriter, r *http.Request, n int) bool {
	ok, wait := c.takeN(r, n)
	if !ok {
		writeRateLimited(w, wait)
	}
	return ok
}

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
