package main

import (
	"log"
	"net/url"
)

func (c *PlannerConfig) enabled() bool { return c != nil && c.Enabled }

// resolved applies defaults; invalid values fall back with a warning.
func (c *PlannerConfig) resolved() (PlannerConfig, []string) {
	out := PlannerConfig{
		TileURL:          defaultPlannerTileURL,
		TileCacheTiles:   defaultPlannerTileCache,
		DefaultRangeKm:   defaultPlannerRangeKm,
		NewAntennaM:      defaultPlannerNewAntennaM,
		ExistingAntennaM: defaultPlannerExistAntenna,
	}
	if c == nil {
		return out, nil
	}
	var warns []string
	out.Enabled = c.Enabled
	if c.TileURL != "" {
		if u, err := url.Parse(c.TileURL); err == nil && u.Scheme == "https" && u.Host != "" && u.RawQuery == "" {
			out.TileURL = c.TileURL
		} else {
			warns = append(warns, "planner.tileUrl must be an https URL without a query; using the default")
		}
	}
	if c.TileCacheTiles != 0 {
		if c.TileCacheTiles >= 4 && c.TileCacheTiles <= 200 {
			out.TileCacheTiles = c.TileCacheTiles
		} else {
			warns = append(warns, "planner.tileCacheTiles must be within 4-200; using the default")
		}
	}
	if c.DefaultRangeKm != 0 {
		if plannerRangeAllowed(c.DefaultRangeKm) {
			out.DefaultRangeKm = c.DefaultRangeKm
		} else {
			warns = append(warns, "planner.defaultRangeKm must be one of 10,15,20,30,40,50,60; using the default")
		}
	}
	if c.NewAntennaM != 0 {
		if c.NewAntennaM > 0 && c.NewAntennaM <= 100 {
			out.NewAntennaM = c.NewAntennaM
		} else {
			warns = append(warns, "planner.newAntennaM must be within 0-100; using the default")
		}
	}
	if c.ExistingAntennaM != 0 {
		if c.ExistingAntennaM > 0 && c.ExistingAntennaM <= 100 {
			out.ExistingAntennaM = c.ExistingAntennaM
		} else {
			warns = append(warns, "planner.existingAntennaM must be within 0-100; using the default")
		}
	}
	return out, warns
}

// tileLimits is the per-endpoint terrain tile caps for c's size limits.
func (c PlannerConfig) tileLimits() plannerTileLimits {
	return plannerTileCaps(plannerMaxAreaKm, plannerNodeMarginKm, plannerMaxLegKm)
}

// tileCache is the terrain cache size in tiles: TileCacheTiles (0 = the
// default), raised to one full view when it is smaller. A smaller cache
// would evict a view's own tiles while loading it, so the next job on the
// same area downloads them again.
func (c PlannerConfig) tileCache() int {
	n := c.TileCacheTiles
	if n <= 0 {
		n = defaultPlannerTileCache
	}
	return max(n, c.tileLimits().max())
}

func plannerRangeAllowed(km float64) bool {
	for _, r := range plannerRangesKm {
		if r == km {
			return true
		}
	}
	return false
}

// clientConfig is /api/config/client "planner"; nil when disabled. Radio
// defaults come from the los block (frequency, k) so both tools agree.
func (c *PlannerConfig) clientConfig(los *LOSConfig) *PlannerClientConfig {
	if !c.enabled() {
		return nil
	}
	r, _ := c.resolved()
	l, _ := los.resolved()
	return &PlannerClientConfig{
		DefaultRangeKm:   r.DefaultRangeKm,
		RangesKm:         plannerRangesKm,
		NewAntennaM:      r.NewAntennaM,
		ExistingAntennaM: r.ExistingAntennaM,
		FrequencyMHz:     l.FrequencyMHz,
		KFactor:          l.KFactor,
		MaxPoints:        plannerMaxPoints,
		MaxLegKm:         plannerMaxLegKm,
		MaxAreaKm:        plannerMaxAreaKm,
		MaxSites:         plannerMaxSites,
		MinClearance:     plannerMinClearance,
	}
}

func logPlannerConfig(c *PlannerConfig) {
	if !c.enabled() {
		return
	}
	r, warns := c.resolved()
	for _, w := range warns {
		log.Printf("[planner] %s", w)
	}
	cache := r.tileCache()
	if cache > r.TileCacheTiles {
		log.Printf("[planner] tileCacheTiles=%d is below one full view; keeping %d tiles resident",
			r.TileCacheTiles, cache)
	}
	log.Printf("[planner] enabled (tiles=%s, cache=%d tiles, range=%.0f km, new=%.0f m, existing=%.0f m)",
		plannerTileHost(r.TileURL), cache, r.DefaultRangeKm, r.NewAntennaM, r.ExistingAntennaM)
}

// plannerTileHost is the tile URL reduced to scheme and host for logs: a
// self-hosted tile server's path or userinfo may carry a token.
func plannerTileHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(invalid)"
	}
	return u.Scheme + "://" + u.Host
}
