package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// External node alerts (#2177): a JSON feed of per-node alerts that a job
// outside CoreScope computes, e.g. coverage sectors where a repeater is
// never heard. The notifier reads it for the node.external event; the
// server never writes anything for it outside users.db.

const (
	externalFeedMaxBytes = 1 << 20
	externalFeedTimeout  = 10 * time.Second
	externalKeyMaxLen    = 64
	externalTextMaxLen   = 200
	// externalBaselineKey is the subject suffix "<pubkey>/*" that marks a
	// user's node.external baseline for one watched node: the first fresh
	// feed after watching stores every current alert without mailing;
	// afterwards an alert without a row is new.
	externalBaselineKey = "*"
)

// externalFeedJSON is the feed as published.
type externalFeedJSON struct {
	GeneratedAt string              `json:"generatedAt"`
	Alerts      []externalAlertJSON `json:"alerts"`
}

type externalAlertJSON struct {
	Pubkey string `json:"pubkey"`
	Key    string `json:"key"`
	Text   string `json:"text"`
	URL    string `json:"url,omitempty"`
}

// externalAlert is one validated alert.
type externalAlert struct {
	Text string // mail-safe
	URL  string // absolute http(s) or ""
}

// externalFeed is a validated, fresh feed: alerts by lowercase pubkey,
// then by key.
type externalFeed struct {
	GeneratedAt time.Time
	Alerts      map[string]map[string]externalAlert
	Skipped     int // malformed entries left out
}

// parseExternalFeed validates body. It fails when the body does not
// decode, generatedAt is missing, or the feed is older than maxAge at now;
// the caller then treats the source as stale. Malformed alerts are
// skipped and counted; a duplicate (pubkey, key) keeps the first.
func parseExternalFeed(body []byte, now time.Time, maxAge time.Duration) (*externalFeed, error) {
	var raw externalFeedJSON
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	gen, err := time.Parse(time.RFC3339, strings.TrimSpace(raw.GeneratedAt))
	if err != nil {
		return nil, errors.New("generatedAt missing or not RFC 3339")
	}
	if age := now.Sub(gen); age > maxAge {
		return nil, fmt.Errorf("generated %s ago, older than %s", age.Round(time.Minute), maxAge)
	}
	f := &externalFeed{GeneratedAt: gen, Alerts: map[string]map[string]externalAlert{}}
	for _, a := range raw.Alerts {
		pk, ok := notifyPubkey(a.Pubkey)
		key := mailSafeText(a.Key)
		text := mailSafeText(a.Text)
		if !ok || key == "" || key == externalBaselineKey || len(key) > externalKeyMaxLen || text == "" {
			f.Skipped++
			continue
		}
		if len(text) > externalTextMaxLen {
			text = strings.ToValidUTF8(text[:externalTextMaxLen], "") + "..."
		}
		if f.Alerts[pk] == nil {
			f.Alerts[pk] = map[string]externalAlert{}
		}
		if _, dup := f.Alerts[pk][key]; dup {
			f.Skipped++
			continue
		}
		f.Alerts[pk][key] = externalAlert{Text: text, URL: externalAlertURL(a.URL)}
	}
	return f, nil
}

// externalAlertURL keeps an absolute http(s) URL and drops anything else.
func externalAlertURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return u.String()
}

// sortedExternalKeys returns the keys of m in order.
func sortedExternalKeys(m map[string]externalAlert) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// fetchExternalFeed GETs the feed: 200 only, at most externalFeedMaxBytes.
func fetchExternalFeed(client *http.Client, feedURL string) ([]byte, error) {
	resp, err := client.Get(feedURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, externalFeedMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > externalFeedMaxBytes {
		return nil, fmt.Errorf("larger than %d bytes", externalFeedMaxBytes)
	}
	return body, nil
}
