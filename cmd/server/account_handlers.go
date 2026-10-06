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

// checkCurrentPassword writes 403 and returns false on a wrong password.
func checkCurrentPassword(w http.ResponseWriter, u *users.User, password string) bool {
	if ok, err := users.VerifyPassword(u.PasswordHash, password); err != nil || !ok {
		writeError(w, http.StatusForbidden, "current password is incorrect")
		return false
	}
	return true
}

func (s *Server) handleAccountPatch(w http.ResponseWriter, r *http.Request, u *users.User, sess *users.Session) {
	var req profileRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	name, err := users.ValidateDisplayName(req.DisplayName)
	if writeValidation(w, err) {
		return
	}
	if err := s.auth.st.SetDisplayName(u.ID, name); err != nil {
		log.Printf("[users] set display name for user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	u.DisplayName = name
	writeJSON(w, meFrom(u, sess))
}

func (s *Server) handleAccountPassword(w http.ResponseWriter, r *http.Request, u *users.User, sess *users.Session) {
	a := s.auth
	var req passwordChangeRequest
	if !decodeJSON(w, r, &req) || !checkCurrentPassword(w, u, req.CurrentPassword) {
		return
	}
	if writeValidation(w, users.ValidatePassword(req.NewPassword)) {
		return
	}
	hash, err := users.HashPassword(req.NewPassword)
	if err == nil {
		err = a.st.SetPassword(u.ID, hash)
	}
	if err != nil {
		log.Printf("[users] password change for user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := a.invalidateTokens("password change", u.ID, users.PurposeEmailChange, users.PurposeReset); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := a.st.DeleteUserSessions(u.ID, sess.ID); err != nil {
		log.Printf("[users] password change: end other sessions for user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	a.audit(idPtr(u.ID), "user.password.change", idPtr(u.ID), nil)
	writeJSON(w, okResponse{OK: true, Message: "Password changed. Your other devices were logged out."})
}

// handleAccountEmail answers and mails the requester identically whether
// the new address is free or taken (no enumeration): the old address gets
// the notice either way, and only a free address gets the confirmation.
func (s *Server) handleAccountEmail(w http.ResponseWriter, r *http.Request, u *users.User, _ *users.Session) {
	a := s.auth
	var req emailChangeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	newEmail, err := users.NormalizeEmail(req.NewEmail)
	if writeValidation(w, err) {
		return
	}
	if newEmail == u.Email {
		writeError(w, http.StatusBadRequest, "that is already your address")
		return
	}
	if !a.allow(w, r, a.signup, "emailchange:#"+strconv.FormatInt(u.ID, 10), "email:"+newEmail) ||
		!checkCurrentPassword(w, u, req.CurrentPassword) {
		return
	}
	switch _, err := a.st.GetByEmail(newEmail); {
	case err == nil:
		// Taken: no confirmation, but the newest request still replaces
		// any earlier link, as in the free branch.
		if err := a.invalidateTokens("email change", u.ID, users.PurposeEmailChange); err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		// The notice is the only mail here; if it fails, answer like a
		// failed confirmation in the free branch.
		if err := a.sendMail(r.Context(), u, "email-change-notice", a.emailChangeNoticeMail(u, newEmail)); err != nil {
			writeError(w, http.StatusServiceUnavailable, msgMailFailed)
			return
		}
	case errors.Is(err, users.ErrNotFound):
		err = a.mailToken(r.Context(), u, users.PurposeEmailChange, 24*time.Hour, newEmail, "email-change",
			func(tok string) mailer.Message { return a.emailChangeConfirmMail(u, newEmail, tok) })
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, msgMailFailed)
			return
		}
		// Best effort: sendMail logs a failure; the confirmation already went out.
		_ = a.sendMail(r.Context(), u, "email-change-notice", a.emailChangeNoticeMail(u, newEmail))
	default:
		log.Printf("[users] email change lookup for user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	a.audit(idPtr(u.ID), "user.email.change.requested", idPtr(u.ID), nil)
	writeJSON(w, okResponse{OK: true, Message: "Check the new address for a confirmation link."})
}

// handleConfirmEmail needs no session: the link may be opened on any device.
func (s *Server) handleConfirmEmail(w http.ResponseWriter, r *http.Request) {
	a := s.auth
	var req tokenRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	uid, newEmail, err := a.st.ConsumeToken(req.Token, users.PurposeEmailChange)
	if err != nil {
		writeTokenError(w, err)
		return
	}
	if err := a.st.SetEmail(uid, newEmail); err != nil {
		if errors.Is(err, users.ErrEmailTaken) {
			writeError(w, http.StatusConflict, "that address is already in use")
			return
		}
		log.Printf("[users] confirm email for user #%d: %v", uid, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	a.audit(idPtr(uid), "user.email.change", idPtr(uid), nil)
	writeJSON(w, okResponse{OK: true, Message: "Your address was changed."})
}

func (s *Server) handleAccountSessions(w http.ResponseWriter, _ *http.Request, u *users.User, sess *users.Session) {
	list, err := s.auth.st.ListSessions(u.ID)
	if err != nil {
		log.Printf("[users] list sessions for user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]sessionJSON, 0, len(list))
	for _, x := range list {
		out = append(out, sessionToJSON(x, sess.ID))
	}
	writeJSON(w, out)
}

func (s *Server) handleAccountSessionDelete(w http.ResponseWriter, r *http.Request, u *users.User, sess *users.Session) {
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid session id")
		return
	}
	if err := s.auth.st.DeleteSession(u.ID, id); err != nil {
		if errors.Is(err, users.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		log.Printf("[users] delete session for user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if id == sess.ID {
		s.auth.clearSessionCookie(w)
	}
	writeJSON(w, okResponse{OK: true})
}

func (s *Server) handleAccountDelete(w http.ResponseWriter, r *http.Request, u *users.User, _ *users.Session) {
	a := s.auth
	var req passwordConfirmRequest
	if !decodeJSON(w, r, &req) || !checkCurrentPassword(w, u, req.CurrentPassword) {
		return
	}
	if u.Role == users.RoleAdmin {
		n, err := a.st.CountActiveAdmins()
		if err != nil {
			log.Printf("[users] self-delete: count admins for user #%d: %v", u.ID, err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if n <= 1 {
			writeError(w, http.StatusConflict, "you are the last admin; promote someone else first")
			return
		}
	}
	if err := a.st.Delete(u.ID); err != nil {
		log.Printf("[users] self-delete user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	a.audit(idPtr(u.ID), "user.delete.self", idPtr(u.ID), nil)
	a.clearSessionCookie(w)
	writeJSON(w, okResponse{OK: true, Message: "Your account was deleted."})
}
