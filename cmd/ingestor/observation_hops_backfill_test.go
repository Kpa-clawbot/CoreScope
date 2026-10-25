package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/meshcore-analyzer/dbschema"
)

const day = int64(86400)

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

// hopSchemaObjects lists the index's tables and triggers present in the DB.
func hopSchemaObjects(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.db.Query(`SELECT type || ' ' || name FROM sqlite_master
		WHERE name LIKE 'observation_hops%' OR name LIKE 'trg_observation_hops%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		out = append(out, n)
	}
	return out
}

// checkHopIndex is the oracle for the index invariant: observation_hops holds
// exactly one row per (upper-cased text hop, observation) for every observation
// at or above the coverage mark, and nothing else. The expected set is built in
// Go from path_json, independently of the production SQL.
func checkHopIndex(t *testing.T, s *Store) {
	t.Helper()
	since := hopCoverage(s)
	want := map[string]bool{}
	rows, err := s.db.Query(`SELECT id, timestamp, path_json FROM observations WHERE path_json IS NOT NULL`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, ts int64
		var pj string
		rows.Scan(&id, &ts, &pj)
		if ts < since {
			continue
		}
		var elems []interface{}
		if json.Unmarshal([]byte(pj), &elems) != nil {
			continue
		}
		for _, e := range elems {
			if h, ok := e.(string); ok {
				want[fmt.Sprintf("%s|%d|%d", strings.ToUpper(h), ts, id)] = true
			}
		}
	}
	rows.Close()
	got := map[string]bool{}
	rows, err = s.db.Query(`SELECT hop, timestamp, observation_id FROM observation_hops`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var h string
		var ts, id int64
		rows.Scan(&h, &ts, &id)
		got[fmt.Sprintf("%s|%d|%d", h, ts, id)] = true
	}
	rows.Close()
	var missing, extra []string
	for k := range want {
		if !got[k] {
			missing = append(missing, k)
		}
	}
	for k := range got {
		if !want[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	if len(missing)+len(extra) > 0 {
		t.Fatalf("hop index out of step with observations at/after since=%d:\n missing %v\n extra   %v", since, missing, extra)
	}
}

func assertNear(t *testing.T, what string, got, want int64) {
	t.Helper()
	if got < want-5 || got > want+5 {
		t.Fatalf("%s = %d, want ~%d", what, got, want)
	}
}

type ob = struct {
	path string
	ts   int64
}

func insertObs(t *testing.T, s *Store, obs ...ob) {
	t.Helper()
	for i, o := range obs {
		if _, err := s.db.Exec(`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp) VALUES (1, ?, ?, ?)`, i, o.path, o.ts); err != nil {
			t.Fatal(err)
		}
	}
}

// legacyStore returns a store with the index on (days) whose observations
// predate it: no hop rows, and a coverage mark claiming only the present.
func legacyStore(t *testing.T, days int, obs ...ob) *Store {
	t.Helper()
	s := newTestStore(t)
	if _, err := s.db.Exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (1,'00','h1','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	insertObs(t, s, obs...)
	if err := s.ConfigureObservationHops(days); err != nil {
		t.Fatal(err)
	}
	return s
}

// restart re-applies the setting and runs the boot-time backfill to completion,
// as main does on the next ingestor start.
func restart(t *testing.T, s *Store, days int) {
	t.Helper()
	if err := s.ConfigureObservationHops(days); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TrimObservationHops(); err != nil {
		t.Fatal(err)
	}
	s.ScheduleObservationHopsBackfill()
	s.WaitForAsyncMigrations()
}

// The default install and an upgrade that never sets the key: no table, no
// triggers, no backfill, and ingest still works.
func TestHopIndex_OffByDefault_InstallsNothing(t *testing.T) {
	s := newTestStore(t)
	if got := hopSchemaObjects(t, s); len(got) != 0 {
		t.Fatalf("OpenStore installed hop index objects: %v", got)
	}
	cfg := &Config{}
	restart(t, s, cfg.HopIndexDaysOrZero())
	if got := hopSchemaObjects(t, s); len(got) != 0 {
		t.Fatalf("hopIndexDays unset installed hop index objects: %v", got)
	}
	if _, err := s.db.Exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (1,'00','h1','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	insertObs(t, s, ob{`["AA"]`, time.Now().Unix()})
	if n, err := s.TrimObservationHops(); n != 0 || err != nil {
		t.Fatalf("trim with index off: %d, %v", n, err)
	}
}

func TestHopIndexDaysOrZero(t *testing.T) {
	for _, c := range []struct {
		r    *RetentionConfig
		want int
	}{{nil, 0}, {&RetentionConfig{}, 0}, {&RetentionConfig{HopIndexDays: 14}, 14}} {
		if got := (&Config{Retention: c.r}).HopIndexDaysOrZero(); got != c.want {
			t.Errorf("HopIndexDaysOrZero(%+v) = %d, want %d", c.r, got, c.want)
		}
	}
}

// Review #2214 round 2: observerDays reads -1 as "keep forever". The same value
// here would remove the index without a word, so LoadConfig refuses it; 0 and
// positive values load as before.
func TestLoadConfig_RejectsNegativeHopIndexDays(t *testing.T) {
	path := t.TempDir() + "/config.json"
	for _, c := range []struct {
		json    string
		wantErr bool
	}{
		{`{"retention":{"hopIndexDays":-1}}`, true},
		{`{"retention":{"hopIndexDays":-14}}`, true},
		{`{"retention":{"hopIndexDays":0}}`, false},
		{`{"retention":{"hopIndexDays":14}}`, false},
		{`{"retention":{}}`, false},
	} {
		if err := os.WriteFile(path, []byte(c.json), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadConfig(path)
		switch {
		case c.wantErr && (err == nil || !strings.Contains(err.Error(), "retention.hopIndexDays")):
			t.Errorf("%s: LoadConfig error = %v, want one naming retention.hopIndexDays", c.json, err)
		case !c.wantErr && err != nil:
			t.Errorf("%s: LoadConfig error = %v, want nil", c.json, err)
		}
	}
}

// Switching the index off removes the table and triggers, and ingest, path
// repair and deletes keep working without them. Switching it back on rebuilds
// the window.
func TestHopIndex_OffRemovesEverything_OnRebuilds(t *testing.T) {
	fastHopBackfill(t, 3600)
	fixed := time.Now().Truncate(time.Second)
	previousClock := hopNow
	t.Cleanup(func() { hopNow = previousClock })
	hopNow = func() time.Time { return fixed }
	now := fixed.Unix()
	s := legacyStore(t, 14, ob{`["AA","BB"]`, now - 2*day})
	restart(t, s, 14)
	checkHopIndex(t, s)
	if hopRows(s) != 2 {
		t.Fatalf("on: %d hop rows, want 2", hopRows(s))
	}
	restart(t, s, 0)
	if got := hopSchemaObjects(t, s); len(got) != 0 {
		t.Fatalf("hopIndexDays 0 left objects behind: %v", got)
	}
	insertObs(t, s, ob{`["CC"]`, now - day})
	if _, err := s.db.Exec(`UPDATE observations SET path_json = '["DD"]' WHERE path_json = '["CC"]'`); err != nil {
		t.Fatalf("path update with index off: %v", err)
	}
	if _, err := s.db.Exec(`DELETE FROM observations WHERE path_json = '["DD"]'`); err != nil {
		t.Fatalf("delete with index off: %v", err)
	}
	insertObs(t, s, ob{`["EE"]`, now - 3*day})
	restart(t, s, 14)
	checkHopIndex(t, s)
	if got, want := hopCoverage(s), now-14*day; got != want {
		t.Fatalf("coverage after re-enable = %d, want %d", got, want)
	}
	if hopRows(s) != 3 { // AA, BB, EE
		t.Fatalf("re-enabled: %d hop rows, want 3", hopRows(s))
	}
}

func TestBackfillObservationHops_IndexesWindowAndLowersCoverageToFloor(t *testing.T) {
	fastHopBackfill(t, 100)
	now := time.Now().Unix()
	s := legacyStore(t, 14,
		ob{`["AA","BB"]`, now - 1000},
		ob{`["BB"]`, now - 800},
		ob{`not json`, now - 600},
		ob{`["CC","AA"]`, now - 400},
		ob{`["OLD"]`, now - 17*day}, // outside the window: must NOT be indexed
	)
	if err := s.backfillObservationHops(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := hopRows(s); n != 5 { // AA,BB | BB | - | CC,AA
		t.Fatalf("hop rows = %d, want 5", n)
	}
	assertNear(t, "coverage", hopCoverage(s), now-14*day)
	checkHopIndex(t, s)
	// Idempotent / resumable.
	if err := s.backfillObservationHops(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := hopRows(s); n != 5 {
		t.Fatalf("hop rows after rerun = %d, want 5", n)
	}
}

// Review #2214: raising hopIndexDays on an install whose backfill already
// finished must extend coverage on the next start, not leave it at the old
// window.
func TestHopIndex_RaisingWindowBackfillsOnNextStart(t *testing.T) {
	fastHopBackfill(t, 3600)
	now := time.Now().Unix()
	s := legacyStore(t, 3,
		ob{`["AA"]`, now - day},
		ob{`["BB"]`, now - 5*day},
		ob{`["CC"]`, now - 9*day},
		ob{`["DD"]`, now - 12*day}, // beyond even the raised window
	)
	restart(t, s, 3)
	assertNear(t, "coverage at 3 days", hopCoverage(s), now-3*day)
	checkHopIndex(t, s)
	if hopRows(s) != 1 {
		t.Fatalf("3-day window: %d hop rows, want 1", hopRows(s))
	}
	restart(t, s, 10)
	assertNear(t, "coverage after raising to 10 days", hopCoverage(s), now-10*day)
	checkHopIndex(t, s)
	if hopRows(s) != 3 {
		t.Fatalf("10-day window: %d hop rows, want 3", hopRows(s))
	}
}

func TestHopIndex_LoweringWindowTrimsOnNextStart(t *testing.T) {
	fastHopBackfill(t, 3600)
	now := time.Now().Unix()
	s := legacyStore(t, 10, ob{`["AA"]`, now - day}, ob{`["BB","CC"]`, now - 5*day}, ob{`["DD"]`, now - 9*day})
	restart(t, s, 10)
	if hopRows(s) != 4 {
		t.Fatalf("10-day window: %d hop rows, want 4", hopRows(s))
	}
	restart(t, s, 3)
	assertNear(t, "coverage after lowering to 3 days", hopCoverage(s), now-3*day)
	checkHopIndex(t, s)
	if hopRows(s) != 1 {
		t.Fatalf("3-day window: %d hop rows, want 1", hopRows(s))
	}
}

func TestBackfillObservationHops_StopsOnCancelKeepingProgress(t *testing.T) {
	fastHopBackfill(t, 100)
	now := time.Now().Unix()
	s := legacyStore(t, 14, ob{`["AA"]`, now - 1000})
	before := hopCoverage(s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.backfillObservationHops(ctx); err == nil {
		t.Fatal("cancelled backfill must return an error")
	}
	if got := hopCoverage(s); got != before {
		t.Fatalf("cancelled-before-start run moved coverage %d -> %d", before, got)
	}
	// Resumes on the next start.
	restart(t, s, 14)
	checkHopIndex(t, s)
	assertNear(t, "coverage after resume", hopCoverage(s), now-14*day)
}

// A bad-clock observer leaves rows near timestamp 0. The backfill must jump the
// gap, not walk empty chunks (which hung the whole test suite).
func TestBackfillObservationHops_SkipsHugeEmptyGaps(t *testing.T) {
	fastHopBackfill(t, 3600)
	now := time.Now().Unix()
	s := legacyStore(t, 14, ob{`["AA"]`, 1}, ob{`["BB"]`, now - 3*day})
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
	checkHopIndex(t, s)
}

func TestTrimObservationHops_SlidesWindowAndDeletesByKey(t *testing.T) {
	fastHopBackfill(t, 3600)
	fixed := time.Now().Truncate(time.Second)
	previousClock := hopNow
	t.Cleanup(func() { hopNow = previousClock })
	hopNow = func() time.Time { return fixed }
	now := fixed.Unix()
	s := legacyStore(t, 14,
		ob{`["AA","BB"]`, now - 12*day}, // stays
		ob{`["CC"]`, now - 15*day},      // falls out
		ob{`["DD","EE","FF"]`, now - 16*day},
	)
	// An older, deeper window was indexed (as with a larger setting before).
	s.db.Exec(`UPDATE observation_hops_coverage SET since = ?`, now-19*day)
	if _, err := dbschema.IndexObservationHops(s.db, 0, now); err != nil {
		t.Fatal(err)
	}
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
	if got, want := hopCoverage(s), now-14*day; got != want {
		t.Fatalf("coverage = %d, want %d", got, want)
	}
	checkHopIndex(t, s)
	if n, _ := s.TrimObservationHops(); n != 0 {
		t.Fatalf("second trim deleted %d, want 0 (nothing newly out of window)", n)
	}
}

// Stored hops are upper-case whichever path wrote them (insert trigger, update
// trigger, backfill), so a lookup matches what the case-insensitive LIKE scan
// matches.
func TestHopIndex_StoresHopsUpperCase(t *testing.T) {
	fastHopBackfill(t, 3600)
	now := time.Now().Unix()
	s := legacyStore(t, 14, ob{`["ab12","Cd"]`, now - 2*day}) // indexed by the backfill
	restart(t, s, 14)
	insertObs(t, s, ob{`["ef"]`, now - day}) // indexed by the insert trigger
	if _, err := s.db.Exec(`UPDATE observations SET path_json = '["gh","ab12"]' WHERE path_json = '["ef"]'`); err != nil {
		t.Fatal(err)
	}
	checkHopIndex(t, s)
	var lower int
	s.db.QueryRow(`SELECT COUNT(*) FROM observation_hops WHERE hop != UPPER(hop)`).Scan(&lower)
	if lower != 0 {
		t.Fatalf("%d hop rows are not upper-case", lower)
	}
	// Deletes find the upper-cased rows.
	s.db.Exec(`DELETE FROM observations`)
	if n := hopRows(s); n != 0 {
		t.Fatalf("delete left %d hop rows", n)
	}
}

// Review #2214: a trim must never interleave with a running backfill. Here the
// window shrinks while the backfill is past the new floor, which makes the trim
// want to delete rows the backfill has just indexed; if it ran between chunks,
// the next chunk would lower the mark again over the deleted rows.
func TestTrimWaitsForRunningBackfill(t *testing.T) {
	fastHopBackfill(t, 3600)
	fixed := time.Now().Truncate(time.Second)
	previousClock := hopNow
	t.Cleanup(func() { hopNow = previousClock })
	hopNow = func() time.Time { return fixed }
	now := fixed.Unix()
	var obs []ob
	for d := int64(1); d <= 9; d++ {
		obs = append(obs, ob{fmt.Sprintf(`["H%d"]`, d), now - d*day})
	}
	s := legacyStore(t, 10, obs...)
	trimDone := make(chan error, 1)
	var trimWorker sync.WaitGroup
	fired := false
	t.Cleanup(func() { hopBackfillAfterChunk = nil })
	// Cleanup is LIFO: join before resetting the hook, store, or clock.
	t.Cleanup(trimWorker.Wait)
	hopBackfillAfterChunk = func() {
		if fired || hopCoverage(s) > now-5*day {
			return
		}
		fired = true
		s.hopIndexDays = 3
		trimWorker.Add(1)
		go func() {
			defer trimWorker.Done()
			_, err := s.TrimObservationHops()
			trimDone <- err
		}()
		select {
		case <-trimDone:
			t.Error("trim ran while the backfill was between chunks")
			trimDone <- nil
		case <-time.After(300 * time.Millisecond):
		}
	}
	if err := s.backfillObservationHops(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Fatal("test hook never fired")
	}
	select {
	case err := <-trimDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("trim never finished after the backfill")
	}
	checkHopIndex(t, s)
	if got, want := hopCoverage(s), now-3*day; got != want {
		t.Fatalf("coverage = %d, want %d", got, want)
	}
}

// Scheduling the backfill inside OpenStore keeps live ingest dark for its whole
// duration: main gates ingest on WaitForAsyncMigrations(). It must be started
// explicitly, after the gate.
func TestOpenStoreDoesNotScheduleHopBackfill(t *testing.T) {
	fastHopBackfill(t, 900)
	now := time.Now().Unix()
	s := legacyStore(t, 14, ob{`["AA"]`, now - day})
	s.WaitForAsyncMigrations()
	if hopRows(s) != 0 {
		t.Fatal("hop rows indexed before ScheduleObservationHopsBackfill; it would block the ingest gate")
	}
	s.ScheduleObservationHopsBackfill()
	s.WaitForAsyncMigrations()
	if hopRows(s) != 1 {
		t.Fatalf("explicit schedule: %d hop rows, want 1", hopRows(s))
	}
	checkHopIndex(t, s)
}

// Both shipped defaults leave the index off: no config file (zero-config
// startup) and config.example.json, which docker/entrypoint.sh copies to
// config.json on a fresh install. On the store side anything at or below 0 is
// off; a negative value never gets this far from a config file
// (TestLoadConfig_RejectsNegativeHopIndexDays).
func TestHopIndex_ShippedDefaultsAreOff(t *testing.T) {
	for _, path := range []string{"../../config.example.json", t.TempDir() + "/missing.json"} {
		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatal(err)
		}
		if d := cfg.HopIndexDaysOrZero(); d != 0 {
			t.Errorf("%s: hopIndexDays = %d, want 0 (opt-in)", path, d)
		}
	}
	s := newTestStore(t)
	restart(t, s, -1)
	if got := hopSchemaObjects(t, s); len(got) != 0 {
		t.Fatalf("hopIndexDays -1 installed %v", got)
	}
}

// A trim that fails midway (crash, disk full) must leave the mark exactly where
// its committed deletions end: readers never trust a range whose rows are gone.
func TestTrimStoppedMidwayKeepsMarkHonest(t *testing.T) {
	fastHopBackfill(t, 3600)
	now := time.Now().Unix()
	s := legacyStore(t, 3,
		ob{`["AA"]`, now - day},
		ob{`["BB"]`, now - 6*day},
		ob{`["ZZ"]`, now - 5*day}, // its chunk fails
		ob{`["CC"]`, now - 4*day},
	)
	restart(t, s, 10)
	if hopRows(s) != 4 {
		t.Fatalf("setup: %d hop rows, want 4", hopRows(s))
	}
	if _, err := s.db.Exec(`CREATE TRIGGER test_trim_fails BEFORE DELETE ON observation_hops
		WHEN OLD.hop = 'ZZ' BEGIN SELECT RAISE(ABORT, 'injected'); END`); err != nil {
		t.Fatal(err)
	}
	s.hopIndexDays = 3
	if _, err := s.TrimObservationHops(); err == nil {
		t.Fatal("trim succeeded despite the injected failure")
	}
	checkHopIndex(t, s)
	if since := hopCoverage(s); since > now-5*day {
		t.Fatalf("mark %d moved past the failed chunk at %d", since, now-5*day)
	}
	if hopRows(s) != 3 { // BB's chunk committed; ZZ, CC, AA remain
		t.Fatalf("%d hop rows after failed trim, want 3", hopRows(s))
	}
	s.db.Exec(`DROP TRIGGER test_trim_fails`)
	if _, err := s.TrimObservationHops(); err != nil {
		t.Fatal(err)
	}
	checkHopIndex(t, s)
	assertNear(t, "coverage after retry", hopCoverage(s), now-3*day)
}

// Review #2214 round 2: the read that picks a chunk runs outside the chunk's
// transaction. A row whose timestamp falls inside the range that transaction is
// about to claim can arrive in between; the trigger skips it (the mark has not
// moved yet), so the transaction itself must index it. Both the chunk around an
// observation and the final step down to the floor get such a row.
func TestBackfill_RowArrivingBeforeTheMarkMovesIsIndexed(t *testing.T) {
	fastHopBackfill(t, 3600)
	fixed := time.Now().Truncate(time.Second)
	t.Cleanup(func() { hopNow = time.Now })
	hopNow = func() time.Time { return fixed }
	now := fixed.Unix()
	floor := now - 14*day
	s := legacyStore(t, 14, ob{`["AA"]`, now - day})
	t.Cleanup(func() { hopMaintAfterRead = nil })
	calls := 0
	hopMaintAfterRead = func() {
		calls++
		switch calls {
		case 1: // the chunk below AA is about to be claimed
			insertObs(t, s, ob{`["LATE1"]`, now - day - 600})
		case 2: // nothing is left; the claim goes down to the floor
			insertObs(t, s, ob{`["LATE2"]`, floor + 100})
		}
	}
	if err := s.backfillObservationHops(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("hook fired %d times, want 2 (one chunk, then the step to the floor)", calls)
	}
	if got := hopCoverage(s); got != floor {
		t.Fatalf("coverage = %d, want the floor %d", got, floor)
	}
	checkHopIndex(t, s) // both late rows are above the mark, so both must be indexed
	if hopRows(s) != 3 {
		t.Fatalf("%d hop rows, want 3 (AA, LATE1, LATE2)", hopRows(s))
	}
}

// Review #2214 round 2: the trim reads the oldest observation outside its first
// transaction. A row with a timestamp between the mark and that observation can
// arrive in between; the trigger indexes it (it is above the mark), and a trim
// that deleted only from the observation it had found would leave those hop
// rows under the raised mark for good. Each transaction deletes from the current
// mark instead.
func TestTrim_RowArrivingBeforeTheMarkMovesIsRemoved(t *testing.T) {
	fastHopBackfill(t, 3600)
	now := time.Now().Unix()
	// BB sits a non-whole number of chunks below the new floor, so the last
	// chunk has to be clamped to the floor or the mark overshoots it.
	s := legacyStore(t, 10, ob{`["AA"]`, now - day}, ob{`["BB"]`, now - 6*day - 1234})
	restart(t, s, 10)
	if hopRows(s) != 2 {
		t.Fatalf("setup: %d hop rows, want 2", hopRows(s))
	}
	s.hopIndexDays = 3 // the window shrinks: BB falls out
	t.Cleanup(func() { hopMaintAfterRead = nil })
	fired := false
	hopMaintAfterRead = func() {
		if fired {
			return
		}
		fired = true
		insertObs(t, s, ob{`["LATE"]`, now - 8*day}) // between the mark (10 d) and BB (6 d)
		if n := hopRows(s); n != 3 {
			t.Fatalf("the trigger did not index the late row: %d hop rows", n)
		}
	}
	n, err := s.TrimObservationHops()
	if err != nil {
		t.Fatal(err)
	}
	if !fired {
		t.Fatal("hook never fired")
	}
	if n != 2 {
		t.Fatalf("trim deleted %d hop rows, want 2 (BB and LATE)", n)
	}
	assertNear(t, "coverage", hopCoverage(s), now-3*day)
	checkHopIndex(t, s) // LATE is below the mark now: no hop row may remain for it
	if hopRows(s) != 1 {
		t.Fatalf("%d hop rows, want 1 (AA)", hopRows(s))
	}
}

// The backfill stops exactly at the floor: a row at the floor second is indexed
// (and so covered), a row one second older is not, and the mark lands on the
// floor itself, not a chunk below it.
func TestBackfillObservationHops_StopsExactlyAtFloor(t *testing.T) {
	fastHopBackfill(t, 3600)
	fixed := time.Now().Truncate(time.Second)
	t.Cleanup(func() { hopNow = time.Now })
	hopNow = func() time.Time { return fixed }
	floor := fixed.Unix() - 14*day
	s := legacyStore(t, 14,
		ob{`["NEW"]`, fixed.Unix() - 10},
		ob{`["IN"]`, floor + 5000}, // more than one chunk above the floor
		ob{`["EDGE"]`, floor},
		ob{`["OUT"]`, floor - 1},
	)
	if err := s.backfillObservationHops(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := hopCoverage(s); got != floor {
		t.Fatalf("coverage = %d, want exactly the floor %d", got, floor)
	}
	checkHopIndex(t, s)
	if hopRows(s) != 3 {
		t.Fatalf("%d hop rows, want 3 (NEW, IN, EDGE)", hopRows(s))
	}
}
