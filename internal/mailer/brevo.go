package mailer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const brevoBaseURL = "https://api.brevo.com/v3"

// Brevo sends through Brevo's transactional API.
type Brevo struct {
	APIKey    string
	FromEmail string
	FromName  string
	BaseURL   string // overridable for tests
	HTTP      *http.Client
}

func NewBrevo(apiKey, fromEmail, fromName string) *Brevo {
	return &Brevo{APIKey: apiKey, FromEmail: fromEmail, FromName: fromName,
		BaseURL: brevoBaseURL, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

type brevoAddress struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type brevoSendRequest struct {
	Sender      brevoAddress   `json:"sender"`
	To          []brevoAddress `json:"to"`
	Subject     string         `json:"subject"`
	HTMLContent string         `json:"htmlContent"`
	TextContent string         `json:"textContent,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
}

type brevoError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (b *Brevo) Send(ctx context.Context, m Message) (string, error) {
	req := brevoSendRequest{
		Sender:      brevoAddress{Email: b.FromEmail, Name: b.FromName},
		To:          []brevoAddress{{Email: m.To, Name: m.ToName}},
		Subject:     m.Subject,
		HTMLContent: m.HTML,
		TextContent: m.Text,
	}
	if m.Tag != "" {
		req.Tags = []string{m.Tag}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.BaseURL+"/smtp/email", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	data, status, err := b.do(httpReq)
	if err != nil {
		return "", fmt.Errorf("brevo: send: %w", err)
	}
	if status != http.StatusCreated && status != http.StatusAccepted {
		return "", brevoErr("send", status, data)
	}
	var out struct {
		MessageID string `json:"messageId"`
	}
	if err := json.Unmarshal(data, &out); err != nil || out.MessageID == "" {
		return "", errors.New("brevo: send: response has no messageId")
	}
	return out.MessageID, nil
}

type brevoEventsResponse struct {
	Events []struct {
		Date      string `json:"date"`
		Email     string `json:"email"`
		Event     string `json:"event"`
		MessageID string `json:"messageId"`
		Reason    string `json:"reason"`
	} `json:"events"`
}

func (b *Brevo) Events(ctx context.Context, messageID string) ([]Event, error) {
	q := url.Values{"messageId": {messageID}, "limit": {"100"}, "sort": {"asc"}}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, b.BaseURL+"/smtp/statistics/events?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	data, status, err := b.do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("brevo: events: %w", err)
	}
	if status != http.StatusOK {
		return nil, brevoErr("events", status, data)
	}
	var resp brevoEventsResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("brevo: events: %w", err)
	}
	out := make([]Event, 0, len(resp.Events))
	for _, e := range resp.Events {
		at, _ := parseBrevoDate(e.Date)
		out = append(out, Event{MessageID: e.MessageID, Email: e.Email, Event: NormalizeBrevoEvent(e.Event), Reason: e.Reason, At: at})
	}
	return out, nil
}

func (b *Brevo) do(r *http.Request) ([]byte, int, error) {
	r.Header.Set("api-key", b.APIKey)
	r.Header.Set("Accept", "application/json")
	resp, err := b.HTTP.Do(r)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	return data, resp.StatusCode, err
}

func brevoErr(op string, status int, body []byte) error {
	var e brevoError
	if json.Unmarshal(body, &e) == nil && e.Message != "" {
		return fmt.Errorf("brevo: %s: HTTP %d %s: %s", op, status, e.Code, e.Message)
	}
	return fmt.Errorf("brevo: %s: HTTP %d", op, status)
}

func parseBrevoDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000-07:00", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("brevo: unrecognized date %q", s)
}

// brevoEventNames maps webhook and events-API spellings to canonical names.
// "bounces" (events API, unspecified kind) is treated as soft so it never
// flags an address on its own.
var brevoEventNames = map[string]string{
	"request": EventSent, "requests": EventSent,
	"delivered": EventDelivered,
	"opened":    EventOpened, "unique_opened": EventOpened, "proxy_open": EventOpened,
	"unique_proxy_open": EventOpened, "loadedbyproxy": EventOpened,
	"click": EventClicked, "clicks": EventClicked,
	"soft_bounce": EventSoftBounce, "softbounces": EventSoftBounce, "bounces": EventSoftBounce,
	"hard_bounce": EventHardBounce, "hardbounces": EventHardBounce,
	"invalid_email": EventInvalidEmail, "invalid": EventInvalidEmail,
	"deferred": EventDeferred, "spam": EventSpam, "blocked": EventBlocked,
	"error": EventError, "unsubscribed": EventUnsubscribed,
}

// NormalizeBrevoEvent maps a Brevo event name to CoreScope's canonical name.
func NormalizeBrevoEvent(name string) string {
	key := strings.ToLower(strings.TrimSpace(name))
	if v, ok := brevoEventNames[key]; ok {
		return v
	}
	return key
}
