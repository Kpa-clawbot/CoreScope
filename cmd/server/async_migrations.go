// async_migrations.go — server-side reader for the ingestor's
// _async_migrations bookkeeping table (#1724).
//
// The ingestor records every long-running schema/data migration here:
//
//   CREATE TABLE _async_migrations (
//     name           TEXT PRIMARY KEY,
//     status         TEXT NOT NULL,             -- pending_async | done | failed
//     started_at     TEXT NOT NULL,
//     ended_at       TEXT,
//     error          TEXT,
//     rows_processed INTEGER NOT NULL DEFAULT 0, -- #1724
//     rows_total     INTEGER NOT NULL DEFAULT 0, -- #1724
//     last_update_at TEXT                        -- #1724
//   );
//
// The server reads this table (read-only) so /api/perf and the
// /api/healthz warm-up banner can surface mid-flight backfill state.
// Without it operators upgrading to v3.9.2 see a black-box pause while
// tx_last_seen_backfill_v1 runs and can't tell "still backfilling"
// from "real bug" (#1724 root cause).

package main

import (
	"database/sql"
	"strings"
	"time"
)

// AsyncMigrationInfo is the JSON shape served on /api/perf.asyncMigrations
// (and embedded in /api/healthz). Field names are stable.
type AsyncMigrationInfo struct {
	Name          string  `json:"name"`
	Status        string  `json:"status"` // running | complete | failed
	RowsProcessed int64   `json:"rowsProcessed"`
	RowsTotal     int64   `json:"rowsTotal"`
	Rate          float64 `json:"rate"`           // rows / sec
	EtaSeconds    int64   `json:"etaSeconds"`     // 0 when complete or total unknown
	ElapsedSec    int64   `json:"elapsedSeconds"` // wall time since started_at
	ErrorMessage  string  `json:"errorMessage,omitempty"`
}

// IsRunning reports whether the migration is still in progress.
func (a AsyncMigrationInfo) IsRunning() bool { return a.Status == "running" }

// readAsyncMigrations returns the current state of every async migration
// the ingestor has registered. Returns nil when the table doesn't exist
// (fresh DB on first server boot before the ingestor ran) — caller must
// treat nil as "no migrations to report" rather than an error.
func readAsyncMigrations(db *sql.DB) []AsyncMigrationInfo {
	if db == nil {
		return nil
	}
	// Use a tolerant SELECT — older ingestor builds may not have the
	// rows_processed columns yet; COALESCE every additive field.
	rows, err := db.Query(`
		SELECT name, status,
		       COALESCE(rows_processed, 0),
		       COALESCE(rows_total, 0),
		       started_at,
		       COALESCE(ended_at, ''),
		       COALESCE(error, '')
		FROM _async_migrations
		ORDER BY started_at`)
	if err != nil {
		// Table missing or columns missing → silent empty.
		return nil
	}
	defer rows.Close()
	var out []AsyncMigrationInfo
	now := time.Now()
	for rows.Next() {
		var (
			info      AsyncMigrationInfo
			startedAt string
			endedAt   string
			errMsg    string
			rawStatus string
		)
		if err := rows.Scan(&info.Name, &rawStatus, &info.RowsProcessed, &info.RowsTotal, &startedAt, &endedAt, &errMsg); err != nil {
			continue
		}
		info.Status = mapAsyncStatus(rawStatus)
		if errMsg != "" {
			info.ErrorMessage = errMsg
		}

		started, _ := parseAsyncTime(startedAt)
		end := now
		if endedAt != "" {
			if t, ok := parseAsyncTime(endedAt); ok {
				end = t
			}
		}
		if !started.IsZero() {
			elapsed := end.Sub(started).Seconds()
			if elapsed < 0 {
				elapsed = 0
			}
			info.ElapsedSec = int64(elapsed)
			if info.IsRunning() && elapsed > 0 && info.RowsProcessed > 0 {
				info.Rate = float64(info.RowsProcessed) / elapsed
				if info.RowsTotal > info.RowsProcessed && info.Rate > 0 {
					info.EtaSeconds = int64(float64(info.RowsTotal-info.RowsProcessed) / info.Rate)
				}
			}
		}
		out = append(out, info)
	}
	return out
}

// mapAsyncStatus normalizes the bookkeeping `status` column to the
// API-stable vocabulary {running, complete, failed}.
func mapAsyncStatus(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "done":
		return "complete"
	case "failed":
		return "failed"
	default:
		return "running"
	}
}

// parseAsyncTime accepts both the SQLite default datetime('now')
// format ("2006-01-02 15:04:05") and ISO-8601, returning the parsed
// UTC time and ok=true on success.
func parseAsyncTime(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		time.RFC3339,
		time.RFC3339Nano,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// anyAsyncMigrationRunning returns true when any registered async
// migration is still in progress. Used to suppress
// backgroundLoadComplete=true on /api/healthz while a backfill is
// active (#1724 acceptance criterion #4 — warm-up banner must stay
// up + analytics 503 must keep returning Retry-After).
func anyAsyncMigrationRunning(migrations []AsyncMigrationInfo) bool {
	for _, m := range migrations {
		if m.IsRunning() {
			return true
		}
	}
	return false
}
