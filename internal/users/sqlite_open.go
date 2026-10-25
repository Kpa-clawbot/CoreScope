package users

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/meshcore-analyzer/dbconfig"
	"modernc.org/sqlite"
)

func openSQLite(path string, forbidden ...string) (*Store, error) {
	return openSQLiteMode(path, true, forbidden...)
}
func openSQLiteMode(path string, create bool, forbidden ...string) (*Store, error) {
	if !create && strings.TrimSpace(path) == "" {
		return nil, errors.New("users: accounts are not initialized; run explicit account setup")
	}
	if strings.TrimSpace(path) == "" || strings.Contains(path, "://") || strings.HasPrefix(strings.ToLower(path), "file:") {
		return nil, errors.New("users: SQLite account storage requires a local filesystem path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("users: cannot resolve account database path")
	}
	for _, other := range forbidden {
		if strings.TrimSpace(other) != "" && samePath(abs, other) {
			return nil, errors.New("users: refusing to open the measurement database")
		}
	}
	if create {
		if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
			return nil, err
		}
	} else {
		info, err := os.Stat(abs)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return nil, errors.New("users: initialized SQLite accounts are missing or empty; restore the original store")
		}
	}
	// Immediate write transactions serialize read-before-write across processes.
	// The pinned modernc driver leaves explicit read-only transactions deferred.
	dsn, err := sqliteAccountDSN(abs, create)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, errors.New("users: cannot open SQLite account storage")
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if !create {
		if err := validateExistingSQLiteAccounts(db); err != nil {
			db.Close()
			return nil, err
		}
	}

	if err := ApplySQLite(db); err != nil {
		db.Close()
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := enableSQLiteWAL(ctx, db); err != nil {
		db.Close()
		return nil, err
	}

	return &Store{db: db, target: abs, backend: dbconfig.SQLite, now: time.Now}, nil
}

func samePath(abs, other string) bool {
	otherAbs, err := filepath.Abs(other)
	if err != nil {
		return false
	}
	a, ea := os.Stat(abs)
	b, eb := os.Stat(otherAbs)
	if ea == nil && eb == nil {
		return os.SameFile(a, b)
	}
	return strings.EqualFold(filepath.Clean(abs), filepath.Clean(otherAbs))
}

func sqliteAccountDSN(path string, create bool) (string, error) {
	q := url.Values{"_pragma": {"busy_timeout(5000)", "foreign_keys(1)"}, "_txlock": {"immediate"}}
	if !create {
		q.Set("mode", "rw")
	}
	return dbconfig.SQLiteURI(path, q)
}

func validateExistingSQLiteAccounts(db *sql.DB) error {
	if err := dbconfig.AssertSQLiteImportComplete(db); err != nil {
		return err
	}
	var count, version int
	if err := db.QueryRow(`SELECT count(*),COALESCE(MAX(version),0) FROM schema_version`).Scan(&count, &version); err != nil || count != 1 || version < 1 || version > SQLiteSchemaVersion {
		return errors.New("users: initialized SQLite accounts have no supported schema; restore or explicitly upgrade the original store")
	}
	return validateSQLiteLayout(db, version)
}

// journal_mode may return SQLITE_BUSY immediately while another first opener
// changes the journal or creates its schema, even with busy_timeout enabled.
// Retry only that transient native result, bounded by the startup deadline.
func enableSQLiteWAL(ctx context.Context, db *sql.DB) error {
	for {
		var mode string
		err := db.QueryRowContext(ctx, `PRAGMA journal_mode=WAL`).Scan(&mode)
		if err == nil {
			if mode != "wal" {
				return errors.New("users: SQLite accounts require WAL mode")
			}
			return nil
		}
		var busy *sqlite.Error
		if !errors.As(err, &busy) || busy.Code()&0xff != 5 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}
