package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// rfNoiseRow is one raw client_rf_samples reading: a companion's own noise
// floor reading paired with its GPS fix.
type rfNoiseRow struct {
	Lat, Lon   float64
	NoiseFloor int
	Stationary bool
}

// rfNoiseFeatureCap bounds the number of hex cells returned in one response,
// mirroring coverageFeatureCap (#12) for the same reason: a wide bbox at high
// zoom over the 30-day window could otherwise emit unbounded GeoJSON.
const rfNoiseFeatureCap = 5000

// RfNoiseFeatureCollection is the GeoJSON payload for GET /api/rf-noise.
// Truncated is a non-standard foreign member (ignored by GeoJSON consumers
// like Leaflet) that signals the cell list was capped at rfNoiseFeatureCap.
type RfNoiseFeatureCollection struct {
	Type      string           `json:"type"` // "FeatureCollection"
	Features  []RfNoiseFeature `json:"features"`
	Truncated bool             `json:"truncated,omitempty"`
}

type RfNoiseFeature struct {
	Type       string            `json:"type"` // "Feature"
	Geometry   CoveragePolygon   `json:"geometry"`
	Properties RfNoiseProperties `json:"properties"`
}

// RfNoiseProperties summarizes one cell's noise-floor samples. NoiseFloor
// values are dBm and negative; lower (more negative) is quieter — the
// opposite direction from the SNR the coverage layer colours.
type RfNoiseProperties struct {
	Cell  string `json:"cell"`
	Count int    `json:"count"`
	// MedianNoiseFloor is the MEDIAN (not mean) of the cell's samples: a
	// single anomalous reading (a burst of local interference at one GPS
	// point) shouldn't drag the whole cell's reported value the way it would
	// pull a mean.
	MedianNoiseFloor   float64 `json:"median_noise_floor"`
	QuietestNoiseFloor int     `json:"quietest_noise_floor"` // min sample in the cell (best/quietest)
	NoisiestNoiseFloor int     `json:"noisiest_noise_floor"` // max sample in the cell (worst/busiest)
}

type rfNoiseAgg struct {
	values []int // noise_floor samples retained for this cell (mobile-only, see aggregateRfNoise)
}

// aggregateRfNoise bins raw RF-environment samples into display-resolution
// hex cells and emits GeoJSON polygons carrying the noise-floor distribution
// per cell.
//
// Stationary samples (stationary=1, a parked companion) are dropped entirely
// rather than down-weighted: a single parked driver can log hundreds of
// samples at one GPS point in the time a drive-by logs a handful, so any
// inclusion — even down-weighted — still lets the parked run dominate a
// cell's reported noise floor. This layer exists to show what the band looks
// like from the road; a stationary run isn't that measurement.
func aggregateRfNoise(rows []rfNoiseRow, res int) RfNoiseFeatureCollection {
	byCell := map[string]*rfNoiseAgg{}
	for _, row := range rows {
		if row.Stationary {
			continue
		}
		cell := hexCellAt(row.Lat, row.Lon, res)
		a := byCell[cell]
		if a == nil {
			a = &rfNoiseAgg{}
			byCell[cell] = a
		}
		a.values = append(a.values, row.NoiseFloor)
	}
	fc := RfNoiseFeatureCollection{Type: "FeatureCollection", Features: []RfNoiseFeature{}}
	for cell, a := range byCell {
		ring := hexBoundary(cell)
		if ring == nil {
			continue
		}
		sort.Ints(a.values)
		fc.Features = append(fc.Features, RfNoiseFeature{
			Type:     "Feature",
			Geometry: CoveragePolygon{Type: "Polygon", Coordinates: [][][2]float64{ring}},
			Properties: RfNoiseProperties{
				Cell:               cell,
				Count:              len(a.values),
				MedianNoiseFloor:   medianInt(a.values),
				QuietestNoiseFloor: a.values[0],
				NoisiestNoiseFloor: a.values[len(a.values)-1],
			},
		})
	}
	// Bound the response the same way aggregateCoverage does (#12): when more
	// cells exist than rfNoiseFeatureCap, keep the densest (highest count)
	// and flag the truncation.
	if len(fc.Features) > rfNoiseFeatureCap {
		sort.Slice(fc.Features, func(i, j int) bool {
			ci, cj := fc.Features[i].Properties.Count, fc.Features[j].Properties.Count
			if ci != cj {
				return ci > cj // densest first
			}
			return fc.Features[i].Properties.Cell < fc.Features[j].Properties.Cell // deterministic tie-break
		})
		fc.Features = fc.Features[:rfNoiseFeatureCap]
		fc.Truncated = true
	}
	// Map iteration is randomized, so sort by cell for a deterministic
	// payload, same as aggregateCoverage (#8).
	sort.Slice(fc.Features, func(i, j int) bool {
		return fc.Features[i].Properties.Cell < fc.Features[j].Properties.Cell
	})
	return fc
}

// medianInt returns the median of vals (average of the two middle values
// when the count is even). vals must be sorted ascending and non-empty.
func medianInt(vals []int) float64 {
	n := len(vals)
	if n%2 == 1 {
		return float64(vals[n/2])
	}
	return float64(vals[n/2-1]+vals[n/2]) / 2
}

// requireClientRfSamples writes a 404 and returns false when the opt-in RF
// sample stream is disabled, mirroring requireClientRxCoverage so the
// endpoint reads as "not found" instead of serving data on deployments that
// haven't enabled it.
func (s *Server) requireClientRfSamples(w http.ResponseWriter, r *http.Request) bool {
	if s == nil || s.cfg == nil || !s.cfg.ClientRfSamplesEnabled() {
		http.NotFound(w, r)
		return false
	}
	return true
}

// queryRfNoiseRows returns raw RF-environment samples within a bbox, over a
// time window (days; 0 = all time). Read-only (server RO connection).
func (s *Server) queryRfNoiseRows(days int, b bbox) ([]rfNoiseRow, error) {
	where := []string{"lat BETWEEN ? AND ?", "lon BETWEEN ? AND ?", "noise_floor IS NOT NULL"}
	args := []interface{}{b.MinLat, b.MaxLat, b.MinLon, b.MaxLon}
	if days > 0 {
		since := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)
		where = append(where, "sampled_at >= ?")
		args = append(args, since)
	}
	rows, err := s.db.conn.Query(`
		SELECT lat, lon, noise_floor, stationary
		FROM client_rf_samples
		WHERE `+strings.Join(where, " AND "), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []rfNoiseRow{}
	for rows.Next() {
		var lat, lon float64
		var noiseFloor, stationary int
		if err := rows.Scan(&lat, &lon, &noiseFloor, &stationary); err != nil {
			return nil, err
		}
		out = append(out, rfNoiseRow{Lat: lat, Lon: lon, NoiseFloor: noiseFloor, Stationary: stationary != 0})
	}
	return out, rows.Err()
}

// handleRfNoise serves the RF noise-floor map layer as GeoJSON hex cells,
// over a time window. Mirrors handleRxCoverage's parameter handling.
func (s *Server) handleRfNoise(w http.ResponseWriter, r *http.Request) {
	if !s.requireClientRfSamples(w, r) {
		return
	}
	b, ok := parseBBox(r.URL.Query().Get("bbox"))
	if !ok {
		http.Error(w, "bbox required as minLat,minLon,maxLat,maxLon", http.StatusBadRequest)
		return
	}
	if s.db == nil || s.db.conn == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	days := clampDays(atoiDefault(r.URL.Query().Get("days"), 7))
	z, _ := strconv.Atoi(r.URL.Query().Get("z"))
	rows, err := s.queryRfNoiseRows(days, b)
	if err != nil {
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	fc := aggregateRfNoise(rows, zoomToHexRes(z))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(fc)
}
