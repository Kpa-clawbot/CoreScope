package main

// Open-Meteo client for the tropospheric ducting overlay: pressure-level
// forecasts for a list of grid cells, batched, paced and budgeted so the
// server stays inside the free tier (600 calls/min, 5,000/hour,
// 10,000/day; a location costs ductingCallsPerPoint calls). Errors never
// carry the request URL: baseUrl may be a private host.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	ductingBatchSize      = 50               // locations per request
	ductingCallsPerMinute = 400.0            // sustained pace, under the 600/min limit
	ductingMaxBodyBytes   = 8 << 20          // per batch response
	ductingMaxErrBody     = 64 << 10         // error responses are only scanned for "reason"
	ductingReasonMaxLen   = 120              // runes of the API "reason" kept in errors
	ductingHourlyCallCap  = 4500.0           // hard cap per rolling hour (limit 5,000)
	ductingDailyCallCap   = 9500.0           // hard cap per rolling day (limit 10,000)
	ductingHTTPTimeout    = 60 * time.Second // per request
	ductingUserAgent      = "CoreScope-Ducting/1.0 (+https://github.com/Kpa-clawbot/CoreScope)"
	ductingLedgerWindow   = 24 * time.Hour
)

// ductingPublicHost is the free public Open-Meteo API, the only host the
// call caps below apply to; a self-hosted instance has no such limits.
const ductingPublicHost = "api.open-meteo.com"

// ductingIsPublicAPI reports whether baseURL is the public Open-Meteo API.
// An empty or unparseable URL counts as public (the safe, capped side).
func ductingIsPublicAPI(baseURL string) bool {
	if baseURL == "" {
		return true
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Hostname() == "" {
		return true
	}
	return strings.EqualFold(strings.TrimSuffix(u.Hostname(), "."), ductingPublicHost)
}

// errDuctingRateLimited is returned when Open-Meteo answers 429.
var errDuctingRateLimited = errors.New("open-meteo rate limit (HTTP 429)")

// errDuctingBudget is returned, before any request is sent, when a fetch
// would push the calls spent in the last hour or day past the hard caps.
// Only the public API is capped (ductingSource.capped).
// Regular refreshes never reach them (ductingMaxPoints sizes the grid for
// ductingDailyCallBudget); repeated retries can.
var errDuctingBudget = errors.New("open-meteo call budget exhausted")

// ductingHTTPError is a non-2xx answer other than 429. Reason is the API's
// "reason", truncated; the URL is never included.
type ductingHTTPError struct {
	Status int
	Reason string
}

func (e *ductingHTTPError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf("open-meteo HTTP %d", e.Status)
	}
	return fmt.Sprintf("open-meteo HTTP %d: %s", e.Status, e.Reason)
}

// ductingHour is one forecast hour of one location. Per-level slices are in
// the model's LevelsHPa order; missing values are NaN.
type ductingHour struct {
	SurfP, T2m, RH2m float64
	Temp, RH, Geo    []float64
}

// ductingColumn is the forecast for one requested cell.
type ductingColumn struct {
	Lat, Lon  float64 // the requested cell centre
	Elevation float64 // m; NaN if the API gave none
	Times     []int64 // unix seconds
	Hours     []ductingHour
}

type ductingLedgerEntry struct {
	at    time.Time
	calls float64
}

type ductingSource struct {
	baseURL string
	model   DuctingModel
	client  *http.Client
	ua      string
	sleep   func(ctx context.Context, d time.Duration) error
	now     func() time.Time
	maxBody int64
	capped  bool // enforce the hourly/daily hard caps (public API only)

	mu     sync.Mutex
	ledger []ductingLedgerEntry // calls sent in the last ductingLedgerWindow
}

func newDuctingSource(baseURL string, model DuctingModel) *ductingSource {
	return &ductingSource{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		// A redirect is never followed: a 3xx is reported as a plain non-2xx
		// status, so a hostile or misconfigured upstream cannot bounce the
		// request (and its User-Agent) to another host.
		client: &http.Client{
			Timeout:       ductingHTTPTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		ua:      ductingUserAgent,
		sleep:   ductingSleep,
		now:     time.Now,
		maxBody: ductingMaxBodyBytes,
		capped:  ductingIsPublicAPI(baseURL),
	}
}

// ductingSleep waits d or until ctx is done.
func ductingSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// ductingVariables lists the hourly variables in request order.
func ductingVariables(m DuctingModel) []string {
	vars := []string{"temperature_2m", "relative_humidity_2m", "surface_pressure"}
	for _, p := range m.LevelsHPa {
		lv := strconv.FormatFloat(p, 'f', -1, 64)
		vars = append(vars,
			"temperature_"+lv+"hPa",
			"relative_humidity_"+lv+"hPa",
			"geopotential_height_"+lv+"hPa")
	}
	return vars
}

// spentLocked returns the calls recorded in the last hour and day, pruning older
// entries. Caller holds mu.
func (s *ductingSource) spentLocked(now time.Time) (hour, day float64) {
	keep := s.ledger[:0]
	for _, e := range s.ledger {
		age := now.Sub(e.at)
		if age >= ductingLedgerWindow {
			continue
		}
		keep = append(keep, e)
		day += e.calls
		if age < time.Hour {
			hour += e.calls
		}
	}
	s.ledger = keep
	return hour, day
}

func (s *ductingSource) record(calls float64) {
	s.mu.Lock()
	s.ledger = append(s.ledger, ductingLedgerEntry{at: s.now(), calls: calls})
	s.mu.Unlock()
}

// fetch returns the forecast for cells ([lat, lon]), in request order, minus
// the locations the API answered with a per-location error. Requests carry
// at most ductingBatchSize locations and are paced to ductingCallsPerMinute.
// Any request failure fails the whole fetch.
func (s *ductingSource) fetch(ctx context.Context, cells [][2]float64, forecastHours int) ([]ductingColumn, error) {
	if len(cells) == 0 {
		return nil, nil
	}
	perPoint := ductingCallsPerPoint(s.model)
	planned := float64(len(cells)) * perPoint
	s.mu.Lock()
	hour, day := s.spentLocked(s.now())
	s.mu.Unlock()
	if s.capped && (hour+planned > ductingHourlyCallCap || day+planned > ductingDailyCallCap) {
		return nil, errDuctingBudget
	}

	vars := ductingVariables(s.model)
	out := make([]ductingColumn, 0, len(cells))
	prevCalls := 0.0
	for start := 0; start < len(cells); start += ductingBatchSize {
		end := start + ductingBatchSize
		if end > len(cells) {
			end = len(cells)
		}
		batch := cells[start:end]
		if prevCalls > 0 {
			// The previous batch's calls at ductingCallsPerMinute.
			wait := time.Duration(prevCalls / ductingCallsPerMinute * float64(time.Minute))
			if err := s.sleep(ctx, wait); err != nil {
				return nil, err
			}
		}
		cols, err := s.fetchBatch(ctx, batch, vars, forecastHours)
		if err != nil {
			return nil, err
		}
		prevCalls = float64(len(batch)) * perPoint
		out = append(out, cols...)
	}
	return out, nil
}

func (s *ductingSource) query(batch [][2]float64, vars []string, forecastHours int) string {
	lats := make([]string, len(batch))
	lons := make([]string, len(batch))
	for i, c := range batch {
		lats[i] = strconv.FormatFloat(c[0], 'f', 4, 64)
		lons[i] = strconv.FormatFloat(c[1], 'f', 4, 64)
	}
	var b strings.Builder
	b.WriteString("latitude=")
	b.WriteString(strings.Join(lats, ","))
	b.WriteString("&longitude=")
	b.WriteString(strings.Join(lons, ","))
	b.WriteString("&hourly=")
	b.WriteString(strings.Join(vars, ","))
	b.WriteString("&models=")
	b.WriteString(s.model.OpenMeteoID)
	b.WriteString("&forecast_hours=")
	b.WriteString(strconv.Itoa(forecastHours))
	b.WriteString("&timeformat=unixtime&timezone=GMT")
	return b.String()
}

func (s *ductingSource) fetchBatch(ctx context.Context, batch [][2]float64, vars []string, forecastHours int) ([]ductingColumn, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		s.baseURL+"/v1/forecast?"+s.query(batch, vars, forecastHours), nil)
	if err != nil {
		return nil, errors.New("open-meteo request could not be built")
	}
	req.Header.Set("User-Agent", s.ua)
	req.Header.Set("Accept", "application/json")
	s.record(float64(len(batch)) * ductingCallsPerPoint(s.model))
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, ductingTransportError(ctx, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, errDuctingRateLimited
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, ductingMaxErrBody))
		return nil, &ductingHTTPError{Status: resp.StatusCode, Reason: ductingReason(body)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, s.maxBody+1))
	if err != nil {
		return nil, ductingTransportError(ctx, err)
	}
	if int64(len(body)) > s.maxBody {
		return nil, fmt.Errorf("open-meteo response larger than %d bytes", s.maxBody)
	}
	return parseDuctingResponse(body, batch, s.model, forecastHours, s.now())
}

// ductingTransportError drops the *url.Error wrapper (it names the URL) and
// the address-bearing detail beneath it.
func ductingTransportError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	var te interface{ Timeout() bool }
	if errors.As(err, &te) && te.Timeout() {
		return errors.New("open-meteo request timed out")
	}
	return errors.New("open-meteo request failed")
}

// ductingReason extracts and truncates the "reason" of an Open-Meteo error
// body; "" if there is none.
func ductingReason(body []byte) string {
	var e struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal(body, &e) != nil {
		return ""
	}
	r := strings.Map(func(c rune) rune {
		if c < 0x20 || c == 0x7f {
			return ' '
		}
		return c
	}, strings.TrimSpace(e.Reason))
	if utf8.RuneCountInString(r) > ductingReasonMaxLen {
		r = string([]rune(r)[:ductingReasonMaxLen])
	}
	return r
}

// openMeteoLocation is one location of a forecast response. The hourly
// block maps variable name to values (null -> nil); "time" is in it too.
type openMeteoLocation struct {
	Error     bool                  `json:"error"`
	Reason    string                `json:"reason"`
	Elevation *float64              `json:"elevation"`
	Hourly    map[string][]*float64 `json:"hourly"`
}

// parseDuctingResponse decodes a forecast response for batch: a JSON array
// for several locations, a single object for one.
func parseDuctingResponse(body []byte, batch [][2]float64, m DuctingModel, forecastHours int, now time.Time) ([]ductingColumn, error) {
	var locs []openMeteoLocation
	trimmed := bytes.TrimLeft(body, " \t\r\n")
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &locs); err != nil {
			return nil, fmt.Errorf("open-meteo response is not valid JSON: %w", err)
		}
	} else {
		var one openMeteoLocation
		if err := json.Unmarshal(trimmed, &one); err != nil {
			return nil, fmt.Errorf("open-meteo response is not valid JSON: %w", err)
		}
		locs = []openMeteoLocation{one}
	}
	if len(locs) != len(batch) {
		return nil, fmt.Errorf("open-meteo returned %d locations for %d requested", len(locs), len(batch))
	}
	out := make([]ductingColumn, 0, len(locs))
	for i, l := range locs {
		if col, ok := l.column(batch[i], m, forecastHours, now); ok {
			out = append(out, col)
		}
	}
	return out, nil
}

func ductingValue(vals []*float64, i int) float64 {
	if i < len(vals) && vals[i] != nil {
		return *vals[i]
	}
	return math.NaN()
}

// ductingTimeSlackSeconds is how far outside [now, now+forecastHours] a
// returned time may fall (clock skew, a forecast that started earlier).
const ductingTimeSlackSeconds = 24 * 3600

// ductingTimeAxisValid reports whether raw is a sane hourly axis for a
// forecastHours request: at most forecastHours+1 entries, finite, strictly
// ascending in exact 3600 s steps, and inside the slack window around now.
// It allocates nothing, so it runs before anything sized by the axis.
func ductingTimeAxisValid(raw []*float64, forecastHours int, now time.Time) bool {
	if len(raw) == 0 || len(raw) > forecastHours+1 {
		return false
	}
	lo := now.Unix() - ductingTimeSlackSeconds
	hi := now.Unix() + int64(forecastHours)*3600 + ductingTimeSlackSeconds
	var prev int64
	for i, t := range raw {
		if t == nil || !ductFinite(*t) || *t < float64(lo) || *t > float64(hi) {
			return false
		}
		v := int64(*t)
		if float64(v) != *t || (i > 0 && v-prev != 3600) {
			return false
		}
		prev = v
	}
	return true
}

// column converts one location; false for an error object or one without
// a usable time axis (see ductingTimeAxisValid).
func (l openMeteoLocation) column(cell [2]float64, m DuctingModel, forecastHours int, now time.Time) (ductingColumn, bool) {
	if l.Error || l.Hourly == nil {
		return ductingColumn{}, false
	}
	rawTimes := l.Hourly["time"]
	if !ductingTimeAxisValid(rawTimes, forecastHours, now) {
		return ductingColumn{}, false
	}
	times := make([]int64, len(rawTimes))
	for i, t := range rawTimes {
		times[i] = int64(*t)
	}
	col := ductingColumn{Lat: cell[0], Lon: cell[1], Elevation: math.NaN(), Times: times}
	if l.Elevation != nil {
		col.Elevation = *l.Elevation
	}
	nl := len(m.LevelsHPa)
	temps := make([][]*float64, nl)
	rhs := make([][]*float64, nl)
	geos := make([][]*float64, nl)
	for k, p := range m.LevelsHPa {
		lv := strconv.FormatFloat(p, 'f', -1, 64)
		temps[k] = l.Hourly["temperature_"+lv+"hPa"]
		rhs[k] = l.Hourly["relative_humidity_"+lv+"hPa"]
		geos[k] = l.Hourly["geopotential_height_"+lv+"hPa"]
	}
	sp, t2, rh2 := l.Hourly["surface_pressure"], l.Hourly["temperature_2m"], l.Hourly["relative_humidity_2m"]
	col.Hours = make([]ductingHour, len(times))
	// One backing array for every level value of the column.
	flat := make([]float64, 3*nl*len(times))
	for i := range times {
		h := ductingHour{
			SurfP: ductingValue(sp, i),
			T2m:   ductingValue(t2, i),
			RH2m:  ductingValue(rh2, i),
		}
		base := 3 * nl * i
		h.Temp = flat[base : base+nl : base+nl]
		h.RH = flat[base+nl : base+2*nl : base+2*nl]
		h.Geo = flat[base+2*nl : base+3*nl : base+3*nl]
		for k := 0; k < nl; k++ {
			h.Temp[k] = ductingValue(temps[k], i)
			h.RH[k] = ductingValue(rhs[k], i)
			h.Geo[k] = ductingValue(geos[k], i)
		}
		col.Hours[i] = h
	}
	return col, true
}
