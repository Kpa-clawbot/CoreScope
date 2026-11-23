package main

import (
	"math"
	"sort"
	"strings"
	"time"
)

// The node analytics "Heard by observers" list: per fixed observer that
// heard the node directly, the reception count and SNR spread in the
// window, read from the direct-heard snapshot (see heard_snr.go).

const (
	// heardChangeMinDays is the shortest window that shows the change
	// column (last 24 h against the rest of the window).
	heardChangeMinDays = 3
	// heardChangeMinCount is the fewest SNR receptions each part needs.
	heardChangeMinCount = 3
	// snrTrendMax caps snrTrend to the newest receptions in the window.
	snrTrendMax = 500
)

func hourKey(t time.Time) int32 { return int32(t.Unix() / 3600) }

// heardByRows builds one row per observer heard in [from, now], sorted by
// median SNR (best first), rows without an SNR last, then by observer id.
// The window is cut at hour granularity: the hour holding from counts.
func heardByRows(byObs map[string]*directHeardAgg, from, now time.Time) []HeardByObserver {
	fromKey := hourKey(from)
	splitKey := hourKey(now.Add(-24 * time.Hour))
	showChange := now.Sub(from) >= heardChangeMinDays*24*time.Hour
	rows := make([]HeardByObserver, 0, len(byObs))
	for id, agg := range byObs {
		var all, recent, earlier snrHist
		count := 0
		for k, h := range agg.Hours {
			if k < fromKey {
				continue
			}
			count += int(h.N)
			all.addHour(h)
			if !showChange {
				continue
			}
			if k >= splitKey {
				recent.addHour(h)
			} else {
				earlier.addHour(h)
			}
		}
		if count == 0 {
			continue
		}
		row := HeardByObserver{ObserverID: id, ObserverName: agg.ObserverName, Count: count}
		row.MedianSNR = quantilePtr(&all, 0.5)
		row.P10SNR = quantilePtr(&all, 0.1)
		row.P90SNR = quantilePtr(&all, 0.9)
		if showChange && recent.total >= heardChangeMinCount && earlier.total >= heardChangeMinCount {
			row.RecentMedianSNR = quantilePtr(&recent, 0.5)
			row.EarlierMedianSNR = quantilePtr(&earlier, 0.5)
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i].MedianSNR, rows[j].MedianSNR
		if (a == nil) != (b == nil) {
			return a != nil
		}
		if a != nil && *a != *b {
			return *a > *b
		}
		return rows[i].ObserverID < rows[j].ObserverID
	})
	return rows
}

func quantilePtr(h *snrHist, q float64) *float64 {
	v, ok := h.quantile(q)
	if !ok {
		return nil
	}
	return &v
}

// heardSnrTrend returns the newest max direct receptions with an SNR at or
// after from, across all observers, oldest first.
func heardSnrTrend(byObs map[string]*directHeardAgg, from time.Time, max int) []SnrTrendEntry {
	type ref struct {
		id  string
		smp heardSample
	}
	fromT := uint32(from.Unix())
	refs := make([]ref, 0, 64)
	for id, agg := range byObs {
		for _, smp := range agg.Recent {
			if smp.T >= fromT && smp.SNR != math.MinInt8 {
				refs = append(refs, ref{id, smp})
			}
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].smp.T != refs[j].smp.T {
			return refs[i].smp.T < refs[j].smp.T
		}
		return refs[i].id < refs[j].id
	})
	if len(refs) > max {
		refs = refs[len(refs)-max:]
	}
	out := make([]SnrTrendEntry, 0, len(refs))
	for _, r := range refs {
		e := SnrTrendEntry{
			Timestamp:    time.Unix(int64(r.smp.T), 0).UTC().Format(time.RFC3339),
			SNR:          float64(r.smp.SNR) / 4,
			ObserverID:   r.id,
			ObserverName: strOrNil(byObs[r.id].ObserverName),
		}
		if r.smp.RSSI != math.MinInt16 {
			e.RSSI = float64(r.smp.RSSI)
		}
		out = append(out, e)
	}
	return out
}

// signalFromMedians averages the per-observer median SNRs, so each observer
// counts once however often it heard the node. Rows without a median are
// left out.
func signalFromMedians(rows []HeardByObserver) (mean, stddev float64, grade string) {
	var vals []float64
	for _, r := range rows {
		if r.MedianSNR != nil {
			vals = append(vals, *r.MedianSNR)
		}
	}
	if len(vals) > 0 {
		for _, v := range vals {
			mean += v
		}
		mean /= float64(len(vals))
		var sq float64
		for _, v := range vals {
			sq += (v - mean) * (v - mean)
		}
		stddev = math.Sqrt(sq / float64(len(vals)))
	}
	return mean, stddev, signalGradeFor(mean, stddev)
}

// signalGradeFor maps a mean SNR and its spread to the node analytics grade.
func signalGradeFor(mean, stddev float64) string {
	switch {
	case mean > 15 && stddev < 2:
		return "A"
	case mean > 15:
		return "A-"
	case mean > 12 && stddev < 3:
		return "B+"
	case mean > 8:
		return "B"
	case mean > 3:
		return "C"
	}
	return "D"
}

// directHeardFor returns the snapshot's observers for one node.
func directHeardFor(idx directHeardIndex, pubkey string) map[string]*directHeardAgg {
	return idx[strings.ToLower(pubkey)]
}
