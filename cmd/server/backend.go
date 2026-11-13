package main

// The server reads either a SQLite file or a Postgres database. Almost all of
// its SQL is written to run unchanged on both (the Postgres driver in
// internal/dbx rewrites ? placeholders); the statements that cannot be shared
// pick their text with sqlFor, so both forms sit side by side where they are
// used and a reviewer sees them together.

// isPostgres reports whether db reads a Postgres database. Nil-safe.
func (db *DB) isPostgres() bool { return db != nil && db.pg }

// sqlFor returns sqlite on SQLite and postgres on Postgres. Use it only where
// no single statement works on both; prefer portable SQL everywhere else.
func (db *DB) sqlFor(sqlite, postgres string) string {
	if db.isPostgres() {
		return postgres
	}
	return sqlite
}
