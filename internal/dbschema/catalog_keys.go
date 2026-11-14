package dbschema

import "database/sql"

// ensureCatalogKeys creates the table behind the catalogue key tier (#2107).
//
// One row per catalogue name the ingestor found evidence for. status is
// 'proposed' (evidence above the threshold, not in force), 'promoted' (in
// force as the lowest key tier) or 'rejected' (set by an operator: never
// proposed or promoted again, see docs/catalogue-keys.md). Only promoted rows
// are loaded into the key sets; proposed rows exist so an operator can see what the catalogue would
// add before switching auto-promotion on, and so a later UI can show "from
// catalogue, confirmed by N transmissions". hits/threshold/window_n/candidates
// record the evidence the decision was made on.
//
// PREFLIGHT: async=true reason="creates one empty table; rows are written in small batches by the catalogue worker after live ingest is ready"
func ensureCatalogKeys(rw *sql.DB) error {
	_, err := rw.Exec(`
		CREATE TABLE IF NOT EXISTS catalog_keys (
			kind TEXT NOT NULL CHECK(kind IN ('channel','region')),
			name TEXT NOT NULL,
			key_hex TEXT NOT NULL,
			status TEXT NOT NULL CHECK(status IN ('proposed','promoted','rejected')),
			hits INTEGER NOT NULL DEFAULT 0,
			threshold INTEGER NOT NULL DEFAULT 0,
			window_n INTEGER NOT NULL DEFAULT 0,
			candidates INTEGER NOT NULL DEFAULT 0,
			first_seen INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			PRIMARY KEY(kind, name)
		) WITHOUT ROWID;
	`)
	return err
}
