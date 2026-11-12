package main

import (
	"log"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// rateLimiter is a keyed token bucket: n requests per period, refilled
// continuously. In-memory, so limits reset on restart. That is acceptable
// for brute-force damping, not a quota system.
//
// A capped limiter (newCappedRateLimiter) gives each key a second, slower
// bucket, charged together with the first, all or nothing: a burst that
// refills quickly under a ceiling over a longer window.
type rateLimiter struct {
	mu       sync.Mutex
	rate     float64 // tokens per second
	burst    float64
	capRate  float64 // the cap bucket's tokens per second; 0 without one
	capBurst float64
	buckets  map[string]*rlBucket
	now      func() time.Time

	maxBuckets int // cap on len(buckets); a field so tests can lower it
	warnFull   sync.Once
}

const rateLimiterMaxBuckets = 100000

type rlBucket struct {
	tokens    float64
	capTokens float64 // the cap bucket's; unused without one
	last      time.Time
}

func newRateLimiter(n int, per time.Duration) *rateLimiter {
	return &rateLimiter{rate: float64(n) / per.Seconds(), burst: float64(n),
		buckets: map[string]*rlBucket{}, now: time.Now, maxBuckets: rateLimiterMaxBuckets}
}

// newCappedRateLimiter holds burst tokens per key, refilled at n per per,
// under a cap of capN tokens refilled at capN per capPer: a key never takes
// more than capN plus what capPer refills in any capPer.
func newCappedRateLimiter(burst, n int, per time.Duration, capN int, capPer time.Duration) *rateLimiter {
	l := newRateLimiter(n, per)
	l.burst = float64(burst)
	l.capRate, l.capBurst = float64(capN)/capPer.Seconds(), float64(capN)
	return l
}

// take consumes one token for key. When none is left it reports how long
// until the next one.
func (l *rateLimiter) take(key string) (bool, time.Duration) {
	return l.takeN(key, 1)
}

// takeN consumes n tokens for key, all or nothing: when fewer than n are left
// (in either bucket of a capped limiter) it takes none and reports how long
// until n are available (never, in effect, when n exceeds the burst or the
// cap). n < 1 takes nothing.
func (l *rateLimiter) takeN(key string, n int) (bool, time.Duration) {
	need := float64(max(n, 0))
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.buckets[key]
	if b == nil {
		if len(l.buckets) >= l.maxBuckets {
			l.sweepLocked(now)
			if len(l.buckets) >= l.maxBuckets {
				l.warnFull.Do(func() {
					log.Printf("[users] rate limiter full (%d keys), refusing new keys until buckets refill", l.maxBuckets)
				})
				return false, time.Duration(1 / l.rate * float64(time.Second))
			}
		}
		b = &rlBucket{tokens: l.burst, capTokens: l.capBurst, last: now}
		l.buckets[key] = b
	}
	dt := now.Sub(b.last).Seconds()
	b.tokens = math.Min(l.burst, b.tokens+dt*l.rate)
	b.capTokens = math.Min(l.capBurst, b.capTokens+dt*l.capRate)
	b.last = now
	capped := l.capRate > 0
	if b.tokens >= need && (!capped || b.capTokens >= need) {
		b.tokens -= need
		if capped {
			b.capTokens -= need
		}
		return true, 0
	}
	wait := math.Max(0, (need-b.tokens)/l.rate)
	if capped {
		wait = math.Max(wait, (need-b.capTokens)/l.capRate)
	}
	return false, time.Duration(wait * float64(time.Second))
}

// refund gives back one token taken from key (a charge for work that was then
// refused elsewhere), never above the burst. A key with no bucket is a no-op.
func (l *rateLimiter) refund(key string) { l.refundN(key, 1) }

// refundN gives back n tokens taken from key, to both buckets of a capped
// limiter, never above either's size. A key with no bucket is a no-op.
func (l *rateLimiter) refundN(key string, n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if b := l.buckets[key]; b != nil && n > 0 {
		b.tokens = math.Min(l.burst, b.tokens+float64(n))
		b.capTokens = math.Min(l.capBurst, b.capTokens+float64(n))
	}
}

// gc drops buckets that have refilled completely (they carry no state).
func (l *rateLimiter) gc() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(l.now())
}

// sweepLocked deletes fully refilled buckets (both, when capped). The
// caller holds l.mu.
func (l *rateLimiter) sweepLocked(now time.Time) {
	for k, b := range l.buckets {
		dt := now.Sub(b.last).Seconds()
		if b.tokens+dt*l.rate >= l.burst && b.capTokens+dt*l.capRate >= l.capBurst {
			delete(l.buckets, k)
		}
	}
}

// allow applies l to the client IP (only when IPs can be told apart; same
// rule as the /ws limiter, see ws_limits.go) and to each extra key. It
// writes 429 + Retry-After and returns false when any bucket is empty.
func (a *authService) allow(w http.ResponseWriter, r *http.Request, l *rateLimiter, extraKeys ...string) bool {
	keys := extraKeys
	if ip, distinct := a.ipr.clientIP(r); distinct && ip != nil {
		keys = append([]string{"ip:" + ip.String()}, extraKeys...)
	} else {
		a.warnIndistinct.Do(func() {
			log.Printf("[users] rate limits by IP are off: requests arrive from a proxy and userManagement.trustedProxies is empty; per-address limits still apply, and the webhook limiter is off too")
		})
	}
	for _, k := range keys {
		if ok, wait := l.take(k); !ok {
			writeTooManyRequests(w, wait)
			return false
		}
	}
	return true
}

// writeTooManyRequests answers 429 with a Retry-After of at least 1 s.
func writeTooManyRequests(w http.ResponseWriter, wait time.Duration) {
	secs := int(math.Ceil(wait.Seconds()))
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	writeError(w, http.StatusTooManyRequests, "too many attempts, try again later")
}
