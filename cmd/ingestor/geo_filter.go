package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sync"

	"github.com/meshcore-analyzer/geofilter"
)

// NodePassesGeoFilter returns true if the node should be kept.
// Nodes with no GPS coordinates are always allowed.
func NodePassesGeoFilter(lat, lon *float64, gf *GeoFilterConfig) bool {
	if gf == nil {
		return true
	}
	if lat == nil || lon == nil {
		return true
	}
	return geofilter.PassesFilter(*lat, *lon, gf)
}

// geoFilterSource yields the ingestor's current geo_filter for the geo-prune
// pass. The filter can be edited through the server (which saves it to the
// config file) while the ingestor runs, so each pass re-reads the geo_filter
// key from the ingestor's own config file, the one it loaded at startup: a
// small local read of a trusted file, once per pass. If the read fails the
// startup value is used, with one warning until a read succeeds again.
type geoFilterSource struct {
	path    string
	startup *GeoFilterConfig

	mu     sync.Mutex
	warned bool
}

func newGeoFilterSource(path string, startup *GeoFilterConfig) *geoFilterSource {
	return &geoFilterSource{path: path, startup: startup}
}

// Current returns the geo_filter in the config file now (nil when the file
// has none), or the startup value when the file cannot be read or parsed.
func (g *geoFilterSource) Current() *GeoFilterConfig {
	gf, err := readConfigGeoFilter(g.path)
	g.mu.Lock()
	defer g.mu.Unlock()
	if err != nil {
		if !g.warned {
			g.warned = true
			log.Printf("[prune-queue] re-reading geo_filter failed, using the startup value: %v", err)
		}
		return g.startup
	}
	g.warned = false
	return gf
}

// readConfigGeoFilter reads only the geo_filter key of the config file.
func readConfigGeoFilter(path string) (*GeoFilterConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}
	var cfg struct {
		GeoFilter *GeoFilterConfig `json:"geo_filter"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}
	return cfg.GeoFilter, nil
}
