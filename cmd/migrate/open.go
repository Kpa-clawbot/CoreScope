package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/dbschema/pgschema"
	"github.com/meshcore-analyzer/dbx"
)

// openReadOnly opens ref for reading: SQLite read-only (mode=ro), Postgres
// through the dbx driver. The pool is capped at one connection so every read
// shares the snapshot transaction begun on it.
func openReadOnly(ctx context.Context, ref dbRef) (*sql.DB, error) {
	var db *sql.DB
	var err error
	if ref.kind == sqliteBackend {
		st, serr := os.Stat(ref.path)
		if serr != nil {
			return nil, fmt.Errorf("%s: %w", ref.display, serr)
		}
		if st.IsDir() {
			return nil, fmt.Errorf("%s is a directory", ref.display)
		}
		db, err = sql.Open("sqlite3", sqliteReadOnlyDSN(ref.path))
	} else {
		db, err = sql.Open(dbx.PostgresDriverName, ref.url)
	}
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", ref.display, err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to %s: %w", ref.display, err)
	}
	return db, nil
}

// beginSnapshot starts the read transaction everything is read through. On
// SQLite a read transaction pins one WAL snapshot from its first read; on
// Postgres REPEATABLE READ pins one snapshot for the whole transaction.
func beginSnapshot(ctx context.Context, db *sql.DB, kind backend) (*sql.Tx, error) {
	if kind == postgresBackend {
		return db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	}
	return db.BeginTx(ctx, nil)
}

// openSnapshot opens ref read-only and returns a side reading one consistent
// snapshot of it.
func openSnapshot(ctx context.Context, ref dbRef) (side, func(), error) {
	db, err := openReadOnly(ctx, ref)
	if err != nil {
		return side{}, nil, err
	}
	return snapshotSide(ctx, ref, db, nil)
}

// snapshotSide begins the snapshot on db, runs check (if any) inside it,
// then loads the schema from it.
func snapshotSide(ctx context.Context, ref dbRef, db *sql.DB, check func(*sql.Tx) error) (side, func(), error) {
	tx, err := beginSnapshot(ctx, db, ref.kind)
	if err != nil {
		db.Close()
		return side{}, nil, fmt.Errorf("begin read transaction on %s: %w", ref.display, err)
	}
	closeFn := func() {
		tx.Rollback()
		db.Close()
	}
	if check != nil {
		if err := check(tx); err != nil {
			closeFn()
			return side{}, nil, err
		}
	}
	s, err := loadSchema(ctx, tx, ref.kind)
	if err != nil {
		closeFn()
		return side{}, nil, fmt.Errorf("read schema of %s: %w", ref.display, err)
	}
	return side{q: tx, kind: ref.kind, schema: s}, closeFn, nil
}

// openSource opens the copy source and refuses one that is not fully
// migrated: the schema must pass AssertReady and no async migration may be
// unfinished (their backfills rewrite rows, so a copy taken midway would
// carry half-migrated data).
func openSource(ctx context.Context, ref dbRef) (side, func(), error) {
	db, err := openReadOnly(ctx, ref)
	if err != nil {
		return side{}, nil, err
	}
	const hint = "start the ingestor once on the source so its migrations finish, then rerun"
	if ref.kind == sqliteBackend {
		err = dbschema.AssertReady(db)
	} else {
		err = pgschema.AssertReady(ctx, db)
	}
	if err != nil {
		db.Close()
		return side{}, nil, fmt.Errorf("source %s is not ready: %v; %s", ref.display, err, hint)
	}
	return snapshotSide(ctx, ref, db, func(tx *sql.Tx) error {
		ok, err := dbschema.TableExists(tx, "_async_migrations")
		if err != nil {
			return fmt.Errorf("probe _async_migrations on %s: %w", ref.display, err)
		}
		if !ok {
			return fmt.Errorf("source %s has no _async_migrations table; %s", ref.display, hint)
		}
		rows, err := tx.QueryContext(ctx, `SELECT name, status FROM _async_migrations WHERE status <> 'done' ORDER BY name`)
		if err != nil {
			return fmt.Errorf("read _async_migrations on %s: %w", ref.display, err)
		}
		defer rows.Close()
		var pending []string
		for rows.Next() {
			var name, status string
			if err := rows.Scan(&name, &status); err != nil {
				return err
			}
			pending = append(pending, name+" ("+status+")")
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(pending) > 0 {
			return errors.New("source " + ref.display + " has unfinished async migrations: " +
				strings.Join(pending, ", ") + "; " + hint)
		}
		return nil
	})
}
