package users

import (
	"database/sql"
	"errors"
	"fmt"
)

// migrations[i] upgrades the schema from version i to i+1. Forward-only:
// never edit a shipped entry, append a new one.
var migrations = [][]string{
	{ // v1: sub-project A
		`CREATE TABLE users (
			id INTEGER PRIMARY KEY,
			email TEXT NOT NULL UNIQUE,
			display_name TEXT NOT NULL,
			password_hash TEXT NOT NULL,
			role TEXT NOT NULL DEFAULT 'user' CHECK (role IN ('user','admin')),
			status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','active','disabled')),
			created_at INTEGER NOT NULL,
			activated_at INTEGER,
			activated_by INTEGER,
			last_login_at INTEGER,
			email_bouncing INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE sessions (
			id INTEGER PRIMARY KEY,
			token_hash TEXT NOT NULL UNIQUE,
			user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			csrf_token TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			last_seen_at INTEGER NOT NULL,
			user_agent TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX sessions_user ON sessions(user_id)`,
		`CREATE TABLE tokens (
			token_hash TEXT PRIMARY KEY,
			user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
			purpose TEXT NOT NULL CHECK (purpose IN ('activate','reset','email_change')),
			new_email TEXT,
			expires_at INTEGER NOT NULL,
			used_at INTEGER
		)`,
		`CREATE INDEX tokens_user ON tokens(user_id, purpose)`,
		`CREATE TABLE audit_log (
			id INTEGER PRIMARY KEY,
			at INTEGER NOT NULL,
			actor_user_id INTEGER,
			action TEXT NOT NULL,
			target_user_id INTEGER,
			detail TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE INDEX audit_target ON audit_log(target_user_id)`,
		`CREATE INDEX audit_actor ON audit_log(actor_user_id)`,
		`CREATE TABLE mail_log (
			id INTEGER PRIMARY KEY,
			user_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
			to_email TEXT NOT NULL,
			purpose TEXT NOT NULL,
			provider_message_id TEXT,
			sent_at INTEGER NOT NULL,
			last_event TEXT NOT NULL DEFAULT 'sent',
			last_event_at INTEGER NOT NULL,
			last_reason TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX mail_user ON mail_log(user_id)`,
		`CREATE UNIQUE INDEX mail_msgid ON mail_log(provider_message_id) WHERE provider_message_id IS NOT NULL`,
		`CREATE TABLE mail_events (
			mail_id INTEGER NOT NULL REFERENCES mail_log(id) ON DELETE CASCADE,
			event TEXT NOT NULL,
			at INTEGER NOT NULL,
			reason TEXT NOT NULL DEFAULT '',
			UNIQUE (mail_id, event, at)
		)`,
	},
	{ // v2: sub-project B, settings sync (one document per user)
		`CREATE TABLE user_settings (
			user_id INTEGER PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
			doc TEXT NOT NULL,
			revision INTEGER NOT NULL,
			generation TEXT NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
	},
	{ // v3: sub-project C, global audit list ordered and filtered by time
		`CREATE INDEX audit_at ON audit_log(at)`,
	},
	{ // v4: sub-project D, proposals (one generic table; this version ships the kind 'hashtag_channel')
		`CREATE TABLE proposals (
			id INTEGER PRIMARY KEY,
			kind TEXT NOT NULL,
			subject TEXT NOT NULL,
			status TEXT NOT NULL CHECK (status IN ('pending','approved','rejected','revoked')),
			proposer_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
			reviewer_id INTEGER REFERENCES users(id) ON DELETE SET NULL,
			note TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL,
			decided_at INTEGER
		)`,
		`CREATE UNIQUE INDEX proposals_kind_subject ON proposals(kind, subject)`,
		`CREATE INDEX proposals_status ON proposals(status)`,
	},
}

func (s *Store) migrate() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return fmt.Errorf("users: schema_version: %w", err)
	}
	v, err := s.SchemaVersion()
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := s.db.Exec(`INSERT INTO schema_version (version) VALUES (0)`); err != nil {
			return fmt.Errorf("users: init schema_version: %w", err)
		}
		v = 0
	} else if err != nil {
		return fmt.Errorf("users: read schema_version: %w", err)
	}
	if v > len(migrations) {
		return fmt.Errorf("users: database schema version %d is newer than this binary supports (%d)", v, len(migrations))
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		for _, stmt := range migrations[i] {
			if _, err := tx.Exec(stmt); err != nil {
				tx.Rollback()
				return fmt.Errorf("users: migration %d: %w", i+1, err)
			}
		}
		if _, err := tx.Exec(`UPDATE schema_version SET version = ?`, i+1); err != nil {
			tx.Rollback()
			return fmt.Errorf("users: migration %d: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("users: migration %d: %w", i+1, err)
		}
	}
	return nil
}

// SchemaVersion returns the applied schema version (sql.ErrNoRows on a
// database that has never been migrated).
func (s *Store) SchemaVersion() (int, error) {
	var v int
	err := s.db.QueryRow(`SELECT version FROM schema_version`).Scan(&v)
	return v, err
}
