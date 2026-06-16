package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
)

// coverageRow is one raw reception read from client_receptions.
type coverageRow struct {
	Lat, Lon float64
	SNR      *float64
	RSSI     *int
	HeardKey string // directly-heard node key (2-3 byte prefix or full pubkey), lowercase
	RxAt     string // reception time (RFC3339); used to pick the latest SNR per node
}

// GeoJSON output (named structs, no map[string]interface{} — AGENTS.md).
type CoverageFeatureCollection struct {
	Type     string            `json:"type"` // "FeatureCollection"
	Features []CoverageFeature `json:"features"`
}
type CoverageFeature struct {
	Type       string             `json:"type"` // "Feature"
	Geometry   CoveragePolygon    `json:"geometry"`
	Properties CoverageProperties `json:"properties"`
}
type CoveragePolygon struct {
	Type        string         `json:"type"`        // "Polygon"
	Coordinates [][][2]float64 `json:"coordinates"` // one ring: [ [ [lon,lat], ... ] ]
}
type CoverageProperties struct {
	Cell    string         `json:"cell"`
	Count   int            `json:"count"`
	BestSNR *float64       `json:"best_snr"`
	HasSig  bool           `json:"has_sig"` // false → render grey (no signal metric)
	Nodes   []CoverageNode `json:"nodes"`   // per-node breakdown, strongest latest-SNR first
}

// CoverageNode is one directly-heard node within a cell, with its latest SNR.
type CoverageNode struct {
	Prefix string   `json:"prefix"`         // heard_key (resolved to Name when unique)
	Name   string   `json:"name,omitempty"` // node name, empty if unknown/ambiguous prefix
	SNR    *float64 `json:"snr"`            // latest SNR (by rx_at); nil → heard without signal
	Count  int      `json:"count"`
}

type covAgg struct {
	count   int
	bestSNR *float64
	hasSig  bool
	nodes   map[string]*covNodeAgg
}

// covNodeAgg tracks, per directly-heard node within a cell, its reception count and
// the SNR of its most recent reception (by rx_at). name/prefix are the resolved node
// name (when known) and a display prefix fallback. nameKeyLen records the heard_key
// length that set the current name, so the chosen identity is the most specific one
// regardless of row order (#20).
type covNodeAgg struct {
	count      int
	latestAt   string
	latestSNR  *float64
	name       string
	nameKeyLen int
	prefix     string
}

// nodeResolver maps a heard_key (2-3 byte prefix or full pubkey) to a canonical
// identity key and a display name. A unique match returns (pubkey, name) so the same
// node heard under different prefix lengths collapses into one bucket; unknown or
// ambiguous keys return (heardKey, "") and stay distinct. nil disables resolution.
type nodeResolver func(heardKey string) (key, name string)

// aggregateCoverage bins raw rows into display-resolution hex cells, keeping the
// best (max) SNR per cell, and emits GeoJSON polygons. resolve (may be nil) collapses
// per-node receptions by resolved node identity.
func aggregateCoverage(rows []coverageRow, res int, resolve nodeResolver) CoverageFeatureCollection {
	byCell := map[string]*covAgg{}
	for _, row := range rows {
		cell := hexCellAt(row.Lat, row.Lon, res)
		a := byCell[cell]
		if a == nil {
			a = &covAgg{}
			byCell[cell] = a
		}
		a.count++
		if row.SNR != nil {
			a.hasSig = true
			if a.bestSNR == nil || *row.SNR > *a.bestSNR {
				v := *row.SNR
				a.bestSNR = &v
			}
		}
		if row.HeardKey != "" {
			if a.nodes == nil {
				a.nodes = map[string]*covNodeAgg{}
			}
			key, name := row.HeardKey, ""
			if resolve != nil {
				if k, n := resolve(row.HeardKey); k != "" {
					key, name = k, n
				}
			}
			na := a.nodes[key]
			if na == nil {
				na = &covNodeAgg{prefix: row.HeardKey}
				a.nodes[key] = na
			}
			// Lock the display identity to the MOST SPECIFIC (longest) heard_key
			// that resolved to a non-empty name, tie-broken lexicographically, so
			// the name no longer flaps with row/map order (#20). A full-pubkey
			// reception thus outranks a short-prefix one for the same node.
			if name != "" && (na.name == "" || len(row.HeardKey) > na.nameKeyLen ||
				(len(row.HeardKey) == na.nameKeyLen && name < na.name)) {
				na.name = name
				na.nameKeyLen = len(row.HeardKey)
			}
			// Display-prefix fallback (shown when name is empty): same precedence so
			// it is also order-independent.
			if len(row.HeardKey) > len(na.prefix) ||
				(len(row.HeardKey) == len(na.prefix) && row.HeardKey < na.prefix) {
				na.prefix = row.HeardKey
			}
			na.count++
			// rx_at is RFC3339, so lexical >= is chronological; keep the latest SNR.
			if na.count == 1 || row.RxAt >= na.latestAt {
				na.latestAt = row.RxAt
				na.latestSNR = row.SNR
			}
		}
	}
	fc := CoverageFeatureCollection{Type: "FeatureCollection", Features: []CoverageFeature{}}
	for cell, a := range byCell {
		ring := hexBoundary(cell)
		if ring == nil {
			continue
		}
		fc.Features = append(fc.Features, CoverageFeature{
			Type:     "Feature",
			Geometry: CoveragePolygon{Type: "Polygon", Coordinates: [][][2]float64{ring}},
			Properties: CoverageProperties{
				Cell: cell, Count: a.count, BestSNR: a.bestSNR, HasSig: a.hasSig,
				Nodes: sortedCoverageNodes(a.nodes),
			},
		})
	}
	// Map iteration is randomized, so sort features by cell for a deterministic
	// payload — stable ETag/caching and a non-flaky "first feature" in e2e (#8).
	sort.Slice(fc.Features, func(i, j int) bool {
		return fc.Features[i].Properties.Cell < fc.Features[j].Properties.Cell
	})
	return fc
}

// sortedCoverageNodes flattens the per-node aggregates into a slice sorted by latest
// SNR descending (nodes heard without a signal sort last), tie-broken by count then
// prefix for a stable order.
func sortedCoverageNodes(m map[string]*covNodeAgg) []CoverageNode {
	out := make([]CoverageNode, 0, len(m))
	for _, na := range m {
		out = append(out, CoverageNode{Prefix: na.prefix, Name: na.name, SNR: na.latestSNR, Count: na.count})
	}
	sort.Slice(out, func(i, j int) bool {
		si, sj := out[i].SNR, out[j].SNR
		if (si == nil) != (sj == nil) {
			return si != nil // signal before no-signal
		}
		if si != nil && *si != *sj {
			return *si > *sj
		}
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Prefix < out[j].Prefix
	})
	return out
}

type bbox struct{ MinLat, MinLon, MaxLat, MaxLon float64 }

// queryCoverageRows returns raw coverage rows where the directly-heard node
// matches the target pubkey by its 2-3 byte prefix (or full pubkey), within the
// bbox. Read-only (server RO connection).
func (s *Server) queryCoverageRows(pubkey string, b bbox) ([]coverageRow, error) {
	pk := strings.ToLower(pubkey)
	rows, err := s.db.conn.Query(`
		SELECT lat, lon, snr, rssi, heard_key, rx_at
		FROM client_receptions
		WHERE lat BETWEEN ? AND ? AND lon BETWEEN ? AND ?
		  AND ( (heard_keylen = 32 AND heard_key = ?)
		     OR (heard_keylen IN (2,3) AND substr(?, 1, heard_keylen*2) = heard_key) )`,
		b.MinLat, b.MaxLat, b.MinLon, b.MaxLon, pk, pk)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCoverageRows(rows)
}

// mobileRxStats returns the total mobile-client receptions of a node (by its
// 2-3 byte prefix or full pubkey) and the number of distinct contributing clients.
func (s *Server) mobileRxStats(pubkey string) (count, clients int) {
	if s.db == nil || s.db.conn == nil {
		return 0, 0
	}
	pk := strings.ToLower(pubkey)
	s.db.conn.QueryRow(`
		SELECT COUNT(*), COUNT(DISTINCT rx_pubkey) FROM client_receptions
		WHERE (heard_keylen = 32 AND heard_key = ?)
		   OR (heard_keylen IN (2,3) AND substr(?, 1, heard_keylen*2) = heard_key)`,
		pk, pk).Scan(&count, &clients)
	return count, clients
}

// zoomToHexRes maps a Leaflet zoom level to the display resolution used for hex
// binning. Resolution == zoom (clamped to a sane range) so hex size tracks the map
// scale 1:1 and renders at a constant ~hexTargetPx (see hexSizeForRes). The clamp also
// guards the missing-param case (z parses to 0).
func zoomToHexRes(z int) int {
	switch {
	case z < 3:
		return 3
	case z > 18:
		return 18
	default:
		return z
	}
}

func parseBBox(s string) (bbox, bool) {
	p := strings.Split(s, ",")
	if len(p) != 4 {
		return bbox{}, false
	}
	v := make([]float64, 4)
	for i := range p {
		f, err := strconv.ParseFloat(strings.TrimSpace(p[i]), 64)
		if err != nil {
			return bbox{}, false
		}
		v[i] = f
	}
	return bbox{MinLat: v[0], MinLon: v[1], MaxLat: v[2], MaxLon: v[3]}, true
}

// handleNodeRxCoverage serves per-node mobile RX coverage as a GeoJSON hex grid.
func (s *Server) handleNodeRxCoverage(w http.ResponseWriter, r *http.Request) {
	if !s.requireClientRxCoverage(w, r) {
		return
	}
	pubkey := strings.ToLower(mux.Vars(r)["pubkey"])
	b, ok := parseBBox(r.URL.Query().Get("bbox"))
	if !ok {
		http.Error(w, "bbox required as minLat,minLon,maxLat,maxLon", http.StatusBadRequest)
		return
	}
	if s.db == nil || s.db.conn == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	z, _ := strconv.Atoi(r.URL.Query().Get("z"))
	rows, err := s.queryCoverageRows(pubkey, b)
	if err != nil {
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	fc := aggregateCoverage(rows, zoomToHexRes(z), s.heardKeyResolver())
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(fc)
}
