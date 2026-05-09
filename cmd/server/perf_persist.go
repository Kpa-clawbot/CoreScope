package main

import (
	"encoding/json"
	"log"
	"time"
)

const perfPersistMaxAge = 49 * time.Hour // keep a 1-hour buffer beyond the 48 h ring

// ensurePerfHistoryTable creates the perf_history table if it does not exist.
// Uses the same cached RW connection as neighbour-persist and vacuum operations.
func ensurePerfHistoryTable(dbPath string) error {
	rw, err := cachedRW(dbPath)
	if err != nil {
		return err
	}
	_, err = rw.Exec(`CREATE TABLE IF NOT EXISTS perf_history (
		ts     INTEGER PRIMARY KEY,
		sample TEXT    NOT NULL
	)`)
	return err
}

// loadPerfHistoryFromDB reads persisted samples from SQLite, ordered oldest-first,
// capped at the ring-buffer maximum of 2880 entries (48 h at 1-min resolution).
func loadPerfHistoryFromDB(dbPath string) []PerfSample {
	rw, err := cachedRW(dbPath)
	if err != nil {
		log.Printf("[perf] persist: open rw for load: %v", err)
		return nil
	}

	cutoff := time.Now().Add(-48 * time.Hour).UnixMilli()
	rows, err := rw.Query(
		`SELECT sample FROM perf_history WHERE ts > ? ORDER BY ts ASC LIMIT 2880`,
		cutoff,
	)
	if err != nil {
		log.Printf("[perf] persist: load query: %v", err)
		return nil
	}
	defer rows.Close()

	var out []PerfSample
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			continue
		}
		var s PerfSample
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			continue
		}
		out = append(out, s)
	}
	return out
}

// asyncSavePerfSample persists one sample to SQLite and prunes rows older than
// perfPersistMaxAge.  Runs in a fire-and-forget goroutine; errors are logged but
// never propagated — persistence is best-effort and must not affect the hot path.
func asyncSavePerfSample(dbPath string, sample PerfSample) {
	go func() {
		rw, err := cachedRW(dbPath)
		if err != nil {
			log.Printf("[perf] persist: open rw: %v", err)
			return
		}

		raw, err := json.Marshal(sample)
		if err != nil {
			return
		}

		cutoff := time.Now().Add(-perfPersistMaxAge).UnixMilli()

		tx, err := rw.Begin()
		if err != nil {
			log.Printf("[perf] persist: begin tx: %v", err)
			return
		}
		if _, err := tx.Exec(
			`INSERT OR REPLACE INTO perf_history (ts, sample) VALUES (?, ?)`,
			sample.Ts, string(raw),
		); err != nil {
			tx.Rollback() //nolint:errcheck
			log.Printf("[perf] persist: insert: %v", err)
			return
		}
		if _, err := tx.Exec(`DELETE FROM perf_history WHERE ts < ?`, cutoff); err != nil {
			tx.Rollback() //nolint:errcheck
			log.Printf("[perf] persist: prune: %v", err)
			return
		}
		if err := tx.Commit(); err != nil {
			log.Printf("[perf] persist: commit: %v", err)
		}
	}()
}
