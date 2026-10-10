package mailer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPostalSendRequestShape(t *testing.T) {
	var gotPath, gotKey, gotCT string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey, gotCT = r.URL.Path, r.Header.Get("X-Server-API-Key"), r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		w.Write([]byte(`{"status":"success","time":0.01,"flags":{},"data":{"message_id":"abc@rp.postal.example",
			"messages":{"a@example.org":{"id":1234,"token":"tk"}}}}`))
	}))
	defer srv.Close()
	p := NewPostal(srv.URL, "postal-key", "noreply@example.org", "CoreScope Mesh")

	id, err := p.Send(context.Background(), Message{To: "a@example.org", ToName: "Alice", Subject: "S",
		HTML: "<p>h</p>", Text: "t", Tag: "activate", Headers: map[string]string{"List-Unsubscribe": "<x>"}})
	if err != nil || id != "1234" {
		t.Fatalf("Send = %q, %v", id, err)
	}
	if gotPath != "/api/v1/send/message" || gotKey != "postal-key" || !strings.HasPrefix(gotCT, "application/json") {
		t.Fatalf("path=%q key=%q content-type=%q", gotPath, gotKey, gotCT)
	}
	to := body["to"].([]any)
	if len(to) != 1 || to[0] != `"Alice" <a@example.org>` || body["from"] != `"CoreScope Mesh" <noreply@example.org>` ||
		body["subject"] != "S" || body["html_body"] != "<p>h</p>" || body["plain_body"] != "t" || body["tag"] != "activate" {
		t.Fatalf("request body = %+v", body)
	}
	if hdr, ok := body["headers"].(map[string]any); !ok || hdr["List-Unsubscribe"] != "<x>" {
		t.Fatalf("headers = %#v", body["headers"])
	}
}

func TestPostalSendOmitsEmptyOptionalFields(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		w.Write([]byte(`{"status":"success","data":{"messages":{"A@Example.org":{"id":7}}}}`))
	}))
	defer srv.Close()
	p := NewPostal(srv.URL+"/", "k", "noreply@example.org", "")
	id, err := p.Send(context.Background(), Message{To: "a@example.org", Text: "t"})
	if err != nil || id != "7" { // recipient key matched case-insensitively
		t.Fatalf("Send = %q, %v", id, err)
	}
	for _, k := range []string{"headers", "tag", "html_body"} {
		if _, present := body[k]; present {
			t.Errorf("%s sent when empty: %#v", k, body)
		}
	}
	if body["from"] != "noreply@example.org" || body["to"].([]any)[0] != "a@example.org" {
		t.Fatalf("addresses without names = %v / %v", body["from"], body["to"])
	}
}

func TestPostalSendErrorEnvelopes(t *testing.T) {
	cases := map[string]string{
		`{"status":"error","data":{"code":"InvalidServerAPIKey","message":"The API token provided in X-Server-API-Key was not valid.","token":"bad-key"}}`: "InvalidServerAPIKey",
		`{"status":"error","data":{"code":"UnauthenticatedFromAddress","message":"The From address is not authorised"}}`:                                   "UnauthenticatedFromAddress",
		`{"status":"parameter-error","data":{"message":"Invalid parameters"}}`:                                                                             "Invalid parameters",
		`{"status":"success","data":{"messages":{}}}`:                                                                                                      "no message id",
		`<html>Bad gateway</html>`: "HTTP 200",
	}
	for resp, want := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(resp))
		}))
		p := NewPostal(srv.URL, "bad-key", "noreply@example.org", "")
		_, err := p.Send(context.Background(), Message{To: "a@example.org", Text: "t"})
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v; want %q", resp, err, want)
			continue
		}
		if strings.Contains(err.Error(), "bad-key") {
			t.Errorf("API key leaked into the error text: %v", err)
		}
	}
}

func TestPostalSendHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	p := NewPostal(srv.URL, "k", "noreply@example.org", "")
	if _, err := p.Send(context.Background(), Message{To: "a@example.org", Text: "t"}); err == nil ||
		!strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("err = %v", err)
	}
}

func TestPostalSendRejectsEmptyContentAndNilClient(t *testing.T) {
	p := &Postal{APIKey: "k", FromEmail: "f@example.org", BaseURL: "http://127.0.0.1:1"}
	if _, err := p.Send(context.Background(), Message{To: "a@example.org"}); err == nil ||
		!strings.Contains(err.Error(), "no content") {
		t.Fatalf("err = %v", err)
	}
	// nil HTTP client must not panic (connection error expected).
	if _, err := p.Send(context.Background(), Message{To: "a@example.org", Text: "t"}); err == nil {
		t.Fatal("expected connection error")
	}
}

func TestPostalDoesNotFollowRedirects(t *testing.T) {
	var leaked string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("X-Server-API-Key")
		w.Write([]byte(`{"status":"success","data":{"messages":{"a@example.org":{"id":1}}}}`))
	}))
	defer target.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusPermanentRedirect)
	}))
	defer srv.Close()
	p := NewPostal(srv.URL, "secret-key", "noreply@example.org", "")
	if _, err := p.Send(context.Background(), Message{To: "a@example.org", Text: "t"}); err == nil {
		t.Fatal("Send followed a redirect")
	}
	if leaked != "" {
		t.Fatalf("API key reached the redirect target: %q", leaked)
	}
}

func TestPostalEvents(t *testing.T) {
	var gotPath, gotKey string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey = r.URL.Path, r.Header.Get("X-Server-API-Key")
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		w.Write([]byte(`{"status":"success","data":[
			{"id":1,"status":"SoftFail","details":"Temporary failure","output":"451 4.7.1 Try again later","timestamp":1791288005.25},
			{"id":2,"status":"HardFail","details":"Permanent SMTP delivery error when sending to mx.example.org","output":"550 5.1.1 User unknown","timestamp":1791288065.5},
			{"id":3,"status":"HardFail","details":"Message is likely spam. Threshold is 5.0 and the message scored 7.2.","output":"","timestamp":1791288125}]}`))
	}))
	defer srv.Close()
	p := NewPostal(srv.URL, "k", "noreply@example.org", "")
	evs, err := p.Events(context.Background(), "1234")
	if err != nil || len(evs) != 3 {
		t.Fatalf("Events = %+v, %v", evs, err)
	}
	if gotPath != "/api/v1/messages/deliveries" || gotKey != "k" || body["id"] != float64(1234) {
		t.Fatalf("path=%q key=%q body=%v", gotPath, gotKey, body)
	}
	if evs[0].MessageID != "1234" || evs[0].Event != EventDeferred || evs[0].Reason != "Temporary failure: 451 4.7.1 Try again later" ||
		!evs[0].At.Equal(time.Unix(1791288005, 250e6).UTC()) {
		t.Fatalf("first event = %+v", evs[0])
	}
	if evs[1].Event != EventHardBounce || evs[1].Reason != "Permanent SMTP delivery error when sending to mx.example.org: 550 5.1.1 User unknown" {
		t.Fatalf("5xx rejection = %+v", evs[1])
	}
	if evs[2].Event != EventError || evs[2].Reason != "Message is likely spam. Threshold is 5.0 and the message scored 7.2." {
		t.Fatalf("spam hard fail = %+v", evs[2])
	}
}

func TestPostalEventsErrors(t *testing.T) {
	p := NewPostal("http://127.0.0.1:1", "k", "noreply@example.org", "")
	if _, err := p.Events(context.Background(), "<brevo-id@relay>"); err == nil || !strings.Contains(err.Error(), "not a Postal message id") {
		t.Fatalf("non-numeric id: err = %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"status":"error","data":{"code":"MessageNotFound","message":"No message found matching provided ID","id":99}}`))
	}))
	defer srv.Close()
	p.BaseURL = srv.URL
	if _, err := p.Events(context.Background(), "99"); err == nil || !strings.Contains(err.Error(), "MessageNotFound") {
		t.Fatalf("not found: err = %v", err)
	}
}

func TestNormalizePostalDelivery(t *testing.T) {
	cases := []struct{ status, output, want string }{
		{"Sent", "250 2.0.0 OK", EventDelivered},
		{"SoftFail", "451 4.7.1 Greylisted", EventDeferred},
		{"Held", "", EventBlocked},
		{"Error", "", EventError},
		{"HoldCancelled", "", "holdcancelled"},
		// HardFail is a bounce only when the recipient's server rejected the
		// mail with a permanent 5xx reply.
		{"HardFail", "550 5.1.1 <a@example.org>: Recipient address rejected: User unknown", EventHardBounce},
		{"HardFail", "554-5.7.1 Rejected by policy", EventHardBounce},
		{"HardFail", " 552 5.2.2 Mailbox full", EventHardBounce},
		{"HardFail", "550", EventHardBounce},
		// Postal-side hard fails carry no SMTP reply: spam threshold, maximum
		// attempts, raw message removed, domain deleted.
		{"HardFail", "", EventError},
		{"HardFail", "421 4.3.2 Service shutting down", EventError},
		{"HardFail", "5000 not a reply code", EventError},
		{"HardFail", "connection refused", EventError},
		// A bounce message carries no SMTP reply either, and may be a delay
		// notice or an auto-reply.
		{"Bounced", "", EventError},
	}
	for _, c := range cases {
		if got := NormalizePostalDelivery(c.status, c.output); got != c.want {
			t.Errorf("NormalizePostalDelivery(%q, %q) = %q; want %q", c.status, c.output, got, c.want)
		}
	}
}
