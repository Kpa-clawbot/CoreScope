package dbschema

import _ "embed"

// SQLiteEndState is the complete SQLite schema a freshly booted ingestor
// creates: every table, index and view, as CREATE statements runnable in one
// Exec. Tools that must create a ready SQLite database without running the
// ingestor's migration history (the copy tool's SQLite target, tests) use it.
// It omits the opt-in observation_hops index, which the ingestor creates only
// when retention.hopIndexDays > 0.
//
// cmd/ingestor's TestSQLiteEndStateMatchesIngestor compares it against a real
// OpenStore, so it cannot silently fall behind the ingestor.
//
//go:embed sqlite_endstate.sql
var SQLiteEndState string
