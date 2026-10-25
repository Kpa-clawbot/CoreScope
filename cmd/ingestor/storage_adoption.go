package main

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/dbschema/legacy"
	"github.com/meshcore-analyzer/pgutil"
)

// Adoption never initializes a missing telemetry file. Setup creates a fresh
// installation; this path records only an existing, validated native target.
func adoptLegacyStorage(raw dbconfig.StorageInputs) error {
	selectionPath, err := storageSelectionPath(raw)
	if err != nil {
		return err
	}
	if _, err := dbconfig.ReadSelection(selectionPath); err == nil {
		return nil
	} else if !errors.Is(err, dbconfig.ErrSelectionMissing) {
		return err
	}
	if raw.Backend == "" && raw.EnvBackend == "" && raw.DBPath == "" && raw.DatabaseURL == "" && raw.ReaderDatabaseURL == "" && raw.WriterDatabaseURL == "" && raw.UsersDatabaseURL == "" && raw.ApprovedChannelsDatabaseURL == "" {
		raw.DBPath = filepath.Join(raw.BaseDir, "data", "meshcore.db")
	}
	raw.StateDir = filepath.Dir(selectionPath)
	storage, err := dbconfig.ResolveStorage(raw)
	if err != nil {
		return err
	}
	selected, err := dbconfig.NewSelection(storage)
	if err != nil {
		return err
	}
	if storage.Backend == dbconfig.SQLite {
		db, err := openExistingSQLite(storage.DBPath)
		if err != nil {
			return err
		}
		err = dbconfig.AssertSQLiteImportComplete(db)
		if err == nil {
			err = legacy.CheckLegacySource(db)
		}
		db.Close()
		if err != nil {
			return err
		}
		if _, err := os.Stat(storage.UsersDBPath); err == nil {
			accounts, err := openExistingSQLite(storage.UsersDBPath)
			if err != nil {
				return err
			}
			var version int
			if err = dbconfig.AssertSQLiteImportComplete(accounts); err != nil {
				accounts.Close()
				return err
			}
			err = accounts.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&version)
			accounts.Close()
			if err != nil || version < 1 || version > 6 {
				return errors.New("existing SQLite accounts schema is unsupported; use setup or offline recovery")
			}
		} else if errors.Is(err, os.ErrNotExist) {
			// Absent legacy accounts are uninitialized, not an initialized empty store.
			// Only explicit setup may create them and fill this selection target.
			selected.Accounts = nil
		} else {
			return err
		}
	} else {
		if storage.UsersDatabaseURL == "" && storage.ApprovedChannelsDatabaseURL == "" {
			legacyAccounts := raw.UsersDBPath
			if legacyAccounts == "" {
				legacyAccounts = filepath.Join(storage.StateDir, "users.db")
			}
			if _, err := os.Stat(legacyAccounts); err == nil {
				return errors.New("existing SQLite accounts require offline conversion before adopting PostgreSQL")
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		db, err := pgutil.Open(storage.WriterDatabaseURL, false)
		if err != nil {
			return err
		}
		err = dbschema.AssertPostgresReady(db)
		if err == nil {
			err = dbschema.AssertWriter(db)
		}
		db.Close()
		if err != nil {
			return err
		}
		accountURL := storage.UsersDatabaseURL
		if accountURL == "" {
			accountURL = storage.ApprovedChannelsDatabaseURL
		}
		if accountURL != "" {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			same, err := pgutil.SameDatabase(ctx, storage.WriterDatabaseURL, accountURL)
			cancel()
			if err != nil {
				return err
			}
			if same {
				return errors.New("accounts must not use the telemetry database")
			}
			accounts, err := pgutil.Open(accountURL, true)
			if err != nil {
				return err
			}
			var ready bool
			err = accounts.QueryRow(`SELECT ready FROM corescope_schema WHERE kind='accounts' AND version=1`).Scan(&ready)
			accounts.Close()
			if err != nil || !ready {
				return errors.New("PostgreSQL accounts import/bootstrap is incomplete")
			}
		}
	}

	_, err = dbconfig.AdoptSelection(selectionPath, selected)
	return err
}

func openExistingSQLite(path string) (*sql.DB, error) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return nil, errors.New("existing SQLite storage is missing or empty; run explicit setup instead of creating a new runtime database")
	}
	dsn, err := dbconfig.SQLiteURI(path, url.Values{"mode": {"ro"}, "_query_only": {"on"}, "_busy_timeout": {"5000"}})
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}
