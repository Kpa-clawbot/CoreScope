package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func airPt(base time.Time, minAgo int, secs int) airtimePoint {
	v := secs
	return airtimePoint{At: base.Add(-time.Duration(minAgo) * time.Minute), Secs: &v}
}

func pctOrNil(p *float64) interface{} {
	if p == nil {
		return nil
	}
	return *p
}

func TestRollingAirtimePct(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		pts  []airtimePoint
		want interface{}
	}{
		{"steady 10%", []airtimePoint{airPt(now, 60, 1000), airPt(now, 30, 1180), airPt(now, 0, 1360)}, 10.0},
		{"reboot mid-hour uses the segment after the reset",
			[]airtimePoint{airPt(now, 60, 0), airPt(now, 40, 100), airPt(now, 30, 5), airPt(now, 0, 185)}, 10.0},
		{"single sample", []airtimePoint{airPt(now, 0, 50)}, nil},
		{"no samples", nil, nil},
		{"span below 30 minutes", []airtimePoint{airPt(now, 25, 0), airPt(now, 0, 150)}, nil},
		{"span of exactly 30 minutes", []airtimePoint{airPt(now, 30, 0), airPt(now, 0, 180)}, 10.0},
		{"reboot leaves less than 30 minutes", []airtimePoint{airPt(now, 60, 0), airPt(now, 30, 400), airPt(now, 25, 2), airPt(now, 0, 20)}, nil},
		{"newest sample older than 15 minutes", []airtimePoint{airPt(now, 60, 0), airPt(now, 20, 240)}, nil},
		{"newest sample exactly 15 minutes old", []airtimePoint{airPt(now, 60, 0), airPt(now, 15, 270)}, 10.0},
		{"nil counters are skipped",
			[]airtimePoint{airPt(now, 60, 0), {At: now.Add(-30 * time.Minute)}, airPt(now, 0, 36)}, 1.0},
		{"clamped to 100", []airtimePoint{airPt(now, 30, 0), airPt(now, 0, 5000)}, 100.0},
		{"rounded to 2 decimals", []airtimePoint{airPt(now, 60, 0), airPt(now, 0, 1)}, 0.03},
	}
	for _, c := range cases {
		got := pctOrNil(rollingAirtimePct(c.pts, now))
		if got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestGetMetricsSummaryAirtime1h(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	now := time.Now().UTC().Truncate(time.Second)
	ins := func(obs string, minAgo, tx, rx int) {
		ts := now.Add(-time.Duration(minAgo) * time.Minute).Format(time.RFC3339)
		if _, err := db.conn.Exec("INSERT INTO observer_metrics (observer_id, timestamp, noise_floor, tx_air_secs, rx_air_secs) VALUES (?, ?, ?, ?, ?)",
			obs, ts, -110.0, tx, rx); err != nil {
			t.Fatal(err)
		}
	}
	// obs1: a sample 3 h ago with a far lower counter must not count; inside
	// the hour it transmits 180 s in 30 min (10%) and receives 360 s (20%).
	ins("obs1", 180, 0, 0)
	ins("obs1", 30, 5000, 9000)
	ins("obs1", 0, 5180, 9360)
	// obs2: only one sample inside the hour, so no rolling figure.
	ins("obs2", 120, 0, 0)
	ins("obs2", 5, 900, 900)
	// obs3: a full hour at 10%, but silent for the last 40 minutes.
	ins("obs3", 60, 0, 0)
	ins("obs3", 40, 120, 120)

	summary, err := db.GetMetricsSummary(now.Add(-24*time.Hour).Format(time.RFC3339), now)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]MetricsSummaryRow{}
	for _, r := range summary {
		byID[r.ObserverID] = r
	}
	if got := pctOrNil(byID["obs1"].TxAirtimePct1h); got != 10.0 {
		t.Errorf("obs1 tx 1h = %v, want 10", got)
	}
	if got := pctOrNil(byID["obs1"].RxAirtimePct1h); got != 20.0 {
		t.Errorf("obs1 rx 1h = %v, want 20", got)
	}
	if got := pctOrNil(byID["obs2"].TxAirtimePct1h); got != nil {
		t.Errorf("obs2 tx 1h = %v, want nil", got)
	}
	if _, ok := byID["obs3"]; !ok {
		t.Fatal("obs3 missing from the summary")
	}
	if got := pctOrNil(byID["obs3"].TxAirtimePct1h); got != nil {
		t.Errorf("obs3 (stale) tx 1h = %v, want nil", got)
	}
}

func TestMetricsSummaryDutyCycleWarnPct(t *testing.T) {
	srv, router := setupTestServer(t)
	get := func() float64 {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest("GET", "/api/observers/metrics/summary?window=1h", nil))
		if w.Code != 200 {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
		var resp MetricsSummaryResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Observers == nil {
			t.Fatal("observers must be [] not null")
		}
		return resp.DutyCycleWarnPct
	}
	if got := get(); got != 10 {
		t.Errorf("default duty-cycle warn = %v, want 10", got)
	}
	srv.cfg.DutyCycleWarnPct = 1
	if got := get(); got != 1 {
		t.Errorf("configured duty-cycle warn = %v, want 1", got)
	}
}
