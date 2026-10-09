package users

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/meshcore-analyzer/dbconfig"
)

func TestSQLitePendingImportRefusesBeforeAccountSchemaChanges(t *testing.T) {
	for name, check := range map[string]func(*sql.DB) error{"apply": ApplySQLite, "ready": AssertSQLiteReady} {
		t.Run(name, func(t *testing.T) {
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "partial.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`CREATE TABLE corescope_reverse_progress (table_name TEXT)`); err != nil {
				t.Fatal(err)
			}
			if err := check(db); !errors.Is(err, dbconfig.ErrSQLiteImportIncomplete) {
				t.Errorf("pending account import was not identified: %v", err)
			}
			var objects int
			if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name <> 'corescope_reverse_progress'`).Scan(&objects); err != nil || objects != 0 {
				t.Errorf("pending source changed before refusal: objects=%d, err=%v", objects, err)
			}
		})
	}
}
