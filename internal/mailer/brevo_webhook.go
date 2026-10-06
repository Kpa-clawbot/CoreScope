package mailer

import (
	"bytes"
	"encoding/json"
	"errors"
	"time"
)

type brevoWebhookEvent struct {
	Event     string `json:"event"`
	Email     string `json:"email"`
	MessageID string `json:"message-id"`
	Reason    string `json:"reason"`
	Date      string `json:"date"`
	Ts        int64  `json:"ts"`
	TsEvent   int64  `json:"ts_event"`
}

// ParseBrevoWebhook parses a transactional webhook body: one event object or,
// for batched webhooks, an array. Entries without a message id or event name
// are skipped; an error is returned if nothing usable remains.
func ParseBrevoWebhook(body []byte) ([]Event, error) {
	trimmed := bytes.TrimSpace(body)
	var raw []brevoWebhookEvent
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &raw); err != nil {
			return nil, err
		}
	} else {
		var one brevoWebhookEvent
		if err := json.Unmarshal(trimmed, &one); err != nil {
			return nil, err
		}
		raw = append(raw, one)
	}
	out := make([]Event, 0, len(raw))
	for _, e := range raw {
		if e.MessageID == "" || e.Event == "" {
			continue
		}
		var at time.Time
		switch {
		case e.TsEvent > 0:
			at = time.Unix(e.TsEvent, 0).UTC()
		case e.Ts > 0:
			at = time.Unix(e.Ts, 0).UTC()
		default:
			at, _ = parseBrevoDate(e.Date)
		}
		if at.IsZero() {
			at = time.Now().UTC()
		}
		out = append(out, Event{MessageID: e.MessageID, Email: e.Email, Event: NormalizeBrevoEvent(e.Event), Reason: e.Reason, At: at})
	}
	if len(out) == 0 {
		return nil, errors.New("brevo webhook: no usable events")
	}
	return out, nil
}
