package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// TestHandleNodeHopAnalytics covers upstream issue #1812: hop-count is the
// target node's own 0-based index within a transmission's resolved relay
// path (NOT path length to the reporting observer, and NOT +1 — matches
// MeshCore firmware's getPathHashCount() vs flood_max comparison), broken
// down by which flood.max* firmware knob applies.
func TestHandleNodeHopAnalytics(t *testing.T) {
	db := setupTestDB(t)
	// setupTestDB's schema predates #899's scope_name column; add it and
	// flip the detector flag the same way TestGetScopeStats does, so
	// store.Load() actually reads it into StoreTx.ScopeName below.
	if _, err := db.conn.Exec(`ALTER TABLE transmissions ADD COLUMN scope_name TEXT DEFAULT NULL`); err != nil {
		t.Fatalf("add scope_name column: %v", err)
	}
	db.hasScopeName = true

	recent := time.Now().Add(-1 * time.Hour).Format(time.RFC3339)
	recentEpoch := time.Now().Add(-1 * time.Hour).Unix()
	old := time.Now().Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	oldEpoch := time.Now().Add(-30 * 24 * time.Hour).Unix()

	// Resolved-path/path_json arrays list only RELAY hops a packet passed
	// through (never the originator) -- targetPK's 2-char raw prefix "aa"
	// must appear in path_json at the SAME array position as targetPK
	// appears in resolved_path for byPathHop indexing to find these as
	// candidates at all.
	targetPK := "aabb1111aaaabbbb"
	relayXPK := "cc001111ccccdddd"

	db.conn.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, 'Target', 'repeater', 0, 0, ?, '2026-01-01', 1)`, targetPK, recent)

	// tx1: TRANSPORT_FLOOD (scoped), not an advert. Target is the SECOND
	// relay (relayX forwarded it first) -> hops=1, "flood".
	db.conn.Exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, scope_name)
		VALUES (1, 'AA', 'hash_flood', ?, 0, 5, '#dk')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
		VALUES (1, NULL, '["cc","aa"]', ?, ?)`, recentEpoch, `["`+relayXPK+`","`+targetPK+`"]`)

	// tx2: FLOOD (unscoped) ADVERT -- payload type wins over route, so this
	// must classify as "flood_advert" not "flood_unscoped". Target is the
	// FIRST relay to touch it (empty path when it evaluated) -> hops=0.
	db.conn.Exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, scope_name)
		VALUES (2, 'BB', 'hash_advert', ?, 1, 4, '')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
		VALUES (2, NULL, '["aa"]', ?, ?)`, recentEpoch, `["`+targetPK+`"]`)

	// tx3: plain FLOOD (unscoped), not an advert -> "flood_unscoped", hops=0.
	db.conn.Exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, scope_name)
		VALUES (3, 'CC', 'hash_unscoped', ?, 1, 5, '')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
		VALUES (3, NULL, '["aa"]', ?, ?)`, recentEpoch, `["`+targetPK+`"]`)

	// tx4: DIRECT -- "direct", hops=1 (target is the second hop).
	db.conn.Exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, scope_name)
		VALUES (4, 'DD', 'hash_direct', ?, 2, 5, '')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
		VALUES (4, NULL, '["cc","aa"]', ?, ?)`, recentEpoch, `["`+relayXPK+`","`+targetPK+`"]`)

	// tx5: no resolved_path at all -- must be excluded entirely (no
	// reliable hop index to report, not guessed).
	db.conn.Exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, scope_name)
		VALUES (5, 'EE', 'hash_noresolve', ?, 0, 5, '#dk')`, recent)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
		VALUES (5, NULL, '["aa"]', ?, NULL)`, recentEpoch)

	// tx6: within the resolved path but outside the default 7-day window --
	// must be excluded by the days filter.
	db.conn.Exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, scope_name)
		VALUES (6, 'FF', 'hash_old', ?, 0, 5, '#dk')`, old)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
		VALUES (6, NULL, '["aa"]', ?, ?)`, oldEpoch, `["`+targetPK+`"]`)

	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/nodes/"+targetPK+"/hop_analytics?days=7", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp NodeHopAnalyticsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	byHash := map[string]HopAnalyticsPacket{}
	for _, p := range resp.Packets {
		byHash[p.Hash] = p
	}

	if len(resp.Packets) != 4 {
		t.Fatalf("expected 4 packets (tx5 unresolved + tx6 out-of-window excluded), got %d: %+v", len(resp.Packets), resp.Packets)
	}

	if p, ok := byHash["hash_flood"]; !ok {
		t.Error("hash_flood missing")
	} else if p.Hops != 1 || p.Transport != "flood" || !p.Scoped {
		t.Errorf("hash_flood = %+v, want hops=1 transport=flood scoped=true", p)
	}

	if p, ok := byHash["hash_advert"]; !ok {
		t.Error("hash_advert missing")
	} else if p.Hops != 0 || p.Transport != "flood_advert" || p.Scoped {
		t.Errorf("hash_advert = %+v, want hops=0 transport=flood_advert scoped=false", p)
	}

	if p, ok := byHash["hash_unscoped"]; !ok {
		t.Error("hash_unscoped missing")
	} else if p.Hops != 0 || p.Transport != "flood_unscoped" || p.Scoped {
		t.Errorf("hash_unscoped = %+v, want hops=0 transport=flood_unscoped scoped=false", p)
	}

	if p, ok := byHash["hash_direct"]; !ok {
		t.Error("hash_direct missing")
	} else if p.Hops != 1 || p.Transport != "direct" || p.Scoped {
		t.Errorf("hash_direct = %+v, want hops=1 transport=direct scoped=false", p)
	}

	if _, ok := byHash["hash_noresolve"]; ok {
		t.Error("hash_noresolve should be excluded (no resolved_path)")
	}
	if _, ok := byHash["hash_old"]; ok {
		t.Error("hash_old should be excluded (outside 7-day window)")
	}
}

// TestHandleNodeHopAnalytics_UnknownNode returns 404, matching the sibling
// /analytics endpoint's behavior for a pubkey with no node row.
func TestHandleNodeHopAnalytics_UnknownNode(t *testing.T) {
	db := setupTestDB(t)
	cfg := &Config{Port: 3000}
	hub := NewHub()
	srv := NewServer(db, cfg, hub)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	req := httptest.NewRequest("GET", "/api/nodes/deadbeef00000000/hop_analytics", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", w.Code, w.Body.String())
	}
}
