package main

// Terrain for route line-of-sight (elevation.go) and the repeater planner
// (planner_api.go). Both read ONE SRTM tile source (terrain_srtm.go), so a
// leg's terrain, and with it its verdict, is the same in both views. The
// source exists only while los or planner is enabled; the "terrain" block has
// no switch of its own.

import (
	"log"
	"net/url"
	"strings"
	"time"
)

// TerrainConfig is the "terrain" block in config.json.
type TerrainConfig struct {
	// TileURL is the base of an SRTM "skadi" tile tree:
	// <TileURL>/N30/N30W098.hgt.gz, https without a query. Default AWS Open
	// Data elevation-tiles-prod.
	TileURL string `json:"tileUrl"`
	// TileCacheTiles caps resident downsampled tiles (~2.9 MB each), 4-200.
	// The effective cache never goes below one full view (cacheTiles).
	TileCacheTiles int `json:"tileCacheTiles"`
}

const (
	defaultTerrainTileURL   = "https://s3.amazonaws.com/elevation-tiles-prod/skadi"
	defaultTerrainTileCache = 24
	// terrainDefaultSource attributes the default tiles in responses.
	terrainDefaultSource = "SRTM (AWS Open Data)"

	// terrainClientTilesPerMin refills a client's tile download budget
	// (terrainClientBudget), half the global srtmPerMinute: one client
	// cannot hold the shared budget on its own.
	terrainClientTilesPerMin = 10
	// terrainClientHourFloor is the least a client may download in an hour.
	terrainClientHourFloor = 40
)

// terrainClientBudget is one client's tile download budget, shared by route
// LOS and the planner, for a source whose largest view is viewTiles
// (terrainViewTiles): burst tokens, so any single request can load in one
// go, refilled at terrainClientTilesPerMin, under an hourly cap of hourly
// tokens refilled at hourly an hour. hourly stays below the global
// srtmPerHour at every size the planner allows (40 tiles: 80 of 200).
func terrainClientBudget(viewTiles int) (burst, hourly int) {
	return viewTiles, max(terrainClientHourFloor, 2*viewTiles)
}

// newTerrainClientLimiter builds the per-client terrain download limiter
// route LOS and the planner share (newTerrainServices): one token per tile
// download a request or job starts, keyed like every other per-client limit
// (clientKey).
func newTerrainClientLimiter(trustedProxies []string, viewTiles int) *clientLimiter {
	burst, hourly := terrainClientBudget(viewTiles)
	return &clientLimiter{
		ipr: &wsLimiter{state: map[string]*wsIPState{},
			trustedProxies: parseCIDRList(trustedProxies, "webSocket.trustedProxies")},
		rl:  newCappedRateLimiter(burst, terrainClientTilesPerMin, time.Minute, hourly, time.Hour),
		tag: "terrain", what: "terrain download",
	}
}

// resolved applies defaults; invalid values fall back with a warning.
func (c *TerrainConfig) resolved() (TerrainConfig, []string) {
	out := TerrainConfig{TileURL: defaultTerrainTileURL, TileCacheTiles: defaultTerrainTileCache}
	if c == nil {
		return out, nil
	}
	var warns []string
	if c.TileURL != "" {
		if u, err := url.Parse(c.TileURL); err == nil && u.Scheme == "https" && u.Host != "" && u.RawQuery == "" {
			out.TileURL = c.TileURL
		} else {
			warns = append(warns, "terrain.tileUrl must be an https URL without a query; using the default")
		}
	}
	if c.TileCacheTiles != 0 {
		if c.TileCacheTiles >= 4 && c.TileCacheTiles <= 200 {
			out.TileCacheTiles = c.TileCacheTiles
		} else {
			warns = append(warns, "terrain.tileCacheTiles must be within 4-200; using the default")
		}
	}
	return out, warns
}

// cacheTiles is the cache size in tiles: TileCacheTiles (0 = the default),
// raised to viewTiles when it is smaller. A smaller cache would evict a
// request's own tiles while loading them, so the next request downloads them
// again.
func (c TerrainConfig) cacheTiles(viewTiles int) int {
	n := c.TileCacheTiles
	if n <= 0 {
		n = defaultTerrainTileCache
	}
	return max(n, viewTiles)
}

// terrainSourceName is the attribution every terrain response carries:
// terrainDefaultSource for the default tiles, else "SRTM (<tile host>)".
func terrainSourceName(tileURL string) string {
	if tileURL == "" || strings.TrimRight(tileURL, "/") == defaultTerrainTileURL {
		return terrainDefaultSource
	}
	u, err := url.Parse(tileURL)
	if err != nil || u.Hostname() == "" {
		return "SRTM"
	}
	return "SRTM (" + u.Hostname() + ")"
}

// terrainViewTiles is the most tiles one load of the shared source may span:
// the largest planner cap (planner on) and losMaxTiles (los on); 0 when both
// are off. It is the source's per-view cap and its cache floor.
func terrainViewTiles(los *LOSConfig, planner *PlannerConfig) int {
	n := 0
	if los.enabled() {
		n = losMaxTiles
	}
	if planner.enabled() {
		r, _ := planner.resolved()
		n = max(n, r.tileLimits().max())
	}
	return n
}

// terrainUsers names the features reading the source, for the startup log.
func terrainUsers(los *LOSConfig, planner *PlannerConfig) string {
	switch {
	case los.enabled() && planner.enabled():
		return "route LOS and the planner"
	case los.enabled():
		return "route LOS"
	}
	return "the planner"
}

// logTerrainConfig reports the effective source once at startup.
func logTerrainConfig(c *TerrainConfig, los *LOSConfig, planner *PlannerConfig) {
	r, warns := c.resolved()
	for _, w := range warns {
		log.Printf("[terrain] %s", w)
	}
	views := terrainViewTiles(los, planner)
	cache := r.cacheTiles(views)
	if cache > r.TileCacheTiles {
		log.Printf("[terrain] tileCacheTiles=%d is below one full view; keeping %d tiles resident",
			r.TileCacheTiles, cache)
	}
	log.Printf("[terrain] SRTM tiles from %s for %s: up to %d tiles per view, cache %d tiles (up to %.0f MB, %.1f MB a tile)",
		terrainTileHost(r.TileURL), terrainUsers(los, planner), views, cache,
		float64(cache)*srtmTileBytes/1e6, float64(srtmTileBytes)/1e6)
	burst, hourly := terrainClientBudget(views)
	log.Printf("[terrain] tile downloads: %d/min and %d/hour for everyone; per client, %d at once, "+
		"then %d/min, at most %d/hour",
		srtmPerMinute, srtmPerHour, burst, terrainClientTilesPerMin, hourly)
}

// terrainTileHost is the tile URL reduced to scheme and host for logs: a
// self-hosted tile server's path or userinfo may carry a token.
func terrainTileHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(invalid)"
	}
	return u.Scheme + "://" + u.Host
}

// newTerrainServices builds the services los and planner enable over one
// shared SRTM source, and logs the effective config of each once. A service
// whose feature is off is nil; with both off no source is built.
func newTerrainServices(cfg *Config, s *Server) (*elevationService, *plannerService) {
	if !cfg.LOS.enabled() && !cfg.Planner.enabled() {
		return nil, nil
	}
	logTerrainConfig(cfg.Terrain, cfg.LOS, cfg.Planner)
	tc, _ := cfg.Terrain.resolved()
	views := terrainViewTiles(cfg.LOS, cfg.Planner)
	src := newSRTMSource(tc, views)
	// One budget per client across both features: a client cannot get twice
	// the downloads by using both.
	downloads := newTerrainClientLimiter(cfg.WSTrustedProxies(), views)

	var elev *elevationService
	if cfg.LOS.enabled() {
		logLOSConfig(cfg.LOS)
		elev = newElevationService(src)
		elev.nodes = serverPlannerData{s}.nodes
		elev.visibility = s
		elev.downloads = downloads
	}
	var planner *plannerService
	if cfg.Planner.enabled() {
		logPlannerConfig(cfg.Planner)
		pc, _ := cfg.Planner.resolved()
		planner = newPlannerService(pc, s, src)
		planner.downloads = downloads
	}
	return elev, planner
}
