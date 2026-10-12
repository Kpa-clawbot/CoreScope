package main

import (
	"log"
	"net/http"
	"time"

	"github.com/meshcore-analyzer/users"
)

// The field sets match users.DayCount, users.MailCounts and
// users.GuessedAccount, so plain type conversions fill them.
type dayCountJSON struct {
	Day   string `json:"day"`
	Count int    `json:"count"`
}

type mailCountsJSON struct {
	Delivered int `json:"delivered"`
	Bounced   int `json:"bounced"`
	Blocked   int `json:"blocked"`
	Spam      int `json:"spam"`
	Pending   int `json:"pending"`
	Other     int `json:"other"`
}

type guessedAccountJSON struct {
	UserID      int64  `json:"userId"`
	DisplayName string `json:"displayName"`
	Failed      int    `json:"failed"`
}

// adminNotifyJSON: notification figures, counts only (watch lists are
// private). Present only with notifications on.
type adminNotifyJSON struct {
	MailsLast24h   int `json:"mailsLast24h"`
	MaxMailsPerDay int `json:"maxMailsPerDay"`
	Watches        int `json:"watches"`
	WatchingUsers  int `json:"watchingUsers"`
}

type adminStatsJSON struct {
	Total           int                  `json:"total"`
	Active          int                  `json:"active"`
	Pending         int                  `json:"pending"`
	Disabled        int                  `json:"disabled"`
	Admins          int                  `json:"admins"`
	StuckPending    int                  `json:"stuckPending"`
	Bouncing        int                  `json:"bouncing"`
	New7d           int                  `json:"new7d"`
	New30d          int                  `json:"new30d"`
	NewPerDay       []dayCountJSON       `json:"newPerDay"`
	Active7d        int                  `json:"active7d"`
	Active30d       int                  `json:"active30d"`
	Logins24h       int                  `json:"logins24h"`
	FailedLogins24h int                  `json:"failedLogins24h"`
	Mail7d          mailCountsJSON       `json:"mail7d"`
	Guessing        []guessedAccountJSON `json:"guessing"`
	Notify          *adminNotifyJSON     `json:"notify,omitempty"`
}

func adminStatsFrom(st users.Stats) adminStatsJSON {
	out := adminStatsJSON{Total: st.Total, Active: st.Active, Pending: st.Pending, Disabled: st.Disabled,
		Admins: st.Admins, StuckPending: st.StuckPending, Bouncing: st.Bouncing, New7d: st.New7d, New30d: st.New30d,
		Active7d: st.Active7d, Active30d: st.Active30d, Logins24h: st.Logins24h, FailedLogins24h: st.FailedLogins24h,
		Mail7d:    mailCountsJSON(st.Mail7d),
		NewPerDay: make([]dayCountJSON, 0, len(st.NewPerDay)),
		Guessing:  make([]guessedAccountJSON, 0, len(st.Guessing))}
	for _, d := range st.NewPerDay {
		out.NewPerDay = append(out.NewPerDay, dayCountJSON(d))
	}
	for _, g := range st.Guessing {
		out.Guessing = append(out.Guessing, guessedAccountJSON(g))
	}
	return out
}

func (s *Server) handleAdminStats(w http.ResponseWriter, _ *http.Request, _ *users.User, _ *users.Session) {
	st, err := s.auth.st.Stats(time.Now())
	if err != nil {
		log.Printf("[users] admin stats: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := adminStatsFrom(st)
	if s.auth.notify != nil {
		total, _, err := s.auth.st.NotifyMailCounts(s.auth.notify.now().Add(-24 * time.Hour))
		if err != nil {
			log.Printf("[users] admin stats notification mails: %v", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		watches, watching, err := s.auth.st.NotifyWatchStats()
		if err != nil {
			log.Printf("[users] admin stats watches: %v", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		out.Notify = &adminNotifyJSON{MailsLast24h: total, MaxMailsPerDay: s.auth.set.notify.maxMailsPerDay, Watches: watches, WatchingUsers: watching}
	}
	writeJSON(w, out)
}
