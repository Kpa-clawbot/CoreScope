package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestTerrainConfigResolve(t *testing.T) {
	// Unset: the defaults, no warning.
	for _, c := range []*TerrainConfig{nil, {}} {
		r, warns := c.resolved()
		if r.TileURL != defaultTerrainTileURL || r.TileCacheTiles != defaultTerrainTileCache || len(warns) != 0 {
			t.Fatalf("%+v: resolved %+v, warnings %q", c, r, warns)
		}
	}
	// Valid values are kept, each end of the cache range included.
	for _, c := range []TerrainConfig{
		{TileURL: "https://tiles.example.com/skadi", TileCacheTiles: 4},
		{TileURL: "https://user:pw@tiles.example.com:8443/tok/skadi/", TileCacheTiles: 200},
	} {
		r, warns := c.resolved()
		if r != c || len(warns) != 0 {
			t.Fatalf("%+v: resolved %+v, warnings %q", c, r, warns)
		}
	}
	// Invalid values fall back, one warning each.
	const badURL = "terrain.tileUrl must be an https URL without a query; using the default"
	const badCache = "terrain.tileCacheTiles must be within 4-200; using the default"
	for _, c := range []struct {
		cfg  TerrainConfig
		warn []string
	}{
		{TerrainConfig{TileURL: "http://tiles.example.com/skadi"}, []string{badURL}},
		{TerrainConfig{TileURL: "https://tiles.example.com/skadi?key=x"}, []string{badURL}},
		{TerrainConfig{TileURL: "https:///skadi"}, []string{badURL}},
		{TerrainConfig{TileURL: "file:///etc"}, []string{badURL}},
		{TerrainConfig{TileURL: "::"}, []string{badURL}},
		{TerrainConfig{TileCacheTiles: 3}, []string{badCache}},
		{TerrainConfig{TileCacheTiles: 201}, []string{badCache}},
		{TerrainConfig{TileURL: "ftp://x", TileCacheTiles: -1}, []string{badURL, badCache}},
	} {
		r, warns := c.cfg.resolved()
		if r.TileURL != defaultTerrainTileURL || r.TileCacheTiles != defaultTerrainTileCache || !slices.Equal(warns, c.warn) {
			t.Errorf("%+v: resolved %+v, warnings %q", c.cfg, r, warns)
		}
	}
	// The block decodes from config.json, and los/planner lost their tile keys.
	var cfg Config
	raw := `{"terrain":{"tileUrl":"https://tiles.example.com/skadi","tileCacheTiles":50}}`
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Terrain == nil || cfg.Terrain.TileURL != "https://tiles.example.com/skadi" || cfg.Terrain.TileCacheTiles != 50 {
		t.Fatalf("decoded %+v", cfg.Terrain)
	}
	if b, _ := json.Marshal(LOSConfig{}); string(b) != `{"enabled":false,"frequencyMhz":0,"antennaHeightM":0,"kFactor":0}` {
		t.Errorf("los keys %s", b)
	}
	if b, _ := json.Marshal(PlannerConfig{}); strings.Contains(string(b), "tileUrl") || strings.Contains(string(b), "tileCacheTiles") {
		t.Errorf("planner keys %s", b)
	}
}

// Responses name the terrain they read: the default tiles as AWS Open Data,
// any other tile server by its host (never its path or credentials).
func TestTerrainSourceName(t *testing.T) {
	for _, c := range []struct{ url, want string }{
		{"", terrainDefaultSource},
		{defaultTerrainTileURL, "SRTM (AWS Open Data)"},
		{defaultTerrainTileURL + "/", "SRTM (AWS Open Data)"},
		{"https://user:pw@tiles.example.com:8443/tok-1/skadi", "SRTM (tiles.example.com)"},
		{"https://s3.amazonaws.com/my-bucket/skadi", "SRTM (s3.amazonaws.com)"},
	} {
		if got := terrainSourceName(c.url); got != c.want {
			t.Errorf("terrainSourceName(%q) = %q, want %q", c.url, got, c.want)
		}
	}
	if got := newSRTMSource(TerrainConfig{}, 0).source; got != terrainDefaultSource {
		t.Errorf("default source %q", got)
	}
	if got := newSRTMSource(TerrainConfig{TileURL: "https://dem.example.org/skadi/"}, 0).source; got != "SRTM (dem.example.org)" {
		t.Errorf("custom source %q", got)
	}
}

func TestTerrainViewTiles(t *testing.T) {
	on := true
	los := &LOSConfig{Enabled: on}
	small := &PlannerConfig{Enabled: on}
	big := &PlannerConfig{Enabled: on, MaxAreaKm: 200, NodeMarginKm: 50, MaxLegKm: 150}
	if small.tileLimits().max() >= losMaxTiles || func() int { r, _ := big.resolved(); return r.tileLimits().max() }() <= losMaxTiles {
		t.Fatal("the test needs one planner below losMaxTiles and one above")
	}
	for _, c := range []struct {
		name    string
		los     *LOSConfig
		planner *PlannerConfig
		want    int
	}{
		{"neither", nil, nil, 0},
		{"both disabled", &LOSConfig{}, &PlannerConfig{}, 0},
		{"los only", los, nil, losMaxTiles},
		{"planner only", nil, small, small.tileLimits().max()},
		{"both, los larger", los, small, losMaxTiles},
		{"both, planner larger", los, big, 28},
	} {
		if got := terrainViewTiles(c.los, c.planner); got != c.want {
			t.Errorf("%s: %d tiles, want %d", c.name, got, c.want)
		}
	}
}

// Review #2391 point 9: route LOS and the planner read one SRTM source, so
// a leg's terrain cannot differ between them. Its per-view cap and cache
// cover whichever of them needs more.
func TestTerrainServicesShareOneSource(t *testing.T) {
	terrainQuietLog(t)
	newSrv := func(cfg *Config) (*elevationService, *plannerService) {
		t.Helper()
		e, p := newTerrainServices(cfg, &Server{cfg: cfg})
		if p != nil {
			t.Cleanup(p.Close)
		}
		return e, p
	}

	// Both on: one source, sized for the larger of the two.
	tc := &TerrainConfig{TileURL: "https://dem.example.org/skadi", TileCacheTiles: 4}
	cfg := &Config{LOS: &LOSConfig{Enabled: true}, Planner: &PlannerConfig{Enabled: true}, Terrain: tc}
	e, p := newSrv(cfg)
	if e == nil || p == nil {
		t.Fatalf("services %v %v", e, p)
	}
	if ps, ok := p.src.(*srtmSource); !ok || ps != e.src {
		t.Fatalf("planner source %p, LOS source %p: not shared", p.src, e.src)
	}
	if e.src.maxTiles != losMaxTiles || e.src.cache.maxItems != losMaxTiles || e.src.baseURL != tc.TileURL {
		t.Fatalf("shared source: max %d, cache %d, base %s", e.src.maxTiles, e.src.cache.maxItems, e.src.baseURL)
	}
	if e.src.source != "SRTM (dem.example.org)" || p.source != e.src.source {
		t.Fatalf("attribution: LOS %q, planner %q", e.src.source, p.source)
	}
	if e.nodes == nil || e.visibility == nil || e.downloads == nil || e.downloads.ipr == nil {
		t.Fatalf("LOS service not wired: %+v", e)
	}
	// One download budget per client across both features, sized for one
	// full view of the shared source.
	if p.downloads != e.downloads || e.downloads.rl.burst != float64(e.src.maxTiles) {
		t.Fatalf("download budgets: LOS %p, planner %p, burst %v", e.downloads, p.downloads, e.downloads.rl.burst)
	}

	// A larger planner raises the shared cap and cache floor.
	big := &PlannerConfig{Enabled: true, MaxAreaKm: 200, NodeMarginKm: 50, MaxLegKm: 150}
	e, p = newSrv(&Config{LOS: &LOSConfig{Enabled: true}, Planner: big})
	if e.src != p.src.(*srtmSource) || e.src.maxTiles != 28 || e.src.cache.maxItems != 28 {
		t.Fatalf("big planner: max %d, cache %d", e.src.maxTiles, e.src.cache.maxItems)
	}
	if e.src.source != terrainDefaultSource {
		t.Fatalf("default attribution %q", e.src.source)
	}

	// One feature: the other service is nil.
	if e, p := newSrv(&Config{LOS: &LOSConfig{Enabled: true}}); e == nil || p != nil || e.src.maxTiles != losMaxTiles {
		t.Fatalf("los only: %v %v", e, p)
	}
	if e, p := newSrv(&Config{Planner: &PlannerConfig{Enabled: true}}); e != nil || p == nil ||
		p.src.(*srtmSource).maxTiles != (PlannerConfig{}).tileLimits().max() {
		t.Fatalf("planner only: %v %v", e, p)
	}
	if e, p := newSrv(&Config{Terrain: tc}); e != nil || p != nil {
		t.Fatalf("neither: %v %v", e, p)
	}
}

// Review #2391: a terrain cache smaller than one view evicted the view's
// own tiles while it loaded, so the next request on the same area downloaded
// them again. The configured size is still validated as 4-200, but the
// cache in use never holds fewer tiles than one full view.
func TestTerrainCacheHoldsOneView(t *testing.T) {
	view := PlannerConfig{}.tileLimits().max()
	if view <= 4 {
		t.Fatalf("one view is %d tiles: the test needs it above the configurable minimum", view)
	}
	for _, tc := range []struct{ configured, want int }{
		{0, defaultTerrainTileCache}, {4, view}, {view - 1, view}, {view, view},
		{defaultTerrainTileCache, defaultTerrainTileCache}, {200, 200},
	} {
		c := TerrainConfig{TileCacheTiles: tc.configured}
		if got := c.cacheTiles(view); got != tc.want {
			t.Errorf("tileCacheTiles=%d: cache %d, want %d", tc.configured, got, tc.want)
		}
		if got := newSRTMSource(c, view).cache.maxItems; got != tc.want {
			t.Errorf("tileCacheTiles=%d: source capacity %d, want %d", tc.configured, got, tc.want)
		}
	}

	// The raise is logged at startup, once; a large enough cache is not.
	var buf bytes.Buffer
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(flags) })
	planner := &PlannerConfig{Enabled: true}
	logTerrainConfig(&TerrainConfig{TileCacheTiles: 4}, nil, planner)
	raised := fmt.Sprintf("tileCacheTiles=4 is below one full view; keeping %d tiles resident", view)
	if out := buf.String(); strings.Count(out, raised) != 1 || !strings.Contains(out, fmt.Sprintf("cache %d tiles", view)) {
		t.Fatalf("log %q", out)
	}
	buf.Reset()
	logTerrainConfig(nil, nil, planner)
	if out := buf.String(); strings.Contains(out, "below one full view") {
		t.Fatalf("log %q", out)
	}

	// End to end: with tileCacheTiles 4, a 3×4-tile view stays resident
	// after it loads, and loading it again downloads nothing.
	box := geoBBox{S: 49.5, W: 2.5, N: 51.5, E: 5.5}
	sp, _ := srtmSpanOf(box)
	if sp.count() != 12 || sp.count() > view {
		t.Fatalf("test box spans %d tiles, one view %d", sp.count(), view)
	}
	tile := srtmConstTile(t, 100, srtmRawBytes, gzip.BestSpeed)
	tiles := map[string][]byte{}
	for i := 0; i < sp.count(); i++ {
		tiles[srtmPath(sp.key(i))] = tile
	}
	s, u, _ := newSRTMTest(t, 4, srtmServe(tiles))
	srtmMustView(t, s, box)
	if !s.Resident(box) || srtmCacheLen(s) != sp.count() {
		t.Fatalf("after one view: resident %v, %d tiles cached", s.Resident(box), srtmCacheLen(s))
	}
	srtmMustView(t, s, box)
	if n := u.total(); n != sp.count() {
		t.Fatalf("two views of %d tiles made %d requests", sp.count(), n)
	}
}

func TestTerrainConfigLog(t *testing.T) {
	var buf bytes.Buffer
	prev, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev); log.SetFlags(flags) })
	logTerrainConfig(&TerrainConfig{TileURL: "https://user:s3cret@tiles.example.com/tok-123/skadi", TileCacheTiles: 3},
		&LOSConfig{Enabled: true}, &PlannerConfig{Enabled: true})
	out := buf.String()
	for _, want := range []string{
		"terrain.tileCacheTiles must be within 4-200; using the default",
		"[terrain] SRTM tiles from https://tiles.example.com for route LOS and the planner: up to 16 tiles per view, cache 24 tiles (69 MB, 2.9 MB a tile); " +
			"views in use can briefly keep about 20 evicted tiles more (58 MB, a minute of downloads)",
		"[terrain] tile downloads: 20/min and 200/hour for everyone; per client, 16 at once, then 10/min, at most 40/hour",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	// The path or userinfo of a self-hosted tile server may carry a token.
	for _, leak := range []string{"user", "s3cret", "tok-123", "skadi"} {
		if strings.Contains(out, leak) {
			t.Fatalf("log leaks %q: %q", leak, out)
		}
	}
	if got := terrainTileHost("not a url"); got != "(invalid)" {
		t.Fatalf("terrainTileHost = %q", got)
	}
	buf.Reset()
	logTerrainConfig(nil, &LOSConfig{Enabled: true}, nil)
	if out := buf.String(); !strings.Contains(out, "for route LOS: up to 16 tiles per view") {
		t.Fatalf("los-only log %q", out)
	}
}

// Security review (#2391): the per-client budget is one full view of the
// shared source, so any single request can load at once, refills at half the
// global rate, and is capped per hour well below the global hourly budget.
func TestTerrainClientBudget(t *testing.T) {
	terrainQuietLog(t)
	for _, los := range []*LOSConfig{nil, {Enabled: true}} {
		for _, sz := range plannerTestSizes {
			planner := &PlannerConfig{Enabled: true, MaxAreaKm: sz.area, NodeMarginKm: sz.margin, MaxLegKm: sz.leg}
			view := planner.tileLimits().max()
			if los.enabled() {
				view = max(view, losMaxTiles)
			}
			burst, hourly := terrainClientBudget(terrainViewTiles(los, planner))
			if burst != view || hourly != max(terrainClientHourFloor, 2*view) {
				t.Fatalf("los %v, %+v: burst %d, hourly %d; largest view %d", los.enabled(), sz, burst, hourly, view)
			}
			// A token bucket lets a client take its cap and one refill in any
			// hour: that still leaves part of the global hour to others.
			if global := srtmBudgetFor(view).perHour; 2*hourly >= global {
				t.Fatalf("%+v: a client may take %d of the %d downloads an hour", sz, 2*hourly, global)
			}
			cfg := &Config{LOS: los, Planner: planner}
			e, p := newTerrainServices(cfg, &Server{cfg: cfg})
			t.Cleanup(p.Close)
			rl := p.downloads.rl
			if (e != nil && e.downloads != p.downloads) || rl.burst != float64(view) || rl.capBurst != float64(hourly) ||
				math.Abs(rl.rate*60-terrainClientTilesPerMin) > 1e-9 || math.Abs(rl.capRate*3600-float64(hourly)) > 1e-9 {
				t.Fatalf("los %v, %+v: limiter %+v", los.enabled(), sz, rl)
			}
			if ok, _ := p.downloads.rl.takeN("198.51.100.1", view); !ok {
				t.Fatalf("%+v: the largest view does not fit a fresh client's budget", sz)
			}
		}
	}
}

// Security review (#2391): route LOS and the planner draw on one budget per
// client, so using both buys no more downloads, and the hourly cap holds
// while the per-minute bucket refills.
func TestTerrainClientHourlyCapAcrossFeatures(t *testing.T) {
	terrainQuietLog(t)
	tb := newTerrainBoth(t, srtmServe(nil)) // every tile 404s: cheap, one request each
	_, hourly := terrainClientBudget(losMaxTiles)
	if hourly != 40 || tb.p.downloads.rl.burst != losMaxTiles {
		t.Fatalf("budget: hourly %d, burst %v", hourly, tb.p.downloads.rl.burst)
	}
	// route is one leg per tile along latitude lat+0.5: n+1 tiles.
	route := func(lat, n int) string {
		parts := make([]string, n+1)
		for i := range parts {
			parts[i] = fmt.Sprintf("%d.5,%d.5", lat, i)
		}
		return strings.Join(parts, ",")
	}
	// area is a 2×2-tile planner profile view at lat, lat+1 × 5, 6.
	area := func(lat int) string { return fmt.Sprintf("%d.8,5.8,%d.2,6.2", lat, lat+1) }
	tb.known(area(50), area(52), area(54), area(56))
	const ip, other = "198.51.100.160", "198.51.100.161"
	los := func(pts, from string, want int) {
		t.Helper()
		if w := tb.getFrom(pts, from+":5000", nil); w.Code != want {
			t.Fatalf("route LOS %s: %d, want %d: %s", pts, w.Code, want, w.Body.String())
		}
	}
	minute := func() { // refills 10 per-minute tokens, 0.67 hourly ones
		tb.clk.add(time.Minute)
		tb.p.downloads.rl.takeN(ip, 0)
	}

	los(route(40, 15), ip, http.StatusOK) // 16: the burst
	for i, lat := range []int{50, 52} {
		minute()
		tb.profileDone(t, area(lat), ip)       // 4
		los(route(45+i, 5), ip, http.StatusOK) // 6
	}
	minute()
	tb.profileDone(t, area(54), ip) // 4: 40 in all, the hourly cap
	if got := tb.up.total(); got != hourly {
		t.Fatalf("%d downloads, want %d", got, hourly)
	}
	// The minute bucket has room for 6 more; the hourly cap has 2.
	if got := tb.tokens(ip); got < 6 {
		t.Fatalf("per-minute tokens %v: the test needs room in them", got)
	}
	los(route(47, 5), ip, http.StatusTooManyRequests)
	minute()
	if w := tb.profile(area(56), ip); w.Code != http.StatusTooManyRequests {
		t.Fatalf("planner past the hourly cap: %d %s", w.Code, w.Body.String())
	}
	// Downloads are capped per client, not for everyone.
	los(route(47, 5), other, http.StatusOK)
	if got := tb.up.total(); got != hourly+6 {
		t.Fatalf("%d downloads, want %d", got, hourly+6)
	}
}

// terrainQuietLog discards the log for the rest of t.
func terrainQuietLog(t *testing.T) {
	prev := log.Writer()
	log.SetOutput(&bytes.Buffer{})
	t.Cleanup(func() { log.SetOutput(prev) })
}
