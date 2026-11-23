package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/meshcore-analyzer/users"
)

// relayPreset is a push relay an admin can pick by name. Any other HTTPS
// relay that implements the push relay contract can be entered as a custom
// origin; it reaches only the users of the app whose push credentials it
// holds.
type relayPreset struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Origin    string   `json:"origin"`
	Platforms []string `json:"platforms"`
	AppURL    string   `json:"appUrl"`
}

var relayPresets = []relayPreset{
	{ID: "nodescope", Name: "NodeScope", Origin: "https://push.nodescope.app", Platforms: []string{"ios", "android"}, AppURL: "https://nodescope.app"},
}

// defaultRelayPlatforms applies to a custom relay unless the server
// configuration names its platforms.
var defaultRelayPlatforms = []string{"ios", "android"}

func relayPresetFor(origin string) (relayPreset, bool) {
	for _, preset := range relayPresets {
		if preset.Origin == origin {
			return preset, true
		}
	}
	return relayPreset{}, false
}

// canonicalRelayOrigin returns the origin in the one form the push relay
// contract defines (spec, Enrollment step 3), so presets and saved
// enrollments compare equal and the enrollment proof signs what the relay
// rebuilds: "https://", a DNS hostname in lowercase ASCII without a trailing
// dot (an internationalized name in its xn-- form; IP literals are not
// accepted), and ":<port>" in plain decimal only when it isn't 443.
func canonicalRelayOrigin(raw string) (string, error) {
	client, err := newNotifyRelayClient(strings.TrimSuffix(strings.TrimSpace(raw), "/"), nil)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(client.origin)
	if err != nil {
		return "", err
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return "", errors.New("notification relay requires an HTTPS origin")
	}
	if strings.HasSuffix(host, ".") {
		return "", errors.New("notification relay host has more than one trailing dot")
	}
	for i := 0; i < len(host); i++ {
		if host[i] >= utf8.RuneSelf {
			return "", errors.New("notification relay host must be ASCII: write an internationalized domain name in its xn-- form")
		}
	}
	if net.ParseIP(host) != nil || strings.Contains(host, ":") {
		return "", errors.New("notification relay origin must use a DNS hostname, not an IP address")
	}
	port := u.Port()
	if port == "" {
		return "https://" + host, nil
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return "", errors.New("notification relay origin has an invalid port")
	}
	if n == 443 {
		return "https://" + host, nil
	}
	return "https://" + host + ":" + strconv.Itoa(n), nil
}

// relayProofContext separates enrollment proofs from any other use of the
// key. Spec: docs/specs/2026-11-22-push-relay-contract.md, Enrollment step 3.
const relayProofContext = "corescope-push-relay-proof-v1"

// relayProofMessage is what the analyzer signs for a relay's challenge. It
// names the relay the enrollment was sent to, so a relay cannot pass a proof
// it obtained on to another relay.
func relayProofMessage(relayOrigin string, challenge []byte) []byte {
	return append([]byte(relayProofContext+"\n"+relayOrigin+"\n"), challenge...)
}

// relayProofSigner exists only while an enrollment request is in flight.
type relayProofSigner struct {
	key         ed25519.PrivateKey
	relayOrigin string
}

// relayPendingPath names the pending enrollment file after the relay, so a
// credential prepared for one relay is never offered to another.
func relayPendingPath(dbPath, relayOrigin string) string {
	sum := sha256.Sum256([]byte(relayOrigin))
	return dbPath + ".push-relay-pending-" + hex.EncodeToString(sum[:8]) + ".json"
}

type relayEnrollmentFlow struct {
	mu        sync.Mutex
	proofKey  atomic.Pointer[relayProofSigner]
	transport http.RoundTripper // Override in tests.
}

type relayEnrollmentStatus struct {
	State             string        `json:"state"`
	PublicOrigin      string        `json:"publicOrigin"`
	RelayOrigin       string        `json:"relayOrigin,omitempty"`
	RelayName         string        `json:"relayName,omitempty"`
	OriginLocked      bool          `json:"originLocked,omitempty"`
	Presets           []relayPreset `json:"presets,omitempty"`
	AnalyzerID        string        `json:"analyzerId,omitempty"`
	Paused            bool          `json:"paused,omitempty"`
	DisconnectPending bool          `json:"disconnectPending,omitempty"`
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
		State: "available", RelayOrigin: a.set.selfEnrollOrigin, OriginLocked: a.set.selfEnrollOrigin != "",
		PublicOrigin: relayPublicOrigin(a.set.baseURL),
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
		status.OriginLocked = false
		status.Paused = a.relayDeliveryPaused()
		status.DisconnectPending = a.relayDisconnectPending()
	case status.PublicOrigin == "":
		status.State = "unavailable"
	default:
		if !status.OriginLocked {
			status.Presets = relayPresets
		}
	}
	if preset, ok := relayPresetFor(status.RelayOrigin); ok {
		status.RelayName = preset.Name
	}
	return status
}

// selfEnrollRelay picks the relay for an Enable request: the origin locked
// in the server configuration, or the preset or custom origin in the body.
// A non-empty problem is the message for the admin.
func (a *authService) selfEnrollRelay(requested string) (origin string, platforms []string, problem string) {
	if strings.TrimSpace(requested) != "" {
		var err error
		if origin, err = canonicalRelayOrigin(requested); err != nil {
			return "", nil, "Enter the relay as an HTTPS origin, such as https://push.example.org."
		}
	}
	if locked := a.set.selfEnrollOrigin; locked != "" {
		if origin != "" && origin != locked {
			return "", nil, "This server only enrolls with " + locked + "."
		}
		return locked, a.set.selfEnrollPlatforms, ""
	}
	if origin == "" {
		return "", nil, "Choose a relay before enabling push."
	}
	if preset, ok := relayPresetFor(origin); ok {
		return origin, preset.Platforms, ""
	}
	return origin, defaultRelayPlatforms, ""
}

func (s *Server) handlePushRelayEnrollmentStatus(w http.ResponseWriter, _ *http.Request, _ *users.User, _ *users.Session) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, s.auth.relayEnrollmentStatus())
}

// The signing key exists only while an authenticated admin request is enrolling.
// Passing a relay challenge to this endpoint proves control of the public origin
// to the relay that enrollment was sent to, and to no other.
func (s *Server) handlePushRelayEnrollmentProof(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	signer := s.auth.relayEnrollment.proofKey.Load()
	if signer == nil {
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
	}{base64.RawURLEncoding.EncodeToString(ed25519.Sign(signer.key, relayProofMessage(signer.relayOrigin, challenge)))})
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
	var body struct {
		RelayOrigin string `json:"relayOrigin"`
	}
	if !decodeJSONMax(w, r, &body, 2048, http.StatusRequestEntityTooLarge) {
		return
	}
	status := a.relayEnrollmentStatus()
	switch status.State {
	case "connected", "manual":
		writeJSON(w, status)
		return
	case "notifications_disabled":
		writeError(w, http.StatusConflict, "Enable CoreScope notifications before connecting a push relay.")
		return
	case "unavailable":
		writeError(w, http.StatusConflict, "Set publicBaseUrl to a public HTTPS origin on port 443 before enabling push.")
		return
	}
	relayOrigin, platforms, problem := a.selfEnrollRelay(body.RelayOrigin)
	if problem != "" {
		writeError(w, http.StatusBadRequest, problem)
		return
	}
	path := relayPendingPath(a.set.dbPath, relayOrigin)
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
	relay, err := newNotifyRelayClient(relayOrigin, flow.transport)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not configure the push relay client.")
		return
	}
	flow.proofKey.Store(&relayProofSigner{key: privateKey, relayOrigin: relayOrigin})
	defer flow.proofKey.Store(nil)
	req := notifyRelaySelfEnrollmentRequest{
		Origin: status.PublicOrigin, PublicKey: base64.RawURLEncoding.EncodeToString(publicKey), SenderCredential: credential,
	}
	enrollment, err := relay.SelfEnroll(r.Context(), req)
	// The relay has answered: the proof route closes before anything else.
	flow.proofKey.Store(nil)
	if err != nil {
		writeRelayEnrollmentError(w, err)
		return
	}
	verified, err := relay.Analyzer(r.Context(), credential)
	if err != nil || verified.AnalyzerID != enrollment.AnalyzerID || verified.Origin != status.PublicOrigin {
		writeError(w, http.StatusBadGateway, "Could not verify the relay enrollment. Try Enable again.")
		return
	}
	if err := a.activateRelayEnrollment(relayOrigin, platforms, enrollment.AnalyzerID, credential); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not save the enrollment. Check the users database directory and try Enable again.")
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		// The file still contains the active credential, with owner-only access.
		// Enrollment is already durable and usable.
		log.Printf("[notify] could not remove the pending push relay enrollment file: %v", err)
	}
	a.audit(&actor.ID, "notify.push.enroll", nil, map[string]string{"relayOrigin": relayOrigin})
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
			writeError(w, http.StatusUnprocessableEntity, "The relay could not verify this analyzer. Check that its public HTTPS URL exposes the enrollment proof route, and that the relay origin entered here is the relay's own public origin, not an alias (the relay checks the proof against the origin it is configured with).")
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
