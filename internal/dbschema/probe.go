package dbschema

import (
	"database/sql"
	"errors"
	"strings"
)

// Schema probes that work on both backends without being told which one they
// are talking to: they take a Querier, which may be a transaction, and a
// transaction does not say what database it belongs to.
//
// Each probe asks information_schema first. Postgres answers it. SQLite has no
// information_schema, so the query fails with "no such table" — a prepare-time
// error that leaves a SQLite transaction usable — and the probe falls back to
// the SQLite catalogue. The order is load-bearing: asking SQLite's catalogue
// first would put a syntax error into a Postgres transaction, and Postgres
// aborts the whole transaction on any error.

const (
	pgColumnProbe = `SELECT 1 FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = ? AND column_name = ?`
	pgTableProbe = `SELECT 1 FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_name = ? AND table_type = 'BASE TABLE'`
	// observers' primary key is exactly the one column idx. pg_index, not
	// information_schema.table_constraints: the latter lists only tables the
	// role owns or may change, so the server's SELECT-only role sees no keys.
	pgObserversIdxPKProbe = `SELECT 1 FROM pg_catalog.pg_index i
		JOIN pg_catalog.pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = i.indkey[0]
		WHERE i.indrelid = to_regclass('observers') AND i.indisprimary
		  AND i.indnatts = 1 AND a.attname = 'idx'`
)

// infoSchemaProbe runs a Postgres information_schema probe. ok is false only
// when the database has no information_schema (SQLite), in which case the
// caller falls back to the SQLite catalogue.
func infoSchemaProbe(db Querier, query string, args ...any) (found, ok bool, err error) {
	var one int
	err = db.QueryRow(query, args...).Scan(&one)
	switch {
	case err == nil:
		return true, true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, true, nil
	case strings.Contains(err.Error(), "no such table"):
		return false, false, nil
	default:
		return false, true, err
	}
}

// TableExists reports whether a base table with the given name exists in the
// current database (SQLite) or the current schema (Postgres).
//
// Pass the transaction, not the pool, when you are inside one. See Querier.
func TableExists(db Querier, table string) (bool, error) {
	if found, ok, err := infoSchemaProbe(db, pgTableProbe, table); ok {
		return found, err
	}
	var one int
	err := db.QueryRow(`SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?`, table).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// pgObserversIdxIsPK reports, on Postgres, whether observers' primary key is
// the single column idx. isPG is false on SQLite, which has no
// information_schema; the caller then asks the SQLite catalogue instead
// (observersColumnsInfo). The pg_catalog query is not SQLite syntax, so it
// runs only once the information_schema probe has shown this is Postgres.
func pgObserversIdxIsPK(db Querier) (idxIsPK, isPG bool, err error) {
	if _, ok, err := infoSchemaProbe(db, pgTableProbe, "observers"); !ok || err != nil {
		return false, ok, err
	}
	var one int
	err = db.QueryRow(pgObserversIdxPKProbe).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, true, nil
	}
	return err == nil, true, err
}
