package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// #2142: the scope audit lists only repeaters that answered a declared-regions
// request (17 of ~200 nodes on the instance that asked). The transport view
// lists every repeater seen forwarding in the window, with the region scopes
// it carried, its declared regions where known, and a scope filter (?scope=)
// that splits the fleet into carriers and non-carriers for rollout tracking.

var testFullPubkeyC = "cccc" + strings.Repeat("33", 30)

func getScopeTransport(t *testing.T, router *mux.Router, query string) ScopeTransportResponse {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/scope-audit?mode=transport"+query, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var got ScopeTransportResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got
}

func transportRow(t *testing.T, rows []ScopeTransportRow, pk string) ScopeTransportRow {
	t.Helper()
	for _, r := range rows {
		if r.PublicKey == pk {
			return r
		}
	}
	t.Fatalf("no row for %s in %d rows", pk[:8], len(rows))
	return ScopeTransportRow{}
}

func addRepeater(t *testing.T, srv *Server, pk, name string) {
	t.Helper()
	if _, err := srv.db.conn.Exec(`INSERT INTO nodes (public_key, name, role) VALUES (?, ?, 'repeater')
		ON CONFLICT(public_key) DO UPDATE SET name = excluded.name, role = 'repeater'`, pk, name); err != nil {
		t.Fatal(err)
	}
}

// seedTwoRepeaters: A declared "be" and carries #be; B never answered and
// carries #fr twice; C is a repeater with no traffic in the window.
func seedTwoRepeaters(t *testing.T, srv *Server) {
	t.Helper()
	now := time.Now().UTC()
	insertDeclared(t, srv, testFullPubkeyA, now.Format(time.RFC3339), "be", 0)
	addRepeater(t, srv, testFullPubkeyA, "Alpha")
	addRepeater(t, srv, testFullPubkeyB, "Bravo")
	addRepeater(t, srv, testFullPubkeyC, "Charlie")
	recent := now.Add(-time.Minute).Format(time.RFC3339)
	seedTransmissionRouteAt(t, srv.store, testFullPubkeyA[:4], scopeMatched("#be"), RouteFlood, recent)
	seedTransmissionRouteAt(t, srv.store, testFullPubkeyB[:4], scopeMatched("#fr"), RouteFlood, recent)
	seedTransmissionRouteAt(t, srv.store, testFullPubkeyB[:4], scopeMatched("#fr"), RouteFlood, recent)
}

func TestScopeTransport_ListsRepeatersThatNeverAnswered(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	seedTwoRepeaters(t, srv)
	got := getScopeTransport(t, router, "")
	if got.Mode != "transport" {
		t.Fatalf("mode = %q, want transport", got.Mode)
	}
	a := transportRow(t, got.Repeaters, testFullPubkeyA)
	if !a.Asked || len(a.DeclaredRegions) != 1 || a.DeclaredRegions[0] != "be" {
		t.Errorf("A: asked=%v declared=%v, want asked with [be]", a.Asked, a.DeclaredRegions)
	}
	if len(a.Transported) != 1 || a.Transported[0].Scope != "be" || a.Transported[0].Packets != 1 {
		t.Errorf("A: transported = %+v, want be x1", a.Transported)
	}
	if len(a.NotObserved) != 0 {
		t.Errorf("A: notObserved = %v, want none", a.NotObserved)
	}
	b := transportRow(t, got.Repeaters, testFullPubkeyB)
	if b.Asked || b.DeclaredRegions != nil || b.NotObserved != nil {
		t.Errorf("B: asked=%v declared=%v notObserved=%v, want not asked with null declared fields", b.Asked, b.DeclaredRegions, b.NotObserved)
	}
	if len(b.Transported) != 1 || b.Transported[0].Scope != "fr" || b.Transported[0].Packets != 2 {
		t.Errorf("B: transported = %+v, want fr x2", b.Transported)
	}
	if b.Name == nil || *b.Name != "Bravo" {
		t.Errorf("B: name = %v, want Bravo", b.Name)
	}
}

func TestScopeTransport_OmitsRepeatersNotSeenForwarding(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	seedTwoRepeaters(t, srv)
	for _, r := range getScopeTransport(t, router, "").Repeaters {
		if r.PublicKey == testFullPubkeyC {
			t.Fatalf("C forwarded nothing in the window and must not be listed: %+v", r)
		}
	}
}

func TestScopeTransport_ScopeFilterSplitsCarriers(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	seedTwoRepeaters(t, srv)
	for _, q := range []string{"&scope=fr", "&scope=%23fr", "&scope=FR"} {
		got := getScopeTransport(t, router, q)
		if got.Scope != "fr" {
			t.Errorf("%s: scope = %q, want normalised \"fr\"", q, got.Scope)
		}
		a := transportRow(t, got.Repeaters, testFullPubkeyA)
		b := transportRow(t, got.Repeaters, testFullPubkeyB)
		if a.CarriesScope == nil || *a.CarriesScope || b.CarriesScope == nil || !*b.CarriesScope {
			t.Errorf("%s: carriesScope A=%v B=%v, want A false, B true", q, a.CarriesScope, b.CarriesScope)
		}
		if got.Carrying == nil || *got.Carrying != 1 || got.NotCarry == nil || *got.NotCarry != 1 {
			t.Errorf("%s: carrying=%v notCarrying=%v, want 1 and 1", q, got.Carrying, got.NotCarry)
		}
	}
	plain := getScopeTransport(t, router, "")
	if plain.Scope != "" || plain.Carrying != nil || transportRow(t, plain.Repeaters, testFullPubkeyB).CarriesScope != nil {
		t.Error("without a scope filter, no carriesScope or counts may be reported")
	}
}

func TestScopeTransport_FiltersBlacklistedRepeater(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	seedTwoRepeaters(t, srv)
	srv.cfg.NodeBlacklist = []string{testFullPubkeyB}
	for _, r := range getScopeTransport(t, router, "").Repeaters {
		if r.PublicKey == testFullPubkeyB {
			t.Fatal("blacklisted repeater must be excluded")
		}
	}
}

func TestScopeAudit_RejectsUnknownMode(t *testing.T) {
	_, router := setupScopeAuditServer(t)
	req := httptest.NewRequest("GET", "/api/scope-audit?mode=bogus", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an unknown mode", w.Code)
	}
}

func TestScopeTransport_CachedSeparatelyFromDeclaredView(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	seedTwoRepeaters(t, srv)
	declared := getScopeAudit(t, router, "")
	transport := getScopeTransport(t, router, "")
	if len(declared.Repeaters) != 1 {
		t.Fatalf("declared view = %d rows, want only the repeater that answered", len(declared.Repeaters))
	}
	if len(transport.Repeaters) != 2 {
		t.Fatalf("transport view = %d rows after the declared view was cached, want 2", len(transport.Repeaters))
	}
}

// BenchmarkComputeScopeTransport: a fleet of 1,200 repeaters (#1975 quotes an
// instance with 1,179) and 30k flood transmissions in the last 24h, each
// forwarded along a 4-hop path of 3-byte prefixes, a third of them scoped to
// one of eight regions. Reports the response size too, as #2142 asks.
func BenchmarkComputeScopeTransport(b *testing.B) {
	log.SetOutput(io.Discard)
	b.Cleanup(func() { log.SetOutput(os.Stderr) })
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	for _, ddl := range []string{
		`CREATE TABLE transmissions (id INTEGER PRIMARY KEY AUTOINCREMENT, raw_hex TEXT NOT NULL, hash TEXT NOT NULL UNIQUE,
			first_seen TEXT NOT NULL, route_type INTEGER, payload_type INTEGER, code1 TEXT, code2 TEXT, scope_name TEXT)`,
		`CREATE INDEX idx_transmissions_first_seen ON transmissions(first_seen)`,
		`CREATE TABLE observations (id INTEGER PRIMARY KEY AUTOINCREMENT, transmission_id INTEGER NOT NULL, path_json TEXT, timestamp INTEGER NOT NULL)`,
		`CREATE INDEX idx_obs_tx ON observations(transmission_id)`,
		`CREATE TABLE nodes (public_key TEXT PRIMARY KEY, name TEXT, role TEXT, configured_scope TEXT, configured_scope_at TEXT)`,
	} {
		if _, err := conn.Exec(ddl); err != nil {
			b.Fatal(err)
		}
	}
	db := &DB{conn: conn}
	if err := db.detectSchema(context.Background(), conn); err != nil {
		b.Fatal(err)
	}
	const fleet = 1200
	pks := make([]string, fleet)
	conn.Exec("BEGIN")
	for i := range pks {
		pks[i] = fmt.Sprintf("%06x", i*13+0x100000) + strings.Repeat("ab", 29)
		scope := sql.NullString{}
		if i%7 == 0 {
			scope = sql.NullString{String: "be,be-van,*", Valid: true}
		}
		conn.Exec(`INSERT INTO nodes (public_key, name, role, configured_scope, configured_scope_at) VALUES (?, ?, 'repeater', ?, ?)`,
			pks[i], fmt.Sprintf("Repeater %d", i), scope, time.Now().UTC().Format(time.RFC3339))
	}
	regions := []string{"#be", "#be-van", "#be-ant", "#be-gnt", "#be-lge", "#nl", "#nl-ams", "#de"}
	now := time.Now().UTC()
	for i := 0; i < 30000; i++ {
		var scope interface{}
		route := RouteFlood
		if i%3 == 0 {
			route, scope = 0, regions[i%len(regions)]
		}
		res, _ := conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, code1, code2, scope_name)
			VALUES ('AA', ?, ?, ?, 1, '1234', '00', ?)`, fmt.Sprintf("bench%d", i), now.Add(-time.Duration(i)*2*time.Second).Format(time.RFC3339), route, scope)
		id, _ := res.LastInsertId()
		path := fmt.Sprintf(`["%s","%s","%s","%s"]`, strings.ToUpper(pks[i%fleet][:6]), strings.ToUpper(pks[(i*7)%fleet][:6]),
			strings.ToUpper(pks[(i*11)%fleet][:6]), strings.ToUpper(pks[(i*17)%fleet][:6]))
		conn.Exec(`INSERT INTO observations (transmission_id, path_json, timestamp) VALUES (?, ?, ?)`, id, path, now.Unix())
	}
	conn.Exec("COMMIT")
	cfg := &Config{Port: 3000}
	srv := NewServer(db, cfg, NewHub())
	srv.store = newTestStoreWithDB(b, db, cfg)
	since := now.Add(-24 * time.Hour).Format(time.RFC3339)
	b.ResetTimer()
	var resp *ScopeTransportResponse
	for i := 0; i < b.N; i++ {
		if resp, err = srv.computeScopeTransport("24h", since); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	body, _ := json.Marshal(resp)
	b.ReportMetric(float64(len(resp.Repeaters)), "rows")
	b.ReportMetric(float64(len(body))/1024, "response-KB")
}
