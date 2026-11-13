package main

import (
	"context"
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
// With a PostgreSQL account store (share) the hits are counted in the store
// instead, so every replica sees the same limit: users.RateLimitHit with the
// same key, n as the maximum and the period as a fixed window aligned to the
// Unix epoch. Fixed windows differ from the bucket at the boundary: the
// count starts over when a window ends, so up to 2n requests can pass in a
// short span around it (n at the end of one window, n at the start of the
// next), and a key that used up its n waits until the window ends rather
// than one refill interval. The in-memory bucket stays as the fallback when
// the store cannot be reached (each replica then limits on its own).
type rateLimiter struct {
	mu      sync.Mutex
	rate    float64 // tokens per second
	burst   float64
	buckets map[string]*rlBucket
	now     func() time.Time

	maxBuckets int // cap on len(buckets); a field so tests can lower it
	warnFull   sync.Once

	n   int           // requests per period, as given to newRateLimiter
	per time.Duration // the period; the fixed window when shared
	// shared and name are set by share: hits are counted in the store under
	// name + ":" + key. Fixed after construction.
	shared   sharedRateLimits
	name     string
	warnedAt time.Time // last logged store failure; under mu
}

// sharedRateLimits is the store's cross-replica counter (*users.Store).
type sharedRateLimits interface {
	RateLimitHit(ctx context.Context, key string, window time.Duration, max int, now time.Time) (bool, error)
}

// sharedRateLimitTimeout bounds one store hit; past it the in-memory bucket
// decides.
const sharedRateLimitTimeout = 2 * time.Second

// sharedRateLimitWarnEvery spaces the log lines of a failing store.
const sharedRateLimitWarnEvery = time.Minute

const rateLimiterMaxBuckets = 100000

type rlBucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(n int, per time.Duration) *rateLimiter {
	return &rateLimiter{rate: float64(n) / per.Seconds(), burst: float64(n),
		buckets: map[string]*rlBucket{}, now: time.Now, maxBuckets: rateLimiterMaxBuckets,
		n: n, per: per}
}

// share counts l's hits in st under keys prefixed with name (which names the
// limiter, so two limiters never share a row). Call before first use.
func (l *rateLimiter) share(st sharedRateLimits, name string) {
	l.shared, l.name = st, name
}

// take consumes one token for key. When none is left it reports how long
// until the next one.
func (l *rateLimiter) take(key string) (bool, time.Duration) {
	if l.shared != nil {
		return l.takeShared(key)
	}
	return l.takeLocal(key)
}

// takeShared records one hit in the store's fixed window; when the window's
// count is over n it reports the time left in the window. A store failure
// falls back to the in-memory bucket.
func (l *rateLimiter) takeShared(key string) (bool, time.Duration) {
	now := l.now()
	ctx, cancel := context.WithTimeout(context.Background(), sharedRateLimitTimeout)
	ok, err := l.shared.RateLimitHit(ctx, l.name+":"+key, l.per, l.n, now)
	cancel()
	if err != nil {
		l.mu.Lock()
		if l.warnedAt.IsZero() || now.Sub(l.warnedAt) >= sharedRateLimitWarnEvery {
			l.warnedAt = now
			log.Printf("[users] shared rate limit %s: %v; this replica limits on its own meanwhile", l.name, err)
		}
		l.mu.Unlock()
		return l.takeLocal(key)
	}
	if ok {
		return true, 0
	}
	return false, fixedWindowLeft(now, l.per)
}

// fixedWindowLeft is the time from now to the end of the epoch-aligned
// window of length per containing now (users.RateLimitHit's windows).
func fixedWindowLeft(now time.Time, per time.Duration) time.Duration {
	w := int64(per / time.Second)
	t := now.Unix()
	start := t / w * w
	if start > t {
		start -= w
	}
	return time.Unix(start+w, 0).Sub(now)
}

// takeLocal is take on the in-memory bucket.
func (l *rateLimiter) takeLocal(key string) (bool, time.Duration) {
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
