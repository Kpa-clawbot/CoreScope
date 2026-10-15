package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/meshcore-analyzer/dbschema"
)

// defaultHopIndexDays is the rolling window the hop index covers. 14 holds the
// UI's 7-day default with margin; ~330 bytes/observation puts it near 3GB on a
// 650k-observations/day instance. hopIndexDays is set from
// retention.hopIndexDays before the store opens.
const defaultHopIndexDays = 14

var hopIndexDays = defaultHopIndexDays

// Chunk size and pacing for the observation_hops backfill. vars so tests can
// run it without sleeping.
var (
	// hopBackfillChunkSeconds is the timestamp span indexed per writer
	// transaction. 15 minutes is ~7k observations (~80k hop rows) on a busy
	// mesh: on a small EBS-backed host an hour-sized chunk held the writer for
	// 2-7s, which stalls live MQTT ingest behind it (writerMu, see WriterTx).
	hopBackfillChunkSeconds int64 = 900
	// hopBackfillPause is the idle gap between chunks. The backfill reads the
	// whole observations table once; on a box whose page cache is far smaller
	// than the DB that is real I/O contention, so it yields between chunks.
	hopBackfillPause = 250 * time.Millisecond
)

// backfillObservationHops indexes existing observations into observation_hops,
// newest first, lowering observation_hops_coverage.since as each chunk commits
// so readers can use the index for any window the backfill has already covered
// while it is still running.
//
// Idempotent and resumable: it continues from the stored coverage mark, and the
// inserts are OR IGNORE (the insert trigger may already have indexed a row that
// arrived late with an old timestamp).
// ScheduleObservationHopsBackfill starts the backfill as a tracked async
// migration. It must be called AFTER the ingest buffer is opened: main gates
// ingest on WaitForAsyncMigrations(), and a backfill that long (15 minutes on a
// 13GB database) scheduled earlier keeps live ingest dark the whole time. Run
// after the gate, it shares the writer with ingest one short chunk at a time.
// Close() cancels it; it resumes from the stored coverage mark on next boot.
func (s *Store) ScheduleObservationHopsBackfill() {
	ctx, cancel := context.WithCancel(context.Background())
	s.hopBackfillCancel = cancel
	if err := s.RunAsyncMigration(ctx, "observation_hops_backfill_v1",
		func(ctx context.Context, _ *sql.DB) error {
			log.Println("[migration/async] Backfilling observation_hops from observations.path_json (newest first)...")
			return s.backfillObservationHops(ctx)
		}); err != nil {
		log.Printf("[migration/async] scheduling observation_hops_backfill_v1 failed: %v", err)
	}
}

func (s *Store) backfillObservationHops(ctx context.Context) error {
	var since int64
	if err := s.db.QueryRow(`SELECT since FROM ` + dbschema.ObservationHopsCoverageTable + ` WHERE id = 1`).Scan(&since); err != nil {
		return fmt.Errorf("read hop coverage: %w", err)
	}
	start := time.Now()
	var rows int64
	// Only the rolling window is indexed. since never drops below the floor, so
	// the coverage mark honestly says "indexed from here".
	floor := time.Now().Unix() - int64(hopIndexDays)*86400
	for since > floor {
		if err := ctx.Err(); err != nil {
			return err
		}
		// Jump straight to the newest observation below the coverage mark
		// instead of stepping a fixed span: a bad-clock observer can leave rows
		// at timestamp 0 or in 1970, and walking decades of empty hourly
		// chunks would take days. timestamp is indexed, so this is a seek.
		var next sql.NullInt64
		if err := s.db.QueryRow(`SELECT MAX(timestamp) FROM observations WHERE timestamp < ?`, since).Scan(&next); err != nil {
			return fmt.Errorf("next observation below %d: %w", since, err)
		}
		if !next.Valid || next.Int64 < floor {
			// Nothing left inside the window: claim exactly the floor.
			return s.finishHopBackfill(floor, rows, start)
		}
		// Cover [lower, since). Nothing exists in [next+1, since), so lowering
		// the mark across that gap claims nothing false.
		lower := next.Int64 - hopBackfillChunkSeconds + 1
		if lower < floor {
			lower = floor
		}
		var n int64
		err := s.WriterTx("observation_hops_backfill", func(tx *sql.Tx) error {
			res, err := tx.Exec(`
				INSERT OR IGNORE INTO observation_hops (hop, timestamp, observation_id)
				SELECT DISTINCT j.value, o.timestamp, o.id
				FROM observations o, json_each(o.path_json) j
				WHERE o.timestamp >= ? AND o.timestamp < ?
				  AND json_valid(o.path_json) AND j.type = 'text'`, lower, since)
			if err != nil {
				return err
			}
			n, _ = res.RowsAffected()
			_, err = tx.Exec(`UPDATE `+dbschema.ObservationHopsCoverageTable+` SET since = ? WHERE id = 1`, lower)
			return err
		})
		if err != nil {
			return fmt.Errorf("backfill chunk [%d,%d): %w", lower, since, err)
		}
		rows += n
		since = lower
		// Yield between chunks, not after the last one.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(hopBackfillPause):
		}
	}
	return s.finishHopBackfill(floor, rows, start)
}

func (s *Store) finishHopBackfill(floor, rows int64, start time.Time) error {
	// Never raise the mark here: a concurrent trim may already have moved it up.
	if _, err := s.db.Exec(`UPDATE `+dbschema.ObservationHopsCoverageTable+` SET since = MIN(since, ?) WHERE id = 1`, floor); err != nil {
		return err
	}
	log.Printf("[migration/async] observation_hops backfill complete: %d rows in %s", rows, time.Since(start).Round(time.Second))
	return nil
}

// TrimObservationHops slides the index window forward: it raises the coverage
// mark to now-hopIndexDays FIRST (so readers stop trusting the old range), then
// deletes the hop rows of the observations that fell out, by primary key, one
// chunk per transaction. If it dies midway the leftover rows sit below the mark
// where nothing reads them, and the observation delete trigger or the next trim
// removes them. Returns hop rows deleted.
func (s *Store) TrimObservationHops() (int64, error) {
	floor := time.Now().Unix() - int64(hopIndexDays)*86400
	var since int64
	if err := s.db.QueryRow(`SELECT since FROM ` + dbschema.ObservationHopsCoverageTable + ` WHERE id = 1`).Scan(&since); err != nil {
		return 0, fmt.Errorf("read hop coverage: %w", err)
	}
	// A backfill is still lowering the mark toward the floor; trimming now would
	// fight it. It re-trims on the next tick.
	if since >= floor {
		return 0, nil
	}
	// Start at the oldest observation, not at a possibly tiny mark: stepping
	// from timestamp 0 in hourly chunks would take hundreds of thousands of
	// empty transactions.
	var minTS sql.NullInt64
	if err := s.db.QueryRow(`SELECT MIN(timestamp) FROM observations`).Scan(&minTS); err != nil {
		return 0, fmt.Errorf("min observation timestamp: %w", err)
	}
	if minTS.Valid && minTS.Int64 > since {
		since = minTS.Int64
	}
	var total int64
	for a := since; a < floor; a += hopBackfillChunkSeconds {
		b := a + hopBackfillChunkSeconds
		if b > floor {
			b = floor
		}
		var n int64
		err := s.WriterTx("observation_hops_trim", func(tx *sql.Tx) error {
			if _, err := tx.Exec(`UPDATE `+dbschema.ObservationHopsCoverageTable+` SET since = ? WHERE id = 1`, b); err != nil {
				return err
			}
			res, err := tx.Exec(`
				DELETE FROM observation_hops WHERE (hop, timestamp, observation_id) IN (
					SELECT j.value, o.timestamp, o.id
					FROM observations o, json_each(o.path_json) j
					WHERE o.timestamp >= ? AND o.timestamp < ?
					  AND json_valid(o.path_json) AND j.type = 'text')`, a, b)
			if err != nil {
				return err
			}
			n, _ = res.RowsAffected()
			return nil
		})
		if err != nil {
			return total, fmt.Errorf("trim chunk [%d,%d): %w", a, b, err)
		}
		total += n
	}
	return total, nil
}

func (s *Store) setHopCoverage(since int64) error {
	return s.WriterTx("observation_hops_backfill", func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE `+dbschema.ObservationHopsCoverageTable+` SET since = ? WHERE id = 1`, since)
		return err
	})
}
