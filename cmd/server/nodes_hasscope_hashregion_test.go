package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
)

// TestHandleNodes_HasScopeAndHashRegion covers issue #1862's suggested
// filters: ?hasScope=true|false and ?hashRegion=#eu,#be on /api/nodes.
// Both read TransportedScopes off the bulk relay-info map (same source as
// the Scopes tab's "Repeaters Never Relaying Any Scope" section) rather
// than any DB column, so the test seeds that cache directly instead of
// reconstructing real path-hop parsing.
func TestHandleNodes_HasScopeAndHashRegion(t *testing.T) {
	srv, router := setupTestServer(t)
	if _, err := srv.db.conn.Exec(`DELETE FROM nodes`); err != nil {
		t.Fatalf("clear nodes: %v", err)
	}

	insert := func(pk, name, lastSeen string) {
		if _, err := srv.db.conn.Exec(`INSERT INTO nodes
			(public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
			VALUES (?, ?, 'repeater', 55.0, 10.0, ?, '2026-06-01T00:00:00Z', 1)`,
			pk, name, lastSeen); err != nil {
			t.Fatalf("insert %s: %v", name, err)
		}
	}
	insert("pk0000000000000a", "RelaysEU", "2026-06-07T00:00:00Z")
	insert("pk0000000000000b", "RelaysBoth", "2026-06-06T00:00:00Z")
	insert("pk0000000000000c", "NeverRelays", "2026-06-05T00:00:00Z")

	// Seed the relay cache directly (same style as
	// TestGetRepeaterRelayInfoMap_ServesStaleOnTTLExpiry) -- lookups are by
	// lowercase pubkey.
	srv.store.repeaterRelayCache = map[string]RepeaterRelayInfo{
		"pk0000000000000a": {TransportedScopes: []string{"#eu"}},
		"pk0000000000000b": {TransportedScopes: []string{"#eu", "#be"}},
		// pk0000000000000c intentionally absent -- zero-value on lookup.
	}

	fetchNames := func(query string) []string {
		req := httptest.NewRequest("GET", "/api/nodes?"+query, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("query %q: status=%d body=%s", query, w.Code, w.Body.String())
		}
		var resp struct {
			Nodes []map[string]interface{} `json:"nodes"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("query %q: decode: %v body=%s", query, err, w.Body.String())
		}
		names := make([]string, len(resp.Nodes))
		for i, n := range resp.Nodes {
			names[i], _ = n["name"].(string)
		}
		return names
	}

	assertNames := func(t *testing.T, query string, want ...string) {
		t.Helper()
		got := fetchNames(query)
		gotSet := map[string]bool{}
		for _, n := range got {
			gotSet[n] = true
		}
		if len(got) != len(want) {
			t.Errorf("query %q: got %v, want %v", query, got, want)
			return
		}
		for _, w := range want {
			if !gotSet[w] {
				t.Errorf("query %q: got %v, missing %q", query, got, w)
			}
		}
	}

	t.Run("hasScope=true", func(t *testing.T) {
		assertNames(t, "hasScope=true", "RelaysEU", "RelaysBoth")
	})
	t.Run("hasScope=false", func(t *testing.T) {
		assertNames(t, "hasScope=false", "NeverRelays")
	})
	t.Run("hashRegion single, with # and case-insensitive", func(t *testing.T) {
		assertNames(t, fmt.Sprintf("hashRegion=%s", "%23EU"), "RelaysEU", "RelaysBoth")
	})
	t.Run("hashRegion comma-separated OR match", func(t *testing.T) {
		assertNames(t, "hashRegion=be", "RelaysBoth")
	})
	t.Run("hasScope and hashRegion combine (AND)", func(t *testing.T) {
		assertNames(t, "hasScope=true&hashRegion=be", "RelaysBoth")
	})
	t.Run("no filter returns all", func(t *testing.T) {
		assertNames(t, "", "RelaysEU", "RelaysBoth", "NeverRelays")
	})
}
