// Package users is CoreScope's optional account store (user management,
// docs/specs/2026-10-06-user-management-design.md). It owns users.db, a
// SQLite file separate from the measurement database. cmd/server never
// writes measurement data (#1283); this package is the single, opt-in
// exception, and Open refuses to touch the measurement database by path.
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
	ErrNoScopes = errors.New("users: a device session needs at least one scope")
)

// noLimit is SQLite's LIMIT value for "every row" (the account export).
const noLimit = -1

// Store is the users.db handle. Safe for concurrent use.
type Store struct {
	db  *sql.DB
	now func() time.Time
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
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}
