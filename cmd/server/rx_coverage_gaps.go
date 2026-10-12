package main

import (
	"sort"
	"strings"
	"time"
)

// "Nothing received" cells for the coverage map: hex cells a companion drove
// through (moving client_rf_samples) in which no client_receptions row lies in
// the same window. Served as the `gaps` foreign member of /api/rx-coverage when
// asked with ?gaps=1, so the reception features stay exactly what they were.
//
// One moving sample is enough. Measured on the live network (2026-09/10): of
// cells flagged with nothing received in one half of the period and driven again
// in the other, 29% (1 sample), 32% (2), 35% (3) and 40% (5) did have a
// reception the second time. A higher threshold only hides cells; it does not
// make the rest more reliable. Hence the wording "nothing received in this
// period", never "no coverage".

// coverageGapCap bounds the gap cells per response, like coverageFeatureCap
// and rfNoiseFeatureCap.
const coverageGapCap = 5000

type CoverageGap struct {
	Type       string                `json:"type"` // "Feature"
	Geometry   CoveragePolygon       `json:"geometry"`
	Properties CoverageGapProperties `json:"properties"`
}

type CoverageGapProperties struct {
	Cell    string `json:"cell"`
	Samples int    `json:"samples"` // moving track samples in the cell
}

// trackPoint is one client_rf_samples GPS fix: the companion was connected
// to the app at that place and time.
type trackPoint struct {
	Lat, Lon   float64
	Stationary bool
}

// aggregateGaps returns the cells with moving track samples and no reception,
// sorted by cell. When more than coverageGapCap qualify, the most-sampled are
// kept and truncated is true.
func aggregateGaps(track []trackPoint, receptions []coverageRow, res int) (gaps []CoverageGap, truncated bool) {
	heard := make(map[string]bool, len(receptions))
	for _, r := range receptions {
		heard[hexCellAt(r.Lat, r.Lon, res)] = true
	}
	samples := map[string]int{}
	for _, p := range track {
		if p.Stationary {
			continue
		}
		if c := hexCellAt(p.Lat, p.Lon, res); !heard[c] {
			samples[c]++
		}
	}
	gaps = []CoverageGap{}
	for cell, n := range samples {
		ring := hexBoundary(cell)
		if ring == nil {
			continue
		}
		gaps = append(gaps, CoverageGap{
			Type:       "Feature",
			Geometry:   CoveragePolygon{Type: "Polygon", Coordinates: [][][2]float64{ring}},
			Properties: CoverageGapProperties{Cell: cell, Samples: n},
		})
	}
	if len(gaps) > coverageGapCap {
		sort.Slice(gaps, func(i, j int) bool {
			if gaps[i].Properties.Samples != gaps[j].Properties.Samples {
				return gaps[i].Properties.Samples > gaps[j].Properties.Samples
			}
			return gaps[i].Properties.Cell < gaps[j].Properties.Cell
		})
		gaps, truncated = gaps[:coverageGapCap], true
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i].Properties.Cell < gaps[j].Properties.Cell })
	return gaps, truncated
}

// queryTrackPoints returns client_rf_samples GPS fixes within a bbox and time
// window (days; 0 = all time), optionally for one companion. Unlike the noise
// layer it keeps rows without a noise_floor reading: any sample proves the
// companion was connected there. Read-only.
func (s *Server) queryTrackPoints(rx string, days int, b bbox) ([]trackPoint, error) {
	where := []string{"lat BETWEEN ? AND ?", "lon BETWEEN ? AND ?", "stationary = 0"}
	args := []interface{}{b.MinLat, b.MaxLat, b.MinLon, b.MaxLon}
	if rx != "" {
		where = append(where, "rx_pubkey = ?")
		args = append(args, strings.ToLower(rx))
	}
	if days > 0 {
		where = append(where, "sampled_at >= ?")
		args = append(args, time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339))
	}
	rows, err := s.db.conn.Query("SELECT lat, lon FROM client_rf_samples WHERE "+strings.Join(where, " AND "), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []trackPoint{}
	for rows.Next() {
		var p trackPoint
		if err := rows.Scan(&p.Lat, &p.Lon); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
