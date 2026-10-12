package users

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

// Session kinds (sessions.kind).
const (
	SessionKindWeb    = "web"
	SessionKindDevice = "device"
)

// DeviceSessionTTL is a device token's lifetime, sliding on use: callers
// pass it to ExtendSession whenever the token is used.
const DeviceSessionTTL = 90 * 24 * time.Hour

// Session is one logged-in browser or device. The raw token exists only in
// the cookie (web) or on the device (bearer).
type Session struct {
	ID         int64
	UserID     int64
	CSRFToken  string // filled but unused for device sessions
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
	UserAgent  string
	Kind       string   // SessionKindWeb or SessionKindDevice
	Label      string   // device name; "" for web sessions
	Scopes     []string // nil = full access (web sessions)
}

// HasScope reports whether the session may use scope. Web sessions have
// full access; device sessions only their listed scopes.
func (s *Session) HasScope(scope string) bool {
	if s.Kind != SessionKindDevice {
		return true
	}
	for _, sc := range s.Scopes {
		if sc == scope {
			return true
		}
	}
	return false
}

const maxUserAgent = 200

const maxLabel = 64

// CleanLabel makes a user-supplied device or companion name safe to store:
// invalid UTF-8 and control characters removed, trimmed, at most 64 runes.
func CleanLabel(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxLabel {
		s = strings.TrimSpace(string(r[:maxLabel]))
	}
	return s
}

func validScope(sc string) bool {
	if sc == "" {
		return false
	}
	for _, r := range sc {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == ':' || r == '.' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func splitScopes(v string) []string {
	if v == "" {
		return nil
	}
	return strings.Split(v, ",")
}

const sessionCols = `id, user_id, csrf_token, created_at, expires_at, last_seen_at, user_agent, kind, label, scopes`

func scanSession(row rowScanner) (*Session, error) {
	var s Session
	var created, expires, seen int64
	var scopes string
	if err := row.Scan(&s.ID, &s.UserID, &s.CSRFToken, &created, &expires, &seen, &s.UserAgent, &s.Kind, &s.Label, &scopes); err != nil {
		return nil, err
	}
	s.CreatedAt, s.ExpiresAt, s.LastSeenAt = fromUnix(created), fromUnix(expires), fromUnix(seen)
	s.Scopes = splitScopes(scopes)
	return &s, nil
}

// CreateSession starts a web session and returns the raw cookie token.
func (s *Store) CreateSession(userID int64, ttl time.Duration, userAgent string) (string, *Session, error) {
	return s.createSession(userID, ttl, userAgent, SessionKindWeb, "", nil)
}

// CreateDeviceSession issues a device (bearer) token for userID, limited to
// scopes and valid for DeviceSessionTTL. label is cleaned with CleanLabel.
func (s *Store) CreateDeviceSession(userID int64, label string, scopes []string, userAgent string) (string, *Session, error) {
	if len(scopes) == 0 {
		return "", nil, ErrNoScopes
	}
	for _, sc := range scopes {
		if !validScope(sc) {
			return "", nil, fmt.Errorf("users: invalid scope %q", sc)
		}
	}
	return s.createSession(userID, DeviceSessionTTL, userAgent, SessionKindDevice, CleanLabel(label), scopes)
}

func (s *Store) createSession(userID int64, ttl time.Duration, userAgent, kind, label string, scopes []string) (string, *Session, error) {
	raw, hash, err := NewToken()
	if err != nil {
		return "", nil, err
	}
	csrf, _, err := NewToken()
	if err != nil {
		return "", nil, err
	}
	if len(userAgent) > maxUserAgent {
		userAgent = strings.ToValidUTF8(userAgent[:maxUserAgent], "")
	}
	now := s.now()
	res, err := s.db.Exec(`INSERT INTO sessions (token_hash, user_id, csrf_token, created_at, expires_at, last_seen_at, user_agent, kind, label, scopes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, hash, userID, csrf, unix(now), unix(now.Add(ttl)), unix(now), userAgent,
		kind, label, strings.Join(scopes, ","))
	if err != nil {
		return "", nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return "", nil, err
	}
	return raw, &Session{ID: id, UserID: userID, CSRFToken: csrf, CreatedAt: fromUnix(unix(now)),
		ExpiresAt: fromUnix(unix(now.Add(ttl))), LastSeenAt: fromUnix(unix(now)), UserAgent: userAgent,
		Kind: kind, Label: label, Scopes: splitScopes(strings.Join(scopes, ","))}, nil
}

// LookupSession resolves a raw cookie token. Unknown and expired sessions
// return ErrNotFound; expired ones are deleted on the way.
func (s *Store) LookupSession(raw string) (*Session, error) {
	sess, err := scanSession(s.db.QueryRow(`SELECT `+sessionCols+` FROM sessions WHERE token_hash = ?`, HashToken(raw)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("users: lookup session: %w", err)
	}
	if !s.now().Before(sess.ExpiresAt) {
		_, _ = s.db.Exec(`DELETE FROM sessions WHERE id = ?`, sess.ID)
		return nil, ErrNotFound
	}
	return sess, nil
}

// ExtendSession marks the session seen now and moves its expiry to now+ttl.
func (s *Store) ExtendSession(id int64, ttl time.Duration) error {
	now := s.now()
	return expectOne(s.db.Exec(`UPDATE sessions SET last_seen_at = ?, expires_at = ? WHERE id = ?`,
		unix(now), unix(now.Add(ttl)), id))
}

func (s *Store) DeleteSessionByToken(raw string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, HashToken(raw))
	return err
}

// DeleteSession revokes one session, only if it belongs to userID.
func (s *Store) DeleteSession(userID, sessionID int64) error {
	return expectOne(s.db.Exec(`DELETE FROM sessions WHERE id = ? AND user_id = ?`, sessionID, userID))
}

// DeleteUserSessions revokes all of a user's sessions except exceptID (0 = none kept).
func (s *Store) DeleteUserSessions(userID, exceptID int64) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE user_id = ? AND id != ?`, userID, exceptID)
	return err
}

// ListSessions returns a user's sessions, most recently seen first.
func (s *Store) ListSessions(userID int64) ([]Session, error) {
	rows, err := s.db.Query(`SELECT `+sessionCols+` FROM sessions WHERE user_id = ? ORDER BY last_seen_at DESC, id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sess)
	}
	return out, rows.Err()
}

func (s *Store) PruneExpiredSessions() (int64, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, unix(s.now()))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
