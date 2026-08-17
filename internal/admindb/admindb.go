// Package admindb owns the server's admin-account and session store.
//
// It is the ONE writable database in the whole system that the server
// (cmd/server) itself opens read-write. Unlike meshcore.db — which the
// server only ever opens mode=ro, per the invariant enforced by
// cmd/server/readonly_invariant_test.go — admin.db holds no mesh data at
// all, just admin accounts and login sessions, so the read-only
// invariant for meshcore.db is untouched. Living in its own module
// (rather than directly in cmd/server/*.go) also keeps the writer call
// site out of the files that invariant test scans.
package admindb

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
	_ "modernc.org/sqlite"
)

// Role distinguishes admins who can manage the site from super-admins,
// who can additionally create new admin accounts.
type Role string

const (
	RoleAdmin      Role = "admin"
	RoleSuperAdmin Role = "super_admin"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool {
	return r == RoleAdmin || r == RoleSuperAdmin
}

// Admin is an admin account, never carrying its password hash outward.
type Admin struct {
	ID        int64
	Username  string
	Role      Role
	Disabled  bool
	CreatedAt time.Time
	CreatedBy *int64
}

// ErrInvalidCredentials is returned by Authenticate for any failure —
// unknown username, wrong password, or a disabled account — so callers
// never leak which case occurred (no username enumeration).
var ErrInvalidCredentials = errors.New("invalid username or password")

// ErrUsernameTaken is returned by CreateAdmin on a duplicate username.
var ErrUsernameTaken = errors.New("username already taken")

// ErrSessionInvalid is returned by ValidateSession for an unknown or
// expired session token.
var ErrSessionInvalid = errors.New("session invalid or expired")

// ErrPasswordTooShort is returned by ChangePassword when newPassword is
// under minPasswordLen.
var ErrPasswordTooShort = errors.New("password must be at least 8 characters")

// minPasswordLen is the minimum length enforced on a newly *chosen*
// password (ChangePassword). Existing accounts and CreateAdmin are left
// as-is — this only guards the one path where an admin is actively
// picking their own new password.
const minPasswordLen = 8

// sessionTTL is how long a session stays valid after its last use;
// ValidateSession slides this window forward on every successful check.
const sessionTTL = 24 * time.Hour

// bcryptCost is one step above bcrypt.DefaultCost (10). This is a
// low-traffic admin panel, not a consumer service hashing thousands of
// logins/sec, so the extra ~4x hashing time is free in practice and
// buys real brute-force margin. Safe to raise further later — bcrypt
// embeds its cost in the hash string itself, so existing hashes keep
// verifying correctly no matter what this constant changes to.
const bcryptCost = 12

// Store wraps a read-write SQLite connection dedicated to admin.db.
type Store struct {
	db *sql.DB
}

// Open opens (creating if necessary) the admin database at path and
// ensures its schema exists. Safe to call repeatedly / idempotent.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open admin db: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite single-writer; avoid SQLITE_BUSY under concurrent admin writes
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping admin db: %w", err)
	}
	if err := ensureSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("ensure admin db schema: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

func ensureSchema(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS admins (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
			password_hash TEXT NOT NULL,
			role          TEXT NOT NULL CHECK(role IN ('admin','super_admin')),
			disabled      INTEGER NOT NULL DEFAULT 0,
			created_at    TEXT NOT NULL,
			created_by    INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS admin_sessions (
			token_hash   TEXT PRIMARY KEY,
			admin_id     INTEGER NOT NULL REFERENCES admins(id),
			created_at   TEXT NOT NULL,
			expires_at   TEXT NOT NULL,
			last_seen_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_admin_sessions_admin_id ON admin_sessions(admin_id)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("exec %q: %w", stmt, err)
		}
	}
	return nil
}

// CreateAdmin hashes password and inserts a new admin account.
// createdBy is the ID of the super-admin who created it, or nil for the
// bootstrap account created by cmd/admin.
func (s *Store) CreateAdmin(username, password string, role Role, createdBy *int64) (*Admin, error) {
	username = strings.TrimSpace(username)
	if username == "" {
		return nil, errors.New("username is required")
	}
	if password == "" {
		return nil, errors.New("password is required")
	}
	if !role.Valid() {
		return nil, fmt.Errorf("invalid role %q", role)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return nil, fmt.Errorf("hash password: %w", err)
	}
	now := time.Now().UTC()
	res, err := s.db.Exec(
		`INSERT INTO admins (username, password_hash, role, disabled, created_at, created_by) VALUES (?, ?, ?, 0, ?, ?)`,
		username, string(hash), string(role), now.Format(time.RFC3339), createdBy,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, ErrUsernameTaken
		}
		return nil, fmt.Errorf("insert admin: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("last insert id: %w", err)
	}
	return &Admin{ID: id, Username: username, Role: role, CreatedAt: now, CreatedBy: createdBy}, nil
}

// Authenticate verifies username/password and returns the matching
// account. Returns ErrInvalidCredentials for any failure — unknown
// user, wrong password, or a disabled account.
func (s *Store) Authenticate(username, password string) (*Admin, error) {
	row := s.db.QueryRow(
		`SELECT id, username, password_hash, role, disabled, created_at, created_by FROM admins WHERE username = ? COLLATE NOCASE`,
		strings.TrimSpace(username),
	)
	var (
		a         Admin
		hash      string
		disabled  int
		createdAt string
		createdBy sql.NullInt64
	)
	if err := row.Scan(&a.ID, &a.Username, &hash, &a.Role, &disabled, &createdAt, &createdBy); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalidCredentials
		}
		return nil, fmt.Errorf("query admin: %w", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return nil, ErrInvalidCredentials
	}
	if disabled != 0 {
		return nil, ErrInvalidCredentials
	}
	a.Disabled = disabled != 0
	if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
		a.CreatedAt = t
	}
	if createdBy.Valid {
		id := createdBy.Int64
		a.CreatedBy = &id
	}
	return &a, nil
}

// ChangePassword verifies oldPassword against adminID's stored hash,
// then replaces it with newPassword. The current password is always
// required, even when called on behalf of the account's own owner —
// a stolen or idle session cookie alone must not be enough to change
// (and thereby lock out) the account. Returns ErrInvalidCredentials if
// oldPassword doesn't match (never distinguishes that from "unknown
// admin", consistent with Authenticate), or ErrPasswordTooShort if
// newPassword is under the minimum length.
func (s *Store) ChangePassword(adminID int64, oldPassword, newPassword string) error {
	if len(newPassword) < minPasswordLen {
		return ErrPasswordTooShort
	}
	var hash string
	if err := s.db.QueryRow(`SELECT password_hash FROM admins WHERE id = ?`, adminID).Scan(&hash); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvalidCredentials
		}
		return fmt.Errorf("query admin: %w", err)
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(oldPassword)); err != nil {
		return ErrInvalidCredentials
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcryptCost)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	if _, err := s.db.Exec(`UPDATE admins SET password_hash = ? WHERE id = ?`, string(newHash), adminID); err != nil {
		return fmt.Errorf("update password: %w", err)
	}
	return nil
}

// CreateSession issues a new random session token for adminID and
// returns the raw token (only its SHA-256 hash is persisted).
func (s *Store) CreateSession(adminID int64) (token string, expiresAt time.Time, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("generate session token: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	now := time.Now().UTC()
	expiresAt = now.Add(sessionTTL)
	_, err = s.db.Exec(
		`INSERT INTO admin_sessions (token_hash, admin_id, created_at, expires_at, last_seen_at) VALUES (?, ?, ?, ?, ?)`,
		hashToken(token), adminID, now.Format(time.RFC3339), expiresAt.Format(time.RFC3339), now.Format(time.RFC3339),
	)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("insert session: %w", err)
	}
	return token, expiresAt, nil
}

// ValidateSession looks up token and returns the associated admin if
// the session exists and has not expired. On success it slides the
// session's expiry forward by sessionTTL.
func (s *Store) ValidateSession(token string) (*Admin, error) {
	if token == "" {
		return nil, ErrSessionInvalid
	}
	th := hashToken(token)
	row := s.db.QueryRow(
		`SELECT a.id, a.username, a.role, a.disabled, a.created_at, a.created_by, s.expires_at
		 FROM admin_sessions s JOIN admins a ON a.id = s.admin_id
		 WHERE s.token_hash = ?`,
		th,
	)
	var (
		a         Admin
		disabled  int
		createdAt string
		createdBy sql.NullInt64
		expiresAt string
	)
	if err := row.Scan(&a.ID, &a.Username, &a.Role, &disabled, &createdAt, &createdBy, &expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSessionInvalid
		}
		return nil, fmt.Errorf("query session: %w", err)
	}
	exp, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil || time.Now().UTC().After(exp) {
		return nil, ErrSessionInvalid
	}
	if disabled != 0 {
		return nil, ErrSessionInvalid
	}
	a.Disabled = disabled != 0
	if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
		a.CreatedAt = t
	}
	if createdBy.Valid {
		id := createdBy.Int64
		a.CreatedBy = &id
	}

	now := time.Now().UTC()
	newExpiry := now.Add(sessionTTL).Format(time.RFC3339)
	if _, err := s.db.Exec(
		`UPDATE admin_sessions SET last_seen_at = ?, expires_at = ? WHERE token_hash = ?`,
		now.Format(time.RFC3339), newExpiry, th,
	); err != nil {
		return nil, fmt.Errorf("slide session expiry: %w", err)
	}

	return &a, nil
}

// DeleteSession removes a session (logout). Deleting an unknown token
// is a no-op, not an error.
func (s *Store) DeleteSession(token string) error {
	if token == "" {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM admin_sessions WHERE token_hash = ?`, hashToken(token))
	if err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// DeleteOtherSessions removes every session belonging to adminID except
// the one matching keepToken (pass "" to remove all of them). Called
// after a password change so any other logged-in session — e.g. on a
// device that's since been lost, or if the old password had leaked —
// doesn't silently stay valid; the session making the change itself
// stays logged in.
func (s *Store) DeleteOtherSessions(adminID int64, keepToken string) error {
	keep := ""
	if keepToken != "" {
		keep = hashToken(keepToken)
	}
	_, err := s.db.Exec(`DELETE FROM admin_sessions WHERE admin_id = ? AND token_hash != ?`, adminID, keep)
	if err != nil {
		return fmt.Errorf("delete other sessions: %w", err)
	}
	return nil
}

// ListAdmins returns every admin account, ordered by creation time.
func (s *Store) ListAdmins() ([]*Admin, error) {
	rows, err := s.db.Query(`SELECT id, username, role, disabled, created_at, created_by FROM admins ORDER BY created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("query admins: %w", err)
	}
	defer rows.Close()

	var out []*Admin
	for rows.Next() {
		var (
			a         Admin
			disabled  int
			createdAt string
			createdBy sql.NullInt64
		)
		if err := rows.Scan(&a.ID, &a.Username, &a.Role, &disabled, &createdAt, &createdBy); err != nil {
			return nil, fmt.Errorf("scan admin: %w", err)
		}
		a.Disabled = disabled != 0
		if t, err := time.Parse(time.RFC3339, createdAt); err == nil {
			a.CreatedAt = t
		}
		if createdBy.Valid {
			id := createdBy.Int64
			a.CreatedBy = &id
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
