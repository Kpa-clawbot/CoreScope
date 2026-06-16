package main

import (
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/meshcore-analyzer/packetpath"
)

// clientPubkeyRe validates the companion pubkey taken from the MQTT topic
// (meshcore/client/<PUBLIC_KEY>/packets). A no-ACL broker would let a client
// publish under an arbitrary topic segment (e.g. "!@#$"), so we reject anything
// that is not lowercase hex before it reaches client_receptions/client_observers.
// Mirrors the server-side hexPrefixRe (cmd/server/node_resolve.go).
var clientPubkeyRe = regexp.MustCompile(`^[0-9a-f]{2,64}$`)

// handleClientPacket processes a packet from the mobile client RX topic
// (meshcore/client/{PUBLIC_KEY}/packets). Unlike observer packets, a roaming
// companion reports WHERE it directly heard a node, so we write a
// client_receptions row and never touch the observers/observations tables.
// rxPubkey is the companion pubkey from the topic (ACL-bound by the broker).
func handleClientPacket(store *Store, tag, rxPubkey string, msg map[string]interface{}, channelKeys map[string]string) {
	// The companion identity IS the (ACL-bound) topic pubkey. Reject non-hex
	// topic segments so a no-ACL broker can't pollute the coverage tables, and
	// never fall back to a payload-supplied id (that would defeat the ACL trust
	// model — see docs/client-rx-coverage.md).
	rxPubkey = strings.ToLower(strings.TrimSpace(rxPubkey))
	if !clientPubkeyRe.MatchString(rxPubkey) {
		log.Printf("MQTT [%s] client: invalid pubkey %.8q, dropping", tag, rxPubkey)
		return
	}
	rawHex, _ := msg["raw"].(string)
	if rawHex == "" {
		return
	}
	gps, ok := msg["gps"].(map[string]interface{})
	if !ok {
		return // a client packet without a GPS fix is not coverage; drop
	}
	lat, latOK := toFloat64(gps["lat"])
	lon, lonOK := toFloat64(gps["lon"])
	if !latOK || !lonOK {
		return
	}
	var accPtr *float64
	if acc, ok := toFloat64(gps["acc_m"]); ok {
		accPtr = &acc
	}

	decoded, err := DecodePacket(rawHex, channelKeys, false)
	if err != nil {
		log.Printf("MQTT [%s] client decode error: %v", tag, err)
		return
	}

	direction := ""
	if v, ok := msg["direction"].(string); ok {
		direction = v
	} else if v, ok := msg["Direction"].(string); ok {
		direction = v
	}

	var snrPtr *float64
	if f, ok := toFloat64(firstPresent(msg, "SNR", "snr")); ok {
		snrPtr = &f
	}
	var rssiPtr *int
	if f, ok := toFloat64(firstPresent(msg, "RSSI", "rssi")); ok {
		v := int(f)
		rssiPtr = &v
	}

	rxAt, _ := resolveRxTime(msg, tag)
	isAdvert := decoded.Header.PayloadTypeName == "ADVERT"

	rec, ok := buildClientReception(
		rxPubkey,
		direction, decoded.Header.RouteType, decoded.Path.Hops, decoded.Payload.PubKey, isAdvert,
		snrPtr, rssiPtr, lat, lon, accPtr, rxAt, time.Now().UTC().Format(time.RFC3339),
	)
	if !ok {
		return
	}
	if _, err := store.InsertClientReception(rec); err != nil {
		log.Printf("MQTT [%s] client_reception insert: %v", tag, err)
	}
	// Remember the companion's self-reported name (sent as "origin") so the
	// leaderboard can show a name even if this companion never advertised.
	if name := stringField(msg, "origin"); name != "" {
		if err := store.UpsertClientObserver(rec.RxPubkey, name, time.Now().UTC().Format(time.RFC3339)); err != nil {
			log.Printf("MQTT [%s] client_observer upsert: %v", tag, err)
		}
	}
}

// UpsertClientObserver records/updates a mobile client's self-reported name.
// All writes live in the ingestor (read/write invariant #1283).
func (s *Store) UpsertClientObserver(pubkey, name, ts string) error {
	if pubkey == "" || name == "" {
		return nil
	}
	_, err := s.db.Exec(`
		INSERT INTO client_observers (pubkey, name, last_seen) VALUES (?,?,?)
		ON CONFLICT(pubkey) DO UPDATE SET name = excluded.name, last_seen = excluded.last_seen`,
		strings.ToLower(pubkey), name, ts)
	return err
}

// firstPresent returns the first present value among the given keys.
func firstPresent(msg map[string]interface{}, keys ...string) interface{} {
	for _, k := range keys {
		if v, ok := msg[k]; ok {
			return v
		}
	}
	return nil
}

// stringField returns msg[key] as a string, or "" if absent/not a string.
func stringField(msg map[string]interface{}, key string) string {
	if v, ok := msg[key].(string); ok {
		return v
	}
	return ""
}

// ClientReception is one mobile RX coverage point: a companion (RxPubkey)
// directly heard a node (HeardKey) at a GPS position. Hex binning is done
// server-side from Lat/Lon at query time, so no cell id is stored here.
type ClientReception struct {
	RxPubkey    string
	HeardKey    string
	HeardKeyLen int
	RSSI        *int
	SNR         *float64
	Lat         float64
	Lon         float64
	PosAccM     *float64
	RxAt        string
	IngestedAt  string
	Src         string
}

// deriveHeardKey applies the RX capture HARD RULE: record only what the
// companion heard itself and directly.
//   - direction must be "rx".
//   - hops present AND a FLOOD route → the directly-heard node is the LAST hop
//     (path[len-1] = the forwarder that just transmitted; each FLOOD forwarder
//     appends its hash to the end). 1-byte (2 hex char) prefixes are rejected.
//   - hops present on a DIRECT route → NOT attributable: direct forwarders
//     consume the next hop from the FRONT (firmware Mesh.cpp removeSelfFromPath),
//     so path[len-1] is the route's destination-side end, not who was heard.
//   - hops empty + isAdvert → the 0-hop advertiser, by its full pubkey.
//   - otherwise → not attributable (ok=false).
//
// Returns (heardKey lowercased, keylenBytes, src, ok).
func deriveHeardKey(direction string, routeType int, hops []string, advertPubkey string, isAdvert bool) (string, int, string, bool) {
	if !strings.EqualFold(direction, "rx") {
		return "", 0, "", false
	}
	if len(hops) > 0 {
		// FLOOD routes (TRANSPORT_FLOOD 0, FLOOD 1) APPEND each forwarder's hash to
		// the END of the path, so path[last] is the immediate RF transmitter. DIRECT
		// routes (2, 3) consume the next hop from the FRONT, so path[last] is the
		// route's destination-side end, NOT who was heard.
		if routeType != packetpath.RouteTransportFlood && routeType != packetpath.RouteFlood { // direct route: path[last] is not the transmitter
			return "", 0, "", false
		}
		last := strings.ToLower(strings.TrimSpace(hops[len(hops)-1]))
		keylen := len(last) / 2
		if keylen < 2 { // exclude 1-byte (collision-prone), matching Reach
			return "", 0, "", false
		}
		return last, keylen, "rxlog", true
	}
	if isAdvert && advertPubkey != "" {
		pk := strings.ToLower(strings.TrimSpace(advertPubkey))
		return pk, len(pk) / 2, "advert", true
	}
	return "", 0, "", false
}

// buildClientReception validates inputs and assembles a ClientReception, or
// returns ok=false when the packet is not attributable / out of range.
func buildClientReception(
	rxPubkey, direction string, routeType int, hops []string, advertPubkey string, isAdvert bool,
	snr *float64, rssi *int, lat, lon float64, posAccM *float64, rxAt, ingestedAt string,
) (*ClientReception, bool) {
	if rxPubkey == "" || rxAt == "" {
		return nil, false
	}
	if lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return nil, false
	}
	heardKey, keylen, src, ok := deriveHeardKey(direction, routeType, hops, advertPubkey, isAdvert)
	if !ok {
		return nil, false
	}
	return &ClientReception{
		RxPubkey: strings.ToLower(rxPubkey), HeardKey: heardKey, HeardKeyLen: keylen,
		RSSI: rssi, SNR: snr, Lat: lat, Lon: lon, PosAccM: posAccM,
		RxAt: rxAt, IngestedAt: ingestedAt, Src: src,
	}, true
}

// InsertClientReception writes one coverage row. Idempotent via the
// UNIQUE(rx_pubkey, heard_key, rx_at) constraint; returns ins=false when the
// row already existed. All writes live in the ingestor (read/write invariant #1283).
func (s *Store) InsertClientReception(r *ClientReception) (bool, error) {
	res, err := s.db.Exec(`
		INSERT INTO client_receptions
			(rx_pubkey, heard_key, heard_keylen, rssi, snr, lat, lon, pos_acc_m, rx_at, ingested_at, src)
		VALUES (?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(rx_pubkey, heard_key, rx_at) DO NOTHING`,
		r.RxPubkey, r.HeardKey, r.HeardKeyLen, r.RSSI, r.SNR, r.Lat, r.Lon, r.PosAccM, r.RxAt, r.IngestedAt, r.Src)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
