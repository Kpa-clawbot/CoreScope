package main

import (
	"io"
	"log"
	"net/http"

	"github.com/meshcore-analyzer/mailer"
)

// handleBrevoWebhook ingests Brevo transactional events. Brevo is configured
// with auth {"type":"bearer","token":<webhookSecret>}, so it sends
// "Authorization: Bearer <secret>".
func (s *Server) handleBrevoWebhook(w http.ResponseWriter, r *http.Request) {
	s.handleMailWebhook(w, r, "brevo", func(r *http.Request) bool {
		return constantTimeEqual(r.Header.Get("Authorization"), "Bearer "+s.auth.set.webhookSecret)
	}, mailer.ParseBrevoWebhook)
}

// handlePostalWebhook ingests Postal message events. Postal cannot add a
// header of its own, so the webhook URL carries the secret as Basic auth
// (https://postal:<secret>@host/api/mail/postal/webhook); the user name is
// not checked.
func (s *Server) handlePostalWebhook(w http.ResponseWriter, r *http.Request) {
	s.handleMailWebhook(w, r, "postal", func(r *http.Request) bool {
		_, pass, ok := r.BasicAuth()
		return ok && constantTimeEqual(pass, s.auth.set.webhookSecret)
	}, mailer.ParsePostalWebhook)
}

// handleMailWebhook is the shared webhook flow. Authenticated payloads that
// cannot be used, and unknown message ids, get 200 so the provider does not
// retry forever.
func (s *Server) handleMailWebhook(w http.ResponseWriter, r *http.Request, provider string,
	authorized func(*http.Request) bool, parse func([]byte) ([]mailer.Event, error)) {
	a := s.auth
	if !a.allow(w, r, a.hook) {
		return
	}
	if !authorized(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	if err != nil {
		writeError(w, http.StatusBadRequest, "body too large")
		return
	}
	evs, err := parse(body)
	if err != nil {
		log.Printf("[users] %s webhook: ignored payload: %s", provider, redactAddrs(err))
		writeJSON(w, okResponse{OK: true})
		return
	}
	a.ingestMailEvents(evs)
	writeJSON(w, okResponse{OK: true})
}
