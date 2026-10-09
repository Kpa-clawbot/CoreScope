package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// #2142: the scope audit lists only repeaters that answered a declared-regions
// request (17 of ~200 nodes on the instance that asked). The transport view
// lists every repeater seen forwarding in the window, with the region scopes
// it carried, its declared regions where known, and a region filter that
// splits the fleet into carriers and non-carriers for rollout tracking.

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

func TestScopeTransport_RegionFilterSplitsCarriers(t *testing.T) {
	srv, router := setupScopeAuditServer(t)
	seedTwoRepeaters(t, srv)
	for _, q := range []string{"&region=fr", "&region=%23fr", "&region=FR"} {
		got := getScopeTransport(t, router, q)
		if got.Region != "fr" {
			t.Errorf("%s: region = %q, want normalised \"fr\"", q, got.Region)
		}
		a := transportRow(t, got.Repeaters, testFullPubkeyA)
		b := transportRow(t, got.Repeaters, testFullPubkeyB)
		if a.CarriesRegion == nil || *a.CarriesRegion || b.CarriesRegion == nil || !*b.CarriesRegion {
			t.Errorf("%s: carriesRegion A=%v B=%v, want A false, B true", q, a.CarriesRegion, b.CarriesRegion)
		}
		if got.Carrying == nil || *got.Carrying != 1 || got.NotCarry == nil || *got.NotCarry != 1 {
			t.Errorf("%s: carrying=%v notCarrying=%v, want 1 and 1", q, got.Carrying, got.NotCarry)
		}
	}
	plain := getScopeTransport(t, router, "")
	if plain.Region != "" || plain.Carrying != nil || transportRow(t, plain.Repeaters, testFullPubkeyB).CarriesRegion != nil {
		t.Error("without a region filter, no carriesRegion or counts may be reported")
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
