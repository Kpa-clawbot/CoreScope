package users

import (
	"database/sql"
	"errors"
	"time"
)

// PushoverSettings is a user's Pushover destination. The key is validated
// with Pushover before it is stored.
type PushoverSettings struct {
	UserID      int64
	UserKey     string
	Enabled     bool
	ValidatedAt time.Time
	UpdatedAt   time.Time
}

// PushoverMessage is one rendered notification for a user, queued with the
// evaluation that produced it.
type PushoverMessage struct {
	UserID         int64
	Title, Message string
	URL, URLTitle  string
	ExpiresAt      time.Time
}

// PushoverJob is a queued message with the user's current key.
type PushoverJob struct {
	ID             int64
	UserID         int64
	UserKey        string
	Title, Message string
	URL, URLTitle  string
	ExpiresAt      time.Time
	Attempts       int
}

// PushoverFor returns the user's settings; ok is false without a row.
func (s *Store) PushoverFor(userID int64) (PushoverSettings, bool, error) {
	var p PushoverSettings
	var validated, updated int64
	err := s.db.QueryRow(`SELECT user_id, user_key, enabled, validated_at, updated_at
		FROM notification_pushover WHERE user_id = ?`, userID).
		Scan(&p.UserID, &p.UserKey, &p.Enabled, &validated, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return PushoverSettings{}, false, nil
	}
	if err != nil {
		return PushoverSettings{}, false, err
	}
	p.ValidatedAt, p.UpdatedAt = fromUnix(validated), fromUnix(updated)
	return p, true, nil
}

// SetPushoverKey stores a validated key for an active user and turns
// Pushover on. A changed key drops messages queued for the old one.
func (s *Store) SetPushoverKey(userID int64, userKey string) (PushoverSettings, error) {
	now := s.now()
	tx, err := s.db.Begin()
	if err != nil {
		return PushoverSettings{}, err
	}
	defer tx.Rollback()
	if err := requireActivePushUser(tx, userID); err != nil {
		return PushoverSettings{}, err
	}
	if _, err := tx.Exec(`INSERT INTO notification_pushover (user_id, user_key, enabled, validated_at, updated_at)
		VALUES (?, ?, 1, ?, ?)
		ON CONFLICT(user_id) DO UPDATE SET user_key = excluded.user_key, enabled = 1,
			validated_at = excluded.validated_at, updated_at = excluded.updated_at`,
		userID, userKey, unix(now), unix(now)); err != nil {
		return PushoverSettings{}, err
	}
	if err := tx.Commit(); err != nil {
		return PushoverSettings{}, err
	}
	p, _, err := s.PushoverFor(userID)
	return p, err
}

// SetPushoverEnabled turns a stored destination on or off. Off drops its
// queued messages. ErrNotFound without a stored key.
func (s *Store) SetPushoverEnabled(userID int64, enabled bool) (PushoverSettings, error) {
	res, err := s.db.Exec(`UPDATE notification_pushover SET enabled = ?, updated_at = ? WHERE user_id = ?`,
		enabled, unix(s.now()), userID)
	if err != nil {
		return PushoverSettings{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return PushoverSettings{}, ErrNotFound
	}
	p, _, err := s.PushoverFor(userID)
	return p, err
}

// DeletePushover removes the user's key and queued messages.
func (s *Store) DeletePushover(userID int64) error {
	_, err := s.db.Exec(`DELETE FROM notification_pushover WHERE user_id = ?`, userID)
	return err
}

// DisablePushoverKey turns Pushover off after Pushover rejected userKey,
// unless the user has since saved another key. It reports whether it did.
func (s *Store) DisablePushoverKey(userID int64, userKey string) (bool, error) {
	res, err := s.db.Exec(`UPDATE notification_pushover SET enabled = 0, updated_at = ?
		WHERE user_id = ? AND user_key = ? AND enabled = 1`, unix(s.now()), userID, userKey)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func queuePushoverMessages(tx *sql.Tx, now time.Time, messages []PushoverMessage, perUserPerDay int) error {
	limit := perUserPerDay
	if limit <= 0 {
		limit = -1 // no per-user limit
	}
	stmt, err := tx.Prepare(`INSERT INTO notification_pushover_jobs
			(user_id, title, message, url, url_title, created_at, expires_at)
		SELECT ?, ?, ?, ?, ?, ?, ?
		WHERE EXISTS (SELECT 1 FROM notification_pushover o JOIN users u ON u.id = o.user_id
				WHERE o.user_id = ? AND o.enabled = 1 AND u.status = 'active')
			AND (? < 0 OR (SELECT COUNT(*) FROM notification_pushover_sent WHERE user_id = ? AND sent_at >= ?)
				+ (SELECT COUNT(*) FROM notification_pushover_jobs WHERE user_id = ? AND expires_at > ?) < ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	since := unix(now.Add(-24 * time.Hour))
	for _, m := range messages {
		if !m.ExpiresAt.After(now) {
			continue
		}
		if _, err := stmt.Exec(m.UserID, m.Title, m.Message, m.URL, m.URLTitle, unix(now), unix(m.ExpiresAt), m.UserID,
			limit, m.UserID, since, m.UserID, unix(now), limit); err != nil {
			return err
		}
	}
	return nil
}

// PrunePushoverJobs deletes expired messages.
func (s *Store) PrunePushoverJobs(now time.Time) error {
	_, err := s.db.Exec(`DELETE FROM notification_pushover_jobs WHERE expires_at <= ?`, unix(now))
	return err
}

// DuePushoverJobs returns up to limit due, unexpired messages for active
// users with Pushover on, with their current key. Expired messages are
// left for PrunePushoverJobs.
func (s *Store) DuePushoverJobs(now time.Time, limit int) ([]PushoverJob, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.db.Query(`SELECT j.id, j.user_id, o.user_key, j.title, j.message, j.url, j.url_title, j.expires_at, j.attempts
		FROM notification_pushover_jobs j
		JOIN notification_pushover o ON o.user_id = j.user_id AND o.enabled = 1
		JOIN users u ON u.id = j.user_id AND u.status = 'active'
		WHERE j.next_at <= ? AND j.expires_at > ?
		ORDER BY j.next_at, j.id LIMIT ?`, unix(now), unix(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []PushoverJob
	for rows.Next() {
		var j PushoverJob
		var expires int64
		if err := rows.Scan(&j.ID, &j.UserID, &j.UserKey, &j.Title, &j.Message, &j.URL, &j.URLTitle, &expires, &j.Attempts); err != nil {
			return nil, err
		}
		j.ExpiresAt = fromUnix(expires)
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// CompletePushoverJob removes a job; sent also records the send, for the
// monthly cap and the user's daily limit.
func (s *Store) CompletePushoverJob(id int64, sent bool) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if sent {
		if _, err := tx.Exec(`INSERT INTO notification_pushover_sent (user_id, sent_at)
			SELECT user_id, ? FROM notification_pushover_jobs WHERE id = ?`, unix(s.now()), id); err != nil {
			return err
		}
		if err := prunePushoverSent(tx, s.now()); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM notification_pushover_jobs WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RetryPushoverJob(id int64, next time.Time) error {
	_, err := s.db.Exec(`UPDATE notification_pushover_jobs SET next_at = ?, attempts = attempts + 1 WHERE id = ?`, unix(next), id)
	return err
}

// RecordPushoverSend counts a message sent to userID outside the queue (a
// test).
func (s *Store) RecordPushoverSend(userID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO notification_pushover_sent (user_id, sent_at) VALUES (?, ?)`, userID, unix(s.now())); err != nil {
		return err
	}
	if err := prunePushoverSent(tx, s.now()); err != nil {
		return err
	}
	return tx.Commit()
}

// prunePushoverSent keeps the send log a little longer than the 30 days
// the cap looks back.
func prunePushoverSent(tx *sql.Tx, now time.Time) error {
	_, err := tx.Exec(`DELETE FROM notification_pushover_sent WHERE sent_at < ?`, unix(now.Add(-32*24*time.Hour)))
	return err
}

// PushoverSentSince counts messages sent at or after since.
func (s *Store) PushoverSentSince(since time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM notification_pushover_sent WHERE sent_at >= ?`, unix(since)).Scan(&n)
	return n, err
}

// PushoverSentToUserSince counts messages sent to userID at or after since.
func (s *Store) PushoverSentToUserSince(userID int64, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM notification_pushover_sent WHERE user_id = ? AND sent_at >= ?`,
		userID, unix(since)).Scan(&n)
	return n, err
}
