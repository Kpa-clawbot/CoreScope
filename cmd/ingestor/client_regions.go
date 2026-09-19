package main

import (
	"log"
	"regexp"
	"strings"
	"time"
)

// targetPubkeyRe validates the `target` field of a /regions payload: the
// repeater the companion asked. Unlike the topic pubkey (clientPubkeyRe,
// 2-64 hex — a separate contract with its own history), target has one
// legitimate shape only. Task 1's buildRegionsRequest throws a TypeError
// unless the pubkey it sends is exactly 64 hex characters, so the app can
// only ever ask a full pubkey and can only ever report one back. A target
// shorter than that is a malformed or forged payload, not a legitimate
// variant — and accepting it anyway would be a silent-accumulation bug, not
// a loud one: CurrentDeclaredRegions matches on the exact target, so a row
// stored against a short prefix is invisible to every query for the real
// node and just accumulates as junk that looks like data.
var targetPubkeyRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// handleClientRegions processes one region-discovery answer from
// meshcore/client/{PUBLIC_KEY}/regions: a mobile companion asked a repeater
// (target) which regions it declares flood-allowed, and relays the answer.
// The topic pubkey (rxPubkey) is the reporting companion's identity
// (ACL-bound by the broker) and is never taken from the payload. `target` is
// the repeater that was asked; it arrives as payload data and is validated
// as a full 64-hex pubkey (targetPubkeyRe) before being trusted. An empty
// `regions` list is a valid, deliberate answer ("nothing flood-allowed") and
// is stored; a missing or malformed `regions` field is not an answer at all
// and the whole message is dropped rather than half-stored as an empty list.
// GPS is optional here, unlike client coverage: a declared-regions answer is
// meaningful without a position, so a missing/invalid fix stores NULL
// lat/lon instead of dropping the row.
func handleClientRegions(store *Store, cfg *Config, tag, rxPubkey string, msg map[string]interface{}) {
	rxPubkey = strings.ToLower(strings.TrimSpace(rxPubkey))
	if !clientPubkeyRe.MatchString(rxPubkey) {
		log.Printf("MQTT [%s] regions: invalid pubkey %.8q, dropping", tag, rxPubkey)
		return
	}
	target, _ := msg["target"].(string)
	target = strings.ToLower(strings.TrimSpace(target))
	if !targetPubkeyRe.MatchString(target) {
		log.Printf("MQTT [%s] regions: invalid target %.8q, dropping", tag, target)
		return
	}
	regionsRaw, ok := msg["regions"].([]interface{})
	if !ok {
		log.Printf("MQTT [%s] regions: missing/malformed regions field, dropping", tag)
		return
	}
	regions := make([]string, 0, len(regionsRaw))
	for _, r := range regionsRaw {
		s, ok := r.(string)
		if !ok {
			log.Printf("MQTT [%s] regions: non-string region entry, dropping", tag)
			return
		}
		// The repeater's reply is encrypted with a block cipher, so its plaintext
		// is NUL-padded to a 16-byte boundary and the padding lands inside the LAST
		// region name. Clients trim it since coredrive-rx v1.10.2, but a PWA keeps
		// running its cached build until the user reloads, and this instance has no
		// control over when eight-odd independent clients do that. Trimming here
		// closes it once, for every client version: "belml\x00\x00" stored verbatim
		// matches no observed scope and reads as a region the repeater declares but
		// never forwards.
		s = strings.TrimRight(s, "\x00")
		if s == "" {
			continue // padding-only entry — not a declared region
		}
		regions = append(regions, s)
	}
	truncated, _ := msg["truncated"].(bool)

	rxTime, _ := resolveRxTimeCore(msg, tag)
	observedAt := rxTime.Format(rxTimeMillisLayout)

	var latPtr, lonPtr, posAccPtr *float64
	if gps, ok := msg["gps"].(map[string]interface{}); ok {
		lat, latOK := toFloat64(gps["lat"])
		lon, lonOK := toFloat64(gps["lon"])
		if latOK && lonOK && lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180 {
			latPtr, lonPtr = &lat, &lon
			if acc, ok := toFloat64(gps["acc_m"]); ok {
				posAccPtr = &acc
			}
		}
	}

	o := &ClientDeclaredRegions{
		Target:        target,
		RxPubkey:      rxPubkey,
		ObservedAt:    observedAt,
		IngestedAt:    time.Now().UTC().Format(time.RFC3339),
		RegionsCSV:    strings.Join(regions, ","),
		Truncated:     truncated,
		Lat:           latPtr,
		Lon:           lonPtr,
		PosAccM:       posAccPtr,
		RepeaterClock: optInt64(msg, "repeater_clock"),
	}
	if _, err := store.InsertClientDeclaredRegions(o); err != nil {
		log.Printf("MQTT [%s] node_declared_regions insert: %v", tag, err)
	}
}
