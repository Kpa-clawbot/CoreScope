package main

import (
	"fmt"
	"log"
	"time"
)

// checkAutoVacuum inspects the current auto_vacuum mode and logs a warning
// if it's not INCREMENTAL. Optionally performs a one-time full VACUUM if
// the operator has set db.vacuumOnStartup: true in config (#919).
func checkAutoVacuum(db *DB, cfg *Config, dbPath string) {
	var autoVacuum int
	if err := db.conn.QueryRow("PRAGMA auto_vacuum").Scan(&autoVacuum); err != nil {
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

		rw, err := openRW(dbPath)
		if err != nil {
			log.Printf("[db] VACUUM failed: could not open RW connection: %v", err)
			return
		}
		defer rw.Close()

		if _, err := rw.Exec("PRAGMA auto_vacuum = INCREMENTAL"); err != nil {
			log.Printf("[db] VACUUM failed: could not set auto_vacuum: %v", err)
			return
		}
		if _, err := rw.Exec("VACUUM"); err != nil {
			log.Printf("[db] VACUUM failed: %v", err)
			return
		}

		elapsed := time.Since(start)
		log.Printf("[db] VACUUM complete in %v — auto_vacuum is now INCREMENTAL", elapsed.Round(time.Millisecond))

		// Re-check
		var newMode int
		if err := db.conn.QueryRow("PRAGMA auto_vacuum").Scan(&newMode); err == nil {
			if newMode == 2 {
				log.Printf("[db] auto_vacuum=INCREMENTAL (confirmed after VACUUM)")
			} else {
				log.Printf("[db] warning: auto_vacuum=%d after VACUUM — expected 2", newMode)
			}
		}
	}
}

// runIncrementalVacuum runs PRAGMA incremental_vacuum(N) on a read-write
// connection. Safe to call on auto_vacuum=NONE databases (noop).
func runIncrementalVacuum(dbPath string, pages int) {
	rw, err := openRW(dbPath)
	if err != nil {
		log.Printf("[vacuum] could not open RW connection: %v", err)
		return
	}
	defer rw.Close()

	if _, err := rw.Exec(fmt.Sprintf("PRAGMA incremental_vacuum(%d)", pages)); err != nil {
		log.Printf("[vacuum] incremental_vacuum error: %v", err)
	}
}
