package main

// Issue #1478 — surface observers whose envelope timestamps were clamped
// because they were emitted with a naive (zone-less) local-time string.
// /api/observers and /api/observers/{id} must expose four new fields so the
// UI can render a ⚠️ chip + a banner explaining "this observer's clock is
// off and per-packet timing is being clamped to ingest time".
//
// Tests are behavioral: they seed the same DB columns the ingestor will write
// to and assert the JSON response carries the field values plus the derived
// `clock_naive` boolean. They will FAIL on master (columns don't exist; JSON
// has no clock_* keys) → red commit.

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

// recentNaiveObserver inserts an observer row with a recent clock-naive
// skew event already recorded. The handler should report clock_naive=true.
func TestHandleObservers_Issue1478_SurfacesRecentNaiveSkew(t *testing.T) {
	srv, router := setupTestServer(t)
	_ = srv

	now := time.Now().UTC()
	recent := now.Add(-2 * time.Hour).Format(time.RFC3339)
	// Seed an observer whose ingestor has recorded a -8h naive clamp 2h ago.
	_, err := srv.db.conn.Exec(`INSERT INTO observers
		(id, name, iata, last_seen, first_seen, packet_count,
		 clock_skew_seconds, clock_skew_count_24h, clock_last_naive_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"naive-obs-1", "California Pi", "SFO",
		now.Format(time.RFC3339), now.Add(-7*24*time.Hour).Format(time.RFC3339),
		42, -28800, 17, recent)
	if err != nil {
		t.Fatalf("seed observer: %v", err)
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
		if o["id"] == "naive-obs-1" {
			got = o
			break
		}
	}
	if got == nil {
		t.Fatalf("expected observer naive-obs-1 in response, got %d entries", len(body.Observers))
	}

	if v, ok := got["clock_naive"]; !ok {
		t.Fatalf("expected clock_naive field in observer JSON, missing. keys=%v", mapKeys1478(got))
	} else if b, ok := v.(bool); !ok || !b {
		t.Errorf("expected clock_naive=true, got %v (%T)", v, v)
	}
	if v, ok := got["clock_skew_seconds"]; !ok {
		t.Errorf("expected clock_skew_seconds field, missing")
	} else if n, ok := v.(float64); !ok || int64(n) != -28800 {
		t.Errorf("expected clock_skew_seconds=-28800, got %v", v)
	}
	if v, ok := got["clock_skew_count_24h"]; !ok {
		t.Errorf("expected clock_skew_count_24h field, missing")
	} else if n, ok := v.(float64); !ok || int(n) != 17 {
		t.Errorf("expected clock_skew_count_24h=17, got %v", v)
	}
	if v, ok := got["clock_last_naive_at"]; !ok || v == nil {
		t.Errorf("expected clock_last_naive_at populated, got %v", v)
	}
}

func TestHandleObservers_Issue1478_DecaysAfter24h(t *testing.T) {
	srv, router := setupTestServer(t)
	now := time.Now().UTC()
	// 30h ago — past the 24h window. clock_naive must be false.
	stale := now.Add(-30 * time.Hour).Format(time.RFC3339)
	_, err := srv.db.conn.Exec(`INSERT INTO observers
		(id, name, iata, last_seen, first_seen, packet_count,
		 clock_skew_seconds, clock_skew_count_24h, clock_last_naive_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"naive-obs-old", "Fixed Pi", "LAX",
		now.Format(time.RFC3339), now.Add(-30*24*time.Hour).Format(time.RFC3339),
		99, -28800, 5, stale)
	if err != nil {
		t.Fatalf("seed observer: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/observers", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var body struct {
		Observers []map[string]interface{} `json:"observers"`
	}
	json.Unmarshal(w.Body.Bytes(), &body)

	var got map[string]interface{}
	for _, o := range body.Observers {
		if o["id"] == "naive-obs-old" {
			got = o
			break
		}
	}
	if got == nil {
		t.Fatalf("expected naive-obs-old in response")
	}
	if v, _ := got["clock_naive"]; v != false {
		t.Errorf("after 24h decay clock_naive must be false, got %v", v)
	}
	// Count and skew should also be zeroed for the response (decay).
	if v, _ := got["clock_skew_count_24h"]; v != nil {
		if n, ok := v.(float64); ok && int(n) != 0 {
			t.Errorf("expected clock_skew_count_24h=0 after decay, got %v", v)
		}
	}
}

func TestHandleObserverDetail_Issue1478_IncludesClockNaiveFields(t *testing.T) {
	srv, router := setupTestServer(t)
	now := time.Now().UTC()
	recent := now.Add(-5 * time.Minute).Format(time.RFC3339)
	_, err := srv.db.conn.Exec(`INSERT INTO observers
		(id, name, iata, last_seen, first_seen, packet_count,
		 clock_skew_seconds, clock_skew_count_24h, clock_last_naive_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"naive-obs-detail", "Detail Pi", "SJC",
		now.Format(time.RFC3339), now.Add(-2*24*time.Hour).Format(time.RFC3339),
		7, 25200, 3, recent)
	if err != nil {
		t.Fatalf("seed observer: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/observers/naive-obs-detail", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}
	var got map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if v, _ := got["clock_naive"]; v != true {
		t.Errorf("expected clock_naive=true, got %v", v)
	}
	if v, _ := got["clock_skew_seconds"]; v == nil {
		t.Errorf("expected clock_skew_seconds set")
	}
}

func mapKeys1478(m map[string]interface{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
