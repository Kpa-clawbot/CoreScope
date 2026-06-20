// Read-only surface for async migration status (#1724).
//
// The ingestor writes _async_migrations (status, rows_processed, rows_total,
// last_update_at, error). The server READS this table to surface progress
// via /api/healthz (so the warm-up banner can stay visible while a long
// backfill runs) and /api/perf (so operators see per-migration progress
// + ETA + error message).
//
// Reads go through SetMaxOpenConns(1)? No — cmd/server/db.go uses
// SetMaxOpenConns(4) in mode=ro, but the underlying SQLite file's writer
// is single-threaded (the ingestor). To avoid every /api/healthz request
// hitting the disk while a migration is mid-batch, we cache the result
// for asyncMigrationsTTL.

package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const asyncMigrationsTTL = 5 * time.Second

// asyncMigrationsSF collapses concurrent /api/healthz + /api/perf calls
// during a cache miss into a single DB read. Errors are not cached and
// each caller gets the same error on a shared in-flight read.
var asyncMigrationsSF singleflight.Group

// AsyncMigrationInfo is the JSON shape returned via /api/perf and embedded
// in /api/healthz.
type AsyncMigrationInfo struct {
	Name           string  `json:"name"`
	Status         string  `json:"status"` // "running" | "done" | "failed" | "unknown"
	StartedAt      string  `json:"startedAt,omitempty"`
	EndedAt        string  `json:"endedAt,omitempty"`
	LastUpdateAt   string  `json:"lastUpdateAt,omitempty"`
	RowsProcessed  int64   `json:"rowsProcessed"`
	RowsTotal      int64   `json:"rowsTotal"`
	ElapsedSec     float64 `json:"elapsedSec"`
	EtaSec         float64 `json:"etaSec"`         // only meaningful when status="running"
	RatePerSec     float64 `json:"ratePerSec"`     // only meaningful when status="running"
	ErrorMessage   string  `json:"errorMessage,omitempty"`
}

// asyncMigrationsCache caches the latest successful readAsyncMigrationsRaw
// result. Errors are NOT cached (#1735 finding #4 / Group C): every error
// path retries on the next call so transient I/O failures don't get
// pinned for asyncMigrationsTTL.
var (
	asyncMigrationsCacheMu sync.Mutex
	asyncMigrationsCacheAt time.Time
	asyncMigrationsCached  []AsyncMigrationInfo
)

// asyncMigrationsNow is overridable for tests.
var asyncMigrationsNow = time.Now

// readAsyncMigrations returns the current set of async migration info,
// using a short TTL cache to avoid hammering the writer-held DB on hot
// paths like /api/healthz.
//
// Concurrency contract (#1735 finding #3 / Group C):
//   - Cache mutex is NEVER held across db.Query — only across the
//     check/populate steps. The actual I/O runs through singleflight so
//     concurrent callers during a cache miss share one DB read.
//   - Errors are NOT cached (#1735 finding #4): a transient query failure
//     does not pin healthz/perf at "empty" for asyncMigrationsTTL.
func readAsyncMigrations(db *sql.DB) ([]AsyncMigrationInfo, error) {
	// Step 1: cache hit under lock, release before any I/O.
	asyncMigrationsCacheMu.Lock()
	if !asyncMigrationsCacheAt.IsZero() &&
		asyncMigrationsNow().Sub(asyncMigrationsCacheAt) < asyncMigrationsTTL {
		cached := asyncMigrationsCached
		asyncMigrationsCacheMu.Unlock()
		return cached, nil
	}
	asyncMigrationsCacheMu.Unlock()

	// Step 2: do the I/O through singleflight so a thundering herd of
	// /api/healthz polls collapses into one query.
	v, err, _ := asyncMigrationsSF.Do("read", func() (interface{}, error) {
		return readAsyncMigrationsRaw(db)
	})
	if err != nil {
		// Do NOT cache the error — let the next caller retry.
		return nil, err
	}
	out, _ := v.([]AsyncMigrationInfo)

	// Step 3: re-acquire to populate cache.
	asyncMigrationsCacheMu.Lock()
	asyncMigrationsCached = out
	asyncMigrationsCacheAt = asyncMigrationsNow()
	asyncMigrationsCacheMu.Unlock()
	return out, nil
}

// readAsyncMigrationsRaw bypasses the cache.
func readAsyncMigrationsRaw(db *sql.DB) ([]AsyncMigrationInfo, error) {
	if db == nil {
		return []AsyncMigrationInfo{}, nil
	}
	rows, err := db.Query(`
		SELECT name,
		       status,
		       COALESCE(started_at, ''),
		       COALESCE(ended_at, ''),
		       COALESCE(last_update_at, ''),
		       COALESCE(rows_processed, 0),
		       COALESCE(rows_total, 0),
		       COALESCE(error, '')
		FROM _async_migrations
		ORDER BY name
	`)
	if err != nil {
		// Table may not exist on freshly-initialized ingestor DBs that
		// have not run a single migration yet. Empty result is the
		// honest answer there; everything else is a real error and
		// MUST propagate (operators should see ANY corruption, not
		// silently get an empty banner).
		return []AsyncMigrationInfo{}, err
	}
	defer rows.Close()

	now := asyncMigrationsNow()
	out := make([]AsyncMigrationInfo, 0, 4)
	for rows.Next() {
		var info AsyncMigrationInfo
		var rawStatus string
		if err := rows.Scan(&info.Name, &rawStatus, &info.StartedAt, &info.EndedAt,
			&info.LastUpdateAt, &info.RowsProcessed, &info.RowsTotal, &info.ErrorMessage); err != nil {
			return nil, err
		}
		info.Status = mapAsyncStatus(rawStatus)

		startTs, startErr := parseAsyncTime(info.StartedAt)
		endTs, endErr := parseAsyncTime(info.EndedAt)
		// #1735 finding #6: do not silently discard parse errors. Build
		// the parseMsg now; append it AFTER the status-driven
		// ErrorMessage wipe below so it survives non-failed statuses too.
		parseMsg := ""
		if startErr != nil {
			parseMsg = "startedAt: " + startErr.Error()
		}
		if endErr != nil {
			if parseMsg != "" {
				parseMsg += "; "
			}
			parseMsg += "endedAt: " + endErr.Error()
		}
		switch info.Status {
		case "running":
			if !startTs.IsZero() {
				info.ElapsedSec = now.Sub(startTs).Seconds()
				if info.ElapsedSec > 0 && info.RowsProcessed > 0 {
					info.RatePerSec = float64(info.RowsProcessed) / info.ElapsedSec
					remaining := info.RowsTotal - info.RowsProcessed
					if remaining > 0 && info.RatePerSec > 0 {
						info.EtaSec = float64(remaining) / info.RatePerSec
					}
				}
			}
		case "done", "failed":
			if !startTs.IsZero() && !endTs.IsZero() {
				info.ElapsedSec = endTs.Sub(startTs).Seconds()
			}
		}
		if info.Status != "failed" {
			info.ErrorMessage = ""
		}
		// Append parse errors after the wipe so they always surface.
		if parseMsg != "" {
			if info.ErrorMessage == "" {
				info.ErrorMessage = parseMsg
			} else {
				info.ErrorMessage = info.ErrorMessage + " | " + parseMsg
			}
		}
		out = append(out, info)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// mapAsyncStatus maps the raw ingestor-side status string to the API enum.
// Unknown values map to "unknown" (NOT "running") so a corrupted row
// cannot pin the warm-up banner in a perpetual loading state.
func mapAsyncStatus(raw string) string {
	switch raw {
	case "pending_async":
		return "running"
	case "done":
		return "done"
	case "failed":
		return "failed"
	default:
		return "unknown"
	}
}

// anyAsyncMigrationRunning returns true iff any migration is in status
// "running". Failed migrations DO NOT count (operator should see
// "warm-up complete + alert", not an endless banner).
func anyAsyncMigrationRunning(infos []AsyncMigrationInfo) bool {
	for _, m := range infos {
		if m.Status == "running" {
			return true
		}
	}
	return false
}

// parseAsyncTime parses either RFC3339 (last_update_at written by
// recordAsyncMigrationProgress) or "YYYY-MM-DD HH:MM:SS" (SQLite's
// datetime('now') default for started_at/ended_at).
func parseAsyncTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.Parse("2006-01-02 15:04:05", s); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, errParseAsyncTime{s: s}
}

type errParseAsyncTime struct{ s string }

func (e errParseAsyncTime) Error() string { return "parseAsyncTime: cannot parse " + e.s }

// invalidateAsyncMigrationsCache is exported for tests that want to skip
// the TTL gate.
func invalidateAsyncMigrationsCache() {
	asyncMigrationsCacheMu.Lock()
	asyncMigrationsCacheAt = time.Time{}
	asyncMigrationsCached = nil
	asyncMigrationsCacheMu.Unlock()
}

// handlePerfAsyncMigrations exposes the read-only async-migration state at
// /api/perf/async-migrations so dashboards / curl can poll progress
// without fetching the full /api/perf payload.
//
// #1735 finding #1 (Group A): on readAsyncMigrations error, return
// HTTP 500 with the error body instead of silently returning an empty
// list. An empty list is a meaningful operator signal (no migrations
// pending); a query failure must be visible, not disguised.
func (s *Server) handlePerfAsyncMigrations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.db == nil {
		writeJSON(w, []AsyncMigrationInfo{})
		return
	}
	infos, err := readAsyncMigrations(s.db.conn)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{
			"error": "readAsyncMigrations: " + err.Error(),
		})
		return
	}
	if infos == nil {
		infos = []AsyncMigrationInfo{}
	}
	writeJSON(w, infos)
}
