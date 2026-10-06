package users

import (
	"database/sql"
	"encoding/json"
	"time"
)

// AuditEntry is one recorded action. Rows outlive the users they mention.
type AuditEntry struct {
	ID           int64
	At           time.Time
	ActorUserID  *int64 // nil = system or API key
	Action       string
	TargetUserID *int64
	Detail       map[string]string
}

// Audit records an action. detail may be nil.
func (s *Store) Audit(actor *int64, action string, target *int64, detail map[string]string) error {
	if detail == nil {
		detail = map[string]string{}
	}
	b, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO audit_log (at, actor_user_id, action, target_user_id, detail) VALUES (?, ?, ?, ?, ?)`,
		unix(s.now()), nullInt(actor), action, nullInt(target), string(b))
	return err
}

// AuditFor returns entries where userID is the target or the actor, newest first.
func (s *Store) AuditFor(userID int64, limit int) ([]AuditEntry, error) {
	rows, err := s.db.Query(`SELECT id, at, actor_user_id, action, target_user_id, detail FROM audit_log
		WHERE target_user_id = ? OR actor_user_id = ? ORDER BY at DESC, id DESC LIMIT ?`, userID, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		var at int64
		var actor, target sql.NullInt64
		var detail string
		if err := rows.Scan(&e.ID, &at, &actor, &e.Action, &target, &detail); err != nil {
			return nil, err
		}
		e.At = fromUnix(at)
		if actor.Valid {
			v := actor.Int64
			e.ActorUserID = &v
		}
		if target.Valid {
			v := target.Int64
			e.TargetUserID = &v
		}
		e.Detail = map[string]string{}
		_ = json.Unmarshal([]byte(detail), &e.Detail)
		out = append(out, e)
	}
	return out, rows.Err()
}
