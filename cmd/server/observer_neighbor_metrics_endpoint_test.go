package main

// #1865 follow-up: GET /api/observers/{id}/neighbors/{pubkey}/metrics
// serves SNR/heard_secs_ago history for one observer<->neighbor link.

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestHandleObserverNeighborMetrics_ReturnsOrderedHistory(t *testing.T) {
	srv, router := setupTestServer(t)

	pubkey := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	rows := []struct {
		ts  string
		snr float64
		hsa int
	}{
		{"2026-07-26T14:00:00Z", 6.0, 60},
		{"2026-07-26T12:00:00Z", 5.0, 75},
	}
	for _, r := range rows {
		if _, err := srv.db.conn.Exec(`INSERT INTO observer_neighbor_metrics (observer_id, neighbor_pubkey, timestamp, snr, heard_secs_ago) VALUES (?, ?, ?, ?, ?)`,
			"obs1", pubkey, r.ts, r.snr, r.hsa); err != nil {
			t.Fatalf("seed observer_neighbor_metrics: %v", err)
		}
	}

	req := httptest.NewRequest("GET", "/api/observers/obs1/neighbors/"+pubkey+"/metrics?since=2026-07-01T00:00:00Z", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var body struct {
		Metrics []struct {
			Timestamp    string   `json:"timestamp"`
			SNR          *float64 `json:"snr"`
			HeardSecsAgo *int     `json:"heardSecsAgo"`
		} `json:"metrics"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if len(body.Metrics) != 2 {
		t.Fatalf("expected 2 metric points, got %d", len(body.Metrics))
	}
	// Oldest first.
	if body.Metrics[0].Timestamp != "2026-07-26T12:00:00Z" || body.Metrics[1].Timestamp != "2026-07-26T14:00:00Z" {
		t.Errorf("expected chronological order, got %+v", body.Metrics)
	}
	if body.Metrics[0].SNR == nil || *body.Metrics[0].SNR != 5.0 {
		t.Errorf("first SNR = %v, want 5.0", body.Metrics[0].SNR)
	}
	if body.Metrics[0].HeardSecsAgo == nil || *body.Metrics[0].HeardSecsAgo != 75 {
		t.Errorf("first HeardSecsAgo = %v, want 75", body.Metrics[0].HeardSecsAgo)
	}
}

func TestHandleObserverNeighborMetrics_NoDataReturnsEmptyNotError(t *testing.T) {
	srv, router := setupTestServer(t)
	_ = srv

	req := httptest.NewRequest("GET", "/api/observers/obs1/neighbors/deadbeef/metrics", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var body struct {
		Metrics []interface{} `json:"metrics"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if body.Metrics == nil {
		t.Error("metrics must be an empty array, not null")
	}
}

func TestHandleObserverNeighborMetrics_SinceFiltersOlderRows(t *testing.T) {
	srv, router := setupTestServer(t)

	pubkey := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if _, err := srv.db.conn.Exec(`INSERT INTO observer_neighbor_metrics (observer_id, neighbor_pubkey, timestamp, snr) VALUES (?, ?, ?, ?)`,
		"obs1", pubkey, "2020-01-01T00:00:00Z", 1.0); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := srv.db.conn.Exec(`INSERT INTO observer_neighbor_metrics (observer_id, neighbor_pubkey, timestamp, snr) VALUES (?, ?, ?, ?)`,
		"obs1", pubkey, "2026-07-26T00:00:00Z", 2.0); err != nil {
		t.Fatalf("seed: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/observers/obs1/neighbors/"+pubkey+"/metrics?since=2026-01-01T00:00:00Z", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var body struct {
		Metrics []struct{ Timestamp string } `json:"metrics"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Metrics) != 1 || body.Metrics[0].Timestamp != "2026-07-26T00:00:00Z" {
		t.Errorf("expected only the recent row, got %+v", body.Metrics)
	}
}
