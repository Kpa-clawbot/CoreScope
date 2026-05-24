package main

import (
	"context"
	"encoding/json"
	"log"
	"math"
	"net/http"
)

// ─── Types ─────────────────────────────────────────────────────────────────────

type rfCoverageRequest struct {
	Lat           float64 `json:"lat"`
	Lon           float64 `json:"lon"`
	TxPowerDBm    float64 `json:"tx_power_dbm"`
	FreqMHz       float64 `json:"freq_mhz"`
	SF            int     `json:"sf"`
	AntennaHeight float64 `json:"antenna_height"`
	Model         string  `json:"model"` // "free" | "suburban" | "urban" | "indoor"
}

type rfCoveragePoint struct {
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
	RangeKm float64 `json:"range_km"`
}

type rfCoverageResponse struct {
	Coverage       []rfCoveragePoint `json:"coverage"`
	CenterLat      float64           `json:"center_lat"`
	CenterLon      float64           `json:"center_lon"`
	TxPowerDBm     float64           `json:"tx_power_dbm"`
	FreqMHz        float64           `json:"freq_mhz"`
	SF             int               `json:"sf"`
	Model          string            `json:"model"`
	SensitivityDBm float64           `json:"sensitivity_dbm"`
	DataGaps       bool              `json:"data_gaps,omitempty"`
}

// ─── Math ──────────────────────────────────────────────────────────────────────

// rfSensitivityDBm returns the LoRa RX sensitivity in dBm for a given
// spreading factor (BW=125 kHz assumed). Defaults to SF7 for unknown values.
func rfSensitivityDBm(sf int) float64 {
	switch sf {
	case 7:
		return -123.0
	case 8:
		return -126.0
	case 9:
		return -129.0
	case 10:
		return -132.0
	case 11:
		return -134.5
	case 12:
		return -137.0
	default:
		return -123.0
	}
}

// rfPathLossExponent returns the log-distance path loss exponent for the given
// environment model. Defaults to free-space (n=2.0) for unknown models.
func rfPathLossExponent(model string) float64 {
	switch model {
	case "suburban":
		return 2.2
	case "urban":
		return 2.3
	case "indoor":
		return 2.7
	default: // "free" and anything else
		return 2.0
	}
}

// rfPathLossDB computes path loss in dB using the log-distance model:
//
//	PL(d) = FSPL_1m + 10·n·log10(d)
//
// where FSPL_1m = 20·log10(4π·freqHz / c). Returns 0 for d ≤ 0.
func rfPathLossDB(distM, freqMHz, n float64) float64 {
	if distM <= 0 {
		return 0
	}
	freqHz := freqMHz * 1e6
	fspl1m := 20 * math.Log10(4*math.Pi*freqHz/299_792_458)
	return fspl1m + 10*n*math.Log10(distM)
}

// destCoordFromBearing returns the lat/lon reached by traveling distKm from
// (lat, lon) along bearing degrees (0 = north, clockwise).
// Uses the spherical Earth great-circle formula.
func destCoordFromBearing(lat, lon, distKm, bearing float64) (float64, float64) {
	const R = 6371.0
	d := distKm / R
	brng := bearing * math.Pi / 180
	lat1 := lat * math.Pi / 180
	lon1 := lon * math.Pi / 180
	lat2 := math.Asin(math.Sin(lat1)*math.Cos(d) + math.Cos(lat1)*math.Sin(d)*math.Cos(brng))
	lon2 := lon1 + math.Atan2(math.Sin(brng)*math.Sin(d)*math.Cos(lat1), math.Cos(d)-math.Sin(lat1)*math.Sin(lat2))
	return lat2 * 180 / math.Pi, lon2 * 180 / math.Pi
}

// ─── Coverage computation ──────────────────────────────────────────────────────

// computeRFCoverage samples numBearings radial directions from req.Lat/Lon,
// fetching all elevation points in one batched call, then walks each radial to
// find the farthest step where the link budget and terrain LOS are still met.
func (h *losHandler) computeRFCoverage(ctx context.Context, req rfCoverageRequest, maxRangeKm float64, numBearings int, stepKm float64) ([]rfCoveragePoint, bool, error) {
	n := rfPathLossExponent(req.Model)
	sensitivity := rfSensitivityDBm(req.SF)
	numSteps := int(maxRangeKm / stepKm)
	if numSteps < 1 {
		numSteps = 1
	}

	// ── Build all sample points ────────────────────────────────────────────────
	type samplePoint struct{ lat, lon float64 }
	allPoints := make([]samplePoint, 0, 1+numBearings*numSteps)
	allPoints = append(allPoints, samplePoint{req.Lat, req.Lon}) // index 0 = TX

	bearingOffsets := make([]int, numBearings)
	for b := 0; b < numBearings; b++ {
		bearing := float64(b) * 360.0 / float64(numBearings)
		bearingOffsets[b] = len(allPoints)
		for s := 1; s <= numSteps; s++ {
			distKm := float64(s) * stepKm
			lat, lon := destCoordFromBearing(req.Lat, req.Lon, distKm, bearing)
			allPoints = append(allPoints, samplePoint{lat, lon})
		}
	}

	// ── Fetch all elevations in one shot ───────────────────────────────────────
	lats := make([]float64, len(allPoints))
	lons := make([]float64, len(allPoints))
	for i, p := range allPoints {
		lats[i] = p.lat
		lons[i] = p.lon
	}
	elevs, dataGaps, err := h.fetchElevations(ctx, lats, lons)
	if err != nil {
		return nil, false, err
	}

	txElev := elevs[0] + req.AntennaHeight

	// ── Walk each radial ───────────────────────────────────────────────────────
	coverage := make([]rfCoveragePoint, numBearings)
	for b := 0; b < numBearings; b++ {
		bearing := float64(b) * 360.0 / float64(numBearings)
		offset := bearingOffsets[b]
		edgeKm := 0.0

		for s := 1; s <= numSteps; s++ {
			distKm := float64(s) * stepKm
			distM := distKm * 1000

			// Path loss check (cheap — skip LOS build if budget exceeded)
			pl := rfPathLossDB(distM, req.FreqMHz, n)
			if req.TxPowerDBm-pl < sensitivity {
				break
			}

			// Build LOS subprofile TX (index 0) → step s (index offset+s-1).
			// profile[j] covers intermediate sample j=1..s-1 plus the target at j=s.
			targetElev := elevs[offset+s-1] + req.AntennaHeight
			profile := make([]losProfilePoint, s+1)
			profile[0] = losProfilePoint{
				Lat: lats[0], Lon: lons[0],
				TerrainElev: elevs[0], LOSElev: txElev, Bulge: 0, Blocked: false,
			}
			for j := 1; j <= s; j++ {
				idx := offset + j - 1
				t := float64(j) / float64(s)
				losElevJ := txElev + t*(targetElev-txElev)
				bulge := earthBulgeM(t, distM)
				terrain := elevs[idx]
				profile[j] = losProfilePoint{
					Lat: lats[idx], Lon: lons[idx],
					TerrainElev: terrain, LOSElev: losElevJ,
					Bulge: bulge, Blocked: terrain > (losElevJ + bulge),
				}
			}

			result := losAnalyze(profile)
			if !result.LOSClear {
				break // terrain blocks at this step
			}
			edgeKm = distKm
		}

		// edgeKm=0 means every step was blocked or budget was exceeded immediately.
		// destCoordFromBearing with distKm=0 returns the TX position — the polygon
		// collapses on that bearing, which is the correct representation of zero range.
		endLat, endLon := destCoordFromBearing(req.Lat, req.Lon, edgeKm, bearing)
		coverage[b] = rfCoveragePoint{Lat: endLat, Lon: endLon, RangeKm: edgeKm}
	}

	return coverage, dataGaps, nil
}

// ─── HTTP handler ──────────────────────────────────────────────────────────────

func (s *Server) handleRFCoverage(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	var req rfCoverageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Lat < -90 || req.Lat > 90 {
		writeError(w, http.StatusBadRequest, "lat out of range")
		return
	}
	if req.Lon < -180 || req.Lon > 180 {
		writeError(w, http.StatusBadRequest, "lon out of range")
		return
	}

	// Apply defaults
	if req.TxPowerDBm == 0 {
		req.TxPowerDBm = 20
	}
	if req.FreqMHz == 0 {
		req.FreqMHz = 869.618
	}
	if req.SF == 0 {
		req.SF = 7
	}
	if req.SF < 7 || req.SF > 12 {
		req.SF = 7
	}
	if req.AntennaHeight <= 0 {
		req.AntennaHeight = 2
	}
	switch req.Model {
	case "free", "suburban", "urban", "indoor":
		// valid
	default:
		req.Model = "free"
	}
	if req.TxPowerDBm < -30 || req.TxPowerDBm > 36 {
		writeError(w, http.StatusBadRequest, "tx_power_dbm out of range (must be -30 to 36 dBm)")
		return
	}

	h := s.getLOSHandler()
	coverage, dataGaps, err := h.computeRFCoverage(
		r.Context(), req,
		s.cfg.RFMaxRangeKm(), s.cfg.RFBearings(), s.cfg.RFStepKm(),
	)
	if err != nil {
		log.Printf("[rf-coverage] error: %v", err)
		writeError(w, http.StatusBadGateway, "elevation API unavailable")
		return
	}

	resp := rfCoverageResponse{
		Coverage:       coverage,
		CenterLat:      req.Lat,
		CenterLon:      req.Lon,
		TxPowerDBm:     req.TxPowerDBm,
		FreqMHz:        req.FreqMHz,
		SF:             req.SF,
		Model:          req.Model,
		SensitivityDBm: rfSensitivityDBm(req.SF),
		DataGaps:       dataGaps,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
