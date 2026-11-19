package main

import (
	"bytes"
	"log"
	"math"
	"os"
	"strings"
	"testing"
)

func hasWarn(warns []string, sub string) bool {
	for _, w := range warns {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

func TestDuctingEnabledNilSafe(t *testing.T) {
	var c *DuctingConfig
	if c.enabled() {
		t.Fatal("nil config must be disabled")
	}
	if (&DuctingConfig{}).enabled() {
		t.Fatal("zero config must be disabled")
	}
	if !(&DuctingConfig{Enabled: true}).enabled() {
		t.Fatal("enabled=true must be enabled")
	}
	if c.clientConfig() != nil {
		t.Fatal("nil config must have no client config")
	}
}

func TestDuctingDefaults(t *testing.T) {
	r, warns := (&DuctingConfig{Enabled: true}).resolved()
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if r.Model != "gfs" || r.GridStepDeg != 0.5 || r.MaxPoints != 450 || r.RefreshMinutes != 360 ||
		r.ForecastHours != 48 || r.BaseURL != "https://api.open-meteo.com" || r.Area != nil ||
		r.MarginDeg == nil || *r.MarginDeg != 5 {
		t.Fatalf("bad defaults: %+v", r)
	}
	if _, w := (*DuctingConfig)(nil).resolved(); len(w) != 0 {
		t.Fatalf("nil warnings: %v", w)
	}
}

func TestDuctingModelAllowlist(t *testing.T) {
	g, _ := ductingModel("gfs")
	if g.OpenMeteoID != "gfs_global" || !g.Global || len(g.LevelsHPa) != 13 || g.LevelsHPa[0] != 1000 || g.LevelsHPa[12] != 700 {
		t.Fatalf("gfs: %+v", g)
	}
	i, _ := ductingModel("icon")
	if i.OpenMeteoID != "icon_global" || len(i.LevelsHPa) != 8 {
		t.Fatalf("icon: %+v", i)
	}
	r, _ := ductingModel("rrfs-conus")
	if r.OpenMeteoID != "ncep_rrfs_conus" || r.Global || r.Bounds == nil || *r.Bounds != [4]float64{21, -134, 53, -60} ||
		len(r.LevelsHPa) != 13 || r.RegionalNote == "" {
		t.Fatalf("rrfs: %+v", r)
	}
	if _, ok := ductingModel("nope"); ok {
		t.Fatal("unknown model accepted")
	}
}

func TestDuctingOutOfRangeKeys(t *testing.T) {
	cases := []struct {
		key string
		cfg DuctingConfig
	}{
		{"gridStepDeg", DuctingConfig{GridStepDeg: 0.1}},
		{"gridStepDeg", DuctingConfig{GridStepDeg: 2.5}},
		{"gridStepDeg", DuctingConfig{GridStepDeg: -1}},
		{"gridStepDeg", DuctingConfig{GridStepDeg: math.NaN()}},
		{"maxPoints", DuctingConfig{MaxPoints: -1}},
		{"maxPoints", DuctingConfig{MaxPoints: 1001}},
		{"refreshMinutes", DuctingConfig{RefreshMinutes: 59}},
		{"refreshMinutes", DuctingConfig{RefreshMinutes: 1441}},
		{"forecastHours", DuctingConfig{ForecastHours: -2}},
		{"forecastHours", DuctingConfig{ForecastHours: 121}},
	}
	for _, tc := range cases {
		r, warns := tc.cfg.resolved()
		if !hasWarn(warns, "ducting."+tc.key) {
			t.Errorf("%s %+v: no warning, got %v", tc.key, tc.cfg, warns)
		}
		if r.GridStepDeg != 0.5 || r.MaxPoints != 450 || r.RefreshMinutes != 360 || r.ForecastHours != 48 {
			t.Errorf("%s: default not applied: %+v", tc.key, r)
		}
	}
}

func TestDuctingBoundaryValuesAccepted(t *testing.T) {
	r, warns := (&DuctingConfig{GridStepDeg: 0.25, MaxPoints: 1000, RefreshMinutes: 60, ForecastHours: 120}).resolved()
	if len(warns) != 0 || r.GridStepDeg != 0.25 || r.MaxPoints != 1000 || r.RefreshMinutes != 60 || r.ForecastHours != 120 {
		t.Fatalf("%+v %v", r, warns)
	}
	r, warns = (&DuctingConfig{GridStepDeg: 2, MaxPoints: 1, RefreshMinutes: 1440, ForecastHours: 25}).resolved()
	if len(warns) != 0 || r.GridStepDeg != 2 || r.MaxPoints != 1 || r.RefreshMinutes != 1440 || r.ForecastHours != 25 {
		t.Fatalf("%+v %v", r, warns)
	}
}

func TestDuctingBadModel(t *testing.T) {
	r, warns := (&DuctingConfig{Model: "ecmwf"}).resolved()
	if r.Model != "gfs" || !hasWarn(warns, "ducting.model") {
		t.Fatalf("%q %v", r.Model, warns)
	}
	r, warns = (&DuctingConfig{Model: "icon"}).resolved()
	if r.Model != "icon" || len(warns) != 0 {
		t.Fatalf("%q %v", r.Model, warns)
	}
}

func TestDuctingBaseURL(t *testing.T) {
	bad := []string{"ftp://api.example.com", "https://", "/relative", "api.example.com", "https://u:p@h", "https://user@h.example",
		"https://om.example.com?apikey=k", "https://om.example.com/?x=1", "https://om.example.com/#frag", "https://om.example.com/?"}
	for _, u := range bad {
		r, warns := (&DuctingConfig{BaseURL: u}).resolved()
		if r.BaseURL != defaultDuctingBaseURL {
			t.Errorf("%q: BaseURL %q, want default", u, r.BaseURL)
		}
		if !hasWarn(warns, "ducting.baseUrl") {
			t.Errorf("%q: no warning %v", u, warns)
		}
		for _, w := range warns {
			if strings.Contains(w, u) || strings.Contains(w, "u:p") {
				t.Errorf("%q: warning echoes the URL: %q", u, w)
			}
		}
	}
	r, warns := (&DuctingConfig{BaseURL: "https://om.example.com/proxy/"}).resolved()
	if r.BaseURL != "https://om.example.com/proxy" || len(warns) != 0 {
		t.Fatalf("%q %v", r.BaseURL, warns)
	}
	r, _ = (&DuctingConfig{BaseURL: "http://localhost:8080"}).resolved()
	if r.BaseURL != "http://localhost:8080" {
		t.Fatalf("%q", r.BaseURL)
	}
}

func TestDuctingAreaValidation(t *testing.T) {
	r, warns := (&DuctingConfig{Area: &[4]float64{40, -10, 50, 10}}).resolved()
	if r.Area == nil || *r.Area != [4]float64{40, -10, 50, 10} || len(warns) != 0 {
		t.Fatalf("%v %v", r.Area, warns)
	}
	bad := [][4]float64{
		{50, -10, 40, 10},     // south >= north
		{40, 10, 50, -10},     // west >= east
		{-91, 0, 10, 10},      // south out of range
		{0, 0, 91, 10},        // north out of range
		{0, -181, 10, 10},     // west out of range
		{0, 0, 10, 181},       // east out of range
		{40, 170, 50, -170},   // antimeridian wrap
		{math.NaN(), 0, 1, 1}, // NaN
		{10, 0, 10, 5},        // zero height
	}
	for _, a := range bad {
		a := a
		r, warns := (&DuctingConfig{Area: &a}).resolved()
		if r.Area != nil || !hasWarn(warns, "ducting.area") {
			t.Errorf("%v: area %v warns %v", a, r.Area, warns)
		}
	}
}

func TestDuctingRegionalModelWarnings(t *testing.T) {
	// nil area: warn, keep the model
	r, warns := (&DuctingConfig{Model: "rrfs-conus"}).resolved()
	if r.Model != "rrfs-conus" || !hasWarn(warns, "US only") {
		t.Fatalf("nil area: %q %v", r.Model, warns)
	}
	// area outside bounds
	r, warns = (&DuctingConfig{Model: "rrfs-conus", Area: &[4]float64{40, -10, 50, 10}}).resolved()
	if r.Model != "rrfs-conus" || !hasWarn(warns, "US only") {
		t.Fatalf("outside: %q %v", r.Model, warns)
	}
	// partially outside
	_, warns = (&DuctingConfig{Model: "rrfs-conus", Area: &[4]float64{30, -100, 60, -90}}).resolved()
	if !hasWarn(warns, "US only") {
		t.Fatalf("partial: %v", warns)
	}
	// inside: no warning
	_, warns = (&DuctingConfig{Model: "rrfs-conus", Area: &[4]float64{30, -100, 45, -80}}).resolved()
	if len(warns) != 0 {
		t.Fatalf("inside: %v", warns)
	}
	// global model never warns about coverage
	_, warns = (&DuctingConfig{Model: "gfs", Area: &[4]float64{40, -10, 50, 10}}).resolved()
	if len(warns) != 0 {
		t.Fatalf("gfs: %v", warns)
	}
}

func TestDuctingBudget(t *testing.T) {
	gfs, _ := ductingModel("gfs")
	if got := ductingCallsPerPoint(gfs); math.Abs(got-4.2) > 1e-9 {
		t.Fatalf("gfs calls/point = %v, want 4.2", got)
	}
	icon, _ := ductingModel("icon")
	if got := ductingCallsPerPoint(icon); math.Abs(got-2.7) > 1e-9 {
		t.Fatalf("icon calls/point = %v, want 2.7", got)
	}
	if got := ductingCallsPerPoint(DuctingModel{LevelsHPa: []float64{1000}}); got != 1 {
		t.Fatalf("small model calls/point = %v, want floor of 1", got)
	}
	if got := ductingMaxPoints(DuctingConfig{MaxPoints: 200, RefreshMinutes: 360}, gfs); got != 200 {
		t.Fatalf("refresh 360: %d, want 200", got)
	}
	if got := ductingMaxPoints(DuctingConfig{MaxPoints: 450, RefreshMinutes: 360}, gfs); got != 450 {
		t.Fatalf("default maxPoints at refresh 360: %d, want 450", got)
	}
	if got := ductingMaxPoints(DuctingConfig{MaxPoints: 1000, RefreshMinutes: 360}, gfs); got != 476 {
		t.Fatalf("refresh 360 max 1000: %d, want 476", got)
	}
	if got := ductingMaxPoints(DuctingConfig{MaxPoints: 200, RefreshMinutes: 60}, gfs); got != 79 {
		t.Fatalf("refresh 60: %d, want 79", got)
	}
	if got := ductingMaxPoints(DuctingConfig{MaxPoints: 0, RefreshMinutes: 60}, gfs); got != 1 {
		t.Fatalf("floor: %d, want 1", got)
	}
}

func TestDuctingClientConfig(t *testing.T) {
	if (&DuctingConfig{Model: "gfs"}).clientConfig() != nil {
		t.Fatal("disabled config must have no client config")
	}
	cc := (&DuctingConfig{Enabled: true, Model: "icon", GridStepDeg: 1, BaseURL: "https://u:p@secret.example"}).clientConfig()
	if cc == nil || cc.Model != "icon" || cc.ModelLabel != "DWD ICON" || cc.StepDeg != 1 ||
		cc.Attribution != "Weather data by Open-Meteo.com (CC BY 4.0), DWD ICON" {
		t.Fatalf("%+v", cc)
	}
	cc = (&DuctingConfig{Enabled: true, Model: "bogus"}).clientConfig()
	if cc.Model != "gfs" || cc.StepDeg != 0.5 {
		t.Fatalf("fallback: %+v", cc)
	}
}

func TestDuctingClientConfigEndpoint(t *testing.T) {
	off := clientConfigFor(t, `{"port":3000}`)
	if _, ok := off["ducting"]; ok {
		t.Fatal("ducting present with no config")
	}
	off = clientConfigFor(t, `{"ducting":{"enabled":false}}`)
	if _, ok := off["ducting"]; ok {
		t.Fatal("ducting present when disabled")
	}
	on := clientConfigFor(t, `{"ducting":{"enabled":true,"model":"icon","gridStepDeg":1,"baseUrl":"https://om.example.com"}}`)
	d, ok := on["ducting"].(map[string]interface{})
	if !ok {
		t.Fatalf("ducting is %T", on["ducting"])
	}
	if d["model"] != "icon" || d["modelLabel"] != "DWD ICON" || d["stepDeg"] != float64(1) || d["attribution"] == "" {
		t.Fatalf("%v", d)
	}
	for k := range d {
		if strings.EqualFold(k, "baseUrl") {
			t.Fatalf("baseUrl leaked: %v", d)
		}
	}
}

func TestDuctingLogConfig(t *testing.T) {
	logDuctingConfig(nil)
	logDuctingConfig(&DuctingConfig{})
	logDuctingConfig(&DuctingConfig{Enabled: true, Model: "rrfs-conus", BaseURL: "ftp://x"})
}

func TestDuctingMarginDeg(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	r, warns := (&DuctingConfig{Enabled: true}).resolved()
	if len(warns) != 0 || r.MarginDeg == nil || *r.MarginDeg != 5 || r.marginDegOrDefault() != 5 {
		t.Fatalf("default: %v %v", r.MarginDeg, warns)
	}
	for _, v := range []float64{0, 0.5, 10} {
		r, warns = (&DuctingConfig{MarginDeg: f(v)}).resolved()
		if len(warns) != 0 || *r.MarginDeg != v {
			t.Fatalf("explicit %v: %v %v", v, *r.MarginDeg, warns)
		}
	}
	for _, v := range []float64{-1, 11, math.NaN(), math.Inf(1)} {
		r, warns = (&DuctingConfig{MarginDeg: f(v)}).resolved()
		if !hasWarn(warns, "ducting.marginDeg must be 0..10, using 5") || *r.MarginDeg != 5 {
			t.Fatalf("invalid %v: %v %v", v, *r.MarginDeg, warns)
		}
	}
	// area set: margin is kept as given but never warned about
	r, warns = (&DuctingConfig{MarginDeg: f(5), Area: &[4]float64{10, 20, 12, 22}}).resolved()
	if len(warns) != 0 || r.Area == nil {
		t.Fatalf("area: %v", warns)
	}
	if (DuctingConfig{}).marginDegOrDefault() != 5 {
		t.Fatal("unresolved default")
	}
}

func TestDuctingMaxPointsSelfHosted(t *testing.T) {
	gfs, _ := ductingModel("gfs")
	// Public API: lowered to the 8,000 calls/day budget (see the test above).
	pub := DuctingConfig{MaxPoints: 1000, RefreshMinutes: 360, BaseURL: "https://api.open-meteo.com"}
	if got := ductingMaxPoints(pub, gfs); got != 476 {
		t.Errorf("public: %d, want 476", got)
	}
	// Self-hosted: only maxPoints applies.
	self := DuctingConfig{MaxPoints: 1000, RefreshMinutes: 60, BaseURL: "http://localhost:8080"}
	if got := ductingMaxPoints(self, gfs); got != 1000 {
		t.Errorf("self-hosted: %d, want 1000", got)
	}
	self.MaxPoints = 0
	if got := ductingMaxPoints(self, gfs); got != 1 {
		t.Errorf("self-hosted maxPoints 0: %d, want 1", got)
	}
}

func TestDuctingLogConfigSelfHosted(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	logDuctingConfig(&DuctingConfig{Enabled: true, Model: "gfs", BaseURL: "http://localhost:8080"})
	if !strings.Contains(buf.String(), "self-hosted: no call budget") || strings.Contains(buf.String(), "budget cap") {
		t.Errorf("log %q", buf.String())
	}
	buf.Reset()
	logDuctingConfig(&DuctingConfig{Enabled: true, Model: "gfs"})
	if !strings.Contains(buf.String(), "budget cap") {
		t.Errorf("public log %q", buf.String())
	}
}

func TestDuctingForecastHoursCoversRefresh(t *testing.T) {
	for _, tc := range []struct{ refresh, hours, want int }{
		{360, 3, 7},    // 6 h refresh needs 7 h
		{90, 1, 3},     // ceil(90/60)+1
		{60, 2, 2},     // exactly enough
		{1440, 24, 25}, // a day
		{360, 48, 48},  // default untouched
	} {
		r, warns := (&DuctingConfig{RefreshMinutes: tc.refresh, ForecastHours: tc.hours}).resolved()
		if r.ForecastHours != tc.want {
			t.Errorf("refresh %d hours %d: got %d, want %d", tc.refresh, tc.hours, r.ForecastHours, tc.want)
		}
		if raised := tc.want != tc.hours; raised != hasWarn(warns, "ducting.forecastHours") {
			t.Errorf("refresh %d hours %d: warnings %v", tc.refresh, tc.hours, warns)
		}
	}
}
