package users

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

type Role string

const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool { return r == RoleUser || r == RoleAdmin }

type Status string

const (
	StatusPending  Status = "pending"
	StatusActive   Status = "active"
	StatusDisabled Status = "disabled"
)

// User is one account. PasswordHash never leaves the server.
type User struct {
	ID            int64
	Email         string
	DisplayName   string
	PasswordHash  string
	Role          Role
	Status        Status
	CreatedAt     time.Time
	ActivatedAt   *time.Time
	ActivatedBy   *int64 // admin who activated manually; nil = activated by link
	LastLoginAt   *time.Time
	EmailBouncing bool
}

const userCols = `id, email, display_name, password_hash, role, status, created_at, activated_at, activated_by, last_login_at, email_bouncing`

func scanUser(row rowScanner) (*User, error) {
	var u User
	var role, status string
	var created int64
	var activatedAt, activatedBy, lastLogin sql.NullInt64
	var bouncing int
	if err := row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.PasswordHash, &role, &status,
		&created, &activatedAt, &activatedBy, &lastLogin, &bouncing); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	u.Role, u.Status = Role(role), Status(status)
	u.CreatedAt = fromUnix(created)
	u.ActivatedAt = fromNullUnix(activatedAt)
	if activatedBy.Valid {
		v := activatedBy.Int64
		u.ActivatedBy = &v
	}
	u.LastLoginAt = fromNullUnix(lastLogin)
	u.EmailBouncing = bouncing != 0
	return &u, nil
}

// CreatePending inserts a new pending user. email must already be normalized.
func (s *Store) CreatePending(email, displayName, passwordHash string) (*User, error) {
	res, err := s.db.Exec(`INSERT INTO users (email, display_name, password_hash, role, status, created_at)
		VALUES (?, ?, ?, 'user', 'pending', ?)`, email, displayName, passwordHash, unix(s.now()))
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrEmailTaken
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetByID(id)
}

func (s *Store) GetByID(id int64) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

// GetByEmail looks up a normalized address.
func (s *Store) GetByEmail(email string) (*User, error) {
	return scanUser(s.db.QueryRow(`SELECT `+userCols+` FROM users WHERE email = ?`, email))
}

// Activate moves a pending user to active with role. by is the admin who
// activated manually, nil for link activation. ErrNotFound if not pending.
func (s *Store) Activate(id int64, role Role, by *int64) error {
	return expectOne(s.db.Exec(`UPDATE users SET status = 'active', role = ?, activated_at = ?, activated_by = ?
		WHERE id = ? AND status = 'pending'`, string(role), unix(s.now()), nullInt(by), id))
}

func (s *Store) SetStatus(id int64, st Status) error {
	return expectOne(s.db.Exec(`UPDATE users SET status = ? WHERE id = ?`, string(st), id))
}

func (s *Store) SetRole(id int64, r Role) error {
	return expectOne(s.db.Exec(`UPDATE users SET role = ? WHERE id = ?`, string(r), id))
}

func (s *Store) SetPassword(id int64, hash string) error {
	return expectOne(s.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, hash, id))
}

func (s *Store) SetDisplayName(id int64, name string) error {
	return expectOne(s.db.Exec(`UPDATE users SET display_name = ? WHERE id = ?`, name, id))
}

// SetEmail changes the address (normalized) and clears the bounce flag.
func (s *Store) SetEmail(id int64, email string) error {
	err := expectOne(s.db.Exec(`UPDATE users SET email = ?, email_bouncing = 0 WHERE id = ?`, email, id))
	if isUniqueViolation(err) {
		return ErrEmailTaken
	}
	return err
}

func (s *Store) SetEmailBouncing(id int64, v bool) error {
	b := 0
	if v {
		b = 1
	}
	return expectOne(s.db.Exec(`UPDATE users SET email_bouncing = ? WHERE id = ?`, b, id))
}

func (s *Store) TouchLogin(id int64) error {
	return expectOne(s.db.Exec(`UPDATE users SET last_login_at = ? WHERE id = ?`, unix(s.now()), id))
}

// Delete removes a user; sessions and tokens cascade. Mail-log rows survive
// for delivery forensics, with the address replaced by HashedEmail.
func (s *Store) Delete(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow(`SELECT 1 FROM users WHERE id = ?`, id).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if err := hashMailLogTx(tx, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM users WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// ListFilter narrows List. Zero values mean "any".
type ListFilter struct {
	Status Status
	Role   Role
	Query  string // substring of email or display name, case-insensitive
}

// List returns at most 1000 users, newest first. SQLite lower()/LIKE fold
// ASCII only, so a non-ASCII search is case-sensitive.
func (s *Store) List(f ListFilter) ([]User, error) {
	q := `SELECT ` + userCols + ` FROM users WHERE 1=1`
	var args []any
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, string(f.Status))
	}
	if f.Role != "" {
		q += ` AND role = ?`
		args = append(args, string(f.Role))
	}
	if t := strings.TrimSpace(f.Query); t != "" {
		like := "%" + escapeLike(strings.ToLower(t)) + "%"
		q += ` AND (email LIKE ? ESCAPE '\' OR lower(display_name) LIKE ? ESCAPE '\')`
		args = append(args, like, like)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT 1000`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// CountActiveAdmins counts admins whose status is active.
func (s *Store) CountActiveAdmins() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'admin' AND status = 'active'`).Scan(&n)
	return n, err
}

// UsersByID loads the users with the given ids in one query. Ids that no
// longer exist are absent from the map.
func (s *Store) UsersByID(ids []int64) (map[int64]User, error) {
	out := make(map[int64]User, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	rows, err := s.db.Query(`SELECT `+userCols+` FROM users WHERE id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out[u.ID] = *u
	}
	return out, rows.Err()
}

// PruneStalePending deletes pending accounts older than maxAge that have no
// unused, unexpired activation token left.
func (s *Store) PruneStalePending(maxAge time.Duration) (int64, error) {
	now := unix(s.now())
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id FROM users WHERE status = 'pending' AND created_at < ?
		AND NOT EXISTS (SELECT 1 FROM tokens t WHERE t.user_id = users.id AND t.purpose = 'activate'
			AND t.used_at IS NULL AND t.expires_at > ?)`, now-int64(maxAge/time.Second), now)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := hashMailLogTx(tx, id); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(`DELETE FROM users WHERE id = ?`, id); err != nil {
			return 0, err
		}
	}
	return int64(len(ids)), tx.Commit()
}

// hashMailLogTx replaces the plaintext address of every mail_log row of the
// user with HashedEmail of that row's own address (rows may predate an email
// change). Rows already hashed are skipped. The cursor is closed before any
// UPDATE because the store uses a single connection.
func hashMailLogTx(tx *sql.Tx, userID int64) error {
	rows, err := tx.Query(`SELECT id, to_email FROM mail_log WHERE user_id = ? AND to_email NOT LIKE 'sha256:%'`, userID)
	if err != nil {
		return err
	}
	type row struct {
		id    int64
		email string
	}
	var list []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.email); err != nil {
			rows.Close()
			return err
		}
		list = append(list, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range list {
		if _, err := tx.Exec(`UPDATE mail_log SET to_email = ? WHERE id = ?`, HashedEmail(r.email), r.id); err != nil {
			return err
		}
	}
	return nil
}
