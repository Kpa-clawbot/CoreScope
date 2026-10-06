package users

import (
	"database/sql"
	"errors"
	"time"
)

// MailEvent is one provider delivery event.
type MailEvent struct {
	Event  string
	At     time.Time
	Reason string
}

// MailRecord is one sent mail with its delivery summary and history.
type MailRecord struct {
	ID                int64
	UserID            *int64
	ToEmail           string
	Purpose           string
	ProviderMessageID string
	SentAt            time.Time
	LastEvent         string
	LastEventAt       time.Time
	LastReason        string
	Events            []MailEvent // chronological; filled by MailByID / MailForUser
}

const mailCols = `id, user_id, to_email, purpose, provider_message_id, sent_at, last_event, last_event_at, last_reason`

func scanMail(row rowScanner) (*MailRecord, error) {
	var m MailRecord
	var uid sql.NullInt64
	var msgID sql.NullString
	var sent, lastAt int64
	if err := row.Scan(&m.ID, &uid, &m.ToEmail, &m.Purpose, &msgID, &sent, &m.LastEvent, &lastAt, &m.LastReason); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if uid.Valid {
		v := uid.Int64
		m.UserID = &v
	}
	m.ProviderMessageID = msgID.String
	m.SentAt, m.LastEventAt = fromUnix(sent), fromUnix(lastAt)
	return &m, nil
}

// LogMail records a sent mail. messageID may be empty if the provider gave none.
func (s *Store) LogMail(userID *int64, to, purpose, messageID string) (int64, error) {
	now := unix(s.now())
	var mid any
	if messageID != "" {
		mid = messageID
	}
	res, err := s.db.Exec(`INSERT INTO mail_log (user_id, to_email, purpose, provider_message_id, sent_at, last_event, last_event_at)
		VALUES (?, ?, ?, ?, ?, 'sent', ?)`, nullInt(userID), to, purpose, mid, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// RecordMailEvent appends a provider event to the mail with that provider
// message id. Duplicates (same event and time) are ignored; the summary
// columns follow the newest event. found is false for unknown ids.
func (s *Store) RecordMailEvent(messageID, event string, at time.Time, reason string) (userID *int64, found bool, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	var id, lastAt int64
	var uid sql.NullInt64
	err = tx.QueryRow(`SELECT id, user_id, last_event_at FROM mail_log WHERE provider_message_id = ?`, messageID).
		Scan(&id, &uid, &lastAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO mail_events (mail_id, event, at, reason) VALUES (?, ?, ?, ?)`,
		id, event, unix(at), reason); err != nil {
		return nil, false, err
	}
	if unix(at) >= lastAt {
		if _, err := tx.Exec(`UPDATE mail_log SET last_event = ?, last_event_at = ?, last_reason = ? WHERE id = ?`,
			event, unix(at), reason, id); err != nil {
			return nil, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	if uid.Valid {
		v := uid.Int64
		userID = &v
	}
	return userID, true, nil
}

func (s *Store) eventsFor(mailID int64) ([]MailEvent, error) {
	rows, err := s.db.Query(`SELECT event, at, reason FROM mail_events WHERE mail_id = ? ORDER BY at, rowid`, mailID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MailEvent
	for rows.Next() {
		var e MailEvent
		var at int64
		if err := rows.Scan(&e.Event, &at, &e.Reason); err != nil {
			return nil, err
		}
		e.At = fromUnix(at)
		out = append(out, e)
	}
	return out, rows.Err()
}

// MailByID returns one record with its events.
func (s *Store) MailByID(id int64) (*MailRecord, error) {
	m, err := scanMail(s.db.QueryRow(`SELECT `+mailCols+` FROM mail_log WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	if m.Events, err = s.eventsFor(m.ID); err != nil {
		return nil, err
	}
	return m, nil
}

// MailForUser returns a user's mails, newest first, each with its events.
func (s *Store) MailForUser(userID int64, limit int) ([]MailRecord, error) {
	rows, err := s.db.Query(`SELECT `+mailCols+` FROM mail_log WHERE user_id = ? ORDER BY sent_at DESC, id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	var out []MailRecord
	for rows.Next() {
		m, err := scanMail(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, *m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].Events, err = s.eventsFor(out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// LatestMailByUser maps user id → that user's most recent mail (no events).
func (s *Store) LatestMailByUser() (map[int64]MailRecord, error) {
	rows, err := s.db.Query(`SELECT ` + mailCols + ` FROM mail_log m WHERE user_id IS NOT NULL
		AND id = (SELECT MAX(id) FROM mail_log WHERE user_id = m.user_id)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]MailRecord{}
	for rows.Next() {
		m, err := scanMail(rows)
		if err != nil {
			return nil, err
		}
		out[*m.UserID] = *m
	}
	return out, rows.Err()
}

// PruneMail deletes mail records (and their events) older than maxAge.
func (s *Store) PruneMail(maxAge time.Duration) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM mail_log WHERE sent_at < ?`, unix(s.now())-int64(maxAge/time.Second))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
