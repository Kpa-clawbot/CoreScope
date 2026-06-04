package main

// Issue #1561: When the server is fronted by a CDN (Cloudflare, Fastly,
// etc.) we cannot guarantee /api/* responses are not cached unless the
// operator configures a bypass rule. Detect typical CDN request headers
// at the first such request and log a one-shot warning pointing the
// operator at the bypass doc.
//
// Contract:
//   - Warning logs ONLY when a CDN-typical header is present
//     (CF-Connecting-IP, CF-Ray, X-Forwarded-For, X-Real-IP,
//      Fastly-Client-IP, True-Client-IP).
//   - Warning logs at most ONCE per process boot (sync.Once).
//   - Middleware NEVER blocks the request — it observes and continues.

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// resetCDNDetectionOnce restores a fresh sync.Once so each test starts
// from a clean "have not warned yet" state.
func resetCDNDetectionOnce() {
	cdnWarnOnce = sync.Once{}
}

func runWithCDNMiddleware(t *testing.T, req *http.Request) string {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	h := cdnDetectionMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("middleware must not block request; got status %d", w.Code)
	}
	return buf.String()
}

func TestCDNDetection_LogsOnCFRayHeader(t *testing.T) {
	resetCDNDetectionOnce()
	req := httptest.NewRequest("GET", "/api/observers", nil)
	req.Header.Set("CF-Ray", "abc123-LAX")

	out := runWithCDNMiddleware(t, req)

	if !strings.Contains(out, "detected request via CDN") {
		t.Errorf("expected log to contain 'detected request via CDN', got: %q", out)
	}
	if !strings.Contains(out, "deployment-behind-cdn") {
		t.Errorf("expected log to reference deployment-behind-cdn doc, got: %q", out)
	}
}

func TestCDNDetection_SilentWithoutCDNHeader(t *testing.T) {
	resetCDNDetectionOnce()
	req := httptest.NewRequest("GET", "/api/observers", nil)
	// No CDN-typical headers set.

	out := runWithCDNMiddleware(t, req)

	if strings.Contains(out, "detected request via CDN") {
		t.Errorf("expected no CDN warning without CDN headers, got: %q", out)
	}
}

func TestCDNDetection_LogsOnlyOnce(t *testing.T) {
	resetCDNDetectionOnce()

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	h := cdnDetectionMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/api/observers", nil)
		req.Header.Set("CF-Ray", "abc123")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
	}

	got := strings.Count(buf.String(), "detected request via CDN")
	if got != 1 {
		t.Errorf("expected CDN warning exactly once across multiple requests; got %d in output: %q", got, buf.String())
	}
}

// Each of the CDN-typical headers should trip the detector on its own.
func TestCDNDetection_RecognizesAllCommonCDNHeaders(t *testing.T) {
	headers := []string{
		"CF-Connecting-IP",
		"CF-Ray",
		"X-Forwarded-For",
		"X-Real-IP",
		"Fastly-Client-IP",
		"True-Client-IP",
	}
	for _, h := range headers {
		t.Run(h, func(t *testing.T) {
			resetCDNDetectionOnce()
			req := httptest.NewRequest("GET", "/api/observers", nil)
			req.Header.Set(h, "1.2.3.4")
			out := runWithCDNMiddleware(t, req)
			if !strings.Contains(out, "detected request via CDN") {
				t.Errorf("header %s should trip CDN detection; log was: %q", h, out)
			}
		})
	}
}
