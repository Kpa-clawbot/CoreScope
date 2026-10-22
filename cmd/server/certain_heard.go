package main

import (
	"strings"
	"time"

	"github.com/meshcore-analyzer/packetpath"
)

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
	v, ok := tx.ParsedDecoded()["signatureValid"].(bool)
	return ok && !v
}

// relayHopHexLen is the hex length of one relay hop in tx's path, or 0 when
// the path is no evidence of who transmitted: not a flood route, a TRACE, or
// an unreadable header.
func relayHopHexLen(tx *StoreTx) int {
	if tx.PayloadType != nil && !packetpath.PathBytesAreHops(byte(*tx.PayloadType)) {
		return 0
	}
	routeType, pathByte, ok := packetpath.PathByte(tx.RawHex)
	if !ok || (routeType != packetpath.RouteFlood && routeType != packetpath.RouteTransportFlood) {
		return 0
	}
	size := int(pathByte>>6) + 1
	if size > 3 { // reserved (firmware Packet::isValidPathLen)
		return 0
	}
	return 2 * size
}

// noteCertainRelays records tx for each resolved relay hop pubkey whose hop
// prefix is unique among path-capable nodes in pm. pm nil records nothing.
// Caller holds s.mu for writing.
func (s *PacketStore) noteCertainRelays(tx *StoreTx, pks []string, pm *prefixMap) {
	if pm == nil || len(pks) == 0 {
		return
	}
	n := relayHopHexLen(tx)
	if n == 0 {
		return
	}
	for _, pk := range pks {
		if uniqueHopPrefix(pk, n, pm) {
			s.noteCertainHeard(pk, tx)
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
