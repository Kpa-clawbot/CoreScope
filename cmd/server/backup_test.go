package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestHandleBackup_RequiresAPIKey(t *testing.T) {
	s := &Server{cfg: &Config{APIKey: ""}}
	handler := s.requireAPIKey(http.HandlerFunc(s.handleBackup))

	req := httptest.NewRequest("GET", "/api/backup", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden && rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected auth rejection, got %d", rr.Code)
	}
}

func TestHandleBackup_ReturnsSQLite(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// Initialize an empty SQLite file so OpenDB (read-only) can open it.
	if err := os.WriteFile(dbPath, []byte{}, 0644); err != nil {
		t.Fatalf("create db file: %v", err)
	}

	db, err := OpenDB(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	s := &Server{
		cfg: &Config{APIKey: "a-strong-test-api-key-1234"},
		db:  db,
	}

	req := httptest.NewRequest("GET", "/api/backup", nil)
	req.Header.Set("X-API-Key", "a-strong-test-api-key-1234")
	rr := httptest.NewRecorder()
	s.handleBackup(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	ct := rr.Header().Get("Content-Type")
	if ct != "application/octet-stream" {
		t.Errorf("expected application/octet-stream, got %s", ct)
	}

	// SQLite files start with the magic header "SQLite format 3"
	magic := []byte("SQLite format 3")
	if !bytes.HasPrefix(rr.Body.Bytes(), magic) {
		t.Errorf("response does not look like a SQLite file")
	}
}
