package users

import (
	"strings"
	"time"
)

// Session is one logged-in device. The raw token exists only in the cookie.
type Session struct {
	ID         int64
	UserID     int64
	CSRFToken  string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	LastSeenAt time.Time
	UserAgent  string
}

const maxUserAgent = 200

const sessionCols = `id, user_id, csrf_token, created_at, expires_at, last_seen_at, user_agent`

func scanSession(row rowScanner) (*Session, error) {
	var s Session
	var created, expires, seen int64
	if err := row.Scan(&s.ID, &s.UserID, &s.CSRFToken, &created, &expires, &seen, &s.UserAgent); err != nil {
		return nil, err
	}
	s.CreatedAt, s.ExpiresAt, s.LastSeenAt = fromUnix(created), fromUnix(expires), fromUnix(seen)
	return &s, nil
}

// CreateSession starts a session and returns the raw cookie token.
func (s *Store) CreateSession(userID int64, ttl time.Duration, userAgent string) (string, *Session, error) {
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
	res, err := s.db.Exec(`INSERT INTO sessions (token_hash, user_id, csrf_token, created_at, expires_at, last_seen_at, user_agent)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, hash, userID, csrf, unix(now), unix(now.Add(ttl)), unix(now), userAgent)
	if err != nil {
		return "", nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return "", nil, err
	}
	return raw, &Session{ID: id, UserID: userID, CSRFToken: csrf, CreatedAt: fromUnix(unix(now)),
		ExpiresAt: fromUnix(unix(now.Add(ttl))), LastSeenAt: fromUnix(unix(now)), UserAgent: userAgent}, nil
}

// LookupSession resolves a raw cookie token. Unknown and expired sessions
// return ErrNotFound; expired ones are deleted on the way.
func (s *Store) LookupSession(raw string) (*Session, error) {
	sess, err := scanSession(s.db.QueryRow(`SELECT `+sessionCols+` FROM sessions WHERE token_hash = ?`, HashToken(raw)))
	if err != nil {
		return nil, ErrNotFound
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
