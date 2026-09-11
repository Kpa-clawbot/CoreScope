package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

// TestHandleNodesOmitsScopeConfigStateWithoutASource is the difference between
// "nobody has answered" and "this database cannot hold an answer". The stock
// test schema has neither configured_scope nor node_declared_regions, so every
// repeater would otherwise be labelled "none" — a positive claim about a
// network, made from a database that structurally cannot carry the fact.
func TestHandleNodesOmitsScopeConfigStateWithoutASource(t *testing.T) {
	srv, router := setupTestServer(t)
	if srv.db.hasConfiguredScope || srv.db.hasDeclaredRegionsTable {
		t.Fatal("fixture has a declared-regions source: this test would prove nothing")
	}
	if _, err := srv.db.conn.Exec(`INSERT INTO nodes
		(public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES ('PK_NOSOURCE', 'rp', 'repeater', 51.0, 4.0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1)`,
	); err != nil {
		t.Fatal(err)
	}

	nodes := nodesByPubkey(t, router, "?limit=200")

	if v, present := nodes["PK_NOSOURCE"]["scope_config_state"]; present {
		t.Errorf("repeater carries scope_config_state = %v on a schema with no declared-regions source, want the field absent", v)
	}
}

// TestHandleNodesMatchesDeclaredTargetCaseInsensitively pins the join the
// Scope Audit already makes case-insensitively (scope_audit.go lowercases the
// declared target before joining). node_declared_regions is filled by an
// external collector whose casing this repo does not control, and a case-only
// mismatch would put the audit and the map in disagreement about the same node.
func TestHandleNodesMatchesDeclaredTargetCaseInsensitively(t *testing.T) {
	srv, router := setupScopeConfigStateServer(t)

	if _, err := srv.db.conn.Exec(`INSERT INTO nodes
		(public_key, name, role, lat, lon, last_seen, first_seen, advert_count, configured_scope, configured_scope_at)
		VALUES ('pk_mixedcase', 'rp', 'repeater', 51.0, 4.0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1, NULL, NULL)`,
	); err != nil {
		t.Fatal(err)
	}
	// The declared answer names the same node in a different case.
	if _, err := srv.db.conn.Exec(`INSERT INTO nodes
		(public_key, configured_scope, configured_scope_at)
		VALUES ('PK_MIXEDCASE', 'be,*', '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatal(err)
	}

	nodes := nodesByPubkey(t, router, "?limit=200")

	if got := nodes["pk_mixedcase"]["scope_config_state"]; got != ScopeConfigFull {
		t.Errorf("scope_config_state = %v for a declared answer in the other case, want %q", got, ScopeConfigFull)
	}
}

// TestDeclaredRegionsLookupRunsOncePerWindow is the perf assertion AGENTS.md
// asks for: the claim is that scope_config_state costs /api/nodes one cached
// DB round-trip, not one per request, and the enforceable form of that claim is
// the query count. Twenty concurrent requests inside the TTL window must
// produce exactly one execution — one for the cold start, none after.
//
// What this pins is the cache, not the lock: with the cache short-circuit
// removed it reports 6 to 9 executions, and it passes either way on the older
// shape that held the mutex across the query. The lock fix is argued from the
// code, not from this test — see the singleflight comment in
// scope_config_state.go.
func TestDeclaredRegionsLookupRunsOncePerWindow(t *testing.T) {
	srv, router := setupScopeConfigStateServer(t)

	if _, err := srv.db.conn.Exec(`INSERT INTO nodes
		(public_key, name, role, lat, lon, last_seen, first_seen, advert_count, configured_scope, configured_scope_at)
		VALUES ('PK_PERF', 'rp', 'repeater', 51.0, 4.0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1, 'be,*', '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatal(err)
	}

	atomic.StoreInt64(&srv.declaredRegionsQueries, 0)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/api/nodes?limit=200", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Errorf("status = %d, want 200", w.Code)
			}
		}()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&srv.declaredRegionsQueries); got != 1 {
		t.Errorf("declared-regions query ran %d times for 20 requests inside the TTL, want 1", got)
	}
	// And the field is still correct, so the cache is not just cheap.
	nodes := nodesByPubkey(t, router, "?limit=200")
	if got := nodes["PK_PERF"]["scope_config_state"]; got != ScopeConfigFull {
		t.Errorf("scope_config_state = %v, want %q", got, ScopeConfigFull)
	}
}

// TestHandleNodesReportsObservedForUndeclaredForwarder pins the half of the
// field that does not come from a declared answer. A repeater that never
// answered but has been seen carrying scoped traffic must read "observed", not
// "none" — the difference between "has a region configured, we just have not
// asked which" and "we have nothing at all", which is the distinction the map
// colours by.
//
// The store is built after the rows are seeded, because TransportedScopes is
// accumulated at load time from transmissions.scope_name joined to the path
// hops, not read per request.
func TestHandleNodesReportsObservedForUndeclaredForwarder(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)

	// The columns the two halves of the field read, on a schema old enough to
	// have neither.
	for _, stmt := range []string{
		`ALTER TABLE nodes ADD COLUMN configured_scope TEXT`,
		`ALTER TABLE nodes ADD COLUMN configured_scope_at TEXT`,
		`ALTER TABLE transmissions ADD COLUMN scope_name TEXT`,
	} {
		if _, err := db.conn.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}

	// 64-hex pubkey: the path-hop join matches a truncated hop against the
	// node's own pubkey prefix, so a short fixture key would match nothing.
	const pk = "a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f90"
	if _, err := db.conn.Exec(`INSERT INTO nodes
		(public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, 'silent-forwarder', 'repeater', 51.0, 4.0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1)`, pk,
	); err != nil {
		t.Fatal(err)
	}
	res, err := db.conn.Exec(
		`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json, scope_name)
		 VALUES ('AA', 'observed-scope-hash', ?, 0, 1, 1, '{}', '#be')`,
		time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatal(err)
	}
	txID, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	// The hop is the full pubkey, not a truncated wire hop. TransportedScopes
	// is gated on full-pubkey attribution (#1902 — a 1-byte hop cannot prove
	// which of the nodes sharing that byte carried the packet), and in
	// production the full key is produced by hop resolution rather than read
	// off the wire. Seeding the resolved form exercises the same index entry
	// that resolution writes. The decoder uppercases hops, so the fixture does
	// too, which also pins the lower-casing in addTxToPathHopIndex.
	if _, err := db.conn.Exec(
		`INSERT INTO observations (transmission_id, observer_idx, direction, snr, rssi, score, path_json, timestamp)
		 VALUES (?, 0, 'rx', 1.0, -100, 0, ?, ?)`,
		txID, `["`+strings.ToUpper(pk)+`"]`, time.Now().Unix(),
	); err != nil {
		t.Fatal(err)
	}

	if err := db.detectSchema(context.Background(), db.conn); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{Port: 3000}
	srv := NewServer(db, cfg, NewHub())
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	if !store.WaitIndexesReady(5 * time.Second) {
		t.Fatal("background indexes never became ready")
	}
	srv.store = store
	router := mux.NewRouter()
	srv.RegisterRoutes(router)

	nodes := nodesByPubkey(t, router, "?limit=200")
	node := nodes[pk]
	if node == nil {
		t.Fatalf("seeded repeater missing from the response")
	}
	if node["transported_scopes"] == nil {
		t.Fatalf("fixture did not produce transported_scopes; the test would prove nothing (got %v)", node)
	}
	if got := node["scope_config_state"]; got != ScopeConfigObserved {
		t.Errorf("scope_config_state = %v for a never-asked repeater carrying scoped traffic, want %q", got, ScopeConfigObserved)
	}
}

// TestScopeAuditAndNodesAgreeEndToEnd is the guard the unit-level agreement
// test cannot be: it drives the two real handlers over the same stored answer
// and compares what each page would show. The unit test compares against a
// copy of the audit's parse loop living in the test file, so reverting the
// production fix in scope_audit.go leaves it green — which is the exact
// regression this round was opened for.
func TestScopeAuditAndNodesAgreeEndToEnd(t *testing.T) {
	srv, router := setupScopeConfigStateServer(t)
	// /api/scope-audit reads transmissions.scope_name; the stock test schema
	// predates it and the handler fails without it.
	if _, err := srv.db.conn.Exec(`ALTER TABLE transmissions ADD COLUMN scope_name TEXT`); err != nil {
		t.Fatal(err)
	}
	if err := srv.db.detectSchema(context.Background(), srv.db.conn); err != nil {
		t.Fatal(err)
	}

	cases := map[string]string{
		"PK_AGREE_HASHSTAR":  "#be,#*",
		"PK_AGREE_BARESTAR":  "be,*",
		"PK_AGREE_STARONLY":  "#*",
		"PK_AGREE_NAMEDONLY": "#be",
		"PK_AGREE_EMPTY":     "",
	}
	for pk, csv := range cases {
		if _, err := srv.db.conn.Exec(`INSERT INTO nodes
			(public_key, name, role, lat, lon, last_seen, first_seen, advert_count, configured_scope, configured_scope_at)
			VALUES (?, ?, 'repeater', 51.0, 4.0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1, ?, '2026-01-01T00:00:00Z')`,
			pk, "rp-"+pk, csv,
		); err != nil {
			t.Fatal(err)
		}
	}

	audit := getScopeAudit(t, router, "?window=24h")
	// The audit lowercases the declared target before joining (scope_audit.go),
	// so its PublicKey comes back lowercased whatever the collector stored.
	auditState := map[string]string{}
	for _, row := range audit.Repeaters {
		auditState[strings.ToLower(row.PublicKey)] = row.ConfigState
	}
	nodes := nodesByPubkey(t, router, "?limit=200")

	for pk, csv := range cases {
		got, ok := auditState[strings.ToLower(pk)]
		if !ok {
			t.Errorf("%s (%q): the scope audit does not list the repeater at all", pk, csv)
			continue
		}
		want := nodes[pk]["scope_config_state"]
		if got != want {
			t.Errorf("regions_csv %q: the scope audit page shows %q, the map shows %v", csv, got, want)
		}
	}
}
