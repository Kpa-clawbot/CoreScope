package main

import (
	"errors"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/users"
)

type notifyPushCapabilityJSON struct {
	RelayOrigin     string   `json:"relayOrigin"`
	AnalyzerID      string   `json:"analyzerId"`
	ContractVersion int      `json:"contractVersion"`
	Platforms       []string `json:"platforms"`
}

type notifyPushLinkJSON struct {
	Proof string `json:"proof"`
}

type notifyPushDeviceJSON struct {
	SubscriptionID string    `json:"subscription_id"`
	Status         string    `json:"status"`
	ExpiresAt      time.Time `json:"expires_at"`
}

var (
	notifyPushIDPattern      = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	notifyLinkProofPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{43,512}$`)
	notifyPushDeviceRequests sync.Map
)

type notifyPushDeviceKey struct {
	analyzerID     string
	installationID string
}

func (s *Server) registerNotifyPushRoutes(r *mux.Router) {
	r.HandleFunc("/api/account/notifications/devices/{installationID}", s.withUser(s.handleNotifyPushLink)).Methods(http.MethodPut)
	r.HandleFunc("/api/account/notifications/devices/{installationID}", s.withUser(s.handleNotifyPushUnlink)).Methods(http.MethodDelete)
}

// Only one link or unlink may be in flight for an installation on this
// analyzer. Skipping concurrent work prevents a slow relay response from
// overwriting a later device change. The entries live for one request.
func (s *Server) beginNotifyPushDevice(w http.ResponseWriter, r *http.Request, ns notifySettings) (notifyPushDeviceKey, bool) {
	if ns.relay == nil || s.auth.relayDisconnectPending() {
		writeError(w, http.StatusNotFound, "Push is not enabled")
		return notifyPushDeviceKey{}, false
	}
	key := notifyPushDeviceKey{ns.relayAnalyzerID, mux.Vars(r)["installationID"]}
	if !notifyPushIDPattern.MatchString(key.installationID) {
		writeError(w, http.StatusBadRequest, "invalid installation ID")
		return key, false
	}
	if _, busy := notifyPushDeviceRequests.LoadOrStore(key, struct{}{}); busy {
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "device change in progress")
		return key, false
	}
	return key, true
}

func (s *Server) handleNotifyPushLink(w http.ResponseWriter, r *http.Request, u *users.User, _ *users.Session) {
	if s.auth.relaySettings().relay == nil || s.auth.relayDisconnectPending() {
		writeError(w, http.StatusNotFound, "Push is not enabled")
		return
	}
	var request notifyPushLinkJSON
	if !decodeJSONMax(w, r, &request, 1024, http.StatusRequestEntityTooLarge) {
		return
	}
	if !notifyLinkProofPattern.MatchString(request.Proof) {
		writeError(w, http.StatusBadRequest, "invalid link proof")
		return
	}

	s.auth.relayDelivery.mu.RLock()
	defer s.auth.relayDelivery.mu.RUnlock()
	ns := s.auth.relaySettings()
	key, ok := s.beginNotifyPushDevice(w, r, ns)
	if !ok {
		return
	}
	defer notifyPushDeviceRequests.Delete(key)

	st := s.auth.st
	bindingID, _, err := users.NewToken()
	if err != nil {
		notifyInternal(w, "create push binding ID", u.ID, err)
		return
	}
	binding, err := st.StagePushBinding(key.analyzerID, key.installationID, u.ID, bindingID, users.HashToken(request.Proof))
	switch {
	case errors.Is(err, users.ErrPushBindingLimit):
		writeError(w, http.StatusConflict, "too many push devices")
		return
	case errors.Is(err, users.ErrNotFound):
		writeError(w, http.StatusNotFound, "account no longer active")
		return
	case err != nil:
		notifyInternal(w, "stage push binding", u.ID, err)
		return
	}

	subscription, err := ns.relay.Link(r.Context(), ns.relayCredential, notifyRelayLinkRequest{
		InstallationID: key.installationID,
		BindingID:      binding.BindingID,
		Proof:          request.Proof,
	})
	if err != nil {
		s.writeNotifyPushLinkError(w, binding, err)
		return
	}
	if subscription.Status != "active" || !notifyPushIDPattern.MatchString(subscription.SubscriptionID) ||
		!subscription.ExpiresAt.After(s.auth.notify.now()) {
		writeError(w, http.StatusServiceUnavailable, "relay returned an inactive or invalid subscription")
		return
	}
	active, err := st.ActivatePushBinding(binding, subscription.SubscriptionID, subscription.ExpiresAt)
	if errors.Is(err, users.ErrTokenExpired) {
		writeError(w, http.StatusServiceUnavailable, "relay subscription expired before activation")
		return
	}
	if errors.Is(err, users.ErrNotFound) {
		writeError(w, http.StatusConflict, "push binding is no longer current")
		return
	}
	if err != nil {
		notifyInternal(w, "activate push binding", u.ID, err)
		return
	}
	writeJSON(w, notifyPushDeviceJSON{
		SubscriptionID: active.SubscriptionID, Status: "active", ExpiresAt: active.ExpiresAt.UTC(),
	})
}

func (s *Server) writeNotifyPushLinkError(w http.ResponseWriter, binding users.PushBinding, err error) {
	var relayErr *notifyRelayError
	if errors.As(err, &relayErr) {
		switch relayErr.Status {
		case http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusGone:
			// The relay explicitly rejected the proof. Ambiguous failures keep
			// the staged binding so the same proof can safely be retried.
			if err := s.auth.st.CancelPushBinding(binding); err != nil {
				log.Printf("[notify] cancel failed push binding: %v", err)
			}
			writeError(w, relayErr.Status, "relay did not accept the link proof")
			return
		case http.StatusTooManyRequests:
			if relayErr.RetryAfter > 0 {
				seconds := (relayErr.RetryAfter-1)/time.Second + 1
				w.Header().Set("Retry-After", strconv.FormatInt(int64(seconds), 10))
			}
			writeError(w, http.StatusTooManyRequests, "push relay rate limit")
			return
		}
	}
	writeError(w, http.StatusServiceUnavailable, "push relay unavailable")
}

func (s *Server) handleNotifyPushUnlink(w http.ResponseWriter, r *http.Request, u *users.User, _ *users.Session) {
	s.auth.relayDelivery.mu.RLock()
	defer s.auth.relayDelivery.mu.RUnlock()
	key, ok := s.beginNotifyPushDevice(w, r, s.auth.relaySettings())
	if !ok {
		return
	}
	defer notifyPushDeviceRequests.Delete(key)

	if err := s.auth.st.RevokePushBinding(key.analyzerID, key.installationID, u.ID); err != nil {
		notifyInternal(w, "revoke push binding", u.ID, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
