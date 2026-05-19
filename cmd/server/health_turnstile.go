package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"
)

type turnstileResponse struct {
	Success bool `json:"success"`
}

func verifyTurnstileToken(secretKey, token, remoteIP string) error {
	resp, err := http.PostForm(
		"https://challenges.cloudflare.com/turnstile/v0/siteverify",
		url.Values{
			"secret":   {secretKey},
			"response": {token},
			"remoteip": {remoteIP},
		},
	)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var result turnstileResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return err
	}
	if !result.Success {
		return fmt.Errorf("turnstile verification failed")
	}
	return nil
}

// HealthTokenStore holds valid hc_auth cookie values with expiry.
type HealthTokenStore struct {
	mu     sync.Mutex
	tokens map[string]time.Time
	done   chan struct{}
}

func NewHealthTokenStore() *HealthTokenStore {
	ts := &HealthTokenStore{
		tokens: make(map[string]time.Time),
		done:   make(chan struct{}),
	}
	go ts.cleanup()
	return ts
}

func generateAuthToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (ts *HealthTokenStore) Add(token string, expiry time.Time) {
	ts.mu.Lock()
	ts.tokens[token] = expiry
	ts.mu.Unlock()
}

func (ts *HealthTokenStore) Valid(token string) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	exp, ok := ts.tokens[token]
	return ok && time.Now().Before(exp)
}

func (ts *HealthTokenStore) cleanup() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ts.done:
			return
		case <-ticker.C:
			ts.mu.Lock()
			now := time.Now()
			for t, exp := range ts.tokens {
				if now.After(exp) {
					delete(ts.tokens, t)
				}
			}
			ts.mu.Unlock()
		}
	}
}

func (ts *HealthTokenStore) Stop() {
	select {
	case <-ts.done:
	default:
		close(ts.done)
	}
}
