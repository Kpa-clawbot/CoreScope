package main

import (
	"net/http"
	"strconv"

	"github.com/meshcore-analyzer/users"
)

// mailTestJSON answers POST /api/admin/mail/test.
type mailTestJSON struct {
	OK        bool   `json:"ok"`
	Provider  string `json:"provider"`
	MessageID string `json:"messageId,omitempty"`
	Error     string `json:"error,omitempty"`
}

// handleAdminMailTest sends a fixed test mail to the calling admin's own
// address through the configured provider. The address is never taken from
// the request, so the route cannot mail anyone else; any body is ignored.
func (s *Server) handleAdminMailTest(w http.ResponseWriter, r *http.Request, actor *users.User, _ *users.Session) {
	a := s.auth
	if !a.allow(w, r, a.mailTest, "user:"+strconv.FormatInt(actor.ID, 10)) {
		return
	}
	provider := a.set.provider
	id, err := a.sendMailID(r.Context(), actor, "test", a.testMail(actor))
	if err != nil {
		a.audit(idPtr(actor.ID), "mail.test", nil, map[string]string{"provider": provider, "result": "failed"})
		writeJSONStatus(w, http.StatusBadGateway, mailTestJSON{Provider: provider, Error: redactAddrs(err)})
		return
	}
	a.audit(idPtr(actor.ID), "mail.test", nil, map[string]string{"provider": provider, "result": "ok"})
	writeJSON(w, mailTestJSON{OK: true, Provider: provider, MessageID: id})
}
