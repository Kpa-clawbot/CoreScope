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
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	ductingBackoffBase      = 5 * time.Minute  // first retry after a failure
	ductingRateLimitBackoff = 60 * time.Minute // minimum wait after a 429 or a spent budget
	// ductingNodeMaxAgeDays: only nodes heard (last_seen) within this many
	// days place the grid, so a node that moved away or went dark long ago
	// cannot keep the forecast over the wrong place.
	ductingNodeMaxAgeDays = 14
	// ductingExpiryGrace: the last forecast hour stays current for this long.
	ductingExpiryGrace = 3600 // seconds
	// Against the public API the first fetch after start waits a random
	// ductingStartDelayMin..ductingStartDelayMax, so restarts in a row (or
	// several instances on one host, which share Open-Meteo's per-IP limits)
	// do not all fetch at once.
	ductingStartDelayMin = 60 * time.Second
	ductingStartDelayMax = 300 * time.Second
)

var errDuctingBusy = errors.New("ducting refresh already running")

// errDuctingNoCells: the grid is empty (no node positions yet, or none the
// model covers). Served as "pending" and retried after ductingBackoffBase;
// it is not a failure of the weather source.
var errDuctingNoCells = errors.New("no grid cells")

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
	// startDelay is the wait before the first refresh: random in
	// ductingStartDelayMin..ductingStartDelayMax against the public API, 0
	// for a self-hosted baseUrl.
	startDelay time.Duration

	refreshMu   sync.Mutex // one refresh at a time
	emptyLogged bool       // the empty grid was logged; guarded by refreshMu
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
	if s.source.capped {
		s.startDelay = ductingStartDelay(rand.Int64N)
	}
	s.snap.Store(s.statusSnapshot("pending", ""))
	return s
}

// ductingStartDelay draws the first-refresh delay, uniform in
// [ductingStartDelayMin, ductingStartDelayMax], from randN (rand.Int64N).
func ductingStartDelay(randN func(n int64) int64) time.Duration {
	span := int64(ductingStartDelayMax-ductingStartDelayMin) + 1
	return ductingStartDelayMin + time.Duration(randN(span))
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
	resp, queried, fetched, err := s.build(ctx)
	if errors.Is(err, errDuctingNoCells) {
		if !s.emptyLogged {
			log.Printf("[ducting] no grid cells yet (no node positions from the last %d days, or none the model covers); retrying every %s",
				ductingNodeMaxAgeDays, ductingBackoffBase)
			s.emptyLogged = true
		}
		// A good snapshot is kept, as after a failure; it expires on its own.
		if cur := s.snap.Load(); cur == nil || cur.resp.Status != "ok" {
			s.snap.Store(s.statusSnapshot("pending", ""))
		}
		return err
	}
	s.emptyLogged = false
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
	failed := ""
	if fetched.Failed > 0 {
		failed = ", " + strconv.Itoa(fetched.Failed) + " of " + strconv.Itoa(fetched.Batches) + " batches failed"
	}
	log.Printf("[ducting] refreshed: %d cells (%d dropped%s), %d hours, ~%.0f calls, %s",
		len(resp.Cells), queried-len(resp.Cells), failed, len(resp.Times),
		float64(queried)*ductingCallsPerPoint(s.model), time.Since(started).Round(time.Millisecond))
	return nil
}

// build fetches and analyses the grid. queried is the number of cells sent
// to the API; fetched says how many batches failed.
func (s *ductingService) build(ctx context.Context) (DuctingResponse, int, ductingFetch, error) {
	var nodes [][2]float64
	if s.cfg.Area == nil {
		var err error
		if nodes, err = s.nodes(); err != nil {
			return DuctingResponse{}, 0, ductingFetch{}, errors.New("node positions unavailable")
		}
	}
	cells, step, fellBack, info := ductingGridWithInfo(nodes, s.cfg.Area, s.cfg.GridStepDeg, ductingMaxPoints(s.cfg, s.model), s.cfg.marginDegOrDefault())
	if fellBack {
		log.Printf("[ducting] node area too large for maxPoints even at %g deg; covering the regions around nodes instead", ductingMaxStep)
	}
	cells = s.inCoverage(cells)
	if len(cells) == 0 {
		return DuctingResponse{}, 0, ductingFetch{}, errDuctingNoCells
	}
	log.Print(ductingGridLogLine(info, step, len(cells)))

	resp := s.baseResponse("ok", "")
	resp.StepDeg = step
	resp.GeneratedAt = s.now().Unix()
	fetched, err := s.source.fetch(ctx, cells, s.cfg.ForecastHours)
	if err != nil {
		return DuctingResponse{}, len(cells), fetched, err
	}
	if len(fetched.Cols) == 0 {
		return DuctingResponse{}, len(cells), fetched, errors.New("no forecast data for any cell")
	}
	// Every column has exactly the window's hours (ductingTimeAxisValid).
	resp.Times = fetched.Window.times()
	resp.Cells = make([]DuctingCell, 0, len(fetched.Cols))
	for _, col := range fetched.Cols {
		resp.Cells = append(resp.Cells, s.analyzeColumn(col))
	}
	return resp, len(cells), fetched, nil
}

// ductingGridLogLine describes the grid of one refresh: where and how big,
// never which nodes.
func ductingGridLogLine(info ductingGridInfo, step float64, cells int) string {
	b := info.box
	where := "box"
	if info.area {
		where = "area"
	}
	var sb strings.Builder
	sb.WriteString("[ducting] grid: " + where + " " +
		strconv.FormatFloat(b[0], 'f', 2, 64) + ".." + strconv.FormatFloat(b[2], 'f', 2, 64) + ", " +
		strconv.FormatFloat(b[1], 'f', 2, 64) + ".." + strconv.FormatFloat(b[3], 'f', 2, 64))
	if !info.area {
		sb.WriteString(" (" + strconv.Itoa(info.nodes) + " nodes, " + strconv.Itoa(info.trimmed) + " trimmed)")
	}
	sb.WriteString(", step " + strconv.FormatFloat(step, 'g', -1, 64) + " deg, " + strconv.Itoa(cells) + " cells")
	return sb.String()
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
		T: make([]int, n), DM: make([]int, n), G: make([]int, n), N: make([]int, n),
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
		cell.N[i] = ductingRound(r.MeanGradNkm)
	}
	return cell
}

// nextDelay is the wait before the next refresh: RefreshMinutes after a
// success; 5, 10, 20... minutes (capped at RefreshMinutes) after
// consecutive failures; at least an hour after a 429 or a spent budget;
// always 5 minutes (ductingBackoffBase) while the grid is empty.
func (s *ductingService) nextDelay(err error, failures int) time.Duration {
	refresh := time.Duration(s.cfg.RefreshMinutes) * time.Minute
	if err == nil {
		return refresh
	}
	if errors.Is(err, errDuctingNoCells) {
		if refresh < ductingBackoffBase {
			return refresh
		}
		return ductingBackoffBase
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
// after startDelay; startup is never blocked.
func (s *ductingService) run(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if s.startDelay > 0 && s.sleep(ctx, s.startDelay) != nil {
			return
		}
		failures := 0
		for {
			err := s.refreshOnce(ctx)
			if ctx.Err() != nil {
				return
			}
			switch {
			case errors.Is(err, errDuctingNoCells):
				// not a failure: nothing to back off from
			case err != nil:
				failures++
			default:
				failures = 0
			}
			if s.sleep(ctx, s.nextDelay(err, failures)) != nil {
				return
			}
		}
	}()
	return done
}

// serveHTTP writes the snapshot. The forecast is expired once its last hour
// is more than ductingExpiryGrace old. Only "ok" may be cached (5 minutes,
// well under any refresh interval) and carries an ETag (a snapshot's body
// never changes, and a new one has a new generatedAt); an If-None-Match
// naming it gets 304 with no body. "pending" and "error" are no-store so a
// cache never pins a transient state.
func (s *ductingService) serveHTTP(w http.ResponseWriter, r *http.Request) {
	snap := s.snap.Load()
	if snap == nil {
		snap = s.statusSnapshot("pending", "")
	}
	if t := snap.resp.Times; snap.resp.Status == "ok" && len(t) > 0 && t[len(t)-1]+ductingExpiryGrace < s.now().Unix() {
		resp := s.baseResponse("error", "forecast expired")
		resp.GeneratedAt = snap.resp.GeneratedAt
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, resp)
		return
	}
	if snap.resp.Status != "ok" {
		w.Header().Set("Cache-Control", "no-store")
		writeJSONBytes(w, snap.body)
		return
	}
	etag := ductingETag(snap.resp)
	w.Header().Set("Cache-Control", "public, max-age=300")
	w.Header().Set("ETag", etag)
	if r != nil && ductingETagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSONBytes(w, snap.body)
}

// ductingETag is the strong ETag of an ok snapshot: "<generatedAt>-<model>".
func ductingETag(resp DuctingResponse) string {
	return `"` + strconv.FormatInt(resp.GeneratedAt, 10) + "-" + resp.Model + `"`
}

// ductingETagMatches reports whether an If-None-Match header names etag:
// "*", or a comma-separated list compared weakly (a W/ prefix is ignored),
// as RFC 9110 13.1.2 asks for If-None-Match.
func ductingETagMatches(header, etag string) bool {
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimSpace(t)
		if t == "*" || strings.TrimPrefix(t, "W/") == etag {
			return true
		}
	}
	return false
}

// handleDucting is GET /api/ducting; 404 unless ducting.enabled.
func (s *Server) handleDucting(w http.ResponseWriter, r *http.Request) {
	if s.cfg == nil || !s.cfg.Ducting.enabled() || s.ducting == nil {
		writeError(w, http.StatusNotFound, "ducting disabled")
		return
	}
	s.ducting.serveHTTP(w, r)
}

// ductingNodes returns the positions of the nodes /api/nodes shows, so the
// overlay's cells cannot reveal a hidden or blacklisted node.
func (s *Server) ductingNodes() ([][2]float64, error) {
	return s.db.ductingNodeCoords(s.visibleNodeInfo(), time.Now())
}

// ductingParseLastSeen parses nodes.last_seen in the forms CoreScope and
// SQLite write; ok is false for anything else.
func ductingParseLastSeen(v string) (time.Time, bool) {
	v = strings.TrimSpace(v)
	if t, ok := parseRelayTS(v); ok {
		return t, true
	}
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04:05.000", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// ductingNodeCoords returns [lat, lon] of the nodes with a position fix,
// a last_seen within ductingNodeMaxAgeDays of now (missing or unparsable:
// left out) and that visible accepts (nil accepts all). Read-only.
func (db *DB) ductingNodeCoords(visible func(n *nodeInfo) bool, now time.Time) ([][2]float64, error) {
	rows, err := db.conn.Query("SELECT public_key, name, lat, lon, last_seen FROM nodes WHERE lat IS NOT NULL AND lon IS NOT NULL AND last_seen IS NOT NULL")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cutoff := now.Add(-ductingNodeMaxAgeDays * 24 * time.Hour)
	var out [][2]float64
	for rows.Next() {
		var pk string
		var name, lastSeen sql.NullString
		var lat, lon float64
		if err := rows.Scan(&pk, &name, &lat, &lon, &lastSeen); err != nil {
			return nil, err
		}
		if lat == 0 && lon == 0 {
			continue // no real fix
		}
		if seen, ok := ductingParseLastSeen(lastSeen.String); !ok || seen.Before(cutoff) {
			continue // stale, or no usable timestamp
		}
		n := nodeInfo{PublicKey: pk, Name: name.String, Lat: lat, Lon: lon, HasGPS: true}
		if visible != nil && !visible(&n) {
			continue
		}
		out = append(out, [2]float64{lat, lon})
	}
	return out, rows.Err()
}
