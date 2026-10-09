package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/dbschema/legacy"
	"github.com/meshcore-analyzer/pgutil"
	"github.com/meshcore-analyzer/users"
)

type storageOptions struct {
	Action, SelectionFile, Backend, SQLitePath, UsersSQLitePath     string
	OwnerURL, UsersOwnerURL, StateDir, JobID, ConfigFile, ConfigDir string
	Offline                                                         bool
	afterStore                                                      func(string) error
	beforeCommit                                                    func() error
}

func firstSetting(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func storageActionInputs(o storageOptions, recorded bool) (dbconfig.StorageInputs, error) {
	var raw dbconfig.StorageInputs
	cwd, err := os.Getwd()
	if err != nil {
		return raw, err
	}
	configDir, err := filepath.Abs(firstSetting(o.ConfigDir, "."))
	if err != nil {
		return raw, err
	}
	if o.ConfigFile != "" {
		raw, err = dbconfig.ReadStorageConfig(o.ConfigFile)
		if err != nil {
			return raw, err
		}
	} else {
		for _, path := range []string{filepath.Join(configDir, "config.json"), filepath.Join(configDir, "data", "config.json")} {
			config, e := dbconfig.ReadStorageConfig(path)
			if e == nil {
				raw = config
				err = nil
				break
			}
			if !errors.Is(e, os.ErrNotExist) {
				return raw, e
			}
		}
		if err != nil {
			return raw, err
		}
	}
	envPath := os.Getenv("DB_PATH")
	if !recorded && o.SQLitePath == "" && envPath != "" && raw.DBPath != "" {
		a, e := filepath.Abs(envPath)
		if e != nil {
			return raw, e
		}
		b, e := filepath.Abs(raw.DBPath)
		if e != nil {
			return raw, e
		}
		if a != b {
			return raw, errors.New("legacy config dbPath and DB_PATH differ between services; supply one explicit -sqlite-path before adoption")
		}
	}
	raw.BaseDir = cwd
	raw.Backend = dbconfig.Backend(firstSetting(o.Backend, string(raw.Backend)))
	raw.EnvBackend = dbconfig.Backend(os.Getenv("CORESCOPE_DB_BACKEND"))
	if o.Backend != "" {
		raw.EnvBackend = raw.Backend
	}
	raw.DBPath = firstSetting(o.SQLitePath, envPath, raw.DBPath)
	raw.UsersDBPath = firstSetting(o.UsersSQLitePath, raw.UsersDBPath)
	raw.DatabaseURL = firstSetting(os.Getenv("CORESCOPE_DATABASE_URL"), raw.DatabaseURL)
	raw.ReaderDatabaseURL = os.Getenv("CORESCOPE_READER_DATABASE_URL")
	raw.WriterDatabaseURL = os.Getenv("CORESCOPE_WRITER_DATABASE_URL")
	raw.UsersDatabaseURL = firstSetting(os.Getenv("CORESCOPE_USERS_DATABASE_URL"), raw.UsersDatabaseURL)
	raw.ApprovedChannelsDatabaseURL = firstSetting(os.Getenv("CORESCOPE_APPROVED_CHANNELS_DATABASE_URL"), raw.ApprovedChannelsDatabaseURL)
	raw.StateDir = filepath.Dir(o.SelectionFile) // stable discovery, not the recovery directory
	raw.FreshInstall = true                      // only permits resolution; native facts below authorize initialization
	sqliteDefault := raw.Backend == dbconfig.SQLite || raw.EnvBackend == dbconfig.SQLite || (raw.Backend == "" && raw.EnvBackend == "" && raw.DatabaseURL == "" && raw.ReaderDatabaseURL == "" && raw.WriterDatabaseURL == "" && raw.UsersDatabaseURL == "" && raw.ApprovedChannelsDatabaseURL == "" && o.OwnerURL == "" && o.UsersOwnerURL == "")
	if raw.DBPath == "" && sqliteDefault {
		raw.DBPath = filepath.Join(configDir, "data", "meshcore.db")
	}
	return raw, nil
}

func runStorageAction(ctx context.Context, o storageOptions, out io.Writer) (err error) {
	defer func() {
		var pgError *pgconn.PgError
		if errors.As(err, &pgError) {
			err = fmt.Errorf("storage operation failed (SQLSTATE %s); keep writers stopped and inspect the private recovery job", pgError.Code)
		}
	}()
	if !filepath.IsAbs(o.SelectionFile) || filepath.Clean(o.SelectionFile) != o.SelectionFile || filepath.Base(o.SelectionFile) != "storage-selection.json" {
		return errors.New("provide an absolute normalized -selection-file ending in storage-selection.json")
	}
	status, inspectErr := dbconfig.InspectSelection(o.SelectionFile)
	if o.Action == "status" {
		if err := json.NewEncoder(out).Encode(status); err != nil {
			return err
		}
		return inspectErr
	}
	if !o.Offline {
		return errors.New("storage setup and switching require -offline after stopping all telemetry and account writers")
	}
	if o.StateDir == "" {
		o.StateDir = filepath.Join(filepath.Dir(o.SelectionFile), "migration")
	}
	switch o.Action {
	case "setup", "adopt", "init", "switch", "resume", "abort":
	default:
		return errors.New("unsupported storage action; use -help")
	}
	if inspectErr != nil {
		return inspectErr
	}
	o.OwnerURL = firstSetting(o.OwnerURL, os.Getenv("CORESCOPE_DATABASE_URL"))
	o.UsersOwnerURL = firstSetting(o.UsersOwnerURL, os.Getenv("CORESCOPE_USERS_OWNER_DATABASE_URL"))
	if o.Action == "resume" || o.Action == "abort" || o.Action == "switch" {
		if err := runStorageSwitch(ctx, o); err != nil {
			return err
		}
	} else {
		if status.State == "pending" {
			return dbconfig.ErrSelectionInProgress
		}
		if err := setupStorage(ctx, o, status.State == "ready"); err != nil {
			return err
		}
	}
	status, err = dbconfig.InspectSelection(o.SelectionFile)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(status)
}

func setupStorage(ctx context.Context, o storageOptions, recorded bool) error {
	if recorded && o.Action == "init" {
		return errors.New("installation already has a recorded backend; use a verified switch")
	}
	raw, err := storageActionInputs(o, recorded)
	if err != nil {
		return err
	}
	if recorded {
		selection, lease, err := dbconfig.OpenSelection(o.SelectionFile)
		if err != nil {
			return err
		}
		defer lease.Close()
		if selection.Accounts == nil && o.Action == "setup" && (selection.Backend == dbconfig.SQLite || o.UsersOwnerURL != "" || raw.UsersDatabaseURL != "" || raw.ApprovedChannelsDatabaseURL != "") {
			lease.Close()
			return initializeAccounts(ctx, o, selection, raw)
		}
		if selection.Backend == dbconfig.Postgres && o.OwnerURL != "" {
			owners, err := bindOwnerURLs(selection, o.OwnerURL, o.UsersOwnerURL)
			if err != nil {
				return err
			}
			if err := grantRuntimeTargets(ctx, selection, owners, raw); err != nil {
				return err
			}
		}
		return validateRuntimeSelection(ctx, selection, raw)
	}
	ownerInputs := raw
	// Owner endpoints are used only for schema operations. Runtime credentials
	// stay separate and are validated against the resulting selected targets.
	if o.OwnerURL != "" {
		ownerInputs.DatabaseURL = o.OwnerURL
		ownerInputs.ReaderDatabaseURL = ""
		ownerInputs.WriterDatabaseURL = ""
	}
	if o.UsersOwnerURL != "" {
		ownerInputs.UsersDatabaseURL = o.UsersOwnerURL
		ownerInputs.ApprovedChannelsDatabaseURL = ""
	}
	storage, err := dbconfig.ResolveStorage(ownerInputs)
	if err != nil {
		return err
	}
	selected, err := dbconfig.NewSelection(storage)
	if err != nil {
		return err
	}
	if storage.Backend == dbconfig.SQLite {
		legacyTelemetry, err := sqliteOccupied(ctx, storage.DBPath)
		if err != nil {
			return err
		}
		for _, store := range []struct{ kind, path string }{{"telemetry", storage.DBPath}, {"accounts", storage.UsersDBPath}} {
			occupied, err := sqliteOccupied(ctx, store.path)
			if err != nil {
				return err
			}
			if store.kind == "accounts" && legacyTelemetry && !occupied {
				selected.Accounts = nil
				storage.UsersDBPath = ""
				continue
			}
			if o.Action == "init" && occupied {
				return errors.New("initialization target already contains SQLite schema; use setup/adopt for an existing installation")
			}
			if o.Action == "adopt" && store.kind == "telemetry" && !occupied {
				return errors.New("adoption requires an existing telemetry database")
			}
			if err := applySQLiteTarget(ctx, store.path, store.kind); err != nil {
				return err
			}
		}
	} else {
		if o.OwnerURL == "" {
			return errors.New("unrecorded PostgreSQL setup requires its telemetry owner URL through CORESCOPE_DATABASE_URL")
		}
		if (raw.UsersDatabaseURL != "" || raw.ApprovedChannelsDatabaseURL != "") && o.UsersOwnerURL == "" {
			return errors.New("PostgreSQL account setup requires CORESCOPE_USERS_OWNER_DATABASE_URL; runtime credentials are never owner credentials")
		}
		if err := validateInitialOwnerTargets(ctx, selected, storage); err != nil {
			return err
		}
		storage, err = bindOwnerURLs(selected, o.OwnerURL, o.UsersOwnerURL)
		if err != nil {
			return err
		}
		if storage.UsersDatabaseURL != "" {
			same, err := pgutil.SameDatabase(ctx, storage.WriterDatabaseURL, storage.UsersDatabaseURL)
			if err != nil {
				return err
			}
			if same {
				return errors.New("telemetry and accounts require separate PostgreSQL databases")
			}
		}
		knownSQLite := firstSetting(raw.DBPath, filepath.Join(filepath.Dir(o.SelectionFile), "meshcore.db"))
		knownUsers := firstSetting(raw.UsersDBPath, filepath.Join(filepath.Dir(knownSQLite), "users.db"))
		for _, store := range []struct{ kind, url, oldPath string }{{"telemetry", storage.WriterDatabaseURL, knownSQLite}, {"accounts", storage.UsersDatabaseURL, knownUsers}} {
			occupied, err := sqliteOccupied(ctx, store.oldPath)
			if err != nil {
				return err
			}
			if store.url == "" {
				if occupied {
					return errors.New("an existing SQLite account store must be preserved; supply its PostgreSQL owner target and use a verified switch")
				}
				continue
			}
			db, err := pgutil.Open(store.url, false)
			if err != nil {
				return err
			}
			readyErr := assertPostgresReady(db, store.kind)
			if readyErr == nil {
				db.Close()
				if o.Action == "init" {
					return errors.New("PostgreSQL target is already initialized; use setup/adopt")
				}
				if occupied && o.Action != "adopt" {
					if err := checkImportedSource(ctx, store.url, store.kind, store.oldPath); err != nil {
						return errors.New("retained SQLite data needs its verified import or explicit adoption of the existing ready PostgreSQL store")
					}
				}
				continue
			}
			var objects int
			err = db.QueryRowContext(ctx, importDestinationObjects).Scan(&objects)
			if err != nil {
				db.Close()
				return err
			}
			if objects != 0 || o.Action == "adopt" {
				db.Close()
				return errors.New("PostgreSQL target is not a ready supported store or a fresh empty schema")
			}
			if occupied {
				db.Close()
				return errors.New("refusing to initialize empty PostgreSQL while primary SQLite data exists; use a verified offline switch")
			}
			if raw.Backend != dbconfig.Postgres && raw.EnvBackend != dbconfig.Postgres {
				db.Close()
				return errors.New("fresh PostgreSQL initialization requires an explicit backend choice")
			}
			if store.kind == "telemetry" {
				err = dbschema.ApplyPostgres(db, nil)
			} else {
				err = users.ApplyPostgres(db)
			}
			db.Close()
			if err != nil {
				return err
			}
		}
	}
	if selected.Backend == dbconfig.Postgres {
		if err := grantRuntimeTargets(ctx, selected, storage, raw); err != nil {
			return err
		}
	}
	if err := validateRuntimeSelection(ctx, selected, raw); err != nil {
		return err
	}
	_, err = dbconfig.AdoptSelection(o.SelectionFile, selected)
	return err
}

func sqliteOccupied(ctx context.Context, path string) (bool, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	db, err := openSQLite(path, "ro")
	if err != nil {
		return false, err
	}
	defer db.Close()
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`).Scan(&count); err != nil {
		return false, errors.New("existing SQLite target cannot be validated; preserve it instead of selecting an empty database")
	}
	return count != 0, nil
}

func applySQLiteTarget(ctx context.Context, path, kind string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	uri, err := legacy.WriterDSN(path)
	if err != nil {
		return err
	}
	db, err := sql.Open(importSQLiteDriver, uri)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := dbconfig.AssertSQLiteImportComplete(db); err != nil {
		return err
	}
	if kind == "telemetry" {
		return dbschema.ApplySQLite(db, func(string, ...interface{}) {})
	}
	return users.ApplySQLite(db)
}

func assertPostgresReady(db *sql.DB, kind string) error {
	if kind == "telemetry" {
		return dbschema.AssertPostgresReady(db)
	}
	return users.AssertPostgresReady(db)
}

func validateRuntimeSelection(ctx context.Context, selection dbconfig.Selection, raw dbconfig.StorageInputs) error {
	bound, err := selection.ApplyTo(raw)
	if err != nil {
		return err
	}
	storage, err := dbconfig.ResolveStorage(bound)
	if err != nil {
		return err
	}
	if storage.Backend == dbconfig.SQLite {
		a, err := os.Stat(storage.DBPath)
		if err != nil {
			return err
		}
		stores := []struct{ kind, path string }{{"telemetry", storage.DBPath}}
		if selection.Accounts != nil {
			b, err := os.Stat(storage.UsersDBPath)
			if err != nil {
				return err
			}
			if os.SameFile(a, b) {
				return errors.New("telemetry and accounts cannot share the same SQLite file")
			}
			stores = append(stores, struct{ kind, path string }{"accounts", storage.UsersDBPath})
		}
		for _, store := range stores {
			db, err := openSQLite(store.path, "ro")
			if err != nil {
				return err
			}
			if err := dbconfig.AssertSQLiteImportComplete(db); err != nil {
				db.Close()
				return err
			}
			if store.kind == "telemetry" {
				err = dbschema.AssertSQLiteReady(db)
			} else {
				err = users.AssertSQLiteReady(db)
			}
			db.Close()
			if err != nil {
				return err
			}
		}
		return nil
	}
	if storage.ReaderDatabaseURL == "" || storage.WriterDatabaseURL == "" {
		return errors.New("prepare both PostgreSQL reader and writer role URLs before selecting this backend")
	}
	reader, err := pgutil.Open(storage.ReaderDatabaseURL, true)
	if err != nil {
		return err
	}
	if err = dbschema.AssertPostgresReady(reader); err == nil {
		err = pgutil.AssertReadOnly(reader)
	}
	reader.Close()
	if err != nil {
		return err
	}
	writer, err := pgutil.Open(storage.WriterDatabaseURL, false)
	if err != nil {
		return err
	}
	if err = dbschema.AssertPostgresReady(writer); err == nil {
		err = dbschema.AssertWriter(writer)
	}
	writer.Close()
	if err != nil {
		return err
	}
	if selection.Accounts != nil {
		if storage.UsersDatabaseURL == "" {
			return errors.New("prepare the selected PostgreSQL account runtime URL before cutover")
		}
		store, err := users.OpenStorage(storage)
		if err != nil {
			return err
		}
		store.Close()
	}
	if storage.ApprovedChannelsDatabaseURL != "" {
		db, err := pgutil.Open(storage.ApprovedChannelsDatabaseURL, true)
		if err != nil {
			return err
		}
		err = pgutil.AssertReadOnly(db)
		db.Close()
		if err != nil {
			return err
		}
	}
	return nil
}
