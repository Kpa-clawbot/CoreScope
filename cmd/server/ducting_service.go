package main

// Tropospheric ducting refresher and GET /api/ducting. A background loop
// fetches the forecast for the grid around the mesh's nodes, analyses every
// cell and hour, and swaps in a pre-encoded snapshot; the handler only
// writes those bytes. Nothing is written to any database.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"math"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	ductingBackoffBase      = 5 * time.Minute  // first retry after a failure
	ductingRateLimitBackoff = 60 * time.Minute // minimum wait after a 429 or a spent budget
)

var errDuctingBusy = errors.New("ducting refresh already running")

// ductingSnapshot is what the handler serves: the response and its JSON.
type ductingSnapshot struct {
	resp DuctingResponse
	body []byte
}

type ductingService struct {
	cfg    DuctingConfig // resolved
	model  DuctingModel
	source *ductingSource
	nodes  func() ([][2]float64, error) // [lat, lon] of visible nodes
	snap   atomic.Pointer[ductingSnapshot]
	now    func() time.Time
	sleep  func(ctx context.Context, d time.Duration) error // between refreshes

	refreshMu sync.Mutex // one refresh at a time
}

func newDuctingService(c *DuctingConfig, nodes func() ([][2]float64, error)) *ductingService {
	cfg, _ := c.resolved() // warnings are logged by logDuctingConfig
	model, _ := ductingModel(cfg.Model)
	s := &ductingService{
		cfg:    cfg,
		model:  model,
		source: newDuctingSource(cfg.BaseURL, model),
		nodes:  nodes,
		now:    time.Now,
		sleep:  ductingSleep,
	}
	s.snap.Store(s.statusSnapshot("pending", ""))
	return s
}

func (s *ductingService) attribution() string {
	return "Weather data by Open-Meteo.com (CC BY 4.0), " + s.model.Label
}

// baseResponse is a response with no data.
func (s *ductingService) baseResponse(status, msg string) DuctingResponse {
	return DuctingResponse{
		Status:      status,
		Error:       msg,
		Model:       s.model.ID,
		ModelLabel:  s.model.Label,
		Attribution: s.attribution(),
		StepDeg:     s.cfg.GridStepDeg,
		Times:       []int64{},
		Cells:       []DuctingCell{},
	}
}

func encodeDuctingSnapshot(resp DuctingResponse) *ductingSnapshot {
	body, err := json.Marshal(resp)
	if err != nil { // only plain types: cannot happen
		log.Printf("[ducting] encode failed: %v", err)
		body = []byte(`{"status":"error","error":"encode failed"}`)
	}
	return &ductingSnapshot{resp: resp, body: body}
}

func (s *ductingService) statusSnapshot(status, msg string) *ductingSnapshot {
	return encodeDuctingSnapshot(s.baseResponse(status, msg))
}

// ductingPublicError maps a refresh error to a short message safe to serve.
func ductingPublicError(err error) string {
	var he *ductingHTTPError
	switch {
	case errors.Is(err, errDuctingRateLimited):
		return "rate limited by Open-Meteo"
	case errors.Is(err, errDuctingBudget):
		return "Open-Meteo call budget exhausted"
	case errors.As(err, &he):
		return "Open-Meteo HTTP " + strconv.Itoa(he.Status)
	default:
		return "weather data unavailable"
	}
}

// refreshOnce runs one full refresh. On success the snapshot is replaced;
// on failure a good snapshot is kept, otherwise the status becomes "error".
func (s *ductingService) refreshOnce(ctx context.Context) error {
	if !s.refreshMu.TryLock() {
		return errDuctingBusy
	}
	defer s.refreshMu.Unlock()
	started := time.Now()
	resp, queried, err := s.build(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return err // shutting down: leave the state as it is
		}
		log.Printf("[ducting] refresh failed: %v", err)
		if cur := s.snap.Load(); cur == nil || cur.resp.Status != "ok" {
			s.snap.Store(s.statusSnapshot("error", ductingPublicError(err)))
		}
		return err
	}
	s.snap.Store(encodeDuctingSnapshot(resp))
	log.Printf("[ducting] refreshed: %d cells (%d dropped), %d hours, ~%.0f calls, %s",
		len(resp.Cells), queried-len(resp.Cells), len(resp.Times),
		float64(queried)*ductingCallsPerPoint(s.model), time.Since(started).Round(time.Millisecond))
	return nil
}

// build fetches and analyses the grid. queried is the number of cells sent
// to the API.
func (s *ductingService) build(ctx context.Context) (DuctingResponse, int, error) {
	var nodes [][2]float64
	if s.cfg.Area == nil {
		var err error
		if nodes, err = s.nodes(); err != nil {
			return DuctingResponse{}, 0, errors.New("node positions unavailable")
		}
	}
	cells, step := ductingGrid(nodes, s.cfg.Area, s.cfg.GridStepDeg, ductingMaxPoints(s.cfg, s.model), s.cfg.marginDegOrDefault())
	cells = s.inCoverage(cells)

	resp := s.baseResponse("ok", "")
	resp.StepDeg = step
	resp.GeneratedAt = s.now().Unix()
	if len(cells) == 0 {
		return resp, 0, nil
	}
	cols, err := s.source.fetch(ctx, cells, s.cfg.ForecastHours)
	if err != nil {
		return DuctingResponse{}, len(cells), err
	}
	if len(cols) == 0 {
		return DuctingResponse{}, len(cells), errors.New("no forecast data for any cell")
	}
	// Every cell shares the first column's hours; a column with others is
	// dropped (never seen in practice, but the arrays must line up).
	resp.Times = cols[0].Times
	resp.Cells = make([]DuctingCell, 0, len(cols))
	for _, col := range cols {
		if !ductingSameTimes(col.Times, resp.Times) {
			continue
		}
		resp.Cells = append(resp.Cells, s.analyzeColumn(col))
	}
	return resp, len(cells), nil
}

// inCoverage drops cells outside a regional model's bounds: the API has no
// data there, and asking would only spend calls.
func (s *ductingService) inCoverage(cells [][2]float64) [][2]float64 {
	b := s.model.Bounds
	if s.model.Global || b == nil {
		return cells
	}
	out := cells[:0]
	for _, c := range cells {
		if c[0] >= b[0] && c[0] <= b[2] && c[1] >= b[1] && c[1] <= b[3] {
			out = append(out, c)
		}
	}
	return out
}

func ductingSameTimes(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func ductingRound(v float64) int {
	if !ductFinite(v) {
		return 0
	}
	return int(math.Round(v))
}

func (s *ductingService) analyzeColumn(col ductingColumn) DuctingCell {
	n := len(col.Hours)
	cell := DuctingCell{
		Lat: col.Lat, Lon: col.Lon,
		C: make([]int, n), F: make([]int, n), B: make([]int, n),
		T: make([]int, n), DM: make([]int, n), G: make([]int, n),
	}
	for i, h := range col.Hours {
		r := AnalyzeDucting(DuctProfile(col.Elevation, h.SurfP, h.T2m, h.RH2m,
			s.model.LevelsHPa, h.Temp, h.RH, h.Geo))
		cell.C[i] = int(r.Class)
		cell.F[i] = ductingRound(r.FminMHz)
		cell.B[i] = ductingRound(r.BaseM)
		cell.T[i] = ductingRound(r.TopM)
		cell.DM[i] = ductingRound(r.DeltaM * 10)
		cell.G[i] = ductingRound(r.MinGradNkm)
	}
	return cell
}

// nextDelay is the wait before the next refresh: RefreshMinutes after a
// success; 5, 10, 20... minutes (capped at RefreshMinutes) after
// consecutive failures; at least an hour after a 429 or a spent budget.
func (s *ductingService) nextDelay(err error, failures int) time.Duration {
	refresh := time.Duration(s.cfg.RefreshMinutes) * time.Minute
	if err == nil {
		return refresh
	}
	d := refresh
	if failures < 16 { // 5 min << 15 is far past the 24 h cap
		if b := ductingBackoffBase << (failures - 1); b < refresh {
			d = b
		}
	}
	if (errors.Is(err, errDuctingRateLimited) || errors.Is(err, errDuctingBudget)) && d < ductingRateLimitBackoff {
		d = ductingRateLimitBackoff
	}
	return d
}

// run starts the refresh loop in a goroutine and returns a channel closed
// when it has exited (after ctx is cancelled). The first refresh starts
// immediately; startup is never blocked.
func (s *ductingService) run(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		failures := 0
		for {
			err := s.refreshOnce(ctx)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				failures++
			} else {
				failures = 0
			}
			if s.sleep(ctx, s.nextDelay(err, failures)) != nil {
				return
			}
		}
	}()
	return done
}

func (s *ductingService) serveHTTP(w http.ResponseWriter) {
	snap := s.snap.Load()
	if snap == nil {
		snap = s.statusSnapshot("pending", "")
	}
	if t := snap.resp.Times; snap.resp.Status == "ok" && len(t) > 0 && t[len(t)-1] < s.now().Unix() {
		resp := s.baseResponse("error", "forecast expired")
		resp.GeneratedAt = snap.resp.GeneratedAt
		writeJSON(w, resp)
		return
	}
	writeJSONBytes(w, snap.body)
}

// handleDucting is GET /api/ducting; 404 unless ducting.enabled.
func (s *Server) handleDucting(w http.ResponseWriter, r *http.Request) {
	if s.cfg == nil || !s.cfg.Ducting.enabled() || s.ducting == nil {
		writeError(w, http.StatusNotFound, "ducting disabled")
		return
	}
	s.ducting.serveHTTP(w)
}

// ductingNodes returns the positions of the nodes /api/nodes shows, so the
// overlay's cells cannot reveal a hidden or blacklisted node.
func (s *Server) ductingNodes() ([][2]float64, error) {
	return s.db.ductingNodeCoords(s.visibleNodeInfo())
}

// ductingNodeCoords returns [lat, lon] of the nodes with a position fix that
// visible accepts (nil accepts all). Read-only.
func (db *DB) ductingNodeCoords(visible func(n *nodeInfo) bool) ([][2]float64, error) {
	rows, err := db.conn.Query("SELECT public_key, name, lat, lon FROM nodes WHERE lat IS NOT NULL AND lon IS NOT NULL")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][2]float64
	for rows.Next() {
		var pk string
		var name sql.NullString
		var lat, lon float64
		if err := rows.Scan(&pk, &name, &lat, &lon); err != nil {
			return nil, err
		}
		if lat == 0 && lon == 0 {
			continue // no real fix
		}
		n := nodeInfo{PublicKey: pk, Name: name.String, Lat: lat, Lon: lon, HasGPS: true}
		if visible != nil && !visible(&n) {
			continue
		}
		out = append(out, [2]float64{lat, lon})
	}
	return out, rows.Err()
}
