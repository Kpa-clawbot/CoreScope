package main

import (
	"fmt"
	"github.com/meshcore-analyzer/dbconfig"
	"log"
	"os"
	"strings"
	"time"
)

func (s *Store) CheckAutoVacuum(cfg *Config) {
	if s.Backend() != dbconfig.SQLite {
		return
	}

	var autoVacuum int
	if err := s.db.QueryRow("PRAGMA auto_vacuum").Scan(&autoVacuum); err != nil {
		log.Printf("[db] warning: could not read auto_vacuum: %v", err)
		return
	}

	if autoVacuum == 2 {
		log.Printf("[db] auto_vacuum=INCREMENTAL")
		return
	}

	modes := map[int]string{0: "NONE", 1: "FULL", 2: "INCREMENTAL"}
	mode := modes[autoVacuum]
	if mode == "" {
		mode = fmt.Sprintf("UNKNOWN(%d)", autoVacuum)
	}

	log.Printf("[db] auto_vacuum=%s — DB needs one-time VACUUM to enable incremental auto-vacuum. "+
		"Set db.vacuumOnStartup: true in config to migrate (will block startup for several minutes on large DBs). "+
		"See https://github.com/Kpa-clawbot/CoreScope/issues/919", mode)

	if cfg.DB != nil && cfg.DB.VacuumOnStartup {
		// WARNING: Full VACUUM creates a temporary copy of the entire DB file.
		// Requires ~2× the DB file size in free disk space or it will fail.
		log.Printf("[db] vacuumOnStartup=true — starting one-time full VACUUM (ensure 2x DB size free disk space)...")
		start := time.Now()

		if _, err := s.instrumentedExec("vacuum", "PRAGMA auto_vacuum = INCREMENTAL"); err != nil {
			log.Printf("[db] VACUUM failed: could not set auto_vacuum: %v", err)
			return
		}
		if _, err := s.instrumentedExec("vacuum", "VACUUM"); err != nil {
			log.Printf("[db] VACUUM failed: %v", err)
			return
		}

		elapsed := time.Since(start)
		log.Printf("[db] VACUUM complete in %v — auto_vacuum is now INCREMENTAL", elapsed.Round(time.Millisecond))
	}
}

func (s *Store) RunIncrementalVacuum(pages int) {
	if s.Backend() != dbconfig.SQLite {
		return
	}

	// Tagged for /api/perf writer-lock visibility (#1340).
	if _, err := s.instrumentedExec("vacuum", fmt.Sprintf("PRAGMA incremental_vacuum(%d)", pages)); err != nil {
		log.Printf("[vacuum] incremental_vacuum error: %v", err)
	}
}

func (s *Store) RefreshPlannerStats(analysisLimit int) bool {
	if s.Backend() != dbconfig.SQLite {
		return false
	}

	if analysisLimit < 0 {
		return false
	}
	first := !s.hasPlannerStats()
	start := time.Now()
	// Tagged for /api/perf writer-lock visibility (#1340).
	if _, err := s.instrumentedExec("analyze", fmt.Sprintf("PRAGMA analysis_limit=%d", analysisLimit)); err != nil {
		log.Printf("[analyze] could not set analysis_limit: %v", err)
		return false
	}
	if _, err := s.instrumentedExec("analyze", "ANALYZE"); err != nil {
		log.Printf("[analyze] ANALYZE failed: %v", err)
		return false
	}
	elapsed := time.Since(start).Round(time.Millisecond)
	s.recordPlannerStatsRefresh(time.Now())
	if first {
		log.Printf("[analyze] planner statistics built in %v (analysis_limit=%d, first run against this database)", elapsed, analysisLimit)
	} else {
		log.Printf("[analyze] planner statistics refreshed in %v (analysis_limit=%d)", elapsed, analysisLimit)
	}
	return true
}

func (s *Store) EnsurePlannerStats(analysisLimit int) bool {
	if s.Backend() != dbconfig.SQLite {
		return false
	}

	if s.hasPlannerStats() {
		return false
	}
	log.Printf("[analyze] this database has no planner statistics; building them now. " +
		"ANALYZE holds the single write connection until it finishes (3m43.9s measured on 9.4 GB, cold), " +
		"so ingest will buffer and catch up. Once per database, not once per restart.")
	return s.RefreshPlannerStats(analysisLimit)
}

func (s *Store) hasPlannerStats() bool {
	if s.Backend() != dbconfig.SQLite {
		return false
	}

	var n int
	if err := s.db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='sqlite_stat1'`).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

func (s *Store) Checkpoint() int {
	if s.Backend() != dbconfig.SQLite {
		return 0
	}
	waitStart := time.Now()
	writerMu.Lock()
	wait := time.Since(waitStart)
	holdStart := time.Now()
	defer func() { writerMu.Unlock(); recordWriterTiming("checkpoint", wait, time.Since(holdStart), "Checkpoint") }()

	var busy, walFrames, checkpointed int
	if err := s.db.QueryRow("PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &walFrames, &checkpointed); err != nil {
		log.Printf("[db] WAL checkpoint error: %v", err)
		return 0
	}
	if walFrames > 0 {
		log.Printf("[db] WAL checkpoint: %d/%d frames checkpointed (blocked=%v)", checkpointed, walFrames, busy != 0)
	}
	return checkpointed
}

// plannerStatsInterval is how often the planner statistics are refreshed, and
// plannerStatsStagger how long after startup the first refresh waits at least.
const (
	plannerStatsInterval = 24 * time.Hour
	plannerStatsStagger  = 2 * time.Minute
)

// nextPlannerStatsRefresh returns how long to wait before the next refresh,
// given when the previous one succeeded (zero if never): until it is
// plannerStatsInterval old, but at least plannerStatsStagger and at most
// plannerStatsInterval.
//
// Before #2146 the first refresh ran plannerStatsStagger after every start.
// On a cold page cache that ANALYZE took 6 to 9 minutes on a 10-11 GB
// database, holding the single write connection, so each restart stalled
// ingest that long for statistics that were hours old at most.
func nextPlannerStatsRefresh(last, now time.Time) time.Duration {
	if last.IsZero() {
		return plannerStatsStagger
	}
	d := last.Add(plannerStatsInterval).Sub(now)
	if d < plannerStatsStagger {
		return plannerStatsStagger
	}
	if d > plannerStatsInterval {
		return plannerStatsInterval
	}
	return d
}

// plannerStatsStampPath is the file next to the database that records when
// the planner statistics were last refreshed.
func (s *Store) plannerStatsStampPath() string {
	return s.path + ".planner-stats-refreshed"
}

// lastPlannerStatsRefresh returns when RefreshPlannerStats last succeeded on
// this database, or the zero time when that is not known: the later of the
// time this process remembers and the time recorded in the file.
func (s *Store) lastPlannerStatsRefresh() time.Time {
	var last time.Time
	if ns := s.plannerStatsAt.Load(); ns != 0 {
		last = time.Unix(0, ns)
	}
	b, err := os.ReadFile(s.plannerStatsStampPath())
	if err != nil {
		return last
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b)))
	if err != nil || t.Before(last) {
		return last
	}
	return t
}

// recordPlannerStatsRefresh remembers when the statistics were refreshed and
// writes it next to the database for the next start. If the write fails, this
// process still waits the full interval; only the first refresh after the
// next restart comes early.
func (s *Store) recordPlannerStatsRefresh(at time.Time) {
	s.plannerStatsAt.Store(at.UnixNano())
	tmp := s.plannerStatsStampPath() + ".tmp"
	if err := os.WriteFile(tmp, []byte(at.UTC().Format(time.RFC3339)), 0o644); err != nil {
		log.Printf("[analyze] could not record the refresh time: %v", err)
		return
	}
	if err := os.Rename(tmp, s.plannerStatsStampPath()); err != nil {
		log.Printf("[analyze] could not record the refresh time: %v", err)
	}
}
