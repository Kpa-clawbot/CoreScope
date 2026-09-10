package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
)

// setupScopeConfigStateServer is setupTestServer plus the declared-regions
// column. The stock test schema predates configured_scope, so without this
// the DB reports hasConfiguredScope == false and every node would classify as
// "never asked" — the fixture would agree with a broken implementation.
func setupScopeConfigStateServer(t *testing.T) (*Server, *mux.Router) {
	t.Helper()
	srv, router := setupTestServer(t)
	for _, stmt := range []string{
		`ALTER TABLE nodes ADD COLUMN configured_scope TEXT`,
		`ALTER TABLE nodes ADD COLUMN configured_scope_at TEXT`,
	} {
		if _, err := srv.db.conn.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := srv.db.detectSchema(context.Background(), srv.db.conn); err != nil {
		t.Fatal(err)
	}
	if !srv.db.hasConfiguredScope {
		t.Fatal("hasConfiguredScope is false after adding the column: the fixture would test nothing")
	}
	return srv, router
}

func nodesByPubkey(t *testing.T, router *mux.Router, query string) map[string]map[string]interface{} {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/nodes"+query, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Nodes []map[string]interface{} `json:"nodes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	out := map[string]map[string]interface{}{}
	for _, n := range resp.Nodes {
		pk, _ := n["public_key"].(string)
		out[pk] = n
	}
	return out
}

// TestHandleNodesExposesScopeConfigState pins the field the map colours by
// (#2001): a repeater that has answered a declared-regions request carries
// that answer's state, and one that never answered carries "none" rather
// than being silently indistinguishable from a fully configured node.
func TestHandleNodesExposesScopeConfigState(t *testing.T) {
	srv, router := setupScopeConfigStateServer(t)

	if _, err := srv.db.conn.Exec(`INSERT INTO nodes
		(public_key, name, role, lat, lon, last_seen, first_seen, advert_count, configured_scope, configured_scope_at)
		VALUES
		('PK_DECLARED', 'declared-rp', 'repeater', 51.0, 4.0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1, 'be,*', '2026-01-01T00:00:00Z'),
		('PK_SILENT',   'silent-rp',   'repeater', 51.1, 4.1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1, NULL, NULL)`,
	); err != nil {
		t.Fatal(err)
	}

	nodes := nodesByPubkey(t, router, "?limit=200")

	if got := nodes["PK_DECLARED"]["scope_config_state"]; got != ScopeConfigFull {
		t.Errorf("declared repeater scope_config_state = %v, want %q", got, ScopeConfigFull)
	}
	if got := nodes["PK_SILENT"]["scope_config_state"]; got != ScopeConfigNone {
		t.Errorf("never-asked repeater scope_config_state = %v, want %q", got, ScopeConfigNone)
	}
}

// TestHandleNodesOmitsScopeConfigStateForNonForwarders keeps the field on the
// roles that forward. A companion neither forwards nor answers a
// declared-regions request, so classifying it would state something we have
// no basis for.
func TestHandleNodesOmitsScopeConfigStateForNonForwarders(t *testing.T) {
	srv, router := setupScopeConfigStateServer(t)

	if _, err := srv.db.conn.Exec(`INSERT INTO nodes
		(public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES ('PK_COMPANION', 'phone', 'companion', 51.2, 4.2, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1)`,
	); err != nil {
		t.Fatal(err)
	}

	nodes := nodesByPubkey(t, router, "?limit=200")

	if _, present := nodes["PK_COMPANION"]["scope_config_state"]; present {
		t.Errorf("companion carries scope_config_state = %v, want the field absent",
			nodes["PK_COMPANION"]["scope_config_state"])
	}
}

// TestHandleNodeDetailExposesScopeConfigState keeps the single-node endpoint
// from drifting away from the list endpoint: the node page and the map must
// not disagree about a repeater's scope state.
func TestHandleNodeDetailExposesScopeConfigState(t *testing.T) {
	srv, router := setupScopeConfigStateServer(t)

	if _, err := srv.db.conn.Exec(`INSERT INTO nodes
		(public_key, name, role, lat, lon, last_seen, first_seen, advert_count, configured_scope, configured_scope_at)
		VALUES ('PK_DETAIL', 'detail-rp', 'repeater', 51.3, 4.3, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1, 'be', '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/api/nodes/PK_DETAIL", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	// The detail endpoint wraps the node: {"node": {...}, "recentAdverts": [...]}.
	var resp struct {
		Node map[string]interface{} `json:"node"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := resp.Node["scope_config_state"]; got != ScopeConfigNoUnscoped {
		t.Errorf("scope_config_state = %v, want %q", got, ScopeConfigNoUnscoped)
	}
}
