// Package users is CoreScope's optional account store (user management,
// docs/specs/2026-10-06-user-management-design.md). It owns users.db, a
// SQLite file separate from the measurement database, or on PostgreSQL the
// schema corescope_users (OpenPostgres). cmd/server never writes
// measurement data (#1283); this package is the single, opt-in exception,
// and Open refuses to touch the measurement database by path.
//
// Both backends run the same SQL except where noted (forUpdate, lockTable,
// the migrations). On SQLite the store has one connection, so every
// read-modify-write is serial; on Postgres the pool is larger and several
// server replicas may share the schema, so each such sequence is made
// atomic in SQL: a conditional statement, or a transaction that locks first.
package users

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
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
	// ErrNoScopes: a device session must name what it may do; an empty
	// scope list would mean full (web) access.
	ErrNoScopes  = errors.New("users: a device session needs at least one scope")
	ErrBadPubkey = errors.New("users: pubkey must be 64 hex characters")
	// Link challenges (companion linking). A challenge is consumed by every
	// lookup, whichever of these it returns.
	ErrChallengeMissing  = errors.New("users: link challenge unknown or already used")
	ErrChallengeExpired  = errors.New("users: link challenge expired")
	ErrChallengeMismatch = errors.New("users: link challenge bound to another user or pubkey")
	// ErrMQTTLoginExists: the user already has an observer MQTT login.
	ErrMQTTLoginExists = errors.New("users: observer login already exists")
)

// noLimit asks a capped list for every row (the account export): the query
// then has no LIMIT clause.
const noLimit = 0

// Store is the users.db handle. Safe for concurrent use.
type Store struct {
	db  *sql.DB
	now func() time.Time
	pg  bool
	// closeHook runs after the database is closed (the Postgres store
	// unregisters its connection config).
	closeHook func()
}

// Open opens (creating if needed) the users database at path and applies
// pending migrations. forbidden lists paths Open must refuse; the server
// passes the measurement DB path so a misconfigured dbPath can never turn
// this package into a writer of measurement data.
func Open(path string, forbidden ...string) (*Store, error) {
	if strings.ContainsAny(path, "?#") {
		return nil, fmt.Errorf("users: database path %q must not contain '?' or '#'", path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("users: resolve %s: %w", path, err)
	}
	for _, f := range forbidden {
		if strings.TrimSpace(f) != "" && samePath(abs, f) {
			return nil, fmt.Errorf("users: refusing to open %s: it is the measurement database", path)
		}
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return nil, fmt.Errorf("users: create dir for %s: %w", path, err)
	}
	db, err := sql.Open("sqlite", abs+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("users: open %s: %w", path, err)
	}
	// Account traffic is tiny; one connection removes SQLITE_BUSY between
	// our own writers and keeps the per-connection pragmas in force.
	db.SetMaxOpenConns(1)
	s := &Store{db: db, now: time.Now}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func samePath(abs, other string) bool {
	oa, err := filepath.Abs(other)
	if err != nil {
		return false
	}
	a, errA := os.Stat(abs)
	b, errB := os.Stat(oa)
	if errA == nil && errB == nil {
		return os.SameFile(a, b)
	}
	return strings.EqualFold(filepath.Clean(abs), filepath.Clean(oa))
}

// Close closes the database.
func (s *Store) Close() error {
	err := s.db.Close()
	if s.closeHook != nil {
		s.closeHook()
		s.closeHook = nil
	}
	return err
}

// IsPostgres reports whether the store was opened with OpenPostgres.
func (s *Store) IsPostgres() bool { return s.pg }

// forUpdate is appended to a SELECT inside a transaction that goes on to
// write what it read: on Postgres it locks the selected rows until commit.
// SQLite has no row locks and needs none (one connection, serial).
func (s *Store) forUpdate() string {
	if s.pg {
		return ` FOR UPDATE`
	}
	return ``
}

// lockTable serializes the writers of table for the rest of tx on Postgres
// (readers are not blocked). For checks that span rows, such as a count
// against a limit, where no single row lock would do. A no-op on SQLite.
func (s *Store) lockTable(tx *sql.Tx, table string) error {
	if !s.pg {
		return nil
	}
	_, err := tx.Exec(`LOCK TABLE ` + table + ` IN SHARE ROW EXCLUSIVE MODE`)
	return err
}

// lockUser serializes, on Postgres, the transactions that read and then
// rewrite rows belonging to one user (FOR NO KEY UPDATE, so inserts that
// only reference the user through a foreign key are not blocked). A user
// that does not exist locks nothing; the write that follows then fails on
// its foreign key as it would on SQLite. A no-op on SQLite.
func (s *Store) lockUser(tx *sql.Tx, userID int64) error {
	if !s.pg {
		return nil
	}
	rows, err := tx.Query(`SELECT id FROM users WHERE id = ? FOR NO KEY UPDATE`, userID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
	}
	return rows.Err()
}

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

// pgUniqueViolation is Postgres' SQLSTATE unique_violation.
const pgUniqueViolation = "23505"

// isUniqueViolation recognises a unique-constraint failure on either
// backend: SQLite's message, or a Postgres error (pgconn.PgError) carrying
// SQLSTATE 23505.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var pe interface{ SQLState() string }
	if errors.As(err, &pe) {
		return pe.SQLState() == pgUniqueViolation
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// sqlMax2 is SQLite's two-argument max(a, b) in portable SQL: NULL when
// either side is NULL (Postgres' GREATEST would ignore the NULL).
func sqlMax2(a, b string) string {
	return `(CASE WHEN (` + a + `) IS NULL OR (` + b + `) IS NULL THEN NULL WHEN (` + a + `) >= (` + b + `) THEN (` + a + `) ELSE (` + b + `) END)`
}

// limitClause returns " LIMIT ?" and its argument, or nothing for noLimit.
func limitClause(limit int) (string, []any) {
	if limit == noLimit {
		return ``, nil
	}
	return ` LIMIT ?`, []any{limit}
}
