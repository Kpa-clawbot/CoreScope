package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ─── Synthetic tiles ───────────────────────────────────────────────────────

// srtmTestTiles caches generated gzip bodies by id: each costs one build.
var srtmTestTiles struct {
	sync.Mutex
	m map[string][]byte
}

// srtmTestGzip gzips whatever write produces, cached under id.
func srtmTestGzip(t testing.TB, id string, level int, write func(w io.Writer) error) []byte {
	t.Helper()
	srtmTestTiles.Lock()
	defer srtmTestTiles.Unlock()
	if b, ok := srtmTestTiles.m[id]; ok {
		return b
	}
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, level)
	if err != nil {
		t.Fatal(err)
	}
	if err := write(zw); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if srtmTestTiles.m == nil {
		srtmTestTiles.m = make(map[string][]byte)
	}
	srtmTestTiles.m[id] = buf.Bytes()
	return buf.Bytes()
}

// srtmTileFunc gzips a full 3601² tile whose raw sample (row, col) is f(row, col).
func srtmTileFunc(t testing.TB, id string, f func(r, c int) int16) []byte {
	return srtmTestGzip(t, id, gzip.BestSpeed, func(w io.Writer) error {
		row := make([]byte, srtmRawRowBytes)
		for r := 0; r < srtmRawSide; r++ {
			for c := 0; c < srtmRawSide; c++ {
				binary.BigEndian.PutUint16(row[2*c:], uint16(f(r, c)))
			}
			if _, err := w.Write(row); err != nil {
				return err
			}
		}
		return nil
	})
}

// srtmConstTile gzips a tile of n raw bytes holding v in every sample (n =
// srtmRawBytes for a valid tile; the last sample may be cut short).
func srtmConstTile(t testing.TB, v int16, n, level int) []byte {
	return srtmTestGzip(t, fmt.Sprintf("const:%d:%d:%d", v, n, level), level, func(w io.Writer) error {
		row := make([]byte, srtmRawRowBytes)
		for c := 0; c < srtmRawSide; c++ {
			binary.BigEndian.PutUint16(row[2*c:], uint16(v))
		}
		for n > 0 {
			k := min(n, len(row))
			if _, err := w.Write(row[:k]); err != nil {
				return err
			}
			n -= k
		}
		return nil
	})
}

// srtmPatternAt is the known 10×10 block at the north-west corner of the
// "grid" tile, at 3-arc-second positions. Negative values check signed
// decoding; distinct (R,C)/(C,R) values check orientation.
func srtmPatternAt(R, C int) int16 { return int16((R*37+C*101+R*C*13)%251 - 120) }

// srtmGridTile: raw sample = row+col (downsampled: 3R+3C), except the 10×10
// pattern block.
func srtmGridTile(t testing.TB) []byte {
	return srtmTileFunc(t, "grid", func(r, c int) int16 {
		if r%srtmStep == 0 && c%srtmStep == 0 && r/srtmStep < 10 && c/srtmStep < 10 {
			return srtmPatternAt(r/srtmStep, c/srtmStep)
		}
		return int16(r + c)
	})
}

// srtmLinearTile is tile (lat, lon) of a field that is linear across tiles:
// elevation = 3600·(32−lat) + 3600·(lon+98) on N30..N31 × W098..W097, so the
// shared tile edges agree exactly as real SRTM edges do.
func srtmLinearTile(t testing.TB, k srtmKey) []byte {
	return srtmTileFunc(t, "linear:"+k.name(), func(r, c int) int16 {
		return int16((31-k.lat)*(srtmRawSide-1) + r + (k.lon+98)*(srtmRawSide-1) + c)
	})
}

func srtmLinearWant(lat, lon float64) float64 { return 3600*(32-lat) + 3600*(lon+98) }

// ─── Test upstream ─────────────────────────────────────────────────────────

type srtmUpstream struct {
	ts   *httptest.Server
	mu   sync.Mutex
	hits map[string]int
	uas  []string
	h    http.HandlerFunc
}

func (u *srtmUpstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	u.hits[r.URL.Path]++
	u.uas = append(u.uas, r.Header.Get("User-Agent"))
	h := u.h
	u.mu.Unlock()
	h(w, r)
}

func (u *srtmUpstream) hitsFor(path string) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.hits[path]
}

func (u *srtmUpstream) total() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	n := 0
	for _, v := range u.hits {
		n += v
	}
	return n
}

// srtmTestViewTiles is a test source's per-view cap: the planner's at its
// default sizes, as when only the planner is enabled.
var srtmTestViewTiles = PlannerConfig{}.tileLimits().max()

// newSRTMTest builds a source against a local upstream with a fake clock and
// its own fetch slots. cacheTiles 0 means the default.
func newSRTMTest(t testing.TB, cacheTiles int, h http.HandlerFunc) (*srtmSource, *srtmUpstream, *elevClock) {
	t.Helper()
	u := &srtmUpstream{hits: make(map[string]int), h: h}
	u.ts = httptest.NewServer(u)
	s := newSRTMSource(TerrainConfig{TileURL: u.ts.URL, TileCacheTiles: cacheTiles}, srtmTestViewTiles)
	s.sem = make(chan struct{}, srtmMaxConcurrent)
	clk := &elevClock{t: time.Unix(1_700_000_000, 0)}
	s.now = clk.now
	t.Cleanup(func() {
		if c, ok := s.client.(*http.Client); ok {
			c.CloseIdleConnections()
		}
		u.ts.Close()
	})
	return s, u, clk
}

func srtmPath(k srtmKey) string {
	n := k.name()
	return "/" + n[:3] + "/" + n + ".hgt.gz"
}

// srtmBox is a bbox well inside tile k.
func srtmBox(k srtmKey) geoBBox {
	la, lo := float64(k.lat), float64(k.lon)
	return geoBBox{S: la + 0.25, W: lo + 0.25, N: la + 0.75, E: lo + 0.75}
}

// srtmServe serves body for path and 404 for everything else.
func srtmServe(tiles map[string][]byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, ok := tiles[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		w.Write(b)
	}
}

func srtmCacheLen(s *srtmSource) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ll.Len()
}

func srtmNegLen(s *srtmSource) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.neg)
}

// srtmGate holds handlers until opened. Register t.Cleanup(gate.open) after
// newSRTMTest: cleanups run last-first, so a failed test opens the gate
// before the server waits for its handlers.
type srtmGate struct {
	ch   chan struct{}
	once sync.Once
}

func newSRTMGate() *srtmGate { return &srtmGate{ch: make(chan struct{})} }

func (g *srtmGate) open() { g.once.Do(func() { close(g.ch) }) }

// wait reports false when the client went away first.
func (g *srtmGate) wait(r *http.Request) bool {
	select {
	case <-g.ch:
		return true
	case <-r.Context().Done():
		return false
	}
}

func srtmEventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func srtmMustView(t *testing.T, s *srtmSource, bb geoBBox) terrainView {
	t.Helper()
	v, err := s.View(context.Background(), bb)
	if err != nil {
		t.Fatalf("View(%+v): %v", bb, err)
	}
	return v
}

func srtmWantElev(t *testing.T, v terrainView, lat, lon, want float64) {
	t.Helper()
	got, ok := v.ElevationAt(lat, lon)
	if !ok {
		t.Fatalf("ElevationAt(%v, %v): ok=false, want %v", lat, lon, want)
	}
	if math.Abs(got-want) > 1e-6 {
		t.Fatalf("ElevationAt(%v, %v) = %v, want %v", lat, lon, got, want)
	}
}

// srtmWantNoElev: (lat, lon) has no terrain, so callers skip it.
func srtmWantNoElev(t *testing.T, v terrainView, lat, lon float64) {
	t.Helper()
	if got, ok := v.ElevationAt(lat, lon); ok {
		t.Fatalf("ElevationAt(%v, %v) = %v, ok=true; want no elevation", lat, lon, got)
	}
}

type srtmLogBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *srtmLogBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *srtmLogBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// ─── Tile selection ────────────────────────────────────────────────────────

var srtmNameRE = regexp.MustCompile(`^[NS]\d{2}[EW]\d{3}$`)

func TestSRTMTileNaming(t *testing.T) {
	cases := []struct {
		lat, lon float64
		want     string
	}{
		{30.2672, -97.7431, "N30W098"},
		{0, 0, "N00E000"},
		{0.0001, 0.0001, "N00E000"},
		{-0.0001, -0.0001, "S01W001"},
		{-0.0001, 0.0001, "S01E000"},
		{0.0001, -0.0001, "N00W001"},
		{-0.5, -49.5, "S01W050"},
		{31.0, -98.0, "N31W098"}, // on a degree line: the tile north/east of it
		{30.9999, -97.0001, "N30W098"},
		{-1.0, 1.0, "S01E001"},
		{-89.9999, 0.5, "S90E000"},
		{-90, -180, "S90W180"},
		{89.9999, 0.5, "N89E000"},
		{90, 0.5, "N89E000"}, // clamped
		{45, -180, "N45W180"},
		{45, -179.9999, "N45W180"},
		{45, 179.9999, "N45E179"},
		{45, 180, "N45E179"}, // clamped
		{1e300, -1e300, "N89W180"},
	}
	for _, c := range cases {
		k := srtmKeyOf(c.lat, c.lon)
		if got := k.name(); got != c.want {
			t.Errorf("srtmKeyOf(%v, %v).name() = %s, want %s", c.lat, c.lon, got, c.want)
		}
	}

	s := newSRTMSource(TerrainConfig{TileURL: "https://tiles.example/skadi/"}, srtmTestViewTiles)
	if got, want := s.tileURL(srtmKey{30, -98}), "https://tiles.example/skadi/N30/N30W098.hgt.gz"; got != want {
		t.Errorf("tileURL = %s, want %s", got, want)
	}
	if got, want := s.tileURL(srtmKey{-1, -50}), "https://tiles.example/skadi/S01/S01W050.hgt.gz"; got != want {
		t.Errorf("tileURL = %s, want %s", got, want)
	}
	if got := newSRTMSource(TerrainConfig{}, srtmTestViewTiles).baseURL; got != defaultTerrainTileURL {
		t.Errorf("default base = %s", got)
	}
}

func TestSRTMTileCount(t *testing.T) {
	cases := []struct {
		name string
		bb   geoBBox
		want int
	}{
		{"inside one tile", geoBBox{30.2, -97.9, 30.8, -97.1}, 1},
		{"exactly one tile on degree lines", geoBBox{30, -98, 31, -97}, 1},
		{"N edge on a degree line", geoBBox{30.5, -97.9, 31.0, -97.1}, 1},
		{"E edge on a degree line", geoBBox{30.2, -97.5, 30.8, -97.0}, 1},
		{"crosses one latitude line", geoBBox{29.9, -98, 31, -97}, 2},
		{"crosses one longitude line", geoBBox{30.5, -98.5, 30.6, -97.5}, 2},
		{"crosses one line each way", geoBBox{29.5, -98.5, 30.5, -97.5}, 4},
		{"crosses two lines each way", geoBBox{29.5, -99.5, 31.5, -97.5}, 9},
		{"equator and prime meridian", geoBBox{-0.5, -0.5, 0.5, 0.5}, 4},
		{"point on a degree line", geoBBox{31, -97.5, 31, -97.5}, 1},
		{"point on a tile corner", geoBBox{31, -97, 31, -97}, 1},
		{"beyond the north pole clamps", geoBBox{89.5, 0.5, 95, 0.6}, 1},
		{"beyond the south pole clamps", geoBBox{-95, 0.5, -89.5, 0.6}, 1},
		{"beyond +180 clamps", geoBBox{45.2, 179.5, 45.5, 200}, 1},
		{"beyond -180 clamps", geoBBox{45.2, -200, 45.5, -179.5}, 1},
		{"whole world", geoBBox{-90, -180, 90, 180}, 180 * 360},
		{"NaN", geoBBox{math.NaN(), 0, 1, 1}, 0},
		{"Inf", geoBBox{0, 0, math.Inf(1), 1}, 0},
		{"-Inf", geoBBox{0, math.Inf(-1), 1, 1}, 0},
		{"inverted lat", geoBBox{31, -98, 30, -97}, 0},
		{"antimeridian", geoBBox{10, 179.5, 11, -179.5}, 0},
	}
	s, u, _ := newSRTMTest(t, 0, srtmServe(nil))
	for _, c := range cases {
		if got := s.TileCount(c.bb); got != c.want {
			t.Errorf("%s: TileCount = %d, want %d", c.name, got, c.want)
		}
		if c.want != 0 && c.want <= s.maxTiles {
			continue
		}
		// Boxes View refuses: never a request, never resident.
		v, err := s.View(context.Background(), c.bb)
		want := errTerrainUnavailable
		if c.want > s.maxTiles {
			want = errTerrainTooManyTiles
		}
		if !errors.Is(err, want) || v != nil {
			t.Errorf("%s: View = %v, %v; want %v", c.name, v, err, want)
		}
		if s.Resident(c.bb) {
			t.Errorf("%s: Resident = true for a box View refuses", c.name)
		}
	}
	tooMany := geoBBox{29.5, -99.5, 32.5, -96.5} // 4×4
	if got := s.TileCount(tooMany); got != 16 || got <= s.maxTiles {
		t.Fatalf("TileCount = %d, want 16 (over the cap of %d)", got, s.maxTiles)
	}
	if _, err := s.View(context.Background(), tooMany); !errors.Is(err, errTerrainTooManyTiles) {
		t.Fatalf("View(16 tiles) err = %v, want errTerrainTooManyTiles", err)
	}
	if n := u.total(); n != 0 {
		t.Fatalf("refused boxes made %d requests", n)
	}
}

// ─── Interpolation ─────────────────────────────────────────────────────────

func TestSRTMBilinear(t *testing.T) {
	k := srtmKey{30, -98}
	s, _, _ := newSRTMTest(t, 0, srtmServe(map[string][]byte{srtmPath(k): srtmGridTile(t)}))
	v := srtmMustView(t, s, srtmBox(k))
	at := func(y, x float64) (float64, float64) { return 31 - y/srtmCells, -98 + x/srtmCells }

	// Outside the pattern the field is 3y+3x and bilinear is exact.
	for _, p := range [][2]float64{{600, 600}, {879.36, 308.28}, {12.5, 1100.25}, {1199.9, 0.1}, {1200, 1200}, {1200, 0}, {0, 1200}, {1200, 600}, {360, 1200}} {
		lat, lon := at(p[0], p[1])
		srtmWantElev(t, v, lat, lon, 3*p[0]+3*p[1])
	}
	srtmWantElev(t, v, 30.2672, -97.7431, 3*((31-30.2672)*1200+(-97.7431+98)*1200))

	// Pattern: exact at grid points (row 0 = north edge, col 0 = west edge)…
	for R := 0; R < 10; R++ {
		for C := 0; C < 10; C++ {
			lat, lon := at(float64(R), float64(C))
			srtmWantElev(t, v, lat, lon, float64(srtmPatternAt(R, C)))
		}
	}
	// …the mean of the four corners at cell centres…
	for R := 0; R < 9; R++ {
		for C := 0; C < 9; C++ {
			lat, lon := at(float64(R)+0.5, float64(C)+0.5)
			want := float64(int(srtmPatternAt(R, C))+int(srtmPatternAt(R, C+1))+int(srtmPatternAt(R+1, C))+int(srtmPatternAt(R+1, C+1))) / 4
			srtmWantElev(t, v, lat, lon, want)
		}
	}
	// …and weighted off-centre.
	lat, lon := at(2.25, 7.75)
	n := 0.25*float64(srtmPatternAt(2, 7)) + 0.75*float64(srtmPatternAt(2, 8))
	sth := 0.25*float64(srtmPatternAt(3, 7)) + 0.75*float64(srtmPatternAt(3, 8))
	srtmWantElev(t, v, lat, lon, 0.75*n+0.25*sth)

	// Outside the view's tiles.
	for _, p := range [][2]float64{{31.0001, -97.5}, {29.9999, -97.5}, {30.5, -98.0001}, {30.5, -96.9999},
		{math.NaN(), -97.5}, {30.5, math.NaN()}, {math.Inf(1), -97.5}, {30.5, math.Inf(-1)}} {
		if e, ok := v.ElevationAt(p[0], p[1]); ok {
			t.Errorf("ElevationAt(%v, %v) = %v, ok=true outside the view", p[0], p[1], e)
		}
	}
}

func TestSRTMTileBoundaryInterpolation(t *testing.T) {
	tiles := map[string][]byte{}
	for _, k := range []srtmKey{{30, -98}, {30, -97}, {31, -98}, {31, -97}} {
		tiles[srtmPath(k)] = srtmLinearTile(t, k)
	}
	s, u, _ := newSRTMTest(t, 0, srtmServe(tiles))
	v := srtmMustView(t, s, geoBBox{S: 30.5, W: -97.5, N: 31.5, E: -96.5})
	if u.total() != 4 {
		t.Fatalf("requests = %d, want 4", u.total())
	}
	// Across the latitude line, the longitude line and the four-tile corner
	// the field stays exact: each side reads its own tile, the edges agree.
	for _, d := range []float64{-0.01, -1.0 / 1200, -1e-4, -1e-9, 0, 1e-9, 1e-4, 1.0 / 1200, 0.01} {
		srtmWantElev(t, v, 31+d, -97.3, srtmLinearWant(31+d, -97.3))
		srtmWantElev(t, v, 30.6, -97+d, srtmLinearWant(30.6, -97+d))
		srtmWantElev(t, v, 31+d, -97+d, srtmLinearWant(31+d, -97+d))
		srtmWantElev(t, v, 31+d, -97-d, srtmLinearWant(31+d, -97-d))
	}
	// The view's own outer edges clamp onto its last tiles.
	srtmWantElev(t, v, 32, -97.5, srtmLinearWant(32, -97.5))
	srtmWantElev(t, v, 30.5, -96, srtmLinearWant(30.5, -96))
	srtmWantElev(t, v, 30, -98, srtmLinearWant(30, -98))
}

// With edges that disagree, a point exactly on a shared edge reads the
// neighbouring tile when the view has it, and clamps to its own tile's edge
// when it does not.
func TestSRTMBoundaryOwnership(t *testing.T) {
	a, north, east := srtmKey{30, -98}, srtmKey{31, -98}, srtmKey{30, -97}
	s, _, _ := newSRTMTest(t, 0, srtmServe(map[string][]byte{
		srtmPath(a):     srtmConstTile(t, 100, srtmRawBytes, gzip.BestSpeed),
		srtmPath(north): srtmConstTile(t, 200, srtmRawBytes, gzip.BestSpeed),
		srtmPath(east):  srtmConstTile(t, 300, srtmRawBytes, gzip.BestSpeed),
	}))
	both := srtmMustView(t, s, geoBBox{S: 30.5, W: -97.5, N: 31.5, E: -97.5})
	srtmWantElev(t, both, 31, -97.5, 200)
	srtmWantElev(t, both, 30.9999, -97.5, 100)
	alone := srtmMustView(t, s, geoBBox{S: 30.5, W: -97.5, N: 31, E: -97.5})
	srtmWantElev(t, alone, 31, -97.5, 100)
	if _, ok := alone.ElevationAt(31.0001, -97.5); ok {
		t.Fatal("point north of a one-tile view reported ok")
	}
	both = srtmMustView(t, s, geoBBox{S: 30.5, W: -97.5, N: 30.5, E: -96.5})
	srtmWantElev(t, both, 30.5, -97, 300)
	alone = srtmMustView(t, s, geoBBox{S: 30.5, W: -97.5, N: 30.5, E: -97})
	srtmWantElev(t, alone, 30.5, -97, 100)
}

// ─── Decode ────────────────────────────────────────────────────────────────

func TestSRTMFillVoids(t *testing.T) {
	const V = srtmVoid
	cases := []struct{ in, want []int16 }{
		{[]int16{1, 2, 3}, []int16{1, 2, 3}},
		{[]int16{V, V, V}, []int16{0, 0, 0}},
		{[]int16{V, V, 7, 8}, []int16{7, 7, 7, 8}},
		{[]int16{7, 8, V, V}, []int16{7, 8, 8, 8}},
		{[]int16{1, V, 3}, []int16{1, 1, 3}},             // tie goes west
		{[]int16{1, V, V, 4}, []int16{1, 1, 4, 4}},       // each to its nearest
		{[]int16{1, V, V, V, 5}, []int16{1, 1, 1, 5, 5}}, // middle tie goes west
		{[]int16{-5, V, V, V, V, 9}, []int16{-5, -5, -5, 9, 9, 9}},
		{[]int16{V}, []int16{0}},
		{[]int16{}, []int16{}},
	}
	for _, c := range cases {
		got := append([]int16(nil), c.in...)
		srtmFillVoids(got)
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("srtmFillVoids(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func srtmVoidTileValue(r, c int) int16 {
	const V = srtmVoid
	switch {
	case r == 0: // kept row: value = col, voids at kept cols 3, 6, 30, 33
		if c == 3 || c == 6 || (c >= 30 && c <= 35) {
			return V
		}
		return int16(c)
	case r == 1, r == 3, r >= 1800: // row 1 is dropped; 3 and ≥1800 are kept, all void
		return V
	case r == 6: // leading and trailing void runs
		switch {
		case c <= 8 || c >= 3590:
			return V
		case c == 9:
			return 777
		case c == 3589:
			return 888
		}
	}
	return 500
}

func TestSRTMDecodeVoids(t *testing.T) {
	body := srtmTileFunc(t, "voids", srtmVoidTileValue)
	grid, voids, err := srtmDecode(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	at := func(R, C int) int16 { return grid[R*srtmSide+C] }
	checks := []struct {
		R, C int
		want int16
	}{
		{0, 0, 0}, {0, 1, 2}, {0, 2, 5}, {0, 10, 29}, {0, 11, 36}, {0, 12, 36}, {0, 1200, 3600},
		{1, 0, 0}, {1, 600, 0}, {1, 1200, 0},
		{2, 0, 777}, {2, 1, 777}, {2, 2, 777}, {2, 3, 777}, {2, 4, 500},
		{2, 1196, 500}, {2, 1197, 888}, {2, 1200, 888},
		{5, 5, 500}, {599, 1200, 500}, {600, 0, 0}, {1200, 1200, 0},
	}
	for _, c := range checks {
		if got := at(c.R, c.C); got != c.want {
			t.Errorf("grid[%d][%d] = %d, want %d", c.R, c.C, got, c.want)
		}
	}
	if want := 4 + srtmSide + 7 + 601*srtmSide; voids != want {
		t.Errorf("voids = %d, want %d", voids, want)
	}

	// Through the source: the >20 % void tile is logged, not rejected.
	var logs srtmLogBuf
	prev := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(prev)
	k := srtmKey{30, -98}
	s, _, _ := newSRTMTest(t, 0, srtmServe(map[string][]byte{srtmPath(k): body}))
	v := srtmMustView(t, s, srtmBox(k))
	srtmWantElev(t, v, 31, -98+1.0/srtmCells, 2)
	srtmWantElev(t, v, 30, -97.5, 0)
	if !strings.Contains(logs.String(), "N30W098 is 50% void") {
		t.Errorf("void warning missing from log: %q", logs.String())
	}
}

func TestSRTMDecompressionBomb(t *testing.T) {
	var streamed atomic.Int64
	s, u, _ := newSRTMTest(t, 0, func(w http.ResponseWriter, r *http.Request) {
		zw, _ := gzip.NewWriterLevel(w, gzip.BestCompression)
		zeros := make([]byte, 1<<20)
		for i := 0; i < 1024; i++ { // 1 GiB of zeros, compressed on the fly
			if r.Context().Err() != nil {
				return
			}
			if _, err := zw.Write(zeros); err != nil {
				return
			}
			streamed.Add(int64(len(zeros)))
		}
		zw.Close()
	})
	k := srtmKey{30, -98}

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	var peak atomic.Uint64
	stop, sampled := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(sampled)
		tick := time.NewTicker(2 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				if m.HeapAlloc > peak.Load() {
					peak.Store(m.HeapAlloc)
				}
			}
		}
	}()
	start := time.Now()
	v, err := s.View(context.Background(), srtmBox(k))
	elapsed := time.Since(start)
	close(stop)
	<-sampled
	runtime.ReadMemStats(&after)

	if err != nil {
		t.Fatalf("View err = %v, want the bomb read as a missing tile", err)
	}
	alloc := after.TotalAlloc - before.TotalAlloc
	growth := int64(peak.Load()) - int64(before.HeapAlloc)
	t.Logf("bomb aborted in %s: total allocated %.1f MiB, peak heap growth %.1f MiB (server had produced %d MiB of the 1024 MiB stream)",
		elapsed.Round(time.Millisecond), float64(alloc)/(1<<20), float64(growth)/(1<<20), streamed.Load()>>20)
	// Everything allocated in the process (source and test server) during
	// the fetch is below even one raw tile, let alone 1 GiB.
	if alloc > 24<<20 {
		t.Errorf("allocated %d bytes during the bomb fetch", alloc)
	}
	if srtmCacheLen(s) != 0 || srtmNegLen(s) != 1 || s.cooling() {
		t.Error("the bomb was cached as data, or not set aside, or paused every download")
	}
	if m, n := v.(terrainCoverage).MissingTiles(); m != 1 || n != 1 {
		t.Errorf("MissingTiles = %d of %d, want 1 of 1", m, n)
	}
	if n := u.total(); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
}

// An unusable body is about that one tile: it has no elevation for
// srtmBadTTL, is never cached as data, and does not pause other downloads.
func TestSRTMBadBodiesReadAsMissing(t *testing.T) {
	valid := srtmConstTile(t, 0, srtmRawBytes, gzip.BestSpeed)
	badCRC := append([]byte(nil), valid...)
	badCRC[len(badCRC)-8] ^= 0xff
	stored := srtmConstTile(t, 0, srtmRawBytes, gzip.NoCompression) // valid, but over the cap
	cases := []struct {
		name          string
		body          []byte
		contentLength bool
		ok            bool
	}{
		{"valid control", valid, true, true},
		{"valid control chunked", valid, false, true},
		{"truncated", valid[:len(valid)/2], true, false},
		{"truncated trailer", valid[:len(valid)-4], true, false},
		{"two bytes short", srtmConstTile(t, 0, srtmRawBytes-2, gzip.BestSpeed), true, false},
		{"two bytes long", srtmConstTile(t, 0, srtmRawBytes+2, gzip.BestSpeed), true, false},
		{"bad CRC", badCRC, true, false},
		{"trailing garbage", append(append([]byte(nil), valid...), "garbage!"...), true, false},
		{"HTML", []byte("<!doctype html><html><body>Not here</body></html>"), true, false},
		{"empty", nil, true, false},
		{"over the cap, chunked", stored, false, false},
		{"over the cap, Content-Length", stored, true, false},
	}
	if len(stored) <= srtmMaxGzipBytes {
		t.Fatalf("stored tile is only %d bytes", len(stored))
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, u, clk := newSRTMTest(t, 0, func(w http.ResponseWriter, r *http.Request) {
				if c.contentLength {
					w.Header().Set("Content-Length", strconv.Itoa(len(c.body)))
				}
				w.Header().Set("Content-Type", "application/x-gzip")
				w.WriteHeader(http.StatusOK)
				for b := c.body; len(b) > 0; { // in pieces, so chunked bodies stream
					n := min(len(b), 64<<10)
					if _, err := w.Write(b[:n]); err != nil {
						return
					}
					b = b[n:]
				}
			})
			k := srtmKey{30, -98}
			v, err := s.View(context.Background(), srtmBox(k))
			if err != nil {
				t.Fatalf("View err = %v", err)
			}
			missing, _ := v.(terrainCoverage).MissingTiles()
			if c.ok {
				if missing != 0 || srtmCacheLen(s) != 1 {
					t.Fatal("valid tile not cached")
				}
				return
			}
			if missing != 1 || !v.(terrainCoverage).MissingAt(30.5, -97.5) {
				t.Fatalf("bad tile not reported missing (%d)", missing)
			}
			srtmWantNoElev(t, v, 30.5, -97.5)
			if srtmCacheLen(s) != 0 || srtmNegLen(s) != 1 {
				t.Fatal("a bad tile was cached as data, or not set aside")
			}
			if s.cooling() {
				t.Fatal("one bad tile paused every download")
			}
			clk.add(srtmBadTTL - time.Second)
			srtmMustView(t, s, srtmBox(k))
			if n := u.total(); n != 1 {
				t.Fatalf("requests = %d within %s, want 1", n, srtmBadTTL)
			}
			clk.add(time.Second)
			if s.Resident(srtmBox(k)) {
				t.Fatalf("bad tile still set aside after %s", srtmBadTTL)
			}
			s.View(context.Background(), srtmBox(k))
			if n := u.total(); n != 2 {
				t.Fatalf("requests = %d, want a retry after %s", n, srtmBadTTL)
			}
		})
	}
}

type srtmFakeDoer func(*http.Request) (*http.Response, error)

func (f srtmFakeDoer) Do(r *http.Request) (*http.Response, error) { return f(r) }

type srtmCountingBody struct {
	reads  atomic.Int64
	closed atomic.Bool
}

func (b *srtmCountingBody) Read(p []byte) (int, error) {
	b.reads.Add(1)
	clear(p)
	return len(p), nil // an endless body
}

func (b *srtmCountingBody) Close() error { b.closed.Store(true); return nil }

// A declared length over the cap is refused before a single body byte is read.
func TestSRTMContentLengthOverCapNotRead(t *testing.T) {
	s, _, _ := newSRTMTest(t, 0, srtmServe(nil))
	body := &srtmCountingBody{}
	s.client = srtmFakeDoer(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, ContentLength: srtmMaxGzipBytes + 1, Body: body, Request: r}, nil
	})
	bb := srtmBox(srtmKey{30, -98})
	v, err := s.View(context.Background(), bb)
	if err != nil {
		t.Fatalf("View err = %v", err)
	}
	if n := body.reads.Load(); n != 0 || !body.closed.Load() {
		t.Fatalf("body read %d times, closed=%v; want unread and closed", n, body.closed.Load())
	}
	if m, _ := v.(terrainCoverage).MissingTiles(); m != 1 || srtmCacheLen(s) != 0 || s.cooling() {
		t.Fatal("oversized tile cached as data, not reported missing, or paused downloads")
	}
}

// ─── Negative cache, cooldown, budget ──────────────────────────────────────

func TestSRTMNegativeCache(t *testing.T) {
	a, b := srtmKey{30, -98}, srtmKey{31, -98}
	s, u, clk := newSRTMTest(t, 0, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == srtmPath(b) {
			w.WriteHeader(http.StatusForbidden) // S3 says 403 for absent keys
			return
		}
		http.NotFound(w, r)
	})
	bb := geoBBox{S: 30.5, W: -97.5, N: 31.5, E: -97.5}
	v := srtmMustView(t, s, bb)
	srtmWantNoElev(t, v, 30.5, -97.5)
	srtmWantNoElev(t, v, 31.5, -97.5)
	if !s.Resident(bb) {
		t.Fatal("404/403 tiles not resident")
	}
	if m, n := v.(terrainCoverage).MissingTiles(); m != 2 || n != 2 {
		t.Fatalf("MissingTiles = %d of %d, want 2 of 2", m, n)
	}
	srtmMustView(t, s, bb)
	// 403 (also what a misconfigured bucket says) is believed for an hour,
	// 404 for a day.
	clk.add(srtmForbiddenTTL - time.Second)
	srtmMustView(t, s, bb)
	if ha, hb := u.hitsFor(srtmPath(a)), u.hitsFor(srtmPath(b)); ha != 1 || hb != 1 {
		t.Fatalf("hits = %d, %d; want one request per tile within an hour", ha, hb)
	}
	clk.add(time.Second)
	srtmMustView(t, s, bb)
	if ha, hb := u.hitsFor(srtmPath(a)), u.hitsFor(srtmPath(b)); ha != 1 || hb != 2 {
		t.Fatalf("hits = %d, %d; want only the 403 tile refetched after an hour", ha, hb)
	}
	clk.add(srtmNegTTL - srtmForbiddenTTL - time.Second)
	if !s.Resident(srtmBox(a)) {
		t.Fatal("404 entry expired early")
	}
	clk.add(time.Second)
	if s.Resident(srtmBox(a)) {
		t.Fatal("404 entry outlived 24 h")
	}
	srtmMustView(t, s, bb)
	if ha := u.hitsFor(srtmPath(a)); ha != 2 {
		t.Fatalf("404 hits = %d; want a refetch after 24 h", ha)
	}
}

func TestSRTMNegativeCacheBounded(t *testing.T) {
	s, _, clk := newSRTMTest(t, 0, srtmServe(nil))
	for i := 0; i < srtmNegMax+100; i++ {
		s.putMissing(srtmKey{-60 + i/300, -150 + i%300}, srtmNegTTL)
		clk.add(time.Second)
	}
	if n := srtmNegLen(s); n != srtmNegMax {
		t.Fatalf("negative cache holds %d, want %d", n, srtmNegMax)
	}
	s.mu.Lock()
	_, first := s.neg[srtmKey{-60, -150}]
	_, last := s.neg[srtmKey{-60 + (srtmNegMax+99)/300, -150 + (srtmNegMax+99)%300}]
	s.mu.Unlock()
	if first || !last {
		t.Fatalf("eviction kept the oldest (%v) or dropped the newest (%v)", first, last)
	}
	clk.add(srtmNegTTL)
	s.putMissing(srtmKey{80, 80}, srtmNegTTL)
	if n := srtmNegLen(s); n != 1 {
		t.Fatalf("expired entries not purged when full: %d left", n)
	}
}

func TestSRTMUpstreamFailuresNotNegativeCached(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusTooManyRequests, 0} {
		name := "timeout"
		if status != 0 {
			name = strconv.Itoa(status)
		}
		t.Run(name, func(t *testing.T) {
			s, u, clk := newSRTMTest(t, 0, func(w http.ResponseWriter, r *http.Request) {
				if status == 0 {
					select {
					case <-r.Context().Done():
					case <-time.After(10 * time.Second):
					}
					return
				}
				w.WriteHeader(status)
			})
			if status == 0 {
				c := newSRTMHTTPClient()
				c.Timeout = 200 * time.Millisecond
				s.client = c
			}
			bb := srtmBox(srtmKey{30, -98})
			if _, err := s.View(context.Background(), bb); !errors.Is(err, errTerrainUnavailable) {
				t.Fatalf("first View err = %v, want errTerrainUnavailable", err)
			}
			if _, err := s.View(context.Background(), bb); !errors.Is(err, errTerrainBusy) {
				t.Fatalf("View in cooldown err = %v, want errTerrainBusy", err)
			}
			if s.Resident(bb) || srtmNegLen(s) != 0 {
				t.Fatal("failure was negatively cached")
			}
			clk.add(time.Minute)
			s.View(context.Background(), bb)
			if n := u.total(); n != 2 {
				t.Fatalf("requests = %d, want 2", n)
			}
		})
	}
}

func TestSRTMCooldownLadder(t *testing.T) {
	good := srtmKey{40, 10}
	goodTile := srtmConstTile(t, 42, srtmRawBytes, gzip.BestSpeed)
	s, u, clk := newSRTMTest(t, 0, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == srtmPath(good) {
			w.Write(goodTile)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	})
	bad, bad2 := srtmBox(srtmKey{30, -98}), srtmBox(srtmKey{31, -98})
	view := func(bb geoBBox) error { _, err := s.View(context.Background(), bb); return err }

	if err := view(bad); !errors.Is(err, errTerrainUnavailable) {
		t.Fatalf("err = %v", err)
	}
	ladder := []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, time.Hour, time.Hour}
	for i, d := range ladder {
		hits := u.total()
		clk.add(d - time.Second)
		if err := view(bad); !errors.Is(err, errTerrainBusy) {
			t.Fatalf("step %d: %s into a %s cooldown err = %v, want errTerrainBusy", i, d-time.Second, d, err)
		}
		if err := view(bad2); !errors.Is(err, errTerrainBusy) {
			t.Fatalf("step %d: other tile err = %v, want errTerrainBusy", i, err)
		}
		if u.total() != hits {
			t.Fatalf("step %d: request made during cooldown", i)
		}
		clk.add(time.Second)
		if err := view(bad); !errors.Is(err, errTerrainUnavailable) {
			t.Fatalf("step %d: err = %v after the cooldown", i, err)
		}
		if u.total() != hits+1 {
			t.Fatalf("step %d: requests = %d, want %d", i, u.total(), hits+1)
		}
	}
	// Success resets the ladder; cached tiles keep working in a cooldown.
	clk.add(time.Hour)
	v := srtmMustView(t, s, srtmBox(good))
	srtmWantElev(t, v, 40.5, 10.5, 42)
	if err := view(bad2); !errors.Is(err, errTerrainUnavailable) {
		t.Fatalf("err = %v", err)
	}
	srtmMustView(t, s, srtmBox(good))
	clk.add(time.Minute - time.Second)
	if err := view(bad2); !errors.Is(err, errTerrainBusy) {
		t.Fatalf("err = %v, want errTerrainBusy", err)
	}
	clk.add(time.Second)
	hits := u.total()
	if err := view(bad2); !errors.Is(err, errTerrainUnavailable) || u.total() != hits+1 {
		t.Fatalf("ladder did not reset to 60 s: err = %v", err)
	}
}

func TestSRTMBudget(t *testing.T) {
	s, u, clk := newSRTMTest(t, 0, srtmServe(nil)) // every tile 404s: cheap, no cooldown
	key := func(i int) geoBBox { return srtmBox(srtmKey{-60 + i/300, -150 + i%300}) }
	view := func(i int) error { _, err := s.View(context.Background(), key(i)); return err }

	next := 0
	for m := 0; m < srtmPerHour/srtmPerMinute; m++ {
		for i := 0; i < srtmPerMinute; i++ {
			if err := view(next); err != nil {
				t.Fatalf("minute %d, download %d: %v", m, i, err)
			}
			next++
		}
		if err := view(next); !errors.Is(err, errTerrainBusy) {
			t.Fatalf("minute %d: download %d err = %v, want errTerrainBusy", m, srtmPerMinute+1, err)
		}
		if m == 0 {
			clk.add(59 * time.Second)
			if err := view(next); !errors.Is(err, errTerrainBusy) {
				t.Fatalf("59 s later err = %v, want errTerrainBusy", err)
			}
			clk.add(time.Second)
			continue
		}
		clk.add(time.Minute)
	}
	if u.total() != srtmPerHour {
		t.Fatalf("requests = %d, want %d", u.total(), srtmPerHour)
	}
	// 10 minutes in: the minute window is empty but the hour is spent.
	if err := view(next); !errors.Is(err, errTerrainBusy) {
		t.Fatalf("hour budget err = %v, want errTerrainBusy", err)
	}
	clk.add(50*time.Minute - time.Second) // 59:59 after the first downloads
	if err := view(next); !errors.Is(err, errTerrainBusy) {
		t.Fatalf("at 59:59 err = %v, want errTerrainBusy", err)
	}
	clk.add(time.Second)
	if err := view(next); err != nil {
		t.Fatalf("at 60:00 err = %v", err)
	}
	if u.total() != srtmPerHour+1 {
		t.Fatalf("requests = %d, want %d", u.total(), srtmPerHour+1)
	}
}

// ─── Concurrency ───────────────────────────────────────────────────────────

func TestSRTMSingleflight(t *testing.T) {
	k := srtmKey{30, -98}
	tile := srtmGridTile(t)
	gate := newSRTMGate()
	s, u, _ := newSRTMTest(t, 0, func(w http.ResponseWriter, r *http.Request) {
		if gate.wait(r) {
			w.Write(tile)
		}
	})
	t.Cleanup(gate.open)
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := s.View(context.Background(), srtmBox(k))
			if err != nil {
				errs <- err
				return
			}
			if e, ok := v.ElevationAt(30.5, -97.5); !ok || e != 3600 {
				errs <- fmt.Errorf("elevation %v %v", e, ok)
			}
		}()
	}
	srtmEventually(t, "the first request", func() bool { return u.total() >= 1 })
	time.Sleep(100 * time.Millisecond) // let the other nine join the flight
	gate.open()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if n := u.total(); n != 1 {
		t.Fatalf("requests = %d, want 1", n)
	}
}

func TestSRTMViewContextDetached(t *testing.T) {
	k := srtmKey{30, -98}
	tile := srtmGridTile(t)
	gate := newSRTMGate()
	s, u, _ := newSRTMTest(t, 0, func(w http.ResponseWriter, r *http.Request) {
		if gate.wait(r) {
			w.Write(tile)
		}
	})
	t.Cleanup(gate.open)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := s.View(ctx, srtmBox(k)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("View err = %v, want the caller's deadline", err)
	}
	gate.open()
	srtmEventually(t, "the abandoned fetch to be cached", func() bool { return s.Resident(srtmBox(k)) })
	v := srtmMustView(t, s, srtmBox(k))
	srtmWantElev(t, v, 30.5, -97.5, 3600)
	if n := u.total(); n != 1 {
		t.Fatalf("requests = %d, want 1", n)
	}
}

func TestSRTMConcurrencyCap(t *testing.T) {
	tile := srtmConstTile(t, 7, srtmRawBytes, gzip.BestSpeed)
	var inflight, peak atomic.Int32
	gate := newSRTMGate()
	s, u, _ := newSRTMTest(t, 0, func(w http.ResponseWriter, r *http.Request) {
		n := inflight.Add(1)
		defer inflight.Add(-1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		if gate.wait(r) {
			w.Write(tile)
		}
	})
	t.Cleanup(gate.open)
	done := make(chan error, 1)
	go func() {
		_, err := s.View(context.Background(), geoBBox{S: 30.5, W: -97.5, N: 31.5, E: -96.5}) // 4 tiles
		done <- err
	}()
	srtmEventually(t, "two fetches in flight", func() bool { return inflight.Load() == 2 })
	time.Sleep(200 * time.Millisecond)
	if n := inflight.Load(); n != 2 {
		t.Fatalf("%d fetches in flight, want %d", n, srtmMaxConcurrent)
	}
	gate.open()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if u.total() != 4 || peak.Load() != srtmMaxConcurrent {
		t.Fatalf("requests = %d, peak concurrency = %d", u.total(), peak.Load())
	}
}

func TestSRTMLRUEviction(t *testing.T) {
	tiles := map[string][]byte{}
	key := func(i int) srtmKey { return srtmKey{40, i} }
	for i := 0; i < 7; i++ {
		tiles[srtmPath(key(i))] = srtmConstTile(t, int16(1000+i), srtmRawBytes, gzip.BestSpeed)
	}
	s, u, _ := newSRTMTest(t, 4, srtmServe(tiles))
	// LRU mechanics at a small size, below the one-view floor newSRTMSource
	// applies to a configured cache (TestPlannerTileCacheHoldsOneView).
	s.capacity = 4
	views := make([]terrainView, 7)
	for i := 0; i < 4; i++ {
		views[i] = srtmMustView(t, s, srtmBox(key(i)))
	}
	srtmMustView(t, s, srtmBox(key(0))) // touch: 1 is now least recently used
	views[4] = srtmMustView(t, s, srtmBox(key(4)))
	if s.Resident(srtmBox(key(1))) || !s.Resident(srtmBox(key(0))) {
		t.Fatal("eviction did not drop the least recently used tile")
	}
	for i := 5; i < 7; i++ {
		views[i] = srtmMustView(t, s, srtmBox(key(i)))
		if n := srtmCacheLen(s); n != 4 {
			t.Fatalf("cache holds %d tiles, want 4", n)
		}
	}
	// Views keep reading their own tiles after eviction.
	for i, v := range views {
		if v != nil {
			srtmWantElev(t, v, 40.5, float64(i)+0.5, float64(1000+i))
		}
	}
	if n := u.total(); n != 7 {
		t.Fatalf("requests = %d, want 7", n)
	}
}

// ─── HTTP client ───────────────────────────────────────────────────────────

func TestSRTMNoRedirect(t *testing.T) {
	k := srtmKey{30, -98}
	tile := srtmGridTile(t)
	s, u, _ := newSRTMTest(t, 0, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == srtmPath(k) {
			http.Redirect(w, r, "/elsewhere/N30W098.hgt.gz", http.StatusFound)
			return
		}
		w.Write(tile)
	})
	v := srtmMustView(t, s, srtmBox(k))
	if n := u.hitsFor("/elsewhere/N30W098.hgt.gz"); n != 0 {
		t.Fatalf("redirect followed (%d requests)", n)
	}
	if m, _ := v.(terrainCoverage).MissingTiles(); m != 1 || srtmCacheLen(s) != 0 || s.cooling() {
		t.Fatal("redirect cached as data, not reported missing, or paused downloads")
	}
}

// ─── Missing terrain ───────────────────────────────────────────────────────

func TestSRTMViewMissingTiles(t *testing.T) {
	good := srtmKey{30, -98} // its northern neighbour 404s
	s, _, _ := newSRTMTest(t, 0, srtmServe(map[string][]byte{
		srtmPath(good): srtmConstTile(t, 250, srtmRawBytes, gzip.BestSpeed),
	}))
	v := srtmMustView(t, s, geoBBox{S: 30.5, W: -97.5, N: 31.5, E: -97.5})
	tc, ok := v.(terrainCoverage)
	if !ok {
		t.Fatalf("%T does not implement terrainCoverage", v)
	}
	if m, n := tc.MissingTiles(); m != 1 || n != 2 {
		t.Fatalf("MissingTiles = %d of %d, want 1 of 2", m, n)
	}
	for _, c := range []struct {
		lat, lon float64
		want     bool
	}{
		{30.5, -97.5, false},      // good tile
		{31.5, -97.5, true},       // 404 tile
		{30.999, -97.5, false},    // just south of the boundary
		{31.0, -97.5, true},       // a boundary point belongs to the tile north of it
		{32.0, -97.5, true},       // the view's north edge clamps onto the 404 tile
		{29.9, -97.5, true},       // outside the view: no data either
		{math.NaN(), -97.5, true}, // nonsense
	} {
		if got := tc.MissingAt(c.lat, c.lon); got != c.want {
			t.Errorf("MissingAt(%v, %v) = %v, want %v", c.lat, c.lon, got, c.want)
		}
		// No data is no elevation: never 0 m, which would read as sea level.
		if e, ok := v.ElevationAt(c.lat, c.lon); ok == c.want {
			t.Errorf("ElevationAt(%v, %v) = %v, ok=%v; want ok=%v", c.lat, c.lon, e, ok, !c.want)
		}
	}
	srtmWantElev(t, v, 30.5, -97.5, 250)
	srtmWantElev(t, v, 30.999, -97.5, 250)
	srtmWantNoElev(t, v, 31.5, -97.5)
}

// A tile with no valid sample would decode to 0 m everywhere: it is reported
// missing, has no elevation, and is not held as 2.9 MB of zeros.
func TestSRTMAllVoidTileIsMissing(t *testing.T) {
	k := srtmKey{30, -98}
	s, u, clk := newSRTMTest(t, 0, srtmServe(map[string][]byte{
		srtmPath(k): srtmConstTile(t, srtmVoid, srtmRawBytes, gzip.BestSpeed),
	}))
	v := srtmMustView(t, s, srtmBox(k))
	if m, _ := v.(terrainCoverage).MissingTiles(); m != 1 || srtmCacheLen(s) != 0 || srtmNegLen(s) != 1 {
		t.Fatalf("all-void tile: missing=%d cached=%d negative=%d", m, srtmCacheLen(s), srtmNegLen(s))
	}
	srtmWantNoElev(t, v, 30.5, -97.5)
	clk.add(srtmNegTTL - time.Second)
	srtmMustView(t, s, srtmBox(k))
	if n := u.total(); n != 1 {
		t.Fatalf("requests = %d, want 1 within %s", n, srtmNegTTL)
	}
}

// A body that stalls mid-download is the network's fault, not the tile's:
// it pauses downloads like any transport error and sets nothing aside.
func TestSRTMBodyTimeoutIsUpstreamFailure(t *testing.T) {
	tile := srtmConstTile(t, 7, srtmRawBytes, gzip.BestSpeed)
	s, _, _ := newSRTMTest(t, 0, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(tile)))
		w.WriteHeader(http.StatusOK)
		w.Write(tile[:len(tile)/4])
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(10 * time.Second):
		}
	})
	c := newSRTMHTTPClient()
	c.Timeout = 300 * time.Millisecond
	s.client = c
	bb := srtmBox(srtmKey{30, -98})
	if _, err := s.View(context.Background(), bb); !errors.Is(err, errTerrainUnavailable) {
		t.Fatalf("View err = %v, want errTerrainUnavailable", err)
	}
	if !s.cooling() || srtmNegLen(s) != 0 || s.Resident(bb) {
		t.Fatalf("stalled body: cooling=%v negative=%d", s.cooling(), srtmNegLen(s))
	}
}

func TestSRTMUpstreamFailedClassification(t *testing.T) {
	for _, c := range []struct {
		status int
		err    error
		want   bool
	}{
		{http.StatusTooManyRequests, errors.New("HTTP 429"), true},
		{http.StatusInternalServerError, errors.New("HTTP 500"), true},
		{http.StatusGatewayTimeout, errors.New("HTTP 504"), true},
		{0, &srtmUpstreamError{errors.New("request failed")}, true},
		{http.StatusOK, fmt.Errorf("wrapped: %w", &srtmUpstreamError{io.ErrClosedPipe}), true},
		{http.StatusOK, errors.New("tile shorter than expected"), false},
		{http.StatusFound, errors.New("HTTP 302"), false},
		{http.StatusUnauthorized, errors.New("HTTP 401"), false},
	} {
		if got := srtmUpstreamFailed(c.status, c.err); got != c.want {
			t.Errorf("srtmUpstreamFailed(%d, %v) = %v, want %v", c.status, c.err, got, c.want)
		}
	}
}

func TestSRTMUserAgent(t *testing.T) {
	prev := Version
	Version = "9.9.9-test"
	defer func() { Version = prev }()
	s, u, _ := newSRTMTest(t, 0, srtmServe(nil))
	srtmMustView(t, s, srtmBox(srtmKey{30, -98}))
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.uas) != 1 || u.uas[0] != "CoreScope/9.9.9-test" {
		t.Fatalf("User-Agent = %q", u.uas)
	}
}

func TestSRTMClientConfig(t *testing.T) {
	c := newSRTMHTTPClient()
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T", c.Transport)
	}
	if c.Timeout != 30*time.Second || tr.TLSHandshakeTimeout != 5*time.Second {
		t.Errorf("timeouts: total %s, TLS %s", c.Timeout, tr.TLSHandshakeTimeout)
	}
	if tr.Proxy != nil {
		t.Error("tile client honours proxy settings")
	}
	if !tr.DisableCompression {
		t.Error("transport may gunzip bodies behind the size cap")
	}
	if c.CheckRedirect == nil || c.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Error("redirects are followed")
	}
	if s := newSRTMSource(TerrainConfig{}, srtmTestViewTiles); s.sem != srtmFetchSlots || cap(s.sem) != 2 || s.capacity != defaultTerrainTileCache {
		t.Errorf("source: %d slots (shared %v), %d tiles", cap(s.sem), s.sem == srtmFetchSlots, s.capacity)
	}
}

// BenchmarkSRTMElevationAt: one lookup on a 3×3-tile view (the planner's
// hot loop samples millions of these per coverage job).
func BenchmarkSRTMElevationAt(b *testing.B) {
	grid := make([]int16, srtmSide*srtmSide)
	for i := range grid {
		grid[i] = int16(i % 3000)
	}
	v := &srtmView{span: srtmSpan{29, -99, 3, 3}, grids: make([][]int16, 9)}
	for i := range v.grids {
		v.grids[i] = grid
	}
	var sink float64
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f := float64(i%100000) / 100000
		e, _ := v.ElevationAt(29+3*f, -99+3*(1-f))
		sink += e
	}
	if sink < 0 {
		b.Fatal(sink)
	}
}

// ─── Fuzz ──────────────────────────────────────────────────────────────────

func FuzzSRTMPixel(f *testing.F) {
	for _, p := range [][2]float64{
		{30.2672, -97.7431}, {0, 0}, {-0.0001, 0.0001}, {31, -98}, {90, 180}, {-90, -180},
		{89.99999999999, 179.99999999999}, {-89.99999999999, -179.99999999999},
		{math.NaN(), 0}, {0, math.NaN()}, {math.Inf(1), math.Inf(-1)}, {1e308, -1e308},
		{5e-324, -5e-324}, {math.Nextafter(31, 30), math.Nextafter(-97, -98)},
		{-0.16666666666666666, 682}, // found by the fuzzer: FMA gave fy = -1.1e-14
	} {
		f.Add(p[0], p[1])
	}
	grid := make([]int16, srtmSide*srtmSide)
	for R := 0; R < srtmSide; R++ {
		for C := 0; C < srtmSide; C++ {
			grid[R*srtmSide+C] = int16(R + C)
		}
	}
	viewOf := func(sp srtmSpan) *srtmView {
		v := &srtmView{span: sp, grids: make([][]int16, sp.count())}
		for i := range v.grids {
			v.grids[i] = grid
		}
		return v
	}
	f.Fuzz(func(t *testing.T, lat, lon float64) {
		finite := !math.IsNaN(lat) && !math.IsInf(lat, 0) && !math.IsNaN(lon) && !math.IsInf(lon, 0)
		k := srtmKeyOf(lat, lon)
		if k.lat < -90 || k.lat > 89 || k.lon < -180 || k.lon > 179 || !srtmNameRE.MatchString(k.name()) {
			t.Fatalf("srtmKeyOf(%v, %v) = %+v %q", lat, lon, k, k.name())
		}
		row, col, fy, fx := srtmPixel(lat, lon, k)
		if row < 0 || row > srtmCells-1 || col < 0 || col > srtmCells-1 || !(fy >= 0 && fy <= 1) || !(fx >= 0 && fx <= 1) {
			t.Fatalf("srtmPixel(%v, %v) = %d, %d, %v, %v", lat, lon, row, col, fy, fx)
		}
		views := []*srtmView{viewOf(srtmSpan{k.lat, k.lon, 1, 1})}
		sp, ok := srtmSpanOf(geoBBox{S: lat - 1, W: lon - 1, N: lat + 1, E: lon + 1})
		if ok != finite {
			t.Fatalf("srtmSpanOf ok=%v for finite=%v", ok, finite)
		}
		if ok {
			if sp.count() < 1 || sp.count() > 9 || sp.lat0 < -90 || sp.lat0+sp.nLat > 90 || sp.lon0 < -180 || sp.lon0+sp.nLon > 180 {
				t.Fatalf("srtmSpanOf around (%v, %v) = %+v", lat, lon, sp)
			}
			views = append(views, viewOf(sp))
		}
		for _, v := range views {
			e, ok := v.ElevationAt(lat, lon)
			sp := v.span
			inside := finite && lat >= float64(sp.lat0) && lat <= float64(sp.lat0+sp.nLat) &&
				lon >= float64(sp.lon0) && lon <= float64(sp.lon0+sp.nLon)
			if ok != inside {
				t.Fatalf("ElevationAt(%v, %v) ok=%v on %+v, want %v", lat, lon, ok, sp, inside)
			}
			if ok && !(e >= 0 && e <= 2*srtmCells) {
				t.Fatalf("ElevationAt(%v, %v) = %v outside the grid's range", lat, lon, e)
			}
		}
	})
}

// ─── Tile sets (route LOS) ─────────────────────────────────────────────────

// srtmTileView reads each point from the tile srtmKeyOf names: a point on a
// degree line from the tile north or east of it, which shares that edge.
func TestSRTMTileView(t *testing.T) {
	sw, nw, se := srtmKey{30, -98}, srtmKey{31, -98}, srtmKey{30, -97}
	v := srtmTileView{
		sw: elevFieldGrid(sw, elevLinear),
		nw: elevFieldGrid(nw, elevLinear),
		se: nil, // known missing
	}
	for _, p := range [][2]float64{
		{30.5, -97.5}, {30.123, -97.987}, // inside
		{31, -97.5},     // on the 31st parallel: the north tile's south edge
		{31.999, -98},   // on the 98th meridian
		{30, -98},       // the south-west corner of the set
		{31.5, -97.001}, // just inside the north-west tile's east edge
	} {
		got, ok := v.ElevationAt(p[0], p[1])
		if !ok || math.Abs(got-elevLinear(p[0], p[1])) > 1e-6 {
			t.Errorf("ElevationAt(%v, %v) = %v, %v; want %v", p[0], p[1], got, ok, elevLinear(p[0], p[1]))
		}
	}
	for _, p := range [][2]float64{
		{30.5, -96.5}, // tile without data
		{30.5, -97},   // its west edge belongs to it, not to the tile west of it
		{32.5, -97.5}, // tile not in the set
		{29.9, -97.5},
		{math.NaN(), -97.5}, {30.5, math.NaN()}, {91, 0}, {0, -181},
	} {
		if got, ok := v.ElevationAt(p[0], p[1]); ok {
			t.Errorf("ElevationAt(%v, %v) = %v, want no terrain", p[0], p[1], got)
		}
	}
	// The poles and the antimeridian clamp onto the last tile.
	edge := srtmTileView{{89, 179}: elevFlatGrid(7)}
	if got, ok := edge.ElevationAt(90, 180); !ok || got != 7 {
		t.Errorf("ElevationAt(90, 180) = %v, %v", got, ok)
	}
}

// tiles reports what is resident and how many downloads loading the rest
// would start; await loads them detached and waits a bounded time.
func TestSRTMTilesAndAwait(t *testing.T) {
	gated, gone, here, broken := srtmKey{40, 10}, srtmKey{40, 11}, srtmKey{40, 12}, srtmKey{40, 13}
	gate := newSRTMGate()
	tile := srtmConstTile(t, 77, srtmRawBytes, gzip.BestSpeed)
	s, u, clk := newSRTMTest(t, 0, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case srtmPath(gated):
			if gate.wait(r) {
				w.Write(tile)
			}
		case srtmPath(broken):
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	})
	t.Cleanup(gate.open)
	s.put(here, elevFlatGrid(5))

	keys := []srtmKey{gated, gone, here}
	ts := s.tiles(keys)
	if len(ts.grids) != 1 || ts.grids[here] == nil || !slices.Equal(ts.need, []srtmKey{gated, gone}) || ts.starts != 2 || ts.cooling {
		t.Fatalf("first look: %d resident, need %v, starts %d, cooling %v", len(ts.grids), ts.need, ts.starts, ts.cooling)
	}
	got, failed := s.await(context.Background(), ts.need, 200*time.Millisecond)
	if g, ok := got[gone]; !ok || g != nil || len(failed) != 0 {
		t.Fatalf("404 tile: got %v (%v), failed %v", g, ok, failed)
	}
	if _, ok := got[gated]; ok {
		t.Fatal("a gated tile finished")
	}

	// Still downloading: known missing counts as resident, and loading the
	// gated tile again would start nothing.
	srtmEventually(t, "the gated download", func() bool { return u.hitsFor(srtmPath(gated)) == 1 })
	ts = s.tiles(keys)
	if len(ts.grids) != 2 || !slices.Equal(ts.need, []srtmKey{gated}) || ts.starts != 0 {
		t.Fatalf("second look: %d resident, need %v, starts %d", len(ts.grids), ts.need, ts.starts)
	}
	// A caller that went away returns at once, and the download goes on.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if got, failed := s.await(ctx, []srtmKey{gated}, time.Minute); len(got) != 0 || len(failed) != 0 || time.Since(start) > 5*time.Second {
		t.Fatalf("cancelled: got %d, failed %d after %s", len(got), len(failed), time.Since(start))
	}
	gate.open()
	got, _ = s.await(context.Background(), []srtmKey{gated}, 10*time.Second)
	if g := got[gated]; len(g) != srtmSide*srtmSide || g[0] != 77 || u.hitsFor(srtmPath(gated)) != 1 {
		t.Fatalf("gated tile: %d samples, fetched %d times", len(g), u.hitsFor(srtmPath(gated)))
	}
	if ts = s.tiles(keys); len(ts.need) != 0 || ts.starts != 0 {
		t.Fatalf("all resident: need %v", ts.need)
	}

	// A failed download is reported as failed and pauses every download.
	if _, failed := s.await(context.Background(), []srtmKey{broken}, 10*time.Second); !failed[broken] {
		t.Fatalf("503 tile not failed: %v", failed)
	}
	if ts = s.tiles([]srtmKey{broken, srtmKey{41, 10}}); !ts.cooling || ts.starts != 2 {
		t.Fatalf("after a 503: cooling %v, starts %d", ts.cooling, ts.starts)
	}
	clk.add(srtmBackoff[0])
	if ts = s.tiles([]srtmKey{broken}); ts.cooling {
		t.Fatal("cooldown did not lapse")
	}
}

// ─── Real tile server (opt-in) ─────────────────────────────────────────────

func TestSRTMRealBucketAustin(t *testing.T) {
	if os.Getenv("CORESCOPE_NET_TESTS") != "1" {
		t.Skip("set CORESCOPE_NET_TESTS=1 to fetch a real SRTM tile")
	}
	s := newSRTMSource(TerrainConfig{}, srtmTestViewTiles)
	s.sem = make(chan struct{}, srtmMaxConcurrent)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	v, err := s.View(ctx, geoBBox{S: 30.2, W: -97.8, N: 30.4, E: -97.7})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct{ lat, lon, want float64 }{{30.2672, -97.7431, 149}, {30.3207, -97.7735, 213}} {
		got, ok := v.ElevationAt(p.lat, p.lon)
		t.Logf("(%v, %v) = %.1f m", p.lat, p.lon, got)
		if !ok || math.Abs(got-p.want) > 15 {
			t.Errorf("ElevationAt(%v, %v) = %.1f, %v; want %.0f ± 15", p.lat, p.lon, got, ok, p.want)
		}
	}
}

// Real rugged tiles (Sierra Nevada, Alps) gzip to 14-17 MB. A 12 MiB cap
// refused them and, worse, paused every download for a minute. A tile in
// that range must load.
func TestSRTMRuggedTileOverOldCapLoads(t *testing.T) {
	rugged := srtmTestGzip(t, "rugged-15mb", gzip.BestSpeed, func(w io.Writer) error {
		rng := rand.New(rand.NewPCG(5, 5))
		row := make([]byte, srtmRawRowBytes)
		for r := 0; r < srtmRawSide; r++ {
			if r%4 != 3 { // 3 rows in 4 incompressible: ~15 MB of gzip
				for c := 0; c < srtmRawSide; c++ {
					binary.BigEndian.PutUint16(row[2*c:], uint16(rng.IntN(2000)))
				}
			} else {
				clear(row)
			}
			if _, err := w.Write(row); err != nil {
				return err
			}
		}
		return nil
	})
	if len(rugged) <= 12<<20 || len(rugged) >= srtmMaxGzipBytes {
		t.Fatalf("test tile is %d bytes, want between 12 MiB and the cap", len(rugged))
	}
	s, _, _ := newSRTMTest(t, 0, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(rugged)))
		w.Write(rugged)
	})
	k := srtmKey{36, -119}
	if _, err := s.View(context.Background(), srtmBox(k)); err != nil {
		t.Fatalf("View err = %v", err)
	}
	if !s.Resident(srtmBox(k)) {
		t.Fatal("rugged tile not cached")
	}
}
