package main

import (
	"strings"
	"time"

	"github.com/meshcore-analyzer/packetpath"
)

// LastCertainRelayMap snapshots only the requested keys: O(page size), with
// one read lock and no packet scan. Keys retain the caller's casing.
func (s *PacketStore) LastCertainRelayMap(pubkeys []string) map[string]time.Time {
	latest := make(map[string]time.Time, len(pubkeys))
	s.mu.RLock()
	for _, pk := range pubkeys {
		if ts := s.certainRelayed[strings.ToLower(pk)]; !ts.IsZero() {
			latest[pk] = ts
		}
	}
	s.mu.RUnlock()
	return latest
}

// Certain liveness (#2260). certainHeard holds, per pubkey, the newest
// FirstSeen of a transmission that can only have come from that node:
//
//   - its own packets (decoded pubKey / srcPubKey), and
//   - flood relay hops whose prefix, at the packet's path hash size, matches
//     exactly one node that can appear in a path, and no retired one
//     (inactive_nodes) that may still relay.
//
// A 1-byte hop shared by several repeaters is not evidence: the resolver may
// still attribute it (affinity, geography) for analytics, but a silent node
// must not stay "Active" on traffic from a namesake prefix. The firmware
// lets the originator pick the hash size for the whole path
// (Mesh::sendFlood) and every relay appends its own hash at that size
// (Mesh::routeRecvPacket), so a multibyte repeater legitimately appears as a
// 1-byte hop; only its uniqueness decides. Direct-route paths list the hops
// still to come (Mesh::removeSelfFromPath), and TRACE reuses the path bytes
// for SNR, so neither proves a hop transmitted.
//
// Liveness reads it: Last Heard, the node status, the nodes list and the
// notify offline check. One entry per node that was ever certainly heard,
// dropped with the node's byNode list on eviction.

// noteCertainHeard records tx as certain evidence for pubkey. Caller holds
// s.mu for writing.
func (s *PacketStore) noteCertainHeard(pubkey string, tx *StoreTx) {
	if pubkey == "" || tx == nil || tx.FirstSeen == "" {
		return
	}
	if s.certainHeard == nil {
		s.certainHeard = make(map[string]string)
	}
	if tx.FirstSeen > s.certainHeard[pubkey] {
		s.certainHeard[pubkey] = tx.FirstSeen
	}
}

// signatureFailed reports a decoded advert whose signature did not verify
// (signatureValid false): a corrupted or forged advert is no evidence the
// node transmitted. Absent means not checked, which is not a failure.
func (tx *StoreTx) signatureFailed() bool {
	// Ordinary relay payloads have no signature field. Leave their decoded
	// cache cold; a Unicode escape may encode the key, so keep that fallback.
	if !strings.Contains(tx.DecodedJSON, "signatureValid") && !strings.Contains(tx.DecodedJSON, `\u`) {
		return false
	}
	v, ok := tx.ParsedDecoded()["signatureValid"].(bool)
	return ok && !v
}

// relayHopHexLen is the hex length of one relay hop in this frame, or 0 when
// the path is no evidence of who transmitted: not a flood route, a TRACE, or
// an unreadable header.
func relayHopHexLen(raw string) int {
	routeType, _, ok := packetpath.PathByte(raw)
	if !ok || (routeType != packetpath.RouteFlood && routeType != packetpath.RouteTransportFlood) {
		return 0
	}
	return 2 * packetpath.HashSize(raw) // also excludes TRACE and reserved widths
}

// observedRelayHopHexLen binds the retained path_json to its own wire frame.
// Only this byte survives loading; no extra raw packets or parsed paths are
// retained. Missing legacy raw_hex is unknown, never borrowed from tx.RawHex.
// The comparison also rejects a truncated frame or a path/header mismatch.
func observedRelayHopHexLen(raw, pathJSON string) uint8 {
	n := relayHopHexLen(raw)
	if n == 0 {
		return 0
	}
	route, pathByte, _ := packetpath.PathByte(raw)
	count := int(pathByte & 63)
	offset := 4 // header and path byte, in hex characters
	if route == packetpath.RouteTransportFlood {
		offset += 8
	}
	if count == 0 || count*n > 128 || len(raw) < offset+count*n {
		return 0 // firmware Packet::isValidPathLen: at most 64 path bytes
	}
	pos := 0
	for i := 0; i < count; i++ {
		hop, next, ok := nextPathHop(pathJSON, pos)
		if !ok || len(hop) != n || !strings.EqualFold(hop, raw[offset+i*n:offset+(i+1)*n]) {
			return 0
		}
		if _, ok := hopKey(hop); !ok {
			return 0
		}
		pos = next
	}
	if _, _, extra := nextPathHop(pathJSON, pos); extra {
		return 0
	}
	return uint8(n)
}

// noteCertainRelays records only hops actually present in this eligible
// observation, using the same membership/uniqueness rule as certainFor.
// Caller holds s.mu for writing and has indexed the relay attribution.
func (s *PacketStore) noteCertainRelays(tx *StoreTx, obs *StoreObs, pm *prefixMap) {
	if tx == nil || obs == nil || obs.relayHopHexLen == 0 || pm == nil || tx.signatureFailed() {
		return
	}
	// Use the transmission's first_seen, as certainHeard does, never a late
	// duplicate's observation time/LatestSeen. Parse once per eligible frame
	// so invalid dates cannot poison the maximum and offsets/fractions compare
	// chronologically. Only adverts need decoding; ordinary payloads stay cold.
	relayTime, _ := parseRelayTS(tx.FirstSeen)
	var advertSender string
	canProveForwarding := true
	if tx.PayloadType != nil && *tx.PayloadType == payloadTypeAdvert {
		advertSender, _ = tx.ParsedDecoded()["pubKey"].(string)
		canProveForwarding = advertSender != ""
	}
	for pos := 0; ; {
		hop, next, ok := nextPathHop(obs.PathJSON, pos)
		if !ok {
			return
		}
		pos = next
		c := pm.relayCandidates(hop)
		if len(c) == 1 {
			pk := strings.ToLower(c[0].PublicKey)
			if s.nodeHasTx(pk, tx) && uniqueHopPrefix(pk, int(obs.relayHopHexLen), pm) {
				s.noteCertainHeard(pk, tx)
				// Originating an advert proves transmission, not forwarding.
				// A different node's observed hop can forward that same advert.
				// An unknown advert origin cannot prove forwarding either.
				if canProveForwarding && !strings.EqualFold(pk, advertSender) && relayTime.After(s.certainRelayed[pk]) {
					if s.certainRelayed == nil {
						s.certainRelayed = make(map[string]time.Time)
					}
					s.certainRelayed[pk] = relayTime
				}
			}
		}
	}
}

// uniqueHopPrefix reports whether pk's first n hex characters, a relay hop
// at that hash size, match exactly one path-capable node in pm, pk itself,
// and no retired one. A repeater that never advertised is unknown to both
// and cannot be ruled out.
func uniqueHopPrefix(pk string, n int, pm *prefixMap) bool {
	if pm == nil || n == 0 || len(pk) < n {
		return false
	}
	prefix := strings.ToLower(pk[:n])
	if _, shared := pm.retired[prefix]; shared {
		return false
	}
	c := pm.relayCandidates(prefix)
	return len(c) == 1 && strings.EqualFold(c[0].PublicKey, pk)
}

// markRetired loads the hop prefixes (1 to 3 bytes) of the path-capable
// nodes in inactive_nodes into pm.retired. Rows of nodes also in nodes are
// skipped: retention copies a node there and nothing removes it when the
// node is heard again, and a node is no namesake of itself. A database
// without the table leaves it empty.
func (pm *prefixMap) markRetired(db *DB) error {
	if db == nil || !db.inactiveNodesPresent() {
		return nil
	}
	rows, err := db.conn.Query(`SELECT public_key, COALESCE(role, '') FROM inactive_nodes
		WHERE lower(public_key) NOT IN (SELECT lower(public_key) FROM nodes)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	retired := map[string]struct{}{}
	for rows.Next() {
		var pk, role string
		if err := rows.Scan(&pk, &role); err != nil {
			return err
		}
		if !canAppearInPath(role) {
			continue
		}
		pk = strings.ToLower(pk)
		for n := 2; n <= 6 && n <= len(pk); n += 2 {
			retired[pk[:n]] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	pm.retired = retired
	return nil
}

// certainFor reports whether tx, listed in byNode[pk], is certain evidence
// for pk under the same rule as certainHeard: pk sent it, or it is a flood
// relay whose hop prefix only pk has. Packet-level counterpart of
// certainHeard for analytics and the adaptive offline threshold; it uses the
// prefix map of now, certainHeard the one at ingest. Caller holds s.mu for
// reading: late duplicates can upgrade an observation's retained evidence.
func certainFor(tx *StoreTx, pk string, pm *prefixMap) bool {
	if tx == nil || pk == "" || tx.signatureFailed() {
		return false
	}
	if tx.DecodedJSON != "" && strings.Contains(tx.DecodedJSON, "ubKey") {
		if d := tx.ParsedDecoded(); d != nil {
			for _, f := range []string{"pubKey", "srcPubKey"} {
				if v, _ := d[f].(string); v != "" && strings.EqualFold(v, pk) {
					return true
				}
			}
		}
	}
	for _, obs := range tx.Observations {
		if obs == nil || !uniqueHopPrefix(pk, int(obs.relayHopHexLen), pm) {
			continue
		}
		for pos := 0; ; {
			hop, next, ok := nextPathHop(obs.PathJSON, pos)
			if !ok {
				break
			}
			if strings.EqualFold(hop, pk[:obs.relayHopHexLen]) {
				return true
			}
			pos = next
		}
	}
	return false
}

// lastHeardValue is a node's last_heard in /api/nodes and the node detail:
// the newer of its DB last_seen (adverts; nil or unparsable counts as
// never) and heard, its certain last heard from LastHeardMap (zero for
// none). Before #2260 last_heard was a plain copy of last_seen.
func lastHeardValue(lastSeen interface{}, heard time.Time) interface{} {
	if heard.IsZero() {
		return lastSeen
	}
	if s, ok := lastSeen.(string); ok {
		if t, ok := parseRelayTS(s); ok && !heard.After(t) {
			return lastSeen
		}
	}
	return heard.UTC().Format("2006-01-02T15:04:05.000Z")
}

// longestSilence is the longest gap between consecutive hourly buckets
// (sorted RFC3339 keys) and where it started, including the silence from
// the start of the covered window (from) to the first bucket and the one
// still running from the last bucket to now: an outage in progress is
// exactly the silence worth seeing. No bucket at all is silence over the
// whole window.
func longestSilence(buckets []string, from, now time.Time) (int, interface{}) {
	var longest int
	var start interface{}
	consider := func(begin string, to time.Time) {
		t, err := time.Parse(time.RFC3339, begin)
		if err != nil {
			return
		}
		if gap := int(to.Sub(t).Milliseconds()); gap > longest {
			longest, start = gap, begin
		}
	}
	fromStr := from.UTC().Format(time.RFC3339)
	if len(buckets) == 0 {
		consider(fromStr, now)
		return longest, start
	}
	if t, err := time.Parse(time.RFC3339, buckets[0]); err == nil && t.Sub(from) > time.Hour {
		consider(fromStr, t)
	}
	for i := 1; i < len(buckets); i++ {
		if t, err := time.Parse(time.RFC3339, buckets[i]); err == nil {
			consider(buckets[i-1], t)
		}
	}
	if len(buckets) > 0 {
		// The last bucket covers its whole hour.
		last := buckets[len(buckets)-1]
		if t, err := time.Parse(time.RFC3339, last); err == nil && now.Sub(t) > time.Hour {
			consider(last, now)
		}
	}
	return longest, start
}
