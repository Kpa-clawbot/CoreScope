package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ductingInversion fills every hour with a surface-based duct: humid 2 m
// air under warm, dry air at 100 m (1000 hPa); every other level null.
func ductingInversion(i int, lat, lon float64, h map[string][]*float64) {
	for k := range h["time"] {
		h["surface_pressure"][k] = ductF(1013)
		h["temperature_2m"][k] = ductF(20)
		h["relative_humidity_2m"][k] = ductF(95)
		h["temperature_1000hPa"][k] = ductF(25)
		h["relative_humidity_1000hPa"][k] = ductF(20)
		h["geopotential_height_1000hPa"][k] = ductF(100)
	}
}

var ductingNoMargin = 0.0

func newTestDuctingService(t *testing.T, f *fakeOpenMeteo, cfg DuctingConfig, nodes [][2]float64) (*ductingService, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cfg.Enabled = true
	cfg.BaseURL = srv.URL
	if cfg.MarginDeg == nil { // these tests count node cells; the margin has its own test
		cfg.MarginDeg = &ductingNoMargin
	}
	svc := newDuctingService(&cfg, func() ([][2]float64, error) { return nodes, nil })
	svc.source.sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }
	// The fake's first hour; a real clock would see its forecast as expired.
	svc.now = func() time.Time { return time.Unix(1795046400, 0) }
	svc.source.now = func() time.Time { return time.Unix(1795046400, 0) }
	return svc, srv
}

func ductingServe(t *testing.T, svc *ductingService) (DuctingResponse, []byte) {
	t.Helper()
	rec := httptest.NewRecorder()
	svc.serveHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/ducting", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type %q", ct)
	}
	var resp DuctingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return resp, rec.Body.Bytes()
}

func ductingKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestDuctingServiceSnapshotMatchesFixture(t *testing.T) {
	f := &fakeOpenMeteo{t: t, fill: ductingInversion}
	svc, _ := newTestDuctingService(t, f, DuctingConfig{RefreshMinutes: 60, ForecastHours: 3}, [][2]float64{{51.3, -0.3}, {51.3, 0.3}})
	gen := time.Unix(1795046000, 0)
	svc.now = func() time.Time { return gen }
	if err := svc.refreshOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	resp, body := ductingServe(t, svc)

	raw, err := os.ReadFile("../../tests/fixtures/ducting-sample.json")
	if err != nil {
		t.Fatal(err)
	}
	var want, got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if w, g := strings.Join(ductingKeys(want), ","), strings.Join(ductingKeys(got), ","); w != g {
		t.Errorf("top-level keys %s, want %s", g, w)
	}
	var wantCells, gotCells []map[string]json.RawMessage
	json.Unmarshal(want["cells"], &wantCells)
	json.Unmarshal(got["cells"], &gotCells)
	if len(gotCells) != 2 {
		t.Fatalf("%d cells, want 2", len(gotCells))
	}
	if w, g := strings.Join(ductingKeys(wantCells[0]), ","), strings.Join(ductingKeys(gotCells[0]), ","); w != g {
		t.Errorf("cell keys %s, want %s", g, w)
	}
	for k, v := range gotCells[0] {
		if k != "lat" && k != "lon" && (len(v) == 0 || v[0] != '[') {
			t.Errorf("cell %q is %s, want a JSON array", k, v)
		}
	}
	if string(gotCells[0]["c"]) != "[4,4,4]" {
		t.Errorf("classes %s, want [4,4,4]", gotCells[0]["c"])
	}

	if resp.Status != "ok" || resp.Model != "gfs" || resp.ModelLabel != "NOAA GFS" || resp.StepDeg != 0.5 {
		t.Errorf("header %+v", resp)
	}
	cc := (&DuctingConfig{Enabled: true}).clientConfig()
	if resp.Attribution != cc.Attribution {
		t.Errorf("attribution %q, want %q", resp.Attribution, cc.Attribution)
	}
	if resp.GeneratedAt != gen.Unix() {
		t.Errorf("generatedAt %d", resp.GeneratedAt)
	}
	if len(resp.Times) != 3 || resp.Times[0] != 1795046400 {
		t.Errorf("times %v", resp.Times)
	}
	if c := resp.Cells[0]; c.Lat != 51.25 || c.Lon != -0.25 || len(c.C) != 3 || len(c.G) != 3 || len(c.N) != 3 {
		t.Errorf("cell %+v", c)
	}
}

func TestDuctingServiceSurfaceDuct(t *testing.T) {
	f := &fakeOpenMeteo{t: t, hours: 1, fill: ductingInversion}
	svc, _ := newTestDuctingService(t, f, DuctingConfig{}, [][2]float64{{51.3, -0.3}})
	if err := svc.refreshOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	resp, _ := ductingServe(t, svc)
	c := resp.Cells[0]

	// Independently: ITU-R P.453 refractivity of both points, M = N + 0.157 h.
	n := func(p, tc, rh float64) float64 {
		tk := tc + 273.15
		e := rh / 100 * 6.1121 * math.Exp(17.502*tc/(240.97+tc))
		return 77.6 / tk * (p + 4810*e/tk)
	}
	n0, n1 := n(1013, 20, 95), n(1000, 25, 20)
	m0, m1 := n0+0.157*2, n1+0.157*100
	dm := m0 - m1 // trapping from the surface point up to 100 m
	if dm <= 0 {
		t.Fatalf("test profile has no trapping layer (dM=%v)", dm)
	}
	fmin := 120000 / (100 * math.Sqrt(dm))
	grad := (n1 - n0) / (98.0 / 1000)

	if c.C[0] != int(DuctClassSurface) {
		t.Errorf("class %d, want %d", c.C[0], DuctClassSurface)
	}
	if c.F[0] != int(math.Round(fmin)) {
		t.Errorf("fmin %d, want %d (%.2f)", c.F[0], int(math.Round(fmin)), fmin)
	}
	if c.B[0] != 0 || c.T[0] != 100 {
		t.Errorf("base/top %d/%d, want 0/100", c.B[0], c.T[0])
	}
	if c.DM[0] != int(math.Round(dm*10)) {
		t.Errorf("dm %d, want %d", c.DM[0], int(math.Round(dm*10)))
	}
	if c.G[0] != int(math.Round(grad)) {
		t.Errorf("g %d, want %d", c.G[0], int(math.Round(grad)))
	}
	// The fake profile tops out at 100 m, below 1.5 km: no mean gradient.
	if len(c.N) != len(c.G) || c.N[0] != 0 {
		t.Errorf("n %v, want %d zeros", c.N, len(c.G))
	}
}

func TestDuctingServicePendingErrorOK(t *testing.T) {
	f := &fakeOpenMeteo{t: t, hours: 2, fill: ductingInversion, status: 500, body: "down"}
	svc, srv := newTestDuctingService(t, f, DuctingConfig{}, [][2]float64{{51.3, -0.3}})
	if resp, _ := ductingServe(t, svc); resp.Status != "pending" || resp.GeneratedAt != 0 || resp.Cells == nil || resp.Times == nil {
		t.Fatalf("initial %+v", resp)
	}
	if err := svc.refreshOnce(context.Background()); err == nil {
		t.Fatal("refresh against a 500 succeeded")
	}
	resp, body := ductingServe(t, svc)
	if resp.Status != "error" || resp.Error != "Open-Meteo HTTP 500" {
		t.Errorf("after failure %+v", resp)
	}
	if strings.Contains(string(body), strings.TrimPrefix(srv.URL, "http://")) {
		t.Error("error response leaks the base URL")
	}

	f.mu.Lock()
	f.status = 0
	f.mu.Unlock()
	if err := svc.refreshOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	good, _ := ductingServe(t, svc)
	if good.Status != "ok" || len(good.Cells) != 1 {
		t.Fatalf("after success %+v", good)
	}

	// A later failure keeps the last good snapshot.
	f.mu.Lock()
	f.status = 429
	f.mu.Unlock()
	if err := svc.refreshOnce(context.Background()); !errors.Is(err, errDuctingRateLimited) {
		t.Fatalf("err %v", err)
	}
	kept, _ := ductingServe(t, svc)
	if kept.Status != "ok" || kept.GeneratedAt != good.GeneratedAt || len(kept.Cells) != 1 {
		t.Errorf("after a failure following success %+v", kept)
	}
}

func TestDuctingServiceNoCellsAndNoData(t *testing.T) {
	f := &fakeOpenMeteo{t: t, hours: 1}
	svc, _ := newTestDuctingService(t, f, DuctingConfig{}, nil)
	if err := svc.refreshOnce(context.Background()); !errors.Is(err, errDuctingNoCells) {
		t.Fatalf("err %v, want errDuctingNoCells", err)
	}
	if resp, _ := ductingServe(t, svc); resp.Status != "pending" || len(resp.Cells) != 0 || len(resp.Times) != 0 {
		t.Errorf("no nodes: %+v", resp)
	}
	if len(f.requests()) != 0 {
		t.Error("API called with no cells")
	}

	// Every location answered with an error: a failure, not an empty ok.
	f2 := &fakeOpenMeteo{t: t, hours: 1, fill: func(i int, lat, lon float64, h map[string][]*float64) {
		h["error"] = []*float64{}
	}}
	svc2, _ := newTestDuctingService(t, f2, DuctingConfig{}, [][2]float64{{51.3, -0.3}})
	if err := svc2.refreshOnce(context.Background()); err == nil {
		t.Error("all-dropped refresh succeeded")
	}
	if resp, _ := ductingServe(t, svc2); resp.Status != "error" {
		t.Errorf("all dropped: %+v", resp)
	}

	// Node source failure.
	svc3, _ := newTestDuctingService(t, f, DuctingConfig{}, nil)
	svc3.nodes = func() ([][2]float64, error) { return nil, errors.New("db gone /secret/path") }
	if err := svc3.refreshOnce(context.Background()); err == nil {
		t.Error("node failure ignored")
	}
	if resp, _ := ductingServe(t, svc3); resp.Status != "error" || strings.Contains(resp.Error, "secret") {
		t.Errorf("node failure: %+v", resp)
	}
}

func TestDuctingServiceAreaAndCoverage(t *testing.T) {
	f := &fakeOpenMeteo{t: t, hours: 1}
	area := [4]float64{51, -1, 52, 0}
	svc, _ := newTestDuctingService(t, f, DuctingConfig{Area: &area}, nil)
	svc.nodes = func() ([][2]float64, error) {
		t.Error("nodes read although an area is configured")
		return nil, nil
	}
	if err := svc.refreshOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if resp, _ := ductingServe(t, svc); len(resp.Cells) != 4 {
		t.Errorf("%d area cells, want 4", len(resp.Cells))
	}

	// A regional model is never asked for cells outside its bounds, even
	// though the box between the nodes reaches far past them.
	f2 := &fakeOpenMeteo{t: t, hours: 1}
	svc2, _ := newTestDuctingService(t, f2, DuctingConfig{Model: "rrfs-conus"}, [][2]float64{{40.3, -100.3}, {51.3, -0.3}})
	if err := svc2.refreshOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := f2.requests()
	if len(reqs) == 0 {
		t.Fatal("no regional request")
	}
	b := ductingModels["rrfs-conus"].Bounds
	var asked int
	for _, r := range reqs {
		q := r.URL.Query()
		if q.Get("models") != "ncep_rrfs_conus" {
			t.Errorf("models %q", q.Get("models"))
		}
		lats, lons := strings.Split(q.Get("latitude"), ","), strings.Split(q.Get("longitude"), ",")
		for i := range lats {
			lat, _ := strconv.ParseFloat(lats[i], 64)
			lon, _ := strconv.ParseFloat(lons[i], 64)
			if lat < b[0] || lat > b[2] || lon < b[1] || lon > b[3] {
				t.Errorf("cell %v,%v outside the model bounds", lat, lon)
			}
			asked++
		}
	}
	if asked < 10 {
		t.Errorf("only %d cells asked: the box between the nodes should be filled", asked)
	}
}

func TestDuctingServiceForecastExpired(t *testing.T) {
	f := &fakeOpenMeteo{t: t, fill: ductingInversion}
	svc, _ := newTestDuctingService(t, f, DuctingConfig{RefreshMinutes: 60, ForecastHours: 3}, [][2]float64{{51.3, -0.3}})
	now := time.Unix(1795046400, 0)
	svc.now = func() time.Time { return now }
	if err := svc.refreshOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if resp, _ := ductingServe(t, svc); resp.Status != "ok" {
		t.Fatalf("fresh %+v", resp)
	}
	// The last hour (t0+7200) stays current for the whole hour it covers.
	now = time.Unix(1795046400+7200, 0)
	if resp, _ := ductingServe(t, svc); resp.Status != "ok" {
		t.Errorf("at the last hour %+v", resp)
	}
	now = time.Unix(1795046400+10800, 0) // last + 3600: still current
	if resp, _ := ductingServe(t, svc); resp.Status != "ok" {
		t.Errorf("an hour after the last hour %+v", resp)
	}
	now = time.Unix(1795046400+10801, 0)
	resp, _ := ductingServe(t, svc)
	if resp.Status != "error" || resp.Error != "forecast expired" || resp.GeneratedAt != 1795046400 || len(resp.Cells) != 0 {
		t.Errorf("expired %+v", resp)
	}
}

func TestDuctingServiceNextDelay(t *testing.T) {
	svc := newDuctingService(&DuctingConfig{Enabled: true}, nil) // refresh 360 min
	boom := errors.New("boom")
	want := []time.Duration{5, 10, 20, 40, 80, 160, 320, 360, 360}
	for i, w := range want {
		if d := svc.nextDelay(boom, i+1); d != w*time.Minute {
			t.Errorf("failure %d: %v, want %v", i+1, d, w*time.Minute)
		}
	}
	if d := svc.nextDelay(boom, 1000); d != 360*time.Minute {
		t.Errorf("many failures: %v", d)
	}
	if d := svc.nextDelay(nil, 0); d != 360*time.Minute {
		t.Errorf("success: %v", d)
	}
	if d := svc.nextDelay(errDuctingRateLimited, 1); d != 60*time.Minute {
		t.Errorf("rate limited: %v", d)
	}
	if d := svc.nextDelay(errDuctingBudget, 8); d != 360*time.Minute {
		t.Errorf("budget after many failures: %v", d)
	}
	hourly := newDuctingService(&DuctingConfig{Enabled: true, RefreshMinutes: 60}, nil)
	if d := hourly.nextDelay(errDuctingRateLimited, 5); d != 60*time.Minute {
		t.Errorf("hourly rate limited: %v", d)
	}
}

func TestDuctingServiceRunBackoff(t *testing.T) {
	statuses := []int{500, 500, 429, 0, 500}
	var calls atomic.Int32
	f := &fakeOpenMeteo{t: t, hours: 1, fill: ductingInversion}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := int(calls.Add(1)) - 1
		if i < len(statuses) && statuses[i] != 0 {
			w.WriteHeader(statuses[i])
			return
		}
		f.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	svc := newDuctingService(&DuctingConfig{Enabled: true, BaseURL: srv.URL, MarginDeg: &ductingNoMargin}, func() ([][2]float64, error) {
		return [][2]float64{{51.3, -0.3}}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var waits []time.Duration
	var statusAtWait []string
	svc.sleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		statusAtWait = append(statusAtWait, svc.snap.Load().resp.Status)
		if len(waits) == len(statuses) {
			cancel()
		}
		return ctx.Err()
	}
	select {
	case <-svc.run(ctx):
	case <-time.After(10 * time.Second):
		t.Fatal("run did not exit")
	}
	want := []time.Duration{5 * time.Minute, 10 * time.Minute, 60 * time.Minute, 360 * time.Minute, 5 * time.Minute}
	if len(waits) != len(want) {
		t.Fatalf("waits %v, want %v", waits, want)
	}
	for i := range want {
		if waits[i] != want[i] {
			t.Errorf("wait %d: %v, want %v", i, waits[i], want[i])
		}
	}
	if got := strings.Join(statusAtWait, ","); got != "error,error,error,ok,ok" {
		t.Errorf("statuses %s", got)
	}
}

func TestDuctingServiceRunStopsOnCancel(t *testing.T) {
	// Idle between refreshes.
	f := &fakeOpenMeteo{t: t, hours: 1, fill: ductingInversion}
	svc, _ := newTestDuctingService(t, f, DuctingConfig{}, [][2]float64{{51.3, -0.3}})
	ctx, cancel := context.WithCancel(context.Background())
	done := svc.run(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for svc.snap.Load().resp.Status != "ok" {
		if time.Now().After(deadline) {
			t.Fatal("first refresh never finished")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not exit after cancel")
	}

	// Mid-request.
	started := make(chan struct{}, 1)
	block := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	t.Cleanup(block.Close)
	svc2 := newDuctingService(&DuctingConfig{Enabled: true, BaseURL: block.URL, MarginDeg: &ductingNoMargin}, func() ([][2]float64, error) {
		return [][2]float64{{51.3, -0.3}}, nil
	})
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := svc2.run(ctx2)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("no request")
	}
	cancel2()
	select {
	case <-done2:
	case <-time.After(2 * time.Second):
		t.Fatal("run did not exit while a request was in flight")
	}
	if st := svc2.snap.Load().resp.Status; st != "pending" {
		t.Errorf("status after shutdown %q, want pending", st)
	}
}

func TestDuctingServiceOneRefreshAtATime(t *testing.T) {
	f := &fakeOpenMeteo{t: t, hours: 1}
	svc, _ := newTestDuctingService(t, f, DuctingConfig{}, [][2]float64{{51.3, -0.3}})
	svc.refreshMu.Lock()
	err := svc.refreshOnce(context.Background())
	svc.refreshMu.Unlock()
	if !errors.Is(err, errDuctingBusy) {
		t.Errorf("err %v, want errDuctingBusy", err)
	}
	if len(f.requests()) != 0 {
		t.Error("a concurrent refresh reached the API")
	}
}

func TestDuctingServiceConcurrentReads(t *testing.T) {
	f := &fakeOpenMeteo{t: t, hours: 2, fill: ductingInversion}
	svc, _ := newTestDuctingService(t, f, DuctingConfig{}, [][2]float64{{51.3, -0.3}, {51.9, 0.1}})
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				rec := httptest.NewRecorder()
				svc.serveHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/ducting", nil))
				var resp DuctingResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
					t.Errorf("torn response: %v", err)
					return
				}
				if resp.Status == "ok" && len(resp.Cells) != 4 {
					t.Errorf("ok snapshot with %d cells", len(resp.Cells))
					return
				}
			}
		}()
	}
	for i := 0; i < 20; i++ {
		if err := svc.refreshOnce(context.Background()); err != nil {
			t.Error(err)
		}
	}
	close(stop)
	wg.Wait()
}

func TestDuctingNodesExcludeHidden(t *testing.T) {
	db := setupTestDB(t)
	visible := "aa11" + strings.Repeat("0", 60)
	black := "bb22" + strings.Repeat("0", 60)
	hidden := "cc33" + strings.Repeat("0", 60)
	seen := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	for _, q := range []string{
		`INSERT INTO nodes (public_key, name, lat, lon, last_seen) VALUES ('` + visible + `', 'Repeater A', 51.3, -0.3, ?)`,
		`INSERT INTO nodes (public_key, name, lat, lon, last_seen) VALUES ('` + black + `', 'Blacklisted', 40.3, -70.3, ?)`,
		`INSERT INTO nodes (public_key, name, lat, lon, last_seen) VALUES ('` + hidden + `', 'secret-base', 30.3, 60.3, ?)`,
		`INSERT INTO nodes (public_key, name, lat, lon, last_seen) VALUES ('dd44', NULL, 0, 0, ?)`,
		`INSERT INTO nodes (public_key, name, lat, lon, last_seen) VALUES ('ee55', 'No fix', NULL, NULL, ?)`,
		`INSERT INTO nodes (public_key, name, lat, lon, last_seen) VALUES ('ff66', NULL, -33.9, 18.4, ?)`,
	} {
		if _, err := db.conn.Exec(q, seen); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &Config{NodeBlacklist: []string{black}, HiddenNamePrefixes: []string{"secret"}}
	s := NewServer(db, cfg, NewHub())
	coords, err := s.ductingNodes()
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(coords, func(i, j int) bool { return coords[i][0] < coords[j][0] })
	if len(coords) != 2 || coords[0] != [2]float64{-33.9, 18.4} || coords[1] != [2]float64{51.3, -0.3} {
		t.Fatalf("coords %v, want only the visible nodes", coords)
	}

	// End to end: the hidden nodes never stretch the forecast box. The two
	// visible nodes span lat -34..51, lon -0.3..18 (4-degree cells after two
	// doublings: 22 x 6 cells, lat -36..52, lon -4..20); the
	// blacklisted (lon -70) and hidden (lon 60) nodes would reach far past.
	f := &fakeOpenMeteo{t: t, hours: 1}
	svc, _ := newTestDuctingService(t, f, DuctingConfig{}, nil)
	svc.nodes = s.ductingNodes
	if err := svc.refreshOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	var asked int
	for _, r := range f.requests() {
		q := r.URL.Query()
		lats, lons := strings.Split(q.Get("latitude"), ","), strings.Split(q.Get("longitude"), ",")
		for i := range lats {
			lat, _ := strconv.ParseFloat(lats[i], 64)
			lon, _ := strconv.ParseFloat(lons[i], 64)
			if lat < -36 || lat > 52 || lon < -4 || lon > 20 {
				t.Errorf("requested %v,%v: a hidden node stretched the box", lat, lon)
			}
			asked++
		}
	}
	if asked != 22*6 {
		t.Errorf("%d cells requested, want 132", asked)
	}
}

func TestDuctingRoute(t *testing.T) {
	s, router := setupTestServer(t)
	get := func() *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/ducting", nil))
		return rec
	}
	if rec := get(); rec.Code != http.StatusNotFound {
		t.Errorf("disabled: %d, want 404", rec.Code)
	}
	s.cfg.Ducting = &DuctingConfig{Enabled: true}
	if rec := get(); rec.Code != http.StatusNotFound {
		t.Errorf("enabled without a service: %d, want 404", rec.Code)
	}
	s.ducting = newDuctingService(s.cfg.Ducting, nil)
	rec := get()
	if rec.Code != http.StatusOK {
		t.Fatalf("enabled: %d", rec.Code)
	}
	var resp DuctingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil || resp.Status != "pending" {
		t.Errorf("enabled body %q (%v)", rec.Body.String(), err)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control %q", cc)
	}
	s.cfg.Ducting.Enabled = false
	if rec := get(); rec.Code != http.StatusNotFound {
		t.Errorf("disabled again: %d, want 404", rec.Code)
	}
}

func TestDuctingServiceDefaultMarginCoversRegion(t *testing.T) {
	f := &fakeOpenMeteo{t: t, hours: 2, fill: ductingInversion}
	one := [][2]float64{{51.3, -0.3}}
	svc, _ := newTestDuctingService(t, f, DuctingConfig{MarginDeg: nil}, one)
	svc.cfg.MarginDeg = nil // unresolved: falls back to the 5 degree default
	if err := svc.refreshOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	resp, _ := ductingServe(t, svc)
	// 10.6 degrees across is 21x21 = 441 cells at 0.5, within the default 450
	// maxPoints: a single-region mesh keeps the 0.5 degree grid.
	if got := len(resp.Cells); got != 441 || resp.StepDeg != 0.5 {
		t.Fatalf("%d cells at step %v, want 441 at 0.5", got, resp.StepDeg)
	}
}

func TestDuctingServiceBoundingBoxFilled(t *testing.T) {
	f := &fakeOpenMeteo{t: t, hours: 1, fill: ductingInversion}
	// Two distant nodes: the whole box between them is forecast, no gaps.
	nodes := [][2]float64{{50.1, 5.1}, {53.1, 9.1}}
	svc, _ := newTestDuctingService(t, f, DuctingConfig{GridStepDeg: 1}, nodes)
	if err := svc.refreshOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	resp, _ := ductingServe(t, svc)
	if resp.StepDeg != 1 || len(resp.Cells) != 4*5 { // lat 50..53, lon 5..9
		t.Fatalf("step %v, %d cells, want 20 at step 1", resp.StepDeg, len(resp.Cells))
	}
}

func TestDuctingServiceNodeAreaFallbackLogged(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	f := &fakeOpenMeteo{t: t, hours: 1, fill: ductingInversion}
	// Worldwide spread: no 8 degree box fits in 450 points (the smallest
	// longitude span is about 270 degrees: 17 x 34 cells at 8 degrees).
	nodes := [][2]float64{{60.1, 100.1}, {-60.1, -100.1}, {0.1, 0.1}, {40.1, -100.1}, {10.1, 170.1}, {-10.1, -170.1}}
	svc, _ := newTestDuctingService(t, f, DuctingConfig{}, nodes)
	if err := svc.refreshOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(buf.String(), "node area too large for maxPoints even at 8 deg; covering the regions around nodes instead"); n != 1 {
		t.Errorf("fallback logged %d times: %s", n, buf.String())
	}
	if resp, _ := ductingServe(t, svc); len(resp.Cells) == 0 || len(resp.Cells) > 450 {
		t.Errorf("%d cells", len(resp.Cells))
	}
}

func TestDuctingAnalyzeColumnMeanGradient(t *testing.T) {
	m, _ := ductingModel("gfs")
	svc := &ductingService{model: m}
	// Levels at 1000 and 850 hPa; geo heights put them at 2 < h and 1500 m AGL.
	levels := m.LevelsHPa
	hr := ductingHour{SurfP: 1013, T2m: 15, RH2m: 50}
	for range levels {
		hr.Temp, hr.RH, hr.Geo = append(hr.Temp, math.NaN()), append(hr.RH, math.NaN()), append(hr.Geo, math.NaN())
	}
	for i, p := range levels {
		switch p {
		case 850:
			hr.Temp[i], hr.RH[i], hr.Geo[i] = 5, 60, 1600 // 100 m terrain
		case 700:
			hr.Temp[i], hr.RH[i], hr.Geo[i] = -5, 50, 3100
		}
	}
	col := ductingColumn{Elevation: 100, Hours: []ductingHour{hr, hr}}
	cell := svc.analyzeColumn(col)
	if len(cell.N) != 2 {
		t.Fatalf("len(n) = %d, want 2", len(cell.N))
	}
	n0 := Refractivity(1013, 15, 50)
	n850 := Refractivity(850, 5, 60) // at 1500 m AGL exactly
	if want := int(math.Round((n850 - n0) / 1.5)); cell.N[0] != want || cell.N[1] != want || want == 0 {
		t.Errorf("n %v, want %d", cell.N, want)
	}
}

func TestDuctingNodesRecentOnly(t *testing.T) {
	db := setupTestDB(t)
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	for _, n := range []struct {
		pk, lastSeen string
		lat          float64
	}{
		{"a1", now.Add(-time.Hour).Format(time.RFC3339), 10.1},                         // fresh
		{"a2", now.Add(-13 * 24 * time.Hour).Format("2006-01-02T15:04:05.000Z"), 20.1}, // 13 days, ms form
		{"a3", now.Add(-24 * time.Hour).Format("2006-01-02 15:04:05"), 30.1},           // SQLite datetime() form
		{"b1", now.Add(-30 * 24 * time.Hour).Format(time.RFC3339), 40.1},               // 30 days: stale
		{"b2", now.Add(-15 * 24 * time.Hour).Format(time.RFC3339Nano), 50.1},           // just past 14 days
		{"b3", "yesterday", 60.1}, // unparseable
		{"b4", "", 70.1},          // empty
	} {
		if _, err := db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon, last_seen) VALUES (?, 'n', ?, 5.1, ?)`, n.pk, n.lat, n.lastSeen); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.conn.Exec(`INSERT INTO nodes (public_key, name, lat, lon, last_seen) VALUES ('b5', 'n', 80.1, 5.1, NULL)`); err != nil {
		t.Fatal(err)
	}
	coords, err := db.ductingNodeCoords(nil, now)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(coords, func(i, j int) bool { return coords[i][0] < coords[j][0] })
	want := [][2]float64{{10.1, 5.1}, {20.1, 5.1}, {30.1, 5.1}}
	if len(coords) != len(want) {
		t.Fatalf("coords %v, want %v", coords, want)
	}
	for i := range want {
		if coords[i] != want[i] {
			t.Fatalf("coords %v, want %v", coords, want)
		}
	}
}

func TestDuctingServiceEmptyGridPending(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	f := &fakeOpenMeteo{t: t, hours: 1, fill: ductingInversion}
	var nodes atomic.Pointer[[][2]float64]
	empty := [][2]float64{}
	nodes.Store(&empty)
	svc, _ := newTestDuctingService(t, f, DuctingConfig{}, nil)
	svc.nodes = func() ([][2]float64, error) { return *nodes.Load(), nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var waits []time.Duration
	var statusAtWait []string
	svc.sleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		statusAtWait = append(statusAtWait, svc.snap.Load().resp.Status)
		switch len(waits) {
		case 3: // the mesh's first node appears
			one := [][2]float64{{51.3, -0.3}}
			nodes.Store(&one)
		case 4:
			cancel()
		}
		return ctx.Err()
	}
	select {
	case <-svc.run(ctx):
	case <-time.After(10 * time.Second):
		t.Fatal("run did not exit")
	}
	want := []time.Duration{5 * time.Minute, 5 * time.Minute, 5 * time.Minute, 360 * time.Minute}
	if len(waits) != len(want) {
		t.Fatalf("waits %v, want %v", waits, want)
	}
	for i := range want {
		if waits[i] != want[i] {
			t.Errorf("wait %d: %v, want %v", i, waits[i], want[i])
		}
	}
	if got := strings.Join(statusAtWait, ","); got != "pending,pending,pending,ok" {
		t.Errorf("statuses %s", got)
	}
	if n := strings.Count(buf.String(), "[ducting] no grid cells yet"); n != 1 {
		t.Errorf("empty grid logged %d times: %s", n, buf.String())
	}
	if strings.Contains(buf.String(), "refresh failed") {
		t.Errorf("an empty grid was logged as a failure: %s", buf.String())
	}
	if len(f.requests()) != 1 {
		t.Errorf("%d API requests, want 1 (only once there was a cell)", len(f.requests()))
	}

	// An empty grid after a good refresh keeps the good snapshot.
	nodes.Store(&empty)
	if err := svc.refreshOnce(context.Background()); !errors.Is(err, errDuctingNoCells) {
		t.Fatalf("err %v", err)
	}
	if resp, _ := ductingServe(t, svc); resp.Status != "ok" || len(resp.Cells) != 1 {
		t.Errorf("after an empty refresh %+v", resp)
	}

	if d := svc.nextDelay(errDuctingNoCells, 0); d != 5*time.Minute {
		t.Errorf("nextDelay(no cells) %v", d)
	}
}

func TestDuctingServiceCacheControl(t *testing.T) {
	f := &fakeOpenMeteo{t: t, fill: ductingInversion, status: 500}
	svc, _ := newTestDuctingService(t, f, DuctingConfig{RefreshMinutes: 60, ForecastHours: 3}, [][2]float64{{51.3, -0.3}})
	cc := func() (string, string) {
		rec := httptest.NewRecorder()
		svc.serveHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/ducting", nil))
		var resp DuctingResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp.Status, rec.Header().Get("Cache-Control")
	}
	if st, h := cc(); st != "pending" || h != "no-store" {
		t.Errorf("pending: %s %q", st, h)
	}
	_ = svc.refreshOnce(context.Background())
	if st, h := cc(); st != "error" || h != "no-store" {
		t.Errorf("error: %s %q", st, h)
	}
	f.mu.Lock()
	f.status = 0
	f.mu.Unlock()
	if err := svc.refreshOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st, h := cc(); st != "ok" || h != "public, max-age=300" {
		t.Errorf("ok: %s %q", st, h)
	}
	svc.now = func() time.Time { return time.Unix(1795046400+10801, 0) }
	if st, h := cc(); st != "error" || h != "no-store" {
		t.Errorf("expired: %s %q", st, h)
	}
}

func TestDuctingRouteCacheControlOK(t *testing.T) {
	// Through the router: the handler's header wins over the /api/ no-store
	// middleware for an ok snapshot.
	s, router := setupTestServer(t)
	s.cfg.Ducting = &DuctingConfig{Enabled: true}
	s.ducting = newDuctingService(s.cfg.Ducting, nil)
	resp := s.ducting.baseResponse("ok", "")
	resp.Times = []int64{time.Now().Unix()}
	s.ducting.snap.Store(encodeDuctingSnapshot(resp))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/ducting", nil))
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=300" {
		t.Errorf("Cache-Control %q", cc)
	}
}

func TestDuctingServiceGridLogLine(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	f := &fakeOpenMeteo{t: t, hours: 1, fill: ductingInversion}
	// 25 nodes around Seattle and one in London: the London node is trimmed.
	var nodes [][2]float64
	for i := 0; i < 5; i++ {
		for j := 0; j < 5; j++ {
			nodes = append(nodes, [2]float64{47.3 + 0.1*float64(i), -122.6 + 0.1*float64(j)})
		}
	}
	nodes = append(nodes, [2]float64{51.5, -0.1})
	svc, _ := newTestDuctingService(t, f, DuctingConfig{}, nodes)
	if err := svc.refreshOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if n := strings.Count(out, "[ducting] grid: box "); n != 1 {
		t.Fatalf("grid logged %d times: %s", n, out)
	}
	if !strings.Contains(out, "[ducting] grid: box 47.30..47.70, -122.60..-122.20 (25 nodes, 1 trimmed), step 0.5 deg, 4 cells") {
		t.Errorf("log %q", out)
	}

	buf.Reset()
	area := [4]float64{51, -1, 52, 0}
	svc2, _ := newTestDuctingService(t, f, DuctingConfig{Area: &area}, nil)
	if err := svc2.refreshOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "[ducting] grid: area 51.00..52.00, -1.00..0.00, step 0.5 deg, 4 cells") {
		t.Errorf("area log %q", buf.String())
	}
}

// A refresh whose batches straddle an hour boundary keeps every cell: the
// window is fixed once per fetch.
func TestDuctingServiceHourRolloverKeepsEveryCell(t *testing.T) {
	f := &fakeOpenMeteo{t: t, fill: ductingInversion}
	area := [4]float64{50, -5, 55, 0} // 10 x 10 cells at 0.5 deg: two batches
	svc, _ := newTestDuctingService(t, f, DuctingConfig{Area: &area}, nil)
	base := time.Unix(1795046400+3600-60, 0)
	var mu sync.Mutex
	calls := 0
	svc.source.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return base.Add(time.Duration(calls) * 40 * time.Second)
	}
	if err := svc.refreshOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if svc.source.now().Unix() < 1795046400+3600 {
		t.Fatal("the clock never crossed the hour")
	}
	resp, _ := ductingServe(t, svc)
	if len(resp.Cells) != 100 {
		t.Fatalf("%d cells, want all 100", len(resp.Cells))
	}
	if len(resp.Times) != 48 || resp.Times[0] != 1795046400 {
		t.Errorf("times start %v, %d hours", resp.Times[:1], len(resp.Times))
	}
	for _, c := range resp.Cells {
		if len(c.C) != len(resp.Times) {
			t.Fatalf("cell has %d hours, want %d", len(c.C), len(resp.Times))
		}
	}
}

func TestDuctingServicePartialFailure(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	var failAll atomic.Bool
	f := &fakeOpenMeteo{t: t, fill: ductingInversion, failReq: func(n int) int {
		if n == 0 || failAll.Load() {
			return http.StatusInternalServerError
		}
		return 0
	}}
	area := [4]float64{50, -5, 55, 0} // 100 cells: two batches
	svc, _ := newTestDuctingService(t, f, DuctingConfig{Area: &area}, nil)
	if err := svc.refreshOnce(context.Background()); err != nil {
		t.Fatalf("one of two batches failed: %v", err)
	}
	resp, body := ductingServe(t, svc)
	if resp.Status != "ok" || len(resp.Cells) != 50 {
		t.Fatalf("status %s, %d cells; want ok with the 50 cells of the good batch", resp.Status, len(resp.Cells))
	}
	for _, c := range resp.Cells {
		if c.Lat < 52.5 { // the first batch (sorted by lat) failed
			t.Fatalf("cell %v,%v from the failed batch", c.Lat, c.Lon)
		}
	}
	out := buf.String()
	if !strings.Contains(out, "[ducting] batch 1 of 2 failed, its 50 cells have no data: open-meteo HTTP 500") ||
		!strings.Contains(out, "50 cells (50 dropped, 1 of 2 batches failed)") {
		t.Errorf("log %q", out)
	}

	// Every batch failing fails the refresh; the last good snapshot stays.
	failAll.Store(true)
	if err := svc.refreshOnce(context.Background()); err == nil {
		t.Fatal("refresh with every batch failed succeeded")
	}
	if _, body2 := ductingServe(t, svc); !bytes.Equal(body, body2) {
		t.Error("the last good snapshot was replaced")
	}
}

func TestDuctingServiceETag(t *testing.T) {
	f := &fakeOpenMeteo{t: t, fill: ductingInversion, status: 500}
	svc, _ := newTestDuctingService(t, f, DuctingConfig{}, [][2]float64{{51.3, -0.3}})
	get := func(inm string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/api/ducting", nil)
		if inm != "" {
			r.Header.Set("If-None-Match", inm)
		}
		svc.serveHTTP(rec, r)
		return rec
	}
	if rec := get(`"1795046400-gfs"`); rec.Code != 200 || rec.Header().Get("ETag") != "" {
		t.Errorf("pending: %d, ETag %q", rec.Code, rec.Header().Get("ETag"))
	}
	_ = svc.refreshOnce(context.Background())
	if rec := get("*"); rec.Code != 200 || rec.Header().Get("ETag") != "" {
		t.Errorf("error: %d, ETag %q", rec.Code, rec.Header().Get("ETag"))
	}
	f.mu.Lock()
	f.status = 0
	f.mu.Unlock()
	if err := svc.refreshOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	const etag = `"1795046400-gfs"`
	rec := get("")
	if rec.Code != 200 || rec.Header().Get("ETag") != etag || rec.Body.Len() == 0 {
		t.Fatalf("ok: %d, ETag %q, %d bytes", rec.Code, rec.Header().Get("ETag"), rec.Body.Len())
	}
	for _, inm := range []string{etag, "W/" + etag, `"other", ` + etag, "*"} {
		rec := get(inm)
		if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
			t.Errorf("If-None-Match %s: %d with %d bytes, want 304 and no body", inm, rec.Code, rec.Body.Len())
		}
		if rec.Header().Get("ETag") != etag || rec.Header().Get("Cache-Control") != "public, max-age=300" {
			t.Errorf("If-None-Match %s: headers %v", inm, rec.Header())
		}
	}
	for _, inm := range []string{`"1795046399-gfs"`, `"1795046400-icon"`, "1795046400-gfs"} {
		if rec := get(inm); rec.Code != 200 || rec.Body.Len() == 0 {
			t.Errorf("If-None-Match %s: %d, want 200 with the body", inm, rec.Code)
		}
	}
	// Expired: no ETag, and a matching If-None-Match does not get 304.
	svc.now = func() time.Time { return time.Unix(1795046400+48*3600+1, 0) }
	if rec := get(etag); rec.Code != 200 || rec.Header().Get("ETag") != "" {
		t.Errorf("expired: %d, ETag %q", rec.Code, rec.Header().Get("ETag"))
	}
}

func TestDuctingRouteETag(t *testing.T) {
	s, router := setupTestServer(t)
	s.cfg.Ducting = &DuctingConfig{Enabled: true}
	s.ducting = newDuctingService(s.cfg.Ducting, nil)
	resp := s.ducting.baseResponse("ok", "")
	resp.GeneratedAt = 1795046400
	resp.Times = []int64{time.Now().Unix()}
	s.ducting.snap.Store(encodeDuctingSnapshot(resp))
	req := httptest.NewRequest(http.MethodGet, "/api/ducting", nil)
	req.Header.Set("If-None-Match", `"1795046400-gfs"`)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 || rec.Header().Get("ETag") != `"1795046400-gfs"` {
		t.Errorf("through the router: %d, %d bytes, ETag %q", rec.Code, rec.Body.Len(), rec.Header().Get("ETag"))
	}
}

func TestDuctingServiceStartDelay(t *testing.T) {
	if d := ductingStartDelay(func(int64) int64 { return 0 }); d != 60*time.Second {
		t.Errorf("lowest draw %v, want 60s", d)
	}
	if d := ductingStartDelay(func(n int64) int64 { return n - 1 }); d != 300*time.Second {
		t.Errorf("highest draw %v, want 300s", d)
	}
	// The public API (the default baseUrl, or one naming its host) waits
	// 60..300 s; a self-hosted baseUrl starts at once.
	for _, base := range []string{"", "https://api.open-meteo.com", "https://API.open-meteo.com."} {
		for i := 0; i < 20; i++ {
			svc := newDuctingService(&DuctingConfig{Enabled: true, BaseURL: base}, nil)
			if svc.startDelay < 60*time.Second || svc.startDelay > 300*time.Second {
				t.Fatalf("baseUrl %q: start delay %v, want 60..300 s", base, svc.startDelay)
			}
		}
	}
	for _, base := range []string{"http://127.0.0.1:13597", "https://weather.example.org"} {
		if svc := newDuctingService(&DuctingConfig{Enabled: true, BaseURL: base}, nil); svc.startDelay != 0 {
			t.Errorf("self-hosted %q: start delay %v, want 0", base, svc.startDelay)
		}
	}
}

func TestDuctingServiceRunWaitsStartDelay(t *testing.T) {
	f := &fakeOpenMeteo{t: t, fill: ductingInversion}
	svc, _ := newTestDuctingService(t, f, DuctingConfig{}, [][2]float64{{51.3, -0.3}})
	if svc.startDelay != 0 {
		t.Fatalf("self-hosted test server: start delay %v", svc.startDelay)
	}
	svc.startDelay = 90 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var waits []time.Duration
	var reqsAtWait []int
	svc.sleep = func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		reqsAtWait = append(reqsAtWait, len(f.requests()))
		if len(waits) == 2 {
			cancel()
		}
		return ctx.Err()
	}
	select {
	case <-svc.run(ctx):
	case <-time.After(10 * time.Second):
		t.Fatal("run did not exit")
	}
	if len(waits) != 2 || waits[0] != 90*time.Second || waits[1] != 360*time.Minute {
		t.Fatalf("waits %v, want [1m30s 6h0m0s]", waits)
	}
	if reqsAtWait[0] != 0 || reqsAtWait[1] == 0 {
		t.Errorf("requests at each wait %v: none before the start delay, then the first fetch", reqsAtWait)
	}

	// Cancelled during the start delay: no fetch at all.
	f2 := &fakeOpenMeteo{t: t, fill: ductingInversion}
	svc2, _ := newTestDuctingService(t, f2, DuctingConfig{}, [][2]float64{{51.3, -0.3}})
	svc2.startDelay = time.Minute
	ctx2, cancel2 := context.WithCancel(context.Background())
	svc2.sleep = func(ctx context.Context, d time.Duration) error { cancel2(); return ctx.Err() }
	<-svc2.run(ctx2)
	if n := len(f2.requests()); n != 0 {
		t.Errorf("%d requests after cancel during the start delay", n)
	}
}
