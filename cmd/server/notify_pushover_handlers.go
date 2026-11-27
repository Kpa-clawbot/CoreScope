package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/users"
)

// notifyPushoverJSON is the caller's Pushover state, in GET
// /api/account/notifications while the instance offers Pushover.
type notifyPushoverJSON struct {
	Subscribe   bool   `json:"subscribe,omitempty"` // "Connect Pushover" is offered
	Configured  bool   `json:"configured"`          // a key is saved
	Enabled     bool   `json:"enabled"`
	KeyHint     string `json:"keyHint,omitempty"` // the last 4 characters of the saved key
	ValidatedAt string `json:"validatedAt,omitempty"`
}

// pushoverSubscribeJSON answers POST .../pushover/subscribe.
type pushoverSubscribeJSON struct {
	URL string `json:"url"` // the pushover.net subscription page
}

// pushoverKeyHint is the part of a key the API shows: its last 4 characters.
func pushoverKeyHint(key string) string {
	if len(key) < 4 {
		return ""
	}
	return key[len(key)-4:]
}

type notifyPushoverRequest struct {
	UserKey *string `json:"userKey"`
	Enabled *bool   `json:"enabled"`
}

func (s *Server) registerPushoverRoutes(r *mux.Router) {
	if s.auth.set.notify.pushover == nil {
		return
	}
	r.HandleFunc("/api/account/notifications/pushover", s.withUser(s.handlePushoverPut)).Methods(http.MethodPut)
	r.HandleFunc("/api/account/notifications/pushover", s.withUser(s.handlePushoverDelete)).Methods(http.MethodDelete)
	r.HandleFunc("/api/account/notifications/pushover/test", s.withUser(s.handlePushoverTest)).Methods(http.MethodPost)
	if s.auth.set.notify.pushover.subscriptionCode != "" {
		r.HandleFunc("/api/account/notifications/pushover/subscribe", s.withUser(s.handlePushoverSubscribe)).Methods(http.MethodPost)
		// Plain GETs: pushover.net sends the browser back here.
		r.HandleFunc("/api/account/notifications/pushover/subscribed/{token}", s.handlePushoverSubscribed).Methods(http.MethodGet)
		r.HandleFunc("/api/account/notifications/pushover/subscribe-failed", s.handlePushoverSubscribeFailed).Methods(http.MethodGet)
	}
}

// pushoverSubscriptions holds the one-time tokens of "Connect Pushover"
// round trips: the token in the return URL ties the key Pushover hands
// back to the user who started, so a crafted link can't plant another
// key in someone's account. In memory: a restart only means trying again.
type pushoverSubscriptions struct {
	mu      sync.Mutex
	pending map[string]pushoverSubscription
}

type pushoverSubscription struct {
	userID  int64
	expires time.Time
}

const pushoverSubscriptionLifetime = 15 * time.Minute

func (p *pushoverSubscriptions) start(userID int64, now time.Time) (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(b[:])
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending == nil {
		p.pending = map[string]pushoverSubscription{}
	}
	for t, sub := range p.pending {
		if !sub.expires.After(now) || sub.userID == userID {
			delete(p.pending, t) // one round trip per user at a time
		}
	}
	p.pending[token] = pushoverSubscription{userID: userID, expires: now.Add(pushoverSubscriptionLifetime)}
	return token, nil
}

// finish consumes token; it reports whether token was started by userID
// and is still valid.
func (p *pushoverSubscriptions) finish(token string, userID int64, now time.Time) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	sub, ok := p.pending[token]
	if !ok {
		return false
	}
	delete(p.pending, token)
	return sub.userID == userID && sub.expires.After(now)
}

// handlePushoverSubscribe starts "Connect Pushover": it answers the
// pushover.net subscription URL to send the browser to.
func (s *Server) handlePushoverSubscribe(w http.ResponseWriter, r *http.Request, u *users.User, _ *users.Session) {
	a := s.auth
	token, err := a.pushoverSubs.start(u.ID, a.notify.now())
	if err != nil {
		notifyInternal(w, "start Pushover subscription", u.ID, err)
		return
	}
	ps, base := a.set.notify.pushover, a.set.baseURL.String()
	q := url.Values{
		"success": {base + "/api/account/notifications/pushover/subscribed/" + token},
		"failure": {base + "/api/account/notifications/pushover/subscribe-failed"},
	}
	writeJSON(w, pushoverSubscribeJSON{URL: ps.subscribeBase + url.PathEscape(ps.subscriptionCode) + "?" + q.Encode()})
}

// pushoverAccountRedirect sends the browser back to the notification
// settings with an outcome the account page reports. A signed-out browser
// goes to the login page, which reports it there.
func (s *Server) pushoverAccountRedirect(w http.ResponseWriter, r *http.Request, outcome string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer") // the URL carried a key
	page := "/#/account?section=notifications&pushover="
	if outcome == "signin" {
		page = "/#/account/login?pushover="
	}
	http.Redirect(w, r, s.auth.set.baseURL.String()+page+outcome, http.StatusSeeOther)
}

// handlePushoverSubscribed is where pushover.net returns the browser with
// the subscribed user key. It needs the session of the user who started,
// and that user's one-time token.
func (s *Server) handlePushoverSubscribed(w http.ResponseWriter, r *http.Request) {
	a := s.auth
	u, _ := a.currentUser(w, r)
	if u == nil {
		s.pushoverAccountRedirect(w, r, "signin")
		return
	}
	token := mux.Vars(r)["token"]
	q := r.URL.Query()
	key := strings.TrimSpace(q.Get("pushover_user_key"))
	subscribing := !(key == "" && q.Get("pushover_unsubscribed") == "1")
	if subscribing {
		if !pushoverKeyPattern.MatchString(key) {
			s.pushoverAccountRedirect(w, r, "failed")
			return
		}
		// Before the token is used up: when busy, the same link works
		// again later.
		if ok, _ := a.pushover.take("user:" + strconv.FormatInt(u.ID, 10)); !ok {
			s.pushoverAccountRedirect(w, r, "busy")
			return
		}
	}
	if !a.pushoverSubs.finish(token, u.ID, a.notify.now()) {
		s.pushoverAccountRedirect(w, r, "expired")
		return
	}
	if !subscribing {
		// The user unsubscribed on pushover.net: forget that key, unless
		// another one has been saved since.
		if p, ok, err := a.st.PushoverFor(u.ID); err == nil && ok && p.UserKey == strings.TrimSpace(q.Get("pushover_unsubscribed_user_key")) {
			if err := a.st.DeletePushover(u.ID); err != nil {
				log.Printf("[notify] remove unsubscribed Pushover key for user #%d: %v", u.ID, err)
				s.pushoverAccountRedirect(w, r, "failed")
				return
			}
			a.audit(&u.ID, "notify.pushover.remove", nil, nil)
		}
		s.pushoverAccountRedirect(w, r, "disconnected")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), pushoverClientTimeout)
	err := a.set.notify.pushover.client.Validate(ctx, key)
	cancel()
	if err != nil {
		log.Printf("[notify] Pushover subscription for user #%d not saved: %v", u.ID, err)
		s.pushoverAccountRedirect(w, r, "failed")
		return
	}
	if _, err := a.st.SetPushoverKey(u.ID, key); err != nil {
		log.Printf("[notify] save Pushover subscription for user #%d: %v", u.ID, err)
		s.pushoverAccountRedirect(w, r, "failed")
		return
	}
	a.audit(&u.ID, "notify.pushover.subscribe", nil, nil)
	s.pushoverAccountRedirect(w, r, "connected")
}

func (s *Server) handlePushoverSubscribeFailed(w http.ResponseWriter, r *http.Request) {
	s.pushoverAccountRedirect(w, r, "cancelled")
}

func (s *Server) pushoverState(uid int64) (*notifyPushoverJSON, error) {
	if s.auth.set.notify.pushover == nil {
		return nil, nil
	}
	subscribe := s.auth.set.notify.pushover.subscriptionCode != ""
	p, ok, err := s.auth.st.PushoverFor(uid)
	if err != nil || !ok {
		return &notifyPushoverJSON{Subscribe: subscribe}, err
	}
	return &notifyPushoverJSON{Subscribe: subscribe, Configured: true, Enabled: p.Enabled,
		KeyHint: pushoverKeyHint(p.UserKey), ValidatedAt: rfc3339(p.ValidatedAt)}, nil
}

// handlePushoverPut saves a user key (checked with Pushover first), and/or
// turns the saved destination on or off. It answers the account state.
func (s *Server) handlePushoverPut(w http.ResponseWriter, r *http.Request, u *users.User, _ *users.Session) {
	var req notifyPushoverRequest
	if !decodeJSONMax(w, r, &req, 1024, http.StatusRequestEntityTooLarge) {
		return
	}
	if req.UserKey == nil && req.Enabled == nil {
		writeError(w, http.StatusBadRequest, "send userKey, enabled, or both")
		return
	}
	a := s.auth
	if req.UserKey != nil {
		key := strings.TrimSpace(*req.UserKey)
		if !pushoverKeyPattern.MatchString(key) {
			writeError(w, http.StatusBadRequest, "A Pushover user key is 30 letters and digits.")
			return
		}
		if !a.allow(w, r, a.pushover, "user:"+strconv.FormatInt(u.ID, 10)) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), pushoverClientTimeout)
		err := a.set.notify.pushover.client.Validate(ctx, key)
		cancel()
		if writePushoverError(w, err) {
			return
		}
		if _, err := a.st.SetPushoverKey(u.ID, key); err != nil {
			if errors.Is(err, users.ErrNotFound) {
				writeError(w, http.StatusForbidden, "account not active")
				return
			}
			notifyInternal(w, "save Pushover key", u.ID, err)
			return
		}
		a.audit(&u.ID, "notify.pushover.key", nil, nil)
	}
	if req.Enabled != nil && (req.UserKey == nil || !*req.Enabled) {
		if _, err := a.st.SetPushoverEnabled(u.ID, *req.Enabled); err != nil {
			if errors.Is(err, users.ErrNotFound) {
				writeError(w, http.StatusConflict, "Add your Pushover user key first.")
				return
			}
			notifyInternal(w, "change Pushover", u.ID, err)
			return
		}
	}
	s.writeNotifyAccount(w, u)
}

func (s *Server) handlePushoverDelete(w http.ResponseWriter, r *http.Request, u *users.User, _ *users.Session) {
	if err := s.auth.st.DeletePushover(u.ID); err != nil {
		notifyInternal(w, "remove Pushover key", u.ID, err)
		return
	}
	s.auth.audit(&u.ID, "notify.pushover.remove", nil, nil)
	s.writeNotifyAccount(w, u)
}

// handlePushoverTest sends one message to the saved key right away. It
// counts toward the monthly cap and the user's daily limit.
func (s *Server) handlePushoverTest(w http.ResponseWriter, r *http.Request, u *users.User, _ *users.Session) {
	a := s.auth
	p, ok, err := a.st.PushoverFor(u.ID)
	if err != nil {
		notifyInternal(w, "read Pushover key", u.ID, err)
		return
	}
	if !ok || !p.Enabled {
		writeError(w, http.StatusConflict, "Turn Pushover on with your user key first.")
		return
	}
	if !a.allow(w, r, a.pushover, "user:"+strconv.FormatInt(u.ID, 10)) {
		return
	}
	now := s.auth.notify.now()
	if capped, err := a.pushoverCapReached(now); err != nil {
		notifyInternal(w, "count Pushover messages", u.ID, err)
		return
	} else if capped {
		writeError(w, http.StatusServiceUnavailable, "This instance reached its monthly Pushover limit.")
		return
	}
	if sent, err := a.st.PushoverSentToUserSince(u.ID, now.Add(-24*time.Hour)); err != nil {
		notifyInternal(w, "count Pushover messages", u.ID, err)
		return
	} else if sent >= a.set.notify.perUserPerDay {
		writeError(w, http.StatusTooManyRequests, "You reached your daily Pushover limit. Try again tomorrow.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), pushoverClientTimeout)
	err = a.set.notify.pushover.client.Send(ctx, p.UserKey, pushoverMessage{
		Title:   a.set.baseURL.Host,
		Message: "Test from " + a.set.baseURL.Host + ": Pushover alerts for your watched nodes are set up.",
		URL:     a.set.baseURL.String() + "/#/account?section=notifications", URLTitle: "Open notifications",
	})
	cancel()
	if err == nil {
		if err := a.st.RecordPushoverSend(u.ID); err != nil {
			log.Printf("[notify] count Pushover test for user #%d: %v", u.ID, err)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var pe *pushoverError
	if errors.As(err, &pe) && pe.InvalidUser {
		if _, derr := a.st.DisablePushoverKey(u.ID, p.UserKey); derr != nil {
			log.Printf("[notify] turn off rejected Pushover key for user #%d: %v", u.ID, derr)
		}
	}
	writePushoverError(w, err)
}

// writePushoverError answers a failed Pushover call and reports whether it
// wrote a response (false for a nil error).
func writePushoverError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	var pe *pushoverError
	switch {
	case errors.As(err, &pe) && pe.InvalidUser:
		writeError(w, http.StatusBadRequest, "Pushover doesn't recognize this user key.")
	case errors.As(err, &pe) && pe.Status == http.StatusTooManyRequests:
		writeError(w, http.StatusServiceUnavailable, "This instance reached its Pushover limit. Try again later.")
	default:
		log.Printf("[notify] Pushover request failed: %v", err)
		writeError(w, http.StatusBadGateway, "Couldn't reach Pushover. Try again in a moment.")
	}
	return true
}
