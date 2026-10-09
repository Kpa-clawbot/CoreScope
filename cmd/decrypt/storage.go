package main

import (
	"database/sql"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/mattn/go-sqlite3"
	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/pgutil"
)

const exportTransmissionsSQL = `SELECT id, hash, raw_hex, first_seen FROM transmissions WHERE payload_type = 5`
const exportPathSQL = `SELECT decoded_json FROM transmissions WHERE id = $1`
const exportObserversSQL = `
	SELECT o.name, obs.snr, obs.rssi, obs.timestamp
	FROM observations obs
	LEFT JOIN observers o ON o.rowid = obs.observer_idx
	WHERE obs.transmission_id = $1
	ORDER BY obs.timestamp, obs.id`

type exportOptions struct {
	Backend                                   dbconfig.Backend
	DBPath, DatabaseURL, StateDir, ConfigPath string
}

// Use the installation's storage fields without depending on the server's full
// configuration. Only absent search candidates fall through to defaults.
func exportStorageInputs(base string, flags exportOptions, getenv func(string) string) (dbconfig.StorageInputs, error) {
	base, err := filepath.Abs(base)
	if err != nil {
		return dbconfig.StorageInputs{}, err
	}
	paths := []string{filepath.Join(base, "config.json"), filepath.Join(base, "data", "config.json")}
	if flags.ConfigPath != "" {
		paths = []string{flags.ConfigPath}
	}
	var raw dbconfig.StorageInputs
	for _, path := range paths {
		raw, err = dbconfig.ReadStorageConfig(path)
		if err == nil {
			break
		}
		if flags.ConfigPath != "" || !errors.Is(err, os.ErrNotExist) {
			return raw, err
		}
	}
	raw.BaseDir = base
	raw.EnvBackend = dbconfig.Backend(getenv("CORESCOPE_DB_BACKEND"))
	for key, target := range map[string]*string{
		"DB_PATH": &raw.DBPath, "CORESCOPE_STATE_DIR": &raw.StateDir,
		"CORESCOPE_DATABASE_URL": &raw.DatabaseURL, "CORESCOPE_READER_DATABASE_URL": &raw.ReaderDatabaseURL,
	} {
		if value := strings.TrimSpace(getenv(key)); value != "" {
			*target = value
		}
	}
	if flags.Backend != "" {
		raw.Backend = flags.Backend
	}
	if flags.DBPath != "" {
		raw.DBPath = flags.DBPath
	}
	if flags.DatabaseURL != "" {
		raw.DatabaseURL, raw.ReaderDatabaseURL = flags.DatabaseURL, flags.DatabaseURL
	}
	if flags.StateDir != "" {
		raw.StateDir = flags.StateDir
	}
	return raw, nil
}

func openSelectedExport(raw dbconfig.StorageInputs) (*sql.DB, io.Closer, error) {
	// The legacy SQLite directory remains the discovery anchor after a switch.
	state := raw.StateDir
	if state == "" {
		path := raw.DBPath
		if path == "" {
			path = filepath.Join("data", "meshcore.db")
		}
		if strings.Contains(path, "://") || strings.HasPrefix(path, "postgres:") || strings.HasPrefix(path, "postgresql:") {
			return nil, nil, errors.New("--db requires a SQLite filesystem path")
		}
		state = filepath.Dir(path)
	}
	if strings.Contains(state, "://") || strings.ContainsRune(state, 0) || (filepath.VolumeName(state) != "" && !filepath.IsAbs(state)) {
		return nil, nil, errors.New("invalid storage state directory")
	}
	if !filepath.IsAbs(state) {
		state = filepath.Join(raw.BaseDir, state)
	}
	selected, lease, err := dbconfig.OpenSelection(dbconfig.SelectionPath(filepath.Clean(state)))
	if err == nil {
		raw, err = selected.ApplyTo(raw)
	} else if errors.Is(err, dbconfig.ErrSelectionMissing) {
		// An offline/legacy export can read an existing source without adopting
		// it. This default is never permission to create or migrate a database.
		err = nil
		if raw.Backend == "" && raw.EnvBackend == "" && raw.DBPath == "" && raw.UsersDBPath == "" && raw.DatabaseURL == "" && raw.ReaderDatabaseURL == "" && raw.WriterDatabaseURL == "" && raw.UsersDatabaseURL == "" && raw.ApprovedChannelsDatabaseURL == "" {
			raw.DBPath = filepath.Join("data", "meshcore.db")
		}
	}
	var db *sql.DB
	if err == nil {
		var storage dbconfig.Storage
		storage, err = dbconfig.ResolveStorage(raw)
		if err == nil {
			target := storage.DBPath
			if storage.Backend == dbconfig.Postgres {
				target = storage.ReaderDatabaseURL
			}
			db, err = openExportDB(target)
		}
	}
	if err != nil && lease != nil {
		lease.Close()
		lease = nil
	}
	return db, lease, err
}

func openExportDB(target string) (*sql.DB, error) {
	postgres := strings.HasPrefix(target, "postgres://") || strings.HasPrefix(target, "postgresql://")
	var db *sql.DB
	var err error
	if postgres {
		db, err = pgutil.Open(target, true)
	} else {
		if target == "" || strings.Contains(target, "://") || strings.HasPrefix(target, "postgres:") || strings.HasPrefix(target, "postgresql:") {
			return nil, errors.New("export requires a SQLite filesystem path or PostgreSQL reader URL")
		}
		var dsn string
		dsn, err = dbconfig.SQLiteURI(target, url.Values{"mode": {"ro"}, "_query_only": {"on"}, "_busy_timeout": {"5000"}})
		if err == nil {
			db, err = sql.Open("sqlite3", dsn)
		}
	}
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	if postgres {
		err = pgutil.AssertReadOnly(db)
		if err == nil {
			err = dbschema.AssertPostgresReady(db)
		}
	} else {
		err = dbconfig.AssertSQLiteImportComplete(db)
		// Export needs only its existing read columns, including on older
		// offline snapshots. Do not run migrations or require writer indexes.
		for _, query := range []string{exportTransmissionsSQL, exportPathSQL, exportObserversSQL} {
			if err != nil {
				break
			}
			var stmt *sql.Stmt
			stmt, err = db.Prepare(query)
			if err == nil {
				err = stmt.Close()
			}
		}
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
