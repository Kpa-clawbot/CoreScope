package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// visTestPolygon is a geo_filter around lat 30..31, lon -98..-97.
func visTestPolygon() *GeoFilterConfig {
	return &GeoFilterConfig{Polygon: [][2]float64{{30, -98}, {30, -97}, {31, -97}, {31, -98}}}
}

func TestNodeVisibilityVisible(t *testing.T) {
	cfg := &Config{NodeBlacklist: []string{"BAD1"}, HiddenNamePrefixes: []string{"zz-"}}
	v := nodeVisibility{cfg: cfg, geo: visTestPolygon()}
	cases := []struct {
		name     string
		pubkey   string
		nodeName string
		lat, lon float64
		foreign  bool
		want     bool
	}{
		{"visible inside the geo_filter", "aa01", "Hill", 30.5, -97.5, false, true},
		{"blacklisted, any case", "bad1", "Hill", 30.5, -97.5, false, false},
		{"hidden name prefix", "aa02", "zz-secret", 30.5, -97.5, false, false},
		{"outside the geo_filter", "aa03", "Far", 40, -80, false, false},
		{"outside the geo_filter but foreign (#730)", "aa04", "Bridged", 40, -80, true, true},
		{"foreign does not spare the blacklist", "bad1", "Bridged", 40, -80, true, false},
		{"foreign does not spare a hidden name", "aa05", "zz-bridged", 40, -80, true, false},
		{"no position fix passes the geo_filter", "aa06", "Nowhere", 0, 0, false, true},
		{"empty pubkey is never blacklisted", "", "Anon", 30.5, -97.5, false, true},
	}
	for _, tc := range cases {
		if got := v.Visible(tc.pubkey, tc.nodeName, tc.lat, tc.lon, tc.foreign); got != tc.want {
			t.Errorf("%s: Visible=%v want %v", tc.name, got, tc.want)
		}
	}
	if !v.filters() {
		t.Error("configured rules: filters() = false")
	}

	// No rules (and the zero value): everything is visible and nothing filters.
	for _, none := range []nodeVisibility{{}, {cfg: &Config{}}} {
		if !none.Visible("bad1", "zz-x", 40, -80, false) || none.filters() {
			t.Errorf("no rules %+v hid a node or reports filtering", none)
		}
	}
	// Each rule alone turns filtering on.
	for name, one := range map[string]nodeVisibility{
		"blacklist": {cfg: &Config{NodeBlacklist: []string{"x"}}},
		"hidden":    {cfg: &Config{HiddenNamePrefixes: []string{"x"}}},
		"geo":       {geo: visTestPolygon()},
	} {
		if !one.filters() {
			t.Errorf("%s alone: filters() = false", name)
		}
	}
}

func TestNodeVisibilityInfoRule(t *testing.T) {
	v := nodeVisibility{cfg: &Config{NodeBlacklist: []string{"bad1"}}, geo: visTestPolygon()}
	rule := v.infoRule(map[string]struct{}{"for1": {}, "bad1": {}})
	cases := []struct {
		n    nodeInfo
		want bool
	}{
		{nodeInfo{PublicKey: "in1", Lat: 30.5, Lon: -97.5, HasGPS: true}, true},
		{nodeInfo{PublicKey: "out1", Lat: 40, Lon: -80, HasGPS: true}, false},
		{nodeInfo{PublicKey: "for1", Lat: 40, Lon: -80, HasGPS: true}, true},
		{nodeInfo{PublicKey: "bad1", Lat: 40, Lon: -80, HasGPS: true}, false},
		// No fix: the position is ignored, as /api/nodes ignores a NULL one.
		{nodeInfo{PublicKey: "nofix", Lat: 40, Lon: -80}, true},
	}
	for _, tc := range cases {
		if got := rule(&tc.n); got != tc.want {
			t.Errorf("%s: %v want %v", tc.n.PublicKey, got, tc.want)
		}
	}
	// A nil foreign set spares nobody.
	if v.infoRule(nil)(&nodeInfo{PublicKey: "for1", Lat: 40, Lon: -80, HasGPS: true}) {
		t.Error("nil foreign set spared a node outside the geo_filter")
	}
}

func TestNodeVisibilityGen(t *testing.T) {
	cfg := &Config{}
	srv := &Server{cfg: cfg}
	g := srv.nodeVisibilityGen()
	if srv.nodeVisibilityGen() != g {
		t.Fatal("generation changed with no rule change")
	}
	// In order: removing the geo filter is only a change once one is set.
	for _, c := range []struct {
		name   string
		change func()
	}{
		{"blacklist", func() { cfg.SetNodeBlacklist([]string{"x"}) }},
		{"hidden", func() { cfg.SetHiddenNamePrefixes([]string{"x"}) }},
		{"geo filter", func() { srv.setGeoFilter(visTestPolygon()) }},
		{"geo filter removed", func() { srv.setGeoFilter(nil) }},
	} {
		before := srv.nodeVisibilityGen()
		c.change()
		if srv.nodeVisibilityGen() == before {
			t.Errorf("%s change kept the generation", c.name)
		}
	}
	var nilSrv *Server
	if nilSrv.nodeVisibilityGen() != "" || (&Server{}).nodeVisibilityGen() != "" {
		t.Fatal("configless server has a generation")
	}
	if v := nilSrv.nodeVisibility(); v.filters() {
		t.Fatal("nil server filters")
	}
}

// visNode is one node of the mixed fixture and whether /api/nodes shows it.
type visNode struct {
	pubkey, name string
	lat, lon     float64 // 0,0: stored as NULL
	foreign      bool
	visible      bool
}

// visFixtureNodes covers every rule alone and foreign against each rule.
// Positions are distinct at elevKey's 5 decimals.
var visFixtureNodes = []visNode{
	{"vis00001", "Visible", 30.40001, -97.50001, false, true},
	{"bad00001", "Banned", 30.40002, -97.50002, false, false},
	{"hid00001", "zz-secret", 30.40003, -97.50003, false, false},
	{"out00001", "Outside", 32.50001, -97.50001, false, false},
	{"for00001", "Bridged", 32.50002, -97.50002, true, true},
	{"fbl00001", "Bridged banned", 32.50003, -97.50003, true, false},
	{"fhd00001", "zz-bridged", 32.50004, -97.50004, true, false},
	{"vis00002", "Visible room", 30.40004, -97.50004, false, true},
	{"nofix001", "No fix", 0, 0, false, true},
}

// visibilityFixture is a server whose DB holds visFixtureNodes (all
// repeaters, newest first in fixture order) and whose rules hide the
// bad*/zz-*/out* ones; rules=false leaves the rules unset.
func visibilityFixture(t *testing.T, rules bool) (*Server, *mux.Router) {
	t.Helper()
	srv, router := setupTestServer(t)
	base := time.Now().UTC().Add(-time.Minute)
	for i, n := range visFixtureNodes {
		var lat, lon interface{}
		if n.lat != 0 || n.lon != 0 {
			lat, lon = n.lat, n.lon
		}
		foreign := 0
		if n.foreign {
			foreign = 1
		}
		seen := base.Add(-time.Duration(i) * time.Second).Format(time.RFC3339)
		if _, err := srv.db.conn.Exec(`INSERT INTO nodes
			(public_key, name, role, lat, lon, last_seen, first_seen, advert_count, foreign_advert)
			VALUES (?, ?, 'repeater', ?, ?, ?, ?, 1, ?)`, n.pubkey, n.name, lat, lon, seen, seen, foreign); err != nil {
			t.Fatal(err)
		}
	}
	srv.store.InvalidateNodeCache()
	if rules {
		setVisibilityRules(srv)
	}
	return srv, router
}

func setVisibilityRules(srv *Server) {
	srv.cfg.SetNodeBlacklist([]string{"BAD00001", "fbl00001"})
	srv.cfg.SetHiddenNamePrefixes([]string{"zz-"})
	srv.setGeoFilter(visTestPolygon())
}

// fixtureKeys keeps, in order, the pubkeys from pks of fixture nodes that
// keep accepts.
func fixtureKeys(pks []string, keep func(visNode) bool) []string {
	var out []string
	for _, pk := range pks {
		for _, n := range visFixtureNodes {
			if n.pubkey == pk && keep(n) {
				out = append(out, pk)
			}
		}
	}
	return out
}

func anyVisNode(visNode) bool       { return true }
func visNodeHasFix(n visNode) bool  { return n.lat != 0 || n.lon != 0 }
func visNodeVisible(n visNode) bool { return n.visible }

type visNodesPage struct {
	Nodes []struct {
		PublicKey string `json:"public_key"`
	} `json:"nodes"`
	Total int `json:"total"`
}

func apiNodeKeys(t *testing.T, router http.Handler) ([]string, visNodesPage) {
	t.Helper()
	req := httptest.NewRequest("GET", "/api/nodes?limit=500", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("/api/nodes: %d %s", w.Code, w.Body.String())
	}
	var page visNodesPage
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	var pks []string
	for _, n := range page.Nodes {
		pks = append(pks, n.PublicKey)
	}
	return pks, page
}

// /api/nodes applies all three rules (and the foreign exemption) in one
// pass: same rows, same order as the unfiltered page, total = rows kept.
func TestHandleNodesAppliesAllVisibilityRules(t *testing.T) {
	srv, router := visibilityFixture(t, false)
	all, page := apiNodeKeys(t, router)
	if page.Total != len(all) {
		t.Fatalf("unfiltered total %d, %d rows", page.Total, len(all))
	}
	if len(fixtureKeys(all, anyVisNode)) != len(visFixtureNodes) {
		t.Fatalf("unfiltered page misses fixture nodes: %v", all)
	}
	want := fixtureKeys(all, visNodeVisible)

	setVisibilityRules(srv)
	got, page := apiNodeKeys(t, router)
	if kept := fixtureKeys(got, anyVisNode); !slices.Equal(kept, want) {
		t.Fatalf("visible fixture nodes %v, want %v", kept, want)
	}
	if page.Total != len(got) {
		t.Fatalf("total %d, want the %d rows kept", page.Total, len(got))
	}
}

// The planner honours the foreign exemption /api/nodes applies (#730): a
// foreign node outside the geo_filter is usable, one that is not foreign is
// not.
func TestPlannerHonoursForeignExemption(t *testing.T) {
	srv, _ := visibilityFixture(t, true)
	all, _ := srv.store.getCachedNodesAndPM()
	d := serverPlannerData{srv}
	area := geoBBox{S: 32.4, W: -97.6, N: 32.6, E: -97.4}
	sel, _ := selectPlannerNodes(all, nil, area, 0, d.filter(), time.Now())
	var got []string
	for _, n := range sel {
		got = append(got, n.PubKey)
	}
	if !slices.Equal(got, []string{"for00001"}) {
		t.Fatalf("planner nodes outside the geo_filter: %v, want only the foreign one", got)
	}
}

// Regression for review #2391 point 4: /api/nodes, the planner and the
// route-LOS elevation proxy agree on which nodes exist.
func TestNodeVisibilityAgreesAcrossSurfaces(t *testing.T) {
	srv, router := visibilityFixture(t, true)
	all, _ := srv.store.getCachedNodesAndPM()

	// Only nodes with a position can reach the planner or the proxy.
	sorted := func(pks []string) []string {
		out := fixtureKeys(pks, visNodeHasFix)
		slices.Sort(out)
		return out
	}
	var want []string
	for _, n := range visFixtureNodes {
		if n.visible && visNodeHasFix(n) {
			want = append(want, n.pubkey)
		}
	}
	slices.Sort(want)

	// /api/nodes
	apiKeys, _ := apiNodeKeys(t, router)
	if got := sorted(apiKeys); !slices.Equal(got, want) {
		t.Fatalf("/api/nodes %v, want %v", got, want)
	}

	// Planner: the node selection and the known set behind its point checks.
	d := serverPlannerData{srv}
	now := time.Now()
	sel, _ := selectPlannerNodes(all, nil, geoBBox{S: 29, W: -99, N: 34, E: -96}, 0, d.filter(), now)
	var selKeys []string
	for _, n := range sel {
		selKeys = append(selKeys, n.PubKey)
	}
	if got := sorted(selKeys); !slices.Equal(got, want) {
		t.Fatalf("planner nodes %v, want %v", got, want)
	}
	var knownKeys []string
	for _, k := range buildPlannerKnown(all, d.filter(), d.filterGen(), now).infra {
		knownKeys = append(knownKeys, k.key)
	}
	if got := sorted(knownKeys); !slices.Equal(got, want) {
		t.Fatalf("planner known set %v, want %v", got, want)
	}

	// Elevation proxy: only visible nodes' positions are valid endpoints.
	svc := newElevationService(nil)
	svc.nodes = func() []nodeInfo { ns, _ := srv.store.getCachedNodesAndPM(); return ns }
	svc.visibility = srv
	var elevKeys []string
	for _, n := range visFixtureNodes {
		if visNodeHasFix(n) && svc.known(elevKey(geoPoint{n.lat, n.lon})) {
			elevKeys = append(elevKeys, n.pubkey)
		}
	}
	if got := sorted(elevKeys); !slices.Equal(got, want) {
		t.Fatalf("elevation endpoints %v, want %v", got, want)
	}
}
