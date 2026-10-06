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
	if err != nil {
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

// withUser requires a logged-in active user; state-changing methods must
// also pass the origin + CSRF-token check.
func (s *Server) withUser(h authedHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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

// withAdmin is withUser plus role admin.
func (s *Server) withAdmin(h authedHandler) http.HandlerFunc {
	return s.withUser(func(w http.ResponseWriter, r *http.Request, u *users.User, sess *users.Session) {
		if u.Role != users.RoleAdmin {
			writeError(w, http.StatusForbidden, "admin role required")
			return
		}
		h(w, r, u, sess)
	})
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
