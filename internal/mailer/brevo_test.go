package mailer

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrevoSendRequestShape(t *testing.T) {
	var gotPath, gotKey string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotKey = r.URL.Path, r.Header.Get("api-key")
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"messageId":"<201798300811.5787683@relay.domain.com>"}`))
	}))
	defer srv.Close()
	b := NewBrevo("xkeysib-test", "noreply@example.org", "CoreScope")
	b.BaseURL = srv.URL

	id, err := b.Send(context.Background(), Message{To: "a@example.org", ToName: "Alice", Subject: "S",
		HTML: "<p>h</p>", Text: "t", Tag: "activate"})
	if err != nil || id != "<201798300811.5787683@relay.domain.com>" {
		t.Fatalf("Send = %q, %v", id, err)
	}
	if gotPath != "/smtp/email" || gotKey != "xkeysib-test" {
		t.Fatalf("path=%q key=%q", gotPath, gotKey)
	}
	sender := body["sender"].(map[string]any)
	to := body["to"].([]any)[0].(map[string]any)
	if sender["email"] != "noreply@example.org" || sender["name"] != "CoreScope" ||
		to["email"] != "a@example.org" || to["name"] != "Alice" ||
		body["subject"] != "S" || body["htmlContent"] != "<p>h</p>" || body["textContent"] != "t" ||
		body["tags"].([]any)[0] != "activate" {
		t.Fatalf("request body = %+v", body)
	}
}

func TestBrevoSendErrorMapping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"code":"unauthorized","message":"Key not found"}`))
	}))
	defer srv.Close()
	b := NewBrevo("bad", "noreply@example.org", "")
	b.BaseURL = srv.URL
	_, err := b.Send(context.Background(), Message{To: "a@example.org"})
	if err == nil || !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "Key not found") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(err.Error(), "bad") {
		t.Fatal("API key leaked into the error text")
	}
}

func TestBrevoEvents(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Write([]byte(`{"events":[
			{"date":"2026-10-06T12:00:05.000+02:00","email":"a@example.org","event":"requests","messageId":"<m1>"},
			{"date":"2026-10-06T12:00:09.000+02:00","email":"a@example.org","event":"hardBounces","messageId":"<m1>","reason":"user unknown"}]}`))
	}))
	defer srv.Close()
	b := NewBrevo("k", "noreply@example.org", "")
	b.BaseURL = srv.URL
	evs, err := b.Events(context.Background(), "<m1>")
	if err != nil || len(evs) != 2 {
		t.Fatalf("Events = %+v, %v", evs, err)
	}
	if !strings.Contains(gotQuery, "messageId=%3Cm1%3E") {
		t.Fatalf("query = %q", gotQuery)
	}
	if evs[0].Event != EventSent || evs[1].Event != EventHardBounce || evs[1].Reason != "user unknown" ||
		evs[1].At.UTC().Hour() != 10 {
		t.Fatalf("events = %+v", evs)
	}
}

func TestNormalizeBrevoEvent(t *testing.T) {
	cases := map[string]string{
		"request": EventSent, "requests": EventSent, "delivered": EventDelivered,
		"opened": EventOpened, "unique_opened": EventOpened, "proxy_open": EventOpened, "loadedByProxy": EventOpened,
		"click": EventClicked, "clicks": EventClicked,
		"soft_bounce": EventSoftBounce, "softBounces": EventSoftBounce, "bounces": EventSoftBounce,
		"hard_bounce": EventHardBounce, "hardBounces": EventHardBounce,
		"invalid_email": EventInvalidEmail, "invalid": EventInvalidEmail,
		"spam": EventSpam, "blocked": EventBlocked, "deferred": EventDeferred, "error": EventError,
		"Something_New": "something_new",
	}
	for in, want := range cases {
		if got := NormalizeBrevoEvent(in); got != want {
			t.Errorf("NormalizeBrevoEvent(%q) = %q; want %q", in, got, want)
		}
	}
}
