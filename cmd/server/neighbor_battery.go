package main

import (
	"sort"
	"strings"
	"time"
)

// Neighbour battery readings: observers that poll the repeaters they hear
// directly publish each repeater's battery on meshcore/<region>/<observer>/battery,
// and the ingestor stores them in neighbor_battery (cmd/ingestor/neighbor_battery.go).
// /api/nodes/{pk}/battery merges them with the node's own status reports and
// always says which observer a value came from.

const (
	batterySourceSelf     = "self"
	batterySourceNeighbor = "neighbor"

	// A node's own report wins while it is this fresh.
	batterySelfFreshFor = 2 * time.Hour
	// Neighbour observers are ranked on readings from this window.
	batteryRankWindow = 24 * time.Hour
)

// BatterySource summarises one source of a node's battery readings: the node
// itself, or one neighbour observer.
type BatterySource struct {
	Source       string   `json:"source"`
	ObserverID   string   `json:"observer_id,omitempty"`
	ObserverName string   `json:"observer_name,omitempty"`
	LatestMv     int      `json:"latest_mv"`
	LatestTs     string   `json:"latest_ts"`
	Samples      int      `json:"samples"`
	MedianSNR    *float64 `json:"median_snr,omitempty"`
	ReplyHops    *int     `json:"reply_hops,omitempty"` // of the latest reading
}

// neighborBatteryPresent reports whether neighbor_battery exists. The
// ingestor creates it, and on the first run of a build that adds it the
// server can start first, so an absent table is re-probed until it appears.
func (db *DB) neighborBatteryPresent() bool {
	if db.neighborBatteryTable.Load() {
		return true
	}
	var n int
	if err := db.conn.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'neighbor_battery'`).Scan(&n); err != nil || n == 0 {
		return false
	}
	db.neighborBatteryTable.Store(true)
	return true
}

// GetNeighborBatteryHistory returns the readings observers published for a
// node since the given time, oldest first, with each observer's name.
func (db *DB) GetNeighborBatteryHistory(pubkey, since string) ([]NodeBatterySample, error) {
	if pubkey == "" || !db.neighborBatteryPresent() {
		return nil, nil
	}
	// sampled_at is stored as 2006-01-02T15:04:05Z; compare in that format.
	if t, err := time.Parse(time.RFC3339, since); err == nil {
		since = t.UTC().Format("2006-01-02T15:04:05Z")
	}
	rows, err := db.conn.Query(`
		SELECT sampled_at, battery_mv, observer_id, snr, reply_hops
		FROM neighbor_battery
		WHERE node_id = ? AND sampled_at >= ?
		ORDER BY sampled_at ASC`, strings.ToLower(pubkey), since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []NodeBatterySample
	observers := map[string]string{}
	for rows.Next() {
		var s NodeBatterySample
		var hops int
		if err := rows.Scan(&s.Timestamp, &s.BatteryMv, &s.ObserverID, &s.SNR, &hops); err != nil {
			return nil, err
		}
		s.Source = batterySourceNeighbor
		s.ReplyHops = &hops
		observers[s.ObserverID] = ""
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	db.fillObserverNames(observers)
	for i := range out {
		out[i].ObserverName = observers[out[i].ObserverID]
	}
	return out, nil
}

// fillObserverNames looks up the name of each observer key in the map.
// observers.id is usually the uppercase key, so both cases are tried, which
// keeps the lookup on the primary key.
func (db *DB) fillObserverNames(observers map[string]string) {
	for id := range observers {
		var name string
		err := db.conn.QueryRow(`SELECT COALESCE(name, '') FROM observers WHERE id IN (?, ?) LIMIT 1`,
			strings.ToUpper(id), strings.ToLower(id)).Scan(&name)
		if err == nil {
			observers[id] = name
		}
	}
}

// mergeBatterySamples interleaves two time-ordered sample lists. Timestamps
// from the two tables differ only in fractional seconds, so they sort on the
// parsed time, falling back to the string.
func mergeBatterySamples(a, b []NodeBatterySample) []NodeBatterySample {
	if len(b) == 0 {
		return a
	}
	out := make([]NodeBatterySample, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	sort.SliceStable(out, func(i, j int) bool {
		ti, ei := time.Parse(time.RFC3339, out[i].Timestamp)
		tj, ej := time.Parse(time.RFC3339, out[j].Timestamp)
		if ei == nil && ej == nil {
			return ti.Before(tj)
		}
		return out[i].Timestamp < out[j].Timestamp
	})
	return out
}

// summariseBatterySources groups time-ordered samples by source: the node
// itself first, then neighbour observers by key.
func summariseBatterySources(samples []NodeBatterySample) []BatterySource {
	idx := map[string]int{}
	snrs := map[string][]float64{}
	var out []BatterySource
	for _, s := range samples {
		key := s.Source + "/" + s.ObserverID
		i, ok := idx[key]
		if !ok {
			i = len(out)
			idx[key] = i
			out = append(out, BatterySource{Source: s.Source, ObserverID: s.ObserverID, ObserverName: s.ObserverName})
		}
		src := &out[i]
		src.Samples++
		src.LatestMv = s.BatteryMv
		src.LatestTs = s.Timestamp
		src.ReplyHops = s.ReplyHops
		if s.SNR != nil {
			snrs[key] = append(snrs[key], *s.SNR)
		}
	}
	for key, i := range idx {
		if v := snrs[key]; len(v) > 0 {
			m := median(v)
			out[i].MedianSNR = &m
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Source == batterySourceSelf) != (out[j].Source == batterySourceSelf) {
			return out[i].Source == batterySourceSelf
		}
		return out[i].ObserverID < out[j].ObserverID
	})
	if out == nil {
		out = []BatterySource{}
	}
	return out
}

// selectBatterySource picks the source a node's latest value and status come
// from: the node's own report while it is under two hours old; otherwise the
// neighbour observer heard best (highest median SNR) among those with a
// reading in the last 24 hours; otherwise whichever source reported last.
func selectBatterySource(sources []BatterySource, now time.Time) *BatterySource {
	var best, newest *BatterySource
	var newestAt time.Time
	for i := range sources {
		s := &sources[i]
		at, err := time.Parse(time.RFC3339, s.LatestTs)
		if err != nil {
			continue
		}
		if newest == nil || at.After(newestAt) {
			newest, newestAt = s, at
		}
		if s.Source == batterySourceSelf && now.Sub(at) < batterySelfFreshFor {
			return s
		}
		if s.Source != batterySourceNeighbor || now.Sub(at) > batteryRankWindow {
			continue
		}
		if best == nil || snrRank(s) > snrRank(best) {
			best = s
		}
	}
	if best != nil {
		return best
	}
	return newest
}

func snrRank(s *BatterySource) float64 {
	if s.MedianSNR == nil {
		return -1000
	}
	return *s.MedianSNR
}
