package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// postgresMagic is the signature of a PostgreSQL custom-format archive.
var postgresMagic = testNativeSQL("SQLite format 3\x00", "PGDMP")

// Installed PostgreSQL selections pin even the default schema explicitly.
func selectedBackupTestDSN(t *testing.T) string {
	t.Helper()
	dsn := testDatabaseDSN(t)
	if testBackendValue() == "postgres" {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal("invalid fixture URL")
		}
		q := u.Query()
		q.Set("search_path", `"public"`)
		u.RawQuery = q.Encode()
		dsn = u.String()
	}
	return dsn
}

func TestBackupRequiresAPIKey(t *testing.T) {
	_, router := setupTestServerWithAPIKey(t, "test-secret-key-strong-enough")

	req := httptest.NewRequest("GET", "/api/backup", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without API key, got %d (body: %s)", w.Code, w.Body.String())
	}
}

func TestBackupRestoresNativePostgresSnapshot(t *testing.T) {
	const apiKey = "test-secret-key-strong-enough"
	srv, router := setupTestServerWithAPIKey(t, apiKey)
	srv.db = setupTestDBAtURL(t, selectedBackupTestDSN(t))
	seedTestData(t, srv.db)
	writerURL := srv.db.path
	reader, err := openFixtureReader(t, writerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	srv.db = reader

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
	if !strings.HasPrefix(cd, "attachment;") || !strings.Contains(cd, "filename=\"corescope-backup-") || !strings.HasSuffix(cd, testNativeSQL(".db\"", ".dump\"")) {
		t.Errorf("expected Content-Disposition attachment with corescope-backup-<ts>.dump filename, got %q", cd)
	}

	body := w.Body.Bytes()
	if len(body) < len(postgresMagic) {
		t.Fatalf("backup body too short (%d bytes) — expected PostgreSQL file", len(body))
	}
	if got := string(body[:len(postgresMagic)]); got != postgresMagic {
		t.Fatalf("expected PostgreSQL magic header %q, got %q", postgresMagic, got)
	}
	path := filepath.Join(t.TempDir(), "telemetry.dump")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	restored := restorePostgresTestBackup(t, path)
	var transmissions, observations int
	if err := restored.QueryRow(`SELECT COUNT(*) FROM transmissions`).Scan(&transmissions); err != nil {
		t.Fatal(err)
	}
	if err := restored.QueryRow(`SELECT COUNT(*) FROM observations`).Scan(&observations); err != nil {
		t.Fatal(err)
	}
	if transmissions != 3 || observations != 4 {
		t.Fatalf("restored counts = %d/%d", transmissions, observations)
	}

}
