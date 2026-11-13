package dbx

import "database/sql"

// IsPostgres reports whether db was opened with PostgresDriverName.
//
// Code that needs a per-backend branch asks the handle it already holds rather
// than having a dialect value threaded through every call: almost all SQL in
// this codebase is written to run unchanged on both backends, and this is the
// switch for the few statements that cannot be.
func IsPostgres(db *sql.DB) bool {
	if db == nil {
		return false
	}
	_, ok := db.Driver().(*qmarkDriver)
	return ok
}
