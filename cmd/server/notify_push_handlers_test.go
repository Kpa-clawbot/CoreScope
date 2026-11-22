package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNotifyPushDeviceRoutes(t *testing.T) {
	const (
		installation = "install_Ab13"
		analyzer     = "analyzer_test"
		sender       = "sender_fixture"
	)
	validProof := strings.Repeat("a", 43)
	expiredProof := strings.Repeat("b", 43)
	renewProof := strings.Repeat("c", 43)
	var renewCalls atomic.Int32
	expiresAt := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	requests := make(chan notifyRelayLinkRequest, 4)
	relay := newTestNotifyRelayClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/subscriptions" || r.Header.Get("Authorization") != "Bearer "+sender {
			t.Errorf("unexpected relay request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var request notifyRelayLinkRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		select {
		case requests <- request:
		default:
			t.Error("unexpected relay retries")
		}
		if request.Proof == expiredProof {
			w.WriteHeader(http.StatusGone)
			_, _ = w.Write([]byte(request.Proof))
			return
		}
		expiry := expiresAt
		if request.Proof == renewProof {
			switch renewCalls.Add(1) {
			case 1:
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			case 2:
				expiry = expiresAt.Add(-30 * 24 * time.Hour)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		if err := json.NewEncoder(w).Encode(notifyRelaySubscription{
			SubscriptionID: "sub_fixture", Status: "active", ExpiresAt: expiry,
		}); err != nil {
			t.Error(err)
		}
	})
	ns := defaultNotifySettings()
	ns.relay = relay
	ns.relayOrigin = relay.origin
	ns.relayAnalyzerID = analyzer
	ns.relayCredential = sender
	ns.relayPlatforms = []string{"ios", "android"}
	f := newNotifyFixture(t, ns)
	pat := f.registerAndActivate(t, "pat@example.org", "Pat", pw)
	quinn := f.registerAndActivate(t, "quinn@example.org", "Quinn", pw)
	path := "/api/account/notifications/devices/" + installation
	body := map[string]string{"proof": validProof}

	expectStatus(t, f.do("PUT", path, body), http.StatusUnauthorized)
	expectStatus(t, f.do("PUT", path, body, as(&client{cookie: pat.cookie})), http.StatusForbidden)
	expectStatus(t, f.do("PUT", path, map[string]string{"proof": "short"}, as(pat)), http.StatusBadRequest)
	if len(requests) != 0 {
		t.Fatal("unauthorized or invalid requests reached the relay")
	}

	w := f.do("GET", "/api/account/notifications", nil, as(pat))
	expectStatus(t, w, http.StatusOK)
	capability := decode[notifyAccountJSON](t, w).PushRelay
	if capability == nil || capability.AnalyzerID != analyzer || capability.RelayOrigin != relay.origin ||
		capability.ContractVersion != 1 || len(capability.Platforms) != 2 || strings.Contains(w.Body.String(), sender) {
		t.Fatalf("invalid relay capability: %+v", capability)
	}

	w = f.do("PUT", path, body, as(pat))
	expectStatus(t, w, http.StatusOK)
	linked := decode[notifyPushDeviceJSON](t, w)
	if linked.SubscriptionID != "sub_fixture" || linked.Status != "active" || !linked.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("link result = %+v", linked)
	}
	first := nextNotifyPushRequest(t, requests)
	if first.BindingID == "" || first.InstallationID != installation || first.Proof != validProof {
		t.Fatalf("incorrect initial relay request: %+v", first)
	}

	w = f.do("PUT", path, map[string]string{"proof": expiredProof}, as(quinn))
	expectStatus(t, w, http.StatusGone)
	if strings.Contains(w.Body.String(), expiredProof) {
		t.Fatal("relay error response leaked the proof")
	}
	if other := nextNotifyPushRequest(t, requests); other.BindingID == first.BindingID {
		t.Fatal("different accounts shared an analyzer-local binding ID")
	}
	expectStatus(t, f.do("DELETE", path, nil, as(quinn)), http.StatusNoContent)
	expectStatus(t, f.do("PUT", path, body, as(pat)), http.StatusOK)
	if replay := nextNotifyPushRequest(t, requests); replay.BindingID != first.BindingID {
		t.Fatal("another account's rejected link or delete removed the active binding")
	}

	w = f.do("PUT", path, map[string]string{"proof": renewProof}, as(pat))
	expectStatus(t, w, http.StatusTooManyRequests)
	if w.Header().Get("Retry-After") != "7" {
		t.Fatal("relay Retry-After was not forwarded")
	}
	if renew := nextNotifyPushRequest(t, requests); renew.BindingID != first.BindingID {
		t.Fatal("renewal used a different analyzer-local binding ID")
	}
	expectStatus(t, f.do("PUT", path, map[string]string{"proof": renewProof}, as(pat)), http.StatusServiceUnavailable)
	if invalid := nextNotifyPushRequest(t, requests); invalid.BindingID != first.BindingID {
		t.Fatal("retry after throttling used a different binding ID")
	}
	expectStatus(t, f.do("PUT", path, map[string]string{"proof": renewProof}, as(pat)), http.StatusOK)
	if renew := nextNotifyPushRequest(t, requests); renew.BindingID != first.BindingID {
		t.Fatal("retry after an expired relay reply used a different binding ID")
	}

	expectStatus(t, f.do("DELETE", path, nil, as(&client{cookie: pat.cookie})), http.StatusForbidden)
	expectStatus(t, f.do("DELETE", path, nil, as(pat)), http.StatusNoContent)
	expectStatus(t, f.do("DELETE", path, nil, as(pat)), http.StatusNoContent)
	if len(requests) != 0 {
		t.Fatal("local opt-out made a synchronous relay request")
	}
}

func nextNotifyPushRequest(t *testing.T, requests <-chan notifyRelayLinkRequest) notifyRelayLinkRequest {
	t.Helper()
	select {
	case request := <-requests:
		return request
	default:
		t.Fatal("expected a relay request")
		return notifyRelayLinkRequest{}
	}
}

func TestNotifyPushCapabilityOmittedWhenUnconfigured(t *testing.T) {
	f := newNotifyFixture(t, defaultNotifySettings())
	pat := f.registerAndActivate(t, "pat@example.org", "Pat", pw)
	w := f.do("GET", "/api/account/notifications", nil, as(pat))
	expectStatus(t, w, http.StatusOK)
	if strings.Contains(w.Body.String(), "pushRelay") {
		t.Fatal("unconfigured analyzer advertised push")
	}
	path := "/api/account/notifications/devices/install_Ab13"
	expectStatus(t, f.do("PUT", path, nil, as(pat)), http.StatusNotFound)
	expectStatus(t, f.do("DELETE", path, nil, as(pat)), http.StatusNotFound)
}
