package main

import (
	"testing"
	"time"
)

// hbAgg builds an aggregate from (time, snr) receptions; snr nil = no SNR.
func hbAgg(name string, recs ...hbRec) *directHeardAgg {
	a := &directHeardAgg{ObserverName: name}
	for _, r := range recs {
		a.Count++
		a.addReception(r.at, r.snr, nil)
	}
	a.trimRecent()
	return a
}

type hbRec struct {
	at  time.Time
	snr *float64
}

func hbF(v float64) *float64 { return &v }

func hbRecs(at time.Time, vals ...float64) []hbRec {
	out := make([]hbRec, 0, len(vals))
	for i, v := range vals {
		out = append(out, hbRec{at: at.Add(time.Duration(i) * time.Minute), snr: hbF(v)})
	}
	return out
}

var hbNow = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

func TestHeardByRows_SortAndQuantiles(t *testing.T) {
	at := hbNow.Add(-2 * time.Hour)
	byObs := map[string]*directHeardAgg{
		"obsB": hbAgg("B", hbRecs(at, -12, -11, -10)...),
		"obsA": hbAgg("A", hbRecs(at, 8, 9, 10)...),
		"obsC": hbAgg("C", hbRec{at: at}, hbRec{at: at.Add(time.Minute)}),
	}
	rows := heardByRows(byObs, hbNow.Add(-7*24*time.Hour), hbNow)
	if len(rows) != 3 || rows[0].ObserverID != "obsA" || rows[1].ObserverID != "obsB" || rows[2].ObserverID != "obsC" {
		t.Fatalf("order = %+v, want obsA, obsB, obsC", rows)
	}
	a := rows[0]
	if a.ObserverName != "A" || a.Count != 3 || *a.MedianSNR != 9 || *a.P10SNR != 8 || *a.P90SNR != 10 {
		t.Fatalf("obsA = %+v, want count 3, median 9, p10 8, p90 10", a)
	}
	if c := rows[2]; c.MedianSNR != nil || c.P10SNR != nil || c.Count != 2 {
		t.Fatalf("obsC = %+v, want count 2 and no median", c)
	}
}

func TestHeardByRows_ChangeColumn(t *testing.T) {
	recent := hbRecs(hbNow.Add(-3*time.Hour), 2, 2, 2)
	earlier := hbRecs(hbNow.Add(-72*time.Hour), 6, 6, 6)
	rows := heardByRows(map[string]*directHeardAgg{"obsA": hbAgg("A", append(earlier, recent...)...)}, hbNow.Add(-7*24*time.Hour), hbNow)
	if r := rows[0]; r.RecentMedianSNR == nil || *r.RecentMedianSNR != 2 || r.EarlierMedianSNR == nil || *r.EarlierMedianSNR != 6 {
		t.Fatalf("change medians = %v, %v; want 2, 6", r.RecentMedianSNR, r.EarlierMedianSNR)
	}

	rows = heardByRows(map[string]*directHeardAgg{"obsA": hbAgg("A", append(earlier, recent[:2]...)...)}, hbNow.Add(-7*24*time.Hour), hbNow)
	if r := rows[0]; r.RecentMedianSNR != nil || r.EarlierMedianSNR != nil {
		t.Fatalf("2 recent receptions: change medians = %v, %v; want both nil", r.RecentMedianSNR, r.EarlierMedianSNR)
	}

	rows = heardByRows(map[string]*directHeardAgg{"obsA": hbAgg("A", recent...)}, hbNow.Add(-24*time.Hour), hbNow)
	if r := rows[0]; r.RecentMedianSNR != nil || r.EarlierMedianSNR != nil {
		t.Fatalf("24 h window: change medians = %v, %v; want both nil", r.RecentMedianSNR, r.EarlierMedianSNR)
	}
}

func TestHeardByRows_WindowCut(t *testing.T) {
	from := hbNow.Add(-24 * time.Hour)
	recs := append(hbRecs(from.Add(-2*time.Hour), -20), hbRecs(hbNow.Add(-time.Hour), 4)...)
	rows := heardByRows(map[string]*directHeardAgg{"obsA": hbAgg("A", recs...)}, from, hbNow)
	if len(rows) != 1 || rows[0].Count != 1 || *rows[0].MedianSNR != 4 || *rows[0].P10SNR != 4 {
		t.Fatalf("rows = %+v, want one reception at 4 dB", rows)
	}

	rows = heardByRows(map[string]*directHeardAgg{"obsA": hbAgg("A", hbRecs(from.Add(-2*time.Hour), 1)...)}, from, hbNow)
	if len(rows) != 0 {
		t.Fatalf("observer heard only before the window is listed: %+v", rows)
	}
}

func TestHeardSnrTrend_CapAndOrder(t *testing.T) {
	from := hbNow.Add(-7 * 24 * time.Hour)
	var a, b []hbRec
	a = append(a, hbRec{at: from.Add(-time.Hour), snr: hbF(-30)})
	for i := 0; i < 350; i++ {
		a = append(a, hbRec{at: hbNow.Add(-time.Duration(2*i) * time.Minute), snr: hbF(1)})
		b = append(b, hbRec{at: hbNow.Add(-time.Duration(2*i+1) * time.Minute), snr: hbF(2)})
	}
	// Receptions arrive oldest first in the store.
	for i, j := 0, len(a)-1; i < j; i, j = i+1, j-1 {
		a[i], a[j] = a[j], a[i]
	}
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	byObs := map[string]*directHeardAgg{"obsA": hbAgg("A", a...), "obsB": hbAgg("B", b...)}
	trend := heardSnrTrend(byObs, from, snrTrendMax)
	if len(trend) != snrTrendMax {
		t.Fatalf("len = %d, want %d", len(trend), snrTrendMax)
	}
	if trend[len(trend)-1].Timestamp != hbNow.Format(time.RFC3339) {
		t.Fatalf("newest = %s, want %s", trend[len(trend)-1].Timestamp, hbNow.Format(time.RFC3339))
	}
	for i := 1; i < len(trend); i++ {
		if trend[i].Timestamp < trend[i-1].Timestamp {
			t.Fatalf("not ascending at %d: %s after %s", i, trend[i].Timestamp, trend[i-1].Timestamp)
		}
	}
	for _, e := range trend {
		if e.SNR == -30.0 {
			t.Fatal("a reception before the window is in snrTrend")
		}
	}
}

func TestSignalFromMedians(t *testing.T) {
	rows := []HeardByObserver{{MedianSNR: hbF(16)}, {MedianSNR: hbF(16)}, {Count: 5}}
	if mean, sd, grade := signalFromMedians(rows); mean != 16 || sd != 0 || grade != "A" {
		t.Fatalf("got %v, %v, %s; want 16, 0, A", mean, sd, grade)
	}
	if mean, _, grade := signalFromMedians(nil); mean != 0 || grade != "D" {
		t.Fatalf("no rows: got %v, %s; want 0, D", mean, grade)
	}
}
