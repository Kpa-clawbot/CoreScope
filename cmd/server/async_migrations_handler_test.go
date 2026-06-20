// HTTP handler tests for /api/perf/async-migrations (#1735 finding #11).
//
// Covers: success (200 + array body), empty list (200 + []),
// readAsyncMigrations error (HTTP 500 + error body), nil db (200 + []).

package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// makeAsyncMigrationsServer constructs a minimal *Server with the DB
// wrapper populated by the supplied conn (which may be a closed handle
// to provoke the error path).
func makeAsyncMigrationsServer(t *testing.T, conn *sql.DB) *Server {
	t.Helper()
	invalidateAsyncMigrationsCache()
	s := &Server{}
	if conn != nil {
		s.db = &DB{conn: conn}
	}
	return s
}

func TestHandlePerfAsyncMigrations_SuccessNonEmpty(t *testing.T) {
	conn := openAsyncTestDB(t)
	_, err := conn.Exec(`INSERT INTO _async_migrations
		(name, status, started_at, rows_processed, rows_total)
		VALUES ('mig_a', 'done', '2026-06-16 11:59:00', 100, 100)`)
	if err != nil {
		t.Fatal(err)
	}
	s := makeAsyncMigrationsServer(t, conn)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/perf/async-migrations", nil)
	s.handlePerfAsyncMigrations(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rr.Code)
	}
	var got []AsyncMigrationInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body=%s)", err, rr.Body.String())
	}
	if len(got) != 1 || got[0].Name != "mig_a" || got[0].Status != "done" {
		t.Errorf("got %+v, want one done mig_a row", got)
	}
}

func TestHandlePerfAsyncMigrations_EmptyList(t *testing.T) {
	conn := openAsyncTestDB(t) // table exists, no rows
	s := makeAsyncMigrationsServer(t, conn)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/perf/async-migrations", nil)
	s.handlePerfAsyncMigrations(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rr.Code)
	}
	var got []AsyncMigrationInfo
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("want empty array, got %v", got)
	}
	// Must be `[]` not `null` so JS consumers can iterate without
	// nil-checks (warmup-banner.js).
	body := strings.TrimSpace(rr.Body.String())
	if !strings.HasPrefix(body, "[") {
		t.Errorf("body should start with '[', got %q", body)
	}
}

func TestHandlePerfAsyncMigrations_ReadErrorReturns500(t *testing.T) {
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close() // poison: subsequent Query fails
	s := makeAsyncMigrationsServer(t, conn)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/perf/async-migrations", nil)
	s.handlePerfAsyncMigrations(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d, want 500 (error must NOT be hidden behind empty list)", rr.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode err body: %v", err)
	}
	if !strings.Contains(body["error"], "readAsyncMigrations") {
		t.Errorf("error body=%v, want mention of readAsyncMigrations", body)
	}
}

func TestHandlePerfAsyncMigrations_NilDBReturnsEmptyOK(t *testing.T) {
	s := makeAsyncMigrationsServer(t, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/perf/async-migrations", nil)
	s.handlePerfAsyncMigrations(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200", rr.Code)
	}
	if strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Errorf("body=%q, want '[]'", rr.Body.String())
	}
}
