package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The two unauthenticated POST endpoints must cap their request bodies.
// Before the cap, json.Decoder buffered the whole body and /api/decode
// hex-decoded it before the 184-byte payload check ran, so one request
// could hold a multi-hundred-MB buffer.
func TestUnauthenticatedPostBodyLimits(t *testing.T) {
	_, router := setupTestServerWithAPIKey(t, "")

	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		return w
	}

	t.Run("decode: normal body still works", func(t *testing.T) {
		w := post("/api/decode", `{"hex":"0200"}`)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
		}
	})

	t.Run("decode: oversized body is rejected with 413", func(t *testing.T) {
		big := `{"hex":"` + strings.Repeat("ab", decodeBodyLimit) + `"}`
		w := post("/api/decode", big)
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("expected 413, got %d (body: %s)", w.Code, w.Body.String())
		}
	})

	t.Run("observations: normal body still works", func(t *testing.T) {
		w := post("/api/packets/observations", `{"hashes":["abc123"]}`)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
		}
	})

	t.Run("observations: oversized body is rejected with 413", func(t *testing.T) {
		big := `{"hashes":["` + strings.Repeat("a", batchObservationsBodyLimit) + `"]}`
		w := post("/api/packets/observations", big)
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("expected 413, got %d (body: %s)", w.Code, w.Body.String())
		}
	})
}
