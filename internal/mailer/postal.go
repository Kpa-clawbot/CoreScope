package mailer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/mail"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Postal sends through a self-hosted Postal server's legacy HTTP API
// (https://apiv1.postalserver.io). Every call is a JSON POST authenticated
// with X-Server-API-Key; Postal answers 200 and reports the outcome in the
// envelope's "status" field.
type Postal struct {
	APIKey    string
	FromEmail string
	FromName  string
	BaseURL   string // e.g. https://postal.example.org, no trailing slash
	HTTP      *http.Client
}

func NewPostal(baseURL, apiKey, fromEmail, fromName string) *Postal {
	return &Postal{APIKey: apiKey, FromEmail: fromEmail, FromName: fromName,
		BaseURL: strings.TrimRight(baseURL, "/"), HTTP: newNoRedirectHTTPClient()}
}

type postalSendRequest struct {
	To        []string          `json:"to"`
	From      string            `json:"from"`
	Subject   string            `json:"subject"`
	PlainBody string            `json:"plain_body,omitempty"`
	HTMLBody  string            `json:"html_body,omitempty"`
	Tag       string            `json:"tag,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

type postalEnvelope struct {
	Status string          `json:"status"`
	Data   json.RawMessage `json:"data"`
}

type postalErrorData struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type postalSendData struct {
	Messages map[string]struct {
		ID int64 `json:"id"`
	} `json:"messages"`
}

type postalDelivery struct {
	Status    string      `json:"status"`
	Details   string      `json:"details"`
	Output    string      `json:"output"` // the remote server's SMTP reply, if any
	Timestamp postalFloat `json:"timestamp"`
}

// Send returns Postal's numeric message id for the recipient, the id the
// deliveries endpoint and the webhooks use.
func (p *Postal) Send(ctx context.Context, m Message) (string, error) {
	if m.HTML == "" && m.Text == "" {
		return "", errors.New("postal: send: message has no content")
	}
	req := postalSendRequest{
		To:        []string{postalAddress(m.ToName, m.To)},
		From:      postalAddress(p.FromName, p.FromEmail),
		Subject:   m.Subject,
		PlainBody: m.Text,
		HTMLBody:  m.HTML,
		Tag:       m.Tag,
		Headers:   m.Headers,
	}
	var out postalSendData
	if err := p.call(ctx, "send", "/api/v1/send/message", req, &out); err != nil {
		return "", err
	}
	// Postal keys the result by the bare recipient address.
	if msg, ok := out.Messages[m.To]; ok && msg.ID > 0 {
		return strconv.FormatInt(msg.ID, 10), nil
	}
	for addr, msg := range out.Messages {
		if strings.EqualFold(addr, m.To) && msg.ID > 0 {
			return strconv.FormatInt(msg.ID, 10), nil
		}
	}
	return "", errors.New("postal: send: response has no message id")
}

func (p *Postal) Events(ctx context.Context, messageID string) ([]Event, error) {
	id, err := strconv.ParseInt(messageID, 10, 64)
	if err != nil || id <= 0 {
		return nil, errors.New("postal: events: not a Postal message id")
	}
	var deliveries []postalDelivery
	if err := p.call(ctx, "events", "/api/v1/messages/deliveries", struct {
		ID int64 `json:"id"`
	}{id}, &deliveries); err != nil {
		return nil, err
	}
	out := make([]Event, 0, len(deliveries))
	for _, d := range deliveries {
		out = append(out, Event{MessageID: messageID, Event: NormalizePostalDelivery(d.Status, d.Output),
			Reason: postalReason(d.Details, d.Output), At: d.Timestamp.timeOrNow()})
	}
	return out, nil
}

// call POSTs params to path and decodes a successful envelope's data into out.
func (p *Postal) call(ctx context.Context, op, path string, params, out any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, p.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	r.Header.Set("X-Server-API-Key", p.APIKey)
	data, status, err := doLimited(p.HTTP, r)
	if err != nil {
		return fmt.Errorf("postal: %s: %w", op, err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("postal: %s: HTTP %d", op, status)
	}
	var env postalEnvelope
	if err := json.Unmarshal(data, &env); err != nil || env.Status == "" {
		return fmt.Errorf("postal: %s: HTTP %d: unreadable response", op, status)
	}
	if env.Status != "success" {
		// Only code and message are used: InvalidServerAPIKey echoes the key
		// back in a "token" field.
		var e postalErrorData
		json.Unmarshal(env.Data, &e)
		return fmt.Errorf("postal: %s: %s %s: %s", op, env.Status, e.Code, e.Message)
	}
	if err := json.Unmarshal(env.Data, out); err != nil {
		return fmt.Errorf("postal: %s: %w", op, err)
	}
	return nil
}

// postalAddress formats "Name <addr>" (RFC 5322 quoted or encoded), or the
// bare address when there is no name.
func postalAddress(name, addr string) string {
	if name == "" {
		return addr
	}
	return (&mail.Address{Name: name, Address: addr}).String()
}

// postalFloat is a Unix timestamp in seconds. Postal sends most as JSON
// numbers, but decimal database columns can arrive as strings.
type postalFloat float64

func (f *postalFloat) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*f = 0
		return nil
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return err
	}
	*f = postalFloat(v)
	return nil
}

func (f postalFloat) time() time.Time {
	if f <= 0 {
		return time.Time{}
	}
	sec, frac := math.Modf(float64(f))
	return time.Unix(int64(sec), int64(math.Round(frac*1e6))*1e3).UTC()
}

func (f postalFloat) timeOrNow() time.Time {
	if t := f.time(); !t.IsZero() {
		return t
	}
	return time.Now().UTC()
}

// postalStatusEvents maps delivery statuses to canonical names. Postal's
// "Sent" means the recipient's server accepted the message. "hardfail" and
// "bounced" are absent: see NormalizePostalDelivery.
var postalStatusEvents = map[string]string{
	"sent": EventDelivered, "softfail": EventDeferred, "held": EventBlocked, "error": EventError,
}

// permanentSMTPReply matches a 5xx SMTP reply line, e.g. "550 5.1.1 ..." or
// the first line of a multi-line reply, "554-5.7.1 ...".
var permanentSMTPReply = regexp.MustCompile(`^5[0-9]{2}(?:[ -]|$)`)

// NormalizePostalDelivery maps a Postal delivery status and its SMTP output
// to CoreScope's canonical event name. Unknown statuses pass through
// lower-cased.
//
// A hard bounce flags the address until the user changes it, so only a
// permanent rejection by the recipient's server counts: a HardFail whose
// output is a 5xx reply. Postal also hard-fails for reasons on its own side
// (outbound spam threshold, maximum attempts after soft failures, raw message
// removed, domain deleted), with no SMTP reply; those and bounce messages,
// which can be delay notices or auto-replies, are reported as errors.
func NormalizePostalDelivery(status, output string) string {
	key := strings.ToLower(strings.TrimSpace(status))
	switch key {
	case "hardfail":
		if permanentSMTPReply.MatchString(strings.TrimSpace(output)) {
			return EventHardBounce
		}
		return EventError
	case "bounced":
		return EventError
	}
	if v, ok := postalStatusEvents[key]; ok {
		return v
	}
	return key
}

// postalReason joins Postal's delivery details with the SMTP reply, which
// carries the actual cause (e.g. "550 5.1.1 User unknown").
func postalReason(details, output string) string {
	details, output = strings.TrimSpace(details), strings.TrimSpace(output)
	switch {
	case output == "":
		return details
	case details == "":
		return output
	}
	return details + ": " + output
}
