package main

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/mailer"
	"github.com/meshcore-analyzer/users"
)

// adminStoreFail answers a failed store call: ErrNotFound is 404, anything
// else is logged (user id only) and answered 500.
func adminStoreFail(w http.ResponseWriter, op string, id int64, err error) {
	if errors.Is(err, users.ErrNotFound) {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	log.Printf("[users] admin %s for user #%d: %v", op, id, err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

// adminTarget loads {id} from the path; writes 400/404/500 and returns nil
// on failure.
func (s *Server) adminTarget(w http.ResponseWriter, r *http.Request) *users.User {
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return nil
	}
	u, err := s.auth.st.GetByID(id)
	if err != nil {
		adminStoreFail(w, "load", id, err)
		return nil
	}
	return u
}

// guardRemoval blocks disabling/deleting/demoting yourself (except a
// self-demotion, allowed via allowSelf), config admins, and the last active
// admin. Writes 409 (or 500 if the admin count fails) and returns false
// when blocked. Count-then-mutate is not atomic; two admins removing each
// other concurrently can still both pass.
func (s *Server) guardRemoval(w http.ResponseWriter, actor, target *users.User, allowSelf bool) bool {
	if target.ID == actor.ID && !allowSelf {
		writeError(w, http.StatusConflict, "use My account to change your own account")
		return false
	}
	if target.Role == users.RoleAdmin && s.auth.isConfigAdmin(target.Email) {
		writeError(w, http.StatusConflict, "this admin is listed in adminEmails; remove them from the config first")
		return false
	}
	if target.Role == users.RoleAdmin && target.Status == users.StatusActive {
		n, err := s.auth.st.CountActiveAdmins()
		if err != nil {
			log.Printf("[users] admin guard: count admins for user #%d: %v", target.ID, err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return false
		}
		if n <= 1 {
			writeError(w, http.StatusConflict, "this is the last admin; promote someone else first")
			return false
		}
	}
	return true
}

func (s *Server) writeAdminRow(w http.ResponseWriter, id int64) {
	u, err := s.auth.st.GetByID(id)
	if err != nil {
		adminStoreFail(w, "reload", id, err)
		return
	}
	last, err := s.auth.st.LatestMailByUser()
	if err != nil {
		log.Printf("[users] admin latest mail: %v", err)
	}
	writeJSON(w, s.auth.adminRow(*u, last))
}

func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request, _ *users.User, _ *users.Session) {
	q := r.URL.Query()
	f := users.ListFilter{Status: users.Status(q.Get("status")), Role: users.Role(q.Get("role")), Query: q.Get("q")}
	switch f.Status {
	case "", users.StatusPending, users.StatusActive, users.StatusDisabled:
	default:
		writeError(w, http.StatusBadRequest, "invalid status filter")
		return
	}
	if f.Role != "" && !f.Role.Valid() {
		writeError(w, http.StatusBadRequest, "invalid role filter")
		return
	}
	list, err := s.auth.st.List(f)
	if err != nil {
		log.Printf("[users] admin list: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	last, err := s.auth.st.LatestMailByUser()
	if err != nil {
		log.Printf("[users] admin latest mail: %v", err)
	}
	out := make([]adminUserJSON, 0, len(list))
	for _, u := range list {
		out = append(out, s.auth.adminRow(u, last))
	}
	writeJSON(w, out)
}

func (s *Server) handleAdminUserDetail(w http.ResponseWriter, r *http.Request, _ *users.User, _ *users.Session) {
	u := s.adminTarget(w, r)
	if u == nil {
		return
	}
	a := s.auth
	last, err := a.st.LatestMailByUser()
	if err != nil {
		log.Printf("[users] admin latest mail: %v", err)
	}
	d := adminUserDetailJSON{User: a.adminRow(*u, last), Sessions: []sessionJSON{}, Mail: []mailJSON{}, Audit: []auditJSON{}}
	list, err := a.st.ListSessions(u.ID)
	if err != nil {
		adminStoreFail(w, "list sessions", u.ID, err)
		return
	}
	for _, x := range list {
		d.Sessions = append(d.Sessions, sessionToJSON(x, 0))
	}
	mails, err := a.st.MailForUser(u.ID, 50)
	if err != nil {
		adminStoreFail(w, "list mail", u.ID, err)
		return
	}
	for _, m := range mails {
		d.Mail = append(d.Mail, mailToJSON(m))
	}
	entries, err := a.st.AuditFor(u.ID, 100)
	if err != nil {
		adminStoreFail(w, "list audit", u.ID, err)
		return
	}
	for _, e := range entries {
		d.Audit = append(d.Audit, auditJSON{ID: e.ID, At: rfc3339(e.At), ActorUserID: e.ActorUserID,
			Action: e.Action, TargetUserID: e.TargetUserID, Detail: e.Detail})
	}
	writeJSON(w, d)
}

func (s *Server) handleAdminDisable(w http.ResponseWriter, r *http.Request, actor *users.User, _ *users.Session) {
	t := s.adminTarget(w, r)
	if t == nil || !s.guardRemoval(w, actor, t, false) {
		return
	}
	if t.Status == users.StatusDisabled {
		writeError(w, http.StatusConflict, "already disabled")
		return
	}
	if err := s.auth.st.SetStatus(t.ID, users.StatusDisabled); err != nil {
		adminStoreFail(w, "disable", t.ID, err)
		return
	}
	if err := s.auth.st.DeleteUserSessions(t.ID, 0); err != nil {
		// currentUser rejects non-active users, so the sessions are dead
		// anyway; keep going but leave a trace.
		log.Printf("[users] disable: delete sessions for user #%d: %v", t.ID, err)
	}
	s.auth.audit(idPtr(actor.ID), "user.disable", idPtr(t.ID), nil)
	s.writeAdminRow(w, t.ID)
}

func (s *Server) handleAdminEnable(w http.ResponseWriter, r *http.Request, actor *users.User, _ *users.Session) {
	t := s.adminTarget(w, r)
	if t == nil {
		return
	}
	if t.Status != users.StatusDisabled {
		writeError(w, http.StatusConflict, "only disabled accounts can be enabled")
		return
	}
	if err := s.auth.st.SetStatus(t.ID, users.StatusActive); err != nil {
		adminStoreFail(w, "enable", t.ID, err)
		return
	}
	s.auth.audit(idPtr(actor.ID), "user.enable", idPtr(t.ID), nil)
	s.writeAdminRow(w, t.ID)
}

func (s *Server) handleAdminDelete(w http.ResponseWriter, r *http.Request, actor *users.User, _ *users.Session) {
	t := s.adminTarget(w, r)
	if t == nil || !s.guardRemoval(w, actor, t, false) {
		return
	}
	if err := s.auth.st.Delete(t.ID); err != nil {
		adminStoreFail(w, "delete", t.ID, err)
		return
	}
	s.auth.audit(idPtr(actor.ID), "user.delete", idPtr(t.ID), nil)
	writeJSON(w, okResponse{OK: true})
}

func (s *Server) handleAdminRole(w http.ResponseWriter, r *http.Request, actor *users.User, _ *users.Session) {
	t := s.adminTarget(w, r)
	if t == nil {
		return
	}
	var req roleRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if !req.Role.Valid() {
		writeError(w, http.StatusBadRequest, "role must be user or admin")
		return
	}
	if req.Role == t.Role {
		s.writeAdminRow(w, t.ID)
		return
	}
	if req.Role == users.RoleUser && !s.guardRemoval(w, actor, t, true) {
		return
	}
	if err := s.auth.st.SetRole(t.ID, req.Role); err != nil {
		adminStoreFail(w, "set role", t.ID, err)
		return
	}
	s.auth.audit(idPtr(actor.ID), "user.role", idPtr(t.ID), map[string]string{"role": string(req.Role)})
	s.writeAdminRow(w, t.ID)
}

func (s *Server) handleAdminResendActivation(w http.ResponseWriter, r *http.Request, actor *users.User, _ *users.Session) {
	a := s.auth
	t := s.adminTarget(w, r)
	if t == nil {
		return
	}
	if t.Status != users.StatusPending {
		writeError(w, http.StatusConflict, "only pending accounts need activation")
		return
	}
	if err := a.mailToken(r.Context(), t, users.PurposeActivate, 48*time.Hour, "", "activate",
		func(tok string) mailer.Message { return a.activationMail(t, tok) }); err != nil {
		writeError(w, http.StatusServiceUnavailable, msgMailFailed)
		return
	}
	a.audit(idPtr(actor.ID), "user.activation.resend", idPtr(t.ID), nil)
	s.writeAdminRow(w, t.ID)
}

// handleAdminActivate activates a pending user without the mail link, for
// when mail keeps failing. The address stays unverified; activated_by and
// the audit row keep that visible.
func (s *Server) handleAdminActivate(w http.ResponseWriter, r *http.Request, actor *users.User, _ *users.Session) {
	a := s.auth
	t := s.adminTarget(w, r)
	if t == nil {
		return
	}
	if t.Status != users.StatusPending {
		writeError(w, http.StatusConflict, "only pending accounts can be activated")
		return
	}
	if err := a.st.InvalidateTokens(t.ID, users.PurposeActivate); err != nil {
		log.Printf("[users] manual activate: invalidate tokens for user #%d: %v", t.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := a.st.Activate(t.ID, a.roleFor(t.Email), idPtr(actor.ID)); err != nil {
		adminStoreFail(w, "activate", t.ID, err)
		return
	}
	a.audit(idPtr(actor.ID), "user.activate.manual", idPtr(t.ID), nil)
	s.writeAdminRow(w, t.ID)
}

func (s *Server) handleAdminMailRefresh(w http.ResponseWriter, r *http.Request, actor *users.User, _ *users.Session) {
	a := s.auth
	t := s.adminTarget(w, r)
	if t == nil {
		return
	}
	mailID, err := strconv.ParseInt(mux.Vars(r)["mailId"], 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid mail id")
		return
	}
	rec, err := a.st.MailByID(mailID)
	if err != nil && !errors.Is(err, users.ErrNotFound) {
		log.Printf("[users] admin mail refresh: load mail %d for user #%d: %v", mailID, t.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err != nil || rec.UserID == nil || *rec.UserID != t.ID {
		writeError(w, http.StatusNotFound, "mail not found")
		return
	}
	if rec.ProviderMessageID == "" {
		writeError(w, http.StatusConflict, "this mail has no provider message id")
		return
	}
	evs, err := a.mail.Events(r.Context(), rec.ProviderMessageID)
	if err != nil {
		log.Printf("[users] admin mail refresh: provider events for user #%d: %v", t.ID, err)
		writeError(w, http.StatusBadGateway, "mail provider unavailable")
		return
	}
	a.ingestMailEvents(evs)
	a.audit(idPtr(actor.ID), "user.mail.refresh", idPtr(t.ID), nil)
	rec, err = a.st.MailByID(mailID)
	if err != nil {
		log.Printf("[users] admin mail refresh: reload mail %d for user #%d: %v", mailID, t.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, mailToJSON(*rec))
}
