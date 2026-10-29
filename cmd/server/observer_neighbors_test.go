package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// #2270 / #2271: the latest /neighbors report on the observer endpoints.

func seedNeighborReport(t *testing.T, srv *Server) {
	t.Helper()
	for _, s := range []string{
		`CREATE TABLE IF NOT EXISTS observer_neighbor_reports (observer_id TEXT PRIMARY KEY, received_at TEXT NOT NULL,
			reported_at TEXT, neighbor_count INTEGER NOT NULL, queried_neighbors INTEGER, self_scopes TEXT,
			self_default_scope TEXT, neighbors_json TEXT NOT NULL)`,
		// aabbccdd11223344 is the fixture's TestRepeater; the second key is unknown.
		`INSERT INTO observer_neighbor_reports VALUES ('obs1', '2026-10-25T10:00:00Z', '2026-10-25T09:59:58Z', 2, 2,
			'#tst', '*', '[{"pubkey":"aabbccdd11223344","snr":6.5,"heard_secs_ago":30,"status":"responded","scopes":"#tst"},
			              {"pubkey":"ff00ff00ff00ff00","status":"timeout"}]')`,
	} {
		if _, err := srv.db.conn.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
}

func getObservers(t *testing.T, srv *Server, router http.Handler, url string, into interface{}) {
	t.Helper()
	srv.observersCacheV2.ptr.Store(nil) // the list is cached for 30 s
	req := httptest.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("%s: %d %s", url, w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), into); err != nil {
		t.Fatal(err)
	}
}

func TestObserversListNeighborSummary(t *testing.T) {
	srv, router := setupTestServer(t)
	seedNeighborReport(t, srv)
	var list ObserverListResponse
	getObservers(t, srv, router, "/api/observers", &list)
	got := map[string]ObserverResp{}
	for _, o := range list.Observers {
		got[o.ID] = o
	}
	o1 := got["obs1"]
	if o1.NeighborsReportAt == nil || *o1.NeighborsReportAt != "2026-10-25T10:00:00Z" || o1.NeighborsCount == nil || *o1.NeighborsCount != 2 {
		t.Errorf("obs1 = at %v count %v", o1.NeighborsReportAt, o1.NeighborsCount)
	}
	if o1.NeighborReport != nil {
		t.Error("the list must not carry the full report")
	}
	if o2 := got["obs2"]; o2.NeighborsReportAt != nil || o2.NeighborsCount != nil {
		t.Errorf("obs2 never reported but has %v / %v", o2.NeighborsReportAt, o2.NeighborsCount)
	}
}

func TestObserverDetailNeighborReport(t *testing.T) {
	srv, router := setupTestServer(t)
	seedNeighborReport(t, srv)
	var detail ObserverResp
	getObservers(t, srv, router, "/api/observers/obs1", &detail)
	rep := detail.NeighborReport
	if rep == nil || rep.NeighborCount != 2 || len(rep.Neighbors) != 2 || rep.SelfScopes == nil || *rep.SelfScopes != "#tst" {
		t.Fatalf("report = %+v", rep)
	}
	known, unknown := rep.Neighbors[0], rep.Neighbors[1]
	if known.Name == nil || *known.Name != "TestRepeater" || known.Role == nil || *known.Role != "repeater" ||
		known.SNR == nil || *known.SNR != 6.5 || known.Status != "responded" {
		t.Errorf("known neighbor = %+v", known)
	}
	if unknown.Name != nil || unknown.Status != "timeout" {
		t.Errorf("unknown neighbor = %+v", unknown)
	}
	var none ObserverResp
	getObservers(t, srv, router, "/api/observers/obs2", &none)
	if none.NeighborReport != nil || none.NeighborsReportAt != nil {
		t.Errorf("obs2 detail has a report: %+v", none.NeighborReport)
	}
}

// Without the table (a server ahead of its ingestor) the endpoints still
// answer, just without neighbor data.
func TestObserversWithoutNeighborTable(t *testing.T) {
	srv, router := setupTestServer(t)
	var list ObserverListResponse
	getObservers(t, srv, router, "/api/observers", &list)
	for _, o := range list.Observers {
		if o.NeighborsReportAt != nil {
			t.Errorf("%v has a report without the table", o.ID)
		}
	}
	var detail ObserverResp
	getObservers(t, srv, router, "/api/observers/obs1", &detail)
	if detail.NeighborReport != nil {
		t.Error("detail has a report without the table")
	}
}
