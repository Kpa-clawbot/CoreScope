package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strings"
	"testing"
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
	if e.nodes == nil || e.visibility == nil || e.ipr == nil || e.rl == nil || e.rl.burst != losClientTilesPerMin {
		t.Fatalf("LOS service not wired: %+v", e)
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
		"[terrain] SRTM tiles from https://tiles.example.com for route LOS and the planner: up to 16 tiles per view, cache 24 tiles (up to 69 MB, 2.9 MB a tile)",
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

// terrainQuietLog discards the log for the rest of t.
func terrainQuietLog(t *testing.T) {
	prev := log.Writer()
	log.SetOutput(&bytes.Buffer{})
	t.Cleanup(func() { log.SetOutput(prev) })
}
