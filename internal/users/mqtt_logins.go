package users

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// NormalizeObserverKey returns an observer's public key as 64 uppercase hex
// characters, or ErrBadPubkey.
func NormalizeObserverKey(s string) (string, error) {
	s = strings.ToUpper(strings.TrimSpace(s))
	if len(s) != 64 {
		return "", ErrBadPubkey
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", ErrBadPubkey
	}
	return s, nil
}

// MQTTLogin is a user's personal MQTT credential for their observers.
// PasswordHash is the hex sha256 the broker check compares against;
// PasswordEnc is the SecretBox-sealed password for later display.
type MQTTLogin struct {
	UserID       int64
	Username     string
	PasswordHash string
	PasswordEnc  string
	CreatedAt    time.Time
	RevokedAt    *time.Time
	LastAuthAt   *time.Time
}

// ObserverBinding ties an observer pubkey to the user who owns it. A nil
// UserID means the owner was deleted and the key is orphaned.
type ObserverBinding struct {
	Pubkey  string
	UserID  *int64
	BoundAt time.Time
	BoundBy string
	// ConfirmedAt is when the owner's own login first published under this
	// pubkey; nil until then (admin assignments start unconfirmed).
	ConfirmedAt *time.Time
}

const mqttLoginCols = `user_id, username, password_hash, password_enc, created_at, revoked_at, last_auth_at`

func scanMQTTLogin(row rowScanner) (*MQTTLogin, error) {
	var l MQTTLogin
	var created int64
	var revoked, lastAuth sql.NullInt64
	if err := row.Scan(&l.UserID, &l.Username, &l.PasswordHash, &l.PasswordEnc, &created, &revoked, &lastAuth); err != nil {
		return nil, err
	}
	l.CreatedAt = fromUnix(created)
	l.RevokedAt = fromNullUnix(revoked)
	l.LastAuthAt = fromNullUnix(lastAuth)
	return &l, nil
}

// CreateMQTTLogin stores a user's first login. ErrMQTTLoginExists when the
// user already has one; a username held by another user is a plain error.
func (s *Store) CreateMQTTLogin(l MQTTLogin) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var one int
	err = tx.QueryRow(`SELECT 1 FROM mqtt_logins WHERE user_id = ?`, l.UserID).Scan(&one)
	if err == nil {
		return ErrMQTTLoginExists
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("users: check mqtt login: %w", err)
	}
	if _, err := tx.Exec(`INSERT INTO mqtt_logins (user_id, username, password_hash, password_enc, created_at) VALUES (?, ?, ?, ?, ?)`,
		l.UserID, l.Username, l.PasswordHash, l.PasswordEnc, unix(s.now())); err != nil {
		return fmt.Errorf("users: create mqtt login: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("users: create mqtt login: %w", err)
	}
	return nil
}

// ReissueMQTTLogin replaces the user's username, hash and sealed password,
// clears the revocation and restarts created_at. ErrNotFound when the user
// has no login.
func (s *Store) ReissueMQTTLogin(l MQTTLogin) error {
	res, err := s.db.Exec(`UPDATE mqtt_logins SET username = ?, password_hash = ?, password_enc = ?, created_at = ?, revoked_at = NULL WHERE user_id = ?`,
		l.Username, l.PasswordHash, l.PasswordEnc, unix(s.now()), l.UserID)
	if err != nil {
		return fmt.Errorf("users: reissue mqtt login: %w", err)
	}
	return expectOne(res, nil)
}

// MQTTLoginForUser returns the user's login, or ErrNotFound.
func (s *Store) MQTTLoginForUser(userID int64) (*MQTTLogin, error) {
	l, err := scanMQTTLogin(s.db.QueryRow(`SELECT `+mqttLoginCols+` FROM mqtt_logins WHERE user_id = ?`, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("users: get mqtt login: %w", err)
	}
	return l, nil
}

// AllMQTTLogins returns every login, ordered by user id.
func (s *Store) AllMQTTLogins() ([]MQTTLogin, error) {
	rows, err := s.db.Query(`SELECT ` + mqttLoginCols + ` FROM mqtt_logins ORDER BY user_id`)
	if err != nil {
		return nil, fmt.Errorf("users: list mqtt logins: %w", err)
	}
	defer rows.Close()
	var out []MQTTLogin
	for rows.Next() {
		l, err := scanMQTTLogin(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

// RevokeMQTTLogin marks the user's login revoked now. ErrNotFound when the
// user has no login.
func (s *Store) RevokeMQTTLogin(userID int64) error {
	return expectOne(s.db.Exec(`UPDATE mqtt_logins SET revoked_at = ? WHERE user_id = ?`, unix(s.now()), userID))
}

// TouchMQTTAuth records a successful broker authentication at at.
func (s *Store) TouchMQTTAuth(userID int64, at time.Time) error {
	return expectOne(s.db.Exec(`UPDATE mqtt_logins SET last_auth_at = ? WHERE user_id = ?`, unix(at), userID))
}

// BindObserver claims pubkey for userID unless a row exists already (first
// claim wins). owner is the stored owner after the call: nil when the stored
// row is orphaned. created is true only when this call inserted the row. by
// is 'auto' or 'admin'.
func (s *Store) BindObserver(pubkey string, userID int64, by string) (owner *int64, created bool, err error) {
	pk, err := NormalizeObserverKey(pubkey)
	if err != nil {
		return nil, false, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	now := unix(s.now())
	var confirmed any
	if by == "auto" {
		confirmed = now
	}
	res, err := tx.Exec(`INSERT INTO observer_bindings (pubkey, user_id, bound_at, bound_by, confirmed_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(pubkey) DO NOTHING`, pk, userID, now, by, confirmed)
	if err != nil {
		return nil, false, fmt.Errorf("users: bind observer: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, false, err
	}
	var stored sql.NullInt64
	if err := tx.QueryRow(`SELECT user_id FROM observer_bindings WHERE pubkey = ?`, pk).Scan(&stored); err != nil {
		return nil, false, fmt.Errorf("users: read observer binding: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("users: bind observer: %w", err)
	}
	if stored.Valid {
		owner = &stored.Int64
	}
	return owner, n > 0, nil
}

// SetObserverOwner is the admin reassignment: a non-nil userID takes the key
// over (bound_by 'admin', fresh bound_at; confirmed_at survives only when the
// owner is unchanged); nil unbinds it by deleting the row.
func (s *Store) SetObserverOwner(pubkey string, userID *int64) error {
	pk, err := NormalizeObserverKey(pubkey)
	if err != nil {
		return err
	}
	if userID == nil {
		if _, err := s.db.Exec(`DELETE FROM observer_bindings WHERE pubkey = ?`, pk); err != nil {
			return fmt.Errorf("users: unbind observer: %w", err)
		}
		return nil
	}
	if _, err := s.db.Exec(`INSERT INTO observer_bindings (pubkey, user_id, bound_at, bound_by) VALUES (?, ?, ?, 'admin')
		ON CONFLICT(pubkey) DO UPDATE SET
			confirmed_at = CASE WHEN observer_bindings.user_id = excluded.user_id THEN observer_bindings.confirmed_at ELSE NULL END,
			user_id = excluded.user_id, bound_at = excluded.bound_at, bound_by = 'admin'`,
		pk, *userID, unix(s.now())); err != nil {
		return fmt.Errorf("users: reassign observer: %w", err)
	}
	return nil
}

// ConfirmObserver marks the binding confirmed now when userID owns it and it
// is still unconfirmed. It reports whether this call set confirmed_at.
func (s *Store) ConfirmObserver(pubkey string, userID int64) (bool, error) {
	pk, err := NormalizeObserverKey(pubkey)
	if err != nil {
		return false, err
	}
	res, err := s.db.Exec(`UPDATE observer_bindings SET confirmed_at = ? WHERE pubkey = ? AND user_id = ? AND confirmed_at IS NULL`,
		unix(s.now()), pk, userID)
	if err != nil {
		return false, fmt.Errorf("users: confirm observer: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func scanBinding(row rowScanner) (ObserverBinding, error) {
	var b ObserverBinding
	var uid, confirmed sql.NullInt64
	var at int64
	if err := row.Scan(&b.Pubkey, &uid, &at, &b.BoundBy, &confirmed); err != nil {
		return b, err
	}
	if uid.Valid {
		b.UserID = &uid.Int64
	}
	b.BoundAt = fromUnix(at)
	b.ConfirmedAt = fromNullUnix(confirmed)
	return b, nil
}

func (s *Store) queryBindings(where string, args ...any) ([]ObserverBinding, error) {
	rows, err := s.db.Query(`SELECT pubkey, user_id, bound_at, bound_by, confirmed_at FROM observer_bindings `+where+` ORDER BY pubkey`, args...)
	if err != nil {
		return nil, fmt.Errorf("users: list observer bindings: %w", err)
	}
	defer rows.Close()
	var out []ObserverBinding
	for rows.Next() {
		b, err := scanBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// ObserverBindings returns every binding, orphaned ones included.
func (s *Store) ObserverBindings() ([]ObserverBinding, error) {
	return s.queryBindings(``)
}

// ObserverBindingsForUser returns the bindings owned by userID.
func (s *Store) ObserverBindingsForUser(userID int64) ([]ObserverBinding, error) {
	return s.queryBindings(`WHERE user_id = ?`, userID)
}
