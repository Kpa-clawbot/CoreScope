package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
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

func TestObserverDetailNeighborReportVisibility(t *testing.T) {
	db := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	publicObserver := strings.Repeat("10", 32)
	publicNode := strings.Repeat("ab", 32)
	hiddenNode := strings.Repeat("cd", 32)
	blockedNode := strings.Repeat("de", 32)
	blockedObserver := strings.Repeat("ef", 32)
	unknownNode := strings.Repeat("01", 32)
	caseControl := strings.Repeat("02", 32)
	keys := []string{publicNode, strings.ToUpper(hiddenNode), blockedNode, blockedObserver, unknownNode, caseControl}
	for _, node := range []struct{ key, name string }{
		{publicNode, "Shared neighbor name"},
		{hiddenNode, "Hidden neighbor"},
		{blockedNode, "Shared neighbor name"}, // A name shared with a blocked key is still public.
		{blockedObserver, "Blocked observer node"},
		{caseControl, "hidden lowercase neighbor"}, // Name prefixes are case-sensitive.
	} {
		if _, err := db.conn.Exec(`INSERT INTO nodes (public_key, name, role) VALUES (?, ?, 'repeater')`, node.key, node.name); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{publicObserver, blockedObserver} {
		if _, err := db.conn.Exec(`INSERT INTO observers (id, name) VALUES (?, 'Synthetic observer')`, id); err != nil {
			t.Fatal(err)
		}
	}
	// The active nodes name is authoritative for this endpoint; a retired
	// name for the public key must not replace it during filtering.
	mustExecDB(t, db, `CREATE TABLE inactive_nodes (public_key TEXT PRIMARY KEY, name TEXT)`)
	if _, err := db.conn.Exec(`INSERT INTO inactive_nodes VALUES (?, 'Hidden retired name')`, publicNode); err != nil {
		t.Fatal(err)
	}
	fixture := NewServer(db, &Config{}, NewHub())
	seedNeighborReport(t, fixture)
	neighbors := make([]ObserverNeighbor, 0, len(keys))
	snr, heard := 6.5, int64(30)
	for _, key := range keys {
		neighbors = append(neighbors, ObserverNeighbor{Pubkey: key, SNR: &snr, HeardSecsAgo: &heard, Status: "responded", Scopes: "#synthetic"})
	}
	body, err := json.Marshal(neighbors)
	if err != nil {
		t.Fatal(err)
	}
	// Reported totals may exceed the sanitized/truncated list. Visibility
	// filtering must preserve that existing meaning on both observer endpoints.
	if _, err := db.conn.Exec(`UPDATE observer_neighbor_reports SET observer_id = ?, neighbor_count = 24, queried_neighbors = 18, neighbors_json = ? WHERE observer_id = 'obs1'`, publicObserver, string(body)); err != nil {
		t.Fatal(err)
	}
	baseline, err := db.GetObserverNeighborReport(publicObserver)
	if err != nil || baseline == nil {
		t.Fatalf("baseline report = %+v, err = %v", baseline, err)
	}
	for _, tc := range []struct {
		name string
		cfg  *Config
		want []int
	}{
		{"unrestricted", &Config{}, []int{0, 1, 2, 3, 4, 5}},
		{"hidden-name", &Config{HiddenNamePrefixes: []string{"Hidden "}}, []int{0, 2, 3, 4, 5}},
		{"node-blacklist", &Config{NodeBlacklist: []string{" " + strings.ToUpper(blockedNode) + " "}}, []int{0, 1, 3, 4, 5}},
		// ObserverBlacklist hides reporters, not physical nodes reported by someone else.
		{"observer-blacklist-preserves-neighbor", &Config{ObserverBlacklist: []string{" " + strings.ToUpper(blockedObserver) + " "}}, []int{0, 1, 2, 3, 4, 5}},
		{"combined", &Config{HiddenNamePrefixes: []string{"Hidden "}, NodeBlacklist: []string{blockedNode}, ObserverBlacklist: []string{blockedObserver}}, []int{0, 3, 4, 5}},
		{"all-hidden", &Config{NodeBlacklist: keys}, []int{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer(db, tc.cfg, NewHub())
			router := setupTestRouter(srv)
			for _, check := range []struct {
				path   string
				hidden bool
			}{
				{"/api/nodes/" + hiddenNode, tc.cfg.IsNameHidden("Hidden neighbor")},
				{"/api/nodes/" + blockedNode, tc.cfg.IsBlacklisted(blockedNode)},
				{"/api/observers/" + blockedObserver, tc.cfg.IsObserverBlacklisted(blockedObserver)},
			} {
				if check.hidden {
					w := httptest.NewRecorder()
					router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, check.path, nil))
					if w.Code != http.StatusNotFound {
						t.Fatalf("direct visibility control %s: got %d, want 404", check.path, w.Code)
					}
				}
			}
			var detail ObserverResp
			getObservers(t, srv, router, "/api/observers/"+publicObserver, &detail)
			if detail.NeighborReport == nil {
				t.Fatal("public observer lost its report")
			}
			want := *baseline
			want.Neighbors = make([]ObserverNeighbor, 0, len(tc.want))
			for _, i := range tc.want {
				want.Neighbors = append(want.Neighbors, baseline.Neighbors[i])
			}
			if !reflect.DeepEqual(detail.NeighborReport, &want) {
				gotKeys := make([]string, 0, len(detail.NeighborReport.Neighbors))
				wantKeys := make([]string, 0, len(want.Neighbors))
				for _, n := range detail.NeighborReport.Neighbors {
					gotKeys = append(gotKeys, n.Pubkey)
				}
				for _, n := range want.Neighbors {
					wantKeys = append(wantKeys, n.Pubkey)
				}
				t.Errorf("neighbor report differs: got keys %v, want %v; report metadata and visible entries must stay unchanged", gotKeys, wantKeys)
			}
			if detail.NeighborsCount == nil || *detail.NeighborsCount != 24 || detail.NeighborsReportAt == nil || *detail.NeighborsReportAt != baseline.ReceivedAt {
				t.Errorf("detail report summary changed: count %v, at %v", detail.NeighborsCount, detail.NeighborsReportAt)
			}
			var list ObserverListResponse
			getObservers(t, srv, router, "/api/observers", &list)
			found := false
			for _, o := range list.Observers {
				if o.ID == publicObserver {
					found = true
					if o.NeighborsCount == nil || *o.NeighborsCount != 24 || o.NeighborReport != nil {
						t.Error("list must retain the reported count without the full report")
					}
				}
			}
			if !found {
				t.Error("public observer missing from list")
			}
			stored, err := db.GetObserverNeighborReport(publicObserver)
			if err != nil || !reflect.DeepEqual(stored, baseline) {
				t.Errorf("API filtering changed the stored report: %v", err)
			}
		})
	}
	t.Run("reporter-blacklist", func(t *testing.T) {
		srv := NewServer(db, &Config{ObserverBlacklist: []string{strings.ToUpper(publicObserver)}}, NewHub())
		router := setupTestRouter(srv)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/observers/"+publicObserver, nil))
		if w.Code != http.StatusNotFound {
			t.Fatalf("blacklisted reporter with stored report: got %d, want 404", w.Code)
		}
	})
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
