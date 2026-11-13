package users

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestRateLimitHit(t *testing.T) {
	forEachBackend(t, func(t *testing.T, newTestStore storeFactory) {
		st, _ := newTestStore(t)
		ctx := context.Background()
		const window, max = 15 * time.Minute, 3
		// Inside one window (it starts at 12:00 UTC: window divides the day).
		t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
		hit := func(key string, at time.Time) bool {
			t.Helper()
			ok, err := st.RateLimitHit(ctx, key, window, max, at)
			if err != nil {
				t.Fatalf("RateLimitHit(%s, %v): %v", key, at, err)
			}
			return ok
		}
		for i := 0; i < max; i++ {
			if !hit("login:a", t0.Add(time.Duration(i)*time.Minute)) {
				t.Fatalf("hit %d refused, want allowed", i+1)
			}
		}
		if hit("login:a", t0.Add(14*time.Minute+59*time.Second)) {
			t.Fatal("hit max+1 allowed, want refused")
		}
		if hit("login:a", t0.Add(10*time.Minute)) {
			t.Fatal("a further hit in the same window allowed")
		}
		// Keys are independent.
		if !hit("login:b", t0) {
			t.Fatal("another key refused")
		}
		// The next window starts afresh.
		next := t0.Add(window)
		for i := 0; i < max; i++ {
			if !hit("login:a", next.Add(time.Duration(i)*time.Second)) {
				t.Fatalf("next window: hit %d refused", i+1)
			}
		}
		if hit("login:a", next.Add(time.Minute)) {
			t.Fatal("next window: hit max+1 allowed")
		}
	})
}

func TestRateLimitHitRejectsBadArguments(t *testing.T) {
	forEachBackend(t, func(t *testing.T, newTestStore storeFactory) {
		st, clk := newTestStore(t)
		ctx := context.Background()
		for _, c := range []struct {
			name   string
			key    string
			window time.Duration
			max    int
		}{
			{"empty key", "", time.Minute, 1},
			{"zero window", "k", 0, 1},
			{"sub-second window", "k", 500 * time.Millisecond, 1},
			{"fractional-second window", "k", 1500 * time.Millisecond, 1},
			{"zero max", "k", time.Minute, 0},
		} {
			if ok, err := st.RateLimitHit(ctx, c.key, c.window, c.max, clk.Now()); err == nil || ok {
				t.Errorf("%s: ok=%v err=%v, want an error", c.name, ok, err)
			}
		}
	})
}

// Times before 1970 floor to the window below, not toward zero.
func TestRateLimitWindowStartFloors(t *testing.T) {
	if got := rateLimitWindowStart(time.Unix(-1, 0), time.Minute); got != -60 {
		t.Fatalf("window start of -1s = %d, want -60", got)
	}
	if got := rateLimitWindowStart(time.Unix(119, 999), time.Minute); got != 60 {
		t.Fatalf("window start of 119s = %d, want 60", got)
	}
}

func TestPruneRateLimits(t *testing.T) {
	forEachBackend(t, func(t *testing.T, newTestStore storeFactory) {
		st, _ := newTestStore(t)
		ctx := context.Background()
		t0 := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
		for i := 0; i < 3; i++ { // windows 12:00, 12:01, 12:02
			if _, err := st.RateLimitHit(ctx, "k", time.Minute, 5, t0.Add(time.Duration(i)*time.Minute)); err != nil {
				t.Fatal(err)
			}
		}
		n, err := st.PruneRateLimits(ctx, t0.Add(2*time.Minute))
		if err != nil || n != 2 {
			t.Fatalf("PruneRateLimits = %d, %v; want 2", n, err)
		}
		var left int
		if err := st.db.QueryRow(`SELECT COUNT(*) FROM rate_limits`).Scan(&left); err != nil || left != 1 {
			t.Fatalf("rows left = %d, %v; want 1", left, err)
		}
		// The surviving window still counts its hit.
		for i := 0; i < 4; i++ {
			if ok, _ := st.RateLimitHit(ctx, "k", time.Minute, 5, t0.Add(2*time.Minute)); !ok {
				t.Fatalf("hit %d refused", i+2)
			}
		}
		if ok, _ := st.RateLimitHit(ctx, "k", time.Minute, 5, t0.Add(2*time.Minute)); ok {
			t.Fatal("hit 6 allowed: the pruned rows took the live window with them")
		}
	})
}

// Two replicas, 20 concurrent hits each on one key: exactly max get through.
func TestPostgresRaceRateLimit(t *testing.T) {
	a, b, clk := twoPostgresStores(t)
	const max = 10
	now := clk.Now()
	var allowed atomic.Int32
	errs := race(40, func(i int) error {
		ok, err := pick(i, a, b).RateLimitHit(context.Background(), "ip:192.0.2.1", time.Hour, max, now)
		if ok {
			allowed.Add(1)
		}
		return err
	})
	for i, err := range errs {
		if err != nil {
			t.Fatalf("racer %d: %v", i, err)
		}
	}
	if got := allowed.Load(); got != max {
		t.Fatalf("allowed = %d, want %d", got, max)
	}
}

// A v8 users.db gains rate_limits on open, keeping its data.
func TestMigrationV9AddsRateLimits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "users.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	stmts := []string{`CREATE TABLE schema_version (version INTEGER NOT NULL)`, `INSERT INTO schema_version (version) VALUES (8)`}
	for _, m := range migrations[:8] {
		stmts = append(stmts, m...)
	}
	stmts = append(stmts, `INSERT INTO users (email, display_name, password_hash, created_at) VALUES ('old@example.org', 'Old', 'x', 1)`)
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("build v8 db: %v", err)
		}
	}
	db.Close()

	st, err := Open(path)
	if err != nil {
		t.Fatalf("Open v8 db: %v", err)
	}
	defer st.Close()
	if v, err := st.SchemaVersion(); err != nil || v != 9 {
		t.Fatalf("SchemaVersion = %d, %v; want 9", v, err)
	}
	if ok, err := st.RateLimitHit(context.Background(), "k", time.Minute, 1, time.Unix(0, 0)); err != nil || !ok {
		t.Fatalf("RateLimitHit on migrated db = %v, %v", ok, err)
	}
	if _, err := st.GetByID(1); err != nil {
		t.Fatalf("user lost in migration: %v", err)
	}
}
