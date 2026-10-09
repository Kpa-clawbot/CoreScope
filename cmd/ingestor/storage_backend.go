package main

import (
	"database/sql"
	"errors"
	"net/url"
	"os"

	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/dbschema/legacy"
)

func (s *Store) Backend() dbconfig.Backend {
	if s.backend == "" {
		return dbconfig.SQLite
	}
	return s.backend
}
func (s *Store) parameter(n int) string { return s.Backend().Parameter(n) }
func (s *Store) nativeSQL(sqlite, postgres string) string {
	if s.Backend() == dbconfig.SQLite {
		return sqlite
	}
	return postgres
}

// OpenStoreStorage is the runtime boundary after the shared selector resolves
// raw inputs. Compatibility constructors still accept paths or explicit URLs.
func OpenStoreStorage(storage dbconfig.Storage, cfg *DBConfig) (*Store, error) {
	return openStoreStorage(storage, cfg, 300)
}

// Retain the shared durability/connection options. Existing runtime targets use
// rw (never rwc); persistent pragmas run only after schema validation succeeds.
func sqliteTelemetryWriterDSN(path string, create bool) (string, error) {
	if !create {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return "", errors.New("selected SQLite telemetry is missing or empty; restore the initialized store")
		}
	}
	dsn, err := legacy.WriterDSN(path)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("_txlock", "immediate")
	if !create {
		q.Set("mode", "rw")
		q.Del("_journal_mode")
		q.Del("_auto_vacuum")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func validateExistingTelemetry(db *sql.DB) error {
	if err := dbconfig.AssertSQLiteImportComplete(db); err != nil {
		return err
	}
	if err := dbschema.AssertSQLiteReady(db); err == nil {
		return nil
	}
	// A supported older v3 store may need the normal writer upgrade. This also
	// rejects empty/header-only/unrelated databases through the opened handle.
	return legacy.CheckLegacySource(db)
}
