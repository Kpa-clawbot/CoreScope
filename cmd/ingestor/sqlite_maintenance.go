package main

import (
	"fmt"
	"github.com/meshcore-analyzer/dbconfig"
	"log"
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
