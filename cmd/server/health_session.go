package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	_ "modernc.org/sqlite"
)

const (
	SessionStatusWaiting   = "waiting"
	SessionStatusActive    = "active"
	SessionStatusExhausted = "exhausted"
	SessionStatusExpired   = "expired"
)

type HealthSession struct {
	ID                   string          `json:"id"`
	Code                 string          `json:"code"`
	CreatedAt            int64           `json:"createdAt"`
	ExpiresAt            int64           `json:"expiresAt"`
	ResultRetainedUntil  int64           `json:"resultRetainedUntil"`
	Status               string          `json:"status"`
	UseCount             int             `json:"useCount"`
	MaxUses              int             `json:"maxUses"`
	MessageHash          string          `json:"messageHash,omitempty"`
	MessageBody          string          `json:"messageBody,omitempty"`
	Sender               string          `json:"sender,omitempty"`
	ChannelHash          string          `json:"channelHash,omitempty"`
	MatchedAt            int64           `json:"matchedAt,omitempty"`
	AllowlistEnabled     bool            `json:"allowlistEnabled"`
	ExpectedObserverKeys []string        `json:"expectedObserverKeys"`
	Receipts             []HealthReceipt `json:"receipts,omitempty"`
}

type HealthReceipt struct {
	ID           int64    `json:"id,omitempty"`
	SessionID    string   `json:"sessionId"`
	ObserverKey  string   `json:"observerKey"`
	ObserverName string   `json:"observerName,omitempty"`
	FirstSeenAt  int64    `json:"firstSeenAt"`
	LastSeenAt   int64    `json:"lastSeenAt"`
	Count        int      `json:"count"`
	MessageHash  string   `json:"messageHash,omitempty"`
	RSSI         float64  `json:"rssi,omitempty"`
	SNR          float64  `json:"snr,omitempty"`
	Duration     float64  `json:"duration,omitempty"`
	Path         []string `json:"path,omitempty"`
}

type HealthDB struct {
	db *sql.DB
}

func healthDBPath(mainDBPath string) string {
	ext := filepath.Ext(mainDBPath)
	return strings.TrimSuffix(mainDBPath, ext) + "-health" + ext
}

func OpenHealthDB(path string) (*HealthDB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)
	hdb := &HealthDB{db: db}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}
	if err := hdb.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return hdb, nil
}

func (h *HealthDB) Close() error {
	h.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")
	return h.db.Close()
}

const codeChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

func generateHealthCode() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = codeChars[int(b[i])%len(codeChars)]
	}
	return "MHC-" + string(b), nil
}

func newHealthID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("health: crypto/rand.Read failed: " + err.Error())
	}
	return fmt.Sprintf("%x%x%x%x%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func (h *HealthDB) CreateSession(cfg *HealthCheckConfig, allowlistEnabled bool, expectedKeys []string) (*HealthSession, error) {
	now := time.Now().Unix()
	code, err := generateHealthCode()
	if err != nil {
		return nil, err
	}
	if expectedKeys == nil {
		expectedKeys = []string{}
	}
	keysJSON, _ := json.Marshal(expectedKeys)

	sess := &HealthSession{
		ID:                   newHealthID(),
		Code:                 code,
		CreatedAt:            now,
		ExpiresAt:            now + int64(cfg.SessionTTLSeconds),
		ResultRetainedUntil:  now + int64(cfg.ResultRetentionSeconds),
		Status:               SessionStatusWaiting,
		MaxUses:              cfg.MaxUsesPerSession,
		AllowlistEnabled:     allowlistEnabled,
		ExpectedObserverKeys: expectedKeys,
	}

	_, err = h.db.Exec(`
		INSERT INTO health_sessions
			(id, code, created_at, expires_at, result_retained_until, status, max_uses, allowlist_enabled, expected_observer_keys)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sess.ID, sess.Code, sess.CreatedAt, sess.ExpiresAt, sess.ResultRetainedUntil,
		sess.Status, sess.MaxUses, boolToInt(allowlistEnabled), string(keysJSON),
	)
	return sess, err
}

func (h *HealthDB) GetSession(id string) (*HealthSession, error) {
	row := h.db.QueryRow(`
		SELECT id, code, created_at, expires_at, result_retained_until, status,
		       use_count, max_uses, COALESCE(message_hash,''), COALESCE(message_body,''),
		       COALESCE(sender,''), COALESCE(channel_hash,''), COALESCE(matched_at,0),
		       allowlist_enabled, COALESCE(expected_observer_keys,'[]')
		FROM health_sessions WHERE id = ?`, id)

	var s HealthSession
	var allowlistInt int
	var keysJSON string
	err := row.Scan(&s.ID, &s.Code, &s.CreatedAt, &s.ExpiresAt, &s.ResultRetainedUntil,
		&s.Status, &s.UseCount, &s.MaxUses, &s.MessageHash, &s.MessageBody,
		&s.Sender, &s.ChannelHash, &s.MatchedAt, &allowlistInt, &keysJSON)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.AllowlistEnabled = allowlistInt != 0
	json.Unmarshal([]byte(keysJSON), &s.ExpectedObserverKeys)

	s.Receipts, err = h.GetReceipts(id)
	return &s, err
}

func (h *HealthDB) GetReceipts(sessionID string) ([]HealthReceipt, error) {
	rows, err := h.db.Query(`
		SELECT id, session_id, observer_key, COALESCE(observer_name,''),
		       first_seen_at, last_seen_at, count, COALESCE(message_hash,''),
		       COALESCE(rssi,0), COALESCE(snr,0), COALESCE(duration,0), COALESCE(path,'[]')
		FROM health_receipts WHERE session_id = ? ORDER BY first_seen_at ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var receipts []HealthReceipt
	for rows.Next() {
		var r HealthReceipt
		var pathJSON string
		err := rows.Scan(&r.ID, &r.SessionID, &r.ObserverKey, &r.ObserverName,
			&r.FirstSeenAt, &r.LastSeenAt, &r.Count, &r.MessageHash,
			&r.RSSI, &r.SNR, &r.Duration, &pathJSON)
		if err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(pathJSON), &r.Path)
		receipts = append(receipts, r)
	}
	return receipts, rows.Err()
}

// UpsertReceipt records a new receipt (or updates existing) and transitions session status.
// Increments use_count only on first receipt per unique observer.
func (h *HealthDB) UpsertReceipt(sessionID string, r HealthReceipt, msgHash string) error {
	now := time.Now().Unix()
	pathJSON, _ := json.Marshal(r.Path)

	_, err := h.db.Exec(`
		INSERT INTO health_receipts (session_id, observer_key, observer_name, first_seen_at, last_seen_at, count, message_hash, rssi, snr, duration, path)
		VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?)
		ON CONFLICT(session_id, observer_key) DO UPDATE SET
			last_seen_at  = excluded.last_seen_at,
			count         = count + 1,
			rssi          = excluded.rssi,
			snr           = excluded.snr,
			duration      = excluded.duration,
			path          = excluded.path,
			observer_name = excluded.observer_name`,
		sessionID, r.ObserverKey, r.ObserverName, now, now, msgHash,
		r.RSSI, r.SNR, r.Duration, string(pathJSON),
	)
	if err != nil {
		return err
	}

	// Count distinct observers that have reported for this session
	var distinctObs int
	h.db.QueryRow(`SELECT COUNT(DISTINCT observer_key) FROM health_receipts WHERE session_id = ?`, sessionID).Scan(&distinctObs)

	var status string
	var maxUses int
	var matchedAt int64
	h.db.QueryRow(`SELECT status, max_uses, COALESCE(matched_at,0) FROM health_sessions WHERE id = ?`, sessionID).
		Scan(&status, &maxUses, &matchedAt)

	newStatus := status
	if status == SessionStatusWaiting {
		newStatus = SessionStatusActive
		matchedAt = now
	}
	if distinctObs >= maxUses {
		newStatus = SessionStatusExhausted
	}

	_, err = h.db.Exec(`
		UPDATE health_sessions
		SET use_count = ?, status = ?, message_hash = ?, matched_at = ?
		WHERE id = ?`,
		distinctObs, newStatus, msgHash, matchedAt, sessionID,
	)
	return err
}

// LoadActiveSessions returns all sessions with status waiting or active that haven't expired.
func (h *HealthDB) LoadActiveSessions() ([]*HealthSession, error) {
	now := time.Now().Unix()
	rows, err := h.db.Query(`
		SELECT id, code, expires_at, status, max_uses, use_count,
		       COALESCE(message_hash,''), allowlist_enabled, COALESCE(expected_observer_keys,'[]')
		FROM health_sessions
		WHERE status IN ('waiting','active') AND expires_at > ?`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []*HealthSession
	for rows.Next() {
		var s HealthSession
		var allowlistInt int
		var keysJSON string
		rows.Scan(&s.ID, &s.Code, &s.ExpiresAt, &s.Status, &s.MaxUses, &s.UseCount,
			&s.MessageHash, &allowlistInt, &keysJSON)
		s.AllowlistEnabled = allowlistInt != 0
		json.Unmarshal([]byte(keysJSON), &s.ExpectedObserverKeys)
		sessions = append(sessions, &s)
	}
	return sessions, rows.Err()
}

// ExpireStale moves waiting/active sessions past their expires_at to expired status.
func (h *HealthDB) ExpireStale() error {
	_, err := h.db.Exec(`
		UPDATE health_sessions SET status = 'expired'
		WHERE status IN ('waiting','active') AND expires_at <= ?`, time.Now().Unix())
	return err
}

// PurgeExpired deletes sessions (and their receipts) past result_retained_until.
func (h *HealthDB) PurgeExpired() error {
	now := time.Now().Unix()
	_, err := h.db.Exec(`
		DELETE FROM health_receipts WHERE session_id IN (
			SELECT id FROM health_sessions WHERE result_retained_until < ?
		)`, now)
	if err != nil {
		return err
	}
	_, err = h.db.Exec(`DELETE FROM health_sessions WHERE result_retained_until < ?`, now)
	return err
}

// ClearReceipts deletes all receipts for a session and resets its use_count to
// 0 and status to waiting. Called when the same code is re-broadcast (new use).
func (h *HealthDB) ClearReceipts(sessionID string) error {
	_, err := h.db.Exec(`DELETE FROM health_receipts WHERE session_id = ?`, sessionID)
	if err != nil {
		return err
	}
	_, err = h.db.Exec(`UPDATE health_sessions SET use_count = 0, status = 'waiting', message_hash = NULL, matched_at = NULL WHERE id = ?`, sessionID)
	return err
}

// IncrementUseCount increments the use_count for a session by 1.
func (h *HealthDB) IncrementUseCount(sessionID string) error {
	_, err := h.db.Exec(`UPDATE health_sessions SET use_count = use_count + 1 WHERE id = ?`, sessionID)
	return err
}

// SetMessageHash stores the message hash and sender for a session (called once
// when the first matching packet for a new use is seen).
func (h *HealthDB) SetMessageHash(sessionID, msgHash, sender string) error {
	now := time.Now().Unix()
	_, err := h.db.Exec(`
		UPDATE health_sessions SET message_hash = ?, sender = ?, matched_at = ?, status = 'active'
		WHERE id = ? AND (message_hash IS NULL OR message_hash = '')`,
		msgHash, sender, now, sessionID)
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (h *HealthDB) migrate() error {
	_, err := h.db.Exec(`
		CREATE TABLE IF NOT EXISTS health_sessions (
			id                     TEXT PRIMARY KEY,
			code                   TEXT UNIQUE NOT NULL,
			created_at             INTEGER NOT NULL,
			expires_at             INTEGER NOT NULL,
			result_retained_until  INTEGER NOT NULL,
			status                 TEXT NOT NULL,
			use_count              INTEGER DEFAULT 0,
			max_uses               INTEGER NOT NULL,
			message_hash           TEXT,
			message_body           TEXT,
			sender                 TEXT,
			channel_hash           TEXT,
			matched_at             INTEGER,
			allowlist_enabled      INTEGER DEFAULT 0,
			expected_observer_keys TEXT
		);
		CREATE TABLE IF NOT EXISTS health_receipts (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id    TEXT    NOT NULL REFERENCES health_sessions(id),
			observer_key  TEXT    NOT NULL,
			observer_name TEXT,
			first_seen_at INTEGER NOT NULL,
			last_seen_at  INTEGER NOT NULL,
			count         INTEGER DEFAULT 1,
			message_hash  TEXT,
			rssi          REAL,
			snr           REAL,
			duration      REAL,
			path          TEXT,
			UNIQUE(session_id, observer_key)
		);
	`)
	return err
}
