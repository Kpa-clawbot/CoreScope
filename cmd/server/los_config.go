package main

// Radio line-of-sight (LOS) for packet routes on the map. OPT-IN, like
// rfParams (#1268): without los.enabled the server fetches nothing,
// /api/elevation/profile answers 404 and /api/config/client omits "los", so
// the frontend shows no LOS UI at all.
//
// The server only supplies terrain elevation along each leg (elevation.go),
// read from the SRTM tiles the planner uses (terrain_config.go: one shared
// source, in memory, never written to disk or the DB). Earth curvature, the
// Fresnel zone and the verdict are computed in the browser (public/los.js) so
// the antenna-height and frequency inputs re-render instantly.

import (
	"log"
	"time"
)

const (
	defaultLOSFrequencyMHz = 910.525
	defaultLOSAntennaM     = 5.0
	defaultLOSKFactor      = 4.0 / 3.0

	// losMaxPoints caps the route points in one profile request (so at most
	// losMaxPoints-1 legs).
	losMaxPoints = 16
	// losMaxLegKm: longer legs are not real LoRa links worth a profile; they
	// come back with error "too_long" and load no terrain.
	losMaxLegKm = 300.0
	// losMaxTiles caps the distinct 1° SRTM tiles one profile request may
	// load; a leg that would take the request past it is "too_many_tiles".
	// A 300 km leg's samples fall in about 10 tiles within the SRTM band. The
	// shared source keeps at least this many resident (terrainViewTiles).
	losMaxTiles = 16
	// losTileWait is how long a request waits for its tiles before it
	// answers 202; the downloads carry on without it.
	losTileWait = 1500 * time.Millisecond
	// losPollAfterS is the 202's retry_after_s.
	losPollAfterS = 2
	// losClientTilesPerMin is the per-client limit in tile downloads per
	// minute: a request is charged one token per download it starts. It
	// covers one full request (losMaxTiles).
	losClientTilesPerMin = 20
)

// LOSConfig is the "los" block in config.json. Terrain tiles come from the
// "terrain" block (TerrainConfig).
type LOSConfig struct {
	Enabled        bool    `json:"enabled"`
	FrequencyMHz   float64 `json:"frequencyMhz"`
	AntennaHeightM float64 `json:"antennaHeightM"`
	KFactor        float64 `json:"kFactor"`
}

func (c *LOSConfig) enabled() bool { return c != nil && c.Enabled }

// resolved returns the config with defaults applied and out-of-range values
// replaced by their default (logged once at startup by the caller).
func (c *LOSConfig) resolved() (LOSConfig, []string) {
	out := LOSConfig{
		FrequencyMHz:   defaultLOSFrequencyMHz,
		AntennaHeightM: defaultLOSAntennaM,
		KFactor:        defaultLOSKFactor,
	}
	var warns []string
	if c == nil {
		return out, nil
	}
	out.Enabled = c.Enabled
	if c.FrequencyMHz != 0 {
		if c.FrequencyMHz >= 100 && c.FrequencyMHz <= 6000 {
			out.FrequencyMHz = c.FrequencyMHz
		} else {
			warns = append(warns, "los.frequencyMhz must be within 100-6000; using the default")
		}
	}
	if c.AntennaHeightM != 0 {
		if c.AntennaHeightM > 0 && c.AntennaHeightM <= 300 {
			out.AntennaHeightM = c.AntennaHeightM
		} else {
			warns = append(warns, "los.antennaHeightM must be within 0-300; using the default")
		}
	}
	if c.KFactor != 0 {
		if c.KFactor >= 0.5 && c.KFactor <= 10 {
			out.KFactor = c.KFactor
		} else {
			warns = append(warns, "los.kFactor must be within 0.5-10; using the default")
		}
	}
	return out, warns
}

// LOSClientConfig is /api/config/client "los"; present only when enabled.
type LOSClientConfig struct {
	FrequencyMHz   float64 `json:"frequencyMhz"`
	AntennaHeightM float64 `json:"antennaHeightM"`
	KFactor        float64 `json:"kFactor"`
	MaxPoints      int     `json:"maxPoints"`
	MaxLegKm       float64 `json:"maxLegKm"`
}

func (c *LOSConfig) clientConfig() *LOSClientConfig {
	if !c.enabled() {
		return nil
	}
	r, _ := c.resolved()
	return &LOSClientConfig{
		FrequencyMHz:   r.FrequencyMHz,
		AntennaHeightM: r.AntennaHeightM,
		KFactor:        r.KFactor,
		MaxPoints:      losMaxPoints,
		MaxLegKm:       losMaxLegKm,
	}
}

// logLOSConfig reports the effective config once at startup.
func logLOSConfig(c *LOSConfig) {
	if !c.enabled() {
		return
	}
	r, warns := c.resolved()
	for _, w := range warns {
		log.Printf("[los] %s", w)
	}
	log.Printf("[los] enabled (%.3f MHz, antenna=%.1f m, k=%.3f, up to %d terrain tiles a request, %d tile downloads/min per client)",
		r.FrequencyMHz, r.AntennaHeightM, r.KFactor, losMaxTiles, losClientTilesPerMin)
}
