package main

import (
	"context"
	"errors"
	"os"
	"reflect"

	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/pgutil"
	"github.com/meshcore-analyzer/users"
)

// The existing journal identifies this one same-backend transition without a
// separate initialization flag or sidecar. Its telemetry target never changes.
func accountInitialization(current, next dbconfig.Selection) bool {
	return current.Generation != "" && current.Accounts == nil && next.Accounts != nil && current.Backend == next.Backend && current.StateDir == next.StateDir && reflect.DeepEqual(current.Telemetry, next.Telemetry)
}

func initializeAccounts(ctx context.Context, o storageOptions, current dbconfig.Selection, raw dbconfig.StorageInputs) error {
	update, err := dbconfig.BeginSelectionUpdate(o.SelectionFile, current.Generation)
	if err != nil {
		return err
	}
	defer update.Close()
	// Failed validation before staging has done no account work. A real process
	// crash still leaves the journal for an explicit metadata-only abort.
	defer func() {
		if update.Target().Generation == "" {
			_ = update.Abort()
		}
	}()
	if err := validateRuntimeSelection(ctx, current, raw); err != nil {
		return err
	}
	bound, err := current.ApplyTo(raw)
	if err != nil {
		return err
	}
	bound.ExistingBackend = "" // This explicit setup, alone, supplies a new account default.
	if current.Backend == dbconfig.SQLite {
		bound.UsersDBPath = raw.UsersDBPath
	} else {
		if o.UsersOwnerURL == "" || raw.UsersDatabaseURL == "" {
			return errors.New("first PostgreSQL account setup requires both account owner and runtime writer URLs")
		}
		bound.UsersDatabaseURL = o.UsersOwnerURL
		bound.ApprovedChannelsDatabaseURL = ""
	}
	storage, err := dbconfig.ResolveStorage(bound)
	if err != nil {
		return err
	}
	next, err := dbconfig.NewSelection(storage)
	if err != nil {
		return err
	}
	if !accountInitialization(current, next) {
		return errors.New("account setup cannot change the selected telemetry target")
	}
	if current.Backend == dbconfig.Postgres {
		if err := validateInitialOwnerTargets(ctx, next, storage); err != nil {
			return err
		}
	}
	if err := update.Stage(next); err != nil {
		return err
	}
	return finishAccountSetup(ctx, o, update, raw, false)
}

func finishAccountSetup(ctx context.Context, o storageOptions, update *dbconfig.SelectionUpdate, raw dbconfig.StorageInputs, resume bool) error {
	current, next := update.Current(), update.Target()
	if !accountInitialization(current, next) {
		return errors.New("pending journal is not a supported first account initialization")
	}
	if err := validateRuntimeSelection(ctx, current, raw); err != nil {
		return err
	}
	if next.Backend == dbconfig.SQLite {
		path := next.Accounts.SQLitePath
		telemetry, err := os.Stat(current.Telemetry.SQLitePath)
		if err != nil {
			return err
		}
		info, statErr := os.Stat(path)
		if statErr == nil && os.SameFile(telemetry, info) {
			return errors.New("account setup cannot use the telemetry file")
		}
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		if resume {
			if statErr != nil {
				return errors.New("pending account target is missing; restore it or abort this uncommitted initialization before explicit setup; it was not recreated")
			}
			db, err := openSQLite(path, "ro")
			if err != nil {
				return err
			}
			err = assertSQLiteData(ctx, db, "accounts")
			db.Close()
			if err != nil {
				return errors.New("account initialization did not reach a ready target; preserve it, abort the uncommitted update and rerun explicit setup")
			}
		} else if err := applySQLiteTarget(ctx, path, "accounts"); err != nil {
			return err
		}
		db, err := openSQLite(path, "ro")
		if err != nil {
			return err
		}
		err = assertSQLiteData(ctx, db, "accounts")
		db.Close()
		if err != nil {
			return err
		}
	} else {
		if o.UsersOwnerURL == "" || raw.UsersDatabaseURL == "" {
			return errors.New("first PostgreSQL account setup requires both account owner and runtime writer URLs")
		}
		ownerInputs := raw
		ownerInputs.UsersDatabaseURL = o.UsersOwnerURL
		ownerInputs.ApprovedChannelsDatabaseURL = ""
		bound, err := next.ApplyTo(ownerInputs)
		if err != nil {
			return err
		}
		owners, err := dbconfig.ResolveStorage(bound)
		if err != nil {
			return err
		}
		same, err := pgutil.SameDatabase(ctx, owners.WriterDatabaseURL, owners.UsersDatabaseURL)
		if err != nil {
			return err
		}
		if same {
			return errors.New("accounts require a separate PostgreSQL database")
		}
		db, err := pgutil.Open(owners.UsersDatabaseURL, false)
		if err != nil {
			return err
		}
		readyErr := users.AssertPostgresReady(db)
		if readyErr != nil {
			if resume {
				db.Close()
				return errors.New("account initialization did not reach a ready PostgreSQL target; preserve it, abort the uncommitted update and rerun setup")
			}
			var objects int
			err = db.QueryRowContext(ctx, importDestinationObjects).Scan(&objects)
			if err == nil && objects != 0 {
				err = errors.New("account target is neither a ready store nor a fresh empty PostgreSQL schema")
			}
			if err == nil {
				err = users.ApplyPostgres(db)
			}
		}
		if err == nil {
			tables, layoutErr := layouts("accounts")
			if layoutErr != nil {
				err = layoutErr
			} else {
				err = validatePostgresLayout(db, "accounts", tables)
			}
		}
		db.Close()
		if err != nil {
			return err
		}
		owners.WriterDatabaseURL = "" // Existing telemetry permissions are not part of first account setup.
		if err := grantRuntimeTargets(ctx, next, owners, raw); err != nil {
			return err
		}
	}
	if err := validateRuntimeSelection(ctx, next, raw); err != nil {
		return err
	}
	if o.beforeCommit != nil {
		if err := o.beforeCommit(); err != nil {
			return err
		}
	}
	_, err := update.Commit(next)
	return err
}
