package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	_ "github.com/mattn/go-sqlite3"
	"github.com/meshcore-analyzer/dbconfig"
	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/dbschema/legacy"
	"github.com/meshcore-analyzer/pgutil"
)

// Backend is selected before opening a connection, never after an open fails.
func (db *DB) Backend() dbconfig.Backend {
	if db == nil || db.backend == "" {
		return dbconfig.SQLite
	}
	return db.backend
}
func (db *DB) parameter(n int) string { return db.Backend().Parameter(n) }
func (db *DB) nativeSQL(sqlite, postgres string) string {
	if db.Backend() == dbconfig.SQLite {
		return sqlite
	}
	return postgres
}

// OpenStorage opens only the selected reader. The application performs the
// selected schema/import readiness check before starting its HTTP listener.
func OpenStorage(storage dbconfig.Storage) (*DB, error) {
	var conn *sql.DB
	var err error
	path := storage.DBPath
	switch storage.Backend {
	case dbconfig.SQLite:
		if strings.Contains(path, "://") || strings.HasPrefix(path, "postgres:") || strings.HasPrefix(path, "postgresql:") {
			return nil, fmt.Errorf("SQLite telemetry requires a filesystem path")
		}
		var dsn string
		dsn, err = dbconfig.SQLiteURI(path, url.Values{"mode": {"ro"}, "_query_only": {"on"}, "_busy_timeout": {"5000"}, "_cache_size": {"-2000"}})
		if err == nil {
			conn, err = sql.Open("sqlite3", dsn)
		}
	case dbconfig.Postgres:
		path = storage.ReaderDatabaseURL
		conn, err = pgutil.Open(path, true)
	default:
		return nil, fmt.Errorf("telemetry requires an explicitly selected storage backend")
	}
	if err != nil {
		return nil, err
	}
	conn.SetMaxOpenConns(4)
	conn.SetMaxIdleConns(4)
	if storage.Backend == dbconfig.Postgres {
		if err = pgutil.AssertReadOnly(conn); err != nil {
			conn.Close()
			return nil, err
		}
	}
	state := storage.StateDir
	if state == "" {
		if storage.Backend == dbconfig.SQLite {
			state = filepath.Dir(path)
		} else {
			state = "data"
		}
	}
	d := &DB{conn: conn, path: path, stateDir: state, backend: storage.Backend}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sc, err := conn.Conn(ctx)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("schema detection: acquire connection: %w", err)
	}
	err = d.detectSchema(ctx, sc)
	_ = sc.Close()
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("schema detection failed: %w", err)
	}
	if err = d.prepareStatements(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("prepare statements: %w", err)
	}
	return d, nil
}

func (db *DB) tableHasColumn(table, column string) (bool, error) {
	if db.Backend() == dbconfig.SQLite {
		return legacy.TableHasColumn(db.conn, table, column)
	}
	return dbschema.TableHasColumn(db.conn, table, column)
}

// Only the PostgreSQL driver understands this extended-protocol query option.
func (db *DB) planWithValues(args []any) []any {
	if db.Backend() == dbconfig.Postgres {
		return append([]any{pgx.QueryExecModeExec}, args...)
	}
	return args
}

func (db *DB) AssertReady() error {
	if db.Backend() == dbconfig.SQLite {
		return dbschema.AssertSQLiteReady(db.conn)
	}
	return dbschema.AssertPostgresReady(db.conn)
}
