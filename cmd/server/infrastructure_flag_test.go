package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

// Infrastructure flag (#infra): operator-curated column written by
// scripts/set-infra.sh, surfaced read-only by the server as the
// `infrastructure` boolean on node responses. These tests pin:
//  1. detectSchema PRAGMA-detects the column (hasInfrastructure).
//  2. GetNodes / GetNodeByPubkey / SearchNodes expose the flag.
//  3. Old-DB compat: without the column the field is present and false
//     (stable API shape, no scan error).

// addInfrastructureColumn upgrades the test schema to post-migration
// shape and re-detects, mirroring prod (ingestor migrates, server
// PRAGMA-detects at startup).
func addInfrastructureColumn(t *testing.T, db *DB) {
	t.Helper()
	if _, err := db.conn.Exec(`ALTER TABLE nodes ADD COLUMN infrastructure INTEGER NOT NULL DEFAULT 0`); err != nil {
		t.Fatal(err)
	}
	db.detectSchema()
	if !db.hasInfrastructure {
		t.Fatal("detectSchema should set hasInfrastructure after ALTER")
	}
}

func TestInfrastructureFlagExposedOnNodeReads(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	addInfrastructureColumn(t, db)

	recent := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	if _, err := db.conn.Exec(`INSERT INTO nodes (public_key, name, role, last_seen, first_seen, infrastructure)
		VALUES ('aabb00000000000000000000000000aa', 'Tower', 'repeater', ?, ?, 1),
		       ('ccdd00000000000000000000000000cc', 'Valley', 'companion', ?, ?, 0)`,
		recent, recent, recent, recent); err != nil {
		t.Fatal(err)
	}

	nodes, _, _, err := db.GetNodes(10, 0, "", "", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, n := range nodes {
		infra, ok := n["infrastructure"].(bool)
		if !ok {
			t.Fatalf("node %v: infrastructure should be a bool, got %T", n["public_key"], n["infrastructure"])
		}
		got[n["public_key"].(string)] = infra
	}
	if !got["aabb00000000000000000000000000aa"] {
		t.Error("GetNodes: marked node should have infrastructure=true")
	}
	if got["ccdd00000000000000000000000000cc"] {
		t.Error("GetNodes: unmarked node should have infrastructure=false")
	}

	// Detail read path.
	node, err := db.GetNodeByPubkey("aabb00000000000000000000000000aa")
	if err != nil {
		t.Fatal(err)
	}
	if node == nil || node["infrastructure"] != true {
		t.Errorf("GetNodeByPubkey: want infrastructure=true, got %v", node["infrastructure"])
	}

	// Search read path.
	results, err := db.SearchNodes("Tower", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0]["infrastructure"] != true {
		t.Errorf("SearchNodes: want 1 result with infrastructure=true, got %v", results)
	}
}

// TestBulkHealthNodesParam covers the ?nodes= exact-pubkey scope on
// /api/nodes/bulk-health (#infra): the infrastructure page fetches health
// for its curated set in one call. Asserts exact scoping (only requested
// nodes return), case-insensitive matching, recency-window bypass (an
// ancient node still returns when requested), the BulkHealthMax-derived
// cap, and that the blacklist filter still applies.
func TestBulkHealthNodesParam(t *testing.T) {
	srv, router := setupTestServer(t)

	insert := func(pk, name, lastSeen string) {
		t.Helper()
		if _, err := srv.db.conn.Exec(`INSERT INTO nodes
			(public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
			VALUES (?, ?, 'repeater', 1, 1, ?, ?, 5)`, pk, name, lastSeen, lastSeen); err != nil {
			t.Fatalf("insert %s: %v", pk, err)
		}
	}
	insert("aaaa000011112222", "Infra One", "2026-07-01T00:00:00Z")
	insert("bbbb000011112222", "Infra Two", "2020-01-01T00:00:00Z") // ancient — outside any recency window
	insert("cccc000011112222", "Not Requested", "2026-07-02T00:00:00Z")

	get := func(url string) []map[string]interface{} {
		t.Helper()
		req := httptest.NewRequest("GET", url, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("GET %s: status=%d body=%s", url, w.Code, w.Body.String())
		}
		var arr []map[string]interface{}
		if err := json.Unmarshal(w.Body.Bytes(), &arr); err != nil {
			t.Fatalf("GET %s: bad JSON: %v", url, err)
		}
		return arr
	}
	keys := func(arr []map[string]interface{}) map[string]bool {
		out := map[string]bool{}
		for _, e := range arr {
			if pk, ok := e["public_key"].(string); ok {
				out[pk] = true
			}
		}
		return out
	}

	// Exact scope + case-insensitive + whitespace tolerated + ancient node included.
	got := keys(get("/api/nodes/bulk-health?limit=50&nodes=AAAA000011112222,%20bbbb000011112222"))
	if len(got) != 2 || !got["aaaa000011112222"] || !got["bbbb000011112222"] {
		t.Errorf("nodes= scope: want exactly {aaaa…, bbbb…}, got %v", got)
	}
	if got["cccc000011112222"] {
		t.Error("nodes= scope leaked an unrequested node")
	}

	// Cap: limit=1 truncates the requested set.
	if arr := get("/api/nodes/bulk-health?limit=1&nodes=aaaa000011112222,bbbb000011112222"); len(arr) != 1 {
		t.Errorf("nodes= cap: want 1 result at limit=1, got %d", len(arr))
	}

	// Blacklist still filters scoped results.
	srv.cfg.NodeBlacklist = []string{"aaaa000011112222"}
	srv.cfg.SetNodeBlacklist(srv.cfg.NodeBlacklist)
	got = keys(get("/api/nodes/bulk-health?limit=50&nodes=aaaa000011112222,bbbb000011112222"))
	if got["aaaa000011112222"] || !got["bbbb000011112222"] {
		t.Errorf("nodes= + blacklist: want only bbbb…, got %v", got)
	}
}

// TestInfrastructureNodesEndpoint covers GET /api/nodes/infrastructure:
// returns exactly the flagged set (indexed WHERE, not a client-side page
// scan), carries the /api/nodes enrichment fields for repeaters, and
// respects blacklist + hidden-prefix filtering.
func TestInfrastructureNodesEndpoint(t *testing.T) {
	srv, router := setupTestServer(t)

	if _, err := srv.db.conn.Exec(`ALTER TABLE nodes ADD COLUMN infrastructure INTEGER NOT NULL DEFAULT 0`); err != nil {
		t.Fatal(err)
	}
	// setupTestDB's shared fixture doesn't include inactive_nodes (most
	// tests don't need it); GetInfrastructureNodes UNIONs it in, so this
	// test needs the table to exist even though this case leaves it empty.
	if _, err := srv.db.conn.Exec(`CREATE TABLE inactive_nodes (
		public_key TEXT PRIMARY KEY, name TEXT, role TEXT, lat REAL, lon REAL,
		last_seen TEXT, first_seen TEXT, advert_count INTEGER DEFAULT 0,
		battery_mv INTEGER, temperature_c REAL, foreign_advert INTEGER DEFAULT 0,
		infrastructure INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	srv.db.detectSchema()

	recent := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	if _, err := srv.db.conn.Exec(`INSERT INTO nodes (public_key, name, role, last_seen, first_seen, infrastructure)
		VALUES ('11aa000011112222000011112222aabb', 'Tower', 'repeater', ?, ?, 1),
		       ('22bb000011112222000011112222aabb', 'Peak', 'repeater', ?, ?, 1),
		       ('33cc000011112222000011112222aabb', '🚫 hidden peak', 'repeater', ?, ?, 1),
		       ('44dd000011112222000011112222aabb', 'Regular', 'companion', ?, ?, 0)`,
		recent, recent, recent, recent, recent, recent, recent, recent); err != nil {
		t.Fatal(err)
	}

	get := func() []map[string]interface{} {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/nodes/infrastructure", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		var resp struct {
			Nodes []map[string]interface{} `json:"nodes"`
			Total int                      `json:"total"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("bad JSON: %v", err)
		}
		if resp.Total != len(resp.Nodes) {
			t.Errorf("total=%d != len(nodes)=%d", resp.Total, len(resp.Nodes))
		}
		return resp.Nodes
	}

	nodes := get()
	names := map[string]bool{}
	for _, n := range nodes {
		names[n["name"].(string)] = true
		if n["infrastructure"] != true {
			t.Errorf("node %v: endpoint returned a non-flagged node", n["name"])
		}
		// Enrichment parity with /api/nodes: repeater rows carry relay fields.
		if n["role"] == "repeater" {
			if _, ok := n["relay_active"]; !ok {
				t.Errorf("node %v: missing relay enrichment (relay_active)", n["name"])
			}
			if _, ok := n["traffic_share_score"]; !ok {
				t.Errorf("node %v: missing traffic_share_score enrichment", n["name"])
			}
		}
	}
	if !names["Tower"] || !names["Peak"] {
		t.Errorf("expected Tower + Peak in response, got %v", names)
	}
	if names["Regular"] {
		t.Error("unflagged node leaked into /api/nodes/infrastructure")
	}

	// Hidden-prefix filter.
	srv.cfg.SetHiddenNamePrefixes([]string{"🚫"})
	nodes = get()
	for _, n := range nodes {
		if name, _ := n["name"].(string); name == "🚫 hidden peak" {
			t.Error("hidden-prefix node leaked into /api/nodes/infrastructure")
		}
	}

	// Blacklist filter.
	srv.cfg.NodeBlacklist = []string{"11aa000011112222000011112222aabb"}
	srv.cfg.SetNodeBlacklist(srv.cfg.NodeBlacklist)
	nodes = get()
	for _, n := range nodes {
		if n["public_key"] == "11aa000011112222000011112222aabb" {
			t.Error("blacklisted node leaked into /api/nodes/infrastructure")
		}
	}
}

// TestInfrastructureNodesEndpointIncludesInactive covers the gap found
// while building the admin-panel infra toggle: a node's infrastructure
// flag is operator-curated and independent of recent activity, so a
// retention-moved (inactive_nodes) infra node must still show up here —
// otherwise an admin who flags a node that later goes stale can never
// find it again in their own management UI to un-flag it.
func TestInfrastructureNodesEndpointIncludesInactive(t *testing.T) {
	srv, router := setupTestServer(t)

	if _, err := srv.db.conn.Exec(`ALTER TABLE nodes ADD COLUMN infrastructure INTEGER NOT NULL DEFAULT 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.db.conn.Exec(`CREATE TABLE inactive_nodes (
		public_key TEXT PRIMARY KEY, name TEXT, role TEXT, lat REAL, lon REAL,
		last_seen TEXT, first_seen TEXT, advert_count INTEGER DEFAULT 0,
		battery_mv INTEGER, temperature_c REAL, foreign_advert INTEGER DEFAULT 0,
		infrastructure INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	srv.db.detectSchema()

	old := time.Now().UTC().Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	if _, err := srv.db.conn.Exec(`INSERT INTO inactive_nodes (public_key, name, role, last_seen, first_seen, infrastructure)
		VALUES ('55ee000011112222000011112222aabb', 'Gone Quiet Tower', 'repeater', ?, ?, 1)`,
		old, old); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/nodes/infrastructure", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Nodes []map[string]interface{} `json:"nodes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	found := false
	for _, n := range resp.Nodes {
		if n["public_key"] == "55ee000011112222000011112222aabb" {
			found = true
			if n["infrastructure"] != true {
				t.Errorf("expected infrastructure=true, got %v", n["infrastructure"])
			}
		}
	}
	if !found {
		t.Error("infra-flagged node in inactive_nodes should still appear in /api/nodes/infrastructure")
	}
}

// TestInfrastructureNodesEndpointOldDB: pre-migration DB (no column) must
// yield an empty list, not a SQL error.
func TestInfrastructureNodesEndpointOldDB(t *testing.T) {
	srv, router := setupTestServer(t)
	if srv.db.hasInfrastructure {
		t.Fatal("test premise broken: setupTestServer should not have the infrastructure column")
	}
	req := httptest.NewRequest("GET", "/api/nodes/infrastructure", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Nodes []map[string]interface{} `json:"nodes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if len(resp.Nodes) != 0 {
		t.Errorf("pre-migration DB should return empty list, got %d", len(resp.Nodes))
	}
}

func TestInfrastructureFlagOldDBDefaultsFalse(t *testing.T) {
	db := setupTestDB(t) // schema WITHOUT the infrastructure column
	defer db.Close()
	if db.hasInfrastructure {
		t.Fatal("test premise broken: setupTestDB should not have the infrastructure column")
	}
	seedTestData(t, db)

	nodes, _, _, err := db.GetNodes(10, 0, "", "", "", "", "", "")
	if err != nil {
		t.Fatalf("GetNodes on pre-migration DB: %v", err)
	}
	if len(nodes) == 0 {
		t.Fatal("expected seeded nodes")
	}
	for _, n := range nodes {
		if n["infrastructure"] != false {
			t.Errorf("node %v: pre-migration DB should expose infrastructure=false, got %v",
				n["public_key"], n["infrastructure"])
		}
	}
}
