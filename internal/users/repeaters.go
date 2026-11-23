package users

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	RepeaterStatusActive         = "active"
	RepeaterStatusMaintenance    = "maintenance"
	RepeaterStatusDecommissioned = "decommissioned"

	RepeaterPowerMains   = "mains"
	RepeaterPowerBattery = "battery"
	RepeaterPowerSolar   = "solar"

	MaxRepeaterNotes   = 500
	MaxRepeaterHeightM = 300
	MinRepeaterGainDBi = -10
	MaxRepeaterGainDBi = 30
)

// RepeaterEdit is one community edit of a repeater's details. HeightM nil is
// unknown (distinct from 0); an empty Status or Power is unset.
type RepeaterEdit struct {
	HeightM *float64
	GainDBi float64
	Status  string
	Power   string
	Notes   string
}

// ValidateRepeaterEdit returns the cleaned edit or a *ValidationError.
func ValidateRepeaterEdit(e RepeaterEdit) (RepeaterEdit, error) {
	if e.HeightM != nil {
		h := *e.HeightM
		if math.IsNaN(h) || math.IsInf(h, 0) || h < 0 || h > MaxRepeaterHeightM {
			return RepeaterEdit{}, &ValidationError{Msg: fmt.Sprintf("height must be between 0 and %d m", MaxRepeaterHeightM)}
		}
		hc := h
		e.HeightM = &hc
	}
	if math.IsNaN(e.GainDBi) || math.IsInf(e.GainDBi, 0) || e.GainDBi < MinRepeaterGainDBi || e.GainDBi > MaxRepeaterGainDBi {
		return RepeaterEdit{}, &ValidationError{Msg: fmt.Sprintf("antenna gain must be between %d and %d dBi", MinRepeaterGainDBi, MaxRepeaterGainDBi)}
	}
	switch e.Status {
	case "", RepeaterStatusActive, RepeaterStatusMaintenance, RepeaterStatusDecommissioned:
	default:
		return RepeaterEdit{}, &ValidationError{Msg: "status must be active, maintenance or decommissioned"}
	}
	switch e.Power {
	case "", RepeaterPowerMains, RepeaterPowerBattery, RepeaterPowerSolar:
	default:
		return RepeaterEdit{}, &ValidationError{Msg: "power must be mains, battery or solar"}
	}
	n := strings.ToValidUTF8(e.Notes, "")
	n = strings.ReplaceAll(n, "\r\n", "\n")
	n = strings.Map(func(r rune) rune {
		switch {
		case r == '\t' || r == '\r':
			return ' '
		case r != '\n' && unicode.IsControl(r):
			return -1
		case (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069):
			return -1
		}
		return r
	}, n)
	lines := strings.Split(n, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	n = strings.TrimSpace(strings.Join(lines, "\n"))
	for strings.Contains(n, "\n\n\n") {
		n = strings.ReplaceAll(n, "\n\n\n", "\n\n")
	}
	if utf8.RuneCountInString(n) > MaxRepeaterNotes {
		return RepeaterEdit{}, &ValidationError{Msg: fmt.Sprintf("notes must be at most %d characters", MaxRepeaterNotes)}
	}
	e.Notes = n
	return e, nil
}

// ErrRepeaterConflict: a conditional SaveRepeaterInfoAt was given a base
// revision that is not the stored one.
var ErrRepeaterConflict = errors.New("users: repeater details revision conflict")

// RepeaterInfo is the community-maintained detail row of one repeater.
// UpdatedBy and UpdatedByName are empty when the editor was deleted or is not
// an active user. Revision counts saves from 1; a repeater without a row is
// at revision 0.
type RepeaterInfo struct {
	Pubkey        string
	Revision      int64
	HeightM       *float64
	GainDBi       float64
	Status        string
	Power         string
	Notes         string
	UpdatedAt     time.Time
	UpdatedBy     *int64
	UpdatedByName string
}

// RepeaterClaim says a user operates a repeater.
type RepeaterClaim struct {
	Pubkey      string
	UserID      int64
	DisplayName string
	ClaimedAt   time.Time
}

const repeaterInfoSelect = `SELECT i.pubkey, i.revision, i.height_m, i.gain_dbi, i.status, i.power, i.notes, i.updated_at, u.id, u.display_name
	FROM repeater_info i LEFT JOIN users u ON u.id = i.updated_by AND u.status = 'active'`

func scanRepeaterInfo(r rowScanner) (*RepeaterInfo, error) {
	var (
		ri            RepeaterInfo
		h             sql.NullFloat64
		st, pw, name  sql.NullString
		ub            sql.NullInt64
		updatedAtUnix int64
	)
	if err := r.Scan(&ri.Pubkey, &ri.Revision, &h, &ri.GainDBi, &st, &pw, &ri.Notes, &updatedAtUnix, &ub, &name); err != nil {
		return nil, err
	}
	if h.Valid {
		v := h.Float64
		ri.HeightM = &v
	}
	ri.Status, ri.Power = st.String, pw.String
	ri.UpdatedAt = fromUnix(updatedAtUnix)
	if ub.Valid {
		v := ub.Int64
		ri.UpdatedBy = &v
		ri.UpdatedByName = name.String
	}
	return &ri, nil
}

func (s *Store) GetRepeaterInfo(pubkey string) (*RepeaterInfo, error) {
	pk, err := NormalizePubkey(pubkey)
	if err != nil {
		return nil, err
	}
	ri, err := scanRepeaterInfo(s.db.QueryRow(repeaterInfoSelect+` WHERE i.pubkey = ?`, pk))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return ri, err
}

// ListRepeaterInfo returns every info row ordered by pubkey.
func (s *Store) ListRepeaterInfo() ([]RepeaterInfo, error) {
	rows, err := s.db.Query(repeaterInfoSelect + ` ORDER BY i.pubkey`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RepeaterInfo
	for rows.Next() {
		ri, err := scanRepeaterInfo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ri)
	}
	return out, rows.Err()
}

// SaveRepeaterInfo validates and upserts the row, whatever its revision. It
// returns the row the write replaced (nil if there was none), read in the
// same transaction, for the audit diff.
func (s *Store) SaveRepeaterInfo(userID int64, pubkey string, e RepeaterEdit) (*RepeaterInfo, error) {
	return s.saveRepeaterInfo(userID, pubkey, e, nil)
}

// SaveRepeaterInfoAt is SaveRepeaterInfo only while the stored revision is
// base (0: no row yet). Otherwise it writes nothing and returns
// ErrRepeaterConflict. The check is part of the write statement, so of two
// saves from the same base exactly one wins.
func (s *Store) SaveRepeaterInfoAt(userID int64, pubkey string, e RepeaterEdit, base int64) (*RepeaterInfo, error) {
	return s.saveRepeaterInfo(userID, pubkey, e, &base)
}

func (s *Store) saveRepeaterInfo(userID int64, pubkey string, e RepeaterEdit, base *int64) (*RepeaterInfo, error) {
	pk, err := NormalizePubkey(pubkey)
	if err != nil {
		return nil, err
	}
	e, err = ValidateRepeaterEdit(e)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	prev, err := scanRepeaterInfo(tx.QueryRow(repeaterInfoSelect+` WHERE i.pubkey = ?`, pk))
	if errors.Is(err, sql.ErrNoRows) {
		prev = nil
	} else if err != nil {
		return nil, err
	}
	var h any
	if e.HeightM != nil {
		h = *e.HeightM
	}
	var st, pw any
	if e.Status != "" {
		st = e.Status
	}
	if e.Power != "" {
		pw = e.Power
	}
	now := unix(s.now())
	var res sql.Result
	switch {
	case base == nil:
		res, err = tx.Exec(`INSERT INTO repeater_info (pubkey, revision, height_m, gain_dbi, status, power, notes, updated_at, updated_by)
			VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(pubkey) DO UPDATE SET revision = repeater_info.revision + 1,
				height_m = excluded.height_m, gain_dbi = excluded.gain_dbi,
				status = excluded.status, power = excluded.power, notes = excluded.notes,
				updated_at = excluded.updated_at, updated_by = excluded.updated_by`,
			pk, h, e.GainDBi, st, pw, e.Notes, now, userID)
	case *base == 0:
		// Only the first save of a repeater: a row that exists is a conflict.
		res, err = tx.Exec(`INSERT INTO repeater_info (pubkey, revision, height_m, gain_dbi, status, power, notes, updated_at, updated_by)
			VALUES (?, 1, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(pubkey) DO NOTHING`,
			pk, h, e.GainDBi, st, pw, e.Notes, now, userID)
	default:
		res, err = tx.Exec(`UPDATE repeater_info SET revision = revision + 1, height_m = ?, gain_dbi = ?,
				status = ?, power = ?, notes = ?, updated_at = ?, updated_by = ?
			WHERE pubkey = ? AND revision = ?`,
			h, e.GainDBi, st, pw, e.Notes, now, userID, pk, *base)
	}
	if err != nil {
		return nil, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, err
	} else if n != 1 {
		return nil, ErrRepeaterConflict
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return prev, nil
}

// ClaimRepeater is idempotent: added is false when the user already claimed it.
func (s *Store) ClaimRepeater(userID int64, pubkey string) (bool, error) {
	pk, err := NormalizePubkey(pubkey)
	if err != nil {
		return false, err
	}
	res, err := s.db.Exec(`INSERT OR IGNORE INTO repeater_claims (pubkey, user_id, claimed_at) VALUES (?, ?, ?)`, pk, userID, unix(s.now()))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// UnclaimRepeater returns ErrNotFound when the user has no claim on it.
func (s *Store) UnclaimRepeater(userID int64, pubkey string) error {
	pk, err := NormalizePubkey(pubkey)
	if err != nil {
		return err
	}
	return expectOne(s.db.Exec(`DELETE FROM repeater_claims WHERE pubkey = ? AND user_id = ?`, pk, userID))
}

func scanRepeaterClaims(rows *sql.Rows) ([]RepeaterClaim, error) {
	defer rows.Close()
	var out []RepeaterClaim
	for rows.Next() {
		var c RepeaterClaim
		var at int64
		if err := rows.Scan(&c.Pubkey, &c.UserID, &c.DisplayName, &at); err != nil {
			return nil, err
		}
		c.ClaimedAt = fromUnix(at)
		out = append(out, c)
	}
	return out, rows.Err()
}

const repeaterClaimsSelect = `SELECT c.pubkey, c.user_id, u.display_name, c.claimed_at
	FROM repeater_claims c JOIN users u ON u.id = c.user_id`

// RepeaterClaims lists the active claimers of one repeater.
func (s *Store) RepeaterClaims(pubkey string) ([]RepeaterClaim, error) {
	pk, err := NormalizePubkey(pubkey)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(repeaterClaimsSelect+` WHERE c.pubkey = ? AND u.status = 'active' ORDER BY c.claimed_at, c.user_id`, pk)
	if err != nil {
		return nil, err
	}
	return scanRepeaterClaims(rows)
}

// AllRepeaterClaims returns the active claimers of every repeater, by pubkey.
func (s *Store) AllRepeaterClaims() (map[string][]RepeaterClaim, error) {
	rows, err := s.db.Query(repeaterClaimsSelect + ` WHERE u.status = 'active' ORDER BY c.pubkey, c.claimed_at, c.user_id`)
	if err != nil {
		return nil, err
	}
	list, err := scanRepeaterClaims(rows)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]RepeaterClaim)
	for _, c := range list {
		out[c.Pubkey] = append(out[c.Pubkey], c)
	}
	return out, nil
}

// RepeaterClaimsByUser lists every claim of a user regardless of their
// status, newest first.
func (s *Store) RepeaterClaimsByUser(userID int64) ([]RepeaterClaim, error) {
	rows, err := s.db.Query(repeaterClaimsSelect+` WHERE c.user_id = ? ORDER BY c.claimed_at DESC, c.pubkey`, userID)
	if err != nil {
		return nil, err
	}
	return scanRepeaterClaims(rows)
}

// RepeaterClaimsOfUserRepeaters returns the active claimers of every
// repeater the user claimed, by pubkey, in one query (My repeaters).
func (s *Store) RepeaterClaimsOfUserRepeaters(userID int64) (map[string][]RepeaterClaim, error) {
	rows, err := s.db.Query(repeaterClaimsSelect+` WHERE u.status = 'active'
		AND c.pubkey IN (SELECT pubkey FROM repeater_claims WHERE user_id = ?)
		ORDER BY c.pubkey, c.claimed_at, c.user_id`, userID)
	if err != nil {
		return nil, err
	}
	list, err := scanRepeaterClaims(rows)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]RepeaterClaim)
	for _, c := range list {
		out[c.Pubkey] = append(out[c.Pubkey], c)
	}
	return out, nil
}

// RepeaterInfoOfUserRepeaters returns the saved info of every repeater the
// user claimed that has some, by pubkey, in one query (My repeaters).
func (s *Store) RepeaterInfoOfUserRepeaters(userID int64) (map[string]RepeaterInfo, error) {
	rows, err := s.db.Query(repeaterInfoSelect+` WHERE i.pubkey IN (SELECT pubkey FROM repeater_claims WHERE user_id = ?)`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]RepeaterInfo)
	for rows.Next() {
		ri, err := scanRepeaterInfo(rows)
		if err != nil {
			return nil, err
		}
		out[ri.Pubkey] = *ri
	}
	return out, rows.Err()
}

// CanEditRepeater: admin always; a non-active user never; with no active
// claimers any active user; otherwise only the active claimers.
func (s *Store) CanEditRepeater(u *User, pubkey string) (bool, error) {
	pk, err := NormalizePubkey(pubkey)
	if err != nil {
		return false, err
	}
	if u == nil || u.Status != StatusActive {
		return false, nil
	}
	if u.Role == RoleAdmin {
		return true, nil
	}
	claims, err := s.RepeaterClaims(pk)
	if err != nil {
		return false, err
	}
	if len(claims) == 0 {
		return true, nil
	}
	for _, c := range claims {
		if c.UserID == u.ID {
			return true, nil
		}
	}
	return false, nil
}
