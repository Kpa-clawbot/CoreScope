package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestNotifyRelayClient(t *testing.T, handler http.HandlerFunc) *notifyRelayClient {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	client, err := newNotifyRelayClient(server.URL, server.Client().Transport)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestNotifyRelayClientRequests(t *testing.T) {
	expiresAt := time.Date(2026, 11, 18, 21, 0, 0, 0, time.UTC)
	requests := []struct {
		method   string
		path     string
		body     string
		response string
		status   int
	}{
		{"POST", "/v1/analyzers/enroll", `{"code":"invite","request_id":"request"}`, `{"analyzer_id":"analyzer","sender_credential":"credential"}`, http.StatusCreated},
		{"GET", "/v1/analyzer", "", `{"analyzer_id":"analyzer","origin":"https://analyzer.example","contract_version":1}`, http.StatusOK},
		{"POST", "/v1/subscriptions", `{"installation_id":"install","binding_id":"binding","proof":"proof"}`, `{"subscription_id":"subscription","status":"active","expires_at":"2026-11-18T21:00:00Z"}`, http.StatusCreated},
		{"POST", "/v1/deliveries", `{"event_id":"event","subscription_id":"subscription","kind":"node.heard_again","node_pubkey":"` + strings.Repeat("a", 64) + `","occurred_at":"2026-11-18T20:00:00Z","expires_at":"2026-11-18T21:00:00Z"}`, `{"delivery_id":"delivery","status":"queued"}`, http.StatusAccepted},
		{"DELETE", "/v1/subscriptions/subscription", "", "", http.StatusNoContent},
		{"DELETE", "/v1/analyzer", "", "", http.StatusNoContent},
	}
	var call atomic.Int32
	client := newTestNotifyRelayClient(t, func(w http.ResponseWriter, r *http.Request) {
		index := int(call.Add(1)) - 1
		if index >= len(requests) {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		want := requests[index]
		if r.Method != want.method || r.URL.EscapedPath() != want.path {
			t.Errorf("request = %s %s, want %s %s", r.Method, r.URL.EscapedPath(), want.method, want.path)
		}
		auth := "Bearer credential"
		if index == 0 {
			auth = ""
		}
		if got := r.Header.Get("Authorization"); got != auth {
			t.Errorf("request %s had unexpected authorization", want.path)
		}
		if want.body != "" {
			var got json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil || string(got) != want.body || r.Header.Get("Content-Type") != "application/json" {
				t.Errorf("request %s: unexpected JSON body or content type, decode error %v", want.path, err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(want.status)
		_, _ = w.Write([]byte(want.response))
	})

	ctx := context.Background()
	enrollment, err := client.Enroll(ctx, "invite", "request")
	if err != nil || enrollment.AnalyzerID != "analyzer" || enrollment.SenderCredential != "credential" {
		t.Fatalf("unexpected enrollment response: %v", err)
	}
	analyzer, err := client.Analyzer(ctx, enrollment.SenderCredential)
	if err != nil || analyzer.AnalyzerID != enrollment.AnalyzerID || analyzer.Origin != "https://analyzer.example" || analyzer.ContractVersion != 1 {
		t.Fatalf("analyzer = %+v, %v", analyzer, err)
	}
	subscription, err := client.Link(ctx, enrollment.SenderCredential, notifyRelayLinkRequest{
		InstallationID: "install", BindingID: "binding", Proof: "proof",
	})
	if err != nil || subscription.SubscriptionID != "subscription" || subscription.Status != "active" || !subscription.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("subscription = %+v, %v", subscription, err)
	}
	delivery, err := client.Deliver(ctx, enrollment.SenderCredential, notifyRelayDeliveryRequest{
		EventID: "event", SubscriptionID: subscription.SubscriptionID, Kind: "node.heard_again",
		NodePubkey: strings.Repeat("a", 64), OccurredAt: expiresAt.Add(-time.Hour), ExpiresAt: expiresAt,
	})
	if err != nil || delivery.DeliveryID != "delivery" || delivery.Status != "queued" {
		t.Fatalf("delivery = %+v, %v", delivery, err)
	}
	if err := client.RevokeSubscription(ctx, enrollment.SenderCredential, subscription.SubscriptionID); err != nil {
		t.Fatal(err)
	}
	if err := client.RevokeAnalyzer(ctx, enrollment.SenderCredential); err != nil {
		t.Fatal(err)
	}
}

func TestNotifyRelayClientRejectsRedirects(t *testing.T) {
	client := newTestNotifyRelayClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/analyzer" {
			t.Error("client followed redirect")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/credential-target", http.StatusTemporaryRedirect)
	})
	_, err := client.Analyzer(context.Background(), "credential")
	var rejected *notifyRelayError
	if !errors.As(err, &rejected) || rejected.Status != http.StatusTemporaryRedirect {
		t.Fatalf("redirect error = %v", err)
	}
}

func TestNotifyRelayClientErrors(t *testing.T) {
	client := newTestNotifyRelayClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":"credential should not be logged"}}`))
	})
	_, err := client.Deliver(context.Background(), "credential", notifyRelayDeliveryRequest{})
	var rejected *notifyRelayError
	if !errors.As(err, &rejected) || rejected.Status != http.StatusTooManyRequests || rejected.RetryAfter != 30*time.Second {
		t.Fatalf("HTTP error = %v, details=%+v", err, rejected)
	}
	if strings.Contains(err.Error(), "credential") {
		t.Fatalf("error exposed response contents: %v", err)
	}
}

func TestNotifyRelayClientRejectsInvalidResponses(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{"unsupported version", 200, `{"analyzer_id":"analyzer","origin":"https://analyzer.example","contract_version":2}`},
		{"oversized", 200, strings.Repeat("x", 100<<10)},
		{"malformed", 200, `{"analyzer_id":"analyzer"`},
		{"missing data", 200, `{}`},
		{"unexpected success status", 202, `{"analyzer_id":"analyzer","origin":"https://analyzer.example","contract_version":1}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := newTestNotifyRelayClient(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			})
			if _, err := client.Analyzer(context.Background(), "credential"); err == nil {
				t.Fatal("Analyzer accepted invalid response")
			}
		})
	}
}

func TestNotifyRelayClientRequiresHTTPSOrigin(t *testing.T) {
	for _, input := range []string{
		"http://relay.example", "https://relay.example/path", "https://relay.example/?q=1",
		"https://relay.example/#fragment", "https://user:pass@relay.example", "https:///missing",
		"https://:443", "https://relay.example?",
	} {
		if _, err := newNotifyRelayClient(input, nil); err == nil {
			t.Errorf("accepted invalid relay origin %q", input)
		}
	}
	if _, err := newNotifyRelayClient("https://relay.example/", nil); err != nil {
		t.Fatalf("HTTPS origin rejected: %v", err)
	}
}
