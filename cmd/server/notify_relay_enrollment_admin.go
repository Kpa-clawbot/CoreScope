package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/meshcore-analyzer/users"
)

const (
	nodeScopeSelfEnrollRelay     = "https://push.nodescope.app"
	nodeScopeStagingRelay        = "https://push-staging.nodescope.app"
	nodeScopeSelfEnrollOriginEnv = "CORESCOPE_NODESCOPE_SELF_ENROLL_RELAY_ORIGIN"
)

func nodeScopeSelfEnrollOrigin(raw string) string {
	switch origin := strings.TrimSuffix(strings.TrimSpace(raw), "/"); origin {
	case "":
		if strings.TrimSpace(raw) == "" {
			return nodeScopeSelfEnrollRelay
		}
	case nodeScopeSelfEnrollRelay, nodeScopeStagingRelay:
		return origin
	}
	return ""
}

func relayPendingPath(dbPath, relayOrigin string) string {
	if relayOrigin == nodeScopeStagingRelay {
		// This filename was already used by staging before production support.
		return dbPath + ".nodescope-pending.json"
	}
	return dbPath + ".nodescope-pending-production.json"
}

type relayEnrollmentFlow struct {
	mu        sync.Mutex
	proofKey  atomic.Pointer[ed25519.PrivateKey]
	transport http.RoundTripper // Override in tests.
}

type relayEnrollmentStatus struct {
	State             string `json:"state"`
	PublicOrigin      string `json:"publicOrigin"`
	RelayOrigin       string `json:"relayOrigin"`
	AnalyzerID        string `json:"analyzerId,omitempty"`
	Paused            bool   `json:"paused,omitempty"`
	DisconnectPending bool   `json:"disconnectPending,omitempty"`
}

func relayPublicOrigin(base *url.URL) string {
	if base == nil || base.Scheme != "https" || base.User != nil || base.RawQuery != "" || base.Fragment != "" ||
		(base.EscapedPath() != "" && base.EscapedPath() != "/") || (base.Port() != "" && base.Port() != "443") {
		return ""
	}
	host := strings.ToLower(base.Hostname())
	if !strings.Contains(host, ".") || strings.HasSuffix(host, ".") || net.ParseIP(host) != nil {
		return ""
	}
	return "https://" + host
}

func (a *authService) relayEnrollmentStatus() relayEnrollmentStatus {
	status := relayEnrollmentStatus{
		State: "available", RelayOrigin: nodeScopeSelfEnrollOrigin(a.set.nodeScopeSelfEnrollOrigin), PublicOrigin: relayPublicOrigin(a.set.baseURL),
	}
	settings := a.relaySettings()
	switch {
	case !a.set.notify.enabled:
		status.State = "notifications_disabled"
	case settings.relay != nil:
		status.State = "connected"
		if a.set.notify.relay != nil {
			status.State = "manual"
		}
		status.AnalyzerID, status.RelayOrigin = settings.relayAnalyzerID, settings.relayOrigin
		status.Paused = a.relayDeliveryPaused()
		status.DisconnectPending = a.relayDisconnectPending()
	case status.PublicOrigin == "" || status.RelayOrigin == "":
		status.State = "unavailable"
	}
	return status
}

func (s *Server) handlePushRelayEnrollmentStatus(w http.ResponseWriter, _ *http.Request, _ *users.User, _ *users.Session) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, s.auth.relayEnrollmentStatus())
}

// The signing key exists only while an authenticated admin request is enrolling.
// Passing a relay challenge to this endpoint proves control of the public origin.
func (s *Server) handlePushRelayEnrollmentProof(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	key := s.auth.relayEnrollment.proofKey.Load()
	if key == nil {
		writeError(w, http.StatusNotFound, "no enrollment in progress")
		return
	}
	query := r.URL.Query()
	challenges := query["challenge"]
	if len(query) != 1 || len(challenges) != 1 || len(challenges[0]) != base64.RawURLEncoding.EncodedLen(32) {
		writeError(w, http.StatusBadRequest, "invalid enrollment challenge")
		return
	}
	challenge, err := base64.RawURLEncoding.Strict().DecodeString(challenges[0])
	if err != nil || len(challenge) != 32 {
		writeError(w, http.StatusBadRequest, "invalid enrollment challenge")
		return
	}
	writeJSON(w, struct {
		Signature string `json:"signature"`
	}{base64.RawURLEncoding.EncodeToString(ed25519.Sign(*key, challenge))})
}

type relayPendingEnrollment struct {
	PublicOrigin     string `json:"public_origin"`
	SenderCredential string `json:"sender_credential"`
}

func relayPendingCredential(path, publicOrigin string) (string, error) {
	// Keep the same credential across retries and process restarts. The relay
	// will return the original enrollment after verifying origin control again.
	file, err := os.Open(path)
	if err == nil {
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return "", errors.New("pending push relay enrollment file must be a private regular file")
		}
		data, err := io.ReadAll(io.LimitReader(file, 4097))
		var pending relayPendingEnrollment
		if err != nil || len(data) > 4096 || json.Unmarshal(data, &pending) != nil {
			return "", errors.New("could not read pending push relay enrollment")
		}
		if pending.PublicOrigin != publicOrigin {
			return "", errors.New("pending push relay enrollment belongs to a different public origin")
		}
		secret, err := base64.RawURLEncoding.Strict().DecodeString(pending.SenderCredential)
		if err != nil || len(secret) != 32 {
			return "", errors.New("invalid pending relay sender credential")
		}
		return pending.SenderCredential, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		return "", err
	}
	credential := base64.RawURLEncoding.EncodeToString(secret[:])
	if err := writePrivateRelayJSON(path, relayPendingEnrollment{PublicOrigin: publicOrigin, SenderCredential: credential}); err != nil {
		return "", err
	}
	return credential, nil
}

func (s *Server) handlePushRelayEnable(w http.ResponseWriter, r *http.Request, actor *users.User, _ *users.Session) {
	w.Header().Set("Cache-Control", "no-store")
	a := s.auth
	flow := &a.relayEnrollment
	if !flow.mu.TryLock() {
		writeError(w, http.StatusConflict, "push relay enrollment is already running. Try again in a moment.")
		return
	}
	defer flow.mu.Unlock()
	status := a.relayEnrollmentStatus()
	switch status.State {
	case "connected", "manual":
		writeJSON(w, status)
		return
	case "notifications_disabled":
		writeError(w, http.StatusConflict, "Enable CoreScope notifications before connecting the push relay.")
		return
	case "unavailable":
		if status.RelayOrigin == "" {
			writeError(w, http.StatusConflict, "Set CORESCOPE_NODESCOPE_SELF_ENROLL_RELAY_ORIGIN to an official NodeScope HTTPS relay.")
			return
		}
		writeError(w, http.StatusConflict, "Set publicBaseUrl to a public HTTPS origin on port 443 before enabling push.")
		return
	}
	path := relayPendingPath(a.set.dbPath, status.RelayOrigin)
	credential, err := relayPendingCredential(path, status.PublicOrigin)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not prepare the server-side enrollment credential. Check the users database directory and public URL.")
		return
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not prepare analyzer verification.")
		return
	}
	relay, err := newNotifyRelayClient(status.RelayOrigin, flow.transport)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not configure the push relay client.")
		return
	}
	flow.proofKey.Store(&privateKey)
	defer flow.proofKey.Store(nil)
	req := notifyRelaySelfEnrollmentRequest{
		Origin: status.PublicOrigin, PublicKey: base64.RawURLEncoding.EncodeToString(publicKey), SenderCredential: credential,
	}
	enrollment, err := relay.SelfEnroll(r.Context(), req)
	if err != nil {
		writeRelayEnrollmentError(w, err)
		return
	}
	verified, err := relay.Analyzer(r.Context(), credential)
	if err != nil || verified.AnalyzerID != enrollment.AnalyzerID || verified.Origin != status.PublicOrigin {
		writeError(w, http.StatusBadGateway, "Could not verify the relay enrollment. Try Enable again.")
		return
	}
	if err := a.activateRelayEnrollment(status.RelayOrigin, enrollment.AnalyzerID, credential); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not save the enrollment. Check the users database directory and try Enable again.")
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		// The file still contains the active credential, with owner-only access.
		// Enrollment is already durable and usable.
		log.Printf("[notify] could not remove the pending push relay enrollment file: %v", err)
	}
	a.audit(&actor.ID, "notify.push.enroll", nil, nil)
	writeJSON(w, a.relayEnrollmentStatus())
}

func (s *Server) handlePushRelayPause(w http.ResponseWriter, r *http.Request, actor *users.User, _ *users.Session) {
	w.Header().Set("Cache-Control", "no-store")
	var request struct {
		Paused *bool `json:"paused"`
	}
	if !decodeJSONMax(w, r, &request, 1024, http.StatusRequestEntityTooLarge) {
		return
	}
	if request.Paused == nil {
		writeError(w, http.StatusBadRequest, "paused must be true or false")
		return
	}

	a := s.auth
	if !a.relayEnrollment.mu.TryLock() {
		writeError(w, http.StatusConflict, "Another push relay change is in progress. Try again in a moment.")
		return
	}
	defer a.relayEnrollment.mu.Unlock()
	wasPaused := a.relayDeliveryPaused()
	if err := a.setRelayDeliveryPaused(*request.Paused); err != nil {
		switch {
		case errors.Is(err, errRelayNoEnrollment):
			writeError(w, http.StatusConflict, "Connect a push relay before changing delivery.")
		case errors.Is(err, errRelayDisconnectPending):
			writeError(w, http.StatusConflict, "Finish disconnecting the push relay before changing delivery.")
		default:
			log.Printf("[notify] save push delivery setting: %v", err)
			writeError(w, http.StatusInternalServerError, "Could not change push delivery. Try again.")
		}
		return
	}
	if wasPaused != *request.Paused {
		action := "notify.push.resume"
		if *request.Paused {
			action = "notify.push.pause"
		}
		a.audit(&actor.ID, action, nil, nil)
	}
	writeJSON(w, a.relayEnrollmentStatus())
}

func (s *Server) handlePushRelayDisconnect(w http.ResponseWriter, r *http.Request, actor *users.User, _ *users.Session) {
	w.Header().Set("Cache-Control", "no-store")
	a := s.auth
	if !a.relayEnrollment.mu.TryLock() {
		writeError(w, http.StatusConflict, "Another push relay change is in progress. Try again in a moment.")
		return
	}
	defer a.relayEnrollment.mu.Unlock()

	if err := a.disconnectRelayEnrollment(r.Context()); err != nil {
		switch {
		case errors.Is(err, errRelayNoEnrollment):
			writeJSON(w, a.relayEnrollmentStatus())
		case errors.Is(err, errRelayManualEnrollment):
			writeError(w, http.StatusConflict, "Disconnect this relay using the manual server configuration.")
		case errors.Is(err, errRelayRevoke):
			log.Printf("[notify] revoke push relay enrollment: %v", err)
			writeError(w, http.StatusBadGateway, "Could not confirm revocation at the relay. Try Disconnect relay again.")
		default:
			log.Printf("[notify] disconnect push relay enrollment: %v", err)
			writeError(w, http.StatusInternalServerError, "Could not finish disconnecting the push relay. Try Disconnect relay again.")
		}
		return
	}
	a.audit(&actor.ID, "notify.push.disconnect", nil, nil)
	writeJSON(w, a.relayEnrollmentStatus())
}

func writeRelayEnrollmentError(w http.ResponseWriter, err error) {
	var relay *notifyRelayError
	if errors.As(err, &relay) {
		switch relay.Status {
		case http.StatusBadRequest:
			writeError(w, http.StatusBadRequest, "The relay rejected the analyzer's public origin.")
			return
		case http.StatusConflict:
			writeError(w, http.StatusConflict, "This origin already has a relay enrollment. Restore it or revoke it before connecting again.")
			return
		case http.StatusUnprocessableEntity:
			writeError(w, http.StatusUnprocessableEntity, "The relay could not verify this analyzer. Check that its public HTTPS URL exposes the enrollment proof route.")
			return
		case http.StatusNotFound:
			writeError(w, http.StatusServiceUnavailable, "The push relay is not accepting self-service enrollments.")
			return
		case http.StatusTooManyRequests:
			writeError(w, http.StatusTooManyRequests, "The push relay is busy. Try Enable again in a moment.")
			return
		}
	}
	writeError(w, http.StatusBadGateway, "Could not reach the push relay. Try Enable again.")
}
