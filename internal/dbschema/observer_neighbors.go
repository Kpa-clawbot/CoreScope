package dbschema

import (
	"database/sql"
	"fmt"
)

// observer_neighbor_reports keeps the latest /neighbors report of each
// observer (#2270, #2271). Until now the ingestor only took the confirmed
// region scopes out of it (#1865, nodes.configured_scope) and dropped the
// rest, so nothing could say when an observer last reported or whom it
// heard.
//
// One row per observer, replaced by every live report. neighbors_json is the
// ingestor's sanitised copy of the report's neighbors[] (the report is
// unauthenticated input), not the raw payload.
//
// Not required by AssertReady: a server that comes up before the ingestor
// created it shows no neighbor data and probes again on the next request.
//
// PREFLIGHT: async=false reason="creates one empty table keyed by observer id; no scan of existing data"
func ensureObserverNeighborReports(rw *sql.DB) error {
	if _, err := rw.Exec(`
		CREATE TABLE IF NOT EXISTS observer_neighbor_reports (
			observer_id        TEXT PRIMARY KEY,
			received_at        TEXT NOT NULL,
			reported_at        TEXT,
			neighbor_count     INTEGER NOT NULL,
			queried_neighbors  INTEGER,
			self_scopes        TEXT,
			self_default_scope TEXT,
			neighbors_json     TEXT NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("create observer_neighbor_reports: %w", err)
	}
	return nil
}
