package mailer

import (
	"testing"
	"time"
)

func TestParseBrevoWebhookSingle(t *testing.T) {
	body := []byte(`{"event":"hard_bounce","email":"a@example.org","id":1,"date":"2026-10-06 12:00:00",
		"ts":1791288000,"message-id":"<m1@relay>","ts_event":1791288005,"reason":"user unknown","subject":"x"}`)
	evs, err := ParseBrevoWebhook(body)
	if err != nil || len(evs) != 1 {
		t.Fatalf("ParseBrevoWebhook = %+v, %v", evs, err)
	}
	e := evs[0]
	if e.MessageID != "<m1@relay>" || e.Event != EventHardBounce || e.Reason != "user unknown" ||
		!e.At.Equal(time.Unix(1791288005, 0).UTC()) {
		t.Fatalf("event = %+v", e)
	}
}

func TestParseBrevoWebhookBatchAndFallbacks(t *testing.T) {
	body := []byte(`[{"event":"delivered","message-id":"<a>","ts":1791288000},
		{"event":"unique_opened","message-id":"<b>","date":"2026-10-06 12:00:00"},
		{"event":"delivered","email":"no-id@example.org"}]`)
	evs, err := ParseBrevoWebhook(body)
	if err != nil || len(evs) != 2 {
		t.Fatalf("batch = %+v, %v", evs, err)
	}
	if evs[0].At.Unix() != 1791288000 || evs[1].Event != EventOpened || evs[1].At.IsZero() {
		t.Fatalf("batch events = %+v", evs)
	}
}

func TestParseBrevoWebhookRejectsGarbage(t *testing.T) {
	for _, body := range []string{``, `not json`, `{}`, `[]`, `{"event":"delivered"}`} {
		if _, err := ParseBrevoWebhook([]byte(body)); err == nil {
			t.Errorf("ParseBrevoWebhook(%q) accepted", body)
		}
	}
}
