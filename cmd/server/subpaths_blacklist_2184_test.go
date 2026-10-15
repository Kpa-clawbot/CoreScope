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

// Issue #2184: nodeBlacklist and hiddenNamePrefixes were not applied to Route
// Patterns. The seeded path ["aa","bb"] resolves its first hop to TestRepeater
// (aabbccdd11223344), so the subpath "TestRepeater → bb" is listed.

const seededRepeaterKey = "aabbccdd11223344"

func subpathPaths(t *testing.T, body []byte) []string {
	t.Helper()
	var resp struct {
		Subpaths []struct {
			Path string `json:"path"`
		} `json:"subpaths"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	out := make([]string, 0, len(resp.Subpaths))
	for _, sp := range resp.Subpaths {
		out = append(out, sp.Path)
	}
	return out
}

func bulkSubpathPaths(t *testing.T, body []byte) []string {
	t.Helper()
	var resp struct {
		Results []struct {
			Subpaths []struct {
				Path string `json:"path"`
			} `json:"subpaths"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v body=%s", err, body)
	}
	var out []string
	for _, r := range resp.Results {
		for _, sp := range r.Subpaths {
			out = append(out, sp.Path)
		}
	}
	return out
}

func anyContains(paths []string, name string) bool {
	for _, p := range paths {
		if strings.Contains(p, name) {
			return true
		}
	}
	return false
}

func getSubpathBody2184(t *testing.T, srv *Server, url string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest("GET", url, nil)
	w := httptest.NewRecorder()
	srv.router.ServeHTTP(w, req)
	return w.Code, w.Body.Bytes()
}

func TestSubpathsBlacklist2184(t *testing.T) {
	cases := []struct {
		name      string
		blacklist []string
		hidden    []string
		wantShown bool
	}{
		{"no filter lists the repeater", nil, nil, true},
		{"blacklisted key drops it", []string{seededRepeaterKey}, nil, false},
		{"blacklisted key in capitals drops it", []string{strings.ToUpper(seededRepeaterKey)}, nil, false},
		{"hidden name prefix drops it", nil, []string{"TestRep"}, false},
		// Another key with the same 1-byte prefix: the displayed node is not
		// the blacklisted one, so the subpath stays.
		{"same-prefix key keeps it", []string{"aa00000000000000"}, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv, _ := setupTestServer(t)
			if c.blacklist != nil {
				srv.cfg.NodeBlacklist = c.blacklist
				srv.cfg.SetNodeBlacklist(c.blacklist)
			}
			if c.hidden != nil {
				srv.cfg.SetHiddenNamePrefixes(c.hidden)
			}

			code, body := getSubpathBody2184(t, srv, "/api/analytics/subpaths?minLen=2&maxLen=8")
			if code != http.StatusOK {
				t.Fatalf("subpaths status=%d body=%s", code, body)
			}
			if got := anyContains(subpathPaths(t, body), "TestRepeater"); got != c.wantShown {
				t.Errorf("subpaths: TestRepeater shown=%v, want %v (%s)", got, c.wantShown, body)
			}

			code, body = getSubpathBody2184(t, srv, "/api/analytics/subpaths-bulk?groups=2-2:50,3-8:50")
			if code != http.StatusOK {
				t.Fatalf("subpaths-bulk status=%d body=%s", code, body)
			}
			if got := anyContains(bulkSubpathPaths(t, body), "TestRepeater"); got != c.wantShown {
				t.Errorf("subpaths-bulk: TestRepeater shown=%v, want %v (%s)", got, c.wantShown, body)
			}

			code, body = getSubpathBody2184(t, srv, "/api/analytics/subpath-detail?hops=aa,bb")
			wantCode := http.StatusOK
			if !c.wantShown {
				wantCode = http.StatusNotFound
			}
			if code != wantCode {
				t.Errorf("subpath-detail status=%d, want %d (%s)", code, wantCode, body)
			}
		})
	}
}

// The filter must not change the store's cached response: a later request,
// or one after the blacklist is cleared, still sees the full list.
func TestSubpathsBlacklistLeavesCacheIntact2184(t *testing.T) {
	srv, _ := setupTestServer(t)
	srv.cfg.NodeBlacklist = []string{seededRepeaterKey}
	srv.cfg.SetNodeBlacklist(srv.cfg.NodeBlacklist)
	if _, body := getSubpathBody2184(t, srv, "/api/analytics/subpaths?minLen=2&maxLen=8"); anyContains(subpathPaths(t, body), "TestRepeater") {
		t.Fatalf("blacklisted repeater still listed: %s", body)
	}

	srv.cfg.NodeBlacklist = nil
	srv.cfg.SetNodeBlacklist(nil)
	if _, body := getSubpathBody2184(t, srv, "/api/analytics/subpaths?minLen=2&maxLen=8"); !anyContains(subpathPaths(t, body), "TestRepeater") {
		t.Errorf("after clearing the blacklist the repeater is missing: the filter mutated the cached response (%s)", body)
	}
}

// setupSubpathServer2184 is setupTestServer with a SQL statement run on the
// seeded DB before the store loads, so the store sees the changed rows.
func setupSubpathServer2184(t *testing.T, stmt string) *Server {
	t.Helper()
	db := setupTestDB(t)
	seedTestData(t, db)
	if _, err := db.conn.Exec(stmt); err != nil {
		t.Fatalf("exec %q: %v", stmt, err)
	}
	srv := NewServer(db, &Config{Port: 3000}, NewHub())
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load failed: %v", err)
	}
	if !store.WaitIndexesReady(5 * time.Second) {
		t.Fatalf("background indexes never became ready")
	}
	srv.store = store
	srv.router = mux.NewRouter()
	srv.RegisterRoutes(srv.router)
	return srv
}

// A blacklisted node without a name (the firmware allows one) has no name to
// match, so the detail must check the resolved key.
func TestSubpathDetailBlacklistUnnamedNode2184(t *testing.T) {
	srv := setupSubpathServer2184(t, `UPDATE nodes SET name = '' WHERE public_key = '`+seededRepeaterKey+`'`)
	srv.cfg.NodeBlacklist = []string{seededRepeaterKey}
	srv.cfg.SetNodeBlacklist(srv.cfg.NodeBlacklist)

	code, body := getSubpathBody2184(t, srv, "/api/analytics/subpath-detail?hops=aa,bb")
	if code != http.StatusNotFound {
		t.Errorf("subpath-detail status=%d, want 404 (%s)", code, body)
	}
}

// Another node that only shares the blacklisted node's name does not hide the
// detail of a path through the seeded repeater.
func TestSubpathDetailBlacklistSameNameOtherNode2184(t *testing.T) {
	const other = "ff99887766554433"
	srv := setupSubpathServer2184(t, `INSERT INTO nodes (public_key, name, role) VALUES ('`+other+`', 'TestRepeater', 'repeater')`)
	srv.cfg.NodeBlacklist = []string{other}
	srv.cfg.SetNodeBlacklist(srv.cfg.NodeBlacklist)

	code, body := getSubpathBody2184(t, srv, "/api/analytics/subpath-detail?hops=aa,bb")
	if code != http.StatusOK {
		t.Errorf("subpath-detail status=%d, want 200 (%s)", code, body)
	}
}
