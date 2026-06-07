package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
)

func serveReach(srv *Server, path string) *httptest.ResponseRecorder {
	router := mux.NewRouter()
	router.HandleFunc("/api/nodes/{pubkey}/reach", srv.handleNodeReach).Methods("GET")
	req := httptest.NewRequest("GET", path, nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)
	return rr
}

func TestClampDays(t *testing.T) {
	cases := []struct{ in, want int }{{0, 1}, {-5, 1}, {1, 1}, {7, 7}, {30, 30}, {31, 30}, {999, 30}}
	for _, c := range cases {
		if got := clampDays(c.in); got != c.want {
			t.Errorf("clampDays(%d)=%d want %d", c.in, got, c.want)
		}
	}
}

func TestNodeReach_UnknownNode(t *testing.T) {
	srv := makeTestServer(makeTestGraph()) // no store/db wired → 404
	rr := serveReach(srv, "/api/nodes/deadbeef/reach")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", rr.Code)
	}
}

func TestNodeReach_ShapeAndClamp(t *testing.T) {
	db := setupTestDBv2(t)
	const pk = "01fa326b475800a31105abcb9e4cac000b3e5d9e2b5ba0739981ce8d5f3a6754"
	mustExecDB(t, db, `INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES ('`+pk+`', 'BE-Test', 'repeater', 50.9, 5.4, '2026-06-07T00:00:00Z', '2026-06-01T00:00:00Z', 3)`)

	cfg := &Config{}
	srv := &Server{store: newTestStoreWithDB(t, db, cfg), db: db, cfg: cfg, perfStats: NewPerfStats()}

	rr := serveReach(srv, "/api/nodes/"+pk+"/reach?days=999")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	var resp NodeReachResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if resp.Window.Days != 30 {
		t.Fatalf("days not clamped to 30: %d", resp.Window.Days)
	}
	if resp.Links == nil || resp.DirectObservers == nil || resp.ReliableTokens == nil {
		t.Fatalf("array fields must be non-nil (never null)")
	}
	if !contains(resp.ReliableTokens, "01FA") {
		t.Fatalf("expected 01FA reliable token, got %v", resp.ReliableTokens)
	}
	if resp.Node.FirstSeen != "2026-06-01T00:00:00Z" {
		t.Fatalf("first_seen not sourced from nodes table: %q", resp.Node.FirstSeen)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
