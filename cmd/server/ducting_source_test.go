package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeOpenMeteo answers /v1/forecast like Open-Meteo: one object per
// requested location (an array when there are several). fill sets the
// hourly values of location i; nil leaves every variable null.
type fakeOpenMeteo struct {
	t     *testing.T
	hours int
	fill  func(i int, lat, lon float64, hourly map[string][]*float64)
	// status, when non-zero, is answered (with body) instead of data.
	status int
	body   string

	mu   sync.Mutex
	reqs []*http.Request
}

func (f *fakeOpenMeteo) requests() []*http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*http.Request(nil), f.reqs...)
}

func (f *fakeOpenMeteo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.reqs = append(f.reqs, r)
	status, body, fill, hours := f.status, f.body, f.fill, f.hours
	f.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		fmt.Fprint(w, body)
		return
	}
	q := r.URL.Query()
	lats := strings.Split(q.Get("latitude"), ",")
	lons := strings.Split(q.Get("longitude"), ",")
	vars := strings.Split(q.Get("hourly"), ",")
	locs := make([]json.RawMessage, len(lats))
	for i := range lats {
		lat, _ := strconv.ParseFloat(lats[i], 64)
		lon, _ := strconv.ParseFloat(lons[i], 64)
		hourly := map[string][]*float64{}
		times := make([]*float64, hours)
		for h := range times {
			v := float64(1795046400 + 3600*h)
			times[h] = &v
		}
		hourly["time"] = times
		for _, v := range vars {
			hourly[v] = make([]*float64, hours)
		}
		if fill != nil {
			fill(i, lat, lon, hourly)
		}
		if hourly["error"] != nil { // marker set by fill: answer a per-location error
			locs[i] = json.RawMessage(`{"error":true,"reason":"No data is available for this location"}`)
			continue
		}
		b, err := json.Marshal(struct {
			Latitude  float64               `json:"latitude"`
			Longitude float64               `json:"longitude"`
			Elevation float64               `json:"elevation"`
			Hourly    map[string][]*float64 `json:"hourly"`
		}{lat, lon, 0, hourly})
		if err != nil {
			f.t.Errorf("marshal: %v", err)
		}
		locs[i] = b
	}
	w.Header().Set("Content-Type", "application/json")
	if len(locs) == 1 {
		w.Write(locs[0])
		return
	}
	b, _ := json.Marshal(locs)
	w.Write(b)
}

func ductF(v float64) *float64 { return &v }

func newTestDuctingSource(t *testing.T, f *fakeOpenMeteo, model string) (*ductingSource, *httptest.Server, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	m, _ := ductingModel(model)
	src := newDuctingSource(srv.URL, m)
	var waits []time.Duration
	src.sleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return ctx.Err()
	}
	return src, srv, &waits
}

func ductingTestCells(n int) [][2]float64 {
	cells := make([][2]float64, n)
	for i := range cells {
		cells[i] = [2]float64{10 + 0.5*float64(i)/3, -20.123456 + float64(i%7)}
	}
	return cells
}

func TestDuctingSourceQuery(t *testing.T) {
	f := &fakeOpenMeteo{t: t, hours: 2}
	src, _, _ := newTestDuctingSource(t, f, "gfs")
	cells := ductingTestCells(120)
	cols, err := src.fetch(context.Background(), cells, 36)
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 120 {
		t.Fatalf("got %d columns, want 120", len(cols))
	}
	reqs := f.requests()
	if len(reqs) != 3 {
		t.Fatalf("got %d requests, want 3 (50+50+20)", len(reqs))
	}
	m, _ := ductingModel("gfs")
	sent := 0
	for ri, r := range reqs {
		if r.URL.Path != "/v1/forecast" {
			t.Errorf("path %q", r.URL.Path)
		}
		if ua := r.Header.Get("User-Agent"); ua != ductingUserAgent {
			t.Errorf("User-Agent %q", ua)
		}
		q := r.URL.Query()
		for k, want := range map[string]string{
			"models": "gfs_global", "forecast_hours": "36", "timeformat": "unixtime", "timezone": "GMT",
		} {
			if got := q.Get(k); got != want {
				t.Errorf("%s=%q, want %q", k, got, want)
			}
		}
		vars := map[string]bool{}
		for _, v := range strings.Split(q.Get("hourly"), ",") {
			vars[v] = true
		}
		want := []string{"temperature_2m", "relative_humidity_2m", "surface_pressure"}
		for _, p := range m.LevelsHPa {
			lv := strconv.Itoa(int(p))
			want = append(want, "temperature_"+lv+"hPa", "relative_humidity_"+lv+"hPa", "geopotential_height_"+lv+"hPa")
		}
		if len(vars) != len(want) {
			t.Errorf("got %d variables, want %d", len(vars), len(want))
		}
		for _, v := range want {
			if !vars[v] {
				t.Errorf("variable %s missing", v)
			}
		}
		lats := strings.Split(q.Get("latitude"), ",")
		lons := strings.Split(q.Get("longitude"), ",")
		wantN := 50
		if ri == 2 {
			wantN = 20
		}
		if len(lats) != wantN || len(lons) != wantN {
			t.Errorf("request %d: %d/%d locations, want %d", ri, len(lats), len(lons), wantN)
		}
		for i := range lats {
			c := cells[sent+i]
			if lats[i] != strconv.FormatFloat(c[0], 'f', 4, 64) || lons[i] != strconv.FormatFloat(c[1], 'f', 4, 64) {
				t.Errorf("coordinate %s,%s for cell %v", lats[i], lons[i], c)
			}
			if dot := strings.IndexByte(lons[i], '.'); dot < 0 || len(lons[i])-dot-1 != 4 {
				t.Errorf("longitude %q not 4 decimals", lons[i])
			}
		}
		sent += len(lats)
	}
	// Columns keep request order and the requested cell centres.
	for i, c := range cols {
		if c.Lat != cells[i][0] || c.Lon != cells[i][1] {
			t.Fatalf("column %d at %v,%v, want %v", i, c.Lat, c.Lon, cells[i])
		}
	}
}

func TestDuctingSourcePacing(t *testing.T) {
	f := &fakeOpenMeteo{t: t, hours: 1}
	src, _, waits := newTestDuctingSource(t, f, "gfs")
	if _, err := src.fetch(context.Background(), ductingTestCells(100), 1); err != nil {
		t.Fatal(err)
	}
	// 50 GFS points = 50 * 4.2 = 210 calls; at 400/min that is 31.5 s.
	if len(*waits) != 1 {
		t.Fatalf("waits %v, want exactly one (none after the last batch)", *waits)
	}
	if d := (*waits)[0]; d < 31400*time.Millisecond || d > 31600*time.Millisecond {
		t.Errorf("wait %v, want ~31.5s", d)
	}
	if n := len(f.requests()); n != 2 {
		t.Errorf("%d requests, want 2", n)
	}
}

func TestDuctingSourceSingleObjectAndNulls(t *testing.T) {
	f := &fakeOpenMeteo{t: t, hours: 3, fill: func(i int, lat, lon float64, h map[string][]*float64) {
		h["surface_pressure"][0] = ductF(1013)
		h["temperature_1000hPa"][1] = ductF(12.5)
	}}
	src, _, _ := newTestDuctingSource(t, f, "gfs")
	cols, err := src.fetch(context.Background(), [][2]float64{{51.25, -0.25}}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 1 {
		t.Fatalf("%d columns", len(cols))
	}
	c := cols[0]
	if len(c.Times) != 3 || c.Times[0] != 1795046400 || c.Times[2] != 1795046400+7200 {
		t.Errorf("times %v", c.Times)
	}
	if c.Elevation != 0 {
		t.Errorf("elevation %v", c.Elevation)
	}
	if c.Hours[0].SurfP != 1013 || !math.IsNaN(c.Hours[1].SurfP) {
		t.Errorf("surface pressure %v %v", c.Hours[0].SurfP, c.Hours[1].SurfP)
	}
	if c.Hours[1].Temp[0] != 12.5 || !math.IsNaN(c.Hours[0].Temp[0]) || !math.IsNaN(c.Hours[1].Temp[1]) {
		t.Errorf("level temps %v %v", c.Hours[0].Temp, c.Hours[1].Temp)
	}
	m, _ := ductingModel("gfs")
	if len(c.Hours[0].Temp) != len(m.LevelsHPa) || len(c.Hours[0].Geo) != len(m.LevelsHPa) {
		t.Errorf("level arrays %d/%d, want %d", len(c.Hours[0].Temp), len(c.Hours[0].Geo), len(m.LevelsHPa))
	}
	// All-null levels are skipped by the physics: no usable profile.
	r := AnalyzeDucting(DuctProfile(c.Elevation, c.Hours[2].SurfP, c.Hours[2].T2m, c.Hours[2].RH2m,
		m.LevelsHPa, c.Hours[2].Temp, c.Hours[2].RH, c.Hours[2].Geo))
	if r.Class != DuctClassNoData {
		t.Errorf("all-null hour class %d, want no data", r.Class)
	}
}

func TestDuctingSourceDropsLocationErrors(t *testing.T) {
	f := &fakeOpenMeteo{t: t, hours: 1, fill: func(i int, lat, lon float64, h map[string][]*float64) {
		if i%2 == 1 {
			h["error"] = []*float64{}
		}
	}}
	src, _, _ := newTestDuctingSource(t, f, "rrfs-conus")
	cells := ductingTestCells(6)
	cols, err := src.fetch(context.Background(), cells, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 3 {
		t.Fatalf("%d columns, want 3", len(cols))
	}
	for k, c := range cols {
		if c.Lat != cells[2*k][0] {
			t.Errorf("column %d is cell %v, want %v", k, c.Lat, cells[2*k][0])
		}
	}
}

func TestDuctingSourceErrors(t *testing.T) {
	long := strings.Repeat("x", 300)
	cases := []struct {
		name   string
		status int
		body   string
		check  func(t *testing.T, err error)
	}{
		{"429", 429, `{"error":true,"reason":"Too many requests"}`, func(t *testing.T, err error) {
			if !errors.Is(err, errDuctingRateLimited) {
				t.Errorf("err %v, want errDuctingRateLimited", err)
			}
		}},
		{"500", 500, "oops", func(t *testing.T, err error) {
			var he *ductingHTTPError
			if !errors.As(err, &he) || he.Status != 500 || !strings.Contains(err.Error(), "500") {
				t.Errorf("err %v", err)
			}
		}},
		{"400 reason", 400, `{"error":true,"reason":"Cannot initialize WeatherVariable from invalid String value"}`, func(t *testing.T, err error) {
			if !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "Cannot initialize WeatherVariable") {
				t.Errorf("err %v", err)
			}
		}},
		{"400 long reason", 400, `{"error":true,"reason":"` + long + `"}`, func(t *testing.T, err error) {
			var he *ductingHTTPError
			if !errors.As(err, &he) || len(he.Reason) != ductingReasonMaxLen {
				t.Errorf("reason length %d, want %d", len(he.Reason), ductingReasonMaxLen)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeOpenMeteo{t: t, status: tc.status, body: tc.body}
			src, srv, _ := newTestDuctingSource(t, f, "gfs")
			_, err := src.fetch(context.Background(), ductingTestCells(2), 1)
			if err == nil {
				t.Fatal("no error")
			}
			tc.check(t, err)
			ductingAssertNoURL(t, err, srv.URL)
		})
	}
}

func ductingAssertNoURL(t *testing.T, err error, base string) {
	t.Helper()
	s := err.Error()
	host := strings.TrimPrefix(base, "http://")
	if strings.Contains(s, base) || strings.Contains(s, host) || strings.Contains(s, "/v1/forecast") || strings.Contains(s, "latitude") {
		t.Errorf("error leaks the URL: %q", s)
	}
}

func TestDuctingSourceTransportErrorHidesURL(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL
	srv.Close() // connection refused
	m, _ := ductingModel("gfs")
	src := newDuctingSource(base, m)
	_, err := src.fetch(context.Background(), ductingTestCells(1), 1)
	if err == nil {
		t.Fatal("no error")
	}
	ductingAssertNoURL(t, err, base)
}

func TestDuctingSourceOversizeBody(t *testing.T) {
	f := &fakeOpenMeteo{t: t, hours: 48}
	src, _, _ := newTestDuctingSource(t, f, "gfs")
	src.maxBody = 1024
	_, err := src.fetch(context.Background(), ductingTestCells(3), 48)
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("err %v, want size error", err)
	}
}

func TestDuctingSourceMalformed(t *testing.T) {
	m, _ := ductingModel("gfs")
	cells := ductingTestCells(2)
	if _, err := parseDuctingResponse([]byte(`[{"hourly":{"time":[1]}}]`), cells, m); err == nil {
		t.Error("location count mismatch accepted")
	}
	if _, err := parseDuctingResponse([]byte(`not json`), cells[:1], m); err == nil {
		t.Error("invalid JSON accepted")
	}
	cols, err := parseDuctingResponse([]byte(`{"elevation":5,"hourly":{"time":[]}}`), cells[:1], m)
	if err != nil || len(cols) != 0 {
		t.Errorf("no-times location: %v %d", err, len(cols))
	}
}

func TestDuctingSourceContextCancel(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	m, _ := ductingModel("gfs")
	src := newDuctingSource(srv.URL, m)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	_, err := src.fetch(ctx, ductingTestCells(1), 1)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("cancel took %v", d)
	}

	// The default pacing sleep honours ctx too.
	ctx2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if err := ductingSleep(ctx2, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("sleep err %v", err)
	}
	if err := ductingSleep(context.Background(), time.Millisecond); err != nil {
		t.Errorf("sleep err %v", err)
	}
}

func TestDuctingSourceCallBudget(t *testing.T) {
	f := &fakeOpenMeteo{t: t, hours: 1}
	src, _, _ := newTestDuctingSource(t, f, "gfs")
	now := time.Unix(1795046400, 0)
	src.now = func() time.Time { return now }
	// 1000 points = 4200 calls: fits the hourly cap once, not twice.
	cells := ductingTestCells(1000)
	if _, err := src.fetch(context.Background(), cells, 1); err != nil {
		t.Fatal(err)
	}
	n := len(f.requests())
	if _, err := src.fetch(context.Background(), cells, 1); !errors.Is(err, errDuctingBudget) {
		t.Fatalf("err %v, want errDuctingBudget", err)
	}
	if len(f.requests()) != n {
		t.Error("a request was sent past the budget")
	}
	// An hour later the hourly cap has room again; the daily one still
	// counts the first fetch.
	now = now.Add(time.Hour)
	if _, err := src.fetch(context.Background(), cells, 1); err != nil {
		t.Fatalf("after an hour: %v", err)
	}
	now = now.Add(2 * time.Hour)
	if _, err := src.fetch(context.Background(), cells, 1); !errors.Is(err, errDuctingBudget) {
		t.Fatalf("daily cap: err %v, want errDuctingBudget", err)
	}
	now = now.Add(23 * time.Hour) // the first two fetches leave the window
	if _, err := src.fetch(context.Background(), cells, 1); err != nil {
		t.Fatalf("after a day: %v", err)
	}
	src.mu.Lock()
	entries := len(src.ledger)
	src.mu.Unlock()
	if entries > 2*1000/ductingBatchSize {
		t.Errorf("ledger kept %d entries; old ones are not pruned", entries)
	}
}
