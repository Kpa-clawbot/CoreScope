package main

// The leak test of the "fuzzy location" setting (location_privacy.go): with
// the fuzzer on, nothing the server sends may reveal a node's or a
// companion's exact position.
//
// What is covered
//   - Every GET route of RegisterRoutes, found by walking the router. A route
//     without an entry in leakRouteParams fails the test, so a new route cannot
//     ship unchecked. The POST routes that return data are requested too.
//   - The live feed: what the Poller broadcasts for a new transmission and for
//     a new observation, read from a hub client's queue.
//   - Oracles: what a client learns from how the answer changes with the
//     request, without any coordinate in a body.
//
// What a body is checked for (lkChecker)
//
//	text    an exact coordinate as text: 3 to 6 decimals, or the integer × 1e6.
//	        (The vertices of cell polygons: 5 and 6 decimals and × 1e6 only.)
//	rawhex  an advert's frame, its location bytes or its signature, in hex.
//	coords  a coordinate pair (lat/lon keys, or a [a, b] number array in either
//	        order) within 2 × radius of an exact position that is not a fuzzed
//	        position: exact, or some other approximation of it.
//	cell    a GeoJSON polygon so small and so close that it places an exact
//	        position within radius / 2. (Polygon features are aggregates, so
//	        their vertices are not held to the coords rule.)
//	dist    a distance that is the exact one between two positions (or between
//	        a point of the request and a position) and not the fuzzed one.
//	packet  a located ADVERT served against the contract: raw_hex not null,
//	        lat / lon / signature in decoded_json, or no location_hidden.
//
// Ratchet: knownLocationLeaks (location_privacy_leak_allowlist_test.go) lists
// the (route, check) pairs that leak today. A leak that is not listed fails,
// and so does a listed one that no longer occurs. LOCPRIV_LEAK_REPORT=1
// prints the current set in the allowlist's format; LOCPRIV_LEAK_DUMP=1 logs
// every request's status and the start of its body.
//
// Invariants the checks rely on
//   - Fixture positions have digits found nowhere else and sit over 2 × radius
//     from every public coordinate (IATA table, request points), and no
//     request names an exact coordinate.
//   - Once Fuzz moves points, newLeakFuzzer picks a secret for which a fuzzed
//     value cannot read as an exact one (see there). While Fuzz is the
//     identity stub, an exact value counts as a leak everywhere, so the
//     allowlist does not change when the real Fuzz lands.

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

// ─── Checker ────────────────────────────────────────────────────────────────

type lkLeak struct{ check, detail string }

type lkNeedle struct {
	text  string
	what  string
	micro bool // an integer: must not be part of a longer number
	// coarse: 3 or 4 decimals, a band 100 m or 10 m wide. A vertex of a
	// coverage cell can fall in it by chance, so cell polygons are exempt
	// (lkWithoutCells).
	coarse bool
}

type lkDistance struct {
	what         string
	exact, fuzzy float64 // km
}

// lkChecker holds what must not appear in a body.
type lkChecker struct {
	radiusM  float64
	points   []lkPoint
	fuzzed   []geoPoint          // parallel to points
	allowed  []geoPoint          // fuzzed positions that differ from their exact one
	coords   []lkNeedle          // exact coordinates as text
	hexes    []lkNeedle          // advert frames, location bytes, signatures (lower case)
	adverts  map[string]lkAdvert // by lower-case hash
	pairDist []lkDistance        // between two fixture positions
}

func newLKChecker(points []lkPoint, fuzz func(lkPoint) geoPoint, adverts []lkAdvert, radiusM float64) *lkChecker {
	c := &lkChecker{radiusM: radiusM, points: points, adverts: map[string]lkAdvert{}}
	seen := map[string]bool{}
	for _, p := range points {
		fz := fuzz(p)
		c.fuzzed = append(c.fuzzed, fz)
		if fz != p.geo() {
			c.allowed = append(c.allowed, fz)
		}
		for i, v := range []float64{p.lat, p.lon} {
			axis := [2]string{"lat", "lon"}[i]
			dec, micro := lkCoordNeedles(v)
			for _, n := range dec {
				if !seen[n] {
					seen[n] = true
					decimals := len(n) - strings.IndexByte(n, '.') - 1
					c.coords = append(c.coords, lkNeedle{text: n, what: p.name + " " + axis, coarse: decimals < 5})
				}
			}
			if !seen[micro] {
				seen[micro] = true
				c.coords = append(c.coords, lkNeedle{text: micro, what: p.name + " " + axis + " ×1e6", micro: true})
			}
		}
	}
	for i, a := range points {
		for j := i + 1; j < len(points); j++ {
			b := points[j]
			if a.geo() == b.geo() || (a.gps && b.gps) {
				continue // no route measures from one GPS point to another
			}
			c.pairDist = append(c.pairDist, lkDistance{
				what:  a.name + " to " + b.name,
				exact: geoDistKm(a.geo(), b.geo()), fuzzy: geoDistKm(c.fuzzed[i], c.fuzzed[j]),
			})
		}
	}
	for _, a := range adverts {
		c.adverts[strings.ToLower(a.hash)] = a
		for _, raw := range a.rawHex {
			c.hexes = append(c.hexes, lkNeedle{text: strings.ToLower(raw), what: "raw_hex of advert " + a.hash})
		}
		c.hexes = append(c.hexes,
			lkNeedle{text: strings.ToLower(a.sigHex), what: "signature of advert " + a.hash},
			lkNeedle{text: strings.ToLower(a.sigHex[:32]), what: "signature (first 16 bytes) of advert " + a.hash},
			lkNeedle{text: strings.ToLower(a.latLonHex), what: "location bytes of advert " + a.hash},
			lkNeedle{text: strings.ToLower(a.latHex), what: "lat bytes of advert " + a.hash},
			lkNeedle{text: strings.ToLower(a.lonHex), what: "lon bytes of advert " + a.hash},
		)
	}
	return c
}

func (f *lkFixture) checker() *lkChecker {
	return newLKChecker(f.points, f.fuzzed, append(append([]lkAdvert{}, f.adverts...), f.truncated...), lkRadiusM)
}

func lkIsDigit(b byte) bool { return b >= '0' && b <= '9' }

// lkHasNumber reports whether body holds needle as (the start of) a number:
// not preceded by a digit, and for an integer needle not inside a longer
// number either, so a timestamp cannot match.
func lkHasNumber(body string, n lkNeedle) bool {
	for from := 0; ; {
		i := strings.Index(body[from:], n.text)
		if i < 0 {
			return false
		}
		i += from
		end := i + len(n.text)
		ok := i == 0 || !lkIsDigit(body[i-1])
		if ok && n.micro {
			if i > 0 && body[i-1] == '.' {
				ok = false
			}
			if end < len(body) && (lkIsDigit(body[end]) || body[end] == '.') {
				ok = false
			}
		}
		if ok {
			return true
		}
		from = i + 1
	}
}

// check runs every check on one body. probes are points the request named:
// their exact distance to a fixture position must not be served either.
func (c *lkChecker) check(body []byte, probes []geoPoint) []lkLeak {
	return c.checkAs(body, probes, false)
}

// checkAs is check; with allDist every non-integer number in the body is
// held to the dist rule, for answers whose distances sit under keys that do
// not say so (min, max, avg).
func (c *lkChecker) checkAs(body []byte, probes []geoPoint, allDist bool) []lkLeak {
	var leaks []lkLeak
	s := string(body)
	coarse := lkWithoutCells(body)
	for _, n := range c.coords {
		in := s
		if n.coarse {
			in = coarse
		}
		if lkHasNumber(in, n) {
			leaks = append(leaks, lkLeak{"text", fmt.Sprintf("exact %s as %q", n.what, n.text)})
		}
	}
	lower := strings.ToLower(s)
	for _, n := range c.hexes {
		if strings.Contains(lower, n.text) {
			leaks = append(leaks, lkLeak{"rawhex", n.what})
		}
	}
	var v any
	if json.Unmarshal(body, &v) == nil {
		w := &lkWalk{c: c, dists: c.pairDist, allDist: allDist}
		for _, pr := range probes {
			for i, p := range c.points {
				w.dists = append(w.dists, lkDistance{
					what:  fmt.Sprintf("request point %v,%v to %s", pr.Lat, pr.Lon, p.name),
					exact: geoDistKm(pr, p.geo()), fuzzy: geoDistKm(pr, c.fuzzed[i]),
				})
			}
		}
		w.walk(v, "$")
		leaks = append(leaks, w.leaks...)
	}
	return leaks
}

// lkWithoutCells is body for the coarse text needles: the same JSON with the
// vertices of every GeoJSON polygon feature taken out. Everything else
// (properties, points, any other number or string) stays. A cell that is
// correct (the cell rule: not small and close enough to place a position)
// has vertices within a few km of the positions it aggregates, and one of
// them sharing three or four decimals with a position is chance, not a leak:
// 1 in 10 per axis at 3 decimals for a 1 km cell. The fine needles (5 and 6
// decimals, × 1e6) are matched on the untouched body, vertices included. A
// body that is not JSON, or holds no such polygon, is returned as it is.
func lkWithoutCells(body []byte) string {
	var v any
	if json.Unmarshal(body, &v) != nil {
		return string(body)
	}
	found := false
	var strip func(v any)
	strip = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, e := range x {
				strip(e)
			}
		case map[string]any:
			if geom, ok := x["geometry"].(map[string]any); ok && x["type"] == "Feature" {
				if gt, _ := geom["type"].(string); gt == "Polygon" || gt == "MultiPolygon" {
					if _, has := geom["coordinates"]; has {
						geom["coordinates"] = nil
						found = true
					}
				}
			}
			for _, e := range x {
				strip(e)
			}
		}
	}
	strip(v)
	if !found {
		return string(body)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return string(body)
	}
	return string(out)
}

type lkWalk struct {
	c        *lkChecker
	dists    []lkDistance
	allDist  bool
	inAdvert int // depth inside located-advert objects
	leaks    []lkLeak
}

// lkInAdvert marks a coords leak found inside a located advert: the payload
// is its source, not a node row (lkTicket).
const lkInAdvert = " [in an advert]"

func (w *lkWalk) add(check, format string, args ...any) {
	w.leaks = append(w.leaks, lkLeak{check, fmt.Sprintf(format, args...)})
}

// lkKeyTokens splits a JSON key into lower-case words: fromLat, from_lat and
// from-lat are all [from lat].
func lkKeyTokens(key string) []string {
	var out []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	rs := []rune(key)
	for i, r := range rs {
		switch {
		case r == '_' || r == '-' || r == '.' || r == ' ':
			flush()
			continue
		case r >= 'A' && r <= 'Z' && i > 0 && !(rs[i-1] >= 'A' && rs[i-1] <= 'Z'):
			flush()
		}
		cur = append(cur, r)
	}
	flush()
	return out
}

var (
	lkLatWords = map[string]bool{"lat": true, "latitude": true}
	lkLonWords = map[string]bool{"lon": true, "lng": true, "long": true, "longitude": true}
)

// lkAxisKey classifies a key as a latitude or longitude and returns what is
// left of it, so fromLat pairs with fromLon and lat with lon.
func lkAxisKey(key string) (axis, base string) {
	toks := lkKeyTokens(key)
	for i, tk := range toks {
		if lkLatWords[tk] || lkLonWords[tk] {
			axis = "lon"
			if lkLatWords[tk] {
				axis = "lat"
			}
			rest := append(append([]string{}, toks[:i]...), toks[i+1:]...)
			return axis, strings.Join(rest, "_")
		}
	}
	return "", ""
}

func lkNumbers(a []any) ([]float64, bool) {
	out := make([]float64, len(a))
	for i, v := range a {
		f, ok := v.(float64)
		if !ok {
			return nil, false
		}
		out[i] = f
	}
	return out, true
}

func (w *lkWalk) walk(v any, path string) {
	switch x := v.(type) {
	case string:
		// JSON carried as a string (decoded_json) is checked like the rest.
		if t := strings.TrimSpace(x); len(t) > 1 && (t[0] == '{' || t[0] == '[') {
			var inner any
			if json.Unmarshal([]byte(t), &inner) == nil {
				w.walk(inner, path)
			}
		}
	case []any:
		if nums, ok := lkNumbers(x); ok {
			switch len(nums) {
			case 2, 3:
				w.pair(nums[0], nums[1], path)
				w.pair(nums[1], nums[0], path)
			case 4: // a bounding box
				for _, p := range [][2]int{{0, 1}, {1, 0}, {2, 3}, {3, 2}} {
					w.pair(nums[p[0]], nums[p[1]], path)
				}
			}
			return
		}
		for i, e := range x {
			w.walk(e, fmt.Sprintf("%s[%d]", path, i))
		}
	case map[string]any:
		if w.packet(x, path) {
			w.inAdvert++
			defer func() { w.inAdvert-- }()
		}
		if geom, ok := x["geometry"].(map[string]any); ok && x["type"] == "Feature" {
			if gt, _ := geom["type"].(string); gt == "Polygon" || gt == "MultiPolygon" {
				w.cell(geom["coordinates"], path)
				return
			}
		}
		lats, lons := map[string]float64{}, map[string]float64{}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		bin := lkIsHistogramBin(x, path)
		for _, k := range keys {
			val := x[k]
			if num, ok := val.(float64); ok {
				switch axis, base := lkAxisKey(k); axis {
				case "lat":
					lats[base] = num
				case "lon":
					lons[base] = num
				}
				if bin && (k == "x" || k == "w") {
					// where a bin starts and how wide it is, not a distance
				} else if k == "avg" && lkIsTimeBucket(path) {
					// mean of the fuzzed distances in one time bucket: it can
					// equal an unrelated exact distance by chance (seen when an
					// hour boundary splits the fixture's adverts); every single
					// distance of the body is still checked
				} else if strings.Contains(strings.ToLower(k), "dist") || (w.allDist && num != math.Trunc(num)) {
					w.distance(num, path+"."+k)
				}
			}
			w.walk(val, path+"."+k)
		}
		for base, la := range lats {
			if lo, ok := lons[base]; ok {
				w.pair(la, lo, path)
			}
		}
	}
}

// lkIsHistogramBin reports whether m is one bin of a histogram (types.go
// HistogramBin): an element of a "bins" array with nothing but x, w and
// count. Its x and w are the bin's left edge and width on a grid the server
// lays between the smallest and largest value; an edge can equal an exact
// distance by chance (13.5 on a 0.5 km grid), and says nothing a count of
// values between two edges does not. The histogram's own min and max, and
// every distance elsewhere in the body, are still checked.
func lkIsHistogramBin(m map[string]any, path string) bool {
	i := strings.LastIndex(path, ".bins[")
	if i < 0 || !strings.HasSuffix(path, "]") || strings.ContainsAny(path[i+len(".bins["):len(path)-1], ".[]") {
		return false
	}
	for k := range m {
		if k != "x" && k != "w" && k != "count" {
			return false
		}
	}
	_, hasX := m["x"]
	_, hasW := m["w"]
	return hasX && hasW
}

// lkIsTimeBucket reports whether path is one element of the distance
// analytics' per-hour series (distOverTime), whose avg is a mean over many
// hops rather than the distance of one pair.
func lkIsTimeBucket(path string) bool {
	i := strings.LastIndex(path, ".distOverTime[")
	return i >= 0 && strings.HasSuffix(path, "]") && !strings.ContainsAny(path[i+len(".distOverTime["):len(path)-1], ".[]")
}

// pair applies the coords rule to one served coordinate.
func (w *lkWalk) pair(lat, lon float64, path string) {
	if math.Abs(lat) > 90 || math.Abs(lon) > 180 {
		return
	}
	got := geoPoint{Lat: lat, Lon: lon}
	for _, a := range w.c.allowed {
		if math.Abs(a.Lat-lat) <= 1e-9 && math.Abs(a.Lon-lon) <= 1e-9 {
			return
		}
	}
	for _, p := range w.c.points {
		if d := lkDistM(got, p.geo()); d <= 2*w.c.radiusM {
			src := ""
			if w.inAdvert > 0 {
				src = lkInAdvert
			}
			w.add("coords", "%s: %v,%v is %.0f m from the exact %s and not a fuzzed position%s", path, lat, lon, d, p.name, src)
			return
		}
	}
}

// cell applies the cell rule to one polygon: a leak when every vertex is
// within radius / 2 of an exact position, unless the polygon is drawn around
// a fuzzed position.
func (w *lkWalk) cell(coords any, path string) {
	var vs []geoPoint
	var collect func(v any)
	collect = func(v any) {
		a, ok := v.([]any)
		if !ok {
			return
		}
		if nums, ok := lkNumbers(a); ok && len(nums) >= 2 {
			vs = append(vs, geoPoint{Lat: nums[1], Lon: nums[0]}) // GeoJSON: lon, lat
			return
		}
		for _, e := range a {
			collect(e)
		}
	}
	collect(coords)
	if len(vs) == 0 {
		return
	}
	bb := geoBBox{S: vs[0].Lat, N: vs[0].Lat, W: vs[0].Lon, E: vs[0].Lon}
	for _, v := range vs {
		bb.S, bb.N = math.Min(bb.S, v.Lat), math.Max(bb.N, v.Lat)
		bb.W, bb.E = math.Min(bb.W, v.Lon), math.Max(bb.E, v.Lon)
	}
	for _, a := range w.c.allowed {
		if a.Lat >= bb.S && a.Lat <= bb.N && a.Lon >= bb.W && a.Lon <= bb.E {
			return
		}
	}
	for _, p := range w.c.points {
		far := 0.0
		for _, v := range vs {
			far = math.Max(far, lkDistM(v, p.geo()))
		}
		if far < w.c.radiusM/2 {
			w.add("cell", "%s: polygon places the exact %s within %.0f m", path, p.name, far)
			return
		}
	}
}

// distance applies the dist rule to one served distance, in km, m or miles.
// The tolerance is the value's own rounding, so a distance rounded so far
// that exact and fuzzed read the same is not held against the server.
func (w *lkWalk) distance(v float64, path string) {
	if v <= 0 {
		return
	}
	for _, unit := range []struct {
		name string
		toKm float64
	}{{"km", 1}, {"m", 0.001}, {"mi", 1.609344}} {
		km := v * unit.toKm
		// The coarsest decimal step v is a multiple of; unrounded values
		// are held to the finest. Whole kilometres and miles say too
		// little (and most whole numbers are counts): skipped.
		step := 0.001
		for _, s := range []float64{1, 0.1, 0.01} {
			if r := v / s; math.Abs(r-math.Round(r)) < 1e-6 {
				step = s
				break
			}
		}
		if step == 1 && unit.name != "m" {
			continue
		}
		step *= unit.toKm
		tol := step/2 + 1e-9
		for _, d := range w.dists {
			if math.Abs(km-d.exact) <= tol && (d.fuzzy == d.exact || math.Abs(km-d.fuzzy) > tol) {
				w.add("dist", "%s: %v %s is the exact distance %s (%.3f km)", path, v, unit.name, d.what, d.exact)
				return
			}
		}
	}
}

// packet applies the packet contract to an object that is a located advert
// (it carries the advert's hash and its frame or payload) and reports
// whether it is one.
func (w *lkWalk) packet(m map[string]any, path string) bool {
	hash, _ := m["hash"].(string)
	if _, ok := w.c.adverts[strings.ToLower(hash)]; !ok || hash == "" {
		return false
	}
	raw, hasRaw := m["raw_hex"]
	dj, hasDJ := m["decoded_json"]
	if !hasRaw && !hasDJ {
		return false
	}
	if hasRaw && raw != nil {
		w.add("packet", "%s: advert %s has raw_hex", path, hash)
	}
	if hasDJ {
		if s, ok := dj.(string); ok {
			var inner any
			if json.Unmarshal([]byte(s), &inner) == nil {
				dj = inner
			}
		}
		if obj, ok := dj.(map[string]any); ok {
			for _, k := range []string{"lat", "lon", "signature"} {
				if _, has := obj[k]; has {
					w.add("packet", "%s: advert %s has %s in decoded_json", path, hash, k)
				}
			}
		}
	}
	if m["location_hidden"] != true {
		w.add("packet", "%s: advert %s is not marked location_hidden", path, hash)
	}
	return true
}

// ─── Ratchet ────────────────────────────────────────────────────────────────

type lkFound map[string][]string // "<route> <check>" → details

func (lf lkFound) add(route string, leaks []lkLeak, skip map[string]string) {
	for _, l := range leaks {
		if _, ok := skip[l.check]; ok {
			continue
		}
		k := route + " " + l.check
		lf[k] = append(lf[k], l.detail)
	}
}

// What serves the companions' GPS (client_* tables): ticket B4.
var lkGPSSources = map[string]bool{
	"/api/rx-coverage": true, "/api/rx-leaderboard": true, "/api/rf-noise": true,
	"/api/nodes/reported": true, "/api/nodes/{pubkey}/rx-coverage": true,
	"oracle:rx-coverage-bbox": true,
	// mobileReceptions.maxDistanceKm: from the node to where a companion heard it.
	"/api/nodes/{pubkey}/analytics dist": true,
}

// lkTicket names the ticket that removes a leak, by its source: an advert's
// frame or payload (B5), the companions' GPS (B4), a node position (B3). A
// route that serves node rows and adverts needs both tickets for its
// coordinates to go.
func lkTicket(key string, found lkFound) string {
	i := strings.LastIndex(key, " ")
	route, check := key[:i], key[i+1:]
	switch {
	case check == "packet" || check == "rawhex" || strings.HasPrefix(route, "ws:"):
		return "B5 packets"
	case lkGPSSources[route] || lkGPSSources[key] || check == "cell":
		return "B4 companion GPS"
	}
	if _, ok := found[route+" packet"]; ok && (check == "coords" || check == "text") {
		for _, d := range found[route+" coords"] {
			if !strings.HasSuffix(d, lkInAdvert) {
				return "B3 coordinates, B5 packets"
			}
		}
		return "B5 packets" // every coordinate is in an advert's payload
	}
	return "B3 coordinates"
}

// lkRatchet compares the leaks found in one scope with knownLocationLeaks.
func lkRatchet(t *testing.T, found lkFound, inScope func(key string) bool) {
	t.Helper()
	keys := make([]string, 0, len(found))
	for k := range found {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if os.Getenv("LOCPRIV_LEAK_REPORT") != "" {
		var b strings.Builder
		for _, k := range keys {
			fmt.Fprintf(&b, "\t%q: %q,\n", k, lkTicket(k, found))
		}
		fmt.Printf("// LOCPRIV_LEAK_REPORT %s: %d leaking (route, check) pairs\n%s// end of %s\n", t.Name(), len(keys), b.String(), t.Name())
	}
	for _, k := range keys {
		if _, ok := knownLocationLeaks[k]; ok {
			continue
		}
		d := found[k]
		more := ""
		if len(d) > 3 {
			more = fmt.Sprintf(" (and %d more)", len(d)-3)
			d = d[:3]
		}
		t.Errorf("LEAK %s:\n    %s%s", k, strings.Join(d, "\n    "), more)
	}
	for k, ticket := range knownLocationLeaks {
		if !inScope(k) {
			continue
		}
		if _, ok := found[k]; !ok {
			t.Errorf("%q no longer leaks: delete it from knownLocationLeaks (%s)", k, ticket)
		}
	}
}

// ─── Routes ─────────────────────────────────────────────────────────────────

// lkRequest is one request of a route and what the fixture makes of it.
type lkRequest struct {
	method string // "" is GET
	path   string
	body   string // JSON, for a POST
	auth   bool   // as the fixture user
	status int    // 0 is 200
	// want must be in the body: proof that the fixture populated the answer,
	// so a route cannot pass by returning nothing.
	want string
	// skip lists checks that do not apply to this request, with the reason.
	skip map[string]string
	// probes are the points the request names (dist check).
	probes []geoPoint
	// allDist: every non-integer number of the answer is a distance.
	allDist bool
}

// adminOnly lists the GET routes behind requireAdmin / withAdmin (API key or
// an admin session). They are out of scope: an operator sees exact positions
// by design (backup, dropped packets, debug). The test only proves each one
// really refuses everybody else, so a public route cannot hide in this list.
var adminOnly = map[string]bool{
	"/api/admin/prune-geo-filter/status":  true,
	"/api/debug/affinity":                 true,
	"/api/dropped-packets":                true,
	"/api/backup":                         true,
	"/api/admin/users":                    true,
	"/api/admin/users/{id}":               true,
	"/api/admin/audit":                    true,
	"/api/admin/stats":                    true,
	"/api/admin/users-backup":             true,
	"/api/admin/proposals":                true,
	"/api/admin/observer-logins":          true,
	"/api/admin/notifications/push-relay": true,
}

// Request points: near enough to the fixture nodes for the planner to accept
// them (within plannerAreaNodeKm), over 2 × radius from every one.
var (
	lkProbeA = geoPoint{Lat: 37.45, Lon: -122.12}
	lkProbeB = geoPoint{Lat: 37.53, Lon: -122.29}
	lkProbeC = geoPoint{Lat: 37.39, Lon: -122.03} // the repeater lies between A and C
)

const lkAreaBBox = "37.2,-122.6,37.8,-121.7" // S,W,N,E around every fixture position

// leakRouteParams maps every GET route template to requests that make it
// answer from the fixture. A route that cannot carry a coordinate still gets
// an entry: it is cheap to call, and it keeps the list complete.
func leakRouteParams(f *lkFixture) map[string][]lkRequest {
	rep, rel, comp, obs := f.repeater, f.relay, f.companion, f.observer
	get := func(paths ...string) []lkRequest {
		out := make([]lkRequest, len(paths))
		for i, p := range paths {
			out[i] = lkRequest{path: p}
		}
		return out
	}
	want := func(want string, paths ...string) []lkRequest {
		out := get(paths...)
		for i := range out {
			out[i].want = want
		}
		return out
	}
	user := func(paths ...string) []lkRequest {
		out := get(paths...)
		for i := range out {
			out[i].auth = true
		}
		return out
	}
	pt := func(p geoPoint) string { return fmt.Sprintf("%v,%v", p.Lat, p.Lon) }
	fz := func(name string) string { return pt(f.fuzzed(f.point(name))) }
	hopRep, hopRel := rep[:6], rel[:6]
	advert := f.adverts[0]
	// The distance between two points the client sent is not the server's secret.
	ownPoints := map[string]string{"dist": "the legs join points of the request"}

	return map[string][]lkRequest{
		// Config and system.
		"/api/config/cache":              get("/api/config/cache"),
		"/api/config/client":             want(`"locationPrivacy"`, "/api/config/client"),
		"/api/config/regions":            get("/api/config/regions"),
		"/api/config/region-quick-picks": get("/api/config/region-quick-picks"),
		"/api/config/theme":              get("/api/config/theme"),
		"/api/config/map":                get("/api/config/map"),
		"/api/config/geo-filter":         want(`"bufferKm"`, "/api/config/geo-filter"),
		"/api/config/areas":              get("/api/config/areas"),
		"/api/config/areas/polygons":     get("/api/config/areas/polygons"),
		"/api/healthz":                   get("/api/healthz"),
		// Only a signature over the challenge, and 404 while no enrollment runs.
		"/api/push-relay/enrollment-proof": {{path: "/api/push-relay/enrollment-proof?challenge=" + strings.Repeat("A", 43), status: http.StatusNotFound, want: `"error"`}},
		"/api/health":                      get("/api/health"),
		"/api/stats":                       get("/api/stats"),
		"/api/scope-stats":                 get("/api/scope-stats", "/api/scope-stats?window=24h"),
		"/api/scope-audit":                 get("/api/scope-audit", "/api/scope-audit?mode=transport"),
		"/api/perf":                        get("/api/perf", "/api/perf?mem=1"),
		"/api/perf/io":                     get("/api/perf/io"),
		"/api/perf/sqlite":                 get("/api/perf/sqlite"),
		"/api/perf/write-sources":          get("/api/perf/write-sources"),
		"/api/mqtt/status":                 get("/api/mqtt/status"),
		"/api/spec":                        get("/api/spec"),
		"/api/docs":                        get("/api/docs"),
		"/api/iata-coords":                 want(`"SJC"`, "/api/iata-coords"),
		"/api/audio-lab/buckets":           get("/api/audio-lab/buckets"),
		"/api/radios":                      get("/api/radios"),
		"/api/known-channels":              get("/api/known-channels"),
		"/api/rf-params":                   get("/api/rf-params"),

		// Packets.
		"/api/packets/timestamps": get("/api/packets/timestamps?since=2020-01-01T00:00:00Z"),
		"/api/packets/{id}": want(advert.hash,
			fmt.Sprintf("/api/packets/%d", f.advertTxID), "/api/packets/"+advert.hash),
		"/api/packets": want(advert.hash,
			"/api/packets?limit=50", "/api/packets?limit=50&groupByHash=true", "/api/packets?limit=50&expand=observations",
			"/api/packets?limit=50&type=4", "/api/packets?limit=50&node="+rep, "/api/packets?limit=50&hash="+advert.hash,
			"/api/packets?limit=50&observer="+obs),
		"/api/traces/{hash}": want(obs, "/api/traces/"+advert.hash),

		// Nodes.
		"/api/nodes": want(rep,
			"/api/nodes", "/api/nodes?limit=100&sortBy=lastSeen", "/api/nodes?role=repeater", "/api/nodes?search=Leak",
			"/api/nodes?includeInactive=true&limit=500"),
		"/api/nodes/search":           want(rep, "/api/nodes/search?q=LeakRep", "/api/nodes/search?q="+rep[:8]),
		"/api/nodes/resolve":          want(rep, "/api/nodes/resolve?prefix="+hopRep, "/api/nodes/resolve?prefix="+rep[:4]),
		"/api/nodes/bulk-health":      want(rep, "/api/nodes/bulk-health", "/api/nodes/bulk-health?limit=200"),
		"/api/nodes/network-status":   get("/api/nodes/network-status"),
		"/api/nodes/clock-skew":       get("/api/nodes/clock-skew"),
		"/api/observers/clock-skew":   get("/api/observers/clock-skew"),
		"/api/nodes/{pubkey}":         want("Leak", "/api/nodes/"+rep, "/api/nodes/"+comp, "/api/nodes/"+rel, "/api/nodes/"+obs),
		"/api/nodes/{pubkey}/health":  want("Leak", "/api/nodes/"+rep+"/health", "/api/nodes/"+comp+"/health"),
		"/api/nodes/{pubkey}/paths":   want(rep, "/api/nodes/"+rep+"/paths", "/api/nodes/"+rel+"/paths"),
		"/api/nodes/{pubkey}/battery": get("/api/nodes/" + rep + "/battery"),
		"/api/nodes/{pubkey}/analytics": want("Leak",
			"/api/nodes/"+rep+"/analytics?days=7", "/api/nodes/"+comp+"/analytics?days=7"),
		"/api/nodes/{pubkey}/hop_analytics": get("/api/nodes/"+rep+"/hop_analytics", "/api/nodes/"+rel+"/hop_analytics"),
		"/api/nodes/{pubkey}/clock-skew":    get("/api/nodes/" + rep + "/clock-skew"),
		"/api/nodes/{pubkey}/neighbors": want("neighbors",
			"/api/nodes/"+rep+"/neighbors", "/api/nodes/"+rel+"/neighbors", "/api/nodes/"+comp+"/neighbors"),
		"/api/nodes/{pubkey}/reach": want("Leak",
			"/api/nodes/"+rep+"/reach?days=7", "/api/nodes/"+rel+"/reach?days=7"),

		// Companion GPS. z=18 asks for the finest grid the server has.
		"/api/nodes/{pubkey}/rx-coverage": want(`"Feature"`,
			"/api/nodes/"+rep+"/rx-coverage?bbox="+lkAreaBBox+"&z=18", "/api/nodes/"+rel+"/rx-coverage?bbox="+lkAreaBBox+"&z=12"),
		"/api/rx-coverage": append(want(`"Feature"`,
			"/api/rx-coverage?bbox="+lkAreaBBox+"&z=18", "/api/rx-coverage?bbox="+lkAreaBBox+"&z=12&days=30",
			"/api/rx-coverage?bbox="+lkAreaBBox+"&z=18&node="+rep,
			"/api/rx-coverage?bbox="+lkAreaBBox+"&z=18&node="+f.ghost),
			// Per-companion views: only its linked owner (the fixture user) or an admin.
			lkRequest{path: "/api/rx-coverage?bbox=" + lkAreaBBox + "&z=18&rx=" + comp, auth: true, want: `"Feature"`},
			lkRequest{path: "/api/rx-coverage?bbox=" + lkAreaBBox + "&z=18&rx=" + comp, status: 403, want: `"error"`},
			lkRequest{path: "/api/rx-coverage?bbox=" + lkAreaBBox + "&z=18&gaps=1", want: `"gaps":[{`},
			lkRequest{path: "/api/rx-coverage?bbox=" + lkAreaBBox + "&z=18&mine=1", auth: true, want: `"Feature"`}),
		"/api/rx-leaderboard": want(comp, "/api/rx-leaderboard", "/api/rx-leaderboard?days=30&limit=5"),
		"/api/rf-noise": append(want(`"Feature"`,
			"/api/rf-noise?bbox="+lkAreaBBox+"&z=18", "/api/rf-noise?bbox="+lkAreaBBox+"&z=12&days=30",
		),
			lkRequest{path: "/api/rf-noise?bbox=" + lkAreaBBox + "&z=18&rx=" + comp, auth: true, want: `"Feature"`},
			lkRequest{path: "/api/rf-noise?bbox=" + lkAreaBBox + "&z=18&rx=" + comp, status: 403, want: `"error"`},
			lkRequest{path: "/api/rf-noise?bbox=" + lkAreaBBox + "&z=18&mine=1", auth: true, want: `"Feature"`}),
		"/api/nodes/reported": want(`"estimate"`, "/api/nodes/reported"),

		// Analytics.
		"/api/analytics/roles":               get("/api/analytics/roles"),
		"/api/analytics/rf":                  get("/api/analytics/rf"),
		"/api/analytics/topology":            get("/api/analytics/topology"),
		"/api/analytics/channels":            get("/api/analytics/channels"),
		"/api/analytics/distance":            {{path: "/api/analytics/distance", want: "LeakRe", allDist: true}},
		"/api/analytics/hash-sizes":          get("/api/analytics/hash-sizes"),
		"/api/analytics/hash-collisions":     get("/api/analytics/hash-collisions"),
		"/api/analytics/subpaths":            get("/api/analytics/subpaths", "/api/analytics/subpaths?minLen=2&maxLen=4"),
		"/api/analytics/subpaths-bulk":       get("/api/analytics/subpaths-bulk?groups=2-2:50,3-3:50"),
		"/api/analytics/subpath-detail":      want(hopRep, "/api/analytics/subpath-detail?hops="+hopRep+","+hopRel),
		"/api/analytics/neighbor-graph":      append(get("/api/analytics/neighbor-graph"), want(rep, "/api/analytics/neighbor-graph?min_count=1&min_score=0")...),
		"/api/analytics/relay-airtime-share": get("/api/analytics/relay-airtime-share"),
		"/api/analytics/retransmissions":     get("/api/analytics/retransmissions"),

		// Hops, channels, observers.
		"/api/resolve-hops": want(rep,
			"/api/resolve-hops?hops="+hopRep+","+hopRel, "/api/resolve-hops?hops="+hopRep+","+hopRel+"&from_node="+comp+"&observer="+obs),
		"/api/channels":                  want("test", "/api/channels", "/api/channels?includeEncrypted=true"),
		"/api/channels/{hash}/messages":  want("Alice", "/api/channels/"+url.PathEscape("#test")+"/messages"),
		"/api/observers":                 want(obs, "/api/observers"),
		"/api/observers/{id}":            want("Leak", "/api/observers/"+obs, "/api/observers/"+rel),
		"/api/observers/{id}/metrics":    get("/api/observers/" + obs + "/metrics"),
		"/api/observers/{id}/analytics":  want("Leak", "/api/observers/"+obs+"/analytics?days=7", "/api/observers/"+rel+"/analytics?days=7"),
		"/api/observers/metrics/summary": get("/api/observers/metrics/summary", "/api/observers/metrics/summary?window=24h"),

		// Terrain. A client sends the positions it was served (the fuzzed
		// ones) or free points, never an exact one.
		"/api/elevation/profile": {
			{path: "/api/elevation/profile?pts=" + url.QueryEscape(fz("repeater")+","+fz("relay")), want: `"legs"`, skip: ownPoints},
		},
		// Ducting is off in the fixture (it would call the weather API), so
		// this is the 404; its node read is TestDuctingNodeCoordsFuzzed.
		"/api/ducting": {{path: "/api/ducting", status: http.StatusNotFound, want: `"error"`}},
		"/api/planner/profile": {
			{path: "/api/planner/profile?pts=" + pt(lkProbeA) + "," + pt(lkProbeB), want: `"elev_m"`, skip: ownPoints,
				probes: []geoPoint{lkProbeA, lkProbeB}},
		},
		"/api/planner/relay": {
			{path: "/api/planner/relay?a=" + pt(lkProbeA) + "&b=" + pt(lkProbeC), want: rep,
				probes: []geoPoint{lkProbeA, lkProbeC}},
			{path: "/api/planner/relay?a=" + pt(lkProbeA) + "&b=" + pt(lkProbeB), want: `"existing"`,
				probes: []geoPoint{lkProbeA, lkProbeB}},
		},
		"/api/planner/viewshed": {
			{path: "/api/planner/viewshed?site=" + pt(lkProbeA), want: `"links"`, probes: []geoPoint{lkProbeA}},
			{path: "/api/planner/viewshed?site=" + pt(lkProbeB), want: `"links"`, probes: []geoPoint{lkProbeB}},
		},
		"/api/planner/coverage": want(rep,
			"/api/planner/coverage?bbox=37.35,-122.4,37.6,-122.0", "/api/planner/coverage?bbox=37.35,-122.4,37.6,-122.0&baseline=observed&stale=1"),

		// User management, as the fixture user: the companion is linked, the
		// repeater claimed and watched.
		"/api/auth/me":                   user("/api/auth/me"),
		"/api/account/sessions":          user("/api/account/sessions"),
		"/api/account/settings":          user("/api/account/settings"),
		"/api/account/companions":        {{path: "/api/account/companions", auth: true, want: comp}},
		"/api/account/export":            {{path: "/api/account/export", auth: true, want: comp}},
		"/api/account/proposals":         user("/api/account/proposals"),
		"/api/account/observer-login":    user("/api/account/observer-login"),
		"/api/account/notifications":     {{path: "/api/account/notifications", auth: true, want: rep}},
		"/api/account/repeaters":         {{path: "/api/account/repeaters", auth: true, want: rep}},
		"/api/repeaters/info":            want(rep, "/api/repeaters/info"),
		"/api/repeaters/{pubkey}/info":   {{path: "/api/repeaters/" + rep + "/info", want: rep}, {path: "/api/repeaters/" + rep + "/info", auth: true, want: rep}},
		"/api/notifications/unsubscribe": {{path: "/api/notifications/unsubscribe?token=none", status: http.StatusSeeOther}},
	}
}

// leakBuildTagRoutes are the GET routes only the e2etest build registers
// (auth_e2e.go): the last mail sent and a user's unsubscribe link.
func leakBuildTagRoutes(f *lkFixture) map[string][]lkRequest {
	return map[string][]lkRequest{
		"/__e2e/last-mail":        {{path: "/__e2e/last-mail"}},
		"/__e2e/unsubscribe-link": {{path: "/__e2e/unsubscribe-link?email=leak@example.org"}},
	}
}

// leakPostRequests are the POST routes that answer with data.
func leakPostRequests(f *lkFixture) map[string][]lkRequest {
	hashes, _ := json.Marshal(map[string]any{"hashes": []string{f.adverts[0].hash, f.adverts[1].hash, f.msgHash}})
	inspect, _ := json.Marshal(map[string]any{"prefixes": []string{f.repeater[:6], f.relay[:6]}})
	return map[string][]lkRequest{
		"POST /api/packets/observations": {{method: "POST", path: "/api/packets/observations", body: string(hashes), want: f.adverts[0].hash}},
		"POST /api/paths/inspect":        {{method: "POST", path: "/api/paths/inspect", body: string(inspect), want: f.repeater}},
		// The client sends the frame here, so the answer tells it nothing
		// new: requested with a packet that has no location.
		"POST /api/decode": {{method: "POST", path: "/api/decode", body: `{"hex":"150012345678"}`, want: `"payload"`}},
	}
}

// lkServe sends one request and returns the body, failing on any status
// other than the expected one.
func (f *lkFixture) lkServe(t *testing.T, route string, rq lkRequest) []byte {
	t.Helper()
	method := rq.method
	if method == "" {
		method = http.MethodGet
	}
	var mods []reqMod
	if rq.auth {
		mods = append(mods, as(f.user))
	}
	var body any
	if rq.body != "" {
		body = json.RawMessage(rq.body)
	}
	w := f.serve(method, rq.path, body, mods...)
	wantStatus := rq.status
	if wantStatus == 0 {
		wantStatus = http.StatusOK
	}
	b := w.Body.Bytes()
	if os.Getenv("LOCPRIV_LEAK_DUMP") != "" {
		t.Logf("%s %s → %d, %d bytes: %.700s", method, rq.path, w.Code, len(b), b)
	}
	if w.Code != wantStatus {
		t.Errorf("%s: %s %s = %d, want %d; body: %.400s", route, method, rq.path, w.Code, wantStatus, b)
	}
	if rq.want != "" && !strings.Contains(string(b), rq.want) {
		t.Errorf("%s: %s %s is not populated from the fixture (no %q in the body): %.400s", route, method, rq.path, rq.want, b)
	}
	// A redirect carries its answer in the header.
	if loc := w.Header().Get("Location"); loc != "" {
		b = append(b, []byte("\n"+loc)...)
	}
	return b
}

// lkGETRoutes walks the router for the template of every GET route.
func lkGETRoutes(t *testing.T, r *mux.Router) []string {
	t.Helper()
	var out []string
	seen := map[string]bool{}
	err := r.Walk(func(route *mux.Route, _ *mux.Router, _ []*mux.Route) error {
		tpl, err := route.GetPathTemplate()
		if err != nil {
			return nil // a matcher without a path (the CORS preflight)
		}
		methods, err := route.GetMethods()
		isGET := err != nil // no method restriction: answers GET too
		for _, m := range methods {
			isGET = isGET || m == http.MethodGet
		}
		if isGET && !seen[tpl] {
			seen[tpl] = true
			out = append(out, tpl)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(out)
	return out
}

// TestLocationLeakRoutes requests every GET route (and the data POSTs) with
// the fuzzer on and checks each body.
//
// RegisterRoutes holds every API route; main adds only /ws and the static
// SPA handler, which serve no data of their own (the feed is
// TestLocationLeakLiveFeed).
func TestLocationLeakRoutes(t *testing.T) {
	f := newLKFixture(t)
	c := f.checker()
	params := leakRouteParams(f)
	found := lkFound{}

	routes := lkGETRoutes(t, f.router)
	registered := map[string]bool{}
	var checked, admin, requests int
	for _, tpl := range routes {
		registered[tpl] = true
		if adminOnly[tpl] {
			admin++
			continue
		}
		if _, ok := leakBuildTagRoutes(f)[tpl]; ok {
			continue // requested below
		}
		reqs, ok := params[tpl]
		if !ok || len(reqs) == 0 {
			t.Errorf("GET %s has no entry in leakRouteParams: add requests that make it answer from the fixture "+
				"(or list it in adminOnly if it is behind requireAdmin / withAdmin)", tpl)
			continue
		}
		checked++
		for _, rq := range reqs {
			requests++
			found.add(tpl, c.checkAs(f.lkServe(t, tpl, rq), rq.probes, rq.allDist), rq.skip)
		}
	}
	// Routes of the e2etest build only: requested when they are there.
	for tpl, reqs := range leakBuildTagRoutes(f) {
		if registered[tpl] {
			params[tpl] = reqs
			checked++
			for _, rq := range reqs {
				requests++
				found.add(tpl, c.check(f.lkServe(t, tpl, rq), rq.probes), rq.skip)
			}
		}
	}
	for tpl := range params {
		if !registered[tpl] {
			t.Errorf("leakRouteParams has %s, which is not a registered GET route", tpl)
		}
	}
	for tpl := range adminOnly {
		if !registered[tpl] {
			t.Errorf("adminOnly has %s, which is not a registered GET route", tpl)
		}
	}
	posts := leakPostRequests(f)
	for route, reqs := range posts {
		for _, rq := range reqs {
			requests++
			found.add(route, c.check(f.lkServe(t, route, rq), rq.probes), rq.skip)
		}
	}
	t.Logf("GET routes walked: %d; requested: %d; admin-only (excluded): %d; POST routes: %d; requests: %d",
		len(routes), checked, admin, len(posts), requests)
	lkRatchet(t, found, func(k string) bool { return strings.HasPrefix(k, "/") || strings.HasPrefix(k, "POST ") })
}

// TestLocationLeakAdminOnlyRoutesNeedAdmin: a route is exempt only if it
// turns away both an anonymous client and a logged-in user who is no admin.
func TestLocationLeakAdminOnlyRoutesNeedAdmin(t *testing.T) {
	f := newLKFixture(t)
	fill := strings.NewReplacer("{id}", "1", "{pubkey}", f.repeater)
	for tpl := range adminOnly {
		path := fill.Replace(tpl)
		for who, mods := range map[string][]reqMod{"anonymous": nil, "user": {as(f.user)}} {
			w := f.serve("GET", path, nil, mods...)
			if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
				t.Errorf("GET %s as %s = %d, want 401 or 403: it is not admin-only and belongs in leakRouteParams", path, who, w.Code)
			}
		}
	}
}

// ─── Live feed ──────────────────────────────────────────────────────────────

// lkFeed runs a real Poller on hub until every wanted hash has been broadcast
// and returns the messages per hash. Messages are read from a registered
// client's queue: what a browser would be sent, with no socket.
func lkFeed(t *testing.T, db *DB, store *PacketStore, hub *Hub, want []string) map[string][][]byte {
	t.Helper()
	cl := &Client{send: make(chan []byte, 256)}
	hub.Register(cl)
	p := NewPoller(db, hub, 2*time.Millisecond)
	p.store = store
	done := make(chan struct{})
	go func() { defer close(done); p.Start() }()
	defer func() {
		p.Stop()
		<-done
		hub.Unregister(cl)
	}()

	got := map[string][][]byte{}
	missing := func() []string {
		var m []string
		for _, h := range want {
			if len(got[h]) == 0 {
				m = append(m, h)
			}
		}
		return m
	}
	deadline := time.After(20 * time.Second)
	for len(missing()) > 0 {
		select {
		case msg := <-cl.send:
			var m struct {
				Type string `json:"type"`
				Data struct {
					Hash string `json:"hash"`
				} `json:"data"`
			}
			if err := json.Unmarshal(msg, &m); err != nil {
				t.Fatalf("feed message is not JSON: %v: %s", err, msg)
			}
			got[m.Data.Hash] = append(got[m.Data.Hash], msg)
		case <-deadline:
			t.Fatalf("the feed never carried %v", missing())
		}
	}
	return got
}

// TestLocationLeakLiveFeed checks what the Poller broadcasts: a new located
// advert, a new observation of an advert already in the store, and the
// messages of the path without a store.
func TestLocationLeakLiveFeed(t *testing.T) {
	f := newLKFixture(t)
	db := f.srv.db
	rep := f.point("repeater")
	found := lkFound{}

	// After the load: a new advert of the repeater, and one more observation
	// (a frame with another path) of its first advert.
	old := &f.adverts[0]
	f.insertAdvert(t, db, lkRepeaterKey, 2, rep, "LeakRepeater", time.Now().UTC(), []lkObs{
		{observerIdx: 1, hops: [][]byte{lkHop(f.relay)}, resolved: []string{f.relay}, snr: 9.25, rssi: -87},
		{observerIdx: 2, snr: 12.5, rssi: -77},
	})
	fresh := f.adverts[len(f.adverts)-1]
	frame := old.rawHex[0][:2] + strings.ToUpper(fmt.Sprintf("%02x", 2<<6|2)+f.relay[:6]+f.observer[:6]) + old.rawHex[0][10:]
	if pkt, err := DecodePacket(frame, true); err != nil || pkt.Payload.SignatureValid == nil || !*pkt.Payload.SignatureValid {
		t.Fatalf("observation frame is not the advert on another path: %v", err)
	}
	old.rawHex = append(old.rawHex, frame)
	if _, err := db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, direction, snr, rssi, score, path_json, timestamp, resolved_path, raw_hex)
		VALUES (?, 2, 'rx', 2.75, -106, 1, ?, ?, ?, ?)`, f.advertTxID,
		`["`+strings.ToUpper(f.relay[:6])+`","`+strings.ToUpper(f.observer[:6])+`"]`, time.Now().Unix(),
		`["`+f.relay+`","`+f.observer+`"]`, frame); err != nil {
		t.Fatal(err)
	}
	c := f.checker()

	msgs := lkFeed(t, db, f.store, f.srv.hub, []string{fresh.hash, old.hash})
	for _, m := range msgs[fresh.hash] {
		found.add("ws:packet", c.check(m, nil), nil)
	}
	for _, m := range msgs[old.hash] {
		found.add("ws:observation", c.check(m, nil), nil)
	}

	// Without a store (main always sets one; tests may not) the Poller
	// broadcasts what GetNewTransmissionsSince returns, with a copy of the
	// row nested under "packet". The same message, built here.
	rows, err := db.GetNewTransmissionsSince(f.advertTxID-1, 100)
	if err != nil {
		t.Fatal(err)
	}
	var noStore [][]byte
	for _, tx := range rows {
		pkt := make(map[string]interface{}, len(tx))
		for k, v := range tx {
			pkt[k] = v
		}
		tx["packet"] = pkt
		b, err := json.Marshal(WSMessage{Type: "packet", Data: tx})
		if err != nil {
			t.Fatal(err)
		}
		noStore = append(noStore, b)
		found.add("ws:packet-nostore", c.check(b, nil), nil)
	}
	if len(noStore) < 5 {
		t.Fatalf("store-less feed: %d rows, want the fixture's adverts", len(noStore))
	}

	t.Logf("feed messages checked: %d new packet, %d new observation, %d store-less",
		len(msgs[fresh.hash]), len(msgs[old.hash]), len(noStore))
	lkRatchet(t, found, func(k string) bool { return strings.HasPrefix(k, "ws:") })
}

// ─── Oracles ────────────────────────────────────────────────────────────────

// lkTightBox finds, by bisection (30 steps an edge), the smallest box inside
// start for which present still holds: each edge is moved inward on its own
// until the feature is gone. ok is false when it is absent from start itself.
func lkTightBox(start geoBBox, present func(geoBBox) bool) (tight geoBBox, ok bool) {
	if !present(start) {
		return geoBBox{}, false
	}
	bisect := func(lo, hi float64, with func(v float64) geoBBox) float64 {
		// present at lo, absent at hi
		for i := 0; i < 30; i++ {
			mid := (lo + hi) / 2
			if present(with(mid)) {
				lo = mid
			} else {
				hi = mid
			}
		}
		return lo
	}
	edge := func(lo, hi float64, with func(v float64) geoBBox) float64 {
		if present(with(hi)) {
			return hi // never goes away: this edge places nothing
		}
		return bisect(lo, hi, with)
	}
	tight.S = edge(start.S, start.N, func(v float64) geoBBox { b := start; b.S = v; return b })
	tight.N = edge(start.N, start.S, func(v float64) geoBBox { b := start; b.N = v; return b })
	tight.W = edge(start.W, start.E, func(v float64) geoBBox { b := start; b.W = v; return b })
	tight.E = edge(start.E, start.W, func(v float64) geoBBox { b := start; b.E = v; return b })
	// The feature needs S ≤ tight.S and N ≥ tight.N: it lies between them.
	if tight.S > tight.N {
		tight.S, tight.N = tight.N, tight.S
	}
	if tight.W > tight.E {
		tight.W, tight.E = tight.E, tight.W
	}
	return tight, true
}

// lkFarCornerM is how far the farthest corner of box is from p: under
// radius / 2, the box places p better than the setting allows.
func lkFarCornerM(box geoBBox, p geoPoint) float64 {
	far := 0.0
	for _, c := range []geoPoint{{box.S, box.W}, {box.S, box.E}, {box.N, box.W}, {box.N, box.E}} {
		far = math.Max(far, lkDistM(c, p))
	}
	return far
}

// lkBoxPlaces reports whether the tightest box an oracle found gives the
// exact position p away: it lies wholly within radius / 2 of p, and it is not
// simply where the server serves p from (one of the fuzzed positions, which
// a filter on served positions finds, and may).
func (c *lkChecker) lkBoxPlaces(box geoBBox, p geoPoint) (farM float64, leak bool) {
	const eps = 1e-6 // degrees: the bisection's resolution and then some
	for _, a := range c.allowed {
		if a.Lat >= box.S-eps && a.Lat <= box.N+eps && a.Lon >= box.W-eps && a.Lon <= box.E+eps {
			return lkFarCornerM(box, p), false
		}
	}
	farM = lkFarCornerM(box, p)
	return farM, farM < c.radiusM/2
}

// The bbox oracle on companion GPS (R16).
//
// A client shrinks the bbox of /api/rx-coverage, one edge at a time, until
// the companion's coverage disappears: the box it converges on (lkTightBox)
// is everything the bbox filter tells it. On an unprotected server that box
// is the fix itself. On a server that bins fixes into privacy cells it is the
// cell (its centre, or its extent), which is all a client may learn. A fix
// that happens to lie next to its cell centre is then still "found" within
// metres, and rightly so: judging one box by its distance to the fix would
// make the verdict depend on where the fixture point falls in the grid.
//
// What must hold instead is that the box does not depend on where the fix is
// inside its cell. The test cannot ask the server for its grid, so it moves
// the fix and compares, along two lines from the fixture's track point
// (lkFixLines), searching the tightest box at each position:
//
//   - near: 5 positions 40 m apart (160 m end to end). Cells are at least
//     the radius wide, so the line crosses at most one cell edge per axis and
//     the 5 positions lie in at most 3 cells: at most 3 different boxes. An
//     unprotected server, or one with cells under 40 m, gives 5.
//   - wide: 5 positions 600 m apart (2.4 km end to end). If every box places
//     its fix within radius / 2, the server follows the fix more closely than
//     the setting allows, whatever it bins to: that catches cells too small
//     for the first line to tell apart from proper ones. With cells of at
//     least 2 × radius converging on their centre it cannot happen: the
//     points within radius / 2 of one centre span under 1000 m of the line,
//     so at most two neighbouring positions, and two such stretches are over
//     1000 m apart (the centres are 2 × radius apart or more), so no two
//     neighbouring positions (600 m) are near different centres. Of 5
//     positions at least one is therefore farther than radius / 2 from every
//     centre. (With cells between radius and 2 × radius this bound is not
//     guaranteed; the feature's cells are 2 × radius.)
//
// Both verdicts are properties of the grid, not of the fixture point.
type lkOracleRun struct {
	fix   geoPoint
	box   geoBBox
	found bool
}

const (
	lkNearStepM, lkWideStepM = 40.0, 600.0
	lkFixSteps               = 5
)

// lkFixLines returns the two lines of positions, both starting at from and
// running south-east (away from the fixture's other receptions).
func lkFixLines(from geoPoint) (near, wide []geoPoint) {
	line := func(stepM float64) []geoPoint {
		d := stepM / math.Sqrt2
		dLat := d / 111_320
		dLon := d / (111_320 * math.Cos(from.Lat*math.Pi/180))
		var out []geoPoint
		for i := 0; i < lkFixSteps; i++ {
			out = append(out, geoPoint{Lat: from.Lat - float64(i)*dLat, Lon: from.Lon + float64(i)*dLon})
		}
		return out
	}
	return line(lkNearStepM), line(lkWideStepM)
}

// lkFixStartBox is the box every search starts from: it holds every position
// of both lines with over 2 km to spare (the cell a fix is binned to can be
// that far), and none of the fixture's other receptions or their cells (the
// nearest, the companion's home, is 4.6 km north and 4.6 km west of from).
func lkFixStartBox(from geoPoint) geoBBox {
	return geoBBox{S: from.Lat - 0.04, W: from.Lon - 0.03, N: from.Lat + 0.02, E: from.Lon + 0.045}
}

// lkSameBox: equal to well under the bisection's resolution on the ground.
func lkSameBox(a, b geoBBox) bool {
	const eps = 1e-7
	return math.Abs(a.S-b.S) < eps && math.Abs(a.N-b.N) < eps && math.Abs(a.W-b.W) < eps && math.Abs(a.E-b.E) < eps
}

// lkOracleVerdict judges the two lines; "" is no leak. A position whose
// coverage is not found at all gives nothing away and counts as one more
// "box" that is the same for every such position.
func lkOracleVerdict(near, wide []lkOracleRun, radiusM float64) string {
	var distinct []lkOracleRun
	for _, r := range near {
		same := false
		for _, d := range distinct {
			if r.found == d.found && (!r.found || lkSameBox(r.box, d.box)) {
				same = true
				break
			}
		}
		if !same {
			distinct = append(distinct, r)
		}
	}
	if len(distinct) > 3 {
		return fmt.Sprintf("the bbox filter tells apart %d of %d fixes %.0f m apart: it follows the fix, not a cell at least the radius wide",
			len(distinct), len(near), lkNearStepM)
	}
	worst, all := 0.0, len(wide) > 0
	for _, r := range wide {
		if !r.found {
			all = false
			break
		}
		far := lkFarCornerM(r.box, r.fix)
		worst = math.Max(worst, far)
		if far >= radiusM/2 {
			all = false
			break
		}
	}
	if all {
		return fmt.Sprintf("the bbox filter places each of %d fixes %.0f m apart within %.1f m (limit: radius/2 = %.0f m)",
			len(wide), lkWideStepM, worst, radiusM/2)
	}
	return ""
}

// TestLocationLeakOracles: what the answers give away without holding a
// coordinate.
//
//	rx-coverage-bbox    shrinking the bbox of /api/rx-coverage until the
//	                    companion's coverage disappears (R16; see lkOracleRun).
//	planner-nodes-bbox  the same on the planner's node listing: the repeaters
//	                    /api/planner/coverage reports for its box.
//	elevation-known     /api/elevation/profile accepts only known node
//	                    positions: an exact one must be unknown to it.
func TestLocationLeakOracles(t *testing.T) {
	f := newLKFixture(t)
	c := f.checker()
	found := lkFound{}
	get := func(path string) (int, []byte) {
		w := f.serve("GET", path, nil)
		return w.Code, w.Body.Bytes()
	}
	box := func(b geoBBox) string { return fmt.Sprintf("%.9f,%.9f,%.9f,%.9f", b.S, b.W, b.N, b.E) }
	around := func(p geoPoint, d float64) geoBBox {
		return geoBBox{S: p.Lat - d, W: p.Lon - d, N: p.Lat + d, E: p.Lon + d}
	}

	// The companion's fix on the road is moved along two lines, and the
	// tightest box is searched for each position (see lkFixLines).
	track := f.point("gps-track")
	requests := 0
	present := func(b geoBBox) bool {
		requests++
		code, body := get("/api/rx-coverage?z=18&bbox=" + box(b))
		if code != http.StatusOK {
			t.Fatalf("/api/rx-coverage bbox=%s = %d: %s", box(b), code, body)
		}
		var fc struct {
			Features []json.RawMessage `json:"features"`
		}
		if err := json.Unmarshal(body, &fc); err != nil {
			t.Fatalf("/api/rx-coverage: %v", err)
		}
		return len(fc.Features) > 0
	}
	at := track.geo()
	search := func(fixes []geoPoint) []lkOracleRun {
		var runs []lkOracleRun
		for _, fix := range fixes {
			res, err := f.srv.db.conn.Exec(`UPDATE client_receptions SET lat = ?, lon = ? WHERE lat = ? AND lon = ?`, fix.Lat, fix.Lon, at.Lat, at.Lon)
			if err != nil {
				t.Fatal(err)
			}
			if n, _ := res.RowsAffected(); n != 4 {
				t.Fatalf("moved %d receptions to %v, want the track point's 4", n, fix)
			}
			at = fix
			run := lkOracleRun{fix: fix}
			run.box, run.found = lkTightBox(lkFixStartBox(track.geo()), present)
			runs = append(runs, run)
			t.Logf("rx-coverage: fix %.6f,%.6f → tightest box %s (found %v), farthest corner %.1f m from the fix",
				fix.Lat, fix.Lon, box(run.box), run.found, lkFarCornerM(run.box, fix))
		}
		return runs
	}
	near, wide := lkFixLines(track.geo())
	if leak := lkOracleVerdict(search(near), search(wide), lkRadiusM); leak != "" {
		found["oracle:rx-coverage-bbox bbox"] = []string{leak}
	}
	t.Logf("rx-coverage: %d requests", requests)

	// The planner snaps its box to a grid, so the listing can only place a
	// node to a grid cell; the check holds it to that.
	rep := f.point("repeater")
	requests = 0
	tight, ok := lkTightBox(around(rep.geo(), 0.15), func(b geoBBox) bool {
		requests++
		code, body := get("/api/planner/coverage?bbox=" + fmt.Sprintf("%.3f,%.3f,%.3f,%.3f", b.S, b.W, b.N, b.E))
		if code == http.StatusBadRequest {
			return false // no node in reach of the box, or a box too thin to snap
		}
		if code != http.StatusOK {
			t.Fatalf("/api/planner/coverage bbox=%s = %d: %s", box(b), code, body)
		}
		return strings.Contains(string(body), f.repeater)
	})
	if ok {
		far, leak := c.lkBoxPlaces(tight, rep.geo())
		t.Logf("planner coverage: %d requests; tightest box %s; farthest corner %.1f m from the exact repeater", requests, box(tight), far)
		if leak {
			found["oracle:planner-nodes-bbox bbox"] = []string{fmt.Sprintf(
				"the node listing places the exact repeater within %.1f m (limit: radius/2 = %.0f m)", far, lkRadiusM/2)}
		}
	} else {
		t.Logf("planner coverage: the repeater is not listed for the start box")
	}

	// Route LOS answers unknown_point for anything that is not a node
	// position, so it must not know the exact ones.
	rel := f.point("relay")
	code, body := get("/api/elevation/profile?pts=" + url.QueryEscape(fmt.Sprintf("%.5f,%.5f,%.5f,%.5f", rep.lat, rep.lon, rel.lat, rel.lon)))
	if code != http.StatusOK {
		t.Fatalf("/api/elevation/profile = %d: %s", code, body)
	}
	if !strings.Contains(string(body), elevErrUnknownPoint) {
		found["oracle:elevation-known known-point"] = []string{"route LOS accepts the exact repeater and relay positions as known nodes: " + string(body)}
	} else {
		// Then it must know the positions the server serves instead, or the
		// check above passes on an endpoint that knows nothing.
		fr, fl := f.fuzzed(rep), f.fuzzed(rel)
		code, body = get("/api/elevation/profile?pts=" + url.QueryEscape(fmt.Sprintf("%.5f,%.5f,%.5f,%.5f", fr.Lat, fr.Lon, fl.Lat, fl.Lon)))
		if code != http.StatusOK || strings.Contains(string(body), elevErrUnknownPoint) {
			t.Errorf("route LOS knows neither the exact nor the served (fuzzed) positions: %d %s", code, body)
		}
	}

	lkRatchet(t, found, func(k string) bool { return strings.HasPrefix(k, "oracle:") })
}

// ─── The checks themselves ──────────────────────────────────────────────────

// lkSynthetic is a checker over two made-up nodes whose fuzzed positions are
// about 700 m from the exact ones, and one advert.
func lkSynthetic() (*lkChecker, []lkPoint, func(lkPoint) geoPoint, lkAdvert) {
	points := []lkPoint{
		{name: "alpha", pubkey: "aa", lat: 48.137154, lon: 11.576124},
		{name: "beta", pubkey: "bb", lat: 48.208176, lon: 11.861253},
	}
	fuzz := func(p lkPoint) geoPoint { return geoPoint{Lat: p.lat + 0.004411, Lon: p.lon - 0.006622} }
	if points[1].pubkey == "bb" {
		// beta moves the other way, so the fuzzed distance differs from the exact one.
		inner := fuzz
		fuzz = func(p lkPoint) geoPoint {
			if p.name == "beta" {
				return geoPoint{Lat: p.lat - 0.005533, Lon: p.lon + 0.004477}
			}
			return inner(p)
		}
	}
	adv := lkAdvert{
		pubkey: "aa", hash: "0123456789abcdef",
		rawHex:    []string{"1100" + strings.Repeat("AB", 100) + "92C282DE02" + "4C83DE02" + "BCB0B000"},
		sigHex:    strings.Repeat("5E", 64),
		latHex:    "4C83DE02",
		lonHex:    "BCB0B000",
		latLonHex: "4C83DE02BCB0B000",
	}
	return newLKChecker(points, fuzz, []lkAdvert{adv}, 1000), points, fuzz, adv
}

func lkChecks(leaks []lkLeak) string {
	set := map[string]bool{}
	for _, l := range leaks {
		set[l.check] = true
	}
	var out []string
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func TestLeakHelperBodies(t *testing.T) {
	c, points, fuzz, adv := lkSynthetic()
	fa, fb := fuzz(points[0]), fuzz(points[1])
	exactKm := geoDistKm(points[0].geo(), points[1].geo())
	fuzzKm := geoDistKm(fa, fb)
	if math.Abs(exactKm-fuzzKm) < 0.2 {
		t.Fatalf("synthetic distances too close: %v vs %v", exactKm, fuzzKm)
	}
	hidden := fmt.Sprintf(`{"hash":%q,"raw_hex":null,"decoded_json":"{\"type\":\"ADVERT\",\"pubKey\":\"aa\",\"name\":\"Alpha\"}","location_hidden":true}`, adv.hash)
	for _, tc := range []struct {
		name, body, want string
	}{
		// What a correct server sends.
		{"fuzzed node", fmt.Sprintf(`{"nodes":[{"public_key":"aa","lat":%v,"lon":%v}]}`, fa.Lat, fa.Lon), ""},
		{"fuzzed pair array", fmt.Sprintf(`{"center":[%v,%v],"lonlat":[%v,%v]}`, fa.Lat, fa.Lon, fb.Lon, fb.Lat), ""},
		{"fuzzed distance", fmt.Sprintf(`{"distance_km":%.3f,"dist":%.1f}`, fuzzKm, fuzzKm), ""},
		{"redacted advert", hidden, ""},
		{"no position", `{"nodes":[{"public_key":"aa","lat":null,"lon":null},{"public_key":"cc","lat":0,"lon":0}]}`, ""},
		{"far coordinates", `{"lat":48.5,"lon":11.2,"bbox":[47.9,11.1,48.6,12.2],"iata":{"MUC":{"lat":48.3538,"lon":11.7861}}}`, ""},
		{"timestamp holding the micro digits", `{"ts":1748137154000,"id":4813715411576124,"v":0.48137154}`, ""},
		{"longer number ending in the needle", `{"snr":148.137,"x":911.576}`, ""},
		{"cell around the fuzzed position", lkHexFeature(geoPoint{Lat: fa.Lat + 0.0001, Lon: fa.Lon + 0.0001}, 0.0005), ""},
		{"wide cell over the exact position", lkHexFeature(lkOff(points[0], 0.0031, 0.0027), 0.02), ""},
		{"whole kilometres", fmt.Sprintf(`{"distance_km":%.0f,"minDist":1,"dist":%d}`, exactKm, int(exactKm)), ""},

		// Exact coordinates as text.
		{"exact, full precision", `{"lat":48.137154,"lon":11.576124}`, "coords,text"},
		{"exact at 4 decimals", `{"note":"seen near 48.1372 / 11.5761"}`, "text"},
		{"exact at 3 decimals, truncated", `{"where":"48.137,11.576"}`, "text"},
		{"exact at 5 decimals as strings", `{"lat":"48.13715","lon":"11.57612"}`, "text"},
		{"exact negative", `{"dlat":-48.137154}`, "text"},
		{"micro integer", `{"lat_e6":48137154,"lon_e6":11576124}`, "text"},
		{"micro integer, negative, in text", `pos=-48137154;`, "text"},
		{"one axis only", `{"lon":11.861253}`, "text"},

		// Coordinates that are neither exact nor fuzzed.
		{"another approximation", `{"lat":48.1391,"lon":11.5799}`, "coords"},
		{"rounded to 2 decimals", `{"latitude":48.14,"longitude":11.58}`, "coords"},
		{"prefixed keys", `{"fromLat":48.2095,"fromLon":11.8631,"to_lat":10,"to_lon":10}`, "coords"},
		{"pair array, lat first", `{"center":[48.1391,11.5799]}`, "coords"},
		{"pair array, lon first", `{"geometry":{"type":"Point","coordinates":[11.5799,48.1391]}}`, "coords"},
		{"bbox array", `{"bbox":[48.13,11.57,48.14,11.58]}`, "coords"},
		{"inside a JSON string", `{"decoded_json":"{\"lat\":48.1391,\"lon\":11.5799}"}`, "coords"},
		{"tight cell on the exact position", lkHexFeature(lkOff(points[0], 0.00205, 0.0024), 0.0008), "cell"},

		// Cell polygons and the text check. A vertex 1.5 km away that shares
		// three (or four) decimals with an exact coordinate is chance.
		{"cell vertex sharing 3 decimals", lkRingFeature(`[11.5761999,48.1502],[11.61,48.1502],[11.61,48.1371999],[11.5761999,48.1502]`), ""},
		{"cell vertex sharing 4 decimals", lkRingFeature(`[11.5761099,48.1502],[11.61,48.1502],[11.61,48.1371099],[11.5761099,48.1502]`), ""},
		{"multipolygon vertex sharing 3 decimals", `{"type":"Feature","geometry":{"type":"MultiPolygon","coordinates":[[[[11.5761999,48.1502],[11.61,48.1502],[11.61,48.16],[11.5761999,48.1502]]]]}}`, ""},
		// The exact value itself, at 5 decimals or more, is no chance.
		{"cell vertex at the exact lon, 5 decimals", lkRingFeature(`[11.57612,48.1502],[11.61,48.1502],[11.61,48.17],[11.57612,48.1502]`), "text"},
		{"cell vertex at the exact lat, 6 decimals", lkRingFeature(`[11.59,48.137154],[11.61,48.1502],[11.61,48.17],[11.59,48.137154]`), "text"},
		{"cell vertex as micro integers", lkRingFeature(`[11576124,48137154],[11610000,48150200],[11610000,48170000],[11576124,48137154]`), "text"},
		// Only the vertices are exempt: not the cell's properties, not a
		// point feature, not a polygon outside a feature, not other JSON.
		{"cell property sharing 3 decimals", `{"type":"Feature","properties":{"note":"near 48.1371999"},"geometry":{"type":"Polygon","coordinates":[[[11.59,48.15],[11.61,48.15],[11.61,48.17],[11.59,48.15]]]}}`, "text"},
		{"point feature sharing 3 decimals", `{"type":"Feature","geometry":{"type":"Point","coordinates":[11.5761999,48.3]}}`, "text"},
		{"bare polygon sharing 3 decimals", `{"area":{"type":"Polygon","coordinates":[[[11.5761999,48.3],[11.61,48.3],[11.61,48.4],[11.5761999,48.3]]]}}`, "text"},
		{"number beside a cell sharing 3 decimals", `{"features":[` + lkRingFeature(`[11.59,48.15],[11.61,48.15],[11.61,48.17],[11.59,48.15]`) + `],"centre_lon":11.5761999}`, "text"},
		{"plain number sharing 3 decimals", `{"x":48.1371999}`, "text"},

		// Distances.
		{"exact distance, 3 decimals", fmt.Sprintf(`{"distance_km":%.3f}`, exactKm), "dist"},
		{"exact distance, 1 decimal", fmt.Sprintf(`{"edges":[{"distKm":%.1f}]}`, exactKm), "dist"},
		{"exact distance in metres", fmt.Sprintf(`{"dist_m":%.0f}`, exactKm*1000), "dist"},

		// Adverts.
		{"raw frame", fmt.Sprintf(`{"raw":%q}`, strings.ToLower(adv.rawHex[0])), "rawhex"},
		{"location bytes inside other hex", `{"hex":"ffee` + strings.ToLower(adv.latLonHex) + `0011"}`, "rawhex"},
		{"lat bytes alone, upper case", `{"hex":"00` + adv.latHex + `"}`, "rawhex"},
		{"signature", fmt.Sprintf(`{"sig":%q}`, adv.sigHex), "rawhex"},
		{"advert with raw_hex", fmt.Sprintf(`{"hash":%q,"raw_hex":"1100","decoded_json":"{}","location_hidden":true}`, adv.hash), "packet"},
		{"advert not marked", fmt.Sprintf(`{"hash":%q,"raw_hex":null,"decoded_json":"{\"name\":\"Alpha\"}"}`, adv.hash), "packet"},
		{"advert with a signature left", fmt.Sprintf(`{"packets":[{"hash":%q,"raw_hex":null,"decoded_json":{"signature":"00"},"location_hidden":true}]}`, adv.hash), "packet"},
		{"advert with lat left", fmt.Sprintf(`{"hash":%q,"decoded_json":"{\"lat\":1,\"lon\":2}","location_hidden":true}`, adv.hash), "packet"},
		{"advert with its payload decoded", fmt.Sprintf(`{"hash":%q,"raw_hex":null,"location_hidden":true,"decoded":{"payload":{"lat":48.1391,"lon":11.5799}}}`, adv.hash), "coords"},
		{"another packet keeps its frame", `{"hash":"ffffffffffffffff","raw_hex":"1500AA","decoded_json":"{\"type\":\"CHAN\"}"}`, ""},
	} {
		if got := lkChecks(c.check([]byte(tc.body), nil)); got != tc.want {
			t.Errorf("%s: checks = %q, want %q\n  body: %s\n  leaks: %v", tc.name, got, tc.want, tc.body, c.check([]byte(tc.body), nil))
		}
	}

	// Distances under keys that do not say so, when the request says they are.
	stats := fmt.Sprintf(`{"catStats":{"R↔R":{"max":%.2f,"count":5,"avg":3.5}}}`, exactKm)
	if got := lkChecks(c.check([]byte(stats), nil)); got != "" {
		t.Errorf("unnamed distance without allDist: checks = %q, want none", got)
	}
	if got := lkChecks(c.checkAs([]byte(stats), nil, true)); got != "dist" {
		t.Errorf("unnamed distance with allDist: checks = %q, want dist", got)
	}

	// A per-hour average that equals an exact distance by chance passes; a
	// distance elsewhere in the same body, or an avg outside the series, is
	// still caught.
	avg := fmt.Sprintf("%.2f", exactKm)
	if got := lkChecks(c.checkAs([]byte(`{"distOverTime":[{"hour":"x","avg":`+avg+`,"count":2}]}`), nil, true)); got != "" {
		t.Errorf("distOverTime avg: checks = %q, want none", got)
	}
	for name, body := range map[string]string{
		"a hop beside the series":    `{"distOverTime":[{"hour":"x","avg":` + avg + `,"count":2}],"topHops":[{"dist":` + avg + `}]}`,
		"avg outside the series":     `{"catStats":{"R":{"avg":` + avg + `}}}`,
		"nested under a series item": `{"distOverTime":[{"hour":"x","avg":1.5,"hops":[{"avg":` + avg + `}]}]}`,
	} {
		if got := lkChecks(c.checkAs([]byte(body), nil, true)); got != "dist" {
			t.Errorf("%s: checks = %q, want dist", name, got)
		}
	}

	// Histogram bins: an edge or a width that equals an exact distance is the
	// grid's doing; a real distance beside it, in the same body, is caught.
	edge := fmt.Sprintf("%.1f", exactKm)
	hist := `{"distHistogram":{"bins":[{"x":` + edge + `,"w":` + edge + `,"count":2},{"x":3.5,"w":0.5,"count":0}],"min":3.25,"max":%s},"topHops":[{"dist":%s}]}`
	clean := fmt.Sprintf(hist, "30.25", "30.25")
	if got := lkChecks(c.checkAs([]byte(clean), nil, true)); got != "" {
		t.Errorf("histogram bin edges: checks = %q, want none", got)
	}
	for name, body := range map[string]string{
		"the histogram's max":      fmt.Sprintf(hist, edge, "30.25"),
		"a hop beside the bins":    fmt.Sprintf(hist, "30.25", edge),
		"x outside a bins array":   `{"summary":{"x":` + edge + `,"w":0.5,"count":1}}`,
		"a bin with another field": `{"bins":[{"x":` + edge + `,"w":0.5,"count":1,"avg":` + edge + `}]}`,
		"a distance key in a bin":  `{"bins":[{"x":1.5,"w":0.5,"count":1,"dist":` + edge + `}]}`,
		"nested under a bin":       `{"bins":[{"x":1.5,"w":0.5,"count":1,"hops":[{"x":` + edge + `,"w":0.5}]}]}`,
	} {
		if got := lkChecks(c.checkAs([]byte(body), nil, true)); got != "dist" {
			t.Errorf("%s: checks = %q, want dist: %s", name, got, body)
		}
	}

	// A distance from a point of the request.
	probe := geoPoint{Lat: 48.17, Lon: 11.65}
	body := fmt.Sprintf(`{"links":[{"distance_km":%.3f}]}`, geoDistKm(probe, points[0].geo()))
	if got := lkChecks(c.check([]byte(body), []geoPoint{probe})); got != "dist" {
		t.Errorf("exact distance from the request point: checks = %q, want dist", got)
	}
	if got := lkChecks(c.check([]byte(body), nil)); got != "" {
		t.Errorf("the same distance without the probe: checks = %q, want none", got)
	}
	body = fmt.Sprintf(`{"links":[{"distance_km":%.3f}]}`, geoDistKm(probe, fa))
	if got := lkChecks(c.check([]byte(body), []geoPoint{probe})); got != "" {
		t.Errorf("fuzzed distance from the request point: checks = %q, want none", got)
	}
}

// lkOff is a point near p: the centre of a cell that holds p without a vertex
// sharing its digits.
func lkOff(p lkPoint, dLat, dLon float64) geoPoint {
	return geoPoint{Lat: p.lat + dLat, Lon: p.lon + dLon}
}

// The ticket of a leak follows its source.
func TestLeakHelperTickets(t *testing.T) {
	c, _, _, adv := lkSynthetic()
	found := lkFound{}
	advert := fmt.Sprintf(`{"hash":%q,"raw_hex":"1100","decoded_json":"{\"lat\":48.137154,\"lon\":11.576124}"}`, adv.hash)
	found.add("/api/only-adverts", c.check([]byte(`{"packets":[`+advert+`]}`), nil), nil)
	found.add("/api/both", c.check([]byte(`{"node":{"lat":48.137154,"lon":11.576124},"recent":[`+advert+`]}`), nil), nil)
	found.add("/api/only-nodes", c.check([]byte(`{"lat":48.137154,"lon":11.576124}`), nil), nil)
	found.add("/api/rx-coverage", c.check([]byte(`{"lat":48.137154,"lon":11.576124}`), nil), nil)
	found.add("ws:packet", c.check([]byte(`{"type":"packet","data":`+advert+`}`), nil), nil)
	for key, want := range map[string]string{
		"/api/only-adverts packet": "B5 packets",
		"/api/only-adverts coords": "B5 packets",
		"/api/only-adverts text":   "B5 packets",
		"/api/both packet":         "B5 packets",
		"/api/both coords":         "B3 coordinates, B5 packets",
		"/api/both text":           "B3 coordinates, B5 packets",
		"/api/only-nodes coords":   "B3 coordinates",
		"/api/only-nodes text":     "B3 coordinates",
		"/api/rx-coverage coords":  "B4 companion GPS",
		"ws:packet text":           "B5 packets",
	} {
		if _, ok := found[key]; !ok {
			t.Errorf("%s was not found", key)
		} else if got := lkTicket(key, found); got != want {
			t.Errorf("lkTicket(%s) = %q, want %q", key, got, want)
		}
	}
	// A check listed under skip is not recorded.
	skipped := lkFound{}
	skipped.add("/api/x", c.check([]byte(`{"lat":48.137154,"lon":11.576124}`), nil), map[string]string{"coords": "why"})
	if _, ok := skipped["/api/x coords"]; ok || len(skipped["/api/x text"]) == 0 {
		t.Errorf("skip: found = %v, want text only", skipped)
	}
}

// lkRingFeature is a GeoJSON polygon feature with the given ring ([lon,lat]
// pairs as JSON).
func lkRingFeature(ring string) string {
	return `{"type":"Feature","properties":{"count":3},"geometry":{"type":"Polygon","coordinates":[[` + ring + `]]}}`
}

func TestLeakHelperWithoutCells(t *testing.T) {
	cell := lkRingFeature(`[11.5761999,48.1502],[11.61,48.1502],[11.61,48.17],[11.5761999,48.1502]`)
	for name, tc := range map[string]struct {
		body string
		gone bool // the vertices are taken out
	}{
		"feature collection": {`{"type":"FeatureCollection","features":[` + cell + `],"gaps":[` + cell + `]}`, true},
		"no polygon":         {`{"lat":11.5761999,"geometry":{"type":"Point","coordinates":[11.5761999,48.15]}}`, false},
		"not a feature":      {`{"geometry":{"type":"Polygon","coordinates":[[[11.5761999,48.15]]]}}`, false},
		"not JSON":           {`<html>11.5761999</html>`, false},
	} {
		got := lkWithoutCells([]byte(tc.body))
		if has := strings.Contains(got, "11.5761999"); has == tc.gone {
			t.Errorf("%s: vertices kept = %v, want %v: %s", name, has, !tc.gone, got)
		}
		if !tc.gone && got != tc.body {
			t.Errorf("%s: body changed: %s", name, got)
		}
		if tc.gone && !strings.Contains(got, `"count":3`) {
			t.Errorf("%s: properties lost: %s", name, got)
		}
	}
}

// lkHexFeature is a GeoJSON hexagon feature of the given size (degrees)
// around p.
func lkHexFeature(p geoPoint, r float64) string {
	var ring []string
	for i := 0; i <= 6; i++ {
		a := float64(i%6) * math.Pi / 3
		ring = append(ring, fmt.Sprintf("[%.7f,%.7f]", p.Lon+r*math.Cos(a), p.Lat+r*math.Sin(a)))
	}
	return `{"type":"FeatureCollection","features":[{"type":"Feature","properties":{"n":3},"geometry":{"type":"Polygon","coordinates":[[` +
		strings.Join(ring, ",") + `]]}}]}`
}

// While Fuzz is the identity (the stub), an exact value is a leak, not an
// allowed "fuzzed" one: the allowlist must not depend on the stub.
func TestLeakHelperIdentityFuzzStillLeaks(t *testing.T) {
	_, points, _, adv := lkSynthetic()
	c := newLKChecker(points, func(p lkPoint) geoPoint { return p.geo() }, []lkAdvert{adv}, 1000)
	km := geoDistKm(points[0].geo(), points[1].geo())
	for body, want := range map[string]string{
		`{"lat":48.137154,"lon":11.576124}`:                     "coords,text",
		`{"center":[48.137154,11.576124]}`:                      "coords,text",
		fmt.Sprintf(`{"distance_km":%.2f}`, km):                 "dist",
		lkHexFeature(lkOff(points[0], 0.00205, 0.0024), 0.0008): "cell",
		`{"lat":48.5,"lon":11.2}`:                               "",
		fmt.Sprintf(`{"other":%.2f,"dist":%.2f}`, km, 3.0):      "",
	} {
		if got := lkChecks(c.check([]byte(body), nil)); got != want {
			t.Errorf("%s: checks = %q, want %q", body, got, want)
		}
	}
}

func TestLeakHelperNumberBoundaries(t *testing.T) {
	dec := lkNeedle{text: "48.137"}
	micro := lkNeedle{text: "48137154", micro: true}
	for _, tc := range []struct {
		body string
		n    lkNeedle
		want bool
	}{
		{"48.137", dec, true},
		{"lat=48.137154", dec, true},
		{"-48.1371", dec, true},
		{"148.137", dec, false},
		{"148.137 then 48.137", dec, true},
		{"48.13", dec, false},
		{"48137154", micro, true},
		{"[-48137154,1]", micro, true},
		{"148137154", micro, false},
		{"481371540", micro, false},
		{"0.48137154", micro, false},
		{"48137154.5", micro, false},
		{"1748137154000 48137154", micro, true},
	} {
		if got := lkHasNumber(tc.body, tc.n); got != tc.want {
			t.Errorf("lkHasNumber(%q, %q) = %v, want %v", tc.body, tc.n.text, got, tc.want)
		}
	}
	// Rounded and truncated forms both count.
	decs, m := lkCoordNeedles(-122.084567)
	joined := strings.Join(decs, " ")
	for _, w := range []string{"122.085", "122.084", "122.0846", "122.0845", "122.08457", "122.08456", "122.084567"} {
		if !strings.Contains(" "+joined+" ", " "+w+" ") {
			t.Errorf("needles of -122.084567 lack %s: %v", w, decs)
		}
	}
	if m != "122084567" {
		t.Errorf("micro needle = %s", m)
	}
}

func TestLeakHelperKeys(t *testing.T) {
	for key, want := range map[string]string{
		"lat": "lat:", "lon": "lon:", "lng": "lon:", "latitude": "lat:", "Longitude": "lon:",
		"fromLat": "lat:from", "from_lon": "lon:from", "minLat": "lat:min", "nodeLon": "lon:node",
		"latency": ":", "relative": ":", "platform": ":", "longest": ":", "colon": ":", "flat": ":",
	} {
		axis, base := lkAxisKey(key)
		if got := axis + ":" + base; got != want {
			t.Errorf("lkAxisKey(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestLeakHelperTightBox(t *testing.T) {
	// An exact filter: present while the point is inside.
	p := geoPoint{Lat: 48.137154, Lon: 11.576124}
	start := geoBBox{S: p.Lat - 0.02, W: p.Lon - 0.02, N: p.Lat + 0.02, E: p.Lon + 0.02}
	inside := func(q geoPoint) func(geoBBox) bool {
		return func(b geoBBox) bool { return q.Lat >= b.S && q.Lat <= b.N && q.Lon >= b.W && q.Lon <= b.E }
	}
	tight, ok := lkTightBox(start, inside(p))
	if !ok || lkFarCornerM(tight, p) > 0.01 {
		t.Errorf("exact filter: tight box %+v is %.3f m off, want the point itself", tight, lkFarCornerM(tight, p))
	}
	// A filter on a position 700 m away finds that one, not the exact one.
	q := geoPoint{Lat: p.Lat + 0.0045, Lon: p.Lon - 0.0066}
	tight, ok = lkTightBox(start, inside(q))
	if far := lkFarCornerM(tight, p); !ok || math.Abs(far-lkDistM(p, q)) > 0.01 {
		t.Errorf("moved filter: farthest corner %.3f m, want %.3f", far, lkDistM(p, q))
	}
	// A grid filter (cells of 0.01°) cannot place the point inside its cell.
	cell := func(b geoBBox) bool {
		s, w := math.Floor(p.Lat*100)/100, math.Floor(p.Lon*100)/100
		return b.S <= s+0.01 && b.N >= s && b.W <= w+0.01 && b.E >= w
	}
	tight, ok = lkTightBox(start, cell)
	if far := lkFarCornerM(tight, p); !ok || far < 500 {
		t.Errorf("grid filter: farthest corner %.1f m of %+v, want a whole cell", far, tight)
	}
	// Nothing in the start box.
	if _, ok := lkTightBox(start, func(geoBBox) bool { return false }); ok {
		t.Error("absent feature reported as found")
	}

	// What the box gives away: the exact point is a leak; the fuzzed point
	// is not, even when it happens to lie within radius / 2; a cell is not.
	pt := lkPoint{name: "p", pubkey: "aa", lat: p.Lat, lon: p.Lon}
	near := geoPoint{Lat: p.Lat + 0.002, Lon: p.Lon + 0.002} // about 270 m
	c := newLKChecker([]lkPoint{pt}, func(lkPoint) geoPoint { return near }, nil, 1000)
	at := func(g geoPoint) geoBBox { return geoBBox{S: g.Lat, W: g.Lon, N: g.Lat, E: g.Lon} }
	if _, leak := c.lkBoxPlaces(at(p), p); !leak {
		t.Error("a box that is the exact point is not reported")
	}
	if _, leak := c.lkBoxPlaces(at(near), p); leak {
		t.Error("a box that is the fuzzed point is reported")
	}
	if _, leak := c.lkBoxPlaces(at(geoPoint{Lat: p.Lat + 0.001, Lon: p.Lon}), p); !leak {
		t.Error("a box 110 m from the exact point, and not the fuzzed one, is not reported")
	}
	if _, leak := c.lkBoxPlaces(tight, p); leak {
		t.Error("a whole grid cell is reported")
	}
	stub := newLKChecker([]lkPoint{pt}, func(q lkPoint) geoPoint { return q.geo() }, nil, 1000)
	if _, leak := stub.lkBoxPlaces(at(p), p); !leak {
		t.Error("with an identity Fuzz the exact point must still be a leak")
	}
}

// The bbox oracle's verdict on model servers: one that filters on the fix,
// and ones that bin fixes to the centre of a square cell of a given side,
// with the grid shifted every way (so the fix is at the centre, on an edge,
// in a corner): the verdict must depend on the cell size alone.
func TestLeakHelperOracleVerdict(t *testing.T) {
	from := geoPoint{Lat: 37.445987, Lon: -122.161728}
	near, wide := lkFixLines(from)
	if d := lkDistM(near[0], near[lkFixSteps-1]); math.Abs(d-160) > 2 {
		t.Fatalf("near line spans %.1f m, want 160", d)
	}
	if d := lkDistM(wide[0], wide[1]); math.Abs(d-600) > 5 {
		t.Fatalf("wide line steps %.1f m, want 600", d)
	}
	start := lkFixStartBox(from)
	// binned(side, …) is where a server with cells of side metres puts fix.
	binned := func(sideM, shiftLat, shiftLon float64) func(geoPoint) geoPoint {
		return func(fix geoPoint) geoPoint {
			if sideM == 0 {
				return fix
			}
			h := sideM / 111_320
			w := sideM / (111_320 * math.Cos(37.4*math.Pi/180))
			return geoPoint{
				Lat: (math.Floor(fix.Lat/h-shiftLat) + 0.5 + shiftLat) * h,
				Lon: (math.Floor(fix.Lon/w-shiftLon) + 0.5 + shiftLon) * w,
			}
		}
	}
	run := func(place func(geoPoint) geoPoint, fixes []geoPoint) []lkOracleRun {
		var out []lkOracleRun
		for _, fix := range fixes {
			q := place(fix)
			r := lkOracleRun{fix: fix}
			r.box, r.found = lkTightBox(start, func(b geoBBox) bool {
				return q.Lat >= b.S && q.Lat <= b.N && q.Lon >= b.W && q.Lon <= b.E
			})
			if !r.found {
				t.Fatalf("fix %v placed at %v is outside the start box %+v", fix, q, start)
			}
			out = append(out, r)
		}
		return out
	}
	for _, tc := range []struct {
		name  string
		sideM float64
		leak  bool
	}{
		{"filter on the fix", 0, true},
		{"10 m cells", 10, true},
		{"30 m cells", 30, true},
		{"100 m cells", 100, true},
		{"300 m cells", 300, true},
		{"cells of 2 × radius", 2000, false},
		{"cells of 4 × radius", 4000, false},
	} {
		for i := 0; i < 10; i++ {
			for j := 0; j < 10; j++ {
				place := binned(tc.sideM, float64(i)/10, float64(j)/10)
				got := lkOracleVerdict(run(place, near), run(place, wide), 1000)
				if (got != "") != tc.leak {
					t.Fatalf("%s, grid shifted %d/10, %d/10: verdict %q, want leak = %v", tc.name, i, j, got, tc.leak)
				}
				if tc.sideM == 0 {
					break
				}
			}
			if tc.sideM == 0 {
				break
			}
		}
	}
	// Coverage that is never found tells nothing.
	hidden := []lkOracleRun{{}, {}, {}, {}, {}}
	if got := lkOracleVerdict(hidden, hidden, 1000); got != "" {
		t.Errorf("nothing found: verdict %q", got)
	}
}
