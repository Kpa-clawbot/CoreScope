package main

import (
	"fmt"
	"log"
	"math"
	"net/url"
	"strings"
)

// Ducting config defaults and limits (see DuctingConfig in ducting_types.go).
const (
	defaultDuctingModel          = "gfs"
	defaultDuctingGridStepDeg    = 0.5
	defaultDuctingMarginDeg      = 5.0
	defaultDuctingMaxPoints      = 450
	defaultDuctingRefreshMinutes = 360
	defaultDuctingForecastHours  = 48
	defaultDuctingBaseURL        = "https://api.open-meteo.com"

	// ductingDailyCallBudget stays under Open-Meteo's free tier of 10,000
	// calls a day, leaving headroom for other users of the same key/IP.
	ductingDailyCallBudget = 8000.0
)

// ductingLevels returns every 25 hPa from 1000 down to lowest (inclusive).
func ductingLevels(lowest float64) []float64 {
	var out []float64
	for p := 1000.0; p >= lowest; p -= 25 {
		out = append(out, p)
	}
	return out
}

// ductingModels is the allowlist of weather models. Regional models are
// never the default.
var ductingModels = map[string]DuctingModel{
	"gfs": {
		ID: "gfs", OpenMeteoID: "gfs_global", Label: "NOAA GFS", Global: true,
		LevelsHPa: ductingLevels(700),
	},
	"icon": {
		ID: "icon", OpenMeteoID: "icon_global", Label: "DWD ICON", Global: true,
		LevelsHPa: []float64{1000, 975, 950, 925, 900, 850, 800, 700},
	},
	"rrfs-conus": {
		ID: "rrfs-conus", OpenMeteoID: "ncep_rrfs_conus", Label: "NOAA RRFS (US only)",
		RegionalNote: "US only (contiguous United States)",
		Bounds:       &[4]float64{21, -134, 53, -60},
		LevelsHPa:    ductingLevels(700),
	},
}

func ductingModel(id string) (DuctingModel, bool) {
	m, ok := ductingModels[id]
	return m, ok
}

// marginDegOrDefault is the effective margin; safe on an unresolved config.
func (c DuctingConfig) marginDegOrDefault() float64 {
	if c.MarginDeg == nil {
		return defaultDuctingMarginDeg
	}
	return *c.MarginDeg
}

func (c *DuctingConfig) enabled() bool { return c != nil && c.Enabled }

// resolved applies defaults and validates. Invalid values fall back to the
// default and add a warning naming the key. Warnings never echo baseUrl,
// which may carry credentials.
func (c *DuctingConfig) resolved() (DuctingConfig, []string) {
	var r DuctingConfig
	var warns []string
	if c == nil {
		c = &DuctingConfig{}
	}
	r.Enabled = c.Enabled

	r.Model = c.Model
	if r.Model == "" {
		r.Model = defaultDuctingModel
	} else if _, ok := ductingModel(r.Model); !ok {
		warns = append(warns, fmt.Sprintf("ducting.model %q is not supported, using %q", r.Model, defaultDuctingModel))
		r.Model = defaultDuctingModel
	}

	r.GridStepDeg = c.GridStepDeg
	if r.GridStepDeg == 0 {
		r.GridStepDeg = defaultDuctingGridStepDeg
	} else if math.IsNaN(r.GridStepDeg) || r.GridStepDeg < 0.25 || r.GridStepDeg > 2 {
		warns = append(warns, fmt.Sprintf("ducting.gridStepDeg must be 0.25..2, using %g", defaultDuctingGridStepDeg))
		r.GridStepDeg = defaultDuctingGridStepDeg
	}

	// MarginDeg is always non-nil once resolved.
	margin := defaultDuctingMarginDeg
	if c.MarginDeg != nil {
		if m := *c.MarginDeg; math.IsNaN(m) || m < 0 || m > 10 {
			warns = append(warns, fmt.Sprintf("ducting.marginDeg must be 0..10, using %g", defaultDuctingMarginDeg))
		} else {
			margin = m
		}
	}
	r.MarginDeg = &margin

	r.MaxPoints = c.MaxPoints
	if r.MaxPoints == 0 {
		r.MaxPoints = defaultDuctingMaxPoints
	} else if r.MaxPoints < 1 || r.MaxPoints > 1000 {
		warns = append(warns, fmt.Sprintf("ducting.maxPoints must be 1..1000, using %d", defaultDuctingMaxPoints))
		r.MaxPoints = defaultDuctingMaxPoints
	}

	r.RefreshMinutes = c.RefreshMinutes
	if r.RefreshMinutes == 0 {
		r.RefreshMinutes = defaultDuctingRefreshMinutes
	} else if r.RefreshMinutes < 60 || r.RefreshMinutes > 1440 {
		warns = append(warns, fmt.Sprintf("ducting.refreshMinutes must be 60..1440, using %d", defaultDuctingRefreshMinutes))
		r.RefreshMinutes = defaultDuctingRefreshMinutes
	}

	r.ForecastHours = c.ForecastHours
	if r.ForecastHours == 0 {
		r.ForecastHours = defaultDuctingForecastHours
	} else if r.ForecastHours < 1 || r.ForecastHours > 120 {
		warns = append(warns, fmt.Sprintf("ducting.forecastHours must be 1..120, using %d", defaultDuctingForecastHours))
		r.ForecastHours = defaultDuctingForecastHours
	}

	r.BaseURL = defaultDuctingBaseURL
	if c.BaseURL != "" {
		if u, ok := ductingBaseURL(c.BaseURL); ok {
			r.BaseURL = u
		} else {
			warns = append(warns, "ducting.baseUrl must be an absolute http(s) URL with a host and no credentials, using the default")
		}
	}

	if c.Area != nil {
		a := *c.Area
		if ductingAreaValid(a) {
			r.Area = &a
		} else {
			warns = append(warns, "ducting.area must be [south, west, north, east] with -90<=south<north<=90 and -180<=west<east<=180, ignoring it")
		}
	}

	if m, _ := ductingModel(r.Model); !m.Global && m.Bounds != nil {
		b := *m.Bounds
		switch {
		case r.Area == nil:
			warns = append(warns, fmt.Sprintf("ducting.model %q only has data for %s; nodes outside it will have no data", m.ID, m.RegionalNote))
		case r.Area[0] < b[0] || r.Area[1] < b[1] || r.Area[2] > b[2] || r.Area[3] > b[3]:
			warns = append(warns, fmt.Sprintf("ducting.area extends outside the coverage of model %q (%s)", m.ID, m.RegionalNote))
		}
	}
	return r, warns
}

func ductingAreaValid(a [4]float64) bool {
	for _, v := range a {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return a[0] >= -90 && a[0] < a[2] && a[2] <= 90 && a[1] >= -180 && a[1] < a[3] && a[3] <= 180
}

// ductingBaseURL validates an operator-supplied base URL and trims trailing
// slashes.
func ductingBaseURL(s string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return "", false
	}
	return strings.TrimRight(u.String(), "/"), true
}

// ductingCallsPerPoint is how many Open-Meteo calls one point costs: the
// API counts requests with more than 10 variables proportionally. Variables
// are temperature, humidity and geopotential height per level, plus three
// surface ones.
func ductingCallsPerPoint(m DuctingModel) float64 {
	return math.Max(1, float64(3*len(m.LevelsHPa)+3)/10)
}

// ductingMaxPoints caps the point count so the daily call volume stays
// within ductingDailyCallBudget. At least 1.
func ductingMaxPoints(c DuctingConfig, m DuctingModel) int {
	refresh := c.RefreshMinutes
	if refresh <= 0 {
		refresh = defaultDuctingRefreshMinutes
	}
	perDay := 1440.0 / float64(refresh)
	n := int(math.Floor(ductingDailyCallBudget / (ductingCallsPerPoint(m) * perDay)))
	if c.MaxPoints < n {
		n = c.MaxPoints
	}
	if n < 1 {
		n = 1
	}
	return n
}

// clientConfig is /api/config/client "ducting"; nil when disabled. Never
// carries baseUrl.
func (c *DuctingConfig) clientConfig() *DuctingClientConfig {
	if !c.enabled() {
		return nil
	}
	r, _ := c.resolved()
	m, _ := ductingModel(r.Model)
	return &DuctingClientConfig{
		Model:       m.ID,
		ModelLabel:  m.Label,
		StepDeg:     r.GridStepDeg,
		Attribution: "Weather data by Open-Meteo.com (CC BY 4.0), " + m.Label,
	}
}

func logDuctingConfig(c *DuctingConfig) {
	if !c.enabled() {
		return
	}
	r, warns := c.resolved()
	for _, w := range warns {
		log.Printf("[ducting] %s", w)
	}
	m, _ := ductingModel(r.Model)
	log.Printf("[ducting] enabled (model=%s, step=%g deg, margin=%g deg, maxPoints=%d (budget cap %d), refresh=%d min, forecast=%d h)",
		m.ID, r.GridStepDeg, r.marginDegOrDefault(), r.MaxPoints, ductingMaxPoints(r, m), r.RefreshMinutes, r.ForecastHours)
}
