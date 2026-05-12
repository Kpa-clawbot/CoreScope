package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// TestIssue871_NoNullHashOrTimestamp verifies that /api/packets never returns
// packets with null/empty hash or null timestamp (issue #871).
func TestIssue871_NoNullHashOrTimestamp(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)

	// Insert bad legacy data: packet with empty hash
	now := time.Now().UTC().Add(-30 * time.Minute).Format(time.RFC3339)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('DEAD', '', ?, 1, 4, '{}')`, now)
	// Insert bad legacy data: packet with NULL first_seen (timestamp)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('BEEF', 'aa11bb22cc33dd44', NULL, 1, 4, '{}')`)

	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/packets?limit=200", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp struct {
		Packets []map[string]interface{} `json:"packets"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error: %v", err)
	}

	for i, p := range resp.Packets {
		hash, _ := p["hash"]
		ts, _ := p["timestamp"]
		if hash == nil || hash == "" {
			t.Errorf("packet[%d] has null/empty hash: %v", i, p)
		}
		if ts == nil || ts == "" {
			t.Errorf("packet[%d] has null/empty timestamp: %v", i, p)
		}
	}
}
