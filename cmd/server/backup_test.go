package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// sqliteMagic is the 16-byte file header identifying a valid SQLite 3 database.
// See https://www.sqlite.org/fileformat.html#magic_header_string
const sqliteMagic = "SQLite format 3\x00"

func TestBackupRequiresAPIKey(t *testing.T) {
	_, router := setupTestServerWithAPIKey(t, "test-secret-key-strong-enough")

	req := httptest.NewRequest("GET", "/api/backup", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without API key, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestBackupReturnsValidSQLiteSnapshot(t *testing.T) {
	const apiKey = "test-secret-key-strong-enough"
	_, router := setupTestServerWithAPIKey(t, apiKey)

	req := httptest.NewRequest("GET", "/api/backup", nil)
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	ct := w.Header().Get("Content-Type")
	if ct != "application/octet-stream" {
		t.Errorf("expected Content-Type application/octet-stream, got %q", ct)
	}

	cd := w.Header().Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment;") || !strings.Contains(cd, "filename=\"corescope-backup-") || !strings.HasSuffix(cd, ".db\"") {
		t.Errorf("expected Content-Disposition attachment with corescope-backup-<ts>.db filename, got %q", cd)
	}

	body := w.Body.Bytes()
	if len(body) < len(sqliteMagic) {
		t.Fatalf("backup body too short (%d bytes) — expected SQLite file", len(body))
	}
	if got := string(body[:len(sqliteMagic)]); got != sqliteMagic {
		t.Fatalf("expected SQLite magic header %q, got %q", sqliteMagic, got)
	}
}
