package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/pgutil"
	"github.com/meshcore-analyzer/users"
)

type switchJob struct {
	Version int                `json:"version"`
	ID      string             `json:"id"`
	Source  dbconfig.Selection `json:"source"`
	Next    dbconfig.Selection `json:"next"`
}

type sqliteFence struct {
	read, writer *sql.DB
	conn         *sql.Conn
	path, digest string
}

func (f *sqliteFence) Close() {
	if f.conn != nil {
		f.conn.ExecContext(context.Background(), `ROLLBACK`)
		f.conn.Close()
	}
	if f.writer != nil {
		f.writer.Close()
	}
	if f.read != nil {
		f.read.Close()
	}
}
func (f *sqliteFence) unchanged(ctx context.Context) error {
	got, err := fingerprint(ctx, f.path)
	if err != nil {
		return err
	}
	if got != f.digest {
		return errors.New("original SQLite source changed; keep writers stopped and preserve recovery files")
	}
	return nil
}

func fenceSQLite(ctx context.Context, path, kind string) (_ *sqliteFence, err error) {
	f := &sqliteFence{path: path}
	defer func() {
		if err != nil {
			f.Close()
		}
	}()
	// The read-only keeper outlives the writer handle, preventing a last-writer
	// close from checkpointing the source's pre-existing WAL into its main file.
	if f.read, err = openSQLite(path, "ro"); err != nil {
		return nil, err
	}
	f.read.SetMaxOpenConns(1)
	if err = dbconfig.AssertSQLiteImportComplete(f.read); err != nil {
		return nil, err
	}
	if kind == "telemetry" {
		err = dbschema.AssertSQLiteReady(f.read)
	} else {
		err = users.AssertSQLiteReady(f.read)
	}
	if err != nil {
		return nil, err
	}
	if f.digest, err = fingerprint(ctx, path); err != nil {
		return nil, err
	}
	if f.writer, err = openSQLite(path, "rw"); err != nil {
		return nil, err
	}
	f.writer.SetMaxOpenConns(1)
	if f.conn, err = f.writer.Conn(ctx); err != nil {
		return nil, err
	}
	if _, err = f.conn.ExecContext(ctx, `PRAGMA busy_timeout=0`); err != nil {
		return nil, err
	}
	if _, err = f.conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return nil, errors.New("SQLite source writer is active; stop all writers before conversion")
	}
	if err = f.unchanged(ctx); err != nil {
		return nil, err
	}
	return f, nil
}

func bindOwnerURLs(selected dbconfig.Selection, owner, accounts string) (dbconfig.Storage, error) {
	raw := dbconfig.StorageInputs{BaseDir: selected.StateDir, DatabaseURL: owner, UsersDatabaseURL: accounts}
	bound, err := selected.ApplyTo(raw)
	if err != nil {
		return dbconfig.Storage{}, err
	}
	storage, err := dbconfig.ResolveStorage(bound)
	if err != nil {
		return storage, err
	}
	if selected.Backend == dbconfig.Postgres && selected.Accounts != nil && storage.UsersDatabaseURL == "" {
		return storage, errors.New("the existing account store requires its PostgreSQL owner URL even when accounts are disabled")
	}
	return storage, nil
}

func validateInitialOwnerTargets(ctx context.Context, selected dbconfig.Selection, storage dbconfig.Storage) error {
	checks := []struct {
		url    string
		target *dbconfig.PostgresTarget
	}{{storage.WriterDatabaseURL, selected.Telemetry.Postgres}}
	if selected.Accounts != nil {
		checks = append(checks, struct {
			url    string
			target *dbconfig.PostgresTarget
		}{storage.UsersDatabaseURL, selected.Accounts.Postgres})
	}
	for _, check := range checks {
		if check.target == nil || check.url == "" {
			return errors.New("initial PostgreSQL owner targets are incomplete")
		}
		db, err := pgutil.Open(check.url, true)
		if err != nil {
			return err
		}
		var database, schema string
		err = db.QueryRowContext(ctx, `SELECT current_database(),current_schema()`).Scan(&database, &schema)
		db.Close()
		if err != nil || database != check.target.Database || schema != check.target.Schema {
			return errors.New("initial PostgreSQL database/schema differs from its proposed selection; specify the intended database and search_path explicitly")
		}
	}
	return nil
}

func runStorageSwitch(ctx context.Context, o storageOptions) error {
	state, err := filepath.Abs(o.StateDir)
	if err != nil {
		return err
	}
	if o.Action == "abort" {
		if o.JobID == "" {
			return errors.New("abort requires the exact -job-id")
		}
		update, err := dbconfig.ResumeSelectionUpdate(o.SelectionFile, o.JobID)
		if err != nil {
			return err
		}
		defer update.Close()
		return update.Abort() // Staged targets and every recovery copy are preserved.
	}
	var job switchJob
	var update *dbconfig.SelectionUpdate
	raw, err := storageActionInputs(o, true)
	if err != nil {
		return err
	}
	if o.Action == "switch" {
		if o.Backend != "sqlite" && o.Backend != "postgres" {
			return errors.New("switch requires an explicit -backend=sqlite or postgres")
		}
		current, err := dbconfig.ReadSelection(o.SelectionFile)
		if err != nil {
			return err
		}
		if string(current.Backend) == o.Backend {
			return errors.New("the requested backend is already selected")
		}
		targetInputs := raw
		targetInputs.Backend = dbconfig.Backend(o.Backend)
		targetInputs.EnvBackend = targetInputs.Backend
		targetInputs.ExistingBackend = ""
		targetInputs.StateDir = current.StateDir
		if targetInputs.Backend == dbconfig.SQLite {
			if o.SQLitePath == "" {
				return errors.New("reverse switch requires a new explicit -sqlite-path; retained original files are never overwritten")
			}
			targetInputs.DBPath = o.SQLitePath
			targetInputs.UsersDBPath = o.UsersSQLitePath
		} else {
			if o.OwnerURL == "" {
				return errors.New("PostgreSQL destination requires its owner URL through the private environment")
			}
			if current.Accounts != nil && o.UsersOwnerURL == "" {
				return errors.New("the existing account store must also be converted; configure CORESCOPE_USERS_OWNER_DATABASE_URL")
			}
			targetInputs.DatabaseURL = o.OwnerURL
			targetInputs.ReaderDatabaseURL = ""
			targetInputs.WriterDatabaseURL = ""
			targetInputs.UsersDatabaseURL = o.UsersOwnerURL
			targetInputs.ApprovedChannelsDatabaseURL = ""
		}
		target, err := dbconfig.ResolveStorage(targetInputs)
		if err != nil {
			return err
		}
		if current.Accounts == nil {
			target.UsersDBPath = ""
			target.UsersDatabaseURL = ""
			target.ApprovedChannelsDatabaseURL = ""
		}
		next, err := dbconfig.NewSelection(target)
		if err != nil {
			return err
		}
		if next.Backend == dbconfig.Postgres {
			if err := validateInitialOwnerTargets(ctx, next, target); err != nil {
				return err
			}
		}
		if next.Backend == dbconfig.SQLite {
			paths := []string{next.Telemetry.SQLitePath}
			if next.Accounts != nil {
				paths = append(paths, next.Accounts.SQLitePath)
			}
			for _, path := range paths {
				if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
					return errors.New("switch SQLite targets must be fresh absent paths")
				}
			}
		}
		update, err = dbconfig.BeginSelectionUpdate(o.SelectionFile, current.Generation)
		if err != nil {
			return err
		}
		defer update.Close()
		job = switchJob{Version: 1, ID: update.ID(), Source: current, Next: next}
		if err := update.Stage(next); err != nil {
			return err
		}
		dir := filepath.Join(state, job.ID)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		if err := writePrivateJSON(filepath.Join(dir, "job.json"), job); err != nil {
			return err
		}
	} else {
		if o.JobID == "" {
			return errors.New("resume requires the exact -job-id")
		}
		update, err = dbconfig.ResumeSelectionUpdate(o.SelectionFile, o.JobID)
		if err != nil {
			return err
		}
		defer update.Close()
		if update.Target().Generation == "" {
			return errors.New("pending job has no staged destination; abort this metadata-only update and start a new job")
		}
		if accountInitialization(update.Current(), update.Target()) {
			return finishAccountSetup(ctx, o, update, raw, true)
		}
		if err := readPrivateJSON(filepath.Join(state, o.JobID, "job.json"), &job); err != nil {
			return errors.New("pending switch has no complete readable job manifest; preserve any recovery files, abort the uncommitted update and start a new job")
		}
		if job.Version != 1 || job.ID != o.JobID || !reflect.DeepEqual(job.Source, update.Current()) || !reflect.DeepEqual(job.Next, update.Target()) || job.Next.StateDir != job.Source.StateDir || job.Next.Backend == job.Source.Backend {
			return errors.New("switch recovery manifest does not match the pending installation generation")
		}
	}
	dir := filepath.Join(state, job.ID)
	ownerSelection := job.Next
	if job.Source.Backend == dbconfig.Postgres {
		ownerSelection = job.Source
	}
	owners, err := bindOwnerURLs(ownerSelection, o.OwnerURL, o.UsersOwnerURL)
	if err != nil {
		return err
	}
	if ownerSelection.Accounts != nil {
		same, err := pgutil.SameDatabase(ctx, owners.WriterDatabaseURL, owners.UsersDatabaseURL)
		if err != nil {
			return err
		}
		if same {
			return errors.New("telemetry and account databases must remain separate")
		}
	}
	var sqliteSources []*sqliteFence
	var postgresSources []*postgresFence
	stores := []struct {
		kind   string
		source dbconfig.Target
		target dbconfig.Target
		url    string
	}{{"telemetry", job.Source.Telemetry, job.Next.Telemetry, owners.WriterDatabaseURL}}
	if job.Source.Accounts != nil {
		if job.Next.Accounts == nil {
			return errors.New("switch manifest would omit an existing account store")
		}
		stores = append(stores, struct {
			kind           string
			source, target dbconfig.Target
			url            string
		}{"accounts", *job.Source.Accounts, *job.Next.Accounts, owners.UsersDatabaseURL})
	}
	// Fence both sources before copying either one. Every defer runs on all
	// failure paths; the durable selection journal remains to block startup.
	for _, store := range stores {
		if job.Source.Backend == dbconfig.SQLite {
			f, err := fenceSQLite(ctx, store.source.SQLitePath, store.kind)
			if err != nil {
				return err
			}
			sqliteSources = append(sqliteSources, f)
			defer f.Close()
		} else {
			f, err := fencePostgres(ctx, store.url, store.kind)
			if err != nil {
				return err
			}
			postgresSources = append(postgresSources, f)
			defer f.Close()
		}
	}
	var reports []importReport
	for i, store := range stores {
		var report importReport
		if job.Source.Backend == dbconfig.SQLite {
			resume := o.Action == "resume"
			if resume {
				if _, err := os.Stat(filepath.Join(dir, store.kind, "manifest.json")); errors.Is(err, os.ErrNotExist) {
					resume = false
				}
			}
			report, err = importSQLite(ctx, importOptions{Source: store.source.SQLitePath, DatabaseURL: store.url, StateDir: dir, Kind: store.kind, Resume: resume})
		} else {
			resume := o.Action == "resume"
			if resume {
				if _, err := os.Stat(filepath.Join(dir, store.kind, "reverse.json")); errors.Is(err, os.ErrNotExist) {
					resume = false
				}
			}
			report, err = reversePostgres(ctx, reverseOptions{DatabaseURL: store.url, Destination: store.target.SQLitePath, StateDir: dir, Kind: store.kind, Resume: resume, source: postgresSources[i], keepPending: true})
		}
		if err != nil {
			return err
		}
		if !report.Verified {
			return errors.New("store copy returned without complete verification")
		}
		reports = append(reports, report)
		if o.afterStore != nil {
			if err := o.afterStore(store.kind); err != nil {
				return err
			}
		}
	}
	for _, source := range sqliteSources {
		if err := source.unchanged(ctx); err != nil {
			return err
		}
	}
	verification := struct {
		JobID   string         `json:"job_id"`
		Reports []importReport `json:"reports"`
	}{job.ID, reports}
	if err := writePrivateJSON(filepath.Join(dir, "verified.json"), verification); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		previous := verification
		previous.Reports = nil
		if err := readPrivateJSON(filepath.Join(dir, "verified.json"), &previous); err != nil {
			return err
		}
		if !reflect.DeepEqual(previous, verification) {
			return errors.New("saved verification receipt differs from the reverified stores")
		}
	}
	if job.Next.Backend == dbconfig.Postgres {
		for _, store := range stores {
			if _, err := finalizeImport(ctx, store.url, store.kind); err != nil {
				return err
			}
		}
		if err := grantRuntimeTargets(ctx, job.Next, owners, raw); err != nil {
			return err
		}
	} else {
		targets := []struct{ kind, path string }{{"telemetry", job.Next.Telemetry.SQLitePath}}
		if job.Next.Accounts != nil {
			targets = append(targets, struct{ kind, path string }{"accounts", job.Next.Accounts.SQLitePath})
		}
		for _, target := range targets {
			db, err := openSQLite(target.path, "rw")
			if err != nil {
				return err
			}
			_, err = db.ExecContext(ctx, `DROP TABLE IF EXISTS corescope_reverse_progress`)
			if err == nil {
				err = assertSQLiteData(ctx, db, target.kind)
			}
			db.Close()
			if err != nil {
				return err
			}
		}
	}
	if err := validateRuntimeSelection(ctx, job.Next, raw); err != nil {
		return err
	}
	if o.beforeCommit != nil {
		if err := o.beforeCommit(); err != nil {
			return err
		}
	}
	for _, source := range sqliteSources {
		if err := source.unchanged(ctx); err != nil {
			return err
		}
	}
	if _, err := update.Commit(job.Next); err != nil {
		return fmt.Errorf("selection commit needs recovery for job %s: %w", job.ID, err)
	}
	return nil
}
