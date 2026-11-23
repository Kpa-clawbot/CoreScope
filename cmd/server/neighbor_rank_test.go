package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type rankNode struct{ pk, name, role string }

// newRankServer builds a Server over an in-memory DB with the given nodes
// rows and neighbor_edges (pairs of pubkeys).
func newRankServer(t testing.TB, nodes []rankNode, edges [][2]string) *Server {
	t.Helper()
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { conn.Close() })
	for _, st := range []string{
		`CREATE TABLE nodes (public_key TEXT, name TEXT, role TEXT, lat REAL, lon REAL, last_seen TEXT, first_seen TEXT, advert_count INTEGER)`,
		`CREATE TABLE neighbor_edges (node_a TEXT, node_b TEXT, count INTEGER)`,
	} {
		if _, err := conn.Exec(st); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := conn.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if _, err := tx.Exec(`INSERT INTO nodes (public_key, name, role) VALUES (?,?,?)`, n.pk, n.name, n.role); err != nil {
			t.Fatal(err)
		}
	}
	for _, e := range edges {
		if _, err := tx.Exec(`INSERT INTO neighbor_edges VALUES (?,?,1)`, e[0], e[1]); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	db := &DB{conn: conn, isV3: true}
	cfg := &Config{}
	return &Server{store: newTestStoreWithDB(t, db, cfg), db: db, cfg: cfg, perfStats: NewPerfStats()}
}

func getRank(t testing.TB, s *Server, query string) (*httptest.ResponseRecorder, NeighborRankResponse) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/nodes/neighbor-rank?"+query, nil)
	rr := httptest.NewRecorder()
	s.handleNeighborRank(rr, req)
	var resp NeighborRankResponse
	if rr.Code == http.StatusOK {
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("bad JSON: %v: %s", err, rr.Body.String())
		}
	}
	return rr, resp
}

func rankOfEntry(resp NeighborRankResponse, pk string) int {
	for _, e := range resp.Entries {
		if e.Pubkey == pk {
			return e.Rank
		}
	}
	return -1
}

var (
	rkA, rkB, rkC, rkD = pk64("a1"), pk64("b2"), pk64("c3"), pk64("d4")
	rkH                = pk64("e5") // hidden by prefix or blacklist in the tests below
	rkZ                = pk64("f6") // no edges
)

// a-b, a-c, b-d: a=2 b=2 c=1 d=1 → ranks 1,1,3,3.
func standardRankServer(t testing.TB) *Server {
	return newRankServer(t, []rankNode{
		{rkA, "Alpha", "repeater"}, {rkB, "Bravo", "room"}, {rkC, "Charlie", "companion"},
		{rkD, "Delta", "repeater"}, {rkZ, "Zulu", "repeater"},
	}, [][2]string{{rkA, rkB}, {rkA, rkC}, {rkB, rkD}})
}

func TestNeighborRankTiesAndDegreeZero(t *testing.T) {
	s := standardRankServer(t)
	rr, resp := getRank(t, s, "")
	if rr.Code != 200 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	want := []struct {
		pk   string
		rank int
		n    int
	}{{rkA, 1, 2}, {rkB, 1, 2}, {rkC, 3, 1}, {rkD, 3, 1}}
	if len(resp.Entries) != len(want) || resp.Total != 4 || resp.Ranked != 4 {
		t.Fatalf("got %d entries total=%d ranked=%d: %+v", len(resp.Entries), resp.Total, resp.Ranked, resp)
	}
	for i, w := range want {
		e := resp.Entries[i]
		if e.Pubkey != w.pk || e.Rank != w.rank || e.Neighbors != w.n {
			t.Errorf("entry %d = %+v, want %+v", i, e, w)
		}
	}
	if rankOfEntry(resp, rkZ) != -1 {
		t.Error("degree-0 node must not be listed")
	}
	if resp.Entries[0].Name != "Alpha" || resp.Entries[0].Role != "repeater" {
		t.Errorf("name/role not filled: %+v", resp.Entries[0])
	}
	if _, err := time.Parse(time.RFC3339, resp.SnapshotAt); err != nil || !strings.HasSuffix(resp.SnapshotAt, "Z") {
		t.Errorf("snapshot_at %q is not RFC3339 UTC", resp.SnapshotAt)
	}
}

func TestNeighborRankNoNodeRowIsNeverRanked(t *testing.T) {
	// rkC has edges but no nodes row: fail closed, it cannot be checked
	// against the hidden prefixes.
	s := newRankServer(t, []rankNode{{rkA, "Alpha", "repeater"}, {rkB, "Bravo", "repeater"}}, [][2]string{{rkA, rkB}, {rkA, rkC}})
	_, resp := getRank(t, s, "")
	if resp.Ranked != 2 || rankOfEntry(resp, rkC) != -1 {
		t.Fatalf("pubkey without a nodes row was ranked: %+v", resp)
	}
	if resp.Entries[0].Pubkey != rkA || resp.Entries[0].Neighbors != 2 {
		t.Errorf("its edge still counts toward the visible neighbour: %+v", resp.Entries)
	}
	if _, r, _ := s.reachDegreeRank(t.Context(), rkC); r != 0 {
		t.Errorf("card ranked a node with no row: %d", r)
	}
}

func TestNeighborRankIgnoresInvalidPubkeyEdges(t *testing.T) {
	s := newRankServer(t, []rankNode{{rkA, "Alpha", "repeater"}, {rkB, "Bravo", "repeater"}, {"", "Empty", "repeater"}, {"ab12", "Short", "repeater"}},
		[][2]string{{rkA, rkB}, {"", rkA}, {"", rkB}, {"ab12", rkA}, {rkA, pk64("c3")[1:]}})
	_, resp := getRank(t, s, "")
	if resp.Ranked != 2 || resp.Entries[0].Neighbors != 1 || resp.Entries[1].Neighbors != 1 {
		t.Fatalf("invalid-key edges counted: %+v", resp)
	}
	for _, e := range resp.Entries {
		if len(e.Pubkey) != 64 {
			t.Errorf("invalid pubkey listed: %q", e.Pubkey)
		}
	}
}

func TestNeighborRankRenameIntoHiddenPrefixAfterCacheRefresh(t *testing.T) {
	s := standardRankServer(t)
	s.cfg.SetHiddenNamePrefixes([]string{"~"})
	if _, resp := getRank(t, s, ""); resp.Ranked != 4 {
		t.Fatalf("ranked %d", resp.Ranked)
	}
	if _, err := s.db.conn.Exec(`UPDATE nodes SET name = '~Alpha' WHERE public_key = ?`, rkA); err != nil {
		t.Fatal(err)
	}
	if _, resp := getRank(t, s, ""); resp.Ranked != 4 {
		t.Fatalf("view changed before the node cache refreshed: %+v", resp)
	}
	s.store.InvalidateNodeCache()
	_, resp := getRank(t, s, "")
	if resp.Ranked != 3 || rankOfEntry(resp, rkA) != -1 {
		t.Fatalf("renamed node still ranked after cache refresh: %+v", resp)
	}
}

func TestNeighborRankStaleViewNotStored(t *testing.T) {
	s := standardRankServer(t)
	newer := &degreeSnapshot{at: time.Now(), deg: map[string]int{rkA: 1}}
	// While a view for the first snapshot is being built, a newer snapshot
	// lands: the slow build must not be stored over it.
	s.reach.beforeRankStore = func() {
		s.reach.degreeMu.Lock()
		s.reach.degreeSnap = newer
		s.reach.degreeMu.Unlock()
	}
	old := s.getNeighborRankView(t.Context())
	if old == nil || old.snap == newer {
		t.Fatal("setup: expected a view for the old snapshot")
	}
	if s.reach.rankView != nil {
		t.Fatal("view for a superseded snapshot was stored")
	}
	s.reach.beforeRankStore = nil
	if v := s.getNeighborRankView(t.Context()); v.snap != newer || s.reach.rankView != v {
		t.Fatal("view for the current snapshot was not built and stored")
	}
}

func TestNeighborRankFocus(t *testing.T) {
	// 130 nodes, each with exactly one edge to a hub-less partner: build 65
	// disjoint pairs so all 130 tie at degree 1 (one tie group, 3 pages of 50).
	var nodes []rankNode
	var edges [][2]string
	for i := 0; i < 65; i++ {
		a, b := pk64(fmt.Sprintf("%04x", 2*i+1)), pk64(fmt.Sprintf("%04x", 2*i+2))
		role := "repeater"
		if i%2 == 1 {
			role = "room"
		}
		nodes = append(nodes, rankNode{a, fmt.Sprintf("N%03d", 2*i), role}, rankNode{b, fmt.Sprintf("N%03d", 2*i+1), role})
		edges = append(edges, [2]string{a, b})
	}
	s := newRankServer(t, nodes, edges)
	last := pk64(fmt.Sprintf("%04x", 130)) // last in pubkey order: index 129
	_, resp := getRank(t, s, "focus="+strings.ToUpper(last)+"&offset=7")
	if resp.Offset != 100 || len(resp.Entries) != 30 || rankOfEntry(resp, last) != 1 || resp.Total != 130 {
		t.Fatalf("focus on last tie member: offset=%d entries=%d rank=%d", resp.Offset, len(resp.Entries), rankOfEntry(resp, last))
	}
	if resp.Entries[29].Pubkey != last {
		t.Errorf("focused node not on the returned page")
	}
	// Not in the filtered list (it is a room-or-repeater mismatch): requested offset.
	_, resp = getRank(t, s, "focus="+last+"&role=companion&offset=0")
	if resp.Offset != 0 || resp.Total != 0 {
		t.Errorf("focus outside the list: %+v", resp)
	}
	_, resp = getRank(t, s, "focus="+pk64("ffff")+"&offset=50&limit=20")
	if resp.Offset != 50 || len(resp.Entries) != 20 {
		t.Errorf("unknown focus must keep the requested offset: %+v", resp)
	}
	// focus with role: index within the filtered list drives the page.
	_, resp = getRank(t, s, "role=repeater&limit=10&focus="+last)
	if rankOfEntry(resp, last) == -1 || resp.Offset%10 != 0 || resp.Total != 66 {
		t.Errorf("focus+role: offset=%d total=%d", resp.Offset, resp.Total)
	}
	_, resp = getRank(t, s, "q=N129&focus="+last)
	if resp.Offset != 0 || resp.Total != 1 || resp.Entries[0].Pubkey != last {
		t.Errorf("focus+q: %+v", resp)
	}
	for _, bad := range []string{"focus=", "focus=abc", "focus=" + strings.Repeat("g", 64), "focus=" + last + "00"} {
		if rr, _ := getRank(t, s, bad); rr.Code != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400", bad, rr.Code)
		}
	}
}

func TestNeighborRankJSONShape(t *testing.T) {
	s := newRankServer(t, nil, nil)
	req := httptest.NewRequest("GET", "/api/nodes/neighbor-rank", nil)
	rr := httptest.NewRecorder()
	s.handleNeighborRank(rr, req)
	body := rr.Body.String()
	for _, f := range []string{`"entries":[]`, `"total":0`, `"ranked":0`, `"snapshot_at":"`} {
		if !strings.Contains(body, f) {
			t.Errorf("body missing %s: %s", f, body)
		}
	}
	s2 := standardRankServer(t)
	rr = httptest.NewRecorder()
	s2.handleNeighborRank(rr, httptest.NewRequest("GET", "/api/nodes/neighbor-rank?limit=1", nil))
	for _, f := range []string{`"rank":1`, `"pubkey":"`, `"name":"Alpha"`, `"role":"repeater"`, `"neighbors":2`} {
		if !strings.Contains(rr.Body.String(), f) {
			t.Errorf("body missing %s: %s", f, rr.Body.String())
		}
	}
}

func TestNeighborRankHiddenExcludedAndCardAgrees(t *testing.T) {
	for _, mode := range []string{"blacklist", "hidden-prefix"} {
		t.Run(mode, func(t *testing.T) {
			// h-a, h-b, a-b, a-c: h=2 a=3 b=2 c=1 before hiding; hidden h must
			// vanish from the list and the population, but its edges still
			// count toward a and b.
			s := newRankServer(t, []rankNode{
				{rkA, "Alpha", "repeater"}, {rkB, "Bravo", "repeater"}, {rkC, "Charlie", "repeater"},
				{rkH, "~hidden-h", "repeater"},
			}, [][2]string{{rkH, rkA}, {rkH, rkB}, {rkA, rkB}, {rkA, rkC}})
			if mode == "blacklist" {
				s.cfg.SetNodeBlacklist([]string{rkH})
			} else {
				s.cfg.SetHiddenNamePrefixes([]string{"~hidden"})
			}
			_, resp := getRank(t, s, "")
			if resp.Ranked != 3 || resp.Total != 3 || rankOfEntry(resp, rkH) != -1 {
				t.Fatalf("hidden node leaked into the list: %+v", resp)
			}
			if resp.Entries[0].Pubkey != rkA || resp.Entries[0].Neighbors != 3 ||
				resp.Entries[1].Pubkey != rkB || resp.Entries[1].Neighbors != 2 {
				t.Fatalf("hidden neighbour's edge must still count: %+v", resp.Entries)
			}
			// The card uses the same view: visible nodes agree with the list,
			// the hidden node is unranked, total is the visible population.
			for _, e := range resp.Entries {
				d, r, total := s.reachDegreeRank(t.Context(), e.Pubkey)
				if d != e.Neighbors || r != e.Rank || total != resp.Ranked {
					t.Errorf("card (%d,%d,%d) != list (%d,%d,%d) for %s", d, r, total, e.Neighbors, e.Rank, resp.Ranked, e.Name)
				}
			}
			if d, r, total := s.reachDegreeRank(t.Context(), rkH); d != 2 || r != 0 || total != 3 {
				t.Errorf("hidden node card = (%d,%d,%d), want (2,0,3)", d, r, total)
			}
		})
	}
}

func TestNeighborRankCardZeroDegree(t *testing.T) {
	s := standardRankServer(t)
	if d, r, total := s.reachDegreeRank(t.Context(), rkZ); d != 0 || r != 0 || total != 4 {
		t.Errorf("degree-0 card = (%d,%d,%d), want (0,0,4)", d, r, total)
	}
	if d, r, total := s.reachDegreeRank(t.Context(), rkC); d != 1 || r != 3 || total != 4 {
		t.Errorf("card for c = (%d,%d,%d), want (1,3,4)", d, r, total)
	}
}

func TestNeighborRankFiltersDoNotRenumber(t *testing.T) {
	s := standardRankServer(t)
	_, resp := getRank(t, s, "role=repeater")
	if resp.Total != 2 || resp.Ranked != 4 || resp.Entries[0].Pubkey != rkA || resp.Entries[1].Pubkey != rkD || resp.Entries[1].Rank != 3 {
		t.Errorf("role filter renumbered or miscounted: %+v", resp)
	}
	_, resp = getRank(t, s, "q=CHARL")
	if resp.Total != 1 || resp.Entries[0].Pubkey != rkC || resp.Entries[0].Rank != 3 {
		t.Errorf("name filter: %+v", resp)
	}
	_, resp = getRank(t, s, "q="+url.QueryEscape("  b2 "))
	if resp.Total != 1 || resp.Entries[0].Pubkey != rkB {
		t.Errorf("pubkey prefix filter: %+v", resp)
	}
	_, resp = getRank(t, s, "q=ravo&role=room")
	if resp.Total != 1 || resp.Entries[0].Pubkey != rkB {
		t.Errorf("q+role: %+v", resp)
	}
	rr, resp := getRank(t, s, "q=nomatch")
	if resp.Total != 0 || resp.Entries == nil || !strings.Contains(rr.Body.String(), `"entries":[]`) {
		t.Errorf("no match must give entries []: %s", rr.Body.String())
	}
}

func TestNeighborRankPaging(t *testing.T) {
	s := standardRankServer(t)
	_, resp := getRank(t, s, "limit=2&offset=1")
	if resp.Total != 4 || len(resp.Entries) != 2 || resp.Entries[0].Pubkey != rkB || resp.Entries[1].Pubkey != rkC {
		t.Errorf("page: %+v", resp)
	}
	_, resp = getRank(t, s, "limit=2&offset=3")
	if resp.Total != 4 || len(resp.Entries) != 1 || resp.Entries[0].Pubkey != rkD {
		t.Errorf("last page: %+v", resp)
	}
	_, resp = getRank(t, s, "offset=1000000")
	if resp.Total != 4 || len(resp.Entries) != 0 {
		t.Errorf("past the end: %+v", resp)
	}
}

func TestNeighborRankBadParams(t *testing.T) {
	s := standardRankServer(t)
	for _, q := range []string{
		"q=" + strings.Repeat("x", 65), "role=gateway", "role=Repeater",
		"offset=-1", "offset=abc", "offset=1000001",
		"limit=0", "limit=201", "limit=-5", "limit=x",
	} {
		rr, _ := getRank(t, s, q)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("%q: status %d, want 400", q, rr.Code)
		}
	}
	if rr, _ := getRank(t, s, "q="+strings.Repeat("x", 64)+"&limit=200&offset=1000000"); rr.Code != 200 {
		t.Errorf("boundary values rejected: %d", rr.Code)
	}
}

func TestNeighborRankNoSnapshot503(t *testing.T) {
	// A DB without neighbor_edges and no prior snapshot: nothing to serve.
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	db := &DB{conn: conn, isV3: true}
	cfg := &Config{}
	s := &Server{store: newTestStoreWithDB(t, db, cfg), db: db, cfg: cfg, perfStats: NewPerfStats()}
	rr, _ := getRank(t, s, "")
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503: %s", rr.Code, rr.Body.String())
	}
}

func TestNeighborRankViewRebuildsOnVisibilityChange(t *testing.T) {
	s := standardRankServer(t)
	_, resp := getRank(t, s, "")
	if resp.Ranked != 4 {
		t.Fatalf("ranked %d", resp.Ranked)
	}
	v1 := s.getNeighborRankView(t.Context())
	if v2 := s.getNeighborRankView(t.Context()); v2 != v1 {
		t.Error("view rebuilt although snapshot and rules are unchanged")
	}
	s.cfg.SetNodeBlacklist([]string{rkA})
	_, resp = getRank(t, s, "")
	if resp.Ranked != 3 || rankOfEntry(resp, rkA) != -1 {
		t.Fatalf("view not rebuilt after the rule changed: %+v", resp)
	}
	if resp.Entries[0].Pubkey != rkB || resp.Entries[0].Rank != 1 {
		t.Errorf("b should lead once a is hidden: %+v", resp.Entries)
	}
}

func TestReachCacheKeyIncludesVisibilityGen(t *testing.T) {
	s := standardRankServer(t)
	resetReachState(t, s)
	before := s.nodeVisibilityGen()
	s.cfg.SetHiddenNamePrefixes([]string{"Zz"})
	if s.nodeVisibilityGen() == before {
		t.Fatal("hidden-prefix change must move the visibility generation")
	}
}
