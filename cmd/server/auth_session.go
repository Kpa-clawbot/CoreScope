package main

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/meshcore-analyzer/users"
)

const (
	sessionCookieName = "cs_session"
	csrfHeader        = "X-CS-CSRF"
)

// Device-token scopes. A token holds the scopes it asked for at
// POST /api/auth/device-token (deviceScopeRX when it asked for none).
const (
	// deviceScopeRX is CoreDrive RX: account settings and companion linking.
	deviceScopeRX = "rx"
	// deviceScopeRepeaterClaim claims a repeater and removes the caller's own
	// claim.
	deviceScopeRepeaterClaim = "repeater:claim"
	// deviceScopeRepeaterEdit edits a repeater's details, still only where
	// CanEditRepeater allows the account to.
	deviceScopeRepeaterEdit = "repeater:edit"
)

// deviceScopes are the scopes a client may request, in a fixed order.
var deviceScopes = []string{deviceScopeRX, deviceScopeRepeaterClaim, deviceScopeRepeaterEdit}

// repeaterRoute splits /api/repeaters/{pubkey}/{what} and returns what
// ("info" or "claim"), or "" for any other path. The pubkey segment is only
// checked to be non-empty; the handler validates it.
func repeaterRoute(path string) string {
	rest, ok := strings.CutPrefix(path, "/api/repeaters/")
	if !ok {
		return ""
	}
	pk, what, ok := strings.Cut(rest, "/")
	if !ok || pk == "" || (what != "info" && what != "claim") {
		return ""
	}
	return what
}

// bearerScopesFor returns the scopes that let a device token use method on
// path (any one of them is enough), or nil when no device token may use it.
// Any device token may read and end its own login; RX routes need rx;
// repeater claims need repeater:claim and repeater edits repeater:edit. A
// read of one repeater's details takes either repeater scope (it adds the
// caller's canEdit and claimedByMe), and so does the list of the caller's
// own claims (My repeaters), which an app cannot build from the public
// catalog: claimers there are display names, and a claimed repeater may
// have left the node list.
func bearerScopesFor(method, path string) []string {
	switch {
	case path == "/api/auth/me", path == "/api/auth/logout":
		return deviceScopes
	case path == "/api/account/repeaters":
		if method == http.MethodGet || method == http.MethodHead {
			return []string{deviceScopeRepeaterClaim, deviceScopeRepeaterEdit}
		}
		return nil
	case path == "/api/account/settings", path == "/api/account/companions",
		strings.HasPrefix(path, "/api/account/companions/"):
		return []string{deviceScopeRX}
	}
	switch repeaterRoute(path) {
	case "claim":
		if method == http.MethodPost || method == http.MethodDelete {
			return []string{deviceScopeRepeaterClaim}
		}
	case "info":
		switch method {
		case http.MethodPut:
			return []string{deviceScopeRepeaterEdit}
		case http.MethodGet, http.MethodHead:
			return []string{deviceScopeRepeaterClaim, deviceScopeRepeaterEdit}
		}
	}
	return nil
}

// bearerCORSPath reports whether path takes cross-origin writes from an
// allowlisted origin: the device-token login and the bearer routes. It is
// judged on the path alone, since a preflight names the method only in a
// header; outside the repeater routes the scopes do not depend on the method.
func bearerCORSPath(path string) bool {
	return path == "/api/auth/device-token" || repeaterRoute(path) != "" ||
		len(bearerScopesFor(http.MethodGet, path)) > 0
}

// hasAnyScope reports whether sess holds at least one of scopes.
func hasAnyScope(sess *users.Session, scopes []string) bool {
	for _, sc := range scopes {
		if sess.HasScope(sc) {
			return true
		}
	}
	return false
}

// bearerToken returns the token of an "Authorization: Bearer" header and
// whether the request is to be judged on it. A request that carries the
// session cookie is not: the cookie decides, so an auth proxy that adds its
// own Authorization header does not break browser logins.
func bearerToken(r *http.Request) (string, bool) {
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		return "", false
	}
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) < len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(h[len(prefix):]), true
}

// bearerUser resolves a device token for r. The code is 0 on success, 401
// for an unknown, expired or web token or an inactive account, and 403
// when the token's scope does not cover the route. A use more than a day
// after the last one extends the token by DeviceSessionTTL (sliding).
func (a *authService) bearerUser(r *http.Request, tok string) (*users.User, *users.Session, int) {
	if tok == "" {
		return nil, nil, http.StatusUnauthorized
	}
	sess, err := a.st.LookupSession(tok)
	// A web token as a bearer would skip the CSRF check: refused.
	if err != nil || sess.Kind != users.SessionKindDevice {
		return nil, nil, http.StatusUnauthorized
	}
	u, err := a.st.GetByID(sess.UserID)
	if err != nil || u.Status != users.StatusActive {
		return nil, nil, http.StatusUnauthorized
	}
	if need := bearerScopesFor(r.Method, r.URL.Path); len(need) == 0 || !hasAnyScope(sess, need) {
		return nil, nil, http.StatusForbidden
	}
	if time.Since(sess.LastSeenAt) > 24*time.Hour {
		_ = a.st.ExtendSession(sess.ID, users.DeviceSessionTTL)
	}
	return u, sess, 0
}

func writeBearerFail(w http.ResponseWriter, code int) {
	if code == http.StatusForbidden {
		writeError(w, http.StatusForbidden, "this token is not allowed on this route")
		return
	}
	writeError(w, http.StatusUnauthorized, "invalid or expired token")
}

func (a *authService) setSessionCookie(w http.ResponseWriter, raw string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: raw, Path: "/",
		Expires: expires, MaxAge: int(time.Until(expires).Seconds()),
		HttpOnly: true, Secure: a.set.secureCookie, SameSite: http.SameSiteLaxMode,
	})
}

func (a *authService) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: a.set.secureCookie, SameSite: http.SameSiteLaxMode})
}

// currentUser returns the active user behind the session cookie, or nils.
// A session last seen more than a day ago is extended (sliding expiry)
// and its cookie re-issued.
func (a *authService) currentUser(w http.ResponseWriter, r *http.Request) (*users.User, *users.Session) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return nil, nil
	}
	sess, err := a.st.LookupSession(c.Value)
	// A device token is a bearer credential only: sent as the cookie it
	// must not pass as a browser session.
	if err != nil || sess.Kind != users.SessionKindWeb {
		return nil, nil
	}
	u, err := a.st.GetByID(sess.UserID)
	if err != nil || u.Status != users.StatusActive {
		return nil, nil
	}
	if time.Since(sess.LastSeenAt) > 24*time.Hour {
		if err := a.st.ExtendSession(sess.ID, a.set.sessionTTL); err == nil {
			a.setSessionCookie(w, c.Value, time.Now().Add(a.set.sessionTTL))
		}
	}
	return u, sess
}

// originOK requires the request's Origin (or, if absent, Referer) to be the
// configured publicBaseUrl's origin.
func (a *authService) originOK(r *http.Request) bool {
	want := a.set.baseURL.Scheme + "://" + a.set.baseURL.Host
	if o := r.Header.Get("Origin"); o != "" {
		return strings.EqualFold(o, want)
	}
	if ref := r.Header.Get("Referer"); ref != "" {
		u, err := url.Parse(ref)
		return err == nil && strings.EqualFold(u.Scheme+"://"+u.Host, want)
	}
	return false
}

func (a *authService) csrfOK(r *http.Request, sess *users.Session) bool {
	return a.originOK(r) && constantTimeEqual(r.Header.Get(csrfHeader), sess.CSRFToken)
}

func isSafeMethod(m string) bool { return m == http.MethodGet || m == http.MethodHead }

type authedHandler func(w http.ResponseWriter, r *http.Request, u *users.User, sess *users.Session)

// optionalUser returns the caller of a route that also answers anonymously
// (the read of one repeater's details), or nils for an anonymous one. A
// bearer that does not resolve to an active device token with a scope for
// the route counts as anonymous: an auth proxy may add its own
// Authorization header (see bearerToken), and a visitor behind it must still
// get the public view. A client learns its token is gone from /api/auth/me
// or the 401 on its next write. Otherwise the cookie session decides, as
// for currentUser.
func (s *Server) optionalUser(w http.ResponseWriter, r *http.Request) (*users.User, *users.Session) {
	tok, ok := bearerToken(r)
	if !ok {
		return s.auth.currentUser(w, r)
	}
	if u, sess, code := s.auth.bearerUser(r, tok); code == 0 {
		return u, sess
	}
	return nil, nil
}

// withUser requires a logged-in active user. A request with an
// Authorization: Bearer header and no session cookie is judged on that
// device token alone (scope check, no CSRF: browsers never attach it on
// their own).
// Otherwise the cookie session is used, and state-changing methods must
// pass the origin + CSRF-token check.
func (s *Server) withUser(h authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if tok, ok := bearerToken(r); ok {
			u, sess, code := s.auth.bearerUser(r, tok)
			if code != 0 {
				writeBearerFail(w, code)
				return
			}
			h(w, r, u, sess)
			return
		}
		u, sess := s.auth.currentUser(w, r)
		if u == nil {
			writeError(w, http.StatusUnauthorized, "not logged in")
			return
		}
		if !isSafeMethod(r.Method) && !s.auth.csrfOK(r, sess) {
			writeError(w, http.StatusForbidden, "CSRF check failed")
			return
		}
		h(w, r, u, sess)
	}
}

// adminSessionOK is the one place that decides whether a session user may
// act as admin: state-changing methods must pass the origin + CSRF check,
// and the role must be admin. It writes the 403 itself when not.
func (a *authService) adminSessionOK(w http.ResponseWriter, r *http.Request, u *users.User, sess *users.Session) bool {
	if !isSafeMethod(r.Method) && !a.csrfOK(r, sess) {
		writeError(w, http.StatusForbidden, "CSRF check failed")
		return false
	}
	if u.Role != users.RoleAdmin {
		writeError(w, http.StatusForbidden, "admin role required")
		return false
	}
	return true
}

// withAdmin requires a logged-in admin (see adminSessionOK).
func (s *Server) withAdmin(h authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := bearerToken(r); ok {
			writeBearerFail(w, http.StatusForbidden)
			return
		}
		u, sess := s.auth.currentUser(w, r)
		if u == nil {
			writeError(w, http.StatusUnauthorized, "not logged in")
			return
		}
		if !s.auth.adminSessionOK(w, r, u, sess) {
			return
		}
		h(w, r, u, sess)
	}
}

// requireOrigin guards unauthenticated state-changing endpoints (login CSRF).
func (s *Server) requireOrigin(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.auth.originOK(r) {
			writeError(w, http.StatusForbidden, "request origin not allowed")
			return
		}
		h(w, r)
	}
}
