package mailer

import (
	"io"
	"net/http"
	"time"
)

// newNoRedirectHTTPClient never follows redirects, so a provider's API key
// header cannot be forwarded to another host.
func newNoRedirectHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// doLimited sends r and reads at most 256 KiB of the response. A nil client
// gets a fresh no-redirect one.
func doLimited(client *http.Client, r *http.Request) ([]byte, int, error) {
	if client == nil {
		client = newNoRedirectHTTPClient()
	}
	resp, err := client.Do(r)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	return data, resp.StatusCode, err
}
