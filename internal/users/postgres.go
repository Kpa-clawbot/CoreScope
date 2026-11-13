package users

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/meshcore-analyzer/dbx"
)

// PostgresSchema is the schema the Postgres store lives in.
const PostgresSchema = "corescope_users"

// UsersReaderRole is the role the ingestor connects as (or is a member of)
// to read the approved channel proposals and the companion links. When it
// exists, OpenPostgres grants it USAGE on the schema and SELECT on those two
// tables, nothing else; when it does not, OpenPostgres grants nothing.
const UsersReaderRole = "corescope_users_reader"

// pgReaderTables are the tables UsersReaderRole may read.
var pgReaderTables = []string{"proposals", "companion_links"}

// pgUndefinedObject is Postgres' SQLSTATE undefined_object (here: the role
// was dropped between the existence check and the GRANT).
const pgUndefinedObject = "42704"

const (
	// pgMigrateLockKey is the pg_advisory_lock key held while the schema is
	// created and migrated ("CoreUsrs"; pgschema uses "CoreScop"), so
	// replicas starting together migrate one at a time.
	pgMigrateLockKey int64 = 0x436f726555737273
	// pgMaxOpenConns bounds the pool. Account traffic is small; more than
	// one connection keeps a slow request from queueing all others.
	pgMaxOpenConns = 8
	// pgConnectWait is how long OpenPostgres retries a database that is not
	// reachable yet (a pod starting before its network is ready).
	pgConnectWait = 30 * time.Second
)

// OpenPostgres opens the account store in schema corescope_users of the
// Postgres database named by dsn (a postgres:// URL or a keyword=value
// string), creating the schema if absent and applying pending migrations.
// The schema must be owned by the connecting role. Errors never contain the
// DSN.
func OpenPostgres(dsn string) (*Store, error) {
	return openPostgres(dsn, PostgresSchema)
}

// openPostgres is OpenPostgres in a named schema (tests use their own).
func openPostgres(dsn, schema string) (*Store, error) {
	if !validSchemaName(schema) {
		return nil, fmt.Errorf("users: invalid schema name %q", schema)
	}
	if pgBaselineVersion+len(pgMigrations) != len(migrations) {
		return nil, fmt.Errorf("users: Postgres migrations reach version %d, SQLite ones %d",
			pgBaselineVersion+len(pgMigrations), len(migrations))
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		// Not wrapped: pgx's parse error quotes the connection string.
		return nil, errors.New("users: unparseable Postgres DSN")
	}
	// search_path is sent as a startup parameter, so every pooled connection
	// uses the schema. One given in the DSN is replaced; one hidden in
	// options would race it, so it is refused.
	for k, v := range cfg.RuntimeParams {
		switch strings.ToLower(k) {
		case "search_path":
			delete(cfg.RuntimeParams, k)
		case "options":
			if strings.Contains(strings.ReplaceAll(strings.ToLower(v), "-", "_"), "search_path") {
				return nil, errors.New("users: the DSN's options parameter sets search_path, which the account store sets itself; remove it")
			}
		}
	}
	cfg.RuntimeParams["search_path"] = schema
	name := stdlib.RegisterConnConfig(cfg)
	db, err := sql.Open(dbx.PostgresDriverName, name)
	if err != nil {
		stdlib.UnregisterConnConfig(name)
		return nil, fmt.Errorf("users: open postgres: %w", err)
	}
	db.SetMaxOpenConns(pgMaxOpenConns)
	db.SetMaxIdleConns(pgMaxOpenConns)
	s := &Store{db: db, now: time.Now, pg: true, closeHook: func() { stdlib.UnregisterConnConfig(name) }}
	ctx := context.Background()
	if err := dbx.PingWithRetry(ctx, db, pgConnectWait); err != nil {
		s.Close()
		return nil, fmt.Errorf("users: connect postgres: %w", err)
	}
	// On error the store is closed, which ends the session and so releases
	// the advisory lock whatever state migratePostgres left it in.
	if err := s.migratePostgres(ctx, schema); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

// validSchemaName accepts a lower-case unquoted identifier.
func validSchemaName(s string) bool {
	if s == "" || len(s) > 63 {
		return false
	}
	for i, c := range s {
		if !(c >= 'a' && c <= 'z' || c == '_' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// migratePostgres creates schema if needed, brings it to the current
// version and grants UsersReaderRole its reads, on one connection that holds
// pgMigrateLockKey throughout (concurrent GRANTs on one object can fail with
// "tuple concurrently updated").
func (s *Store) migratePostgres(ctx context.Context, schema string) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("users: postgres connection: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock(?)`, pgMigrateLockKey); err != nil {
		return fmt.Errorf("users: migration lock: %w", err)
	}
	if err := pgMigrateLocked(ctx, conn, schema); err != nil {
		return err
	}
	if err := pgGrantReader(ctx, conn, schema); err != nil {
		return err
	}
	var unlocked bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_advisory_unlock(?)`, pgMigrateLockKey).Scan(&unlocked); err != nil {
		return fmt.Errorf("users: migration unlock: %w", err)
	}
	if !unlocked {
		return errors.New("users: migration unlock: the lock was not held")
	}
	return nil
}

func pgMigrateLocked(ctx context.Context, conn *sql.Conn, schema string) error {
	var owner, me string
	err := conn.QueryRowContext(ctx, `SELECT pg_get_userbyid(nspowner), current_user FROM pg_namespace WHERE nspname = ?`, schema).
		Scan(&owner, &me)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Checked first because CREATE SCHEMA IF NOT EXISTS needs CREATE on
		// the database even when the schema exists.
		if _, err := conn.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
			return fmt.Errorf("users: create schema %s: %w", schema, err)
		}
	case err != nil:
		return fmt.Errorf("users: look up schema %s: %w", schema, err)
	case owner != me:
		return fmt.Errorf("users: schema %s is owned by role %q, not by the connecting role %q", schema, owner, me)
	}
	var cur sql.NullString
	if err := conn.QueryRowContext(ctx, `SELECT current_schema()`).Scan(&cur); err != nil {
		return fmt.Errorf("users: current schema: %w", err)
	}
	if cur.String != schema {
		return fmt.Errorf("users: current schema is %q, want %s", cur.String, schema)
	}
	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version BIGINT NOT NULL)`); err != nil {
		return fmt.Errorf("users: schema_version: %w", err)
	}
	var v int
	err = conn.QueryRowContext(ctx, `SELECT version FROM schema_version`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		if err := pgApply(ctx, conn, pgBaseline, `INSERT INTO schema_version (version) VALUES (?)`, pgBaselineVersion); err != nil {
			return fmt.Errorf("users: postgres baseline (version %d): %w", pgBaselineVersion, err)
		}
		v = pgBaselineVersion
	} else if err != nil {
		return fmt.Errorf("users: read schema_version: %w", err)
	}
	if v > len(migrations) {
		return fmt.Errorf("users: database schema version %d is newer than this binary supports (%d)", v, len(migrations))
	}
	if v < pgBaselineVersion {
		return fmt.Errorf("users: Postgres schema version %d is below the baseline %d", v, pgBaselineVersion)
	}
	for i := v; i < len(migrations); i++ {
		if err := pgApply(ctx, conn, pgMigrations[i-pgBaselineVersion], `UPDATE schema_version SET version = ?`, i+1); err != nil {
			return fmt.Errorf("users: migration %d: %w", i+1, err)
		}
	}
	return nil
}

// pgGrantReader grants UsersReaderRole USAGE on schema and SELECT on
// pgReaderTables if the role exists. GRANT is idempotent; the connecting
// role owns the schema and its tables (pgMigrateLocked checked the schema).
func pgGrantReader(ctx context.Context, conn *sql.Conn, schema string) error {
	var exists bool
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = ?)`, UsersReaderRole).Scan(&exists); err != nil {
		return fmt.Errorf("users: look up role %s: %w", UsersReaderRole, err)
	}
	if !exists {
		return nil
	}
	stmts := []string{`GRANT USAGE ON SCHEMA "` + schema + `" TO ` + UsersReaderRole}
	for _, t := range pgReaderTables {
		stmts = append(stmts, `GRANT SELECT ON "`+schema+`".`+t+` TO `+UsersReaderRole)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("users: grant %s: %w", UsersReaderRole, err)
	}
	defer tx.Rollback()
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			var pe interface{ SQLState() string }
			if errors.As(err, &pe) && pe.SQLState() == pgUndefinedObject {
				return nil // dropped since the check: nothing to grant to
			}
			return fmt.Errorf("users: grant %s: %w", UsersReaderRole, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("users: grant %s: %w", UsersReaderRole, err)
	}
	return nil
}

// pgApply runs stmts and then record (with version) in one transaction.
func pgApply(ctx context.Context, conn *sql.Conn, stmts []string, record string, version int) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range stmts {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, record, version); err != nil {
		return err
	}
	return tx.Commit()
}
