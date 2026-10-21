package main

import (
	"math"
	"strings"
	"sync"
	"time"
)

// LoRa settings that differ from the surrounding nodes (issue #1268, M3).
//
// Two nodes only hear each other when spreading factor and bandwidth match
// and their frequencies are close enough. The coding rate does not have to: RadioLib starts SX126x
// radios with an explicit LoRa header (SX126x::begin sets
// RADIOLIB_SX126X_LORA_HEADER_EXPLICIT at the RadioLib commit the firmware
// pins), the firmware never switches to implicit, and the explicit header
// carries the coding rate, so a CR mismatch still decodes.
//
// A node is compared with the nodes within the radius on the same band only
// (frequency within rfSameBandMHz), so a deliberate 433 MHz network next to
// an 868 MHz one flags neither. It is flagged when a setting it cannot hear
// is shared by more than half of the group, the node itself counted, and no
// other incompatible setting has the same support.
//
// Frequencies within a quarter of the bandwidth count as the same channel:
// the SX1262 tolerates a transmitter/receiver offset of up to 25% of BW
// without sensitivity loss, for SF5 to SF12 and all bandwidths (FERR_L in the
// S62F SX1262 module datasheet, which repeats the chip's receiver table; at
// SF10-SF12 a -50/-100/-200 ppm limit is listed too, which at 869 MHz is
// wider than BW/4 for the 62.5 and 125 kHz presets).

const (
	defaultRfCompareRadiusKm = 50.0
	rfMinPeers               = 3
	// rfSameBandMHz treats frequencies this close as one band: 869.618 and
	// 868.731 compare, 433.65 and 869.618 do not.
	rfSameBandMHz = 30.0
	// rfDeviationTTL bounds how long one computation is served. The map
	// refreshes every few hours and nodes move rarely; 10,000 nodes in a
	// Belgium-sized box take about 0.4 s (BenchmarkRfDeviations10k).
	rfDeviationTTL = 5 * time.Minute
)

// rfDeviationCache holds the last computation for one snapshot.
type rfDeviationCache struct {
	mu   sync.Mutex
	snap *rfParamsSnapshot
	at   time.Time
	res  map[string]RfDeviation
}

// get returns the cached deviations for snap, computing them when the
// snapshot changed or the entry is older than rfDeviationTTL. Concurrent
// callers wait for one computation instead of running their own.
func (c *rfDeviationCache) get(snap *rfParamsSnapshot, now time.Time, compute func() map[string]RfDeviation) map[string]RfDeviation {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.res != nil && c.snap == snap && now.Sub(c.at) < rfDeviationTTL {
		return c.res
	}
	c.res, c.snap, c.at = compute(), snap, now
	return c.res
}

func (c *RfParamsConfig) compareRadiusKm() float64 {
	if c != nil && c.CompareRadiusKm > 0 {
		return c.CompareRadiusKm
	}
	return defaultRfCompareRadiusKm
}

// RfSetting is the part of a node's radio settings that must match for two
// nodes to hear each other.
type RfSetting struct {
	Freq float64 `json:"freq"`
	SF   int     `json:"sf"`
	BW   float64 `json:"bw"`
}

// RfDeviation describes a node whose setting differs from the majority of
// its same-band peers within the radius.
type RfDeviation struct {
	Majority RfSetting `json:"majority"`
	Agree    int       `json:"agree"`  // peers whose setting is compatible with Majority
	Peers    int       `json:"peers"`  // same-band peers within the radius
	Fields   []string  `json:"fields"` // which of freq, sf, bw differ
}

type rfDevPoint struct {
	pk       string
	lat, lon float64
	set      RfSetting
}

func rfSettingOf(p RfParams) RfSetting {
	return RfSetting{Freq: math.Round(p.Freq*1000) / 1000, SF: p.SF, BW: p.BW}
}

// rfFreqCompatible reports whether two frequencies (MHz) are within a quarter
// of the bandwidth (kHz) of each other. The 1e-9 absorbs float rounding of
// the MHz values at exactly BW/4.
func rfFreqCompatible(a, b, bwKHz float64) bool {
	return math.Abs(a-b) <= bwKHz/4000+1e-9
}

// compatible reports whether nodes on settings a and b can hear each other.
func (a RfSetting) compatible(b RfSetting) bool {
	return a.SF == b.SF && a.BW == b.BW && rfFreqCompatible(a.Freq, b.Freq, a.BW)
}

// less orders settings so the majority pick does not depend on map order.
func (a RfSetting) less(b RfSetting) bool {
	if a.Freq != b.Freq {
		return a.Freq < b.Freq
	}
	if a.SF != b.SF {
		return a.SF < b.SF
	}
	return a.BW < b.BW
}

// computeRfDeviations returns the deviating nodes, keyed by lower-case
// pubkey. Nodes without GPS or without RF parameters take no part. Points
// are bucketed on a grid of radius-sized cells, so each node is only
// measured against nodes in nearby cells.
func computeRfDeviations(params map[string]RfParams, nodes []nodeInfo, radiusKm float64, minPeers int) map[string]RfDeviation {
	out := map[string]RfDeviation{}
	if radiusKm <= 0 || len(params) == 0 {
		return out
	}
	// 110.57 km is the shortest latitude degree (at the equator), so a row is
	// never narrower than the radius and the ±1 row search covers it.
	cellDeg := radiusKm / 110.57
	type cell struct{ y, x int }
	grid := map[cell][]int{}
	pts := make([]rfDevPoint, 0, len(params))
	for _, n := range nodes {
		if !n.HasGPS {
			continue
		}
		pk := strings.ToLower(n.PublicKey)
		p, ok := params[pk]
		if !ok {
			continue
		}
		c := cell{int(math.Floor(n.Lat / cellDeg)), int(math.Floor(n.Lon / cellDeg))}
		grid[c] = append(grid[c], len(pts))
		pts = append(pts, rfDevPoint{pk: pk, lat: n.Lat, lon: n.Lon, set: rfSettingOf(p)})
	}

	// Squared-distance test on an equirectangular projection: within 50 km
	// the error against haversine is far below the radius' own precision,
	// and it avoids the trig per pair.
	r2 := radiusKm * radiusKm
	counts := map[RfSetting]int{}
	for i, me := range pts {
		// A longitude degree shrinks with cos(lat): widen the column search
		// so the radius is still covered away from the equator.
		cosLat := math.Cos(me.lat * math.Pi / 180)
		kmPerLon := 111.32 * cosLat
		dx := 1
		if cosLat > 0.01 {
			dx = int(math.Ceil(1 / cosLat))
		} else {
			dx = int(math.Ceil(360 / cellDeg))
		}
		cy, cx := int(math.Floor(me.lat/cellDeg)), int(math.Floor(me.lon/cellDeg))
		for k := range counts {
			delete(counts, k)
		}
		peers := 0
		for y := cy - 1; y <= cy+1; y++ {
			for x := cx - dx; x <= cx+dx; x++ {
				for _, j := range grid[cell{y, x}] {
					if j == i {
						continue
					}
					o := pts[j]
					if math.Abs(o.set.Freq-me.set.Freq) > rfSameBandMHz {
						continue
					}
					dy := (o.lat - me.lat) * 110.57
					dxk := (o.lon - me.lon) * kmPerLon
					if dy*dy+dxk*dxk > r2 {
						continue
					}
					counts[o.set]++
					peers++
				}
			}
		}
		if peers < minPeers {
			continue
		}
		best, support, ok := rfMajority(counts)
		// Strict majority of the group, the node itself counted: 5 against 5
		// flags neither side.
		if !ok || support*2 <= peers+1 || best.compatible(me.set) {
			continue
		}
		var fields []string
		if !rfFreqCompatible(best.Freq, me.set.Freq, best.BW) {
			fields = append(fields, "freq")
		}
		if best.SF != me.set.SF {
			fields = append(fields, "sf")
		}
		if best.BW != me.set.BW {
			fields = append(fields, "bw")
		}
		out[me.pk] = RfDeviation{Majority: best, Agree: support, Peers: peers, Fields: fields}
	}
	return out
}

// rfMajority picks the setting compatible with the most peers. support counts
// every peer compatible with it, so 869.617 and 869.618 add up. Among settings
// with equal support it prefers the one most peers use exactly, then the
// lowest. ok is false when another setting, incompatible with the pick, has
// the same support: then there is no majority to compare against. Distinct
// settings are few (one per preset in use), so the pairwise pass is cheap.
func rfMajority(counts map[RfSetting]int) (best RfSetting, support int, ok bool) {
	sets := make([]RfSetting, 0, len(counts))
	for s := range counts {
		sets = append(sets, s)
	}
	sup := make([]int, len(sets))
	for i, a := range sets {
		for _, b := range sets {
			if a.compatible(b) {
				sup[i] += counts[b]
			}
		}
	}
	bi := -1
	for i, a := range sets {
		if bi < 0 || sup[i] > sup[bi] ||
			(sup[i] == sup[bi] && (counts[a] > counts[sets[bi]] || (counts[a] == counts[sets[bi]] && a.less(sets[bi])))) {
			bi = i
		}
	}
	if bi < 0 {
		return RfSetting{}, 0, false
	}
	for i, a := range sets {
		if sup[i] == sup[bi] && !a.compatible(sets[bi]) {
			return RfSetting{}, 0, false
		}
	}
	return sets[bi], sup[bi], true
}
