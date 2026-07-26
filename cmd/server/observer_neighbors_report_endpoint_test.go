package main

// #1865 follow-up (cwichura, PR #1867): /api/observers and
// /api/observers/{id} must expose last_neighbors_report_at end-to-end.
// This is a handler-level test on purpose: the DB layer (db.GetObservers/
// GetObserverByID) can carry the field correctly while the HTTP handler's
// separate ObserverResp DTO silently drops it during the field-by-field
// conversion in routes.go -- exactly the gap that shipped once already.

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestHandleObservers_ExposesLastNeighborsReportAt(t *testing.T) {
	srv, router := setupTestServer(t)

	ts := "2026-07-26T10:00:00Z"
	if _, err := srv.db.conn.Exec(`UPDATE observers SET last_neighbors_report_at = ? WHERE id = ?`, ts, "obs1"); err != nil {
		t.Fatalf("seed last_neighbors_report_at: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/observers", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var body struct {
		Observers []map[string]interface{} `json:"observers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}

	var got map[string]interface{}
	for _, o := range body.Observers {
		if o["id"] == "obs1" {
			got = o
			break
		}
	}
	if got == nil {
		t.Fatalf("obs1 not found in /api/observers response")
	}
	if v, ok := got["last_neighbors_report_at"]; !ok || v != ts {
		t.Errorf("last_neighbors_report_at = %v (present=%v), want %q", v, ok, ts)
	}
}

func TestHandleObservers_NilLastNeighborsReportAtWhenNeverReported(t *testing.T) {
	srv, router := setupTestServer(t)
	_ = srv

	req := httptest.NewRequest("GET", "/api/observers", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var body struct {
		Observers []map[string]interface{} `json:"observers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if len(body.Observers) == 0 {
		t.Fatal("expected at least one seeded observer")
	}
	// seedTestData never sets last_neighbors_report_at -- key must be
	// present (not omitted) and null, so the frontend can distinguish
	// "field exists, never reported" from a stale/misdeployed backend.
	got := body.Observers[0]
	v, ok := got["last_neighbors_report_at"]
	if !ok {
		t.Fatal("last_neighbors_report_at key missing from response entirely")
	}
	if v != nil {
		t.Errorf("last_neighbors_report_at = %v, want nil for an observer that never reported", v)
	}
}

func TestHandleObserverDetail_ExposesLastNeighborsReportAt(t *testing.T) {
	srv, router := setupTestServer(t)

	ts := "2026-07-26T11:00:00Z"
	if _, err := srv.db.conn.Exec(`UPDATE observers SET last_neighbors_report_at = ? WHERE id = ?`, ts, "obs1"); err != nil {
		t.Fatalf("seed last_neighbors_report_at: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/observers/obs1", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var got map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if v, ok := got["last_neighbors_report_at"]; !ok || v != ts {
		t.Errorf("last_neighbors_report_at = %v (present=%v), want %q", v, ok, ts)
	}
}
