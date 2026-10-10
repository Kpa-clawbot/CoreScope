package dbschema

import "database/sql"

// ensureKeyBackfill creates the tables behind the key-set backfill (#2107).
//
// tx_rewrite_feed is the change feed: the ingestor appends one row per
// transmission it rewrote in place (a GRP_TXT it can now decrypt, a transport
// scope it can now name), in the same transaction as the rewrite itself. The
// read-only server tails it with its own cursor, exactly like
// advert_route_evidence, so a rewritten row reaches the in-memory store without
// a restart. AUTOINCREMENT keeps feed IDs monotonic after pruning; the
// created_at index serves the age-based prune.
//
// key_backfill_state holds at most one in-flight run per kind. run_fp is the
// fingerprint of the key set the run is working with: a run whose fingerprint
// no longer matches the live key set is restarted, which is what makes a key
// added mid-run safe. cursor advances in the same transaction as the rewrites
// it covers, so a cancelled run resumes exactly where it committed.
//
// key_backfill_done records which keys have had a complete pass over history,
// so a restart with an unchanged key set does no work at all.
//
// PREFLIGHT: async=true reason="creates three empty tables and one index; the backfill itself runs batched in the background after live ingest is ready"
func ensureKeyBackfill(rw *sql.DB) error {
	_, err := rw.Exec(`
		CREATE TABLE IF NOT EXISTS tx_rewrite_feed (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			tx_id INTEGER NOT NULL REFERENCES transmissions(id) ON DELETE CASCADE,
			created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER))
		);
		CREATE INDEX IF NOT EXISTS idx_tx_rewrite_feed_created ON tx_rewrite_feed(created_at);
		CREATE TABLE IF NOT EXISTS key_backfill_state (
			kind TEXT PRIMARY KEY CHECK(kind IN ('channel','region')),
			run_fp TEXT NOT NULL,
			cursor INTEGER NOT NULL DEFAULT 0,
			upper INTEGER NOT NULL DEFAULT 0,
			rewritten INTEGER NOT NULL DEFAULT 0,
			started_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER))
		);
		CREATE TABLE IF NOT EXISTS key_backfill_done (
			kind TEXT NOT NULL,
			key_fp TEXT NOT NULL,
			done_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			PRIMARY KEY(kind, key_fp)
		) WITHOUT ROWID;
	`)
	return err
}
