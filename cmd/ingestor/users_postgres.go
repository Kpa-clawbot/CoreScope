package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/meshcore-analyzer/dbx"
)

// On PostgreSQL the accounts may be in the corescope_users schema of the same
// database (internal/users.PostgresSchema, userManagement.dsn on the server)
// or still in users.db (a single server replica with a volume). The ingestor
// cannot see the server's choice (the accounts DSN never reaches it), so it
// asks the database on every read: while the schema exists it reads
// proposals and companion_links there, which its role may only SELECT
// through the corescope_users_reader group; while it does not, it reads
// users.db exactly as on SQLite. Like the SQLite path it never imports
// internal/users; TestUsersPGSchemaMatchesUsersPackage pins the name.
//
// The accounts role owns those two tables, so it can turn one into a view or
// give it row-level security, and any function either calls would run as the
// writer. The reads therefore never touch the writer's pool: each set has an
// accountsReader of its own (one connection, opened on first use), whose
// session is read-only, searches pg_catalog only and is bounded by timeouts;
// every read locks the table, proves it is a plain table before selecting
// from it, and leaves no advisory lock behind.

// usersPGSchema is the accounts schema. A variable only so a test can point
// the readers at a throwaway schema: the real one has a fixed name shared by
// every test on the database.
var usersPGSchema = "corescope_users"

// accountsReadTimeout bounds one read, connecting included. The session's
// own timeouts (accountsReaderParams) end a stuck statement first.
const accountsReadTimeout = 3 * time.Second

// accountsReaderParams are the startup parameters of the reader's session.
// They replace any the DSN sets: every query qualifies its schema (and pins
// pg_catalog), so search_path holds nothing the accounts role can create in.
var accountsReaderParams = map[string]string{
	"default_transaction_read_only":       "on",
	"search_path":                         "pg_catalog",
	"statement_timeout":                   "2000",
	"lock_timeout":                        "1000",
	"idle_in_transaction_session_timeout": "5000",
	"row_security":                        "off",
}

// pgUsersQuery qualifies the table named after FROM with the accounts schema.
func pgUsersQuery(query, table string) string {
	return strings.Replace(query, "FROM "+table, `FROM "`+usersPGSchema+`".`+table, 1)
}

// accountsReader reads the accounts tables on a connection of its own, never
// the writer's. Its reads are serialized by its owner (the channel or linked
// companion set); mu only guards opening and closing.
type accountsReader struct {
	dsn string

	mu   sync.Mutex
	db   *sql.DB // nil until the first read, and after Close
	name string  // the pgx connection config registered for db
}

// newAccountsReader returns a reader for the database dsn names. Nothing is
// opened until the first read.
func newAccountsReader(dsn string) *accountsReader { return &accountsReader{dsn: dsn} }

func (r *accountsReader) open() (*sql.DB, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.db != nil {
		return r.db, nil
	}
	cfg, err := pgx.ParseConfig(r.dsn)
	if err != nil {
		// Not wrapped: pgx's parse error quotes the connection string.
		return nil, errors.New("accounts reader: unparseable Postgres DSN")
	}
	for k := range cfg.RuntimeParams {
		if _, ok := accountsReaderParams[strings.ToLower(k)]; ok {
			delete(cfg.RuntimeParams, k)
		}
	}
	for k, v := range accountsReaderParams {
		cfg.RuntimeParams[k] = v
	}
	name := stdlib.RegisterConnConfig(cfg)
	db, err := sql.Open(dbx.PostgresDriverName, name)
	if err != nil {
		stdlib.UnregisterConnConfig(name)
		return nil, fmt.Errorf("accounts reader: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	r.db, r.name = db, name
	return db, nil
}

// Close closes the reader's connection, if one was opened. A later read
// opens a new one.
func (r *accountsReader) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.db != nil {
		r.db.Close()
		stdlib.UnregisterConnConfig(r.name)
		r.db, r.name = nil, ""
	}
}

// session runs fn in a READ ONLY transaction on the reader's connection,
// within accountsReadTimeout, and rolls it back (nothing is written, and a
// session setting changed inside it reverts). Then it releases every session
// advisory lock on the connection, which a rollback does not; on any error,
// fn's included, the connection is discarded instead, so nothing taken on it
// outlives the read.
func (r *accountsReader) session(fn func(ctx context.Context, tx *sql.Tx) error) (err error) {
	db, err := r.open()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), accountsReadTimeout)
	defer cancel()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			conn.Raw(func(any) error { return driver.ErrBadConn }) // closes it for good
		}
		conn.Close()
	}()
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	if err := fn(ctx, tx); err != nil {
		tx.Rollback()
		return err
	}
	if err := tx.Rollback(); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `SELECT pg_catalog.pg_advisory_unlock_all()`)
	return err
}

// readAccounts reads one string column with query from table in the
// accounts schema. inPG is false when the schema does not exist (the
// accounts are in users.db); nothing else is read then. Otherwise the table
// is locked against changes, checked by plainAccountsTable, and only then
// queried. cols are the columns query references.
func (r *accountsReader) readAccounts(table string, cols []string, query string, args ...any) (out []string, inPG bool, err error) {
	err = r.session(func(ctx context.Context, tx *sql.Tx) error {
		// The schema is created only by an operator opting accounts into
		// PostgreSQL (scripts/postgres-roles.sh with an accounts password,
		// or the server's first start with userManagement.dsn), so its
		// absence means users.db. pg_namespace is readable by every role.
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname = ?)`, usersPGSchema).Scan(&inPG); err != nil {
			return err
		}
		if !inPG {
			return nil
		}
		qualified := `"` + usersPGSchema + `".` + table
		// ACCESS SHARE conflicts only with ACCESS EXCLUSIVE, which every
		// change plainAccountsTable looks for needs, so what it checks is
		// what the SELECT reads. lock_timeout bounds the wait.
		if _, err := tx.ExecContext(ctx, `LOCK TABLE `+qualified+` IN ACCESS SHARE MODE`); err != nil {
			return err
		}
		ok, err := plainAccountsTable(ctx, tx, table, cols)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%s is not a plain table; refusing to read it as the writer", qualified)
		}
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, false, err
	}
	return out, inPG, nil
}

// plainAccountsTable reports whether table in the accounts schema is one
// whose SELECT can run no code the accounts role wrote: an ordinary table
// (not a view, partitioned or foreign table), without row-level security,
// rules or inheritance children, whose columns cols have pg_catalog types
// (a type of the accounts role's could carry an implicit cast to a function
// of its own into the comparison) and are not virtual generated columns.
func plainAccountsTable(ctx context.Context, tx *sql.Tx, table string, cols []string) (bool, error) {
	var ok bool
	err := tx.QueryRowContext(ctx, `
		SELECT c.relkind = 'r' AND NOT c.relrowsecurity AND NOT c.relhassubclass
		   AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_rewrite w WHERE w.ev_class = c.oid)
		   AND NOT EXISTS (
		       SELECT 1 FROM pg_catalog.pg_attribute a
		       JOIN pg_catalog.pg_type t ON t.oid = a.atttypid
		       WHERE a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
		         AND a.attname = ANY (?::pg_catalog.name[])
		         AND (t.typnamespace <> 'pg_catalog'::pg_catalog.regnamespace OR a.attgenerated = 'v'))
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = ? AND c.relname = ?`, cols, usersPGSchema, table).Scan(&ok)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return ok, err
}

// isMissingTable reports a table that does not exist: SQLite's wording or
// PostgreSQL's undefined_table (a missing schema reads differently and is an
// ordinary failure).
func isMissingTable(err error) bool {
	if err == nil {
		return false
	}
	m := err.Error()
	return strings.Contains(m, "no such table") ||
		(strings.Contains(m, "relation ") && strings.Contains(m, "does not exist"))
}
