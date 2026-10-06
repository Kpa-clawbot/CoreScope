package main

import (
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/meshcore-analyzer/mailer"
	"github.com/meshcore-analyzer/users"
)

const (
	msgCheckMail  = "If the address can receive mail, a message is on its way. Check your inbox."
	msgBadLogin   = "incorrect email or password"
	msgMailFailed = "mail could not be sent, try again later"
)

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	a := s.auth
	var req registerRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	email, err := users.NormalizeEmail(req.Email)
	if writeValidation(w, err) {
		return
	}
	name, err := users.ValidateDisplayName(req.DisplayName)
	if writeValidation(w, err) {
		return
	}
	if writeValidation(w, users.ValidatePassword(req.Password)) {
		return
	}
	if !a.allow(w, r, a.signup, "email:"+email) {
		return
	}
	// Hash before the lookup so new and existing addresses cost the same.
	hash, err := users.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if existing, err := a.st.GetByEmail(email); err == nil {
		// Identical response either way (no account enumeration).
		if existing.Status == users.StatusPending {
			// Newest registration wins: whoever clicks the link gets the
			// password and name of the latest request, so squatting a pending
			// address cannot plant credentials. Failures stay invisible to
			// the client (same response as every other branch).
			if err := a.st.SetPassword(existing.ID, hash); err != nil {
				log.Printf("[users] register: update pending user #%d: %v", existing.ID, err)
			} else if err := a.st.SetDisplayName(existing.ID, name); err != nil {
				log.Printf("[users] register: update pending user #%d: %v", existing.ID, err)
			} else {
				existing.DisplayName = name
				_ = a.mailToken(r.Context(), existing, users.PurposeActivate, 48*time.Hour, "", "activate",
					func(tok string) mailer.Message { return a.activationMail(existing, tok) })
			}
		} else {
			_ = a.sendMail(r.Context(), existing, "register-notice", a.registerNoticeMail(existing))
		}
		writeJSON(w, okResponse{OK: true, Message: msgCheckMail})
		return
	}
	u, err := a.st.CreatePending(email, name, hash)
	if errors.Is(err, users.ErrEmailTaken) { // lost a race with a concurrent register
		writeJSON(w, okResponse{OK: true, Message: msgCheckMail})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	err = a.mailToken(r.Context(), u, users.PurposeActivate, 48*time.Hour, "", "activate",
		func(tok string) mailer.Message { return a.activationMail(u, tok) })
	if err != nil {
		if derr := a.st.Delete(u.ID); derr != nil {
			log.Printf("[users] rollback of user #%d failed: %v", u.ID, derr)
		}
		writeError(w, http.StatusServiceUnavailable, msgMailFailed)
		return
	}
	a.audit(nil, "user.register", idPtr(u.ID), nil)
	writeJSON(w, okResponse{OK: true, Message: msgCheckMail})
}

func (s *Server) handleActivate(w http.ResponseWriter, r *http.Request) {
	a := s.auth
	var req tokenRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	uid, _, err := a.st.ConsumeToken(req.Token, users.PurposeActivate)
	if err != nil {
		writeTokenError(w, err)
		return
	}
	u, err := a.st.GetByID(uid)
	if err != nil || u.Status != users.StatusPending {
		writeError(w, http.StatusGone, "this account is already activated, log in instead")
		return
	}
	if err := a.st.Activate(u.ID, a.roleFor(u.Email), nil); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	a.audit(nil, "user.activate", idPtr(u.ID), nil)
	if u, err = a.st.GetByID(u.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	a.startSession(w, r, u)
}

// startSession creates a session, sets the cookie and answers with /me.
func (a *authService) startSession(w http.ResponseWriter, r *http.Request, u *users.User) {
	raw, sess, err := a.st.CreateSession(u.ID, a.set.sessionTTL, r.UserAgent())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	_ = a.st.TouchLogin(u.ID)
	a.setSessionCookie(w, raw, sess.ExpiresAt)
	writeJSON(w, meFrom(u, sess))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	a := s.auth
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	email, emailErr := users.NormalizeEmail(req.Email)
	key := "email:" + email
	if emailErr != nil {
		key = "email:invalid"
	}
	if !a.allow(w, r, a.login, key) {
		return
	}
	var u *users.User
	if emailErr == nil {
		u, _ = a.st.GetByEmail(email)
	}
	if u == nil {
		users.BurnPasswordCheck(req.Password)
		writeError(w, http.StatusUnauthorized, msgBadLogin)
		return
	}
	ok, err := users.VerifyPassword(u.PasswordHash, req.Password)
	if err != nil || !ok || u.Status != users.StatusActive {
		writeError(w, http.StatusUnauthorized, msgBadLogin)
		return
	}
	// Config wins: an address in adminEmails is always admin.
	if a.isConfigAdmin(u.Email) && u.Role != users.RoleAdmin {
		if err := a.st.SetRole(u.ID, users.RoleAdmin); err == nil {
			u.Role = users.RoleAdmin
			a.audit(nil, "user.role.config", idPtr(u.ID), map[string]string{"role": "admin"})
		}
	}
	a.startSession(w, r, u)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		_ = s.auth.st.DeleteSessionByToken(c.Value)
	}
	s.auth.clearSessionCookie(w)
	writeJSON(w, okResponse{OK: true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u, sess := s.auth.currentUser(w, r)
	if u == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	writeJSON(w, meFrom(u, sess))
}

func (s *Server) handleForgot(w http.ResponseWriter, r *http.Request) {
	a := s.auth
	var req emailRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	email, err := users.NormalizeEmail(req.Email)
	if err != nil {
		writeJSON(w, okResponse{OK: true, Message: msgCheckMail})
		return
	}
	if !a.allow(w, r, a.signup, "email:"+email) {
		return
	}
	u, err := a.st.GetByEmail(email)
	if err != nil || u.Status != users.StatusActive {
		writeJSON(w, okResponse{OK: true, Message: msgCheckMail})
		return
	}
	err = a.mailToken(r.Context(), u, users.PurposeReset, time.Hour, "", "reset",
		func(tok string) mailer.Message { return a.resetMail(u, tok) })
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, msgMailFailed)
		return
	}
	writeJSON(w, okResponse{OK: true, Message: msgCheckMail})
}

func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	a := s.auth
	var req resetRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if writeValidation(w, users.ValidatePassword(req.Password)) {
		return
	}
	uid, _, err := a.st.ConsumeToken(req.Token, users.PurposeReset)
	if err != nil {
		writeTokenError(w, err)
		return
	}
	hash, err := users.HashPassword(req.Password)
	if err == nil {
		err = a.st.SetPassword(uid, hash)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err := a.st.DeleteUserSessions(uid, 0); err != nil {
		log.Printf("[users] reset: end sessions for user #%d: %v", uid, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	a.audit(idPtr(uid), "user.password.reset", idPtr(uid), nil)
	writeJSON(w, okResponse{OK: true, Message: "Password changed. Log in with your new password."})
}
