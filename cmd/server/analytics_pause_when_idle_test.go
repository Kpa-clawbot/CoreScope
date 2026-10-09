package main

import (
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

// The analytics recomputers (#1240) recompute every endpoint on a fixed
// interval whether or not anyone reads it. On a production instance the
// topology, RF and hash-size endpoints were each read 23 times in 45 hours
// while being recomputed every 5 minutes, and the recompute loop accounted for
// 30% of all bytes allocated (121 GB in 45 h), which drives the collector.
//
// With pauseWhenIdle, a tick with no read since the previous compute is
// skipped, and the next Load serves the existing snapshot and kicks a refresh.

func startCountingRecomputer(t *testing.T, interval time.Duration, pause bool) (*analyticsRecomputer, *int64) {
	t.Helper()
	var runs int64
	r := newAnalyticsRecomputer("test", interval, func() interface{} {
		return atomic.AddInt64(&runs, 1)
	})
	r.pauseWhenIdle = pause
	r.Start()
	t.Cleanup(r.Stop)
	return r, &runs
}

func TestAnalyticsRecomputer_PauseWhenIdleSkipsUnreadTicks(t *testing.T) {
	_, runs := startCountingRecomputer(t, 20*time.Millisecond, true)
	time.Sleep(300 * time.Millisecond) // ~15 ticks, no reads
	if got := atomic.LoadInt64(runs); got > 2 {
		t.Fatalf("expected the idle recomputer to stop after its first pass, got %d computes", got)
	}
}

func TestAnalyticsRecomputer_ReadAfterPauseServesSnapshotAndRefreshes(t *testing.T) {
	r, runs := startCountingRecomputer(t, 20*time.Millisecond, true)
	time.Sleep(150 * time.Millisecond) // pause
	before := atomic.LoadInt64(runs)

	if v := r.Load(); v == nil {
		t.Fatal("expected the existing snapshot while paused, got nil")
	}
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt64(runs) == before && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt64(runs) == before {
		t.Fatal("expected a read on a paused recomputer to start a refresh")
	}
	if got, ok := r.Load().(int64); !ok || got <= before {
		t.Fatalf("expected a newer snapshot after the refresh, got %v", r.Load())
	}
}

func TestAnalyticsRecomputer_KeepsTickingWhileRead(t *testing.T) {
	r, runs := startCountingRecomputer(t, 20*time.Millisecond, true)
	stop := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(stop) {
		r.Load()
		time.Sleep(5 * time.Millisecond)
	}
	if got := atomic.LoadInt64(runs); got < 6 {
		t.Fatalf("expected a recomputer that is being read to keep refreshing, got %d computes", got)
	}
}

// Default behaviour is unchanged: without pauseWhenIdle it recomputes on every
// tick, read or not.
func TestAnalyticsRecomputer_DefaultKeepsTickingUnread(t *testing.T) {
	_, runs := startCountingRecomputer(t, 20*time.Millisecond, false)
	time.Sleep(300 * time.Millisecond)
	if got := atomic.LoadInt64(runs); got < 6 {
		t.Fatalf("expected the default recomputer to keep ticking, got %d computes", got)
	}
}

func TestConfig_AnalyticsPauseWhenIdle(t *testing.T) {
	var on, off Config
	if err := json.Unmarshal([]byte(`{"analytics":{"pauseWhenIdle":true}}`), &on); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"analytics":{"defaultIntervalSeconds":300}}`), &off); err != nil {
		t.Fatal(err)
	}
	if !on.AnalyticsPauseWhenIdle() {
		t.Error("expected pauseWhenIdle:true to enable it")
	}
	if off.AnalyticsPauseWhenIdle() {
		t.Error("expected it to default to off")
	}
	if (*Config)(nil).AnalyticsPauseWhenIdle() {
		t.Error("expected a nil config to default to off")
	}
}

func TestStartAnalyticsRecomputers_AppliesPauseWhenIdle(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	stop := store.StartAnalyticsRecomputers(time.Hour, AnalyticsRecomputeIntervals{PauseWhenIdle: true})
	defer stop()
	store.analyticsRecomputerMu.Lock()
	all := store.analyticsRecomputersLocked()
	store.analyticsRecomputerMu.Unlock()
	for _, rc := range all {
		if !rc.pauseWhenIdle {
			t.Errorf("recomputer %q did not get pauseWhenIdle", rc.name)
		}
	}
}
