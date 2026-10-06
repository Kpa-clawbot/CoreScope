package main

import (
	"io"
	"log"
	"net/http"

	"github.com/meshcore-analyzer/mailer"
)

// handleBrevoWebhook ingests Brevo transactional events. Brevo is configured
// with auth {"type":"bearer","token":<webhookSecret>}, so it sends
// "Authorization: Bearer <secret>". Unknown message ids get 200 so Brevo
// does not retry forever.
func (s *Server) handleBrevoWebhook(w http.ResponseWriter, r *http.Request) {
	a := s.auth
	if !a.allow(w, r, a.hook) {
		return
	}
	if !constantTimeEqual(r.Header.Get("Authorization"), "Bearer "+a.set.webhookSecret) {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	if err != nil {
		writeError(w, http.StatusBadRequest, "body too large")
		return
	}
	evs, err := mailer.ParseBrevoWebhook(body)
	if err != nil {
		// Authenticated but unusable: answer 200 so Brevo does not retry
		// forever; log for the operator.
		log.Printf("[users] brevo webhook: ignored payload: %v", err)
		writeJSON(w, okResponse{OK: true})
		return
	}
	a.ingestMailEvents(evs)
	writeJSON(w, okResponse{OK: true})
}
