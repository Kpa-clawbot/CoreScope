package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/meshcore-analyzer/users"
)

// Pushover (https://pushover.net/api) is a per-user destination: each user
// saves their own Pushover user key, and CoreScope sends their alerts with
// the instance's application token. It carries the same alerts as email.

// PushoverConfig enables Pushover. The application token comes from
// CORESCOPE_PUSHOVER_TOKEN, never from the configuration file.
type PushoverConfig struct {
	// MaxPerMonth caps messages per 30 days for the instance; Pushover's
	// free tier allows 10,000 per application per month.
	MaxPerMonth int `json:"maxPerMonth,omitempty"`
	// SubscriptionCode turns on "Connect Pushover": users subscribe on
	// pushover.net and come back with a key made for this application,
	// instead of pasting their user key. From the application's
	// subscription settings on pushover.net.
	SubscriptionCode string `json:"subscriptionCode,omitempty"`
}

const (
	pushoverTokenEnv          = "CORESCOPE_PUSHOVER_TOKEN"
	defaultPushoverMaxMonthly = 7500
	pushoverAPI               = "https://api.pushover.net/1"
	pushoverMessageLifetime   = time.Hour
	pushoverClientTimeout     = 10 * time.Second
	// Pushover's field limits, in characters.
	pushoverMaxTitle    = 250
	pushoverMaxMessage  = 1024
	pushoverMaxURL      = 512
	pushoverMaxURLTitle = 100
)

// Application tokens and user keys are 30 letters and digits.
var pushoverKeyPattern = regexp.MustCompile(`^[A-Za-z0-9]{30}$`)

// Subscription codes look like "Name-1oiwozgx5tg38a1".
var pushoverSubscriptionPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,100}$`)

type pushoverSettings struct {
	client           *pushoverClient
	maxPerMonth      int
	subscriptionCode string // "" when only pasted keys are offered
	subscribeBase    string // pushover.net, overridden in tests
}

func resolvePushover(c *PushoverConfig, getenv func(string) string) (*pushoverSettings, error) {
	if c == nil {
		return nil, nil
	}
	token := strings.TrimSpace(getenv(pushoverTokenEnv))
	if !pushoverKeyPattern.MatchString(token) {
		return nil, fmt.Errorf("userManagement.notifications.pushover requires %s, the 30-character application token", pushoverTokenEnv)
	}
	if c.MaxPerMonth < 0 {
		return nil, errors.New("userManagement.notifications.pushover.maxPerMonth must not be negative")
	}
	code := strings.TrimSpace(c.SubscriptionCode)
	if code != "" && !pushoverSubscriptionPattern.MatchString(code) {
		return nil, errors.New("userManagement.notifications.pushover.subscriptionCode must be the code from the application's subscription settings")
	}
	return &pushoverSettings{
		client:           newPushoverClient(pushoverAPI, token, nil),
		maxPerMonth:      positiveOr(c.MaxPerMonth, defaultPushoverMaxMonthly),
		subscriptionCode: code,
		subscribeBase:    "https://pushover.net/subscribe/",
	}, nil
}

type pushoverClient struct {
	base  string
	token string
	http  *http.Client
}

func newPushoverClient(base, token string, transport http.RoundTripper) *pushoverClient {
	return &pushoverClient{base: base, token: token, http: &http.Client{
		Transport: transport,
		Timeout:   pushoverClientTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// pushoverError is a request Pushover answered with an error.
type pushoverError struct {
	Status      int
	InvalidUser bool // the user key (or device) is not valid
	// Unparsed: the body was not Pushover's JSON (a proxy's error page,
	// say), so the status says nothing about the message.
	Unparsed   bool
	Errors     []string
	RetryAfter time.Duration
}

func (e *pushoverError) Error() string {
	if len(e.Errors) > 0 {
		return fmt.Sprintf("Pushover returned HTTP %d: %s", e.Status, strings.Join(e.Errors, "; "))
	}
	return fmt.Sprintf("Pushover returned HTTP %d", e.Status)
}

type pushoverMessage struct {
	Title, Message, URL, URLTitle string
}

func (c *pushoverClient) post(ctx context.Context, path string, form url.Values) error {
	form.Set("token", c.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var body struct {
		Status int      `json:"status"`
		User   string   `json:"user"`
		Device string   `json:"device"`
		Errors []string `json:"errors"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body)
	if resp.StatusCode == http.StatusOK && decodeErr == nil && body.Status == 1 {
		return nil
	}
	e := &pushoverError{Status: resp.StatusCode, Errors: body.Errors, Unparsed: decodeErr != nil}
	if resp.StatusCode == http.StatusOK {
		e.Status = http.StatusBadGateway // a 200 without status 1 is not a success
	}
	if !e.Unparsed && resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
		e.InvalidUser = body.User == "invalid" || body.Device == "invalid"
	}
	if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
		e.RetryAfter = time.Duration(secs) * time.Second
	}
	return e
}

// Validate checks a user key with Pushover.
func (c *pushoverClient) Validate(ctx context.Context, userKey string) error {
	return c.post(ctx, "/users/validate.json", url.Values{"user": {userKey}})
}

// Send delivers one message to a user.
func (c *pushoverClient) Send(ctx context.Context, userKey string, m pushoverMessage) error {
	form := url.Values{
		"user":    {userKey},
		"title":   {truncateRunes(m.Title, pushoverMaxTitle)},
		"message": {truncateRunes(m.Message, pushoverMaxMessage)},
	}
	if m.URL != "" && len(m.URL) <= pushoverMaxURL {
		form.Set("url", m.URL)
		form.Set("url_title", truncateRunes(m.URLTitle, pushoverMaxURLTitle))
	}
	return c.post(ctx, "/messages.json", form)
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// pushoverMessage renders one user's changes from one evaluation as a
// single message, like the alert mail: a line per change, the newest
// state, and a link to the node or to the account's notifications.
func (a *authService) pushoverMessage(uid int64, changes []notifyChange, now time.Time) users.PushoverMessage {
	base := a.set.baseURL.String()
	lines := make([]string, 0, len(changes))
	for _, c := range changes {
		lines = append(lines, mailSafeText(notifyChangeText(c, a.set.notify.external.label)))
	}
	msg := users.PushoverMessage{
		UserID: uid, Title: a.set.baseURL.Host, Message: fitPushoverLines(lines),
		URL: base + "/#/account?section=notifications", URLTitle: "Open notifications",
		ExpiresAt: now.Add(pushoverMessageLifetime),
	}
	if len(changes) == 1 {
		msg.URL, msg.URLTitle = notifySubjectURL(base, changes[0]), "Open in CoreScope"
	}
	return msg
}

// fitPushoverLines joins lines within Pushover's message limit, ending
// with how many did not fit.
func fitPushoverLines(lines []string) string {
	var b strings.Builder
	for i, line := range lines {
		more := ""
		if rest := len(lines) - i - 1; rest > 0 {
			more = fmt.Sprintf("\n…and %d more", rest)
		}
		sep := ""
		if i > 0 {
			sep = "\n"
		}
		if utf8.RuneCountInString(b.String()+sep+line+more) > pushoverMaxMessage {
			if i == 0 {
				return truncateRunes(line, pushoverMaxMessage)
			}
			b.WriteString(fmt.Sprintf("\n…and %d more", len(lines)-i))
			return b.String()
		}
		b.WriteString(sep + line)
	}
	return b.String()
}

// pushoverDestination queues one Pushover message per user and evaluation
// for users with Pushover on, and sends the queue on its own loop.
type pushoverDestination struct {
	a   *authService
	now func() time.Time
	// capReached keeps the monthly-cap log to one line per stretch, and
	// prunedAt spaces out pruning. Only the run loop touches them.
	capReached bool
	prunedAt   time.Time
}

func (d *pushoverDestination) beginTick() {}

func (d *pushoverDestination) stage(c notifyChanges, out *users.NotifyOutbox) func() {
	if d.a.set.notify.pushover == nil {
		return nil
	}
	ids := make([]int64, 0, len(c.byUser))
	for uid := range c.byUser {
		ids = append(ids, uid)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	now := d.now()
	for _, uid := range ids {
		if len(c.byUser[uid]) == 0 || c.accounts[uid].Status != users.StatusActive {
			continue
		}
		// The store queues it only if the user has Pushover on and is under
		// the daily limit.
		out.PushoverMessages = append(out.PushoverMessages, d.a.pushoverMessage(uid, c.byUser[uid], now))
	}
	out.PushoverPerUserPerDay = d.a.set.notify.perUserPerDay
	return nil
}

func (d *pushoverDestination) deliver(context.Context, time.Time, notifyChanges) int { return 0 }
func (d *pushoverDestination) logKey() string                                        { return "" }

func (d *pushoverDestination) run(stop <-chan struct{}) {
	if d.a.set.notify.pushover == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := d.dispatch(ctx); err != nil && ctx.Err() == nil {
				log.Printf("[notify] process Pushover queue: %v", err)
			}
		}
	}
}

// pushoverRemaining is how many messages the instance may still send in
// the 30-day window (0 at the cap).
func (a *authService) pushoverRemaining(now time.Time) (int, error) {
	sent, err := a.st.PushoverSentSince(now.Add(-30 * 24 * time.Hour))
	if err != nil {
		return 0, err
	}
	return max(a.set.notify.pushover.maxPerMonth-sent, 0), nil
}

// pushoverCapReached reports whether the instance sent its monthly cap.
func (a *authService) pushoverCapReached(now time.Time) (bool, error) {
	n, err := a.pushoverRemaining(now)
	return err == nil && n == 0, err
}

const (
	pushoverBatch      = 25
	pushoverPruneEvery = time.Minute
)

// dispatch sends a batch of due messages. An empty queue costs one indexed
// SELECT: the cap is counted only when something is due, and expired
// messages are pruned at most once a minute.
func (d *pushoverDestination) dispatch(ctx context.Context) error {
	ps := d.a.set.notify.pushover
	if ps == nil {
		return nil
	}
	st := d.a.st
	if now := d.now(); now.Sub(d.prunedAt) >= pushoverPruneEvery {
		if err := st.PrunePushoverJobs(now); err != nil {
			return err
		}
		d.prunedAt = now
	}
	jobs, err := st.DuePushoverJobs(d.now(), pushoverBatch)
	if err != nil || len(jobs) == 0 {
		return err
	}
	remaining, err := d.a.pushoverRemaining(d.now())
	if err != nil {
		return err
	}
	if remaining == 0 {
		if !d.capReached {
			log.Printf("[notify] Pushover monthly cap of %d messages reached; queued messages expire unsent", ps.maxPerMonth)
		}
		d.capReached = true
		return nil
	}
	d.capReached = false
	if len(jobs) > remaining {
		jobs = jobs[:remaining] // never past the cap, even within a batch
	}
	for _, job := range jobs {
		if err := ctx.Err(); err != nil {
			return err
		}
		sendErr := ps.client.Send(ctx, job.UserKey, pushoverMessage{
			Title: job.Title, Message: job.Message, URL: job.URL, URLTitle: job.URLTitle,
		})
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if sendErr == nil {
			if err := st.CompletePushoverJob(job.ID, true); err != nil {
				return err
			}
			continue
		}
		var pe *pushoverError
		if errors.As(sendErr, &pe) && pe.InvalidUser {
			if off, err := st.DisablePushoverKey(job.UserID, job.UserKey); err != nil {
				return err
			} else if off {
				log.Printf("[notify] Pushover rejected user #%d's key; Pushover turned off for them", job.UserID)
			}
			if err := st.CompletePushoverJob(job.ID, false); err != nil {
				return err
			}
			continue
		}
		now := d.now()
		if delay := pushoverRetry(sendErr, job.Attempts); delay > 0 && now.Add(delay).Before(job.ExpiresAt) {
			if err := st.RetryPushoverJob(job.ID, now.Add(delay)); err != nil {
				return err
			}
			continue
		}
		log.Printf("[notify] drop Pushover message %d for user #%d: %v", job.ID, job.UserID, sendErr)
		if err := st.CompletePushoverJob(job.ID, false); err != nil {
			return err
		}
	}
	return nil
}

// pushoverRetry is the delay before another attempt, or 0 to drop: 429,
// 5xx, network errors and answers that aren't Pushover's JSON back off
// from 5 s to 5 min (or follow Retry-After); any other 4xx is final.
func pushoverRetry(err error, attempts int) time.Duration {
	var pe *pushoverError
	if errors.As(err, &pe) {
		if !pe.Unparsed && pe.Status != http.StatusTooManyRequests && pe.Status < 500 {
			return 0
		}
		if pe.RetryAfter > 0 {
			return pe.RetryAfter
		}
	}
	if attempts >= 6 {
		return 5 * time.Minute
	}
	return (5 * time.Second) << attempts
}
