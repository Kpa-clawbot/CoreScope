package dbconfig

import (
	"database/sql"
	"errors"
)

// The native resume ledger also fences adoption through a different state
// directory. Conversion removes it only after complete data verification.
const SQLiteImportPendingTable = "corescope_reverse_progress"

var ErrSQLiteImportIncomplete = errors.New("SQLite target is a staged incomplete conversion; keep it unselected and resume or abort its original offline job")

func AssertSQLiteImportComplete(db *sql.DB) error {
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name=?1`, SQLiteImportPendingTable).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrSQLiteImportIncomplete
	}
	return nil
}
