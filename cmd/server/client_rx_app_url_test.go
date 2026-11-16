package main

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestValidClientRxAppURL(t *testing.T) {
	cases := map[string]bool{
		"https://rx.example.org":          true,
		"https://rx.example.org/":         true,
		"https://rx.example.org/beta/":    true,
		"https://rx.example.org:8443/app": true,
		"":                                false,
		"http://rx.example.org":           false,
		"rx.example.org":                  false,
		"/rx":                             false,
		"https://":                        false,
		"https:///path":                   false,
		"https://:443":                    false,
		"https:rx.example.org":            false,
		"javascript:alert(1)":             false,
		"ftp://rx.example.org":            false,
		"https://rx example.org":          false,
	}
	for raw, want := range cases {
		if got := validClientRxAppURL(raw); got != want {
			t.Errorf("validClientRxAppURL(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestClientConfigClientRxAppURL(t *testing.T) {
	srv, router := setupTestServer(t)
	get := func() ClientConfigResponse {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/api/config/client", nil))
		if strings.Contains(w.Body.String(), `"clientRxAppUrl":""`) {
			t.Fatalf("empty clientRxAppUrl must be omitted: %s", w.Body.String())
		}
		var resp ClientConfigResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}
	const valid = "https://rx.example.org"
	cases := []struct {
		name string
		cov  *ClientRxCoverageConfig
		want string
	}{
		{"section absent", nil, ""},
		{"feature off with valid url", &ClientRxCoverageConfig{Enabled: false, AppURL: valid}, ""},
		{"feature on without url", &ClientRxCoverageConfig{Enabled: true}, ""},
		{"feature on with http url", &ClientRxCoverageConfig{Enabled: true, AppURL: "http://rx.example.org"}, ""},
		{"feature on with relative url", &ClientRxCoverageConfig{Enabled: true, AppURL: "rx.example.org"}, ""},
		{"feature on with valid url", &ClientRxCoverageConfig{Enabled: true, AppURL: valid}, valid},
	}
	for _, c := range cases {
		srv.cfg.ClientRxCoverage = c.cov
		if got := get().ClientRxAppURL; got != c.want {
			t.Errorf("%s: clientRxAppUrl = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestWarnClientRxAppURL(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	for _, cfg := range []*Config{
		{},
		{ClientRxCoverage: &ClientRxCoverageConfig{Enabled: true}},
		{ClientRxCoverage: &ClientRxCoverageConfig{Enabled: true, AppURL: "https://rx.example.org"}},
	} {
		warnClientRxAppURL(cfg)
	}
	if buf.Len() != 0 {
		t.Fatalf("unexpected warning: %q", buf.String())
	}
	warnClientRxAppURL(&Config{ClientRxCoverage: &ClientRxCoverageConfig{Enabled: true, AppURL: "http://rx.example.org"}})
	if n := strings.Count(buf.String(), "clientRxCoverage.appUrl"); n != 1 {
		t.Fatalf("want 1 warning line, got %d: %q", n, buf.String())
	}
}
