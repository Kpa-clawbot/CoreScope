package main

import (
	"os"
	"testing"
	"time"
)

// #2146: the daily planner statistics refresh also ran 2 minutes after every
// start. On a cold page cache that ANALYZE took 6 to 9 minutes on a 10-11 GB
// database while holding the single write connection, so every deploy or
// restart stalled ingest that long. The refresh now runs 24 hours after the
// previous one, recorded next to the database, and never sooner than the
// 2-minute startup stagger.

func TestNextPlannerStatsRefresh(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		last time.Time
		want time.Duration
	}{
		{"never refreshed", time.Time{}, plannerStatsStagger},
		{"refreshed an hour ago", now.Add(-time.Hour), 23 * time.Hour},
		{"refreshed 23h59m ago: 1 minute left, raised to the stagger", now.Add(-(24*time.Hour - time.Minute)), plannerStatsStagger},
		{"refreshed two days ago", now.Add(-48 * time.Hour), plannerStatsStagger},
		{"stamp in the future", now.Add(time.Hour), plannerStatsInterval},
	}
	for _, c := range cases {
		if got := nextPlannerStatsRefresh(c.last, now); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRefreshPlannerStatsRecordsWhen(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	if !s.lastPlannerStatsRefresh().IsZero() {
		t.Fatal("a new database has no recorded refresh")
	}
	before := time.Now().Add(-time.Second)
	if !s.RefreshPlannerStats(100) {
		t.Fatal("refresh failed")
	}
	last := s.lastPlannerStatsRefresh()
	if last.Before(before) || last.After(time.Now().Add(time.Second)) {
		t.Fatalf("recorded refresh time %v, want about now", last)
	}

	// A restart reads it back: the next refresh is a day away, not 2 minutes.
	s2, err := OpenStore(s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if d := nextPlannerStatsRefresh(s2.lastPlannerStatsRefresh(), time.Now()); d < 23*time.Hour {
		t.Fatalf("after a restart the next refresh is in %v, want about 24h", d)
	}
}

func TestRefreshPlannerStatsDisabledRecordsNothing(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	s.RefreshPlannerStats(-1)
	if !s.lastPlannerStatsRefresh().IsZero() {
		t.Fatal("a disabled refresh must not record a time")
	}
}

// When the time cannot be written next to the database, the running process
// still waits the full interval instead of refreshing again 2 minutes later.
func TestFailedRecordStillWaitsTheInterval(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	// A directory where the temporary file should go makes the write fail.
	if err := os.Mkdir(s.plannerStatsStampPath()+".tmp", 0o755); err != nil {
		t.Fatal(err)
	}
	if !s.RefreshPlannerStats(100) {
		t.Fatal("refresh failed")
	}
	if _, err := os.Stat(s.plannerStatsStampPath()); err == nil {
		t.Fatal("test setup: the stamp file was written anyway")
	}
	if d := nextPlannerStatsRefresh(s.lastPlannerStatsRefresh(), time.Now()); d < 23*time.Hour {
		t.Fatalf("next refresh in %v after a failed write, want about 24h", d)
	}
}

// Building statistics on a new database records the time too, so the routine
// refresh does not run a second ANALYZE 2 minutes later.
func TestEnsurePlannerStatsRecordsWhen(t *testing.T) {
	s := newTestStore(t)
	defer s.Close()
	if !s.EnsurePlannerStats(100) {
		t.Fatal("want a build on a database without statistics")
	}
	if s.lastPlannerStatsRefresh().IsZero() {
		t.Fatal("the build did not record its time")
	}
}
