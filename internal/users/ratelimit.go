package users

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Rate limits shared by every server replica: one row per key and fixed
// window, counting the hits in it. A key names its limiter as well as its
// subject ("login:ip:192.0.2.1"), since one key used with two window
// lengths would share rows.

// RateLimitHit records one hit for key in the fixed window containing now and
// reports whether the count, including this hit, is <= max. Atomic across
// replicas (one statement or one transaction; no read-then-write race).
//
// Windows are aligned to the Unix epoch and must be a whole number of
// seconds (window_start is stored in Unix seconds).
func (s *Store) RateLimitHit(ctx context.Context, key string, window time.Duration, max int, now time.Time) (allowed bool, err error) {
	switch {
	case key == "":
		return false, errors.New("users: rate limit key is empty")
	case window < time.Second || window%time.Second != 0:
		return false, fmt.Errorf("users: rate limit window %v is not a whole number of seconds", window)
	case max < 1:
		return false, fmt.Errorf("users: rate limit max %d is below 1", max)
	}
	// One upsert: the row lock it takes on conflict serializes concurrent
	// hits on Postgres, and each sees the count its own increment produced.
	var hits int64
	err = s.db.QueryRowContext(ctx, `INSERT INTO rate_limits (key, window_start, hits) VALUES (?, ?, 1)
		ON CONFLICT (key, window_start) DO UPDATE SET hits = rate_limits.hits + 1
		RETURNING hits`, key, rateLimitWindowStart(now, window)).Scan(&hits)
	if err != nil {
		return false, fmt.Errorf("users: rate limit hit: %w", err)
	}
	return hits <= int64(max), nil
}

// rateLimitWindowStart is the Unix second the window containing now starts
// at (floored, also before 1970). window is a positive whole number of seconds.
func rateLimitWindowStart(now time.Time, window time.Duration) int64 {
	w := int64(window / time.Second)
	t := now.Unix()
	start := t / w * w
	if start > t {
		start -= w
	}
	return start
}

// PruneRateLimits deletes windows that ended before cutoff.
//
// Window lengths are not stored, so a row is deleted when its window
// started before cutoff: the caller passes now minus the longest window it
// uses, and every window so deleted has ended.
func (s *Store) PruneRateLimits(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM rate_limits WHERE window_start < ?`, unix(cutoff))
	if err != nil {
		return 0, fmt.Errorf("users: prune rate limits: %w", err)
	}
	return res.RowsAffected()
}
