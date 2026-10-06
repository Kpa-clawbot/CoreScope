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
type rateLimiter struct {
	mu      sync.Mutex
	rate    float64 // tokens per second
	burst   float64
	buckets map[string]*rlBucket
	now     func() time.Time

	maxBuckets int // cap on len(buckets); a field so tests can lower it
	warnFull   sync.Once
}

const rateLimiterMaxBuckets = 100000

type rlBucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(n int, per time.Duration) *rateLimiter {
	return &rateLimiter{rate: float64(n) / per.Seconds(), burst: float64(n),
		buckets: map[string]*rlBucket{}, now: time.Now, maxBuckets: rateLimiterMaxBuckets}
}

// take consumes one token for key. When none is left it reports how long
// until the next one.
func (l *rateLimiter) take(key string) (bool, time.Duration) {
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
		b = &rlBucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, time.Duration((1 - b.tokens) / l.rate * float64(time.Second))
}

// gc drops buckets that have refilled completely (they carry no state).
func (l *rateLimiter) gc() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweepLocked(l.now())
}

// sweepLocked deletes fully refilled buckets. The caller holds l.mu.
func (l *rateLimiter) sweepLocked(now time.Time) {
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.last).Seconds()*l.rate >= l.burst {
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
			secs := int(math.Ceil(wait.Seconds()))
			if secs < 1 {
				secs = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(secs))
			writeError(w, http.StatusTooManyRequests, "too many attempts, try again later")
			return false
		}
	}
	return true
}
