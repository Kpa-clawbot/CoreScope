package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// The Scope Audit had no region selector, unlike every other page. With
// ?region=<IATA,...> both views count only forwarding heard by observers in
// those regions: "as heard by", the same meaning region= has on /api/nodes
// and /api/packets, not "located in".

// setupScopeRegionServer is setupScopeAuditServer with observers and the v3
// observations.observer_idx column, so a hop can be attributed to the
// observer that heard it.
func setupScopeRegionServer(t *testing.T) (*Server, *mux.Router) {
	t.Helper()
	srv, router := setupScopeAuditServer(t)
	for _, ddl := range []string{
		`CREATE TABLE observers (id TEXT, name TEXT, iata TEXT)`,
		`ALTER TABLE observations ADD COLUMN observer_idx INTEGER`,
		`INSERT INTO observers (rowid, id, name, iata) VALUES (1, 'obs-sfo', 'San Francisco', 'SFO'), (2, 'obs-bru', 'Brussels', 'BRU')`,
	} {
		if _, err := srv.db.conn.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if err := srv.db.detectSchema(context.Background(), srv.db.conn); err != nil {
		t.Fatal(err)
	}
	if !srv.db.isV3 {
		t.Fatal("fixture must be v3 (observer_idx) or the test exercises nothing")
	}
	return srv, router
}

var heardCounter int

// seedHeard inserts one flood transmission forwarded by hop, heard by the
// observer with the given rowid.
func seedHeard(t *testing.T, srv *Server, hop string, seed scopeSeed, observerRowid int) {
	t.Helper()
	heardCounter++
	res, err := srv.db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, code1, code2, scope_name)
		VALUES ('AA', ?, ?, ?, 1, ?, '00', ?)`, fmt.Sprintf("heard%d", heardCounter),
		time.Now().UTC().Add(-time.Minute).Format(time.RFC3339), RouteFlood, seed.code1, seed.scopeName)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if _, err := srv.db.conn.Exec(`INSERT INTO observations (transmission_id, path_json, timestamp, observer_idx) VALUES (?, ?, ?, ?)`,
		id, fmt.Sprintf(`["%s"]`, strings.ToUpper(hop)), time.Now().Unix(), observerRowid); err != nil {
		t.Fatal(err)
	}
}

// A declared "be", heard carrying #be by San Francisco and #de by Brussels.
func seedRegionalForwarding(t *testing.T, srv *Server) {
	t.Helper()
	insertDeclared(t, srv, testFullPubkeyA, time.Now().UTC().Format(time.RFC3339), "be", 0)
	addRepeater(t, srv, testFullPubkeyA, "Alpha")
	addRepeater(t, srv, testFullPubkeyB, "Bravo")
	seedHeard(t, srv, testFullPubkeyA[:4], scopeMatched("#be"), 1)
	seedHeard(t, srv, testFullPubkeyA[:4], scopeMatched("#de"), 2)
	seedHeard(t, srv, testFullPubkeyB[:4], scopeMatched("#fr"), 2)
}

func TestScopeAudit_RegionCountsOnlyHopsHeardThere(t *testing.T) {
	srv, router := setupScopeRegionServer(t)
	seedRegionalForwarding(t, srv)

	all := findScopeAuditRow(t, getScopeAudit(t, router, "").Repeaters, testFullPubkeyA)
	if len(all.UndeclaredObserved) != 1 || all.UndeclaredObserved[0].Scope != "de" {
		t.Fatalf("without a region, #de heard in Brussels should be undeclared-observed: %+v", all.UndeclaredObserved)
	}
	sfo := getScopeAudit(t, router, "?region=SFO")
	row := findScopeAuditRow(t, sfo.Repeaters, testFullPubkeyA)
	if len(row.UndeclaredObserved) != 0 {
		t.Errorf("region=SFO must not count #de heard only in Brussels: %+v", row.UndeclaredObserved)
	}
	if len(row.NotObserved) != 0 {
		t.Errorf("region=SFO: #be was heard in San Francisco, notObserved = %v", row.NotObserved)
	}
	if sfo.Region != "SFO" {
		t.Errorf("region = %q, want the normalised codes echoed", sfo.Region)
	}
}

func TestScopeTransport_RegionListsOnlyRepeatersHeardThere(t *testing.T) {
	srv, router := setupScopeRegionServer(t)
	seedRegionalForwarding(t, srv)
	got := getScopeTransport(t, router, "&region=bru")
	if got.Region != "BRU" {
		t.Errorf("region = %q, want BRU", got.Region)
	}
	a := transportRow(t, got.Repeaters, testFullPubkeyA)
	if len(a.Transported) != 1 || a.Transported[0].Scope != "de" {
		t.Errorf("A as heard in Brussels: transported = %+v, want only de", a.Transported)
	}
	transportRow(t, got.Repeaters, testFullPubkeyB)
	sfo := getScopeTransport(t, router, "&region=SFO")
	for _, r := range sfo.Repeaters {
		if r.PublicKey == testFullPubkeyB {
			t.Fatal("B was only heard in Brussels and must not be listed for region=SFO")
		}
	}
}

func TestScopeAudit_RegionAllMeansNoFilter(t *testing.T) {
	srv, router := setupScopeRegionServer(t)
	seedRegionalForwarding(t, srv)
	got := getScopeAudit(t, router, "?region=All")
	row := findScopeAuditRow(t, got.Repeaters, testFullPubkeyA)
	if len(row.UndeclaredObserved) != 1 || got.Region != "" {
		t.Fatalf("region=All must behave like no region: undeclared=%+v region=%q", row.UndeclaredObserved, got.Region)
	}
}

func TestScopeAudit_RegionsAreCachedSeparately(t *testing.T) {
	srv, router := setupScopeRegionServer(t)
	seedRegionalForwarding(t, srv)
	getScopeAudit(t, router, "?region=SFO") // fills the SFO entry first
	row := findScopeAuditRow(t, getScopeAudit(t, router, "").Repeaters, testFullPubkeyA)
	if len(row.UndeclaredObserved) != 1 {
		t.Fatalf("the unfiltered view must not be served from the SFO cache entry: %+v", row.UndeclaredObserved)
	}
	tr := getScopeTransport(t, router, "&region=SFO")
	if len(transportRow(t, tr.Repeaters, testFullPubkeyA).Transported) != 1 {
		t.Fatal("transport view for SFO should count only #be")
	}
}

func TestScopeAudit_RegionWithNoObserversMatchesNothing(t *testing.T) {
	srv, router := setupScopeRegionServer(t)
	seedRegionalForwarding(t, srv)
	got := getScopeTransport(t, router, "&region=ZZZ")
	if len(got.Repeaters) != 0 {
		t.Fatalf("a region with no observers must match no forwarding, got %d rows", len(got.Repeaters))
	}
}
