package main

import (
	"errors"
	"log"
	"net/http"
	"strconv"
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
			// Newest registration wins: the account takes the password and
			// name of the latest request, and activation demands that
			// password, so a squatter cannot plant credentials and the owner
			// can recover a squatted address. Failures stay invisible to the
			// client (same response as every other branch).
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

// handleActivate needs the token and the account password: a re-register
// of a pending address replaces the password, so whoever clicks must prove
// they know the newest one (a squatter's link cannot log the owner into an
// account carrying the squatter's password).
func (s *Server) handleActivate(w http.ResponseWriter, r *http.Request) {
	a := s.auth
	var req activateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	uid, err := a.st.TokenUser(req.Token, users.PurposeActivate)
	if err != nil {
		writeTokenError(w, err)
		return
	}
	if !a.allow(w, r, a.login, "activate:#"+strconv.FormatInt(uid, 10)) {
		return
	}
	u, err := a.st.GetByID(uid)
	if err != nil && !errors.Is(err, users.ErrNotFound) {
		log.Printf("[users] activate: load user #%d: %v", uid, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if err != nil || u.Status != users.StatusPending {
		writeError(w, http.StatusGone, "this account is already activated, log in instead")
		return
	}
	ok, err := users.VerifyPassword(u.PasswordHash, req.Password)
	if err != nil {
		log.Printf("[users] activate: verify password for user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "wrong password for this account")
		return
	}
	// Activate only on the hash that was just verified: a re-register in
	// between replaces it, and the newest password must be proven.
	switch err := a.st.ActivateWithToken(req.Token, u.ID, a.roleFor(u.Email), u.PasswordHash); {
	case err == nil:
	case errors.Is(err, users.ErrAccountChanged):
		writeError(w, http.StatusConflict, "account changed, try again")
		return
	case errors.Is(err, users.ErrTokenInvalid), errors.Is(err, users.ErrTokenExpired):
		writeTokenError(w, err)
		return
	default:
		log.Printf("[users] activate user #%d: %v", u.ID, err)
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
// It reports whether the session was started.
func (a *authService) startSession(w http.ResponseWriter, r *http.Request, u *users.User) bool {
	raw, sess, err := a.st.CreateSession(u.ID, a.set.sessionTTL, r.UserAgent())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return false
	}
	_ = a.st.TouchLogin(u.ID)
	a.setSessionCookie(w, raw, sess.ExpiresAt)
	writeJSON(w, meFrom(u, sess))
	return true
}

// loginFailReason is the audit reason of a refused login for an existing
// account. A wrong password wins over the account state.
func loginFailReason(passwordOK bool, st users.Status) string {
	if !passwordOK {
		return "wrong_password"
	}
	if st == users.StatusPending {
		return "pending"
	}
	return "disabled"
}

// verifyLogin is the credential check shared by /api/auth/login and
// /api/auth/device-token: rate limits per IP and per address, the
// constant-cost answer for unknown addresses, the failure audit row and
// the config-admin promotion. It returns the user, or nil after writing
// the answer.
func (a *authService) verifyLogin(w http.ResponseWriter, r *http.Request, rawEmail, password string) *users.User {
	email, emailErr := users.NormalizeEmail(rawEmail)
	key := "email:" + email
	if emailErr != nil {
		key = "email:invalid"
	}
	if !a.allow(w, r, a.login, key) {
		return nil
	}
	var u *users.User
	if emailErr == nil {
		u, _ = a.st.GetByEmail(email)
	}
	if u == nil {
		users.BurnPasswordCheck(password)
		writeError(w, http.StatusUnauthorized, msgBadLogin)
		return nil
	}
	ok, err := users.VerifyPassword(u.PasswordHash, password)
	if err != nil || !ok || u.Status != users.StatusActive {
		writeError(w, http.StatusUnauthorized, msgBadLogin)
		// In the background after the answer is decided, so the write never
		// changes response timing.
		a.auditAsync(nil, "user.login.failed", idPtr(u.ID), map[string]string{"reason": loginFailReason(err == nil && ok, u.Status)})
		return nil
	}
	// Config wins: an address in adminEmails is always admin.
	if a.isConfigAdmin(u.Email) && u.Role != users.RoleAdmin {
		if err := a.st.SetRole(u.ID, users.RoleAdmin); err == nil {
			u.Role = users.RoleAdmin
			a.audit(nil, "user.role.config", idPtr(u.ID), map[string]string{"role": "admin"})
		}
	}
	return u
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	a := s.auth
	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	u := a.verifyLogin(w, r, req.Email, req.Password)
	if u == nil {
		return
	}
	if a.startSession(w, r, u) {
		a.auditAsync(nil, "user.login", idPtr(u.ID), nil)
	}
}

// handleDeviceToken issues a CoreDrive RX device token. No Origin check:
// the token is returned in the body and nothing is stored in a browser,
// so login CSRF does not apply, and RX may run on another origin.
func (s *Server) handleDeviceToken(w http.ResponseWriter, r *http.Request) {
	a := s.auth
	var req deviceTokenRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	u := a.verifyLogin(w, r, req.Email, req.Password)
	if u == nil {
		return
	}
	raw, sess, err := a.st.CreateDeviceSession(u.ID, req.DeviceName, []string{deviceScopeRX}, r.UserAgent())
	if err != nil {
		log.Printf("[users] device token for user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	_ = a.st.TouchLogin(u.ID)
	writeJSON(w, deviceTokenResponse{Token: raw, ExpiresAt: rfc3339(sess.ExpiresAt),
		User: deviceTokenUser{ID: u.ID, DisplayName: u.DisplayName}})
	a.auditAsync(nil, "user.login", idPtr(u.ID), map[string]string{"via": "device", "device": sess.Label})
}

// handleLogout ends the caller's session: with a bearer header it revokes
// that device token, otherwise it ends the cookie session (origin-checked,
// as before).
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	a := s.auth
	if tok, ok := bearerToken(r); ok {
		_, sess, code := a.bearerUser(r, tok)
		if code != 0 {
			writeBearerFail(w, code)
			return
		}
		if err := a.st.DeleteSession(sess.UserID, sess.ID); err != nil && !errors.Is(err, users.ErrNotFound) {
			log.Printf("[users] revoke device session #%d: %v", sess.ID, err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, okResponse{OK: true})
		return
	}
	if !a.originOK(r) {
		writeError(w, http.StatusForbidden, "request origin not allowed")
		return
	}
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		_ = a.st.DeleteSessionByToken(c.Value)
	}
	a.clearSessionCookie(w)
	writeJSON(w, okResponse{OK: true})
}

// handleMe answers the caller; a device token gets no CSRF token (it never
// needs one).
func (s *Server) handleMe(w http.ResponseWriter, _ *http.Request, u *users.User, sess *users.Session) {
	me := meFrom(u, sess)
	if sess.Kind == users.SessionKindDevice {
		me.CSRFToken = ""
	}
	writeJSON(w, me)
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
	// Sessions first: a token-store failure below must not leave an
	// attacker's session alive.
	if err := a.st.DeleteUserSessions(uid, 0); err != nil {
		log.Printf("[users] reset: end sessions for user #%d: %v", uid, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	// A pending email change must not survive the owner's recovery.
	if err := a.invalidateTokens("reset", uid, users.PurposeEmailChange); err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	a.audit(idPtr(uid), "user.password.reset", idPtr(uid), nil)
	writeJSON(w, okResponse{OK: true, Message: "Password changed. Log in with your new password."})
}
