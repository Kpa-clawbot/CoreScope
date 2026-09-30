package dbschema

import "database/sql"

// Empty on creation; history is backfilled after the ingestor becomes ready.
// At most two rows per retained transmission. AUTOINCREMENT prevents reuse of
// feed IDs after retention removes the newest rows.
// PREFLIGHT: async=true reason="creates empty bounded evidence and single-row progress tables; history backfill is batched after live ingest readiness"
func ensureAdvertEvidence(rw *sql.DB) error {
	_, err := rw.Exec(`
		CREATE TABLE IF NOT EXISTS advert_route_evidence (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			tx_id INTEGER NOT NULL REFERENCES transmissions(id) ON DELETE CASCADE,
			bit INTEGER NOT NULL CHECK(bit IN (1,2)),
			UNIQUE(tx_id,bit)
		);
		CREATE TABLE IF NOT EXISTS advert_evidence_backfill (
			id INTEGER PRIMARY KEY CHECK(id=1),
			tx_cursor INTEGER NOT NULL DEFAULT 0,
			obs_cursor INTEGER NOT NULL DEFAULT 0
		);
	`)
	return err
}
