package main

import (
	"database/sql"
	"strings"
	"time"
)

// SQL fragments for the ingestor's runtime statements that must run unchanged
// on SQLite and Postgres. Each one reproduces a SQLite-only construct exactly,
// NULL handling included; portable_sql_test.go checks them against the
// SQLite originals over a matrix of inputs.

// sqlMax2 is SQLite's two-argument max(a, b): NULL when either argument is
// NULL, otherwise the larger. Postgres has no two-argument max, and its
// GREATEST ignores NULLs. a and b are repeated in the output, so they must be
// side-effect-free expressions without placeholders.
func sqlMax2(a, b string) string {
	return "(CASE WHEN " + a + " IS NULL OR " + b + " IS NULL THEN NULL WHEN " +
		a + " >= " + b + " THEN " + a + " ELSE " + b + " END)"
}

// sqlMin2 is SQLite's two-argument min(a, b); see sqlMax2.
func sqlMin2(a, b string) string {
	return "(CASE WHEN " + a + " IS NULL OR " + b + " IS NULL THEN NULL WHEN " +
		a + " <= " + b + " THEN " + a + " ELSE " + b + " END)"
}

// clampedMaxSQL is the SQLite expression
//
//	MAX(MIN(COALESCE(col, ''), ?), ?)
//
// the stored value capped at the first argument (ingest time, so a future value from an old bug cannot
// stick), then raised to the second (the receive time). The two placeholders
// are bound once each, in that order, through a one-row derived table, so the
// caller passes the same arguments the SQLite form took. col must be
// table-qualified: inside ON CONFLICT DO UPDATE an unqualified name is
// ambiguous on Postgres.
//
// The CAST is for Postgres, which cannot type a bare placeholder in a SELECT
// list; the callers bind strings (or nil), which it leaves unchanged. On
// SQLite a CAST AS TEXT also gives the value TEXT affinity, which would then
// convert a numeric operand in the comparisons where min()/max() compare by
// storage class. Concatenating the empty string is a no-op on text and NULL
// in both engines and leaves an expression with no affinity.
func clampedMaxSQL(col string) string {
	return "(SELECT " + sqlMax2(sqlMin2("COALESCE("+col+", '')", "clamp.hi"), "clamp.lo") +
		" FROM (SELECT CAST(? AS TEXT) || '' AS hi, CAST(? AS TEXT) || '' AS lo) AS clamp)"
}

// Per-backend statements. Two portable forms measurably slow the SQLite ingest
// hot path (BenchmarkInsertTransmissionHotPath: RETURNING ~+12% CPU, the
// clamp subquery ~+5%), so for those alone SQLite keeps its original SQL,
// chosen once when statements are prepared. TestSQLiteHotPathSQLUnchanged
// pins the SQLite text; portable_sql_guard_test.go confines the SQLite-only
// forms to these functions.

// sqliteInsertTransmissionSQL is the original SQLite statement, byte for byte.
const sqliteInsertTransmissionSQL = `
		INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json, channel_hash, scope_name, from_pubkey, last_seen, code1, code2)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`

// insertTransmissionSQL is the transmissions insert for the backend. Postgres
// has no LastInsertId, so it returns the id instead.
func insertTransmissionSQL(pg bool) string {
	if pg {
		return sqliteInsertTransmissionSQL + "RETURNING id\n"
	}
	return sqliteInsertTransmissionSQL
}

// insertTransmissionFunc runs a statement prepared from insertTransmissionSQL
// and returns the new row's id. The SQLite path ignores a LastInsertId error,
// as it always has: mattn's never fails after a successful Exec.
func insertTransmissionFunc(pg bool, stmt *sql.Stmt) func(args ...any) (int64, error) {
	if pg {
		return func(args ...any) (int64, error) {
			var id int64
			err := stmt.QueryRow(args...).Scan(&id)
			return id, err
		}
	}
	return func(args ...any) (int64, error) {
		res, err := stmt.Exec(args...)
		if err != nil {
			return 0, err
		}
		id, _ := res.LastInsertId()
		return id, nil
	}
}

// lastSeenClampSQL is the last_seen clamp for the backend: SQLite's original
// two-argument MAX/MIN, or clampedMaxSQL, its exact equivalent, on Postgres.
// Both bind (ingest time, receive time) in that order.
func lastSeenClampSQL(pg bool, table, col string) string {
	if pg {
		return clampedMaxSQL(table + "." + col)
	}
	return "MAX(MIN(COALESCE(" + col + ", ''), ?), ?)"
}

// sqliteDatetimeLayout formats a time exactly as SQLite's datetime('now').
const sqliteDatetimeLayout = "2006-01-02 15:04:05"

// sqliteDatetimeNow is the Go-side replacement for datetime('now') in runtime
// writes: byte-identical text, computed by the caller instead of the engine.
func sqliteDatetimeNow() string {
	return time.Now().UTC().Format(sqliteDatetimeLayout)
}

// nodeColumns is every column of nodes, which inactive_nodes mirrors. Named
// explicitly so MoveStaleNodes does not depend on the two tables declaring
// their columns in the same order (ALTER TABLE appends, and the migrations
// have not always run in the same order on both). TestNodeColumnsMatchSchema
// fails when the schema gains a column this list lacks.
var nodeColumns = []string{
	"public_key", "name", "role", "lat", "lon", "last_seen", "first_seen",
	"advert_count", "battery_mv", "temperature_c", "foreign_advert",
	"default_scope", "configured_scope", "configured_scope_at",
	"multibyte_sup", "multibyte_evidence",
}

// moveStaleNodesSQL copies nodes last seen before ? into inactive_nodes,
// replacing a row already there for the same key (the INSERT OR REPLACE it
// replaces). public_key is the only key of inactive_nodes, so the upsert and
// REPLACE resolve the same conflicts.
var moveStaleNodesSQL = func() string {
	cols := strings.Join(nodeColumns, ", ")
	set := make([]string, 0, len(nodeColumns)-1)
	for _, c := range nodeColumns[1:] {
		set = append(set, c+" = excluded."+c)
	}
	return "INSERT INTO inactive_nodes (" + cols + ") SELECT " + cols +
		" FROM nodes WHERE last_seen < ? ON CONFLICT (public_key) DO UPDATE SET " +
		strings.Join(set, ", ")
}()
