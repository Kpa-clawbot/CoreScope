//go:build e2etest

package main

// E2E-only hooks, compiled only with -tags e2etest (CI's corescope-server-e2e):
// the "fake" mail provider and GET /__e2e/last-mail, which returns the newest
// fake mail so Playwright can follow its link, and GET /__e2e/unsubscribe-link,
// which returns a user's notification unsubscribe link. Never part of a
// release build.

import (
	"net/http"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/mailer"
)

type e2eMail struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Text    string `json:"text"`
}

type e2eLink struct {
	Link string `json:"link"`
}

func init() {
	fakeMailerAllowed = true
	e2eRoutes = func(s *Server, r *mux.Router) {
		r.HandleFunc("/__e2e/last-mail", func(w http.ResponseWriter, _ *http.Request) {
			f, ok := s.auth.mail.(*mailer.Fake)
			if !ok {
				writeError(w, http.StatusNotFound, "not using the fake mailer")
				return
			}
			m, _, ok := f.Last()
			if !ok {
				writeError(w, http.StatusNotFound, "no mail sent yet")
				return
			}
			writeJSON(w, e2eMail{To: m.To, Subject: m.Subject, Text: m.Text})
		}).Methods("GET")
		// The unsubscribe link a notification mail would carry, so the E2E
		// can follow it without waiting for a real state change.
		r.HandleFunc("/__e2e/unsubscribe-link", func(w http.ResponseWriter, req *http.Request) {
			u, err := s.auth.st.GetByEmail(req.URL.Query().Get("email"))
			if err != nil {
				writeError(w, http.StatusNotFound, "no such user")
				return
			}
			p, err := s.auth.st.NotifyPrefsFor(u.ID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
			writeJSON(w, e2eLink{Link: s.auth.link("unsubscribe", p.UnsubToken)})
		}).Methods("GET")
	}
}
