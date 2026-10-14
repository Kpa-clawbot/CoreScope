package main

import (
	"math"
	"strings"
	"sync"
	"time"
)

// LoRa settings that differ from the surrounding nodes (issue #1268, M3).
//
// Two nodes only hear each other when frequency, spreading factor and
// bandwidth match. The coding rate does not have to: RadioLib starts SX126x
// radios with an explicit LoRa header (SX126x::begin sets
// RADIOLIB_SX126X_LORA_HEADER_EXPLICIT at the RadioLib commit the firmware
// pins), the firmware never switches to implicit, and the explicit header
// carries the coding rate, so a CR mismatch still decodes.
//
// A node is compared with the nodes within the radius on the same band only
// (frequency within rfSameBandMHz), so a deliberate 433 MHz network next to
// an 868 MHz one flags neither. It is flagged when one setting is held by a
// clear majority of those peers and the node uses another.

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
	Agree    int       `json:"agree"`  // peers using Majority
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

// computeRfDeviations returns the deviating nodes, keyed by lower-case
// pubkey. Nodes without GPS or without RF parameters take no part. Points
// are bucketed on a grid of radius-sized cells, so each node is only
// measured against nodes in nearby cells.
func computeRfDeviations(params map[string]RfParams, nodes []nodeInfo, radiusKm float64, minPeers int) map[string]RfDeviation {
	out := map[string]RfDeviation{}
	if radiusKm <= 0 || len(params) == 0 {
		return out
	}
	cellDeg := radiusKm / 111.0
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
		var best RfSetting
		bestN, tie := 0, false
		for s, n := range counts {
			switch {
			case n > bestN:
				best, bestN, tie = s, n, false
			case n == bestN:
				tie = true
			}
		}
		if tie || bestN*2 < peers || best == me.set {
			continue
		}
		var fields []string
		if best.Freq != me.set.Freq {
			fields = append(fields, "freq")
		}
		if best.SF != me.set.SF {
			fields = append(fields, "sf")
		}
		if best.BW != me.set.BW {
			fields = append(fields, "bw")
		}
		out[me.pk] = RfDeviation{Majority: best, Agree: bestN, Peers: peers, Fields: fields}
	}
	return out
}
