package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"time"

	"github.com/meshcore-analyzer/dbschema"
)

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
	// hopBackfillAfterChunk, when set, runs after each committed backfill
	// chunk. Tests only.
	hopBackfillAfterChunk func()
	// hopMaintAfterRead, when set, runs after the backfill or the trim has read
	// observations to pick its next chunk and before the transaction that moves
	// the coverage mark. Tests only: a row inserted here is what those
	// transactions must still account for.
	hopMaintAfterRead func()
	// hopNow is the clock the index window is measured from; tests pin it.
	hopNow = time.Now
)

// ConfigureObservationHops applies retention.hopIndexDays. Called once from
// main's startup block, after the MQTT subscription is up and before the ingest
// gate opens: removing the index held the writer for 45 to 71 s on a copy of a
// 10.5M-observation database, and run there that time is buffered ingest, not
// lost packets. It precedes the startup trim and the backfill.
//
// days == 0 (the default; LoadConfig rejects negative values) removes the index
// if a previous run installed it, so an upgrade that never sets the key changes
// nothing, and switching it off leaves no table and no triggers behind.
// days > 0 installs the index (or refreshes its triggers);
// ScheduleObservationHopsBackfill then fills whatever part of the window is not
// yet covered.
func (s *Store) ConfigureObservationHops(days int) error {
	s.hopIndexDays = days
	if days <= 0 {
		start := time.Now()
		removed, err := dbschema.DropObservationHopsSchema(s.db)
		if err != nil {
			return err
		}
		if removed {
			log.Printf("[hop-index] off (retention.hopIndexDays unset or 0): removed observation_hops and its triggers in %s", time.Since(start).Round(time.Millisecond))
		}
		return nil
	}
	if err := dbschema.EnsureObservationHopsSchema(s.db, log.Printf); err != nil {
		return err
	}
	since, err := s.hopCoverage()
	if err != nil {
		return err
	}
	log.Printf("[hop-index] on: window %d days, covered since %s", days, time.Unix(since, 0).UTC().Format(time.RFC3339))
	return nil
}

func (s *Store) hopCoverage() (int64, error) {
	var since int64
	err := s.db.QueryRow(`SELECT since FROM ` + dbschema.ObservationHopsCoverageTable + ` WHERE id = 1`).Scan(&since)
	if err != nil {
		return 0, fmt.Errorf("read hop coverage: %w", err)
	}
	return since, nil
}

func (s *Store) hopFloor() int64 {
	return hopNow().Unix() - int64(s.hopIndexDays)*86400
}

// ScheduleObservationHopsBackfill starts the backfill in the background when the
// index is on and its coverage does not yet reach back hopIndexDays: on first
// install, after an interrupted run, and after the window was raised. It must
// be called AFTER the ingest buffer is opened: main gates ingest on
// WaitForAsyncMigrations(), and a backfill that long (15 minutes on a 13GB
// database) scheduled earlier keeps live ingest dark the whole time. Run after
// the gate, it shares the writer with ingest one short chunk at a time. Close()
// cancels it; it resumes from the stored coverage mark on the next boot.
func (s *Store) ScheduleObservationHopsBackfill() {
	if s.hopIndexDays <= 0 {
		return
	}
	since, err := s.hopCoverage()
	if err != nil {
		log.Printf("[hop-index] backfill not scheduled: %v", err)
		return
	}
	if since <= s.hopFloor() {
		return // the whole window is covered; the daily trim keeps it that way
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.hopBackfillCancel = cancel
	s.backfillWg.Add(1)
	go func() {
		defer s.backfillWg.Done()
		log.Printf("[hop-index] backfilling observation_hops from %s back to %d days (newest first)",
			time.Unix(since, 0).UTC().Format(time.RFC3339), s.hopIndexDays)
		if err := s.backfillObservationHops(ctx); err != nil {
			log.Printf("[hop-index] backfill stopped, resumes on next start: %v", err)
		}
	}()
}

// backfillObservationHops indexes existing observations into observation_hops,
// newest first, lowering observation_hops_coverage.since as each chunk commits
// so readers can use the index for any window the backfill has already covered
// while it is still running.
//
// Idempotent and resumable: it continues from the stored coverage mark, and the
// inserts are OR IGNORE (the insert trigger may already have indexed a row that
// arrived late with an old timestamp).
//
// Each transaction indexes every second it moves the mark across, not only the
// chunk around the observation it found: the read that picks the chunk runs
// outside the transaction, and a row with a timestamp in that range can arrive
// in between, too old for the trigger (the mark has not moved yet). Covering the
// whole range makes the mark's claim true whatever arrived meanwhile. The same
// rule applies to the last step, which claims down to the floor.
//
// It holds hopMaintMu for its whole run, so a trim cannot interleave with it.
// Without that, a trim that raised the mark and deleted rows between two chunks
// would be undone by the next chunk lowering the mark again, and readers would
// trust a range whose rows the trim had deleted.
func (s *Store) backfillObservationHops(ctx context.Context) error {
	s.hopMaintMu.Lock()
	defer s.hopMaintMu.Unlock()
	since, err := s.hopCoverage()
	if err != nil {
		return err
	}
	start := time.Now()
	var rows int64
	// Only the rolling window is indexed. since never drops below the floor, so
	// the coverage mark honestly says "indexed from here".
	floor := s.hopFloor()
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
		// Cover [lower, since): the chunk below the newest observation found, or
		// exactly down to the floor when nothing is left inside the window. The
		// INSERT runs over that whole range, so the mark never claims a second
		// the transaction did not index (see the function comment).
		lower := floor
		if next.Valid && next.Int64 >= floor {
			lower = next.Int64 - hopBackfillChunkSeconds + 1
			if lower < floor {
				lower = floor
			}
		}
		if hopMaintAfterRead != nil {
			hopMaintAfterRead()
		}
		var n int64
		err := s.WriterTx("observation_hops_backfill", func(tx *sql.Tx) error {
			var err error
			if n, err = dbschema.IndexObservationHops(tx, lower, since); err != nil {
				return err
			}
			_, err = tx.Exec(`UPDATE `+dbschema.ObservationHopsCoverageTable+` SET since = ? WHERE id = 1`, lower)
			return err
		})
		if err != nil {
			return fmt.Errorf("backfill chunk [%d,%d): %w", lower, since, err)
		}
		rows += n
		since = lower
		if hopBackfillAfterChunk != nil {
			hopBackfillAfterChunk()
		}
		if since <= floor {
			break
		}
		// Yield between chunks, not after the last one.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(hopBackfillPause):
		}
	}
	log.Printf("[hop-index] backfill complete: %d rows in %s", rows, time.Since(start).Round(time.Second))
	return nil
}

// TrimObservationHops slides the index window forward: it deletes the hop rows
// of the observations below now-hopIndexDays, one chunk per writer
// transaction, raising the coverage mark to the chunk's upper bound in the same
// transaction. Each chunk commits or rolls back whole, so a trim that stops
// midway leaves the mark exactly where its deletions end and the next trim
// continues from there. It waits for a running backfill (hopMaintMu). Returns
// hop rows deleted.
//
// Every transaction deletes from the current mark up to the new one, not only
// from the oldest observation it found: that read runs outside the transaction,
// and a row with a timestamp between the mark and that observation can arrive
// in between, indexed by the trigger because it is above the mark. Deleting the
// whole range the mark moves across removes it too, instead of leaving its hop
// rows under the raised mark where nothing would ever delete them.
func (s *Store) TrimObservationHops() (int64, error) {
	if s.hopIndexDays <= 0 {
		return 0, nil
	}
	s.hopMaintMu.Lock()
	defer s.hopMaintMu.Unlock()
	floor := s.hopFloor()
	since, err := s.hopCoverage()
	if err != nil {
		return 0, err
	}
	// The backfill has not reached the floor yet (it lowers the mark down to
	// it on its next run); there is nothing below the window to remove.
	if since >= floor {
		return 0, nil
	}
	// Size the chunks from the oldest observation at or above the mark, not
	// from a possibly tiny mark: stepping from timestamp 0 in 15-minute chunks
	// would take hundreds of thousands of empty transactions.
	var minTS sql.NullInt64
	if err := s.db.QueryRow(`SELECT MIN(timestamp) FROM observations WHERE timestamp >= ?`, since).Scan(&minTS); err != nil {
		return 0, fmt.Errorf("min observation timestamp: %w", err)
	}
	if hopMaintAfterRead != nil {
		hopMaintAfterRead()
	}
	// Nothing indexed below the window: one transaction moves the mark to the
	// floor (and still deletes across the range, in case a row arrived).
	a := floor
	if minTS.Valid && minTS.Int64 < floor {
		a = minTS.Int64
	}
	var total int64
	mark := since
	for mark < floor {
		b := a + hopBackfillChunkSeconds
		if b > floor {
			b = floor
		}
		var n int64
		err := s.WriterTx("observation_hops_trim", func(tx *sql.Tx) error {
			if _, err := tx.Exec(`UPDATE `+dbschema.ObservationHopsCoverageTable+` SET since = ? WHERE id = 1`, b); err != nil {
				return err
			}
			var err error
			n, err = dbschema.UnindexObservationHops(tx, mark, b)
			return err
		})
		if err != nil {
			return total, fmt.Errorf("trim chunk [%d,%d): %w", mark, b, err)
		}
		total += n
		mark, a = b, b
	}
	return total, nil
}
