package main

// Issue #1561: When the server is fronted by a CDN (Cloudflare, Fastly,
// Akamai) we cannot guarantee /api/* responses are not cached unless
// the operator configures a bypass rule. Detect CDN-specific request
// headers at the first such request and log a one-shot warning
// pointing the operator at the bypass doc.
//
// Contract:
//   - Warning logs ONLY when a CDN-specific header is present
//     (CF-Connecting-IP, CF-Ray, Fastly-Client-IP, True-Client-IP).
//   - Generic reverse-proxy headers (X-Forwarded-For, X-Real-IP) MUST
//     NOT trigger the warning — every nginx/Caddy/Traefik/k8s install
//     sets those, so warning on them defeats the entire signal.
//   - Warning logs at most ONCE per process boot (sync.Once), even
//     under concurrent first-request load.
//   - Middleware NEVER blocks the request — it always calls
//     next.ServeHTTP.

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// resetCDNDetectionOnce restores a fresh sync.Once so each test starts
// from a clean "have not warned yet" state.
func resetCDNDetectionOnce() {
	cdnWarnOnce = sync.Once{}
}

// runWithCDNMiddleware fires the request through the middleware and
// returns (log output, whether next was called). The sentinel proves
// the middleware did not silently drop the request.
func runWithCDNMiddleware(t *testing.T, req *http.Request) (string, bool) {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	nextCalled := false
	h := cdnDetectionMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("middleware must not block request; got status %d", w.Code)
	}
	return buf.String(), nextCalled
}

func TestCDNDetection_LogsOnCFRayHeader(t *testing.T) {
	resetCDNDetectionOnce()
	req := httptest.NewRequest("GET", "/api/observers", nil)
	req.Header.Set("CF-Ray", "abc123-LAX")

	out, nextCalled := runWithCDNMiddleware(t, req)

	if !nextCalled {
		t.Fatal("middleware did not call next handler")
	}
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

	out, nextCalled := runWithCDNMiddleware(t, req)

	if !nextCalled {
		t.Fatal("middleware did not call next handler")
	}
	if strings.Contains(out, "detected request via CDN") {
		t.Errorf("expected no CDN warning without CDN headers, got: %q", out)
	}
}

// Regression for round-1 adversarial finding: generic reverse-proxy
// headers must NOT trigger the warning. Every nginx/Caddy/Traefik/
// k8s-ingress reverse proxy sets X-Forwarded-For and X-Real-IP, so
// flagging them produces a false positive on every reverse-proxied
// install and trains operators to ignore the warning.
func TestCDNDetection_SilentOnReverseProxyHeadersAlone(t *testing.T) {
	cases := []struct {
		name   string
		header string
	}{
		{"x-forwarded-for-alone", "X-Forwarded-For"},
		{"x-real-ip-alone", "X-Real-IP"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetCDNDetectionOnce()
			req := httptest.NewRequest("GET", "/api/observers", nil)
			req.Header.Set(tc.header, "10.0.0.1")
			// No CDN-specific headers — just the generic reverse-proxy one.

			out, nextCalled := runWithCDNMiddleware(t, req)

			if !nextCalled {
				t.Fatal("middleware did not call next handler")
			}
			if strings.Contains(out, "detected request via CDN") {
				t.Errorf("header %s alone must NOT trigger CDN warning (would false-positive every nginx/k8s deploy); got: %q", tc.header, out)
			}
		})
	}
}

// When a CDN-specific header is present alongside generic proxy
// headers (common: Cloudflare → nginx → app), the warning still fires.
func TestCDNDetection_LogsWhenCDNHeaderAccompaniesProxyHeaders(t *testing.T) {
	resetCDNDetectionOnce()
	req := httptest.NewRequest("GET", "/api/observers", nil)
	req.Header.Set("X-Forwarded-For", "10.0.0.1")
	req.Header.Set("X-Real-IP", "10.0.0.1")
	req.Header.Set("CF-Connecting-IP", "1.2.3.4")

	out, nextCalled := runWithCDNMiddleware(t, req)

	if !nextCalled {
		t.Fatal("middleware did not call next handler")
	}
	if !strings.Contains(out, "detected request via CDN") {
		t.Errorf("expected CDN warning when CF-Connecting-IP present alongside proxy headers; got: %q", out)
	}
}

func TestCDNDetection_LogsOnlyOnce(t *testing.T) {
	resetCDNDetectionOnce()

	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)

	nextCalled := 0
	h := cdnDetectionMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled++
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", "/api/observers", nil)
		req.Header.Set("CF-Ray", "abc123")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
	}

	if nextCalled != 3 {
		t.Fatalf("middleware must call next on every request; got %d calls, want 3", nextCalled)
	}
	got := strings.Count(buf.String(), "detected request via CDN")
	if got != 1 {
		t.Errorf("expected CDN warning exactly once across multiple requests; got %d in output: %q", got, buf.String())
	}
}

// Each genuinely CDN-specific header should trip the detector on its
// own. X-Forwarded-For / X-Real-IP are NOT in this set — see the
// negative test TestCDNDetection_SilentOnReverseProxyHeadersAlone.
func TestCDNDetection_RecognizesAllCommonCDNHeaders(t *testing.T) {
	headers := []string{
		"CF-Connecting-IP",
		"CF-Ray",
		"Fastly-Client-IP",
		"True-Client-IP",
	}
	for _, h := range headers {
		t.Run(h, func(t *testing.T) {
			resetCDNDetectionOnce()
			req := httptest.NewRequest("GET", "/api/observers", nil)
			req.Header.Set(h, "1.2.3.4")
			out, nextCalled := runWithCDNMiddleware(t, req)
			if !nextCalled {
				t.Fatal("middleware did not call next handler")
			}
			if !strings.Contains(out, "detected request via CDN") {
				t.Errorf("header %s should trip CDN detection; log was: %q", h, out)
			}
		})
	}
}

// Round-1 KB finding #2: sync.Once is what keeps the log from
// spamming — verify it holds under concurrent first-request load.
// CI runs `go test -race`, so this also stresses the underlying
// primitive for data races. Without -race, the assertion still
// catches a plain bool / non-atomic implementation.
func TestCDNDetectionMiddlewareConcurrentFirstRequestLogsOnce(t *testing.T) {
	resetCDNDetectionOnce()

	var buf bytes.Buffer
	var bufMu sync.Mutex
	prev := log.Writer()
	// log.Printf can be called concurrently; serialize writes to buf
	// so we never race the test's own assertion read.
	log.SetOutput(writerFunc(func(p []byte) (int, error) {
		bufMu.Lock()
		defer bufMu.Unlock()
		return buf.Write(p)
	}))
	defer log.SetOutput(prev)

	var nextCalls int64
	h := cdnDetectionMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&nextCalls, 1)
		w.WriteHeader(http.StatusOK)
	}))

	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/api/observers", nil)
			req.Header.Set("CF-Ray", "abc123-LAX")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&nextCalls); got != n {
		t.Fatalf("middleware must call next on every concurrent request; got %d, want %d", got, n)
	}

	bufMu.Lock()
	out := buf.String()
	bufMu.Unlock()
	got := strings.Count(out, "detected request via CDN")
	if got != 1 {
		t.Errorf("expected sync.Once to admit exactly ONE warning under %d concurrent first-requests; got %d. Output:\n%s", n, got, out)
	}
}

// writerFunc adapts a function to io.Writer.
type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
