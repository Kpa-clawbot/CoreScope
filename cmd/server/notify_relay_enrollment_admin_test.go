package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

type enrollmentRelayTransport func(*http.Request) (*http.Response, error)

func (f enrollmentRelayTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestPushRelayAdminEnrollment(t *testing.T) {
	t.Run("production default", func(t *testing.T) {
		testRelayAdminEnrollment(t, "", "https://push.nodescope.app")
	})
	t.Run("staging override", func(t *testing.T) {
		testRelayAdminEnrollment(t, "https://push-staging.nodescope.app/", "https://push-staging.nodescope.app")
	})
}

func testRelayAdminEnrollment(t *testing.T, override, relayOrigin string) {
	t.Helper()
	const adminEmail = "admin-enrollment@example.org"
	const path = "/api/admin/notifications/push-relay"
	const proofPath = "/api/push-relay/enrollment-proof"
	f := newAuthFixture(t, adminEmail)
	a := f.srv.auth
	a.set.notify.enabled = true
	a.set.nodeScopeSelfEnrollOrigin = override
	admin := f.registerAndActivate(t, adminEmail, "Admin", pw)
	member := f.registerAndActivate(t, "member-enrollment@example.org", "Member", pw)
	if w := f.do(http.MethodGet, path, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", w.Code)
	}
	w := f.do(http.MethodGet, path, nil, as(admin))
	var status relayEnrollmentStatus
	if err := json.Unmarshal(w.Body.Bytes(), &status); w.Code != http.StatusOK || err != nil || status.State != "available" || status.RelayOrigin != relayOrigin {
		t.Fatalf("available status: HTTP %d, %+v, %v", w.Code, status, err)
	}
	if w := f.do(http.MethodPost, path, nil, as(member)); w.Code != http.StatusForbidden {
		t.Fatalf("member enroll = %d", w.Code)
	}
	if w := f.do(http.MethodPost, path, nil, as(admin), header(csrfHeader, "invalid")); w.Code != http.StatusForbidden {
		t.Fatalf("enroll without valid CSRF = %d", w.Code)
	}
	if w := f.do(http.MethodGet, proofPath, nil); w.Code != http.StatusNotFound {
		t.Fatalf("idle public proof = %d", w.Code)
	}

	var savedCredential string
	attempts := 0
	a.relayEnrollment.transport = enrollmentRelayTransport(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		if r.URL.Scheme+"://"+r.URL.Host != relayOrigin {
			t.Errorf("unexpected relay origin: %s", r.URL.Host)
		}
		switch r.URL.Path {
		case "/v1/analyzers/self-enroll":
			attempts++
			var req notifyRelaySelfEnrollmentRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Origin != testBase {
				t.Errorf("invalid enrollment body: %v", err)
			}
			if attempts == 1 {
				savedCredential = req.SenderCredential
				writeJSONStatus(w, http.StatusServiceUnavailable, struct{}{})
				break
			}
			if req.SenderCredential == "" || req.SenderCredential != savedCredential {
				t.Error("retry did not preserve the pending sender credential")
			}
			challenge := []byte(strings.Repeat("c", 32))
			proof := f.serve(http.MethodGet, proofPath+"?challenge="+base64.RawURLEncoding.EncodeToString(challenge), nil)
			var result struct{ Signature string }
			if proof.Code != http.StatusOK || json.Unmarshal(proof.Body.Bytes(), &result) != nil {
				t.Errorf("proof request returned %d", proof.Code)
			}
			sig, _ := base64.RawURLEncoding.DecodeString(result.Signature)
			key, _ := base64.RawURLEncoding.DecodeString(req.PublicKey)
			if len(key) != ed25519.PublicKeySize || !ed25519.Verify(ed25519.PublicKey(key), challenge, sig) {
				t.Error("relay challenge signature does not match the enrollment key")
			}
			writeJSONStatus(w, http.StatusCreated, notifyRelayEnrollment{AnalyzerID: "an_test", SenderCredential: req.SenderCredential})
		case "/v1/analyzer":
			if r.Header.Get("Authorization") != "Bearer "+savedCredential {
				t.Error("verification did not use the sender credential")
			}
			writeJSON(w, notifyRelayAnalyzer{AnalyzerID: "an_test", Origin: testBase, ContractVersion: 1})
		default:
			t.Errorf("unexpected relay request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
		return w.Result(), nil
	})
	if w := f.do(http.MethodPost, path, nil, as(admin)); w.Code != http.StatusBadGateway {
		t.Fatalf("failed relay request returned %d", w.Code)
	}
	pending := relayPendingPath(a.set.dbPath, relayOrigin)
	if info, err := os.Stat(pending); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("pending enrollment file missing or not private: %v", err)
	}
	w = f.do(http.MethodPost, path, nil, as(admin))
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &status) != nil ||
		status.State != "connected" || status.AnalyzerID != "an_test" || status.RelayOrigin != relayOrigin || strings.Contains(w.Body.String(), savedCredential) {
		t.Fatalf("unexpected enrollment status: HTTP %d, state %q", w.Code, status.State)
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Fatalf("pending enrollment file was not removed: %v", err)
	}
	if w := f.do(http.MethodGet, proofPath, nil); w.Code != http.StatusNotFound {
		t.Fatalf("proof survived enrollment: %d", w.Code)
	}
	if w := f.do(http.MethodPost, path, nil, as(admin)); w.Code != http.StatusOK || attempts != 2 {
		t.Fatalf("connected analyzer re-enrolled: status %d, attempts %d", w.Code, attempts)
	}
}

func TestPushRelayAdminLifecycle(t *testing.T) {
	const adminEmail = "admin-delivery@example.org"
	const path = "/api/admin/notifications/push-relay"
	f := newAuthFixture(t, adminEmail)
	a := f.srv.auth
	a.set.notify.enabled = true
	admin := f.registerAndActivate(t, adminEmail, "Admin", pw)
	member := f.registerAndActivate(t, "member-delivery@example.org", "Member", pw)
	credential := strings.Repeat("a", 43)
	if err := a.activateRelayEnrollment(nodeScopeSelfEnrollRelay, "an_lifecycle", credential); err != nil {
		t.Fatal(err)
	}
	const otherCredential = "fixture-staging-pending"
	legacyPending := relayPendingPath(a.set.dbPath, nodeScopeStagingRelay)
	if err := os.WriteFile(legacyPending, []byte(otherCredential), 0o600); err != nil {
		t.Fatal(err)
	}
	revokeCalls := 0
	a.relaySettings().relay.http.Transport = enrollmentRelayTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/analyzer" || r.Header.Get("Authorization") != "Bearer "+credential {
			t.Error("revocation request did not target the enrolled analyzer")
		}
		w := httptest.NewRecorder()
		revokeCalls++
		if revokeCalls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
		return w.Result(), nil
	})

	setPaused := func(paused bool) *httptest.ResponseRecorder {
		return f.do(http.MethodPut, path, struct {
			Paused bool `json:"paused"`
		}{paused}, as(admin))
	}
	checkStatus := func(w *httptest.ResponseRecorder, wantState string, wantPaused, wantPending bool) {
		t.Helper()
		var got relayEnrollmentStatus
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil ||
			got.State != wantState || got.Paused != wantPaused || got.DisconnectPending != wantPending {
			t.Fatalf("status HTTP %d (%+v), want %s paused=%v pending=%v", w.Code, got, wantState, wantPaused, wantPending)
		}
	}
	if w := f.do(http.MethodPut, path, struct{}{}, as(admin)); w.Code != http.StatusBadRequest {
		t.Fatalf("missing paused returned %d", w.Code)
	}
	if w := f.do(http.MethodDelete, path, nil, as(member)); w.Code != http.StatusForbidden {
		t.Fatalf("non-admin disconnect returned %d", w.Code)
	}
	if w := f.do(http.MethodDelete, path, nil, as(admin), header(csrfHeader, "invalid")); w.Code != http.StatusForbidden {
		t.Fatalf("disconnect without valid CSRF returned %d", w.Code)
	}
	checkStatus(setPaused(true), "connected", true, false)
	checkStatus(setPaused(false), "connected", false, false)
	if revokeCalls != 0 {
		t.Fatal("pause or resume contacted the relay")
	}
	if w := f.do(http.MethodDelete, path, nil, as(admin)); w.Code != http.StatusBadGateway {
		t.Fatalf("temporary relay failure returned %d", w.Code)
	}
	checkStatus(f.do(http.MethodGet, path, nil, as(admin)), "connected", true, true)
	if w := setPaused(false); w.Code != http.StatusConflict {
		t.Fatalf("resume while disconnect pending returned %d", w.Code)
	}
	checkStatus(f.do(http.MethodDelete, path, nil, as(admin)), "available", false, false)
	if _, err := os.Stat(a.relayEnrollmentPath()); !os.IsNotExist(err) {
		t.Fatalf("disconnected analyzer still has a saved credential: %v", err)
	}
	checkStatus(f.do(http.MethodDelete, path, nil, as(admin)), "available", false, false)
	if contents, err := os.ReadFile(legacyPending); err != nil || string(contents) != otherCredential {
		t.Fatalf("disconnect modified another relay's pending file: %v", err)
	}
	if revokeCalls != 2 {
		t.Fatalf("relay received %d revoke calls, want 2", revokeCalls)
	}
}

func TestPushRelayPendingCredentialsAreScopedToTheRelay(t *testing.T) {
	dbPath := t.TempDir() + "/users.db"
	staging := relayPendingPath(dbPath, nodeScopeStagingRelay)
	production := relayPendingPath(dbPath, nodeScopeSelfEnrollRelay)
	if staging != dbPath+".nodescope-pending.json" || staging == production {
		t.Fatal("pending paths do not preserve and isolate the legacy staging file")
	}
	const legacy = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if err := writePrivateRelayJSON(staging, relayPendingEnrollment{PublicOrigin: testBase, SenderCredential: legacy}); err != nil {
		t.Fatal(err)
	}
	credential, err := relayPendingCredential(production, testBase)
	if err != nil || credential == "" || credential == legacy {
		t.Fatalf("production attempted to reuse a legacy staging credential: %v", err)
	}
	if got, err := relayPendingCredential(production, testBase); err != nil || got != credential {
		t.Fatalf("production retry did not reuse its own credential: %v", err)
	}
	if got, err := relayPendingCredential(staging, testBase); err != nil || got != legacy {
		t.Fatalf("staging retry did not reuse the legacy credential: %v", err)
	}
}

func TestNodeScopeSelfEnrollOriginAllowsOnlyOfficialRelays(t *testing.T) {
	for raw, want := range map[string]string{
		"":                                       "https://push.nodescope.app",
		"https://push.nodescope.app/":            "https://push.nodescope.app",
		" https://push-staging.nodescope.app/ ":  "https://push-staging.nodescope.app",
		"https://push.nodescope.app.example.org": "",
		"http://push.nodescope.app":              "",
		"https://push.nodescope.app/v1":          "",
		"/":                                      "",
	} {
		if got := nodeScopeSelfEnrollOrigin(raw); got != want {
			t.Errorf("relay origin for %q = %q, want %q", raw, got, want)
		}
	}
	f := newAuthFixture(t)
	a := f.srv.auth
	a.set.nodeScopeSelfEnrollOrigin = "https://relay.example.org"
	if got := a.relayEnrollmentStatus(); got.State != "notifications_disabled" {
		t.Fatalf("disabled notifications with bad override: %+v", got)
	}
	a.set.notify.enabled = true
	if got := a.relayEnrollmentStatus(); got.State != "unavailable" || got.RelayOrigin != "" {
		t.Fatalf("new enrollment with bad override: %+v", got)
	}
	if err := a.activateRelayEnrollment(nodeScopeStagingRelay, "an_existing", strings.Repeat("a", 43)); err != nil {
		t.Fatal(err)
	}
	if got := a.relayEnrollmentStatus(); got.State != "connected" || got.RelayOrigin != nodeScopeStagingRelay {
		t.Fatalf("existing enrollment with bad override: %+v", got)
	}
}
