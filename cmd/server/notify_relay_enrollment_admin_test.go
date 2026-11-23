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
	t.Run("NodeScope preset", func(t *testing.T) {
		testRelayAdminEnrollment(t, nil, "https://push.nodescope.app", "https://push.nodescope.app", "ios,android")
	})
	t.Run("custom relay", func(t *testing.T) {
		testRelayAdminEnrollment(t, nil, " https://Relay.Example.org/ ", "https://relay.example.org", "ios,android")
	})
	t.Run("preset entered with its default port", func(t *testing.T) {
		testRelayAdminEnrollment(t, nil, "https://PUSH.nodescope.app:443", "https://push.nodescope.app", "ios,android")
	})
	t.Run("custom relay on another port", func(t *testing.T) {
		testRelayAdminEnrollment(t, nil, "https://relay.example.org:8443", "https://relay.example.org:8443", "ios,android")
	})
	t.Run("origin locked in the server configuration", func(t *testing.T) {
		lock := func(a *authService) {
			a.set.selfEnrollOrigin, a.set.selfEnrollPlatforms = "https://push-staging.nodescope.app", []string{"android"}
		}
		testRelayAdminEnrollment(t, lock, "", "https://push-staging.nodescope.app", "android")
	})
}

func testRelayAdminEnrollment(t *testing.T, setup func(*authService), requested, relayOrigin, wantPlatforms string) {
	t.Helper()
	const adminEmail = "admin-enrollment@example.org"
	const path = "/api/admin/notifications/push-relay"
	const proofPath = "/api/push-relay/enrollment-proof"
	f := newAuthFixture(t, adminEmail)
	a := f.srv.auth
	a.set.notify.enabled = true
	if setup != nil {
		setup(a)
	}
	locked := a.set.selfEnrollOrigin != ""
	enable := struct {
		RelayOrigin string `json:"relayOrigin"`
	}{requested}
	admin := f.registerAndActivate(t, adminEmail, "Admin", pw)
	member := f.registerAndActivate(t, "member-enrollment@example.org", "Member", pw)
	if w := f.do(http.MethodGet, path, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", w.Code)
	}
	w := f.do(http.MethodGet, path, nil, as(admin))
	var status relayEnrollmentStatus
	wantStatusOrigin := ""
	if locked {
		wantStatusOrigin = relayOrigin
	}
	if err := json.Unmarshal(w.Body.Bytes(), &status); w.Code != http.StatusOK || err != nil || status.State != "available" ||
		status.RelayOrigin != wantStatusOrigin || status.OriginLocked != locked || (len(status.Presets) == 0) == !locked {
		t.Fatalf("available status: HTTP %d, %+v, %v", w.Code, status, err)
	}
	if w := f.do(http.MethodPost, path, enable, as(member)); w.Code != http.StatusForbidden {
		t.Fatalf("member enroll = %d", w.Code)
	}
	if w := f.do(http.MethodPost, path, enable, as(admin), header(csrfHeader, "invalid")); w.Code != http.StatusForbidden {
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
			if len(key) != ed25519.PublicKeySize || !ed25519.Verify(ed25519.PublicKey(key), relayProofMessage(relayOrigin, challenge), sig) {
				t.Error("relay challenge signature does not match the enrollment key and relay")
			}
			// A proof made for this relay must not verify anywhere else, nor as
			// a signature over the bare challenge.
			if ed25519.Verify(ed25519.PublicKey(key), challenge, sig) ||
				ed25519.Verify(ed25519.PublicKey(key), relayProofMessage("https://other-relay.example.org", challenge), sig) {
				t.Error("relay proof is not bound to the relay it was made for")
			}
			writeJSONStatus(w, http.StatusCreated, notifyRelayEnrollment{AnalyzerID: "an_test", SenderCredential: req.SenderCredential})
		case "/v1/analyzer":
			if r.Header.Get("Authorization") != "Bearer "+savedCredential {
				t.Error("verification did not use the sender credential")
			}
			// The relay has answered the enrollment: the proof route is closed.
			if p := f.serve(http.MethodGet, proofPath+"?challenge="+strings.Repeat("A", 43), nil); p.Code != http.StatusNotFound {
				t.Errorf("proof route still answers after the relay's response: %d", p.Code)
			}
			writeJSON(w, notifyRelayAnalyzer{AnalyzerID: "an_test", Origin: testBase, ContractVersion: 1})
		default:
			t.Errorf("unexpected relay request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
		return w.Result(), nil
	})
	if w := f.do(http.MethodPost, path, enable, as(admin)); w.Code != http.StatusBadGateway {
		t.Fatalf("failed relay request returned %d", w.Code)
	}
	pending := relayPendingPath(a.set.dbPath, relayOrigin)
	if info, err := os.Stat(pending); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("pending enrollment file missing or not private: %v", err)
	}
	w = f.do(http.MethodPost, path, enable, as(admin))
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &status) != nil ||
		status.State != "connected" || status.AnalyzerID != "an_test" || status.RelayOrigin != relayOrigin || strings.Contains(w.Body.String(), savedCredential) {
		t.Fatalf("unexpected enrollment status: HTTP %d, state %q", w.Code, status.State)
	}
	if got := strings.Join(a.relaySettings().relayPlatforms, ","); got != wantPlatforms {
		t.Fatalf("enrolled platforms = %s, want %s", got, wantPlatforms)
	}
	if _, isPreset := relayPresetFor(relayOrigin); (status.RelayName == "NodeScope") != isPreset {
		t.Fatalf("relay name %q for %s", status.RelayName, relayOrigin)
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Fatalf("pending enrollment file was not removed: %v", err)
	}
	if w := f.do(http.MethodGet, proofPath, nil); w.Code != http.StatusNotFound {
		t.Fatalf("proof survived enrollment: %d", w.Code)
	}
	if w := f.do(http.MethodPost, path, enable, as(admin)); w.Code != http.StatusOK || attempts != 2 {
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
	if err := a.activateRelayEnrollment("https://push.nodescope.app", nil, "an_lifecycle", credential); err != nil {
		t.Fatal(err)
	}
	const otherCredential = "fixture-staging-pending"
	otherPending := relayPendingPath(a.set.dbPath, "https://push-staging.nodescope.app")
	if err := os.WriteFile(otherPending, []byte(otherCredential), 0o600); err != nil {
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
	if contents, err := os.ReadFile(otherPending); err != nil || string(contents) != otherCredential {
		t.Fatalf("disconnect modified another relay's pending file: %v", err)
	}
	if revokeCalls != 2 {
		t.Fatalf("relay received %d revoke calls, want 2", revokeCalls)
	}
}

func TestPushRelayPendingCredentialsAreScopedToTheRelay(t *testing.T) {
	dbPath := t.TempDir() + "/users.db"
	first := relayPendingPath(dbPath, "https://push.nodescope.app")
	second := relayPendingPath(dbPath, "https://relay.example.org")
	if first == second || !strings.HasPrefix(first, dbPath+".push-relay-pending-") || !strings.HasSuffix(first, ".json") {
		t.Fatalf("pending paths do not isolate relays: %s, %s", first, second)
	}
	if first != relayPendingPath(dbPath, "https://push.nodescope.app") {
		t.Fatal("pending path is not stable for one relay")
	}
	credential, err := relayPendingCredential(first, testBase)
	if err != nil || credential == "" {
		t.Fatalf("first relay credential: %v", err)
	}
	other, err := relayPendingCredential(second, testBase)
	if err != nil || other == "" || other == credential {
		t.Fatalf("second relay reused the first relay's credential: %v", err)
	}
	if got, err := relayPendingCredential(first, testBase); err != nil || got != credential {
		t.Fatalf("retry did not reuse the relay's own credential: %v", err)
	}
}

func TestPushRelaySelfEnrollChoice(t *testing.T) {
	f := newAuthFixture(t)
	a := f.srv.auth
	if got := a.relayEnrollmentStatus(); got.State != "notifications_disabled" {
		t.Fatalf("disabled notifications: %+v", got)
	}
	a.set.notify.enabled = true
	got := a.relayEnrollmentStatus()
	if got.State != "available" || got.RelayOrigin != "" || got.OriginLocked || len(got.Presets) != 1 || got.Presets[0].Name != "NodeScope" {
		t.Fatalf("available without a lock: %+v", got)
	}
	for _, tc := range []struct{ requested, origin, platforms string }{
		{"https://push.nodescope.app", "https://push.nodescope.app", "ios,android"},
		{"https://Push.NodeScope.app/", "https://push.nodescope.app", "ios,android"},
		{"https://relay.example.org:8443", "https://relay.example.org:8443", "ios,android"},
		{"", "", ""},
		{"http://relay.example.org", "", ""},
		{"https://relay.example.org/v1", "", ""},
		{"relay.example.org", "", ""},
	} {
		origin, platforms, problem := a.selfEnrollRelay(tc.requested)
		if origin != tc.origin || strings.Join(platforms, ",") != tc.platforms || (problem == "") != (tc.origin != "") {
			t.Errorf("selfEnrollRelay(%q) = %q, %v, %q", tc.requested, origin, platforms, problem)
		}
	}

	a.set.selfEnrollOrigin, a.set.selfEnrollPlatforms = "https://relay.example.org", []string{"android"}
	if got := a.relayEnrollmentStatus(); got.State != "available" || got.RelayOrigin != "https://relay.example.org" || !got.OriginLocked || got.Presets != nil {
		t.Fatalf("locked origin status: %+v", got)
	}
	if origin, platforms, problem := a.selfEnrollRelay(""); origin != "https://relay.example.org" || strings.Join(platforms, ",") != "android" || problem != "" {
		t.Fatalf("locked origin with no choice: %q %v %q", origin, platforms, problem)
	}
	if _, _, problem := a.selfEnrollRelay("https://push.nodescope.app"); problem == "" {
		t.Fatal("locked origin accepted another relay")
	}

	if err := a.activateRelayEnrollment("https://push.nodescope.app", nil, "an_existing", strings.Repeat("a", 43)); err != nil {
		t.Fatal(err)
	}
	if got := a.relayEnrollmentStatus(); got.State != "connected" || got.RelayOrigin != "https://push.nodescope.app" ||
		got.RelayName != "NodeScope" || got.OriginLocked || got.Presets != nil {
		t.Fatalf("existing enrollment: %+v", got)
	}
	if got := a.relaySettings().relayPlatforms; strings.Join(got, ",") != "ios,android" {
		t.Fatalf("enrollment without platforms = %v, want the iOS and Android default", got)
	}
}

func TestResolveSelfEnrollRelay(t *testing.T) {
	for _, tc := range []struct {
		cfg       NotifyRelayConfig
		origin    string
		platforms string
	}{
		{NotifyRelayConfig{Origin: "https://push.nodescope.app/"}, "https://push.nodescope.app", "ios,android"},
		{NotifyRelayConfig{Origin: "https://relay.example.org"}, "https://relay.example.org", "ios,android"},
		{NotifyRelayConfig{Origin: "https://relay.example.org", Platforms: []string{"android"}}, "https://relay.example.org", "android"},
		{NotifyRelayConfig{Origin: "https://relay.example.org:"}, "https://relay.example.org", "ios,android"},
		{NotifyRelayConfig{Origin: "https://relay.example.org:0443"}, "https://relay.example.org", "ios,android"},
		{NotifyRelayConfig{Origin: "https://relay.example.org:08443"}, "https://relay.example.org:8443", "ios,android"},
		{NotifyRelayConfig{Origin: "https://Relay.Example.org./"}, "https://relay.example.org", "ios,android"},
		{NotifyRelayConfig{Origin: "https://xn--rlay-bpa.example.org"}, "https://xn--rlay-bpa.example.org", "ios,android"},
		{NotifyRelayConfig{Origin: "https://rélay.example.org"}, "", ""},
		{NotifyRelayConfig{Origin: "https://relay.example.org:0"}, "", ""},
		{NotifyRelayConfig{Origin: "https://relay.example.org.."}, "", ""},
		{NotifyRelayConfig{Origin: "https://1.2.3.4:8443"}, "", ""},
		{NotifyRelayConfig{Origin: "https://[2001:DB8::1]:0443/"}, "", ""},
		{NotifyRelayConfig{Origin: "https://[::1]"}, "", ""},
		{NotifyRelayConfig{Origin: "https://[fe80::1%25en0]"}, "", ""},
		{NotifyRelayConfig{Origin: "https://relay.example.org:65536"}, "", ""},
		{NotifyRelayConfig{Origin: "http://relay.example.org"}, "", ""},
		{NotifyRelayConfig{Origin: "https://relay.example.org", Platforms: []string{"web"}}, "", ""},
	} {
		origin, platforms, err := resolveSelfEnrollRelay(&tc.cfg)
		if origin != tc.origin || strings.Join(platforms, ",") != tc.platforms || (err == nil) != (tc.origin != "") {
			t.Errorf("resolveSelfEnrollRelay(%+v) = %q, %v, %v", tc.cfg, origin, platforms, err)
		}
	}
}

// The proof format is part of the push relay contract; relays rebuild these
// bytes to verify, so a change here is a contract version bump.
func TestRelayProofMessageFormat(t *testing.T) {
	challenge := []byte(strings.Repeat("c", 32))
	want := "corescope-push-relay-proof-v1\nhttps://push.nodescope.app\n" + strings.Repeat("c", 32)
	if got := string(relayProofMessage("https://push.nodescope.app", challenge)); got != want {
		t.Fatalf("proof message = %q, want %q", got, want)
	}
}
