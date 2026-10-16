package dbschema

import (
	"database/sql"
	"fmt"
)

// observation_hops is an inverted index over observations.path_json: one row
// per (hop token, observation). It exists so "which observations relayed
// through hop X in the last N days" is an index range seek instead of a
// substring scan of path_json over every observation in the window.
//
// The reach endpoint was the motivating reader: on a multi-GB DB the LIKE scan
// reads gigabytes of observations from a cold page cache and outlives the HTTP
// WriteTimeout, so the request 500s and nothing is ever cached.
//
// Layout:
//
//	observation_hops(hop, timestamp, observation_id)  PRIMARY KEY, WITHOUT ROWID
//
// The PK leads with hop so a token lookup is a seek, then timestamp so the
// window bound is a range on the same b-tree, then observation_id to make the
// row unique. WITHOUT ROWID keeps it a single b-tree (no separate rowid index).
//
// Maintenance is by trigger, not by writer code, so every path that touches
// observations.path_json keeps the index in step and the prune/purge paths need
// no changes: the delete trigger removes exactly the rows the insert trigger
// created, by primary key (hop, timestamp, observation_id), never a scan.
//
// Opt-in: none of this is part of Apply. The ingestor installs it only when
// retention.hopIndexDays > 0 (EnsureObservationHopsSchema) and removes it when
// the setting is 0 or unset (DropObservationHopsSchema), so an install that
// never sets the key has no table, no triggers and no per-insert cost.
//
// Invariant: observation_hops holds rows for exactly the observations with
// timestamp >= observation_hops_coverage.since, and none below it. Real paths
// average 10 to 12 hops, so the index holds that many rows (roughly 200 bytes)
// per observation and cannot be kept for the full packet retention on a large
// instance; the ingestor therefore bounds it to a window (see
// cmd/ingestor/observation_hops_backfill.go) and the triggers ignore anything
// below the mark.
//
// Case: hops are stored upper-cased (SQLite UPPER, ASCII only) and readers
// upper-case their tokens the same way, so a lookup matches exactly what the
// case-insensitive LIKE "%\"TOK\"%" scan matches. The decoder already writes
// upper-case hex; this makes the two paths equal by construction, not by habit.
//
// Coverage: the index is only trustworthy for timestamps at or above
// observation_hops_coverage.since. A fresh install of the triggers sets since to
// "now" (only new inserts are indexed); a chunked backfill then walks backwards
// and lowers since as each chunk commits. Readers MUST fall back to the
// substring scan for windows that start before since.
//
// The server never asserts this schema (see AssertReady): it is an optional
// accelerator, and a server whose DB has no index simply uses the slow path.

// ObservationHopsCoverageTable is read by cmd/server to decide whether the
// index may answer a query window.
const ObservationHopsCoverageTable = "observation_hops_coverage"

// observationHopsTriggers lists the triggers EnsureObservationHopsSchema
// creates and DropObservationHopsSchema removes.
var observationHopsTriggers = []string{"trg_observation_hops_ai", "trg_observation_hops_ad", "trg_observation_hops_au"}

// EnsureObservationHopsSchema installs the index tables and (re)creates its
// triggers. The triggers are dropped and created on every call, so a changed
// definition reaches an existing install; the tables and the coverage mark are
// kept. Called by the ingestor when retention.hopIndexDays > 0, and by tests
// that build minimal fixture DBs.
func EnsureObservationHopsSchema(rw *sql.DB, logf Logger) error {
	if logf == nil {
		logf = func(string, ...interface{}) {}
	}
	tx, err := rw.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// PREFLIGHT: async=false reason="creates empty tables and triggers only — no row scan or rewrite of observations; the backfill is a separate chunked, resumable pass run after the ingest gate (ScheduleObservationHopsBackfill)"
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS observation_hops (
			hop            TEXT    NOT NULL,
			timestamp      INTEGER NOT NULL,
			observation_id INTEGER NOT NULL,
			PRIMARY KEY (hop, timestamp, observation_id)
		) WITHOUT ROWID`,
		`CREATE TABLE IF NOT EXISTS ` + ObservationHopsCoverageTable + ` (
			id    INTEGER PRIMARY KEY CHECK (id = 1),
			since INTEGER NOT NULL
		)`,
	}
	for _, name := range observationHopsTriggers {
		stmts = append(stmts, `DROP TRIGGER IF EXISTS `+name)
	}
	stmts = append(stmts,
		// json_valid guards every trigger: json_each raises on malformed JSON,
		// and a trigger error would fail the observation INSERT itself.
		`CREATE TRIGGER trg_observation_hops_ai
		AFTER INSERT ON observations
		WHEN NEW.path_json IS NOT NULL AND json_valid(NEW.path_json)
		  AND NEW.timestamp >= (SELECT since FROM observation_hops_coverage WHERE id = 1)
		BEGIN
			INSERT OR IGNORE INTO observation_hops (hop, timestamp, observation_id)
			SELECT DISTINCT UPPER(value), NEW.timestamp, NEW.id FROM json_each(NEW.path_json) WHERE type = 'text';
		END`,
		`CREATE TRIGGER trg_observation_hops_ad
		AFTER DELETE ON observations
		WHEN OLD.path_json IS NOT NULL AND json_valid(OLD.path_json)
		  AND OLD.timestamp >= (SELECT since FROM observation_hops_coverage WHERE id = 1)
		BEGIN
			DELETE FROM observation_hops
			WHERE observation_id = OLD.id AND timestamp = OLD.timestamp
			  AND hop IN (SELECT UPPER(value) FROM json_each(OLD.path_json) WHERE type = 'text');
		END`,
		// The path-repair pass in the ingestor rewrites path_json in place.
		`CREATE TRIGGER trg_observation_hops_au
		AFTER UPDATE OF path_json ON observations
		WHEN IFNULL(OLD.path_json, '') != IFNULL(NEW.path_json, '')
		BEGIN
			DELETE FROM observation_hops
			WHERE observation_id = OLD.id AND timestamp = OLD.timestamp
			  AND json_valid(OLD.path_json)
			  AND OLD.timestamp >= (SELECT since FROM observation_hops_coverage WHERE id = 1)
			  AND hop IN (SELECT UPPER(value) FROM json_each(OLD.path_json) WHERE type = 'text');
			INSERT OR IGNORE INTO observation_hops (hop, timestamp, observation_id)
			SELECT DISTINCT UPPER(value), NEW.timestamp, NEW.id FROM json_each(NEW.path_json)
			WHERE json_valid(NEW.path_json) AND type = 'text'
			  AND NEW.timestamp >= (SELECT since FROM observation_hops_coverage WHERE id = 1);
		END`,
	)
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("observation_hops schema: %w", err)
		}
	}
	// Seed coverage only if absent. From this instant the triggers index every
	// new row, so "now" is the correct starting claim; the backfill lowers it.
	res, err := tx.Exec(`INSERT OR IGNORE INTO ` + ObservationHopsCoverageTable +
		` (id, since) VALUES (1, CAST(strftime('%s','now') AS INTEGER))`)
	if err != nil {
		return fmt.Errorf("seed observation_hops coverage: %w", err)
	}
	seeded, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return err
	}
	if seeded > 0 {
		logf("[dbschema] observation_hops installed; backfill pending")
	}
	return nil
}

// DropObservationHopsSchema removes the index: its triggers first (so no insert
// touches a half-dropped table), then both tables. Reports whether anything was
// there. Called by the ingestor when retention.hopIndexDays is 0 or unset, so
// switching the setting off leaves nothing behind; it is also the documented
// step before rolling back to a version that does not know this setting.
func DropObservationHopsSchema(rw *sql.DB) (bool, error) {
	var n int
	if err := rw.QueryRow(`SELECT COUNT(*) FROM sqlite_master
		WHERE name IN ('observation_hops', '` + ObservationHopsCoverageTable + `', 'trg_observation_hops_ai', 'trg_observation_hops_ad', 'trg_observation_hops_au')`).Scan(&n); err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	tx, err := rw.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	stmts := []string{}
	for _, name := range observationHopsTriggers {
		stmts = append(stmts, `DROP TRIGGER IF EXISTS `+name)
	}
	stmts = append(stmts, `DROP TABLE IF EXISTS observation_hops`, `DROP TABLE IF EXISTS `+ObservationHopsCoverageTable)
	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return false, fmt.Errorf("drop observation_hops: %w", err)
		}
	}
	return true, tx.Commit()
}

// Execer is satisfied by *sql.DB and *sql.Tx.
type Execer interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
}

// IndexObservationHops adds the hop rows of the observations with
// lo <= timestamp < hi, the same rows the insert trigger writes. Used by the
// ingestor's backfill, one chunk per writer transaction. Returns rows added.
func IndexObservationHops(ex Execer, lo, hi int64) (int64, error) {
	res, err := ex.Exec(`
		INSERT OR IGNORE INTO observation_hops (hop, timestamp, observation_id)
		SELECT DISTINCT UPPER(j.value), o.timestamp, o.id
		FROM observations o, json_each(o.path_json) j
		WHERE o.timestamp >= ? AND o.timestamp < ?
		  AND json_valid(o.path_json) AND j.type = 'text'`, lo, hi)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// UnindexObservationHops deletes the hop rows of the observations with
// lo <= timestamp < hi, by primary key. Used by the ingestor's trim. Returns
// rows deleted.
func UnindexObservationHops(ex Execer, lo, hi int64) (int64, error) {
	res, err := ex.Exec(`
		DELETE FROM observation_hops WHERE (hop, timestamp, observation_id) IN (
			SELECT UPPER(j.value), o.timestamp, o.id
			FROM observations o, json_each(o.path_json) j
			WHERE o.timestamp >= ? AND o.timestamp < ?
			  AND json_valid(o.path_json) AND j.type = 'text')`, lo, hi)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
