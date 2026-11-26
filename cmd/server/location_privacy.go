package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
)

// Location privacy ("fuzzy location"): when locationPrivacy.enabled is set,
// the server never serves a node's exact position. Every coordinate it serves
// or computes with (distances, planner, reach, coverage) is the approximate
// position from (*locationFuzzer).Fuzz; the geo_filter classification is the
// only consumer of exact coordinates. The database stays exact: the ingestor
// is unchanged and the server stays read-only (#1283).
//
// Behaviour:
//
//   Config JSON:   "locationPrivacy": {"enabled": true, "radiusM": 1000, "secret": "<≥32 bytes>"}
//                  The environment variable CORESCOPE_LOCATION_SECRET, when set,
//                  overrides "secret". radiusM 0 → default 1000; valid 100..50000.
//   Startup:       main calls newLocationFuzzer(cfg.LocationPrivacy) right after
//                  LoadConfig and exits (log.Fatalf) on error: enabled with a
//                  missing/short secret or an out-of-range radius never starts.
//                  On success it stores the result with cfg.setLocationFuzzer.
//   Lookup:        cfg.LocationFuzzer() — nil when disabled. Every method is
//                  nil-safe: a nil *locationFuzzer is the identity.
//   Client:        GET /api/config/client gains
//                  "locationPrivacy": {"enabled": bool, "radiusM": number}
//                  (ClientLocationPrivacy). The secret is never serialised or logged.
//   Packets:       for an ADVERT whose payload carries a location, every packet
//                  and observation the server serves (HTTP and WebSocket) has
//                  raw_hex null, decoded_json without the lat, lon and signature keys,
//                  and "location_hidden": true. hash stays. Such a packet also
//                  carries "frame_bytes" (int, the frame length) and "frame_head"
//                  (hex of the frame header and path bytes only — never payload),
//                  so size filters and hash-size badges keep working. An ADVERT
//                  frame that holds signature bytes but no readable appdata flags
//                  byte is treated as located (fail closed), and a hidden packet's
//                  detail never falls back to any observation or canonical frame.
//   Client (off):  when the feature is off /api/config/client omits the
//                  "locationPrivacy" key entirely (byte-identical to upstream).
//   Companions:    with the feature on, per-companion views (?rx=<pubkey> on
//                  /api/rx-coverage, /api/rf-noise, coverage gaps) are served only
//                  to that companion's linked owner or an admin; aggregate views
//                  stay public.
//   Startup:       refuse to start when CORESCOPE_LOCATION_SECRET is set but the
//                  block does not say "enabled": false explicitly; on a config.json
//                  that fails to parse, refuse only when the env secret is set or
//                  the raw file may turn it on (rawLocationPrivacyMayEnable).
//   Offsets:       the offset of a served point derives from the cell only
//                  (row, col), so co-located nodes share one approximate point and
//                  cannot be averaged.
//
// Geometry: metre-based grid, cell side s = 0.4·R, origin offset derived
// from HMAC-SHA256(secret, "grid"); a node keeps its anchor cell while its
// exact position stays inside that cell grown by 0.25·s on every side
// (hysteresis, in memory, bounded); the served point is the anchor cell centre
// plus an offset uniform in a disk of radius r = R − 1.0607·s, derived from
// HMAC-SHA256(secret, "offset" | row | col) — the cell only, never the
// pubkey. So |served − exact| ≤ R always.

// LocationPrivacyConfig is the "locationPrivacy" config block.
//
// Secret is read from config.json but never written out: MarshalJSON and
// String leave it out, so no JSON the server emits and no log line built
// from this value can carry it, even if a future path serialises the
// whole Config.
type LocationPrivacyConfig struct {
	Enabled bool    `json:"enabled"`
	RadiusM float64 `json:"radiusM"`
	Secret  string  `json:"secret"`

	// enabledSet records that the block named "enabled" at all, so an
	// explicit "enabled": false can be told from a missing key
	// (locationPrivacyStartupCheck).
	enabledSet bool
}

// UnmarshalJSON reads the block like the default decoder (same lenient key
// matching) and records whether "enabled" was present.
func (c *LocationPrivacyConfig) UnmarshalJSON(data []byte) error {
	var raw struct {
		Enabled *bool   `json:"enabled"`
		RadiusM float64 `json:"radiusM"`
		Secret  string  `json:"secret"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*c = LocationPrivacyConfig{RadiusM: raw.RadiusM, Secret: raw.Secret}
	if raw.Enabled != nil {
		c.Enabled, c.enabledSet = *raw.Enabled, true
	}
	return nil
}

// explicitlyDisabled reports a block that says "enabled": false.
func (c *LocationPrivacyConfig) explicitlyDisabled() bool {
	return c != nil && c.enabledSet && !c.Enabled
}

// locationPrivacyPublic is the serialised form of LocationPrivacyConfig.
type locationPrivacyPublic struct {
	Enabled bool    `json:"enabled"`
	RadiusM float64 `json:"radiusM"`
}

// MarshalJSON writes the block without the secret.
func (c LocationPrivacyConfig) MarshalJSON() ([]byte, error) {
	return json.Marshal(locationPrivacyPublic{Enabled: c.Enabled, RadiusM: c.RadiusM})
}

// String formats the block without the secret (%v, %+v, %s).
func (c LocationPrivacyConfig) String() string {
	return fmt.Sprintf("{Enabled:%t RadiusM:%g}", c.Enabled, c.RadiusM)
}

// ClientLocationPrivacy is what /api/config/client tells the frontend.
type ClientLocationPrivacy struct {
	Enabled bool    `json:"enabled"`
	RadiusM float64 `json:"radiusM"`
}

const (
	// locationSecretEnv overrides locationPrivacy.secret when non-empty.
	locationSecretEnv = "CORESCOPE_LOCATION_SECRET"
	// locMinSecretLen is the shortest accepted secret, in bytes.
	locMinSecretLen = 32
	// Radius limits and default, metres.
	locDefaultRadiusM = 1000
	locMinRadiusM     = 100
	locMaxRadiusM     = 50000

	// locMetresPerDeg sizes the grid (rows s/111320 degrees high). Distances
	// are measured with haversineKm on the earthRadiusKm sphere, the same
	// one every other CoreScope distance uses.
	locMetresPerDeg = 111320.0
	// locCellFrac is the cell side as a fraction of the radius (s = 0.4·R).
	locCellFrac = 0.4
	// locMarginFrac is the hysteresis margin as a fraction of s.
	locMarginFrac = 0.25
	// locGridMaxLat clamps the grid maths: a node poleward of it picks its
	// cell as if it were at ±85.
	locGridMaxLat = 85.0
	// The disk radius leaves room for the spherical distance from the cell
	// centre to the far corner of the expanded cell (≥ 1.0607·s), plus
	// locRelSlack·R so the bound also holds on the WGS84 ellipsoid (a degree
	// of meridian is up to 0.56% longer than on the sphere) and locAbsSlackM
	// for the null-island nudge (≤ 1.12 m) and rounding.
	locRelSlack  = 0.006
	locAbsSlackM = 2.0
	// locNullEps: a served point with both |lat| and |lon| below this is
	// moved off (0, 0), which every consumer reads as "no fix".
	locNullEps = 1e-5

	// locAnchorCap bounds the hysteresis map. When it is full, inserting a
	// new pubkey first evicts one arbitrary entry (Go map iteration order):
	// O(1), no unbounded growth, and a flood of fake pubkeys only costs the
	// evicted nodes their hysteresis (they re-anchor to their current cell
	// on the next call), never the distance bound or determinism.
	locAnchorCap = 200000
)

// locationFuzzer turns exact positions into approximate ones. Safe for
// concurrent use. A nil *locationFuzzer means the feature is off.
type locationFuzzer struct {
	radiusM float64
	cellM   float64 // s = 0.4·R
	dLat    float64 // row height, degrees
	fLat    float64 // secret row offset, fraction of a row [0, 1)
	fLon    float64 // secret column offset, fraction of a column [0, 1)

	macs sync.Pool // hash.Hash: HMAC-SHA256 keyed with the secret

	mu        sync.Mutex // guards anchors; held only for map access
	anchors   map[string]locAnchor
	anchorCap int
}

// locAnchor is a node's anchor cell, its expanded bounds and the served
// point the cell yields for that node.
type locAnchor struct {
	row, col       int64
	latLo, latHi   float64 // expanded latitude bounds
	lonC, halfLonX float64 // column centre and expanded half-width, degrees
	sLat, sLon     float64 // served point (cell centre + secret offset)
}

// contains reports whether the (grid-clamped) position is inside the anchor
// cell grown by the hysteresis margin.
func (a locAnchor) contains(glat, lon float64) bool {
	return glat >= a.latLo && glat <= a.latHi && math.Abs(wrapLon(lon-a.lonC)) <= a.halfLonX
}

// newLocationFuzzer validates the config and builds the fuzzer. It returns
// (nil, nil) when the block is absent or disabled.
func newLocationFuzzer(c *LocationPrivacyConfig) (*locationFuzzer, error) {
	if c == nil || !c.Enabled {
		return nil, nil
	}
	secret := c.Secret
	if env := os.Getenv(locationSecretEnv); env != "" {
		secret = env
	}
	if len(secret) < locMinSecretLen {
		return nil, fmt.Errorf("locationPrivacy.secret (or %s) must be at least %d bytes", locationSecretEnv, locMinSecretLen)
	}
	radius := c.RadiusM
	if radius == 0 {
		radius = locDefaultRadiusM
	}
	if math.IsNaN(radius) || radius < locMinRadiusM || radius > locMaxRadiusM {
		return nil, fmt.Errorf("locationPrivacy.radiusM must be between %d and %d (got %g)", locMinRadiusM, locMaxRadiusM, c.RadiusM)
	}
	key := []byte(secret)
	f := &locationFuzzer{
		radiusM:   radius,
		cellM:     locCellFrac * radius,
		anchors:   make(map[string]locAnchor),
		anchorCap: locAnchorCap,
	}
	f.dLat = f.cellM / locMetresPerDeg
	f.macs.New = func() any { return hmac.New(sha256.New, key) }
	var sum [sha256.Size]byte
	f.mac([]byte("grid"), &sum)
	f.fLat = unitFloat(sum[0:8])
	f.fLon = unitFloat(sum[8:16])
	return f, nil
}

// mac writes HMAC-SHA256(secret, msg) into out.
func (f *locationFuzzer) mac(msg []byte, out *[sha256.Size]byte) {
	h := f.macs.Get().(hash.Hash)
	h.Reset()
	h.Write(msg)
	h.Sum(out[:0])
	f.macs.Put(h)
}

// unitFloat maps 8 bytes to a uniform float64 in [0, 1).
func unitFloat(b []byte) float64 {
	return float64(binary.BigEndian.Uint64(b)>>11) / (1 << 53)
}

// anchorFor returns the stored anchor of pk.
func (f *locationFuzzer) anchorFor(pk string) (locAnchor, bool) {
	f.mu.Lock()
	a, ok := f.anchors[pk]
	f.mu.Unlock()
	return a, ok
}

// storeAnchor records pk's anchor, evicting one arbitrary entry when the map
// is full and pk is new.
func (f *locationFuzzer) storeAnchor(pk string, a locAnchor) {
	f.mu.Lock()
	if _, ok := f.anchors[pk]; !ok && len(f.anchors) >= f.anchorCap {
		for k := range f.anchors {
			delete(f.anchors, k)
			break
		}
	}
	f.anchors[pk] = a
	f.mu.Unlock()
}

// anchorCount is the number of stored anchors (tests).
func (f *locationFuzzer) anchorCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.anchors)
}

// cellAt returns the cell containing (glat, lon) and the point it serves.
// The point depends on the cell only, so every node in one cell shares it
// and averaging co-located nodes reveals nothing. glat is already clamped
// to ±locGridMaxLat.
func (f *locationFuzzer) cellAt(glat, lon float64) locAnchor {
	row := int64(math.Floor(glat/f.dLat + f.fLat))
	latC := (float64(row) + 0.5 - f.fLat) * f.dLat
	// Columns: width s/(111320·cos(row centre)), rounded down so a whole
	// number of columns wraps the parallel and the antimeridian is no seam.
	cosC := math.Cos(math.Min(math.Abs(latC), locGridMaxLat) * math.Pi / 180)
	ncol := math.Ceil(360 / (f.cellM / (locMetresPerDeg * cosC)))
	dLon := 360 / ncol
	x := (lon+180)/dLon - f.fLon
	col := int64(math.Mod(math.Floor(x), ncol))
	if col < 0 {
		col += int64(ncol)
	}
	lonC := wrapLon(-180 + (float64(col)+0.5+f.fLon)*dLon)

	halfLatX := (0.5 + locMarginFrac) * f.dLat
	halfLonX := (0.5 + locMarginFrac) * dLon
	// Farthest point of the expanded cell from its centre: one of its
	// corners (distance grows with |Δlon| at fixed latitude and is unimodal
	// in latitude along a meridian edge).
	dMax := math.Max(
		haversineKm(latC, lonC, latC+halfLatX, lonC+halfLonX),
		haversineKm(latC, lonC, latC-halfLatX, lonC+halfLonX),
	) * 1000
	dMax = math.Max(dMax, 0.75*math.Sqrt2*f.cellM)
	r := f.radiusM*(1-locRelSlack) - locAbsSlackM - dMax

	var buf [64]byte
	msg := append(buf[:0], "offset|"...)
	msg = strconv.AppendInt(msg, row, 10)
	msg = append(msg, '|')
	msg = strconv.AppendInt(msg, col, 10)
	var sum [sha256.Size]byte
	f.mac(msg, &sum)
	theta := 2 * math.Pi * unitFloat(sum[0:8])
	rho := r * math.Sqrt(unitFloat(sum[8:16]))
	// The offset is applied on the sphere (bearing θ, distance ρ from the
	// cell centre): the local metre frame made exact, so |served − centre|
	// is ρ and the bound follows from the triangle inequality.
	sLat, sLon := destinationPoint(latC, lonC, theta, rho)

	return locAnchor{
		row: row, col: col,
		latLo: latC - halfLatX, latHi: latC + halfLatX,
		lonC: lonC, halfLonX: halfLonX,
		sLat: sLat, sLon: sLon,
	}
}

// wrapLon maps a longitude into [-180, 180).
func wrapLon(lon float64) float64 {
	if lon >= -180 && lon < 180 {
		return lon
	}
	w := math.Mod(lon+180, 360)
	if w < 0 {
		w += 360
	}
	w -= 180
	if w >= 180 {
		w = -180
	}
	return w
}

// destinationPoint is the point at distance distM (metres) and initial
// bearing brg (radians from north) from (lat, lon) on the earthRadiusKm
// sphere. The longitude is wrapped into [-180, 180).
func destinationPoint(lat, lon, brg, distM float64) (float64, float64) {
	const rad = math.Pi / 180
	d := distM / (earthRadiusKm * 1000)
	p1, l1 := lat*rad, lon*rad
	sinP2 := math.Sin(p1)*math.Cos(d) + math.Cos(p1)*math.Sin(d)*math.Cos(brg)
	sinP2 = math.Max(-1, math.Min(1, sinP2))
	p2 := math.Asin(sinP2)
	l2 := l1 + math.Atan2(math.Sin(brg)*math.Sin(d)*math.Cos(p1), math.Cos(d)-math.Sin(p1)*sinP2)
	return p2 / rad, wrapLon(l2 / rad)
}

// initialBearing is the bearing (radians from north) from (lat1, lon1) to
// (lat2, lon2).
func initialBearing(lat1, lon1, lat2, lon2 float64) float64 {
	const rad = math.Pi / 180
	p1, p2 := lat1*rad, lat2*rad
	dl := (lon2 - lon1) * rad
	return math.Atan2(math.Sin(dl)*math.Cos(p2), math.Cos(p1)*math.Sin(p2)-math.Sin(p1)*math.Cos(p2)*math.Cos(dl))
}

// avoidNullIsland moves a served point that would read as (0, 0) to
// latitude ±locNullEps (at most 1.12 m, inside the slack).
func avoidNullIsland(lat, lon float64) (float64, float64) {
	if math.Abs(lat) < locNullEps && math.Abs(lon) < locNullEps {
		if lat < 0 {
			return -locNullEps, lon
		}
		return locNullEps, lon
	}
	return lat, lon
}

// locationPrivacyFailClosed decides whether a config file that could not be
// parsed (loadErr, at cfgPath) must stop the server, because the fallback
// (the next config file or the defaults) would serve exact positions. It
// must when the secret environment variable is set, or when the raw file
// may turn location privacy on (rawLocationPrivacyMayEnable). A file whose
// every locationPrivacy block reads as off, such as the stock disabled block
// in config.example.json (copied by the Docker entrypoint), does not stop
// it; nor does a missing or unreadable file without the env secret, or no
// parse error. The error never contains the secret or the file contents.
func locationPrivacyFailClosed(loadErr error, cfgPath string, getenv func(string) string) error {
	if loadErr == nil {
		return nil
	}
	if getenv(locationSecretEnv) != "" {
		return fmt.Errorf("config.json could not be parsed and %s is set; refusing to start with exact positions: %w", locationSecretEnv, loadErr)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil || !rawLocationPrivacyMayEnable(data) {
		return nil
	}
	return fmt.Errorf("config.json could not be parsed and location privacy appears configured; fix the file, or set locationPrivacy.enabled to false; refusing to start with exact positions: %w", loadErr)
}

// rawLocationPrivacyMayEnable reads a config file that json.Unmarshal
// rejected and reports whether it may turn location privacy on. It walks the
// file's string tokens (escapes included, so braces and quotes inside
// strings are skipped). Every key that decodes to locationPrivacy, matched
// case-insensitively as encoding/json does, has its value decoded on its own
// through LocationPrivacyConfig.UnmarshalJSON, so duplicate keys, escapes and
// a quoted "true" read exactly as a clean load would read them. Any block
// that does not decode, or decodes as enabled, may enable.
//
// A syntax error can also hide a block from the walk: an unescaped quote
// pairs the following strings up wrongly, an unterminated one ends the walk.
// So every case-insensitive "locationprivacy" in the raw bytes must lie
// inside a string token the walk read; one outside them may enable.
func rawLocationPrivacyMayEnable(raw []byte) bool {
	const name = "locationprivacy"
	data := string(raw)
	total := strings.Count(strings.ToLower(data), name)
	seen := 0
	for i := 0; i < len(data); i++ {
		if data[i] != '"' {
			continue
		}
		end := jsonStringEnd(data, i)
		if end < 0 {
			break // unterminated: the rest is unread
		}
		tok := data[i : end+1]
		seen += strings.Count(strings.ToLower(tok), name)
		var key string
		if colon := skipJSONSpace(data, end+1); colon < len(data) && data[colon] == ':' &&
			json.Unmarshal([]byte(tok), &key) == nil && strings.EqualFold(key, "locationPrivacy") {
			var c LocationPrivacyConfig
			if err := json.NewDecoder(strings.NewReader(data[colon+1:])).Decode(&c); err != nil || c.Enabled {
				return true
			}
		}
		i = end
	}
	return seen != total
}

// jsonStringEnd returns the index of the quote closing the JSON string that
// opens at data[start], or -1 when it is not closed.
func jsonStringEnd(data string, start int) int {
	for j := start + 1; j < len(data); j++ {
		switch data[j] {
		case '\\':
			j++
		case '"':
			return j
		}
	}
	return -1
}

// locationPrivacyStartupCheck is the check on a config that did load: with
// the secret environment variable set, the server starts only when location
// privacy is on (f non-nil) or the block says "enabled": false explicitly.
// A missing block, a misspelt key or a missing "enabled" means the operator
// supplied a secret for a feature the config does not turn on.
func locationPrivacyStartupCheck(c *LocationPrivacyConfig, f *locationFuzzer, getenv func(string) string) error {
	if getenv(locationSecretEnv) == "" || f != nil || c.explicitlyDisabled() {
		return nil
	}
	return fmt.Errorf("%s is set but locationPrivacy is not enabled in config.json; set locationPrivacy.enabled to true, or to false explicitly", locationSecretEnv)
}

// reconcileLocationFuzzer makes the DB and the config hold one fuzzer: a
// fuzzer installed on either side is copied to the other, nil never
// replaces a fuzzer, and two different fuzzers are a programming error.
func reconcileLocationFuzzer(db *DB, cfg *Config) {
	cf, df := cfg.LocationFuzzer(), db.locationFuzzer()
	switch {
	case cf != nil && df != nil && cf != df:
		panic("location privacy: the DB and the config hold different location fuzzers")
	case cf != nil && df == nil:
		db.setLocationFuzzer(cf)
	case cf == nil && df != nil:
		cfg.setLocationFuzzer(df)
	}
}

// Enabled reports whether positions are fuzzed.
func (f *locationFuzzer) Enabled() bool { return f != nil }

// RadiusM is the maximum distance between a served and an exact position
// (0 when disabled).
func (f *locationFuzzer) RadiusM() float64 {
	if f == nil {
		return 0
	}
	return f.radiusM
}

// Fuzz returns the approximate position served for the node pubkey (any case)
// at exact position (lat, lon). A no-fix position (0, 0) is returned unchanged;
// any other result is never (0, 0). Identity when f is nil.
//
// Non-finite input carries no position and is returned unchanged. A latitude
// outside [-90, 90] is clamped and a longitude outside [-180, 180) wrapped
// before fuzzing.
func (f *locationFuzzer) Fuzz(pubkey string, lat, lon float64) (float64, float64) {
	if f == nil || (lat == 0 && lon == 0) {
		return lat, lon
	}
	if math.IsNaN(lat) || math.IsNaN(lon) || math.IsInf(lat, 0) || math.IsInf(lon, 0) {
		return lat, lon
	}
	lat = math.Max(-90, math.Min(90, lat))
	lon = wrapLon(lon)
	// Cell selection uses the latitude clamped to ±locGridMaxLat.
	glat := math.Max(-locGridMaxLat, math.Min(locGridMaxLat, lat))
	pk := strings.ToLower(pubkey)

	a, ok := f.anchorFor(pk)
	if !ok || !a.contains(glat, lon) {
		a = f.cellAt(glat, lon) // HMAC work, outside the lock
		f.storeAnchor(pk, a)
	}

	sLat, sLon := a.sLat, a.sLon
	if glat != lat {
		// Polar node: the cell was chosen at (glat, lon), so the served
		// point is placed relative to the exact position with the same
		// bearing and distance it has from (glat, lon). The distance bound
		// carries over unchanged.
		d := haversineKm(glat, lon, sLat, sLon) * 1000
		brg := initialBearing(glat, lon, sLat, sLon)
		sLat, sLon = destinationPoint(lat, lon, brg, d)
	}
	return avoidNullIsland(sLat, sLon)
}

// Client is the /api/config/client view.
func (f *locationFuzzer) Client() ClientLocationPrivacy {
	return ClientLocationPrivacy{Enabled: f.Enabled(), RadiusM: f.RadiusM()}
}

// clientLocationPrivacy is the /api/config/client field: nil (key omitted,
// the payload unchanged from upstream) when the feature is off.
func clientLocationPrivacy(f *locationFuzzer) *ClientLocationPrivacy {
	if !f.Enabled() {
		return nil
	}
	c := f.Client()
	return &c
}

// setLocationFuzzer installs the validated fuzzer (main, tests).
func (c *Config) setLocationFuzzer(f *locationFuzzer) {
	if c != nil {
		c.locFuzz = f
	}
}

// LocationFuzzer is the installed fuzzer; nil (identity) when disabled or c is nil.
func (c *Config) LocationFuzzer() *locationFuzzer {
	if c == nil {
		return nil
	}
	return c.locFuzz
}
