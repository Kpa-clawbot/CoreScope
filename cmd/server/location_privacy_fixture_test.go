package main

// Fixture for the location-privacy leak test (location_privacy_leak_test.go):
// a full Server (every route RegisterRoutes knows, user management, planner,
// route LOS, companion coverage and RF samples all on) with the location
// fuzzer installed, over a database seeded with nodes, adverts, paths and
// companion GPS rows at exact positions no response may reveal.
//
// The exact positions carry six decimals (what an advert holds: int32 × 1e-6)
// and digits that do not occur anywhere else in the fixture, so finding one in
// a response body is a leak, never a coincidence.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/users"
)

const lkRadiusM = 1000.0

// lkPoint is one exact position the server holds and must not serve.
// pubkey is the node Fuzz is keyed with: the node itself, or for a companion
// GPS point the companion that reported it.
type lkPoint struct {
	name     string
	pubkey   string
	lat, lon float64
	gps      bool // a companion GPS point (client_* tables) or derived from them, not a node position
}

func (p lkPoint) geo() geoPoint { return geoPoint{Lat: p.lat, Lon: p.lon} }

// lkAdvert is one seeded ADVERT that carries a location.
type lkAdvert struct {
	pubkey    string
	hash      string
	rawHex    []string // the transmission's frame and every observation's
	sigHex    string
	latHex    string // the four location bytes as they are on the wire
	lonHex    string
	latLonHex string
}

type lkFixture struct {
	*authFixture
	store *PacketStore
	fuzz  *locationFuzzer
	// truncated are located adverts whose frame ends inside the signature:
	// no appdata, so no position bytes, but signature bytes that narrow a
	// brute force of the position. They must be served like located ones.
	truncated []lkAdvert
	user      *client // logged in; linked the companion, claimed the repeater

	points  []lkPoint
	adverts []lkAdvert

	repeater, relay, companion, observer, inactive, ghost string // pubkeys
	msgHash                                               string
	advertTxID                                            int
	nextTS                                                uint32
}

// Node identities: real Ed25519 keys, so the adverts carry signatures that
// verify (a valid signature over a known pubkey and timestamp is what lets a
// reader brute-force the location back out of a redacted advert).
var (
	lkRepeaterKey = lkKey(0x11)
	lkRelayKey    = lkKey(0x22)
	lkObserverKey = lkKey(0x33)
	lkInactiveKey = lkKey(0x44)
	lkGhostKey    = lkKey(0x55)
	// The companion is companionKey (companion_handlers_test.go), so the
	// fixture user can link it through the real challenge flow.
)

func lkKey(seed byte) ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, 32))
}

// lkPoints lists the exact positions, first the node positions, then where
// the companion was. The nodes sit 13 to 60 km apart and over 2 × radius from
// every airport of the IATA table and every request point of the leak test:
// a coordinate served near one of them can only be its own, and they are
// still close enough for links and reach.
func lkPoints() []lkPoint {
	comp := pubHex(companionKey)
	pts := []lkPoint{
		{name: "repeater", pubkey: pubHex(lkRepeaterKey), lat: 37.423123, lon: -122.084567},
		{name: "relay", pubkey: pubHex(lkRelayKey), lat: 37.564219, lon: -122.327194},
		{name: "companion", pubkey: comp, lat: 37.487654, lon: -122.213876},
		{name: "observer", pubkey: pubHex(lkObserverKey), lat: 37.301987, lon: -121.853765},
		{name: "inactive", pubkey: pubHex(lkInactiveKey), lat: 37.655382, lon: -122.456918},
		// Where the companion was: on the road, at home (its advertised
		// position), somewhere it heard nothing, and at three spots from
		// which it heard the ghost repeater.
		{name: "gps-track", pubkey: comp, lat: 37.445987, lon: -122.161728, gps: true},
		{name: "gps-home", pubkey: comp, lat: 37.487654, lon: -122.213876, gps: true},
		{name: "gps-gap", pubkey: comp, lat: 37.479321, lon: -122.129765, gps: true},
		{name: "gps-spot1", pubkey: comp, lat: 37.521345, lon: -122.051234, gps: true},
		{name: "gps-spot2", pubkey: comp, lat: 37.524876, lon: -122.047619, gps: true},
		{name: "gps-spot3", pubkey: comp, lat: 37.518765, lon: -122.045432, gps: true},
	}
	// The position /api/nodes/reported derives for the ghost from the three
	// spots (all heard equally well) is as secret as they are.
	snr := lkGhostSNR
	var spots []rxPoint
	for _, p := range pts[len(pts)-3:] {
		spots = append(spots, rxPoint{Lat: p.lat, Lon: p.lon, SNR: &snr})
	}
	est, _ := estimatePosition(spots)
	return append(pts, lkPoint{name: "ghost-estimate", pubkey: pubHex(lkGhostKey), lat: est.Lat, lon: est.Lon, gps: true})
}

const lkGhostSNR = 2.5

func (f *lkFixture) point(name string) lkPoint {
	for _, p := range f.points {
		if p.name == name {
			return p
		}
	}
	panic("no fixture point " + name)
}

// fuzzed is the position the server may serve for p.
func (f *lkFixture) fuzzed(p lkPoint) geoPoint {
	lat, lon := lkFuzz(f.fuzz, p)
	return geoPoint{Lat: lat, Lon: lon}
}

// lkFuzz serves p as the server does: the ghost's computed estimate through
// FuzzEstimate, every other point through Fuzz. Fixture keys are well formed.
func lkFuzz(f *locationFuzzer, p lkPoint) (float64, float64) {
	fuzz := f.Fuzz
	if p.name == "ghost-estimate" {
		fuzz = f.FuzzEstimate
	}
	lat, lon, ok := fuzz(p.pubkey, p.lat, p.lon)
	if !ok {
		panic("fixture pubkey refused by the fuzzer: " + p.name)
	}
	return lat, lon
}

// lkDistM is the great-circle distance in metres.
func lkDistM(a, b geoPoint) float64 { return geoDistKm(a, b) * 1000 }

// lkCoordNeedles is how an exact coordinate would show in a body: its
// absolute value at 3 to 6 decimals (rounded and truncated) and as the
// integer × 1e6 an advert holds.
func lkCoordNeedles(v float64) (decimals []string, micro string) {
	v = math.Abs(v)
	seen := map[string]bool{}
	for d := 3; d <= 6; d++ {
		scale := math.Pow(10, float64(d))
		for _, s := range []string{
			strconv.FormatFloat(v, 'f', d, 64),
			strconv.FormatFloat(math.Floor(v*scale+1e-9)/scale, 'f', d, 64),
		} {
			if !seen[s] {
				seen[s] = true
				decimals = append(decimals, s)
			}
		}
	}
	return decimals, strconv.FormatInt(int64(math.Round(v*1e6)), 10)
}

// newLeakFuzzer builds the fuzzer the fixture installs. While Fuzz is the
// identity (the contract stub) any secret does. Once it moves points, the
// secret is the first of a fixed list for which the checks cannot misfire on
// a correct server:
//   - no fuzzed coordinate reads like an exact one at 3+ decimals,
//   - every fuzzed distance between two points differs from the exact one by
//     more than lkDistSlackKm (lkGPSDistSlackKm when a GPS point is involved).
//
// The choice depends only on the fuzzer, so runs are reproducible.
//
// Those two rules cannot foresee every misfire: the leak test holds each
// served distance, read as kilometres and as miles, against the exact
// distance of every pair of points, and a fuzzed distance, or a sum or mean of
// several, lands on one of them at two decimals for some secrets (a different
// field and a different pair each time, where a real leak shows under every
// secret). Ruling those out here leaves no secret at all, so the list starts
// at lkSecretStart, the first secret the leak test accepts in full. Move it
// only after reading what the leak test reports for the secrets before it.
func newLeakFuzzer(t testing.TB, points []lkPoint) *locationFuzzer {
	t.Helper()
	var first *locationFuzzer
	for i := lkSecretStart; i < 20000; i++ {
		f, err := newLocationFuzzer(&LocationPrivacyConfig{
			Enabled: true, RadiusM: lkRadiusM,
			Secret: fmt.Sprintf("location-privacy-leak-test-secret-%04d", i),
		})
		if err != nil || f == nil {
			t.Fatalf("newLocationFuzzer: %v (fuzzer %v)", err, f)
		}
		if first == nil {
			first = f
		}
		if lkFuzzerSuits(f, points) {
			return f
		}
	}
	t.Fatal("no test secret gives fuzzed positions the leak checks can tell from exact ones")
	return nil
}

// lkSecretStart is the first candidate secret of newLeakFuzzer. With the
// per-node, per-purpose derivation (csloc/v2) the secrets that pass
// lkFuzzerSuits below 20000 are 1289, 8502, 8661, 8710 and 11868; 1289 and
// 8661 each trip one coincidence on /api/analytics/distance (a hop distance
// reading as relay to gps-spot2; a path total reading as inactive to
// ghost-estimate), the other three pass.
const lkSecretStart = 8502

// How much a fuzzed distance must differ from the exact one: between two
// nodes, more than the dist check's tolerance for a distance served with one
// decimal; from a node to a GPS point, for one served with two.
const (
	lkDistSlackKm    = 0.06
	lkGPSDistSlackKm = 0.006
)

func lkFuzzerSuits(f *locationFuzzer, points []lkPoint) bool {
	fz := make([]geoPoint, len(points))
	identity := true
	for i, p := range points {
		la, lo := lkFuzz(f, p)
		fz[i] = geoPoint{Lat: la, Lon: lo}
		if la != p.lat || lo != p.lon {
			identity = false
		}
	}
	if identity {
		return true // the stub: nothing to tell apart yet
	}
	var needles []string
	for _, p := range points {
		for _, v := range []float64{p.lat, p.lon} {
			dec, micro := lkCoordNeedles(v)
			needles = append(append(needles, dec...), micro)
		}
	}
	for i := range points {
		for _, v := range []float64{fz[i].Lat, fz[i].Lon} {
			s := strconv.FormatFloat(math.Abs(v), 'f', 9, 64)
			micro := strconv.FormatInt(int64(math.Round(math.Abs(v)*1e6)), 10)
			for _, n := range needles {
				if strings.HasPrefix(s, n) || micro == n {
					return false
				}
			}
		}
	}
	for i, p := range points {
		for j := range points[:i] {
			q := points[j]
			if q.geo() == p.geo() || (p.gps && q.gps) {
				continue
			}
			slack := lkDistSlackKm
			if p.gps || q.gps {
				slack = lkGPSDistSlackKm
			}
			if math.Abs(geoDistKm(fz[i], fz[j])-geoDistKm(p.geo(), q.geo())) <= slack {
				return false
			}
		}
	}
	return true
}

// lkClientSchema is the ingestor's DDL (cmd/ingestor/db.go applySchema) for
// the companion tables; the server only reads them.
const lkClientSchema = `
	CREATE TABLE client_receptions (
		id INTEGER PRIMARY KEY AUTOINCREMENT, rx_pubkey TEXT NOT NULL, heard_key TEXT NOT NULL,
		heard_keylen INTEGER NOT NULL, rssi INTEGER, snr REAL, lat REAL NOT NULL, lon REAL NOT NULL,
		pos_acc_m REAL, rx_at TEXT NOT NULL, ingested_at TEXT NOT NULL, src TEXT NOT NULL,
		UNIQUE(rx_pubkey, heard_key, rx_at));
	CREATE INDEX idx_client_recept_heard_geo ON client_receptions(heard_key, heard_keylen, lat, lon);
	CREATE INDEX idx_client_recept_latlon ON client_receptions(lat, lon);
	CREATE INDEX idx_client_recept_rxat ON client_receptions(rx_at);
	CREATE TABLE client_observers (pubkey TEXT PRIMARY KEY, name TEXT, last_seen TEXT);
	CREATE TABLE client_rx_observations (
		id INTEGER PRIMARY KEY AUTOINCREMENT, rx_pubkey TEXT NOT NULL, rx_at TEXT NOT NULL,
		ingested_at TEXT NOT NULL, pkt_hash TEXT NOT NULL, route_type INTEGER NOT NULL,
		payload_type INTEGER NOT NULL, code1 TEXT, code2 TEXT, scope_name TEXT,
		hash_size INTEGER NOT NULL, hop_count INTEGER NOT NULL, path_json TEXT, forwarder TEXT,
		snr REAL, rssi INTEGER, lat REAL NOT NULL, lon REAL NOT NULL, pos_acc_m REAL,
		UNIQUE(rx_pubkey, pkt_hash, rx_at));
	CREATE TABLE client_rf_samples (
		id INTEGER PRIMARY KEY AUTOINCREMENT, rx_pubkey TEXT NOT NULL, sampled_at TEXT NOT NULL,
		ingested_at TEXT NOT NULL, lat REAL NOT NULL, lon REAL NOT NULL, pos_acc_m REAL,
		stationary INTEGER NOT NULL DEFAULT 0, uptime_secs INTEGER NOT NULL, battery_mv INTEGER,
		queue_len INTEGER, errors INTEGER, noise_floor INTEGER, last_rssi INTEGER, last_snr REAL,
		tx_air_secs INTEGER, rx_air_secs INTEGER, recv INTEGER, sent INTEGER, flood_rx INTEGER,
		direct_rx INTEGER, flood_tx INTEGER, direct_tx INTEGER, recv_errors INTEGER,
		UNIQUE(rx_pubkey, sampled_at));
	CREATE INDEX idx_crf_prune ON client_rf_samples(sampled_at);
	CREATE INDEX idx_crf_track ON client_rf_samples(rx_pubkey, sampled_at);
	CREATE TABLE node_declared_regions (
		id INTEGER PRIMARY KEY AUTOINCREMENT, target TEXT NOT NULL, rx_pubkey TEXT NOT NULL,
		observed_at TEXT NOT NULL, ingested_at TEXT NOT NULL, regions_csv TEXT NOT NULL,
		truncated INTEGER NOT NULL DEFAULT 0, lat REAL, lon REAL, pos_acc_m REAL,
		repeater_clock INTEGER, UNIQUE(target, rx_pubkey, observed_at));
	CREATE INDEX idx_ndr_target ON node_declared_regions(target, observed_at);
	CREATE TABLE client_node_names (
		id INTEGER PRIMARY KEY AUTOINCREMENT, target TEXT NOT NULL, rx_pubkey TEXT NOT NULL,
		observed_at TEXT NOT NULL, ingested_at TEXT NOT NULL, name TEXT NOT NULL,
		lat REAL, lon REAL, pos_acc_m REAL, repeater_clock INTEGER,
		UNIQUE(target, rx_pubkey, observed_at));
	CREATE INDEX idx_cnn_target ON client_node_names(target, observed_at);
`

// newLKFixture builds the server and seeds it. The packet store is loaded
// from the database, as at startup.
func newLKFixture(t *testing.T) *lkFixture {
	t.Helper()
	f := &lkFixture{
		points:    lkPoints(),
		repeater:  pubHex(lkRepeaterKey),
		relay:     pubHex(lkRelayKey),
		companion: pubHex(companionKey),
		observer:  pubHex(lkObserverKey),
		inactive:  pubHex(lkInactiveKey),
		ghost:     pubHex(lkGhostKey),
		nextTS:    1_790_000_000,
	}
	f.fuzz = newLeakFuzzer(t, f.points)

	db := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	// The server-side migrations the ingestor applies on top of the base
	// tables (neighbor_edges, inactive_nodes, radios, …), then its companion
	// tables.
	if err := dbschema.Apply(db.conn, nil); err != nil {
		t.Fatalf("dbschema.Apply: %v", err)
	}
	mustExecDB(t, db, lkClientSchema)
	// setupTestDB fixed the schema flags before the migrations ran.
	if err := db.detectSchema(context.Background(), db.conn); err != nil {
		t.Fatalf("detectSchema: %v", err)
	}
	f.seed(t, db)

	big := func(v float64) *float64 { return &v }
	cfg := &Config{
		Port:             3000,
		APIKey:           testAPIKey,
		Planner:          &PlannerConfig{Enabled: true},
		LOS:              &LOSConfig{Enabled: true},
		ClientRxCoverage: &ClientRxCoverageConfig{Enabled: true},
		ClientRfSamples:  &ClientRfSamplesConfig{Enabled: true},
		// The one consumer of exact positions the feature keeps: every seeded
		// point is inside, with the edges far from all of them.
		GeoFilter: &GeoFilterConfig{LatMin: big(35), LatMax: big(40), LonMin: big(-125), LonMax: big(-119)},
		LocationPrivacy: &LocationPrivacyConfig{
			Enabled: true, RadiusM: lkRadiusM, Secret: "installed-below-with-setLocationFuzzer",
		},
	}
	cfg.setLocationFuzzer(f.fuzz)

	srv := NewServer(db, cfg, NewHub())
	store := NewPacketStore(db, nil)
	store.config = cfg
	// Graph before packets, as main does (#1643).
	store.graph.Store(loadNeighborEdgesFromDB(db.conn))
	if err := store.Load(); err != nil {
		t.Fatalf("store.Load: %v", err)
	}
	if !store.WaitIndexesReady(10 * time.Second) {
		t.Fatal("background indexes never became ready")
	}
	srv.store = store
	f.store = store
	// What main does once the load is done: /api/healthz reports ready, and
	// the distance index (built on the first request, which answers 202) is
	// there.
	readiness.Store(1)
	t.Cleanup(func() { readiness.Store(0) })
	store.TriggerDistanceIndexBuild()
	waitForTest(t, "distance index", store.DistanceIndexBuilt)

	// User management with every optional block on, so their routes exist.
	a, fake := newTestAuthService(t)
	a.set.proposals = defaultProposalSettings()
	a.set.observerLogins = observerLoginSettings{enabled: true, hookSecret: hkSecret, key: oaKey,
		host: "mqtt.example.org", port: 8883, transport: "tcp", tls: true}
	idx, err := newObserverLoginIndex(a.st, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.obsIdx = idx
	if a.sealbox, err = users.NewSecretBox(oaKey); err != nil {
		t.Fatal(err)
	}
	a.set.notify = notifySettings{enabled: true, interval: time.Minute, perUserPerDay: 5,
		maxMailsPerDay: 100, maxWatchesPerUser: 10}
	a.notify = newNotifier(a, serverNotifySource{s: srv}, time.Now)
	srv.auth = a

	// Terrain: route LOS over resident flat tiles, the planner over the test
	// terrain. Node data is the real server source in both. The planner waits
	// for its job instead of answering 202, so one request gets the result.
	src, _, _ := newSRTMTest(t, 0, nil)
	for _, p := range f.points {
		src.put(srtmKeyOf(p.lat, p.lon), elevFlatGrid(120))
	}
	elev := newElevationService(src)
	elev.nodes = serverPlannerData{srv}.nodes
	elev.visibility = srv
	elev.wait = 10 * time.Second
	srv.elevation = elev
	pc, _ := cfg.Planner.resolved()
	planner := newPlannerService(pc, srv, nil)
	planner.src = &fakeTerrain{resident: true}
	planner.fastWait = 60 * time.Second
	planner.limit.rl = newRateLimiter(1_000_000, time.Minute)
	planner.global = newRateLimiter(1_000_000, time.Minute)
	t.Cleanup(planner.Close)
	srv.planner = planner

	router := mux.NewRouter()
	srv.RegisterRoutes(router)
	f.authFixture = &authFixture{srv: srv, router: router, fake: fake, st: a.st}

	// The user: owns the companion (linked with its key), maintains the
	// repeater and watches it.
	f.user = f.registerAndActivate(t, "leak@example.org", "Leak Tester", pw)
	tok := f.deviceToken(t, "leak@example.org", pw, "Pixel").Token
	expectStatus(t, f.linkCompanion(t, tok, companionKey, "Car"), 200)
	if _, err := f.st.ClaimRepeater(f.user.me.ID, f.repeater); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.SaveRepeaterInfo(f.user.me.ID, f.repeater, users.RepeaterEdit{Status: users.RepeaterStatusMaintenance}); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, f.do("PUT", "/api/account/notifications/watches/"+f.repeater, map[string]any{}, as(f.user)), 200)
	return f
}

// lkHop is the 3-byte path prefix of a node.
func lkHop(pubkey string) []byte {
	b, _ := hex.DecodeString(pubkey[:6])
	return b
}

// lkFrame assembles a packet: header, path (3-byte hops), payload.
func lkFrame(header byte, hops [][]byte, payload []byte) string {
	buf := []byte{header, byte(2<<6 | len(hops))}
	for _, h := range hops {
		buf = append(buf, h...)
	}
	return strings.ToUpper(hex.EncodeToString(append(buf, payload...)))
}

// lkAdvertPayload is a signed ADVERT payload with a location and a name
// (firmware/docs/payloads.md): pubkey, timestamp, signature, then appdata =
// flags, lat and lon as int32 × 1e6 little-endian, name.
func lkAdvertPayload(key ed25519.PrivateKey, advType byte, lat, lon float64, name string, ts uint32) []byte {
	pub := key.Public().(ed25519.PublicKey)
	appdata := []byte{advType | 0x10 | 0x80}
	appdata = binary.LittleEndian.AppendUint32(appdata, uint32(int32(math.Round(lat*1e6))))
	appdata = binary.LittleEndian.AppendUint32(appdata, uint32(int32(math.Round(lon*1e6))))
	appdata = append(appdata, name...)
	msg := append([]byte{}, pub...)
	msg = binary.LittleEndian.AppendUint32(msg, ts)
	msg = append(msg, appdata...)
	out := append([]byte{}, pub...)
	out = binary.LittleEndian.AppendUint32(out, ts)
	out = append(out, ed25519.Sign(key, msg)...)
	return append(out, appdata...)
}

type lkObs struct {
	observerIdx int
	hops        [][]byte
	resolved    []string
	snr, rssi   float64
}

// insertPacket stores one transmission (its frame is the first observation's)
// and its observations, decoded as the ingestor would store it.
func (f *lkFixture) insertPacket(t *testing.T, db *DB, header byte, payload []byte, decoded map[string]any,
	channelHash any, seen time.Time, obs []lkObs) (txID int, hash string, frames []string) {
	t.Helper()
	for _, o := range obs {
		frames = append(frames, lkFrame(header, o.hops, payload))
	}
	raw := frames[0]
	pkt, err := DecodePacket(raw, true)
	if err != nil {
		t.Fatalf("fixture frame does not decode: %v", err)
	}
	if decoded == nil {
		b, _ := json.Marshal(pkt.Payload)
		json.Unmarshal(b, &decoded)
	}
	dj, _ := json.Marshal(decoded)
	hash = ComputeContentHash(raw)
	var from any
	if pk, _ := decoded["pubKey"].(string); pk != "" {
		from = pk
	}
	res, err := db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json, channel_hash, from_pubkey)
		VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?)`, raw, hash, seen.Format(time.RFC3339), pkt.Header.RouteType, pkt.Header.PayloadType, string(dj), channelHash, from)
	if err != nil {
		t.Fatalf("insert transmission: %v", err)
	}
	id, _ := res.LastInsertId()
	for i, o := range obs {
		op, err := DecodePacket(frames[i], false)
		if err != nil {
			t.Fatalf("fixture observation frame does not decode: %v", err)
		}
		hops := op.Path.Hops
		if hops == nil {
			hops = []string{}
		}
		pj, _ := json.Marshal(hops)
		var rp any
		if len(o.resolved) > 0 {
			b, _ := json.Marshal(o.resolved)
			rp = string(b)
		}
		if _, err := db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, direction, snr, rssi, score, path_json, timestamp, resolved_path, raw_hex)
			VALUES (?, ?, 'rx', ?, ?, 1, ?, ?, ?, ?)`, id, o.observerIdx, o.snr, o.rssi, string(pj), seen.Add(time.Duration(i)*time.Second).Unix(), rp, frames[i]); err != nil {
			t.Fatalf("insert observation: %v", err)
		}
	}
	return int(id), hash, frames
}

// insertAdvert stores a located ADVERT from key and records what of it must
// never be served.
func (f *lkFixture) insertAdvert(t *testing.T, db *DB, key ed25519.PrivateKey, advType byte, p lkPoint, name string,
	seen time.Time, obs []lkObs) int {
	t.Helper()
	f.nextTS += 60
	payload := lkAdvertPayload(key, advType, p.lat, p.lon, name, f.nextTS)
	header := byte(4<<2 | 1) // ADVERT, FLOOD
	id, hash, frames := f.insertPacket(t, db, header, payload, nil, nil, seen, obs)
	loc := strings.ToUpper(hex.EncodeToString(payload[101:109]))
	f.adverts = append(f.adverts, lkAdvert{
		pubkey: pubHex(key), hash: hash, rawHex: frames,
		sigHex: strings.ToUpper(hex.EncodeToString(payload[36:100])),
		latHex: loc[:8], lonHex: loc[8:], latLonHex: loc,
	})
	return id
}

func (f *lkFixture) seed(t *testing.T, db *DB) {
	t.Helper()
	now := time.Now().UTC()
	ts := func(ago time.Duration) string { return now.Add(-ago).Format(time.RFC3339) }
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.conn.Exec(q, args...); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	rep, rel, comp, obs, ina := f.point("repeater"), f.point("relay"), f.point("companion"), f.point("observer"), f.point("inactive")
	track, home, gap := f.point("gps-track"), f.point("gps-home"), f.point("gps-gap")

	for _, n := range []struct {
		p    lkPoint
		name string
		role string
		ago  time.Duration
	}{
		{rep, "LeakRepeater", "repeater", 5 * time.Minute},
		{rel, "LeakRelay", "repeater", 6 * time.Minute},
		{comp, "LeakCompanion", "companion", 7 * time.Minute},
		{obs, "LeakObserver", "repeater", 8 * time.Minute},
	} {
		exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count, battery_mv, temperature_c)
			VALUES (?, ?, ?, ?, ?, ?, '2026-01-01T00:00:00Z', 12, 3900, 21.5)`, n.p.pubkey, n.name, n.role, n.p.lat, n.p.lon, ts(n.ago))
	}
	exec(`INSERT INTO inactive_nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
		VALUES (?, 'LeakInactive', 'repeater', ?, ?, ?, '2026-01-01T00:00:00Z', 3)`, ina.pubkey, ina.lat, ina.lon, ts(40*24*time.Hour))

	// Two observers, both nodes with a position: the dedicated one and the relay.
	exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count, model, firmware, noise_floor, last_packet_at)
		VALUES (?, 'LeakObserver', 'SJC', ?, '2026-01-01T00:00:00Z', 40, 'Heltec V3', '1.16.0', -112, ?)`, obs.pubkey, ts(time.Minute), ts(time.Minute))
	exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count, model, firmware, noise_floor, last_packet_at)
		VALUES (?, 'LeakRelay', 'SFO', ?, '2026-01-01T00:00:00Z', 20, 'RAK4631', '1.16.0', -108, ?)`, rel.pubkey, ts(2*time.Minute), ts(2*time.Minute))
	for i := 0; i < 3; i++ {
		exec(`INSERT INTO observer_metrics (observer_id, timestamp, noise_floor, tx_air_secs, rx_air_secs, recv_errors, battery_mv, packets_sent, packets_recv)
			VALUES (?, ?, -112, ?, ?, 0, 4010, ?, ?)`, obs.pubkey, ts(time.Duration(30-10*i)*time.Minute), 10*i, 20*i, 5*i, 50*i)
	}

	hRep, hRel := lkHop(rep.pubkey), lkHop(rel.pubkey)
	const obsDedicated, obsRelay = 1, 2
	// Adverts with a location: the repeater's (heard through the relay and
	// directly), the companion's, the relay's and the observer's.
	f.advertTxID = f.insertAdvert(t, db, lkRepeaterKey, 2, rep, "LeakRepeater", now.Add(-50*time.Minute), []lkObs{
		{observerIdx: obsDedicated, hops: [][]byte{hRel}, resolved: []string{rel.pubkey}, snr: 9.5, rssi: -88},
		{observerIdx: obsRelay, snr: 11.25, rssi: -79},
	})
	f.insertAdvert(t, db, companionKey, 1, comp, "LeakCompanion", now.Add(-40*time.Minute), []lkObs{
		{observerIdx: obsDedicated, hops: [][]byte{hRep, hRel}, resolved: []string{rep.pubkey, rel.pubkey}, snr: 7.5, rssi: -93},
		{observerIdx: obsRelay, hops: [][]byte{hRep}, resolved: []string{rep.pubkey}, snr: 8.25, rssi: -90},
	})
	f.insertAdvert(t, db, lkRelayKey, 2, rel, "LeakRelay", now.Add(-30*time.Minute), []lkObs{
		{observerIdx: obsDedicated, hops: [][]byte{hRep}, resolved: []string{rep.pubkey}, snr: 6.75, rssi: -96},
		{observerIdx: obsDedicated, snr: 5.5, rssi: -99},
	})
	f.insertAdvert(t, db, lkObserverKey, 2, obs, "LeakObserver", now.Add(-20*time.Minute), []lkObs{
		{observerIdx: obsRelay, hops: [][]byte{hRep}, resolved: []string{rep.pubkey}, snr: 4.25, rssi: -101},
		{observerIdx: obsRelay, snr: 10.5, rssi: -84},
	})
	// A located advert of the repeater cut short after 40 signature bytes:
	// the frame has no appdata flags byte to say whether it is located.
	f.nextTS += 60
	full := lkAdvertPayload(lkRepeaterKey, 2, rep.lat, rep.lon, "LeakRepeater", f.nextTS)
	cut := full[:36+40]
	_, cutHash, cutFrames := f.insertPacket(t, db, byte(4<<2|1), cut, nil, nil, now.Add(-15*time.Minute), []lkObs{
		{observerIdx: obsDedicated, hops: [][]byte{hRel}, resolved: []string{rel.pubkey}, snr: 7.25, rssi: -91},
		{observerIdx: obsRelay, snr: 9.75, rssi: -83},
	})
	cutLoc := strings.ToUpper(hex.EncodeToString(full[101:109]))
	f.truncated = append(f.truncated, lkAdvert{
		pubkey: rep.pubkey, hash: cutHash, rawHex: cutFrames,
		sigHex: strings.ToUpper(hex.EncodeToString(cut[36:])),
		latHex: cutLoc[:8], lonHex: cutLoc[8:], latLonHex: cutLoc,
	})
	// A channel message relayed over both repeaters: paths, subpaths, channels.
	msg := append([]byte{0xd9, 0x4a, 0x7c}, []byte("leak-fixture-ciphertext-16b!")...)
	_, f.msgHash, _ = f.insertPacket(t, db, byte(5<<2|1), msg, map[string]any{
		"type": "CHAN", "channel": "#test", "text": "Alice: no coordinates here", "sender": "Alice",
	}, "#test", now.Add(-10*time.Minute), []lkObs{
		{observerIdx: obsDedicated, hops: [][]byte{hRep, hRel}, resolved: []string{rep.pubkey, rel.pubkey}, snr: 3.5, rssi: -104},
		{observerIdx: obsRelay, hops: [][]byte{hRep}, resolved: []string{rep.pubkey}, snr: 6.5, rssi: -95},
	})

	for _, e := range [][2]string{{rep.pubkey, rel.pubkey}, {rep.pubkey, obs.pubkey}, {rel.pubkey, obs.pubkey}, {comp.pubkey, rep.pubkey}} {
		a, b := e[0], e[1]
		if a > b {
			a, b = b, a
		}
		exec(`INSERT INTO neighbor_edges (node_a, node_b, count, last_seen) VALUES (?, ?, 25, ?)`, a, b, ts(10*time.Minute))
	}

	// The companion's GPS: receptions of both repeaters (by full key and by
	// 3-byte prefix) along its track and at home, RF samples at the same
	// points, and what it relayed about other nodes from there.
	exec(`INSERT INTO client_observers (pubkey, name, last_seen) VALUES (?, 'LeakCompanion', ?)`, comp.pubkey, ts(time.Minute))
	n := 0
	for _, p := range []lkPoint{track, home} {
		for _, heard := range []string{rep.pubkey, rel.pubkey} {
			for _, keylen := range []int{32, 3} {
				n++
				exec(`INSERT INTO client_receptions (rx_pubkey, heard_key, heard_keylen, rssi, snr, lat, lon, pos_acc_m, rx_at, ingested_at, src)
					VALUES (?, ?, ?, -97, ?, ?, ?, 4.5, ?, ?, 'rxlog')`, comp.pubkey, heard[:2*keylen], keylen, -3.25-float64(n)/4,
					p.lat, p.lon, ts(time.Duration(n)*time.Minute), ts(time.Duration(n)*time.Minute))
			}
		}
	}
	// RF samples while moving: the track, home, and a place where nothing was
	// heard (a coverage gap).
	for i := 0; i < 9; i++ {
		p := []lkPoint{track, home, gap}[i/3]
		exec(`INSERT INTO client_rf_samples (rx_pubkey, sampled_at, ingested_at, lat, lon, pos_acc_m, stationary, uptime_secs,
				battery_mv, queue_len, errors, noise_floor, last_rssi, last_snr, tx_air_secs, rx_air_secs, recv, sent,
				flood_rx, direct_rx, flood_tx, direct_tx, recv_errors)
			VALUES (?, ?, ?, ?, ?, 4.5, 0, ?, 4000, 0, 0, ?, -95, 6.5, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
			comp.pubkey, ts(time.Duration(90-10*i)*time.Minute), ts(time.Duration(90-10*i)*time.Minute), p.lat, p.lon,
			1000+600*i, -113+i, 5*i, 30*i, 40*i, 8*i, 30*i, 10*i, 6*i, 2*i)
	}
	exec(`INSERT INTO client_rx_observations (rx_pubkey, rx_at, ingested_at, pkt_hash, route_type, payload_type, hash_size, hop_count,
			path_json, forwarder, snr, rssi, lat, lon, pos_acc_m)
		VALUES (?, ?, ?, ?, 1, 5, 3, 1, ?, ?, -4.5, -99, ?, ?, 4.5)`, comp.pubkey, ts(9*time.Minute), ts(9*time.Minute), f.msgHash,
		`["`+strings.ToUpper(rep.pubkey[:6])+`"]`, rep.pubkey[:6], track.lat, track.lon)
	exec(`INSERT INTO node_declared_regions (target, rx_pubkey, observed_at, ingested_at, regions_csv, truncated, lat, lon, pos_acc_m, repeater_clock)
		VALUES (?, ?, ?, ?, 'us-ca,us-ca-bay', 0, ?, ?, 4.5, ?)`, rep.pubkey, comp.pubkey, ts(15*time.Minute), ts(15*time.Minute), track.lat, track.lon, now.Unix())
	// A repeater known only from the companion: it reported its name, and
	// answered a discover from three spots, which is enough for a position
	// estimate (GET /api/nodes/reported).
	exec(`INSERT INTO client_node_names (target, rx_pubkey, observed_at, ingested_at, name, lat, lon, pos_acc_m, repeater_clock)
		VALUES (?, ?, ?, ?, 'LeakGhost', ?, ?, 4.5, ?)`, f.ghost, comp.pubkey, ts(16*time.Minute), ts(16*time.Minute),
		f.point("gps-spot1").lat, f.point("gps-spot1").lon, now.Unix())
	for i, name := range []string{"gps-spot1", "gps-spot2", "gps-spot3"} {
		p := f.point(name)
		exec(`INSERT INTO client_receptions (rx_pubkey, heard_key, heard_keylen, rssi, snr, lat, lon, pos_acc_m, rx_at, ingested_at, src)
			VALUES (?, ?, 32, -91, ?, ?, ?, 4.5, ?, ?, 'discover')`, comp.pubkey, f.ghost, lkGhostSNR, p.lat, p.lon,
			ts(time.Duration(20+i)*time.Minute), ts(time.Duration(20+i)*time.Minute))
	}
}

// TestLocationPrivacyFixture checks the fixture is what the leak test assumes:
// the fuzzer is on, the adverts are real (they decode to the exact position
// with a signature that verifies) and the database holds the exact positions.
func TestLocationPrivacyFixture(t *testing.T) {
	f := newLKFixture(t)
	if !f.srv.cfg.LocationFuzzer().Enabled() || f.srv.cfg.LocationFuzzer().RadiusM() != lkRadiusM {
		t.Fatalf("fuzzer not installed: enabled=%v radius=%v", f.srv.cfg.LocationFuzzer().Enabled(), f.srv.cfg.LocationFuzzer().RadiusM())
	}
	if len(f.adverts) != 4 {
		t.Fatalf("adverts = %d, want 4", len(f.adverts))
	}
	byKey := map[string]lkPoint{}
	for _, p := range f.points {
		if !p.gps {
			byKey[p.pubkey] = p
		}
	}
	for _, a := range f.adverts {
		want := byKey[a.pubkey]
		for _, raw := range a.rawHex {
			pkt, err := DecodePacket(raw, true)
			if err != nil {
				t.Fatalf("advert %s: %v", a.hash, err)
			}
			pl := pkt.Payload
			if pl.Lat == nil || pl.Lon == nil || *pl.Lat != want.lat || *pl.Lon != want.lon {
				t.Errorf("advert %s decodes to %v,%v; want %v,%v", a.hash, pl.Lat, pl.Lon, want.lat, want.lon)
			}
			if pl.SignatureValid == nil || !*pl.SignatureValid {
				t.Errorf("advert %s: signature does not verify", a.hash)
			}
			if !strings.EqualFold(pl.Signature, a.sigHex) || !strings.Contains(raw, a.latLonHex) {
				t.Errorf("advert %s: recorded signature or location bytes are not the frame's", a.hash)
			}
		}
		var dj string
		if err := f.srv.db.conn.QueryRow(`SELECT decoded_json FROM transmissions WHERE hash = ?`, a.hash).Scan(&dj); err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(dj), &m); err != nil {
			t.Fatal(err)
		}
		if m["lat"] != want.lat || m["lon"] != want.lon || m["signature"] == nil {
			t.Errorf("stored decoded_json of %s lacks the exact location or signature: %s", a.hash, dj)
		}
	}
	// The truncated advert: a prefix of a real signed advert that holds
	// signature bytes and no flags byte, stored as the decoder leaves it.
	if len(f.truncated) != 1 {
		t.Fatalf("truncated adverts = %d, want 1", len(f.truncated))
	}
	for _, a := range f.truncated {
		for _, raw := range a.rawHex {
			pkt, err := DecodePacket(raw, true)
			if err != nil {
				t.Fatalf("truncated advert %s: %v", a.hash, err)
			}
			if pkt.Payload.Type != "ADVERT" || pkt.Payload.Error == "" || pkt.Payload.Flags != nil {
				t.Errorf("truncated advert %s decodes as %+v", a.hash, pkt.Payload)
			}
			if len(a.sigHex) < 32 || !strings.Contains(raw, a.sigHex) {
				t.Errorf("truncated advert %s: recorded signature bytes are not the frame's", a.hash)
			}
		}
	}
	// Needles are unique per measured point, so a hit names its point.
	seen := map[string]string{}
	for _, p := range f.points {
		if p.name == "gps-home" || p.name == "ghost-estimate" {
			continue // the companion's own position; the centroid of the spots
		}
		for _, v := range []float64{p.lat, p.lon} {
			dec, micro := lkCoordNeedles(v)
			for _, n := range append(dec, micro) {
				if other, dup := seen[n]; dup && other != p.name {
					t.Errorf("needle %q is shared by %s and %s", n, other, p.name)
				}
				seen[n] = p.name
			}
		}
	}
	// Public coordinates must not read like a secret one.
	if iata, err := json.Marshal(iataCoords); err != nil {
		t.Fatal(err)
	} else {
		for n, who := range seen {
			if strings.Contains(string(iata), n) {
				t.Errorf("needle %q of %s also occurs in the IATA table: move the fixture point", n, who)
			}
		}
	}
	for _, c := range []struct {
		table string
		want  int
	}{{"nodes", 4}, {"inactive_nodes", 1}, {"observers", 2}, {"transmissions", 6}, {"observations", 12},
		{"neighbor_edges", 4}, {"client_receptions", 11}, {"client_rf_samples", 9}} {
		var n int
		if err := f.srv.db.conn.QueryRow(`SELECT COUNT(*) FROM ` + c.table).Scan(&n); err != nil || n != c.want {
			t.Errorf("%s rows = %d (%v), want %d", c.table, n, err, c.want)
		}
	}
	if got := len(f.store.packets); got != 6 {
		t.Errorf("store holds %d packets, want 6", got)
	}
	if !f.fuzzIsIdentity() {
		for _, p := range f.points {
			if d := lkDistM(f.fuzzed(p), p.geo()); d > lkRadiusM+1 {
				t.Errorf("%s fuzzed %.0f m away, over the %v m radius", p.name, d, lkRadiusM)
			}
		}
	}
}

// fuzzIsIdentity reports whether Fuzz still returns every point unchanged
// (the contract stub).
func (f *lkFixture) fuzzIsIdentity() bool {
	for _, p := range f.points {
		if f.fuzzed(p) != p.geo() {
			return false
		}
	}
	return true
}
