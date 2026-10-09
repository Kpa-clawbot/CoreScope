// Package users is CoreScope's optional PostgreSQL account store. Account
// state lives in a separate database from telemetry; runtime connections
// never bootstrap schemas or write telemetry (#1283).
package users

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/meshcore-analyzer/pgutil"
)

var (
	ErrNotFound     = errors.New("users: not found")
	ErrEmailTaken   = errors.New("users: email already registered")
	ErrTokenInvalid = errors.New("users: token invalid or already used")
	ErrTokenExpired = errors.New("users: token expired")
	// ErrAccountChanged: the account is no longer pending with the
	// password hash the caller verified (a re-register or an admin got
	// there first).
	ErrAccountChanged = errors.New("users: account changed since it was read")
)

// A SQL NULL limit means every row for full account exports.
var noLimit any = nil

// Store owns a separate account database. Safe for concurrent use.
type Store struct {
	db          *sql.DB
	databaseURL string
	now         func() time.Time
}

// Open connects using the account runtime role, never applying migrations.
// Every forbidden URL is checked against the effective PostgreSQL database,
// so alternate hostnames, credentials or search paths cannot bypass isolation.
func Open(databaseURL string, forbidden ...string) (*Store, error) {
	if _, err := pgutil.ParseConfig(databaseURL); err != nil {
		return nil, err
	}
	for _, other := range forbidden {
		if strings.TrimSpace(other) == "" {
			continue
		}
		same, err := pgutil.SameDatabase(context.Background(), databaseURL, other)
		if err != nil {
			return nil, err
		}
		if same {
			return nil, errors.New("users: refusing to open the measurement database")
		}
	}
	db, err := pgutil.Open(databaseURL, false)
	if err != nil {
		return nil, err
	}
	// Account traffic is small. Keep baseline per-process concurrency while
	// database locks also protect transactions across independent processes.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := AssertReady(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := assertRuntimePrivileges(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, databaseURL: databaseURL, now: time.Now}, nil
}

func assertRuntimePrivileges(db *sql.DB) error {
	var elevated bool
	err := db.QueryRow(`SELECT
		EXISTS (SELECT 1 FROM pg_roles WHERE rolname=current_user AND (rolsuper OR rolcreaterole OR rolcreatedb OR rolreplication OR rolbypassrls))
		OR has_database_privilege(current_database(),'CREATE')
		OR has_schema_privilege(current_schema(),'CREATE')
		OR EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON c.relnamespace=n.oid
			WHERE n.nspname=current_schema() AND pg_has_role(c.relowner,'USAGE'))
		OR has_table_privilege('corescope_schema','INSERT,UPDATE,DELETE,TRUNCATE,TRIGGER')
		OR has_table_privilege('schema_version','INSERT,UPDATE,DELETE,TRUNCATE,TRIGGER')`).Scan(&elevated)
	if err != nil {
		return errors.New("users: cannot verify account runtime role privileges")
	}
	if elevated {
		return errors.New("users: account runtime role must be separate from the migration owner and cannot modify schema metadata")
	}
	return nil
}

// lockUser serializes account-level read/modify/write operations, including
// the initial insert where no settings, tokens or watch rows exist yet.
func lockUser(tx *sql.Tx, id int64) error {
	var found int64
	err := tx.QueryRow(`SELECT id FROM users WHERE id=$1 FOR UPDATE`, id).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// SetClock replaces the time source. Tests only.
func (s *Store) SetClock(now func() time.Time) { s.now = now }

type rowScanner interface{ Scan(dest ...any) error }

func unix(t time.Time) int64     { return t.Unix() }
func fromUnix(v int64) time.Time { return time.Unix(v, 0).UTC() }

func fromNullUnix(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromUnix(v.Int64)
	return &t
}

func nullInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func expectOne(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgerr *pgconn.PgError
	return errors.As(err, &pgerr) && pgerr.Code == "23505"
}
