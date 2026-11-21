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

func nodeScopePendingPath(dbPath, relayOrigin string) string {
	if relayOrigin == nodeScopeStagingRelay {
		// This filename was already used by staging before production support.
		return dbPath + ".nodescope-pending.json"
	}
	return dbPath + ".nodescope-pending-production.json"
}

type nodeScopeEnrollmentFlow struct {
	mu        sync.Mutex
	proofKey  atomic.Pointer[ed25519.PrivateKey]
	transport http.RoundTripper // Override in tests.
}

type nodeScopeEnrollmentStatus struct {
	State             string `json:"state"`
	PublicOrigin      string `json:"publicOrigin"`
	RelayOrigin       string `json:"relayOrigin"`
	AnalyzerID        string `json:"analyzerId,omitempty"`
	Paused            bool   `json:"paused,omitempty"`
	DisconnectPending bool   `json:"disconnectPending,omitempty"`
}

func nodeScopePublicOrigin(base *url.URL) string {
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

func (a *authService) nodeScopeEnrollmentStatus() nodeScopeEnrollmentStatus {
	status := nodeScopeEnrollmentStatus{
		State: "available", RelayOrigin: nodeScopeSelfEnrollOrigin(a.set.nodeScopeSelfEnrollOrigin), PublicOrigin: nodeScopePublicOrigin(a.set.baseURL),
	}
	settings := a.nodeScopeRelaySettings()
	switch {
	case !a.set.notify.enabled:
		status.State = "notifications_disabled"
	case settings.relay != nil:
		status.State = "connected"
		if a.set.notify.relay != nil {
			status.State = "manual"
		}
		status.AnalyzerID, status.RelayOrigin = settings.relayAnalyzerID, settings.relayOrigin
		status.Paused = a.nodeScopeDeliveryPaused()
		status.DisconnectPending = a.nodeScopeDisconnectPending()
	case status.PublicOrigin == "" || status.RelayOrigin == "":
		status.State = "unavailable"
	}
	return status
}

func (s *Server) handleNodeScopeEnrollmentStatus(w http.ResponseWriter, _ *http.Request, _ *users.User, _ *users.Session) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, s.auth.nodeScopeEnrollmentStatus())
}

// The signing key exists only while an authenticated admin request is enrolling.
// Passing a relay challenge to this endpoint proves control of the public origin.
func (s *Server) handleNodeScopeEnrollmentProof(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	key := s.auth.nodeScopeEnrollment.proofKey.Load()
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

type nodeScopePendingEnrollment struct {
	PublicOrigin     string `json:"public_origin"`
	SenderCredential string `json:"sender_credential"`
}

func nodeScopePendingCredential(path, publicOrigin string) (string, error) {
	// Keep the same credential across retries and process restarts. The relay
	// will return the original enrollment after verifying origin control again.
	file, err := os.Open(path)
	if err == nil {
		defer file.Close()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			return "", errors.New("pending NodeScope enrollment file must be a private regular file")
		}
		data, err := io.ReadAll(io.LimitReader(file, 4097))
		var pending nodeScopePendingEnrollment
		if err != nil || len(data) > 4096 || json.Unmarshal(data, &pending) != nil {
			return "", errors.New("could not read pending NodeScope enrollment")
		}
		if pending.PublicOrigin != publicOrigin {
			return "", errors.New("pending NodeScope enrollment belongs to a different public origin")
		}
		secret, err := base64.RawURLEncoding.Strict().DecodeString(pending.SenderCredential)
		if err != nil || len(secret) != 32 {
			return "", errors.New("invalid pending NodeScope sender credential")
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
	if err := writePrivateNodeScopeJSON(path, nodeScopePendingEnrollment{PublicOrigin: publicOrigin, SenderCredential: credential}); err != nil {
		return "", err
	}
	return credential, nil
}

func (s *Server) handleNodeScopeEnable(w http.ResponseWriter, r *http.Request, actor *users.User, _ *users.Session) {
	w.Header().Set("Cache-Control", "no-store")
	a := s.auth
	flow := &a.nodeScopeEnrollment
	if !flow.mu.TryLock() {
		writeError(w, http.StatusConflict, "NodeScope enrollment is already running. Try again in a moment.")
		return
	}
	defer flow.mu.Unlock()
	status := a.nodeScopeEnrollmentStatus()
	switch status.State {
	case "connected", "manual":
		writeJSON(w, status)
		return
	case "notifications_disabled":
		writeError(w, http.StatusConflict, "Enable CoreScope notifications before connecting the NodeScope relay.")
		return
	case "unavailable":
		if status.RelayOrigin == "" {
			writeError(w, http.StatusConflict, "Set CORESCOPE_NODESCOPE_SELF_ENROLL_RELAY_ORIGIN to an official NodeScope HTTPS relay.")
			return
		}
		writeError(w, http.StatusConflict, "Set publicBaseUrl to a public HTTPS origin on port 443 before enabling NodeScope push.")
		return
	}
	path := nodeScopePendingPath(a.set.dbPath, status.RelayOrigin)
	credential, err := nodeScopePendingCredential(path, status.PublicOrigin)
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
		writeError(w, http.StatusInternalServerError, "Could not configure the NodeScope relay client.")
		return
	}
	flow.proofKey.Store(&privateKey)
	defer flow.proofKey.Store(nil)
	req := notifyRelaySelfEnrollmentRequest{
		Origin: status.PublicOrigin, PublicKey: base64.RawURLEncoding.EncodeToString(publicKey), SenderCredential: credential,
	}
	enrollment, err := relay.SelfEnroll(r.Context(), req)
	if err != nil {
		writeNodeScopeEnrollmentError(w, err)
		return
	}
	verified, err := relay.Analyzer(r.Context(), credential)
	if err != nil || verified.AnalyzerID != enrollment.AnalyzerID || verified.Origin != status.PublicOrigin {
		writeError(w, http.StatusBadGateway, "Could not verify the relay enrollment. Try Enable again.")
		return
	}
	if err := a.activateNodeScopeEnrollment(status.RelayOrigin, enrollment.AnalyzerID, credential); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not save the enrollment. Check the users database directory and try Enable again.")
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		// The file still contains the active credential, with owner-only access.
		// Enrollment is already durable and usable.
		log.Printf("[notify] could not remove the pending NodeScope enrollment file: %v", err)
	}
	a.audit(&actor.ID, "notify.nodescope.enroll", nil, nil)
	writeJSON(w, a.nodeScopeEnrollmentStatus())
}

func (s *Server) handleNodeScopePause(w http.ResponseWriter, r *http.Request, actor *users.User, _ *users.Session) {
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
	if !a.nodeScopeEnrollment.mu.TryLock() {
		writeError(w, http.StatusConflict, "Another NodeScope change is in progress. Try again in a moment.")
		return
	}
	defer a.nodeScopeEnrollment.mu.Unlock()
	wasPaused := a.nodeScopeDeliveryPaused()
	if err := a.setNodeScopeDeliveryPaused(*request.Paused); err != nil {
		switch {
		case errors.Is(err, errNodeScopeNoEnrollment):
			writeError(w, http.StatusConflict, "Connect a NodeScope relay before changing delivery.")
		case errors.Is(err, errNodeScopeDisconnectPending):
			writeError(w, http.StatusConflict, "Finish disconnecting the NodeScope relay before changing delivery.")
		default:
			log.Printf("[notify] save NodeScope delivery setting: %v", err)
			writeError(w, http.StatusInternalServerError, "Could not change NodeScope delivery. Try again.")
		}
		return
	}
	if wasPaused != *request.Paused {
		action := "notify.nodescope.resume"
		if *request.Paused {
			action = "notify.nodescope.pause"
		}
		a.audit(&actor.ID, action, nil, nil)
	}
	writeJSON(w, a.nodeScopeEnrollmentStatus())
}

func (s *Server) handleNodeScopeDisconnect(w http.ResponseWriter, r *http.Request, actor *users.User, _ *users.Session) {
	w.Header().Set("Cache-Control", "no-store")
	a := s.auth
	if !a.nodeScopeEnrollment.mu.TryLock() {
		writeError(w, http.StatusConflict, "Another NodeScope change is in progress. Try again in a moment.")
		return
	}
	defer a.nodeScopeEnrollment.mu.Unlock()

	if err := a.disconnectNodeScopeEnrollment(r.Context()); err != nil {
		switch {
		case errors.Is(err, errNodeScopeNoEnrollment):
			writeJSON(w, a.nodeScopeEnrollmentStatus())
		case errors.Is(err, errNodeScopeManualEnrollment):
			writeError(w, http.StatusConflict, "Disconnect this relay using the manual server configuration.")
		case errors.Is(err, errNodeScopeRelayRevoke):
			log.Printf("[notify] revoke NodeScope enrollment: %v", err)
			writeError(w, http.StatusBadGateway, "Could not confirm revocation at the relay. Try Disconnect relay again.")
		default:
			log.Printf("[notify] disconnect NodeScope enrollment: %v", err)
			writeError(w, http.StatusInternalServerError, "Could not finish disconnecting NodeScope push. Try Disconnect relay again.")
		}
		return
	}
	a.audit(&actor.ID, "notify.nodescope.disconnect", nil, nil)
	writeJSON(w, a.nodeScopeEnrollmentStatus())
}

func writeNodeScopeEnrollmentError(w http.ResponseWriter, err error) {
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
			writeError(w, http.StatusServiceUnavailable, "The NodeScope relay is not accepting self-service enrollments.")
			return
		case http.StatusTooManyRequests:
			writeError(w, http.StatusTooManyRequests, "The NodeScope relay is busy. Try Enable again in a moment.")
			return
		}
	}
	writeError(w, http.StatusBadGateway, "Could not reach the NodeScope relay. Try Enable again.")
}
