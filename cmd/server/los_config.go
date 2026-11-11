package main

// Radio line-of-sight (LOS) for packet routes on the map. OPT-IN, like
// rfParams (#1268): without los.enabled the server fetches nothing,
// /api/elevation/profile answers 404 and /api/config/client omits "los", so
// the frontend shows no LOS UI at all.
//
// The server only supplies terrain elevation along each leg (elevation.go,
// cached in memory, never written to the DB). Earth curvature, the Fresnel
// zone and the verdict are computed in the browser (public/los.js) so the
// antenna-height and frequency inputs re-render instantly.

import (
	"log"
	"net/url"
)

const (
	defaultLOSElevationURL = "https://api.open-meteo.com/v1/elevation"
	defaultLOSFrequencyMHz = 910.525
	defaultLOSAntennaM     = 5.0
	defaultLOSKFactor      = 4.0 / 3.0

	// Upstream leg-fetch budgets (one leg = one 100-coordinate request, about
	// 10 Open-Meteo "calls" against the free tier's 600/min, 5,000/h and
	// 10,000/day per IP). Cached legs cost nothing.
	defaultLOSLegsPerMinute = 40
	defaultLOSLegsPerHour   = 300
	defaultLOSLegsPerDay    = 700
	maxLOSLegsPerWindow     = 1000000

	// losSamples is the number of elevation samples per leg, both endpoints
	// included, evenly spaced along the great circle.
	losSamples = 100
	// losMaxPoints caps the route points in one profile request (so at most
	// losMaxPoints-1 legs, each one upstream batch of losSamples).
	losMaxPoints = 16
	// losMaxLegKm: longer legs are not real LoRa links worth a profile; they
	// come back with error "too_long" and cost no upstream call.
	losMaxLegKm = 300.0
)

// LOSConfig is the "los" block in config.json.
type LOSConfig struct {
	Enabled bool `json:"enabled"`
	// ElevationURL is an Open-Meteo-compatible elevation endpoint
	// (?latitude=a,b&longitude=c,d → {"elevation":[...]}). Point it at a
	// self-hosted Open-Meteo to keep traffic off the public API.
	ElevationURL   string  `json:"elevationUrl"`
	FrequencyMHz   float64 `json:"frequencyMhz"`
	AntennaHeightM float64 `json:"antennaHeightM"`
	KFactor        float64 `json:"kFactor"`
	// Upstream leg fetches allowed per fixed minute/hour/day window. 0 =
	// default. Raise them for a self-hosted elevation server.
	LegsPerMinute int `json:"legsPerMinute"`
	LegsPerHour   int `json:"legsPerHour"`
	LegsPerDay    int `json:"legsPerDay"`
}

func (c *LOSConfig) enabled() bool { return c != nil && c.Enabled }

// resolved returns the config with defaults applied and out-of-range values
// replaced by their default (logged once at startup by the caller).
func (c *LOSConfig) resolved() (LOSConfig, []string) {
	out := LOSConfig{
		ElevationURL:   defaultLOSElevationURL,
		FrequencyMHz:   defaultLOSFrequencyMHz,
		AntennaHeightM: defaultLOSAntennaM,
		KFactor:        defaultLOSKFactor,

		LegsPerMinute: defaultLOSLegsPerMinute,
		LegsPerHour:   defaultLOSLegsPerHour,
		LegsPerDay:    defaultLOSLegsPerDay,
	}
	var warns []string
	if c == nil {
		return out, nil
	}
	out.Enabled = c.Enabled
	if c.ElevationURL != "" {
		if u, err := url.Parse(c.ElevationURL); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
			out.ElevationURL = c.ElevationURL
		} else {
			warns = append(warns, "los.elevationUrl must be an http(s) URL; using the default")
		}
	}
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
	for _, l := range []struct {
		name string
		in   int
		out  *int
	}{
		{"legsPerMinute", c.LegsPerMinute, &out.LegsPerMinute},
		{"legsPerHour", c.LegsPerHour, &out.LegsPerHour},
		{"legsPerDay", c.LegsPerDay, &out.LegsPerDay},
	} {
		if l.in == 0 {
			continue
		}
		if l.in >= 1 && l.in <= maxLOSLegsPerWindow {
			*l.out = l.in
		} else {
			warns = append(warns, "los."+l.name+" must be within 1-1000000; using the default")
		}
	}
	return out, warns
}

// LOSClientConfig is /api/config/client "los"; present only when enabled.
type LOSClientConfig struct {
	FrequencyMHz   float64 `json:"frequencyMhz"`
	AntennaHeightM float64 `json:"antennaHeightM"`
	KFactor        float64 `json:"kFactor"`
	Samples        int     `json:"samples"`
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
		Samples:        losSamples,
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
	log.Printf("[los] enabled (elevation=%s, %.3f MHz, antenna=%.1f m, k=%.3f, legs/min=%d, legs/h=%d, legs/day=%d)",
		redactURL(r.ElevationURL), r.FrequencyMHz, r.AntennaHeightM, r.KFactor,
		r.LegsPerMinute, r.LegsPerHour, r.LegsPerDay)
}

// redactURL keeps scheme://host/path only: the query (an operator's apikey)
// and any userinfo never reach the logs.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "(invalid url)"
	}
	return u.Scheme + "://" + u.Host + u.Path
}
