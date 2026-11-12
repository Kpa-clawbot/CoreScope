package main

// SRTM terrain for route line-of-sight and the repeater planner: ONE source
// serves both (terrain_config.go), so they read the same tiles.
//
// CONTRACT (planner_search.go / planner_api.go / elevation.go depend on it;
// keep signatures):
//   - Tiles: <TileURL>/<N30>/<N30W098>.hgt.gz, gzipped raw big-endian int16,
//     3601×3601, row 0 = north edge, 1 arc-second. Downsampled in memory to
//     3 arc-seconds (1201×1201). Missing (404/ocean) tiles have no elevation:
//     never 0 m, which would read as sea level.
//   - terrainView is immutable and safe for concurrent use without locks.
//   - The planner loads a rectangle of tiles (View); route LOS loads only the
//     tiles its legs' samples fall in (tiles + await, read through an
//     srtmTileView). Both share the cache, the downloads and the budget, and
//     both charge their client for the downloads they start (Starts, tiles)
//     before starting any.
//
// Safety model (this runs inside a public web server; nothing touches disk):
//   - Tile names and URLs are built only from clamped integer indices.
//   - A fetch reads at most srtmMaxGzipBytes of compressed body and exactly
//     srtmRawBytes of tile data: one byte more, one byte less or a bad gzip
//     CRC fails the tile. Decoding streams row by row, so no 26 MB raw buffer
//     is ever held; a decompression bomb costs one 2.9 MB grid and stops.
//   - At most srtmMaxConcurrent fetches run process-wide; at most
//     srtmPerMinute / srtmPerHour start per rolling window, raised to fit the
//     largest view (srtmBudgetFor). An upstream failure (429, 5xx, transport
//     error) opens the srtmBackoff cooldown ladder, which pauses every
//     download.
//   - Failed or partial tiles are never cached as data. A tile the server
//     does not have (404 for srtmNegTTL, 403 for srtmForbiddenTTL), a tile
//     whose body is unusable (corrupt, oversized, truncated, wrong length,
//     unexpected status; srtmBadTTL, and no cooldown: one bad tile must not
//     stop the others) and a tile that is all void are negative-cached and
//     have no elevation, so callers skip what touches them. Views count them
//     through terrainCoverage, so callers can warn that terrain is missing.

import (
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// terrainView answers elevation queries over the tiles it was built for.
type terrainView interface {
	// ElevationAt returns bilinear-interpolated metres above sea level.
	// ok=false when (lat, lon) has no terrain: it is outside the view's
	// tiles or in a tile without data (terrainCoverage). Callers skip such
	// a point, and anything that samples it; they never read it as 0 m.
	ElevationAt(lat, lon float64) (float64, bool)
}

// terrainSource loads the tiles that cover a bbox.
type terrainSource interface {
	// View returns a view covering bb, fetching missing tiles within the
	// fetch budget. Errors: errTerrainTooManyTiles, errTerrainBusy,
	// errTerrainUnavailable, or ctx.Err().
	View(ctx context.Context, bb geoBBox) (terrainView, error)
	// Resident reports whether every tile covering bb is already loaded
	// (or known missing), i.e. View would not download anything.
	Resident(bb geoBBox) bool
	// TileCount is the number of 1° tiles covering bb.
	TileCount(bb geoBBox) int
	// Starts is how many downloads View(bb) would start now: tiles neither
	// loaded, known missing nor already downloading, as many as the download
	// budget admits (none while downloads are paused). 0 for a box View
	// refuses.
	Starts(bb geoBBox) int
}

// terrainCoverage is implemented by views that know which of their tiles
// have no data: known missing, unusable, or all void, and so without
// elevation. Optional; callers type-assert.
type terrainCoverage interface {
	// MissingTiles counts the view's tiles without data, of total.
	MissingTiles() (missing, total int)
	// MissingAt reports whether (lat, lon) has no data: it lies in such a
	// tile, or outside the view. Exactly where ElevationAt says ok=false.
	MissingAt(lat, lon float64) bool
}

var (
	errTerrainTooManyTiles = errors.New("terrain: area spans too many tiles")
	errTerrainBusy         = errors.New("terrain: fetch budget exhausted")
	errTerrainUnavailable  = errors.New("terrain: tiles unavailable")
)

var (
	_ terrainSource   = (*srtmSource)(nil)
	_ terrainView     = (*srtmView)(nil)
	_ terrainCoverage = (*srtmView)(nil)
	_ terrainView     = srtmTileView(nil)
)

const (
	srtmRawSide     = 3601                          // samples per tile side at 1 arc-second
	srtmRawRowBytes = srtmRawSide * 2               // one big-endian int16 row
	srtmRawBytes    = srtmRawSide * srtmRawRowBytes // 25,934,402: the whole decompressed tile
	srtmStep        = 3                             // keep every 3rd row and column
	srtmSide        = (srtmRawSide-1)/srtmStep + 1  // 1201 samples per side at 3 arc-seconds
	srtmCells       = srtmSide - 1                  // 1200 grid intervals per degree
	srtmTileBytes   = srtmSide * srtmSide * 2       // one resident tile: ~2.9 MB
	srtmVoid        = -32768                        // SRTM no-data value

	srtmMaxGzipBytes  = 24 << 20 // real tiles reach ~17 MB (Sierra, Alps); raw is 25.9 MB
	srtmFetchTimeout  = 30 * time.Second
	srtmDialTimeout   = 5 * time.Second
	srtmSlotWait      = 2 * time.Minute // longest wait for a fetch slot once admitted
	srtmMaxConcurrent = 2
	srtmPerMinute     = 20               // the download budget's floors (srtmBudgetFor)
	srtmPerHour       = 200              // the download budget's floors (srtmBudgetFor)
	srtmNegTTL        = 24 * time.Hour   // 404: the tile does not exist (ocean)
	srtmForbiddenTTL  = time.Hour        // 403: S3's answer for absent keys, but also for a misconfigured bucket
	srtmBadTTL        = 10 * time.Minute // unusable body: retry this tile soon, the others at once
	srtmNegMax        = 1024
	srtmVoidWarnFrac  = 0.20
	srtmErrBodyDrain  = 4096
)

// srtmFetchSlots bounds concurrent tile fetches (and so decode memory) for
// the whole process, however many sources exist.
var srtmFetchSlots = make(chan struct{}, srtmMaxConcurrent)

// srtmBackoff is the download pause after the 1st, 2nd, 3rd... consecutive
// upstream failure; the last step repeats.
var srtmBackoff = []time.Duration{60 * time.Second, 5 * time.Minute, 30 * time.Minute, time.Hour}

// srtmHTTPDoer is the slice of *http.Client the source needs (test seam).
type srtmHTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// srtmKey is a 1° tile by its south-west corner: lat -90..89, lon -180..179.
type srtmKey struct{ lat, lon int }

// name is the skadi tile name, e.g. N30W098 or S01E000.
func (k srtmKey) name() string {
	ns, la := 'N', k.lat
	if la < 0 {
		ns, la = 'S', -la
	}
	ew, lo := 'E', k.lon
	if lo < 0 {
		ew, lo = 'W', -lo
	}
	return fmt.Sprintf("%c%02d%c%03d", ns, la, ew, lo)
}

// srtmIndex turns an already floored degree value into a tile index clamped
// to [lo, hi]. Comparing as floats first keeps huge values from overflowing
// the int conversion; NaN maps to lo.
func srtmIndex(f float64, lo, hi int) int {
	switch {
	case !(f > float64(lo)):
		return lo
	case f >= float64(hi):
		return hi
	}
	return int(f)
}

// srtmKeyOf is the tile holding (lat, lon); a point on a degree line belongs
// to the tile north/east of it, as math.Floor says.
func srtmKeyOf(lat, lon float64) srtmKey {
	return srtmKey{srtmIndex(math.Floor(lat), -90, 89), srtmIndex(math.Floor(lon), -180, 179)}
}

// srtmSpan is the rectangle of tiles covering a bbox.
type srtmSpan struct{ lat0, lon0, nLat, nLon int }

func (sp srtmSpan) count() int { return sp.nLat * sp.nLon }

// key is the i-th tile, row-major from the south-west corner.
func (sp srtmSpan) key(i int) srtmKey {
	return srtmKey{sp.lat0 + i/sp.nLon, sp.lon0 + i%sp.nLon}
}

// srtmSpanOf returns the tiles covering bb; ok=false for a NaN/Inf or
// inverted box (S>N, or W>E as an antimeridian box would be). A north or east
// edge lying exactly on a degree line does not pull in the next tile: N=31.0
// ends in tile N30.
func srtmSpanOf(bb geoBBox) (srtmSpan, bool) {
	for _, v := range [...]float64{bb.S, bb.W, bb.N, bb.E} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return srtmSpan{}, false
		}
	}
	if bb.S > bb.N || bb.W > bb.E {
		return srtmSpan{}, false
	}
	lat0 := srtmIndex(math.Floor(bb.S), -90, 89)
	lat1 := srtmIndex(math.Ceil(bb.N)-1, -90, 89)
	lon0 := srtmIndex(math.Floor(bb.W), -180, 179)
	lon1 := srtmIndex(math.Ceil(bb.E)-1, -180, 179)
	// max: a box that is a single point on a degree line still needs a tile.
	return srtmSpan{lat0, lon0, max(lat1-lat0, 0) + 1, max(lon1-lon0, 0) + 1}, true
}

// srtmCell splits a grid coordinate into the cell's first index, in
// [0, srtmCells-1], and the fraction, in [0, 1]. Out-of-range values (and
// NaN) clamp to the edge.
func srtmCell(v float64) (int, float64) {
	switch {
	case !(v > 0):
		return 0, 0
	case v >= srtmCells:
		return srtmCells - 1, 1
	}
	i := int(v)
	return i, v - float64(i)
}

// srtmPixel locates (lat, lon) on tile k's 1201² grid (row 0 = north edge):
// the north-west corner of its interpolation cell and the offsets into it.
// Positions outside the tile clamp to its edge.
func srtmPixel(lat, lon float64, k srtmKey) (row, col int, fy, fx float64) {
	// The explicit float64() rounds the product. Without it the compiler may
	// fuse it with srtmCell's subtraction into an FMA (arm64 does), and a
	// product that rounds up to an integer then yields a fraction just below 0.
	row, fy = srtmCell(float64((float64(k.lat+1) - lat) * srtmCells))
	col, fx = srtmCell(float64((lon - float64(k.lon)) * srtmCells))
	return row, col, fy, fx
}

// srtmBilinear interpolates grid g inside the cell at (row, col).
func srtmBilinear(g []int16, row, col int, fy, fx float64) float64 {
	c := g[row*srtmSide+col:]
	c = c[:srtmSide+2] // the cell's four corners: [0], [1], [srtmSide], [srtmSide+1]
	n := float64(c[0])*(1-fx) + float64(c[1])*fx
	s := float64(c[srtmSide])*(1-fx) + float64(c[srtmSide+1])*fx
	return n*(1-fy) + s*fy
}

// srtmView is an immutable set of tiles. The grids are never written after
// decode, and the view holds its own references, so reads take no lock and a
// cache eviction never invalidates a view.
type srtmView struct {
	span  srtmSpan
	grids [][]int16 // span.key(i) order; nil = tile without data (no elevation)
}

// tile is the grid holding (lat, lon) (nil: no data) and its key;
// ok=false outside the view. NaN fails too.
func (v *srtmView) tile(lat, lon float64) (g []int16, k srtmKey, ok bool) {
	sp := v.span
	if !(lat >= float64(sp.lat0) && lat <= float64(sp.lat0+sp.nLat) &&
		lon >= float64(sp.lon0) && lon <= float64(sp.lon0+sp.nLon)) {
		return nil, srtmKey{}, false
	}
	// SRTM tiles share their edge rows and columns, so every point's four
	// interpolation corners lie in the tile floor() picks. Only a point on
	// the view's own north/east edge needs clamping back onto the last tile.
	ti := min(int(math.Floor(lat))-sp.lat0, sp.nLat-1)
	tj := min(int(math.Floor(lon))-sp.lon0, sp.nLon-1)
	return v.grids[ti*sp.nLon+tj], srtmKey{sp.lat0 + ti, sp.lon0 + tj}, true
}

func (v *srtmView) MissingTiles() (missing, total int) {
	for _, g := range v.grids {
		if g == nil {
			missing++
		}
	}
	return missing, len(v.grids)
}

func (v *srtmView) MissingAt(lat, lon float64) bool {
	g, _, ok := v.tile(lat, lon)
	return !ok || g == nil
}

// ElevationAt repeats tile's lookup inline: tile is over the inlining
// budget, and this is the planner's innermost loop.
func (v *srtmView) ElevationAt(lat, lon float64) (float64, bool) {
	sp := v.span
	// Written so that NaN fails too.
	if !(lat >= float64(sp.lat0) && lat <= float64(sp.lat0+sp.nLat) &&
		lon >= float64(sp.lon0) && lon <= float64(sp.lon0+sp.nLon)) {
		return 0, false
	}
	ti := min(int(math.Floor(lat))-sp.lat0, sp.nLat-1)
	tj := min(int(math.Floor(lon))-sp.lon0, sp.nLon-1)
	g := v.grids[ti*sp.nLon+tj]
	if g == nil {
		return 0, false
	}
	row, col, fy, fx := srtmPixel(lat, lon, srtmKey{sp.lat0 + ti, sp.lon0 + tj})
	return srtmBilinear(g, row, col, fy, fx), true
}

// srtmTileView is an immutable set of tiles that need not form a rectangle:
// the tiles a route's leg samples fall in (elevation.go). A point is looked
// up in the tile srtmKeyOf names, the one legTiles loads for it, so a point
// on a degree line reads the edge row or column of the tile north or east of
// it, which SRTM tiles share with their neighbour. Like srtmView it takes no
// lock and holds its own grid references.
type srtmTileView map[srtmKey][]int16 // nil grid: tile without data

func (v srtmTileView) ElevationAt(lat, lon float64) (float64, bool) {
	// Written so that NaN fails too.
	if !(lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180) {
		return 0, false
	}
	k := srtmKeyOf(lat, lon)
	g := v[k]
	if g == nil {
		return 0, false
	}
	row, col, fy, fx := srtmPixel(lat, lon, k)
	return srtmBilinear(g, row, col, fy, fx), true
}

// srtmBudget is a sliding log of download starts: at most perMinute in any
// rolling minute and perHour in any rolling hour.
type srtmBudget struct {
	perMinute, perHour int
	starts             []time.Time // oldest first, all within the last hour
}

// srtmBudgetFor is the budget of a source whose largest view is viewTiles:
// srtmPerMinute, or one full view when that is more, so a cold request at
// the largest size the limits allow loads in one go; and srtmPerHour, or
// five minutes of that when more.
func srtmBudgetFor(viewTiles int) srtmBudget {
	m := max(srtmPerMinute, viewTiles)
	return srtmBudget{perMinute: m, perHour: max(srtmPerHour, 5*m)}
}

// take records a start at now when both windows allow it.
func (b *srtmBudget) take(now time.Time) bool {
	drop := 0
	for drop < len(b.starts) && now.Sub(b.starts[drop]) >= time.Hour {
		drop++
	}
	b.starts = b.starts[:copy(b.starts, b.starts[drop:])]
	if b.room(now) == 0 {
		return false
	}
	b.starts = append(b.starts, now)
	return true
}

// room is how many starts take would accept at now, recording none.
func (b *srtmBudget) room(now time.Time) int {
	inHour, inMinute := 0, 0
	for i := len(b.starts) - 1; i >= 0; i-- {
		d := now.Sub(b.starts[i])
		if d >= time.Hour {
			break // oldest first: the rest are older still
		}
		inHour++
		if d < time.Minute {
			inMinute++
		}
	}
	return max(0, min(b.perHour-inHour, b.perMinute-inMinute))
}

// srtmSource is the production terrainSource: an LRU of downsampled tiles in
// memory, filled from the tile server on demand.
type srtmSource struct {
	baseURL   string
	source    string // attribution for responses (terrainSourceName)
	userAgent string
	client    srtmHTTPDoer
	now       func() time.Time
	maxTiles  int           // max tiles in one load (terrainViewTiles)
	sem       chan struct{} // fetch slots; srtmFetchSlots outside tests

	sf singleflight.Group // one fetch per tile name

	mu      sync.Mutex
	cache   *lru[srtmKey, []int16] // decoded tiles; its cap is never below maxTiles
	neg     map[srtmKey]time.Time  // known-missing tile → expiry
	loading map[srtmKey]struct{}   // tiles whose load is running (loadChan)
	fails   int                    // consecutive upstream failures
	coolTo  time.Time              // no downloads before then
	budget  srtmBudget
}

// newSRTMSource builds the source from the resolved terrain config.
// viewTiles is the most tiles one load may span (terrainViewTiles): the
// per-view cap, the floor of the cache and of the per-minute download
// budget.
func newSRTMSource(cfg TerrainConfig, viewTiles int) *srtmSource {
	base := strings.TrimRight(cfg.TileURL, "/")
	if base == "" {
		base = defaultTerrainTileURL
	}
	ua := "CoreScope"
	if Version != "" {
		ua += "/" + Version
	}
	return &srtmSource{
		baseURL:   base,
		source:    terrainSourceName(base),
		userAgent: ua,
		client:    newSRTMHTTPClient(),
		now:       time.Now,
		maxTiles:  viewTiles,
		sem:       srtmFetchSlots,
		cache:     newLRU[srtmKey, []int16](cfg.cacheTiles(viewTiles), 0, nil),
		neg:       make(map[srtmKey]time.Time),
		loading:   make(map[srtmKey]struct{}),
		budget:    srtmBudgetFor(viewTiles),
	}
}

// newSRTMHTTPClient is the dedicated tile client: no proxy, no redirects,
// bounded dial/handshake/total time, and the body exactly as sent.
func newSRTMHTTPClient() *http.Client {
	d := &net.Dialer{Timeout: srtmDialTimeout, KeepAlive: 30 * time.Second}
	return &http.Client{
		Timeout: srtmFetchTimeout,
		Transport: &http.Transport{
			Proxy:                 nil,
			DialContext:           d.DialContext,
			TLSHandshakeTimeout:   srtmDialTimeout,
			ResponseHeaderTimeout: srtmFetchTimeout,
			MaxIdleConnsPerHost:   srtmMaxConcurrent,
			IdleConnTimeout:       90 * time.Second,
			ForceAttemptHTTP2:     true,
			// Tiles are .gz files: never gunzip behind our back, so the size
			// cap applies to the compressed bytes and srtmDecode sees them.
			DisableCompression: true,
		},
		// A tile host has no business redirecting us elsewhere.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// tileURL is built from the configured base and integer indices only.
func (s *srtmSource) tileURL(k srtmKey) string {
	n := k.name()
	return fmt.Sprintf("%s/%s/%s.hgt.gz", s.baseURL, n[:3], n)
}

func (s *srtmSource) TileCount(bb geoBBox) int {
	sp, ok := srtmSpanOf(bb)
	if !ok {
		return 0
	}
	return sp.count()
}

// Resident is false for boxes View would refuse (invalid or too many tiles).
// It does not touch LRU order.
func (s *srtmSource) Resident(bb geoBBox) bool {
	sp, ok := srtmSpanOf(bb)
	if !ok || sp.count() > s.maxTiles {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for i := 0; i < sp.count(); i++ {
		if _, ok := s.lookupLocked(sp.key(i), now, false); !ok {
			return false
		}
	}
	return true
}

func (s *srtmSource) View(ctx context.Context, bb geoBBox) (terrainView, error) {
	sp, ok := srtmSpanOf(bb)
	if !ok {
		return nil, errTerrainUnavailable
	}
	if sp.count() > s.maxTiles {
		return nil, errTerrainTooManyTiles
	}
	v := &srtmView{span: sp, grids: make([][]int16, sp.count())}
	var missing []int
	s.mu.Lock()
	now := s.now()
	for i := range v.grids {
		g, ok := s.lookupLocked(sp.key(i), now, true)
		if !ok {
			missing = append(missing, i)
			continue
		}
		v.grids[i] = g
	}
	cooling := now.Before(s.coolTo)
	s.mu.Unlock()
	if len(missing) == 0 {
		return v, nil
	}
	if cooling {
		return nil, errTerrainBusy
	}
	// Start every fetch before waiting on any.
	chans := make([]<-chan singleflight.Result, len(missing))
	for j, i := range missing {
		chans[j] = s.loadChan(sp.key(i))
	}
	for j, i := range missing {
		select {
		case r := <-chans[j]:
			if r.Err != nil {
				return nil, r.Err
			}
			v.grids[i], _ = r.Val.([]int16)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return v, nil
}

// Starts mirrors tiles for View's rectangle, without touching LRU order.
func (s *srtmSource) Starts(bb geoBBox) int {
	sp, ok := srtmSpanOf(bb)
	if !ok || sp.count() > s.maxTiles {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	n := 0
	for i := 0; i < sp.count(); i++ {
		k := sp.key(i)
		if _, ok := s.lookupLocked(k, now, false); ok {
			continue
		}
		if _, running := s.loading[k]; !running {
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return min(n, s.roomLocked(now))
}

// roomLocked is how many downloads admit would accept now: none while they
// are paused. Caller holds s.mu.
func (s *srtmSource) roomLocked(now time.Time) int {
	if now.Before(s.coolTo) {
		return 0
	}
	return s.budget.room(now)
}

// srtmTileSet is a set of tiles as tiles found them.
type srtmTileSet struct {
	grids   map[srtmKey][]int16 // resident: the grid, or nil for a tile without data
	need    []srtmKey           // to load: already downloading, or a download the budget admits; keys order
	starts  int                 // of need, those no load is running for: the downloads loading need would start
	refused []srtmKey           // not downloading, and over what the budget admits now (all of them while downloads are paused)
}

// tiles looks keys up in one pass, marking the cached ones used. It starts
// nothing, so the caller can charge for starts before calling await, and
// it counts as starts only the downloads the budget would admit now: the
// rest are refused, so no caller is charged for a download that would fail
// at once. Tiles already downloading cost no start.
func (s *srtmSource) tiles(keys []srtmKey) srtmTileSet {
	ts := srtmTileSet{grids: make(map[srtmKey][]int16, len(keys))}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	room := -1 // looked up at the first tile that needs a download
	for _, k := range keys {
		if g, ok := s.lookupLocked(k, now, true); ok {
			ts.grids[k] = g
			continue
		}
		if _, running := s.loading[k]; !running {
			if room < 0 {
				room = s.roomLocked(now)
			}
			if ts.starts == room {
				ts.refused = append(ts.refused, k)
				continue
			}
			ts.starts++
		}
		ts.need = append(ts.need, k)
	}
	return ts
}

// await starts (or joins) the load of each key and waits until all are done,
// ctx ends or wait passes. A finished key lands in got (its grid, or nil for
// a tile without data) or, when its load failed (busy, unavailable), in
// failed; a key in neither is still loading, and goes on loading.
func (s *srtmSource) await(ctx context.Context, keys []srtmKey, wait time.Duration) (got map[srtmKey][]int16, failed map[srtmKey]bool) {
	got, failed = make(map[srtmKey][]int16, len(keys)), make(map[srtmKey]bool)
	chans := make([]<-chan singleflight.Result, len(keys))
	for i, k := range keys {
		chans[i] = s.loadChan(k)
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	waiting := true
	for i, k := range keys {
		var r singleflight.Result
		select {
		case r = <-chans[i]:
		default:
			if !waiting {
				continue // collect only what has already finished
			}
			select {
			case r = <-chans[i]:
			case <-timer.C:
				waiting = false
				continue
			case <-ctx.Done():
				waiting = false
				continue
			}
		}
		if r.Err != nil {
			failed[k] = true
			continue
		}
		got[k], _ = r.Val.([]int16)
	}
	return got, failed
}

// loadChan starts tile k's load, or joins the one running. It runs detached
// from every caller, so one that gives up still leaves the tile cached for
// its retry. While it runs, k is in s.loading (tiles counts no start for it).
func (s *srtmSource) loadChan(k srtmKey) <-chan singleflight.Result {
	return s.sf.DoChan(k.name(), func() (interface{}, error) {
		s.mu.Lock()
		s.loading[k] = struct{}{}
		s.mu.Unlock()
		defer func() {
			s.mu.Lock()
			delete(s.loading, k)
			s.mu.Unlock()
		}()
		return s.load(k)
	})
}

// lookupLocked finds k in the cache (grid) or the negative cache (nil grid).
// touch marks a cached tile most recently used. Caller holds s.mu.
func (s *srtmSource) lookupLocked(k srtmKey, now time.Time, touch bool) ([]int16, bool) {
	var g []int16
	var ok bool
	if touch {
		g, ok = s.cache.Get(k)
	} else {
		g, ok = s.cache.Peek(k)
	}
	if ok {
		return g, true
	}
	if exp, ok := s.neg[k]; ok {
		if now.Before(exp) {
			return nil, true
		}
		delete(s.neg, k)
	}
	return nil, false
}

// load fetches one tile. It runs as the tile's singleflight call, on a
// goroutine singleflight starts, so it must never panic out. A nil grid with
// a nil error is a known-missing tile.
func (s *srtmSource) load(k srtmKey) (grid []int16, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[terrain] tile %s: panic: %v", k.name(), r)
			grid, err = nil, errTerrainUnavailable
		}
	}()
	// A call that starts just after another stored the tile finds it here.
	s.mu.Lock()
	g, ok := s.lookupLocked(k, s.now(), true)
	s.mu.Unlock()
	if ok {
		return g, nil
	}
	// Admission comes before the slot wait, so the number of queued
	// fetches is bounded by the budget.
	if !s.admit() {
		return nil, errTerrainBusy
	}
	wait := time.NewTimer(srtmSlotWait)
	defer wait.Stop()
	select {
	case s.sem <- struct{}{}:
	case <-wait.C:
		return nil, errTerrainBusy
	}
	defer func() { <-s.sem }()
	// A fetch that failed while this one queued has opened a cooldown.
	if s.cooling() {
		return nil, errTerrainBusy
	}
	return s.fetchAndStore(k)
}

// admit applies the cooldown and the rolling budgets, recording one start.
func (s *srtmSource) admit() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if now.Before(s.coolTo) {
		return false
	}
	return s.budget.take(now)
}

func (s *srtmSource) cooling() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.now().Before(s.coolTo)
}

// openCooldown records an upstream failure and blocks downloads for the next
// step of the srtmBackoff ladder, returning how long they stay blocked.
// Failures landing inside a running cooldown do not escalate it.
func (s *srtmSource) openCooldown() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if now.Before(s.coolTo) {
		return s.coolTo.Sub(now)
	}
	d := srtmBackoff[min(s.fails, len(srtmBackoff)-1)]
	s.fails++
	s.coolTo = now.Add(d)
	return d
}

// put caches a decoded tile and resets the failure ladder.
func (s *srtmSource) put(k srtmKey, grid []int16) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fails = 0
	delete(s.neg, k)
	s.cache.Put(k, grid)
}

// putMissing remembers for ttl a tile without data. When the negative
// cache is full it drops expired entries, then the one closest to expiry.
func (s *srtmSource) putMissing(k srtmKey, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if _, ok := s.neg[k]; !ok && len(s.neg) >= srtmNegMax {
		var oldest srtmKey
		var oldestAt time.Time
		for nk, exp := range s.neg {
			if !now.Before(exp) {
				delete(s.neg, nk)
			} else if oldestAt.IsZero() || exp.Before(oldestAt) {
				oldest, oldestAt = nk, exp
			}
		}
		if len(s.neg) >= srtmNegMax {
			delete(s.neg, oldest)
		}
	}
	s.neg[k] = now.Add(ttl)
}

// fetchAndStore downloads one tile under a fetch slot and records the outcome.
func (s *srtmSource) fetchAndStore(k srtmKey) ([]int16, error) {
	// Detached from every caller: the tile is worth having even if the
	// request that asked for it has gone.
	ctx, cancel := context.WithTimeout(context.Background(), srtmFetchTimeout)
	defer cancel()
	start := time.Now()
	grid, voids, status, err := s.fetch(ctx, k)
	switch {
	case status == http.StatusNotFound:
		return s.missing(k, srtmNegTTL, "HTTP 404")
	case status == http.StatusForbidden:
		return s.missing(k, srtmForbiddenTTL, "HTTP 403")
	case err == nil && voids == srtmSide*srtmSide && srtmAllZero(grid):
		return s.missing(k, srtmNegTTL, "all void")
	case err == nil:
	case srtmUpstreamFailed(status, err):
		d := s.openCooldown()
		log.Printf("[terrain] tile %s: %v; tile downloads paused for %s", k.name(), err, d)
		return nil, errTerrainUnavailable
	default:
		// The server answered, but not with a usable tile. Only this tile
		// is set aside; the others keep downloading.
		return s.missing(k, srtmBadTTL, err.Error())
	}
	if frac := float64(voids) / (srtmSide * srtmSide); frac > srtmVoidWarnFrac {
		log.Printf("[terrain] tile %s is %.0f%% void (filled from neighbours or 0 m)", k.name(), frac*100)
	}
	s.put(k, grid)
	log.Printf("[terrain] tile %s loaded in %s", k.name(), time.Since(start).Round(time.Millisecond))
	return grid, nil
}

// missing negative-caches k for ttl, so views report it missing and give no
// elevation there, and logs why.
func (s *srtmSource) missing(k srtmKey, ttl time.Duration, why string) ([]int16, error) {
	s.putMissing(k, ttl)
	log.Printf("[terrain] tile %s: %s; no terrain there for %s", k.name(), why, ttl)
	return nil, nil
}

// srtmUpstreamError is a fetch failure that is the network's fault, not the
// tile's: no response, or a body that stopped with a read error.
type srtmUpstreamError struct{ err error }

func (e *srtmUpstreamError) Error() string { return e.err.Error() }
func (e *srtmUpstreamError) Unwrap() error { return e.err }

// srtmUpstreamFailed reports whether a failed fetch says the tile server or
// the path to it is unwell (429, 5xx, transport): the cases that pause every
// download. Anything else is about this one tile.
func srtmUpstreamFailed(status int, err error) bool {
	var ue *srtmUpstreamError
	return errors.As(err, &ue) || status == http.StatusTooManyRequests || status >= 500
}

// srtmBody records the first read error that is not the body ending, early
// or not: a timeout or reset mid-download is the network's fault, while a
// short body is the tile's.
type srtmBody struct {
	r   io.Reader
	err error
}

func (b *srtmBody) Read(p []byte) (int, error) {
	n, err := b.r.Read(p)
	if err != nil && b.err == nil && err != io.EOF && !errors.Is(err, io.ErrUnexpectedEOF) {
		b.err = err
	}
	return n, err
}

// srtmAllZero reports whether every sample of grid is 0.
func srtmAllZero(grid []int16) bool {
	for _, v := range grid {
		if v != 0 {
			return false
		}
	}
	return true
}

// fetch downloads and decodes one tile. status is the HTTP status, 0 when no
// response arrived; err is nil only for a complete, valid tile, and is an
// *srtmUpstreamError when the network, not the tile, failed.
func (s *srtmSource) fetch(ctx context.Context, k srtmKey) (grid []int16, voids, status int, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.tileURL(k), nil)
	if err != nil {
		return nil, 0, 0, &srtmUpstreamError{errors.New("bad tile URL")}
	}
	req.Header.Set("User-Agent", s.userAgent)
	resp, err := s.client.Do(req)
	if err != nil {
		// Unwrap: url.Error text embeds the URL, which may carry credentials.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, 0, 0, &srtmUpstreamError{fmt.Errorf("request failed: %w", err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, srtmErrBodyDrain))
		return nil, 0, resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > srtmMaxGzipBytes {
		return nil, 0, resp.StatusCode, fmt.Errorf("body of %d bytes is over the %d-byte cap", resp.ContentLength, srtmMaxGzipBytes)
	}
	body := &srtmBody{r: io.LimitReader(resp.Body, srtmMaxGzipBytes)}
	grid, voids, err = srtmDecode(body)
	if err != nil && body.err != nil {
		err = &srtmUpstreamError{fmt.Errorf("reading the body: %w", body.err)}
	}
	return grid, voids, resp.StatusCode, err
}

// srtmDecode gunzips one 1-arc-second tile and returns it downsampled to the
// 1201² grid, with voids filled, plus the number of void samples found. It
// reads the 3601 rows with io.ReadFull (exactly srtmRawBytes in all), keeping
// every srtmStep-th row and column, then requires EOF on one more read, which
// is also when gzip checks the CRC and length trailer. Anything short, long
// or corrupt is an error.
func srtmDecode(r io.Reader) (grid []int16, voids int, err error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, 0, fmt.Errorf("not a gzip tile: %w", err)
	}
	defer zr.Close()
	grid = make([]int16, srtmSide*srtmSide)
	buf := make([]byte, srtmRawRowBytes)
	var raw []int16 // full-resolution row, only for rows with voids
	for y := 0; y < srtmRawSide; y++ {
		if _, err := io.ReadFull(zr, buf); err != nil {
			return nil, 0, fmt.Errorf("tile shorter than %d bytes: %w", srtmRawBytes, err)
		}
		if y%srtmStep != 0 {
			continue
		}
		out := grid[(y/srtmStep)*srtmSide:][:srtmSide]
		rowVoids := 0
		for c := range out {
			v := int16(binary.BigEndian.Uint16(buf[2*srtmStep*c:]))
			if v == srtmVoid {
				rowVoids++
			}
			out[c] = v
		}
		if rowVoids == 0 {
			continue
		}
		// Fill from the nearest valid sample at full resolution.
		voids += rowVoids
		if raw == nil {
			raw = make([]int16, srtmRawSide)
		}
		for c := range raw {
			raw[c] = int16(binary.BigEndian.Uint16(buf[2*c:]))
		}
		srtmFillVoids(raw)
		for c := range out {
			out[c] = raw[c*srtmStep]
		}
	}
	var extra [1]byte
	switch _, err := io.ReadFull(zr, extra[:]); {
	case err == nil:
		return nil, 0, fmt.Errorf("tile longer than %d bytes", srtmRawBytes)
	case err != io.EOF:
		return nil, 0, fmt.Errorf("tile trailer: %w", err)
	}
	return grid, voids, nil
}

// srtmFillVoids replaces each void in row with the nearest valid sample in
// the same row (ties go west), or 0 when the row has none.
func srtmFillVoids(row []int16) {
	for i := 0; i < len(row); {
		if row[i] != srtmVoid {
			i++
			continue
		}
		j := i
		for j < len(row) && row[j] == srtmVoid {
			j++
		}
		// row[i:j] is a run of voids; row[i-1] and row[j] are valid if present.
		for p := i; p < j; p++ {
			switch {
			case i == 0 && j == len(row):
				row[p] = 0
			case i == 0:
				row[p] = row[j]
			case j == len(row), p-(i-1) <= j-p:
				row[p] = row[i-1]
			default:
				row[p] = row[j]
			}
		}
		i = j
	}
}
