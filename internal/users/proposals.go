package users

import (
	"database/sql"
	"errors"
	"fmt"
	"github.com/meshcore-analyzer/dbconfig"
	"time"
)

// Proposals (docs/specs/2026-10-07-channel-proposals-design.md): one row per
// (kind, subject); a state change updates the row, history is in the audit log.

// KindHashtagChannel is the one proposal kind of this version. Subject is a
// name accepted by channel.ValidateHashtagName.
const KindHashtagChannel = "hashtag_channel"

type ProposalStatus string

const (
	ProposalPending  ProposalStatus = "pending"
	ProposalApproved ProposalStatus = "approved"
	ProposalRejected ProposalStatus = "rejected"
	ProposalRevoked  ProposalStatus = "revoked"
)

// Valid reports whether st is a known status.
func (st ProposalStatus) Valid() bool {
	switch st {
	case ProposalPending, ProposalApproved, ProposalRejected, ProposalRevoked:
		return true
	}
	return false
}

type ProposalAction string

const (
	ProposalApprove ProposalAction = "approve"
	ProposalReject  ProposalAction = "reject"
	ProposalRevoke  ProposalAction = "revoke"
)

var (
	ErrProposalPending       = errors.New("users: already proposed")
	ErrProposalApproved      = errors.New("users: already approved")
	ErrProposalRejected      = errors.New("users: proposal was rejected")
	ErrProposalTransition    = errors.New("users: action not allowed in the proposal's state")
	ErrProposalUserLimit     = errors.New("users: daily proposal limit reached")
	ErrProposalPendingLimit  = errors.New("users: pending proposal limit reached")
	ErrProposalApprovedLimit = errors.New("users: approved proposal limit reached")
)

// ProposalListMax caps ListProposals and ProposalsByUser.
const ProposalListMax = 500

// Proposal is one row of the proposals table.
type Proposal struct {
	ID         int64
	Kind       string
	Subject    string
	Status     ProposalStatus
	ProposerID *int64 // nil once the proposer's account is deleted
	ReviewerID *int64
	Note       string // the reviewer's reason, shown to the proposer
	CreatedAt  time.Time
	DecidedAt  *time.Time
}

// ProposalLimits bound Propose. Zero means no limit.
type ProposalLimits struct {
	MaxPending    int // pending proposals of every kind
	PerUserPerDay int // proposals one user created or re-opened in the last 24 hours
}

// ProposalFilter narrows ListProposals. Zero values mean "any".
type ProposalFilter struct {
	Status ProposalStatus
	Kind   string
}

const proposalCols = `id, kind, subject, status, proposer_id, reviewer_id, note, created_at, decided_at`

func scanProposal(row rowScanner) (*Proposal, error) {
	var p Proposal
	var status string
	var created int64
	var proposer, reviewer, decided sql.NullInt64
	if err := row.Scan(&p.ID, &p.Kind, &p.Subject, &status, &proposer, &reviewer, &p.Note, &created, &decided); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.Status = ProposalStatus(status)
	p.ProposerID = nullableID(proposer)
	p.ReviewerID = nullableID(reviewer)
	p.CreatedAt = fromUnix(created)
	p.DecidedAt = fromNullUnix(decided)
	return &p, nil
}

func nullableID(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	id := v.Int64
	return &id
}

// Propose opens a proposal for (kind, subject) in one transaction. A revoked
// row becomes pending again under the new proposer; pending, approved and
// rejected rows refuse. Duplicates are checked before the limits, so a
// duplicate is reported as one.
func (s *Store) Propose(kind, subject string, userID int64, lim ProposalLimits) (*Proposal, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// ponytail: serialize the small proposal queue across processes; replace
	// with per-kind locks only if measured proposal throughput requires it.
	if err := s.lockProposals(tx); err != nil {
		return nil, err
	}
	now := unix(s.now())
	cur, err := scanProposal(tx.QueryRow(`SELECT `+proposalCols+` FROM proposals WHERE kind = `+s.p(1)+` AND subject = `+s.p(2), kind, subject))
	switch {
	case errors.Is(err, ErrNotFound):
		cur = nil
	case err != nil:
		return nil, err
	case cur.Status == ProposalPending:
		return nil, ErrProposalPending
	case cur.Status == ProposalApproved:
		return nil, ErrProposalApproved
	case cur.Status == ProposalRejected:
		return nil, ErrProposalRejected
	}
	if lim.PerUserPerDay > 0 {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM proposals WHERE proposer_id = `+s.p(1)+` AND created_at > `+s.p(2),
			userID, now-secs(24*time.Hour)).Scan(&n); err != nil {
			return nil, err
		}
		if n >= lim.PerUserPerDay {
			return nil, ErrProposalUserLimit
		}
	}
	if lim.MaxPending > 0 {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM proposals WHERE status = 'pending'`).Scan(&n); err != nil {
			return nil, err
		}
		if n >= lim.MaxPending {
			return nil, ErrProposalPendingLimit
		}
	}
	var id int64
	if cur == nil {
		err := tx.QueryRow(`INSERT INTO proposals (kind, subject, status, proposer_id, created_at) VALUES (`+s.p(1)+`, `+s.p(2)+`, 'pending', `+s.p(3)+`, `+s.p(4)+`) RETURNING id`,
			kind, subject, userID, now).Scan(&id)
		if err != nil {
			return nil, err
		}
	} else {
		id = cur.ID
		if _, err := tx.Exec(`UPDATE proposals SET status = 'pending', proposer_id = `+s.p(1)+`, reviewer_id = NULL, note = '',
			created_at = `+s.p(2)+`, decided_at = NULL WHERE id = `+s.p(3), userID, now, id); err != nil {
			return nil, err
		}
	}
	p, err := scanProposal(tx.QueryRow(`SELECT `+proposalCols+` FROM proposals WHERE id = `+s.p(1), id))
	if err != nil {
		return nil, err
	}
	return p, tx.Commit()
}

// Decide applies action in one transaction: approve and reject from
// pending, revoke from approved; anything else is ErrProposalTransition.
// Approve refuses with ErrProposalApprovedLimit when maxApproved (> 0)
// proposals of the same kind are approved already.
func (s *Store) Decide(id int64, action ProposalAction, reviewerID int64, note string, maxApproved int) (*Proposal, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// ponytail: serialize the small proposal queue across processes; replace
	// with per-kind locks only if measured proposal throughput requires it.
	if err := s.lockProposals(tx); err != nil {
		return nil, err
	}
	p, err := scanProposal(tx.QueryRow(`SELECT `+proposalCols+` FROM proposals WHERE id = `+s.p(1), id))
	if err != nil {
		return nil, err
	}
	var to ProposalStatus
	switch {
	case action == ProposalApprove && p.Status == ProposalPending:
		to = ProposalApproved
	case action == ProposalReject && p.Status == ProposalPending:
		to = ProposalRejected
	case action == ProposalRevoke && p.Status == ProposalApproved:
		to = ProposalRevoked
	default:
		return nil, ErrProposalTransition
	}
	if to == ProposalApproved && maxApproved > 0 {
		var n int
		if err := tx.QueryRow(`SELECT COUNT(*) FROM proposals WHERE kind = `+s.p(1)+` AND status = 'approved'`, p.Kind).Scan(&n); err != nil {
			return nil, err
		}
		if n >= maxApproved {
			return nil, ErrProposalApprovedLimit
		}
	}
	now := s.now()
	if _, err := tx.Exec(`UPDATE proposals SET status = `+s.p(1)+`, reviewer_id = `+s.p(2)+`, note = `+s.p(3)+`, decided_at = `+s.p(4)+` WHERE id = `+s.p(5),
		string(to), reviewerID, note, unix(now), id); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	decided := fromUnix(unix(now))
	p.Status, p.ReviewerID, p.Note, p.DecidedAt = to, &reviewerID, note, &decided
	return p, nil
}

// ListProposals returns at most ProposalListMax proposals, newest first.
func (s *Store) ListProposals(f ProposalFilter) ([]Proposal, error) {
	q := `SELECT ` + proposalCols + ` FROM proposals WHERE 1=1`
	var args []any
	if f.Status != "" {
		q += fmt.Sprintf(` AND status = %s`, s.p(len(args)+1))
		args = append(args, string(f.Status))
	}
	if f.Kind != "" {
		q += fmt.Sprintf(` AND kind = %s`, s.p(len(args)+1))
		args = append(args, f.Kind)
	}
	q += fmt.Sprintf(` ORDER BY created_at DESC, id DESC LIMIT %s`, s.p(len(args)+1))
	args = append(args, ProposalListMax)
	return s.queryProposals(q, args...)
}

// ProposalsByUser returns the user's proposals, newest first, at most
// ProposalListMax.
func (s *Store) ProposalsByUser(userID int64) ([]Proposal, error) {
	return s.proposalsByUser(userID, ProposalListMax)
}

// AllProposalsByUser is ProposalsByUser without the cap (the account export).
func (s *Store) AllProposalsByUser(userID int64) ([]Proposal, error) {
	return s.proposalsByUser(userID, s.noLimit())
}

func (s *Store) proposalsByUser(userID int64, limit any) ([]Proposal, error) {
	return s.queryProposals(`SELECT `+proposalCols+` FROM proposals WHERE proposer_id = `+s.p(1)+`
		ORDER BY created_at DESC, id DESC LIMIT `+s.p(2), userID, limit)
}

func (s *Store) queryProposals(q string, args ...any) ([]Proposal, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Proposal
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// ApprovedSubjects returns the approved subjects of kind, oldest decision
// first, at most limit (0 = all). Never nil. cmd/ingestor runs the same
// query as raw SQL (see ingestorApprovedQuery in proposals_test.go).
func (s *Store) ApprovedSubjects(kind string, limit int) ([]string, error) {
	q := `SELECT subject FROM proposals WHERE kind = ` + s.p(1) + ` AND status = 'approved' ORDER BY decided_at, id`
	args := []any{kind}
	if limit > 0 {
		q += ` LIMIT ` + s.p(2)
		args = append(args, limit)
	}
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var subject string
		if err := rows.Scan(&subject); err != nil {
			return nil, err
		}
		out = append(out, subject)
	}
	return out, rows.Err()
}

// PruneProposals deletes rejected and revoked proposals decided more than
// maxAge ago. Pending and approved rows are kept however old they are.
func (s *Store) PruneProposals(maxAge time.Duration) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM proposals WHERE status IN ('rejected','revoked') AND decided_at < `+s.p(1),
		unix(s.now())-secs(maxAge))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// SQLite begins write transactions IMMEDIATE. PostgreSQL needs an explicit
// queue lock because several processes can check the same quota concurrently.
func (s *Store) lockProposals(tx *sql.Tx) error {
	if s.backend == dbconfig.SQLite {
		return nil
	}
	_, err := tx.Exec(`SELECT pg_advisory_xact_lock(hashtextextended(current_schema(),7289041403))`)
	return err
}
