package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// Canned fixture mirroring the upstream channels-by-country.json shape
// (https://raw.githubusercontent.com/marcelverdult/meshcore-channels/main/channels-by-country.json
// pinned 2026-05-24). Two countries: one with entries, one empty (to test
// the "skip empty countries" branch).
const knownChannelsFixture = `{
  "generated_at": "2026-05-24T22:29:02Z",
  "license": "CC0-1.0",
  "countries": {
    "be": [
      {"channel": "#antwerpen", "description": "antwerpen"},
      {"channel": "#bemesh",    "description": "bemesh"}
    ],
    "us": [
      {"channel": "#bayarea", "description": "Bay Area"}
    ],
    "ad": []
  }
}`

// (a) Cache parses a canned JSON fixture into a snapshot.
func TestKnownChannelsParseFixture(t *testing.T) {
	snap, err := parseKnownChannelsJSON([]byte(knownChannelsFixture), "fixture://test", time.Unix(1700000000, 0))
	if err != nil {
		t.Fatalf("parseKnownChannelsJSON: %v", err)
	}
	if snap == nil {
		t.Fatal("snapshot is nil")
	}
	if snap.GeneratedAt != "2026-05-24T22:29:02Z" {
		t.Errorf("GeneratedAt = %q, want 2026-05-24T22:29:02Z", snap.GeneratedAt)
	}
	if snap.License != "CC0-1.0" {
		t.Errorf("License = %q, want CC0-1.0", snap.License)
	}
	if snap.Source != "fixture://test" {
		t.Errorf("Source = %q, want fixture://test", snap.Source)
	}
	if got, want := len(snap.Entries), 3; got != want {
		t.Fatalf("len(Entries) = %d, want %d (empty country ad must be skipped)", got, want)
	}
	// Spot-check one entry's region stamping.
	var foundAntwerpen bool
	for _, e := range snap.Entries {
		if e.Channel == "#antwerpen" {
			foundAntwerpen = true
			if e.Region != "be" {
				t.Errorf("antwerpen Region = %q, want be", e.Region)
			}
		}
	}
	if !foundAntwerpen {
		t.Fatal("antwerpen entry missing from snapshot")
	}
}

// (b) The route returns 200 + filtered list.
func TestKnownChannelsRouteRegionFilter(t *testing.T) {
	snap, err := parseKnownChannelsJSON([]byte(knownChannelsFixture), "fixture://test", time.Now())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	srv := &Server{
		knownChannels: &knownChannelsCache{},
	}
	srv.knownChannels.ptr.Store(snap)

	r := mux.NewRouter()
	r.HandleFunc("/api/known-channels", srv.handleKnownChannels).Methods("GET")

	req := httptest.NewRequest(http.MethodGet, "/api/known-channels?region=be", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp KnownChannelsSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v; body=%s", err, w.Body.String())
	}
	if got := len(resp.Entries); got != 2 {
		t.Fatalf("filtered entries = %d, want 2 (be has 2); got body=%s", got, w.Body.String())
	}
	for _, e := range resp.Entries {
		if e.Region != "be" {
			t.Errorf("entry %q has region %q, want be", e.Channel, e.Region)
		}
		if !strings.HasPrefix(e.Channel, "#") {
			t.Errorf("entry channel %q missing # prefix", e.Channel)
		}
	}
}

// (c) Cache survives upstream 500 (fail-soft): a prior good snapshot must
// remain available after a failed refresh.
func TestKnownChannelsFailSoftOn500(t *testing.T) {
	// First server: returns the fixture (success).
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(knownChannelsFixture))
	}))
	defer good.Close()

	c := newKnownChannelsCache(good.URL, time.Hour)
	if err := c.fetchOnce(context.Background()); err != nil {
		t.Fatalf("initial fetchOnce: %v", err)
	}
	first := c.load()
	if first == nil || len(first.Entries) == 0 {
		t.Fatal("first snapshot must be populated")
	}

	// Second server: always 500.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer bad.Close()

	// Re-point the cache to the failing upstream and fetch.
	c.url = bad.URL
	err := c.fetchOnce(context.Background())
	if err == nil {
		t.Fatal("expected fetchOnce to return error on 500")
	}
	after := c.load()
	if after == nil {
		t.Fatal("snapshot wiped after failed fetch — must be fail-soft")
	}
	if len(after.Entries) != len(first.Entries) {
		t.Errorf("snapshot entry count changed after failed fetch: was %d, now %d", len(first.Entries), len(after.Entries))
	}
	if c.failCount.Load() < 1 {
		t.Errorf("failCount = %d, want >=1", c.failCount.Load())
	}
}
