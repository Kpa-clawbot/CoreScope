package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
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
	return svc, srv
}

func ductingServe(t *testing.T, svc *ductingService) (DuctingResponse, []byte) {
	t.Helper()
	rec := httptest.NewRecorder()
	svc.serveHTTP(rec)
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
	f := &fakeOpenMeteo{t: t, hours: 3, fill: ductingInversion}
	svc, _ := newTestDuctingService(t, f, DuctingConfig{}, [][2]float64{{51.3, -0.3}, {51.3, 0.3}})
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
	if c := resp.Cells[0]; c.Lat != 51.25 || c.Lon != -0.25 || len(c.C) != 3 || len(c.G) != 3 {
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
	if err := svc.refreshOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if resp, _ := ductingServe(t, svc); resp.Status != "ok" || len(resp.Cells) != 0 {
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

	// A regional model is never asked for cells outside its bounds.
	f2 := &fakeOpenMeteo{t: t, hours: 1}
	svc2, _ := newTestDuctingService(t, f2, DuctingConfig{Model: "rrfs-conus"}, [][2]float64{{40.3, -100.3}, {51.3, -0.3}})
	if err := svc2.refreshOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := f2.requests()
	if len(reqs) != 1 || reqs[0].URL.Query().Get("latitude") != "40.2500" || reqs[0].URL.Query().Get("models") != "ncep_rrfs_conus" {
		t.Errorf("regional request %v", reqs)
	}
}

func TestDuctingServiceForecastExpired(t *testing.T) {
	f := &fakeOpenMeteo{t: t, hours: 2, fill: ductingInversion}
	svc, _ := newTestDuctingService(t, f, DuctingConfig{}, [][2]float64{{51.3, -0.3}})
	now := time.Unix(1795046400, 0)
	svc.now = func() time.Time { return now }
	if err := svc.refreshOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if resp, _ := ductingServe(t, svc); resp.Status != "ok" {
		t.Fatalf("fresh %+v", resp)
	}
	now = time.Unix(1795046400+3600, 0) // the last hour itself is still current
	if resp, _ := ductingServe(t, svc); resp.Status != "ok" {
		t.Errorf("at the last hour %+v", resp)
	}
	now = time.Unix(1795046400+3601, 0)
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
	svc, _ := newTestDuctingService(t, f, DuctingConfig{}, [][2]float64{{51.3, -0.3}, {40.3, -70.3}})
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
				svc.serveHTTP(rec)
				var resp DuctingResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
					t.Errorf("torn response: %v", err)
					return
				}
				if resp.Status == "ok" && len(resp.Cells) != 2 {
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
	for _, q := range []string{
		`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('` + visible + `', 'Repeater A', 51.3, -0.3)`,
		`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('` + black + `', 'Blacklisted', 40.3, -70.3)`,
		`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('` + hidden + `', 'secret-base', 30.3, 10.3)`,
		`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('dd44', NULL, 0, 0)`,
		`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('ee55', 'No fix', NULL, NULL)`,
		`INSERT INTO nodes (public_key, name, lat, lon) VALUES ('ff66', NULL, -33.9, 18.4)`,
	} {
		if _, err := db.conn.Exec(q); err != nil {
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

	// End to end: the hidden nodes' cells are never requested.
	f := &fakeOpenMeteo{t: t, hours: 1}
	svc, _ := newTestDuctingService(t, f, DuctingConfig{}, nil)
	svc.nodes = s.ductingNodes
	if err := svc.refreshOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	reqs := f.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}
	if lats := reqs[0].URL.Query().Get("latitude"); lats != "-33.7500,51.2500" {
		t.Errorf("requested latitudes %q", lats)
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
	svc.cfg.MarginDeg = nil // unresolved: falls back to the 2 degree default
	if err := svc.refreshOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	resp, _ := ductingServe(t, svc)
	if got := len(resp.Cells); got != 81 { // 9x9 block at 0.5 degrees
		t.Fatalf("%d cells, want 81", got)
	}
}
