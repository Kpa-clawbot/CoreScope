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
// requested location (an array when there are several). The time axis is
// start_hour..end_hour (inclusive) when they are sent, else hours hours from
// 1795046400. fill sets the hourly values of location i; nil leaves every
// variable null.
type fakeOpenMeteo struct {
	t     *testing.T
	hours int
	fill  func(i int, lat, lon float64, hourly map[string][]*float64)
	// status, when non-zero, is answered (with body) instead of data.
	status int
	body   string
	// failReq, when set, is asked with the request's index (0-based); a
	// non-zero answer is sent as that HTTP status instead of data.
	failReq func(n int) int

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
	if f.failReq != nil {
		if st := f.failReq(len(f.reqs) - 1); st != 0 {
			status, body = st, `{"error":true,"reason":"fake batch failure"}`
		}
	}
	f.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		fmt.Fprint(w, body)
		return
	}
	q := r.URL.Query()
	first := int64(1795046400)
	if q.Has("start_hour") || q.Has("end_hour") {
		st, err1 := time.Parse("2006-01-02T15:04", q.Get("start_hour"))
		en, err2 := time.Parse("2006-01-02T15:04", q.Get("end_hour"))
		if err1 != nil || err2 != nil || en.Before(st) || q.Has("forecast_hours") {
			f.t.Errorf("bad window start_hour=%q end_hour=%q forecast_hours=%q", q.Get("start_hour"), q.Get("end_hour"), q.Get("forecast_hours"))
			w.WriteHeader(400)
			return
		}
		first, hours = st.Unix(), int(en.Sub(st)/time.Hour)+1
	}
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
			v := float64(first + int64(3600*h))
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
	src.now = func() time.Time { return time.Unix(1795046400, 0) } // the fake's first hour
	var waits []time.Duration
	src.sleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		return ctx.Err()
	}
	return src, srv, &waits
}

// ductingFetchCols is src.fetch returning only the columns.
func ductingFetchCols(ctx context.Context, src *ductingSource, cells [][2]float64, hours int) ([]ductingColumn, error) {
	res, err := src.fetch(ctx, cells, hours)
	return res.Cols, err
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
	cols, err := ductingFetchCols(context.Background(), src, cells, 36)
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
			// The fake's first hour, 1795046400, is 2026-11-19T00:00 UTC;
			// end_hour is inclusive: 36 hours end at +35 h.
			"models": "gfs_global", "start_hour": "2026-11-19T00:00", "end_hour": "2026-11-20T11:00",
			"timeformat": "unixtime", "timezone": "GMT", "forecast_hours": "",
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
	src.capped = true // pacing is for the public API's per-minute limit
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

// A self-hosted source has no per-minute limit: batches go back to back.
func TestDuctingSourceSelfHostedNotPaced(t *testing.T) {
	f := &fakeOpenMeteo{t: t, hours: 1}
	src, _, waits := newTestDuctingSource(t, f, "gfs")
	src.capped = false
	if _, err := src.fetch(context.Background(), ductingTestCells(100), 1); err != nil {
		t.Fatal(err)
	}
	if len(*waits) != 0 {
		t.Fatalf("waits %v, want none for a self-hosted source", *waits)
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
	cols, err := ductingFetchCols(context.Background(), src, [][2]float64{{51.25, -0.25}}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 1 {
		t.Fatalf("%d columns", len(cols))
	}
	c := cols[0]
	if len(c.Hours) != 3 {
		t.Errorf("%d hours, want 3", len(c.Hours))
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
	cols, err := ductingFetchCols(context.Background(), src, cells, 1)
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
	if _, err := parseDuctingResponse([]byte(`[{"hourly":{"time":[1]}}]`), cells, m, ductingWindow{Start: 1795046400, Hours: 48}); err == nil {
		t.Error("location count mismatch accepted")
	}
	if _, err := parseDuctingResponse([]byte(`not json`), cells[:1], m, ductingWindow{Start: 1795046400, Hours: 48}); err == nil {
		t.Error("invalid JSON accepted")
	}
	cols, err := parseDuctingResponse([]byte(`{"elevation":5,"hourly":{"time":[]}}`), cells[:1], m, ductingWindow{Start: 1795046400, Hours: 48})
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
	src.capped = true // the fake runs on a loopback URL; exercise the public-API caps
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

func TestDuctingIsPublicAPI(t *testing.T) {
	for u, want := range map[string]bool{
		"":                                        true,
		"https://api.open-meteo.com":              true,
		"https://API.Open-Meteo.com/":             true,
		"https://api.open-meteo.com.":             true,
		"https://API.Open-Meteo.com.:443/x":       true,
		"https://api.open-meteo.com:443":          true,
		"https://om.example.com":                  false,
		"http://localhost:8080":                   false,
		"http://127.0.0.1:8080":                   false,
		"https://api.open-meteo.com.evil.example": false,
	} {
		if got := ductingIsPublicAPI(u); got != want {
			t.Errorf("ductingIsPublicAPI(%q) = %v, want %v", u, got, want)
		}
	}
}

func TestDuctingSourceSelfHostedNoCallCap(t *testing.T) {
	f := &fakeOpenMeteo{t: t, hours: 1}
	src, srv, _ := newTestDuctingSource(t, f, "gfs")
	if src.capped {
		t.Fatalf("source for %s is capped; a non-public host must not be", srv.URL)
	}
	// 1500 points = 6300 calls: over the public hourly cap of 4500.
	cells := ductingTestCells(1500)
	if _, err := src.fetch(context.Background(), cells, 1); err != nil {
		t.Fatalf("self-hosted fetch above the hourly cap: %v", err)
	}
	if len(f.requests()) != 1500/ductingBatchSize {
		t.Errorf("%d requests, want %d", len(f.requests()), 1500/ductingBatchSize)
	}
	// Nor is it paced: the per-minute pace is for the public API only.
	src2, _, waits := newTestDuctingSource(t, &fakeOpenMeteo{t: t, hours: 1}, "gfs")
	if _, err := src2.fetch(context.Background(), ductingTestCells(100), 1); err != nil {
		t.Fatal(err)
	}
	if len(*waits) != 0 {
		t.Errorf("waits %v, want none for a self-hosted source", *waits)
	}
	// The same fetch is refused for the public host.
	src.capped = true
	if _, err := src.fetch(context.Background(), cells, 1); !errors.Is(err, errDuctingBudget) {
		t.Fatalf("public: err %v, want errDuctingBudget", err)
	}
	if !newDuctingSource("https://api.open-meteo.com", src.model).capped {
		t.Error("public API source is not capped")
	}
}

func TestDuctingSourceTimeAxis(t *testing.T) {
	m, _ := ductingModel("gfs")
	cells := ductingTestCells(1)
	mk := func(times ...float64) []byte {
		ts := make([]*float64, len(times))
		for i := range times {
			ts[i] = &times[i]
		}
		b, _ := json.Marshal(map[string]any{"elevation": 1, "hourly": map[string]any{"time": ts}})
		return b
	}
	t0 := float64(1795046400)
	w := ductingWindow{Start: 1795046400, Hours: 3}
	for name, tc := range map[string]struct {
		times []float64
		ok    bool
	}{
		"the window":      {[]float64{t0, t0 + 3600, t0 + 7200}, true},
		"one hour more":   {[]float64{t0, t0 + 3600, t0 + 7200, t0 + 10800}, false},
		"one hour short":  {[]float64{t0, t0 + 3600}, false},
		"empty":           {nil, false},
		"next hour":       {[]float64{t0 + 3600, t0 + 7200, t0 + 10800}, false}, // the hour rolled over
		"previous hour":   {[]float64{t0 - 3600, t0, t0 + 3600}, false},
		"unsorted":        {[]float64{t0 + 3600, t0, t0 + 7200}, false},
		"duplicate":       {[]float64{t0, t0, t0 + 3600}, false},
		"not hourly":      {[]float64{t0, t0 + 1800, t0 + 3600}, false},
		"fractional":      {[]float64{t0 + 0.5, t0 + 3600.5, t0 + 7200.5}, false},
		"epoch zero":      {[]float64{0, 3600, 7200}, false},
		"far future":      {[]float64{4e12, 4e12 + 3600, 4e12 + 7200}, false},
		"gap then extend": {[]float64{t0, t0 + 7200, t0 + 10800}, false},
	} {
		cols, err := parseDuctingResponse(mk(tc.times...), cells, m, w)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := len(cols) == 1; got != tc.ok {
			t.Errorf("%s: accepted=%v, want %v", name, got, tc.ok)
		}
	}
	// A null time is rejected too.
	b := []byte(`{"elevation":1,"hourly":{"time":[1795046400,null,1795053600]}}`)
	if cols, err := parseDuctingResponse(b, cells, m, w); err != nil || len(cols) != 0 {
		t.Errorf("null time: %v %d", err, len(cols))
	}
}

func TestDuctingWindow(t *testing.T) {
	// Any instant inside an hour gives that UTC hour; zones do not matter.
	at := time.Date(2026, 11, 19, 13, 59, 59, 999, time.FixedZone("x", 5*3600+1800))
	w := newDuctingWindow(at, 4)
	want := time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC).Unix() // 13:59 at +05:30 is 08:29 UTC
	if w.Start != want || w.Hours != 4 {
		t.Fatalf("window %+v, want start %d, 4 hours", w, want)
	}
	if ts := w.times(); len(ts) != 4 || ts[0] != want || ts[3] != want+3*3600 {
		t.Errorf("times %v", ts)
	}
	if got := ductingHourParam(want); got != "2026-11-19T08:00" {
		t.Errorf("hour param %q", got)
	}
}

// A paced fetch whose batches straddle an hour boundary asks every batch for
// the same window, so every column is kept (forecast_hours counted from each
// request's own hour and lost the later batches).
func TestDuctingSourceHourRolloverKeepsEveryBatch(t *testing.T) {
	f := &fakeOpenMeteo{t: t}
	src, _, _ := newTestDuctingSource(t, f, "gfs")
	base := time.Unix(1795046400+3600-90, 0) // 90 s before the next hour
	var mu sync.Mutex
	calls := 0
	src.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return base.Add(time.Duration(calls) * 40 * time.Second)
	}
	res, err := src.fetch(context.Background(), ductingTestCells(150), 6)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Cols) != 150 || res.Failed != 0 || res.Batches != 3 {
		t.Fatalf("%d columns, %d of %d batches failed; want all 150 kept", len(res.Cols), res.Failed, res.Batches)
	}
	if res.Window.Start != 1795046400 || res.Window.Hours != 6 {
		t.Errorf("window %+v", res.Window)
	}
	reqs := f.requests()
	if len(reqs) != 3 {
		t.Fatalf("%d requests", len(reqs))
	}
	if src.now().Unix() < 1795046400+3600 {
		t.Fatal("the clock never crossed the hour; the test proves nothing")
	}
	for i, r := range reqs {
		q := r.URL.Query()
		if q.Get("start_hour") != "2026-11-19T00:00" || q.Get("end_hour") != "2026-11-19T05:00" || q.Has("forecast_hours") {
			t.Errorf("request %d: start_hour=%q end_hour=%q", i, q.Get("start_hour"), q.Get("end_hour"))
		}
	}
}

func TestDuctingSourcePartialFailureKeepsOtherBatches(t *testing.T) {
	f := &fakeOpenMeteo{t: t, failReq: func(n int) int {
		if n == 1 {
			return http.StatusInternalServerError
		}
		return 0
	}}
	src, _, waits := newTestDuctingSource(t, f, "gfs")
	src.capped = true // paced like the public API, to check a failed batch still counts
	cells := ductingTestCells(150)
	res, err := src.fetch(context.Background(), cells, 2)
	if err != nil {
		t.Fatalf("one of three batches failed: %v", err)
	}
	if res.Failed != 1 || res.Batches != 3 || len(res.Cols) != 100 {
		t.Fatalf("%d of %d failed, %d columns; want 1 of 3, 100", res.Failed, res.Batches, len(res.Cols))
	}
	// The failed batch's cells are absent; the others keep request order.
	for i, c := range res.Cols {
		want := cells[i]
		if i >= 50 {
			want = cells[i+50]
		}
		if c.Lat != want[0] || c.Lon != want[1] {
			t.Fatalf("column %d at %v,%v, want %v", i, c.Lat, c.Lon, want)
		}
	}
	// The failed batch was paid for, so the pace still waits after it.
	if len(*waits) != 2 {
		t.Errorf("waits %v, want 2", *waits)
	}
}

func TestDuctingSourceMostBatchesFailed(t *testing.T) {
	// Two of three fail: the fetch fails, and stops at the second failure
	// (more than half) without sending the third.
	f := &fakeOpenMeteo{t: t, failReq: func(n int) int {
		if n < 2 {
			return http.StatusBadGateway
		}
		return 0
	}}
	src, srv, _ := newTestDuctingSource(t, f, "gfs")
	_, err := src.fetch(context.Background(), ductingTestCells(150), 2)
	var he *ductingHTTPError
	if !errors.As(err, &he) || he.Status != http.StatusBadGateway || !strings.Contains(err.Error(), "2 of 3 batches failed") {
		t.Fatalf("err %v, want 2 of 3 failed wrapping HTTP 502", err)
	}
	ductingAssertNoURL(t, err, srv.URL)
	if n := len(f.requests()); n != 2 {
		t.Errorf("%d requests, want 2 (stop once more than half failed)", n)
	}

	// Exactly half (1 of 2) is still a success.
	f2 := &fakeOpenMeteo{t: t, failReq: func(n int) int {
		if n == 0 {
			return http.StatusInternalServerError
		}
		return 0
	}}
	src2, _, _ := newTestDuctingSource(t, f2, "gfs")
	res, err := src2.fetch(context.Background(), ductingTestCells(100), 2)
	if err != nil || res.Failed != 1 || len(res.Cols) != 50 {
		t.Errorf("half failed: err %v, %d failed, %d columns", err, res.Failed, len(res.Cols))
	}
}

func TestDuctingSourceRateLimitAbortsFetch(t *testing.T) {
	f := &fakeOpenMeteo{t: t, failReq: func(n int) int {
		if n == 1 {
			return http.StatusTooManyRequests
		}
		return 0
	}}
	src, _, _ := newTestDuctingSource(t, f, "gfs")
	_, err := src.fetch(context.Background(), ductingTestCells(250), 2)
	if !errors.Is(err, errDuctingRateLimited) {
		t.Fatalf("err %v, want errDuctingRateLimited", err)
	}
	if n := len(f.requests()); n != 2 {
		t.Errorf("%d requests, want 2 (nothing after the 429)", n)
	}
}

func TestDuctingSourceHugeTimeAxisSkipped(t *testing.T) {
	const huge = 1000000
	f := &fakeOpenMeteo{t: t, hours: 3}
	f.fill = func(i int, lat, lon float64, h map[string][]*float64) {
		if i != 1 {
			return
		}
		ts := make([]*float64, huge)
		for k := range ts {
			v := float64(1795046400 + 3600*k)
			ts[k] = &v
		}
		h["time"] = ts
	}
	src, _, _ := newTestDuctingSource(t, f, "gfs")
	src.maxBody = 64 << 20
	cells := ductingTestCells(3)
	cols, err := ductingFetchCols(context.Background(), src, cells, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 2 || cols[0].Lat != cells[0][0] || cols[1].Lat != cells[2][0] {
		t.Fatalf("%d columns, want the two well-formed locations (0 and 2)", len(cols))
	}
	for _, c := range cols {
		if len(c.Hours) != 3 {
			t.Errorf("column has %d hours", len(c.Hours))
		}
	}
}

func TestDuctingSourceNoRedirects(t *testing.T) {
	var hits int
	var mu sync.Mutex
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.WriteHeader(200)
	}))
	defer target.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/v1/forecast", http.StatusFound)
	}))
	defer redir.Close()
	m, _ := ductingModel("gfs")
	src := newDuctingSource(redir.URL, m)
	_, err := src.fetch(context.Background(), ductingTestCells(1), 3)
	var he *ductingHTTPError
	if !errors.As(err, &he) || he.Status != http.StatusFound {
		t.Fatalf("err %v, want HTTP 302 error", err)
	}
	ductingAssertNoURL(t, err, target.URL)
	ductingAssertNoURL(t, err, redir.URL)
	mu.Lock()
	defer mu.Unlock()
	if hits != 0 {
		t.Errorf("redirect target was contacted %d times", hits)
	}
}
