package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A UI served from another origin (a static host listed in
// CORS_ALLOWED_ORIGINS) sends its three anonymous JSON POSTs with Content-Type
// text/plain. That keeps each one a CORS simple request: no preflight, which
// the embed CORS tier (Allow-Methods GET, HEAD, OPTIONS) would
// refuse. It works because these handlers decode the body as JSON whatever its
// Content-Type. If a handler starts insisting on application/json, this fails
// here instead of on the static host.
func TestAnonymousPostsAcceptTextPlainJSON(t *testing.T) {
	_, router := setupTestServerWithAPIKey(t, "")
	// The full test server is still warming the inspect graph (503), and the
	// bare server path_inspect_test.go uses has no node data to search. A
	// prefix the handler rejects after decoding still proves the point: a body
	// it could not decode would answer "invalid JSON" instead.
	inspect := newTestServerForInspect(t)

	cases := []struct {
		path, body string
		handler    http.Handler
		want       int
		wantBody   string
	}{
		{"/api/decode", `{"hex":"0200"}`, router, http.StatusOK, `"decoded"`},
		{"/api/packets/observations", `{"hashes":["abc123"]}`, router, http.StatusOK, `"results"`},
		{"/api/paths/inspect", `{"prefixes":["abc"]}`, http.HandlerFunc(inspect.handlePathInspect), http.StatusBadRequest, "even-length hex"},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			post := func(contentType string) *httptest.ResponseRecorder {
				req := httptest.NewRequest("POST", c.path, bytes.NewBufferString(c.body))
				req.Header.Set("Content-Type", contentType)
				req.Header.Set("Origin", "https://beta.corescope.example")
				w := httptest.NewRecorder()
				c.handler.ServeHTTP(w, req)
				return w
			}
			asJSON := post("application/json")
			asText := post("text/plain;charset=UTF-8")
			if asJSON.Code != c.want || !bytes.Contains(asJSON.Body.Bytes(), []byte(c.wantBody)) {
				t.Fatalf("application/json: expected %d with %s, got %d (body: %s)", c.want, c.wantBody, asJSON.Code, asJSON.Body.String())
			}
			if asText.Code != asJSON.Code {
				t.Fatalf("text/plain: got %d, application/json got %d (body: %s)", asText.Code, asJSON.Code, asText.Body.String())
			}
			if asText.Body.String() != asJSON.Body.String() {
				t.Fatalf("text/plain answered differently:\n text/plain: %s\n json:       %s", asText.Body.String(), asJSON.Body.String())
			}
		})
	}
}
