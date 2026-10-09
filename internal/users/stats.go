package users

import "time"

// Admin overview thresholds (docs/specs/2026-10-07-admin-dashboard-design.md).
// Constants in this version; they belong in config later (AGENTS.md rule 8).
const (
	StuckPendingAfter = 24 * time.Hour // a pending account this old counts as stuck
	GuessingWindow    = 24 * time.Hour
	GuessingThreshold = 5 // failed logins on one account inside GuessingWindow

	statsPerDayDays    = 30
	statsGuessingLimit = 50
)

// DayCount is the count for one UTC day; Day is YYYY-MM-DD.
type DayCount struct {
	Day   string
	Count int
}

// MailCounts buckets mails by their latest delivery event.
type MailCounts struct {
	Delivered int // delivered, opened, clicked, unsubscribed
	Bounced   int // hard_bounce, soft_bounce, invalid_email
	Blocked   int
	Spam      int
	Pending   int // sent or deferred: no final event yet
	Other     int // error, or an event this version does not know
}

// GuessedAccount is an account with at least GuessingThreshold failed
// logins inside GuessingWindow.
type GuessedAccount struct {
	UserID      int64
	DisplayName string // empty when the account no longer exists
	Failed      int
}

// Stats holds the figures of the admin overview's Users card and its
// attention rules.
type Stats struct {
	Total, Active, Pending, Disabled int
	Admins                           int // active admins
	StuckPending                     int // pending for longer than StuckPendingAfter
	Bouncing                         int
	New7d, New30d                    int        // user.register audit rows
	NewPerDay                        []DayCount // statsPerDayDays entries, oldest first, the last is today (UTC)
	Active7d, Active30d              int        // last login or session activity inside the window
	Logins24h, FailedLogins24h       int
	Mail7d                           MailCounts
	Guessing                         []GuessedAccount // most failures first, never nil
}

func secs(d time.Duration) int64 { return int64(d / time.Second) }

// Stats computes the overview figures at now: a handful of COUNT queries
// on users.db.
func (s *Store) Stats(now time.Time) (Stats, error) {
	const day = 24 * time.Hour
	t := unix(now)
	var st Stats
	if err := s.db.QueryRow(`SELECT COUNT(*),
			COUNT(*) FILTER (WHERE status = 'active'),
			COUNT(*) FILTER (WHERE status = 'pending'),
			COUNT(*) FILTER (WHERE status = 'disabled'),
			COUNT(*) FILTER (WHERE role = 'admin' AND status = 'active'),
			COUNT(*) FILTER (WHERE status = 'pending' AND created_at < $1),
			COUNT(*) FILTER (WHERE email_bouncing != 0)
		FROM users`, t-secs(StuckPendingAfter)).
		Scan(&st.Total, &st.Active, &st.Pending, &st.Disabled, &st.Admins, &st.StuckPending, &st.Bouncing); err != nil {
		return Stats{}, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FILTER (WHERE at >= $1), COUNT(*) FROM audit_log
		WHERE action = 'user.register' AND at >= $2 AND at <= $3`, t-secs(7*day), t-secs(30*day), t).
		Scan(&st.New7d, &st.New30d); err != nil {
		return Stats{}, err
	}
	perDay, err := s.registrationsPerDay(now)
	if err != nil {
		return Stats{}, err
	}
	st.NewPerDay = perDay
	if err := s.db.QueryRow(`SELECT COUNT(*) FILTER (WHERE last >= $1), COUNT(*) FROM (
			SELECT GREATEST(COALESCE(u.last_login_at, 0),
				COALESCE((SELECT MAX(last_seen_at) FROM sessions s WHERE s.user_id = u.id), 0)) AS last
			FROM users u
		) activity WHERE last >= $2`, t-secs(7*day), t-secs(30*day)).
		Scan(&st.Active7d, &st.Active30d); err != nil {
		return Stats{}, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FILTER (WHERE action = 'user.login'), COUNT(*) FILTER (WHERE action = 'user.login.failed')
		FROM audit_log WHERE action IN ('user.login', 'user.login.failed') AND at >= $1`, t-secs(day)).
		Scan(&st.Logins24h, &st.FailedLogins24h); err != nil {
		return Stats{}, err
	}
	if st.Mail7d, err = s.mailCounts(t - secs(7*day)); err != nil {
		return Stats{}, err
	}
	if st.Guessing, err = s.guessedAccounts(t - secs(GuessingWindow)); err != nil {
		return Stats{}, err
	}
	return st, nil
}

// registrationsPerDay counts user.register rows per UTC day for the last
// statsPerDayDays days, today included.
func (s *Store) registrationsPerDay(now time.Time) ([]DayCount, error) {
	first := now.UTC().Truncate(24*time.Hour).AddDate(0, 0, -(statsPerDayDays - 1))
	rows, err := s.db.Query(`SELECT (at - $1) / 86400, COUNT(*) FROM audit_log
		WHERE action = 'user.register' AND at >= $2 AND at <= $3 GROUP BY 1`, unix(first), unix(first), unix(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := make([]int, statsPerDayDays)
	for rows.Next() {
		var d int64
		var n int
		if err := rows.Scan(&d, &n); err != nil {
			return nil, err
		}
		counts[d] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]DayCount, statsPerDayDays)
	for i, n := range counts {
		out[i] = DayCount{Day: first.AddDate(0, 0, i).Format("2006-01-02"), Count: n}
	}
	return out, nil
}

func (s *Store) mailCounts(since int64) (MailCounts, error) {
	var mc MailCounts
	rows, err := s.db.Query(`SELECT last_event, COUNT(*) FROM mail_log WHERE sent_at >= $1 GROUP BY last_event`, since)
	if err != nil {
		return mc, err
	}
	defer rows.Close()
	for rows.Next() {
		var ev string
		var n int
		if err := rows.Scan(&ev, &n); err != nil {
			return mc, err
		}
		switch ev {
		case "delivered", "opened", "clicked", "unsubscribed":
			mc.Delivered += n
		case "hard_bounce", "soft_bounce", "invalid_email":
			mc.Bounced += n
		case "blocked":
			mc.Blocked += n
		case "spam":
			mc.Spam += n
		case "sent", "deferred":
			mc.Pending += n
		default:
			mc.Other += n
		}
	}
	return mc, rows.Err()
}

func (s *Store) guessedAccounts(since int64) ([]GuessedAccount, error) {
	rows, err := s.db.Query(`SELECT a.target_user_id, COALESCE(u.display_name, ''), COUNT(*) AS n
		FROM audit_log a LEFT JOIN users u ON u.id = a.target_user_id
		WHERE a.action = 'user.login.failed' AND a.at >= $1 AND a.target_user_id IS NOT NULL
		GROUP BY a.target_user_id,u.display_name HAVING COUNT(*) >= $2 ORDER BY n DESC, a.target_user_id LIMIT $3`,
		since, GuessingThreshold, statsGuessingLimit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []GuessedAccount{}
	for rows.Next() {
		var g GuessedAccount
		if err := rows.Scan(&g.UserID, &g.DisplayName, &g.Failed); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}
