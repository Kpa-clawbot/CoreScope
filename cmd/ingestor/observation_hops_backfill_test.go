package main

import (
	"context"
	"testing"
	"time"
)

func fastHopBackfill(t *testing.T, chunk int64) {
	t.Helper()
	c, p := hopBackfillChunkSeconds, hopBackfillPause
	t.Cleanup(func() { hopBackfillChunkSeconds, hopBackfillPause = c, p })
	hopBackfillChunkSeconds, hopBackfillPause = chunk, 0
}

func hopCoverage(s *Store) (since int64) {
	s.db.QueryRow(`SELECT since FROM observation_hops_coverage`).Scan(&since)
	return
}

func hopRows(s *Store) (n int) {
	s.db.QueryRow(`SELECT COUNT(*) FROM observation_hops`).Scan(&n)
	return
}

// legacyStore returns a store whose observations predate the index: no hop rows
// and a coverage mark claiming only the last moment.
func legacyStore(t *testing.T, obs ...struct {
	path string
	ts   int64
}) *Store {
	t.Helper()
	s := newTestStore(t)
	if _, err := s.db.Exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (1,'00','h1','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	for i, o := range obs {
		if _, err := s.db.Exec(`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp) VALUES (1, ?, ?, ?)`, i, o.path, o.ts); err != nil {
			t.Fatal(err)
		}
	}
	s.db.Exec(`DELETE FROM observation_hops`)
	s.db.Exec(`UPDATE observation_hops_coverage SET since = ?`, time.Now().Unix()+10)
	return s
}

type ob = struct {
	path string
	ts   int64
}

func TestBackfillObservationHops_IndexesWindowAndLowersCoverageToFloor(t *testing.T) {
	fastHopBackfill(t, 100)
	now := time.Now().Unix()
	s := legacyStore(t,
		ob{`["AA","BB"]`, now - 1000},
		ob{`["BB"]`, now - 800},
		ob{`not json`, now - 600},
		ob{`["CC","AA"]`, now - 400},
		ob{`["OLD"]`, now - int64(hopIndexDays+3)*86400}, // outside the window: must NOT be indexed
	)
	if err := s.backfillObservationHops(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := hopRows(s); n != 5 { // AA,BB | BB | - | CC,AA
		t.Fatalf("hop rows = %d, want 5", n)
	}
	floor := now - int64(hopIndexDays)*86400
	if got := hopCoverage(s); got < floor-5 || got > floor+5 {
		t.Fatalf("coverage = %d, want ~floor %d (never claims data it did not index)", got, floor)
	}
	// Idempotent / resumable.
	if err := s.backfillObservationHops(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := hopRows(s); n != 5 {
		t.Fatalf("hop rows after rerun = %d, want 5", n)
	}
}

func TestBackfillObservationHops_StopsOnCancelKeepingProgress(t *testing.T) {
	fastHopBackfill(t, 100)
	now := time.Now().Unix()
	s := legacyStore(t, ob{`["AA"]`, now - 1000})
	before := hopCoverage(s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.backfillObservationHops(ctx); err == nil {
		t.Fatal("cancelled backfill must return an error so the async migration stays resumable")
	}
	if got := hopCoverage(s); got != before {
		t.Fatalf("cancelled-before-start run moved coverage %d -> %d", before, got)
	}
}

// A bad-clock observer leaves rows near timestamp 0. The backfill must jump the
// gap, not walk empty chunks (which hung the whole test suite).
func TestBackfillObservationHops_SkipsHugeEmptyGaps(t *testing.T) {
	fastHopBackfill(t, 3600)
	now := time.Now().Unix()
	s := legacyStore(t, ob{`["AA"]`, 1}, ob{`["BB"]`, now - 3*86400})
	done := make(chan error, 1)
	go func() { done <- s.backfillObservationHops(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("backfill did not finish: walking empty chunks instead of skipping gaps")
	}
	if n := hopRows(s); n != 1 {
		t.Fatalf("hop rows = %d, want 1 (only the in-window row)", n)
	}
}

func TestTrimObservationHops_SlidesWindowAndDeletesByKey(t *testing.T) {
	fastHopBackfill(t, 3600)
	now := time.Now().Unix()
	day := int64(86400)
	s := legacyStore(t,
		ob{`["AA","BB"]`, now - int64(hopIndexDays-2)*day}, // stays
		ob{`["CC"]`, now - int64(hopIndexDays+1)*day},      // falls out
		ob{`["DD","EE","FF"]`, now - int64(hopIndexDays+2)*day},
	)
	// Pretend an old, deeper window was indexed (older install / larger setting).
	s.db.Exec(`UPDATE observation_hops_coverage SET since = ?`, now-int64(hopIndexDays+5)*day)
	s.db.Exec(`INSERT INTO observation_hops (hop, timestamp, observation_id)
		SELECT DISTINCT j.value, o.timestamp, o.id FROM observations o, json_each(o.path_json) j`)
	if n := hopRows(s); n != 6 {
		t.Fatalf("setup: %d hop rows, want 6", n)
	}
	n, err := s.TrimObservationHops()
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 || hopRows(s) != 2 {
		t.Fatalf("trim deleted %d, left %d; want 4 deleted, 2 left", n, hopRows(s))
	}
	floor := now - int64(hopIndexDays)*day
	if got := hopCoverage(s); got < floor-5 || got > floor+5 {
		t.Fatalf("coverage = %d, want ~floor %d", got, floor)
	}
	if n, _ := s.TrimObservationHops(); n != 0 {
		t.Fatalf("second trim deleted %d, want 0 (nothing newly out of window)", n)
	}
}

// Scheduling the backfill inside OpenStore keeps live ingest dark for its whole
// duration: main gates ingest on WaitForAsyncMigrations(). It must be started
// explicitly, after the gate.
func TestOpenStoreDoesNotScheduleHopBackfill(t *testing.T) {
	s := newTestStore(t)
	if st, err := s.AsyncMigrationStatus("observation_hops_backfill_v1"); err == nil {
		t.Fatalf("OpenStore scheduled the hop backfill (status %q); it would block the ingest gate", st)
	}
	fastHopBackfill(t, 900)
	s.ScheduleObservationHopsBackfill()
	s.WaitForAsyncMigrations()
	if st, err := s.AsyncMigrationStatus("observation_hops_backfill_v1"); err != nil || st != "done" {
		t.Fatalf("explicit schedule: status %q err %v, want done", st, err)
	}
}
