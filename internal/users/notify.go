package users

import (
	"database/sql"
	"errors"
	"github.com/meshcore-analyzer/dbconfig"
	"sort"
	"strings"
	"time"
)

// Node notifications (docs/specs/2026-10-07-node-notifications-design.md):
// per-user preferences, the watch list, and the last state per (user, event,
// subject) that the server's evaluator compares against.

// Event types.
const (
	NotifyNodeOffline     = "node.offline"
	NotifyNodeBattery     = "node.battery"
	NotifyForeignNew      = "foreign.new"      // admins only
	NotifyObserverOffline = "observer.offline" // admins only
)

// Subject states.
const (
	NotifyGood = "good"
	NotifyBad  = "bad"
	NotifyTold = "told" // foreign.new: the admin was told about this node
)

// NotifyMailPurpose labels notification mails in mail_log; the daily limits
// count it.
const NotifyMailPurpose = "notify"

var (
	// NodeNotifyEvents are open to every user and chosen by default.
	NodeNotifyEvents = []string{NotifyNodeOffline, NotifyNodeBattery}
	// AdminNotifyEvents are open to admins only and off by default.
	AdminNotifyEvents = []string{NotifyForeignNew, NotifyObserverOffline}
)

// ErrWatchLimit: the user already watches the maximum number of nodes.
var ErrWatchLimit = errors.New("users: watch limit reached")

// IsAdminNotifyEvent reports whether only admins may choose e.
func IsAdminNotifyEvent(e string) bool { return e == NotifyForeignNew || e == NotifyObserverOffline }

// ValidNotifyEvent reports whether e is a known event type.
func ValidNotifyEvent(e string) bool {
	return e == NotifyNodeOffline || e == NotifyNodeBattery || IsAdminNotifyEvent(e)
}

// NotifyPrefs is one user's notification preferences.
type NotifyPrefs struct {
	UserID     int64
	Enabled    bool
	Events     []string // chosen events, canonical order, no duplicates
	UnsubToken string   // random, only turns notifications off
	UpdatedAt  time.Time
}

// DefaultNotifyPrefs is what a user without a preferences row gets:
// notifications on, the node events chosen, no unsubscribe token yet.
// NotifyPrefsFor stores these values when it creates the row.
func DefaultNotifyPrefs(userID int64) NotifyPrefs {
	return NotifyPrefs{UserID: userID, Enabled: true, Events: append([]string(nil), NodeNotifyEvents...)}
}

// Has reports whether the user chose event.
func (p NotifyPrefs) Has(event string) bool {
	for _, e := range p.Events {
		if e == event {
			return true
		}
	}
	return false
}

// NotifyWatch is one watched node of one user.
type NotifyWatch struct {
	UserID    int64
	Pubkey    string // lowercase hex
	CreatedAt time.Time
}

// NotifyKey identifies one evaluated subject of one user.
type NotifyKey struct {
	UserID  int64
	Event   string
	Subject string // node pubkey, observer id, or "*" (foreign.new baseline)
}

// NotifyState is the last evaluated state of a NotifyKey.
type NotifyState struct {
	NotifyKey
	State     string // NotifyGood, NotifyBad or NotifyTold
	ChangedAt time.Time
}

// canonicalEvents keeps the known events of list in canonical order, once each.
func canonicalEvents(list []string) []string {
	want := make(map[string]bool, len(list))
	for _, e := range list {
		want[e] = true
	}
	out := []string{}
	for _, group := range [][]string{NodeNotifyEvents, AdminNotifyEvents} {
		for _, e := range group {
			if want[e] {
				out = append(out, e)
			}
		}
	}
	return out
}

func (s *Store) placeholders(n int, offset ...int) string {
	start := 1
	if len(offset) > 0 {
		start += offset[0]
	}
	out := make([]string, n)
	for i := range out {
		out[i] = s.p(start + i)
	}
	return strings.Join(out, ",")
}

const prefsCols = `user_id, enabled, events, unsub_token, updated_at`

func scanPrefs(row rowScanner) (*NotifyPrefs, error) {
	var p NotifyPrefs
	var enabled int
	var events string
	var updated int64
	if err := row.Scan(&p.UserID, &enabled, &events, &p.UnsubToken, &updated); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	p.Enabled = enabled != 0
	p.Events = canonicalEvents(strings.Split(events, ","))
	p.UpdatedAt = fromUnix(updated)
	return &p, nil
}

func (s *Store) getPrefs(userID int64) (*NotifyPrefs, error) {
	return scanPrefs(s.db.QueryRow(`SELECT `+prefsCols+` FROM notification_prefs WHERE user_id = `+s.p(1), userID))
}

// NotifyPrefsFor returns the user's preferences, creating the default row
// (on, node events, a new unsubscribe token) on first use.
func (s *Store) NotifyPrefsFor(userID int64) (NotifyPrefs, error) {
	if p, err := s.getPrefs(userID); !errors.Is(err, ErrNotFound) {
		if err != nil {
			return NotifyPrefs{}, err
		}
		return *p, nil
	}
	tok, _, err := NewToken()
	if err != nil {
		return NotifyPrefs{}, err
	}
	d := DefaultNotifyPrefs(userID)
	if _, err := s.db.Exec(`INSERT INTO notification_prefs (user_id, enabled, events, unsub_token, updated_at)
		VALUES (`+s.p(1)+`, `+s.p(2)+`, `+s.p(3)+`, `+s.p(4)+`, `+s.p(5)+`) ON CONFLICT(user_id) DO NOTHING`, userID, 1, strings.Join(d.Events, ","), tok, unix(s.now())); err != nil {
		return NotifyPrefs{}, err
	}
	p, err := s.getPrefs(userID)
	if err != nil {
		return NotifyPrefs{}, err
	}
	return *p, nil
}

// StoredNotifyPrefs returns the user's stored preferences, or nil when
// none were ever created. Unlike NotifyPrefsFor it never writes.
func (s *Store) StoredNotifyPrefs(userID int64) (*NotifyPrefs, error) {
	p, err := s.getPrefs(userID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return p, err
}

// SetNotifyPrefs stores enabled and events (unknown events dropped,
// canonical order) and deletes the state rows of events no longer chosen,
// so choosing an event again starts with a silent first evaluation. The
// caller checks which events the user may choose.
func (s *Store) SetNotifyPrefs(userID int64, enabled bool, events []string) (NotifyPrefs, error) {
	if _, err := s.NotifyPrefsFor(userID); err != nil {
		return NotifyPrefs{}, err
	}
	ev := canonicalEvents(events)
	en := 0
	if enabled {
		en = 1
	}
	tx, err := s.db.Begin()
	if err != nil {
		return NotifyPrefs{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE notification_prefs SET enabled = `+s.p(1)+`, events = `+s.p(2)+`, updated_at = `+s.p(3)+` WHERE user_id = `+s.p(4),
		en, strings.Join(ev, ","), unix(s.now()), userID); err != nil {
		return NotifyPrefs{}, err
	}
	q := `DELETE FROM notification_state WHERE user_id = ` + s.p(1)
	args := []any{userID}
	if len(ev) > 0 {
		q += ` AND event NOT IN (` + s.placeholders(len(ev), 1) + `)`
		for _, e := range ev {
			args = append(args, e)
		}
	}
	if _, err := tx.Exec(q, args...); err != nil {
		return NotifyPrefs{}, err
	}
	if err := tx.Commit(); err != nil {
		return NotifyPrefs{}, err
	}
	return s.NotifyPrefsFor(userID)
}

// DisableNotifyByToken turns notifications off for the user holding the
// unsubscribe token. wasEnabled reports whether they were on.
// ErrTokenInvalid for an empty or unknown token.
func (s *Store) DisableNotifyByToken(token string) (userID int64, wasEnabled bool, err error) {
	if token == "" {
		return 0, false, ErrTokenInvalid
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback()
	var en int
	err = tx.QueryRow(`SELECT user_id,enabled FROM notification_prefs WHERE unsub_token=`+s.p(1)+s.forUpdate(), token).Scan(&userID, &en)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, ErrTokenInvalid
	}
	if err != nil {
		return 0, false, err
	}
	if en != 0 {
		if _, err = tx.Exec(`UPDATE notification_prefs SET enabled=0,updated_at=`+s.p(1)+` WHERE user_id=`+s.p(2), unix(s.now()), userID); err != nil {
			return 0, false, err
		}
	}
	return userID, en != 0, tx.Commit()
}

// AllNotifyPrefs returns every preferences row, by user id.
func (s *Store) AllNotifyPrefs() ([]NotifyPrefs, error) {
	rows, err := s.db.Query(`SELECT ` + prefsCols + ` FROM notification_prefs ORDER BY user_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NotifyPrefs
	for rows.Next() {
		p, err := scanPrefs(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// AddWatches adds pubkeys (lowercase hex, checked by the caller) to the
// user's watch list in one transaction, in order, while the list holds
// fewer than max entries (max <= 0: no limit). It reports how many were
// added, how many were already watched and how many the limit left out.
func (s *Store) AddWatches(userID int64, pubkeys []string, max int) (added, already, overLimit int, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, 0, err
	}
	defer tx.Rollback()
	if err := s.lockUser(tx, userID); err != nil {
		return 0, 0, 0, err
	}
	have := map[string]bool{}
	rows, err := tx.Query(`SELECT pubkey FROM notification_watches WHERE user_id = `+s.p(1), userID)
	if err != nil {
		return 0, 0, 0, err
	}
	for rows.Next() {
		var pk string
		if err := rows.Scan(&pk); err != nil {
			rows.Close()
			return 0, 0, 0, err
		}
		have[pk] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, 0, err
	}
	now := unix(s.now())
	for _, pk := range pubkeys {
		switch {
		case have[pk]:
			already++
		case max > 0 && len(have) >= max:
			overLimit++
		default:
			if _, err := tx.Exec(`INSERT INTO notification_watches (user_id, pubkey, created_at) VALUES (`+s.p(1)+`, `+s.p(2)+`, `+s.p(3)+`)`, userID, pk, now); err != nil {
				return 0, 0, 0, err
			}
			have[pk] = true
			added++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, 0, err
	}
	return added, already, overLimit, nil
}

// AddWatch watches one node. Watching an already watched node is a no-op;
// ErrWatchLimit when the list is full.
func (s *Store) AddWatch(userID int64, pubkey string, max int) error {
	_, _, over, err := s.AddWatches(userID, []string{pubkey}, max)
	if err != nil {
		return err
	}
	if over > 0 {
		return ErrWatchLimit
	}
	return nil
}

// RemoveWatch stops watching a node and deletes its node.* state rows.
// Removing a node that is not watched is a no-op.
func (s *Store) RemoveWatch(userID int64, pubkey string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.lockUser(tx, userID); errors.Is(err, ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM notification_watches WHERE user_id = `+s.p(1)+` AND pubkey = `+s.p(2), userID, pubkey); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM notification_state WHERE user_id = `+s.p(1)+` AND subject = `+s.p(2)+` AND event IN (`+s.p(3)+`, `+s.p(4)+`)`,
		userID, pubkey, NotifyNodeOffline, NotifyNodeBattery); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) queryWatches(q string, args ...any) ([]NotifyWatch, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NotifyWatch
	for rows.Next() {
		var w NotifyWatch
		var created int64
		if err := rows.Scan(&w.UserID, &w.Pubkey, &created); err != nil {
			return nil, err
		}
		w.CreatedAt = fromUnix(created)
		out = append(out, w)
	}
	return out, rows.Err()
}

// WatchesFor returns the user's watches, oldest first.
func (s *Store) WatchesFor(userID int64) ([]NotifyWatch, error) {
	return s.queryWatches(`SELECT user_id, pubkey, created_at FROM notification_watches WHERE user_id = `+s.p(1)+` ORDER BY created_at, pubkey`, userID)
}

// AllWatches returns every watch, by user and pubkey.
func (s *Store) AllWatches() ([]NotifyWatch, error) {
	return s.queryWatches(`SELECT user_id, pubkey, created_at FROM notification_watches ORDER BY user_id, pubkey`)
}

// AllNotifyStates returns every stored state.
func (s *Store) AllNotifyStates() ([]NotifyState, error) {
	rows, err := s.db.Query(`SELECT user_id, event, subject, state, changed_at FROM notification_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NotifyState
	for rows.Next() {
		var st NotifyState
		var changed int64
		if err := rows.Scan(&st.UserID, &st.Event, &st.Subject, &st.State, &changed); err != nil {
			return nil, err
		}
		st.ChangedAt = fromUnix(changed)
		out = append(out, st)
	}
	return out, rows.Err()
}

// WriteNotifyStates upserts the rows in one transaction. Rows of users
// deleted since the caller read them are skipped instead of failing the
// whole write on the foreign key.
func (s *Store) WriteNotifyStates(list []NotifyState) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	lock := ""
	if s.backend == dbconfig.Postgres {
		lock = " FOR KEY SHARE"
	}
	stmt, err := tx.Prepare(`INSERT INTO notification_state (user_id, event, subject, state, changed_at)
		SELECT id, ` + s.p(2) + `, ` + s.p(3) + `, ` + s.p(4) + `, ` + s.p(5) + ` FROM users WHERE id=` + s.p(1) + lock + `
		ON CONFLICT (user_id, event, subject) DO UPDATE SET state = excluded.state, changed_at = excluded.changed_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	// Match the janitor's ascending account lock order to avoid deadlocks.
	ordered := append([]NotifyState(nil), list...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].UserID < ordered[j].UserID })
	for _, st := range ordered {
		if _, err := stmt.Exec(st.UserID, st.Event, st.Subject, st.State, unix(st.ChangedAt)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteNotifyStates deletes the rows of keys in one transaction; keys
// without a row are ignored.
func (s *Store) DeleteNotifyStates(keys []NotifyKey) error {
	if len(keys) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`DELETE FROM notification_state WHERE user_id = ` + s.p(1) + ` AND event = ` + s.p(2) + ` AND subject = ` + s.p(3))
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, k := range keys {
		if _, err := stmt.Exec(k.UserID, k.Event, k.Subject); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// NotifyMailCounts counts notification mails sent at or after since: in
// total (deleted accounts included) and per existing user.
func (s *Store) NotifyMailCounts(since time.Time) (total int, perUser map[int64]int, err error) {
	perUser = map[int64]int{}
	rows, err := s.db.Query(`SELECT user_id, COUNT(*) FROM mail_log WHERE purpose = `+s.p(1)+` AND sent_at >= `+s.p(2)+` GROUP BY user_id`,
		NotifyMailPurpose, unix(since))
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var uid sql.NullInt64
		var n int
		if err := rows.Scan(&uid, &n); err != nil {
			return 0, nil, err
		}
		total += n
		if uid.Valid {
			perUser[uid.Int64] = n
		}
	}
	return total, perUser, rows.Err()
}

// NotifyWatchStats counts watches and the users who have at least one.
func (s *Store) NotifyWatchStats() (watches, watchingUsers int, err error) {
	err = s.db.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT user_id) FROM notification_watches`).Scan(&watches, &watchingUsers)
	return watches, watchingUsers, err
}
