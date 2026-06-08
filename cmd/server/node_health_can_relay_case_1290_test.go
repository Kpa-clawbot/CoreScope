package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Issue #1290 (MAJOR-1, adversarial review of PR #1624) — regression guard.
// GetNonRelayObserverPubkeys() returns LOWER(id); the disambiguator
// (pm.nonRelay) also uses lowercase. GetNodeHealth previously used
// UPPERCASE for both insert and lookup which happens to work by symmetry,
// but any refactor that changes how pkt.ObserverID is normalized would
// silently break the badge. This test pins lowercase as the convention by
// seeding an observer.id with mixed-case packet ObserverID and asserting
// the listener badge is rendered for the matching observer in HeardBy.
func TestNodeHealth_CanRelayCaseInsensitive_Issue1290(t *testing.T) {
	srv, router := setupTestServer(t)

	// DB row: observer id is the canonical LOWERCASE pubkey with can_relay=0.
	const obsIDLower = "deadbeefcafe1290"
	const obsIDMixed = "DeadBeefCafe1290" // packet observer-id w/ mixed case
	const nodePubkey = "aabbccdd11223344" // seeded by seedTestData
	now := time.Now().UTC().Format(time.RFC3339)
	// The test fixture's observers table predates the can_relay migration;
	// add the column (matches ensureObserverCanRelayColumn in dbschema).
	if _, err := srv.store.db.conn.Exec(
		`ALTER TABLE observers ADD COLUMN can_relay INTEGER DEFAULT 1`); err != nil {
		t.Fatalf("add can_relay column: %v", err)
	}
	if _, err := srv.store.db.conn.Exec(
		`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count, can_relay)
		 VALUES (?, 'ListenerOnly', 'SJC', ?, '2026-01-01T00:00:00Z', 1, 0)`,
		obsIDLower, now); err != nil {
		t.Fatalf("seed observer: %v", err)
	}

	// In-memory packet with the MIXED-case observer id so the badge resolver
	// must lower-case both sides to match against the lower-cased pubkey set.
	snr := 7.0
	srv.store.mu.Lock()
	if srv.store.byNode == nil {
		srv.store.byNode = make(map[string][]*StoreTx)
	}
	srv.store.byNode[nodePubkey] = append(srv.store.byNode[nodePubkey], &StoreTx{
		Hash:             "1290casebadge00",
		FirstSeen:        now,
		SNR:              &snr,
		ObservationCount: 1,
		ObserverID:       obsIDMixed,
		ObserverName:     "ListenerOnly",
	})
	srv.store.mu.Unlock()

	req := httptest.NewRequest(http.MethodGet, "/api/nodes/"+nodePubkey+"/health", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d (body: %s)", w.Code, w.Body.String())
	}

	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("json: %v", err)
	}
	obs, ok := body["observers"].([]interface{})
	if !ok {
		t.Fatalf("expected observers array, got %T", body["observers"])
	}
	var found bool
	for _, raw := range obs {
		row, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if row["observer_id"] != obsIDMixed {
			continue
		}
		found = true
		if row["can_relay"] != false {
			t.Errorf("listener observer with can_relay=0 + mixed-case ObserverID: expected can_relay=false, got %v", row["can_relay"])
		}
	}
	if !found {
		t.Fatalf("did not find observer %q in HeardBy rows; got %v", obsIDMixed, obs)
	}
}
