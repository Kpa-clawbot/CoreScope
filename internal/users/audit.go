package users

import (
	"database/sql"
	"encoding/json"
	"strings"
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

// AuditListMax caps one AuditList page; auditListDefault applies when the
// filter sets no limit.
const (
	AuditListMax     = 500
	auditListDefault = 100
)

// AuditFilter narrows AuditList. Zero values mean "any".
type AuditFilter struct {
	// Actions matches any listed action. An entry ending in ".*" is a
	// group: "user.login.*" matches "user.login" and "user.login.<anything>".
	Actions  []string
	UserID   *int64     // the user as actor or target
	From, To *time.Time // both inclusive
	BeforeID int64      // keyset paging: only rows with id < BeforeID; 0 = from the newest
	Limit    int        // default 100, capped at AuditListMax
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
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.Query(`SELECT id, at, actor_user_id, action, target_user_id, detail FROM audit_log
		WHERE target_user_id = ? OR actor_user_id = ? ORDER BY at DESC, id DESC LIMIT ?`, userID, userID, limit)
	if err != nil {
		return nil, err
	}
	return scanAudit(rows)
}

// AuditList returns the entries matching f, newest (highest id) first.
func (s *Store) AuditList(f AuditFilter) ([]AuditEntry, error) {
	q := `SELECT id, at, actor_user_id, action, target_user_id, detail FROM audit_log WHERE 1=1`
	var args []any
	if len(f.Actions) > 0 {
		var ors []string
		for _, a := range f.Actions {
			if group, ok := strings.CutSuffix(a, ".*"); ok {
				ors = append(ors, `action = ? OR action LIKE ? ESCAPE '\'`)
				args = append(args, group, escapeLike(group)+".%")
			} else {
				ors = append(ors, `action = ?`)
				args = append(args, a)
			}
		}
		q += ` AND (` + strings.Join(ors, ` OR `) + `)`
	}
	if f.UserID != nil {
		q += ` AND (actor_user_id = ? OR target_user_id = ?)`
		args = append(args, *f.UserID, *f.UserID)
	}
	if f.From != nil {
		q += ` AND at >= ?`
		args = append(args, unix(*f.From))
	}
	if f.To != nil {
		q += ` AND at <= ?`
		args = append(args, unix(*f.To))
	}
	if f.BeforeID > 0 {
		q += ` AND id < ?`
		args = append(args, f.BeforeID)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = auditListDefault
	}
	if limit > AuditListMax {
		limit = AuditListMax
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	return scanAudit(rows)
}

// PruneAudit deletes rows of the listed actions older than maxAge. Rows of
// other actions are kept however old they are.
func (s *Store) PruneAudit(actions []string, maxAge time.Duration) (int64, error) {
	if len(actions) == 0 {
		return 0, nil
	}
	args := make([]any, 0, len(actions)+1)
	for _, a := range actions {
		args = append(args, a)
	}
	args = append(args, unix(s.now())-int64(maxAge/time.Second))
	ph := strings.TrimSuffix(strings.Repeat("?,", len(actions)), ",")
	res, err := s.db.Exec(`DELETE FROM audit_log WHERE action IN (`+ph+`) AND at < ?`, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func scanAudit(rows *sql.Rows) ([]AuditEntry, error) {
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
