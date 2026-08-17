package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/meshcore-analyzer/admindb"
)

// adminSessionCookieName is the HttpOnly cookie carrying the opaque
// session token. The token itself is only ever compared against a
// SHA-256 hash stored server-side (admindb.ValidateSession) — losing
// admin.db does not hand out usable session tokens.
const adminSessionCookieName = "corescope_admin_session"

// adminCSRFCookieName is a deliberately NON-HttpOnly cookie set
// alongside the session cookie at login. It carries a random token
// that admin.js reads and echoes back as the X-CSRF-Token header on
// every state-changing (POST) request — the "double-submit cookie"
// pattern. A cross-site page can trigger a browser into sending our
// session cookie (that's the attack CSRF defends against), but it
// cannot READ this cookie's value (same-origin policy) and so cannot
// construct a matching header. This is defense-in-depth on top of the
// session cookie's SameSite=Strict, which already blocks the cookie
// from being sent on cross-site requests in modern browsers.
const adminCSRFCookieName = "corescope_admin_csrf"

// adminCSRFHeaderName is the header admin.js must echo the CSRF cookie
// value back in for state-changing requests.
const adminCSRFHeaderName = "X-CSRF-Token"

// Best-effort brute-force mitigation: lock a username out for
// loginLockoutDuration after loginMaxFailures consecutive failed
// attempts. This is in-memory (not persisted, not shared across
// replicas) — adequate for a single-instance deployment, not a
// substitute for a real rate limiter under heavier threat models.
const (
	loginMaxFailures     = 5
	loginLockoutDuration = 15 * time.Minute
)

type loginAttemptState struct {
	failures    int
	lockedUntil time.Time
}

type adminContextKey struct{}

func adminFromContext(ctx context.Context) *admindb.Admin {
	a, _ := ctx.Value(adminContextKey{}).(*admindb.Admin)
	return a
}

// --- Login attempt bookkeeping ---

func (s *Server) loginLockedOut(username string) (locked bool, retryAfter time.Duration) {
	key := strings.ToLower(strings.TrimSpace(username))
	s.loginAttemptsMu.Lock()
	defer s.loginAttemptsMu.Unlock()
	st, ok := s.loginAttempts[key]
	if !ok || st.lockedUntil.IsZero() || time.Now().After(st.lockedUntil) {
		return false, 0
	}
	return true, time.Until(st.lockedUntil)
}

func (s *Server) recordLoginFailure(username string) {
	key := strings.ToLower(strings.TrimSpace(username))
	s.loginAttemptsMu.Lock()
	defer s.loginAttemptsMu.Unlock()
	if s.loginAttempts == nil {
		s.loginAttempts = make(map[string]*loginAttemptState)
	}
	st, ok := s.loginAttempts[key]
	if !ok {
		st = &loginAttemptState{}
		s.loginAttempts[key] = st
	}
	st.failures++
	if st.failures >= loginMaxFailures {
		st.lockedUntil = time.Now().Add(loginLockoutDuration)
	}
}

func (s *Server) resetLoginAttempts(username string) {
	key := strings.ToLower(strings.TrimSpace(username))
	s.loginAttemptsMu.Lock()
	defer s.loginAttemptsMu.Unlock()
	delete(s.loginAttempts, key)
}

// --- Cookie helpers ---

// isRequestSecure reports whether the client's connection to the edge
// was HTTPS. Caddy terminates TLS in front of the Go server (see
// docker/Caddyfile.prod) and forwards X-Forwarded-Proto; direct TLS
// (r.TLS != nil) covers non-proxied deployments.
func isRequestSecure(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func setAdminSessionCookie(w http.ResponseWriter, r *http.Request, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   isRequestSecure(r),
		SameSite: http.SameSiteStrictMode,
	})
}

func clearAdminSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   isRequestSecure(r),
		SameSite: http.SameSiteStrictMode,
	})
}

// generateCSRFToken returns a random 32-byte token, base64url-encoded.
// Unlike the session token, this is never stored server-side — the
// double-submit pattern validates it by comparing the cookie value
// against the request header on each state-changing request.
func generateCSRFToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func setAdminCSRFCookie(w http.ResponseWriter, r *http.Request, token string, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     adminCSRFCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expiresAt,
		HttpOnly: false, // admin.js must be able to read this
		Secure:   isRequestSecure(r),
		SameSite: http.SameSiteStrictMode,
	})
}

func clearAdminCSRFCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     adminCSRFCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: false,
		Secure:   isRequestSecure(r),
		SameSite: http.SameSiteStrictMode,
	})
}

// --- Middleware ---

// requireAdmin validates the admin session cookie and, on success,
// stashes the authenticated *admindb.Admin on the request context for
// downstream handlers (see adminFromContext).
func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(adminSessionCookieName)
		if err != nil || cookie.Value == "" {
			writeError(w, http.StatusUnauthorized, "not logged in")
			return
		}
		a, err := s.admin.ValidateSession(cookie.Value)
		if err != nil {
			if !errors.Is(err, admindb.ErrSessionInvalid) {
				log.Printf("[admin-auth] ValidateSession error: %v", err)
			}
			clearAdminSessionCookie(w, r)
			writeError(w, http.StatusUnauthorized, "session expired or invalid")
			return
		}
		ctx := context.WithValue(r.Context(), adminContextKey{}, a)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireSuperAdmin additionally requires the authenticated admin to
// hold the super_admin role — the only capability gated by role in
// this phase (creating new admin accounts).
func (s *Server) requireSuperAdmin(next http.Handler) http.Handler {
	return s.requireAdmin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a := adminFromContext(r.Context())
		if a == nil || a.Role != admindb.RoleSuperAdmin {
			writeError(w, http.StatusForbidden, "super-admin role required")
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// requireCSRF enforces the double-submit cookie check on state-changing
// admin requests: the X-CSRF-Token header must be present and match the
// corescope_admin_csrf cookie set at login. Only meaningful once a
// session exists, so this wraps handlers that already sit behind
// requireAdmin/requireSuperAdmin — it does not apply to POST
// /api/admin/login itself (no CSRF cookie exists yet at that point).
func (s *Server) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(adminCSRFCookieName)
		if err != nil || cookie.Value == "" {
			writeError(w, http.StatusForbidden, "missing CSRF token")
			return
		}
		header := r.Header.Get(adminCSRFHeaderName)
		if header == "" || !constantTimeEqual(header, cookie.Value) {
			writeError(w, http.StatusForbidden, "invalid CSRF token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- Wire types ---

type adminView struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	Disabled  bool   `json:"disabled"`
	CreatedAt string `json:"createdAt"`
	CreatedBy *int64 `json:"createdBy,omitempty"`
}

func toAdminView(a *admindb.Admin) adminView {
	return adminView{
		ID:        a.ID,
		Username:  a.Username,
		Role:      string(a.Role),
		Disabled:  a.Disabled,
		CreatedAt: a.CreatedAt.Format(time.RFC3339),
		CreatedBy: a.CreatedBy,
	}
}

// --- Handlers ---

type adminLoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (s *Server) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	var req adminLoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}

	if locked, _ := s.loginLockedOut(req.Username); locked {
		writeError(w, http.StatusTooManyRequests, "too many failed attempts — try again later")
		return
	}

	a, err := s.admin.Authenticate(req.Username, req.Password)
	if err != nil {
		s.recordLoginFailure(req.Username)
		writeError(w, http.StatusUnauthorized, "invalid username or password")
		return
	}
	s.resetLoginAttempts(req.Username)

	token, expiresAt, err := s.admin.CreateSession(a.ID)
	if err != nil {
		log.Printf("[admin-auth] CreateSession error: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}
	setAdminSessionCookie(w, r, token, expiresAt)

	csrfToken, err := generateCSRFToken()
	if err != nil {
		log.Printf("[admin-auth] generateCSRFToken error: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to create session")
		return
	}
	setAdminCSRFCookie(w, r, csrfToken, expiresAt)

	writeJSON(w, toAdminView(a))
}

func (s *Server) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(adminSessionCookieName); err == nil && cookie.Value != "" {
		if err := s.admin.DeleteSession(cookie.Value); err != nil {
			log.Printf("[admin-auth] DeleteSession error: %v", err)
		}
	}
	clearAdminSessionCookie(w, r)
	clearAdminCSRFCookie(w, r)
	writeJSON(w, map[string]bool{"ok": true})
}

type changePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

// handleChangePassword lets a logged-in admin change their own
// password. Requires the current password — see admindb.ChangePassword
// for why a session cookie alone isn't sufficient. On success,
// invalidates every OTHER session belonging to this admin (e.g. a
// device that's since been lost, or in case the old password had
// leaked) while keeping the session making this request logged in.
func (s *Server) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	a := adminFromContext(r.Context())
	if a == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	var req changePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.CurrentPassword == "" || req.NewPassword == "" {
		writeError(w, http.StatusBadRequest, "current and new password are required")
		return
	}

	if err := s.admin.ChangePassword(a.ID, req.CurrentPassword, req.NewPassword); err != nil {
		switch {
		case errors.Is(err, admindb.ErrInvalidCredentials):
			writeError(w, http.StatusUnauthorized, "current password is incorrect")
		case errors.Is(err, admindb.ErrPasswordTooShort):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			log.Printf("[admin-auth] ChangePassword error: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to change password")
		}
		return
	}

	var currentToken string
	if cookie, err := r.Cookie(adminSessionCookieName); err == nil {
		currentToken = cookie.Value
	}
	if err := s.admin.DeleteOtherSessions(a.ID, currentToken); err != nil {
		// Password change already succeeded — this is best-effort
		// cleanup, don't fail the request over it.
		log.Printf("[admin-auth] DeleteOtherSessions error: %v", err)
	}

	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) handleAdminMe(w http.ResponseWriter, r *http.Request) {
	a := adminFromContext(r.Context())
	if a == nil {
		writeError(w, http.StatusUnauthorized, "not logged in")
		return
	}
	writeJSON(w, toAdminView(a))
}

func (s *Server) handleListAdmins(w http.ResponseWriter, r *http.Request) {
	admins, err := s.admin.ListAdmins()
	if err != nil {
		log.Printf("[admin-auth] ListAdmins error: %v", err)
		writeError(w, http.StatusInternalServerError, "failed to list admins")
		return
	}
	views := make([]adminView, 0, len(admins))
	for _, a := range admins {
		views = append(views, toAdminView(a))
	}
	writeJSON(w, map[string]interface{}{"admins": views})
}

type createAdminRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

func (s *Server) handleCreateAdmin(w http.ResponseWriter, r *http.Request) {
	var req createAdminRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	role := admindb.Role(strings.TrimSpace(req.Role))
	if !role.Valid() {
		writeError(w, http.StatusBadRequest, "role must be \"admin\" or \"super_admin\"")
		return
	}
	if strings.TrimSpace(req.Username) == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}

	creator := adminFromContext(r.Context())
	var createdBy *int64
	if creator != nil {
		createdBy = &creator.ID
	}

	a, err := s.admin.CreateAdmin(req.Username, req.Password, role, createdBy)
	if err != nil {
		if errors.Is(err, admindb.ErrUsernameTaken) {
			writeError(w, http.StatusConflict, "username already taken")
			return
		}
		log.Printf("[admin-auth] CreateAdmin error: %v", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, toAdminView(a))
}
