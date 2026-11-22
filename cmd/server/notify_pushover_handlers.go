package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/users"
)

// notifyPushoverJSON is the caller's Pushover state, in GET
// /api/account/notifications while the instance offers Pushover.
type notifyPushoverJSON struct {
	Configured  bool   `json:"configured"` // a key is saved
	Enabled     bool   `json:"enabled"`
	KeyHint     string `json:"keyHint,omitempty"` // the last 4 characters of the saved key
	ValidatedAt string `json:"validatedAt,omitempty"`
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
}

func (s *Server) pushoverState(uid int64) (*notifyPushoverJSON, error) {
	if s.auth.set.notify.pushover == nil {
		return nil, nil
	}
	p, ok, err := s.auth.st.PushoverFor(uid)
	if err != nil || !ok {
		return &notifyPushoverJSON{}, err
	}
	out := &notifyPushoverJSON{Configured: true, Enabled: p.Enabled, ValidatedAt: rfc3339(p.ValidatedAt)}
	if len(p.UserKey) >= 4 {
		out.KeyHint = p.UserKey[len(p.UserKey)-4:]
	}
	return out, nil
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
// counts toward the monthly cap.
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
	ctx, cancel := context.WithTimeout(r.Context(), pushoverClientTimeout)
	err = a.set.notify.pushover.client.Send(ctx, p.UserKey, pushoverMessage{
		Title:   a.set.baseURL.Host,
		Message: "Test from " + a.set.baseURL.Host + ": Pushover alerts for your watched nodes are set up.",
		URL:     a.set.baseURL.String() + "/#/account?section=notifications", URLTitle: "Open notifications",
	})
	cancel()
	if err == nil {
		if err := a.st.RecordPushoverSend(); err != nil {
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
