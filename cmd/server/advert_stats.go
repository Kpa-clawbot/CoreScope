package main

import "time"

// advertRouteTypeFlood is ROUTE_TYPE_FLOOD from the MeshCore packet header.
// Named distinctly from the equivalent constant in the (still open) unscoped-
// relay PR so the two changes merge independently.
const advertRouteTypeFlood = 1

// floodAdvertEntry is one advert transmission originated by a node, reduced to
// what the windowed flood-advert count needs: first-seen timestamp, route type
// and packet hash (for dedup across re-ingests / multi-observer rows).
type floodAdvertEntry struct {
	ts   string
	rt   int
	hash string
}

// countFloodAdverts counts distinct flood adverts (route_type ==
// advertRouteTypeFlood) whose first-seen lies within the past windowHours. Entries
// with unparseable timestamps are skipped, matching relay-liveness behaviour;
// entries without a hash fall back to their timestamp as the dedup key.
func countFloodAdverts(entries []floodAdvertEntry, now time.Time, windowHours float64) int {
	cutoff := now.Add(-time.Duration(windowHours * float64(time.Hour)))
	seen := map[string]struct{}{}
	for _, e := range entries {
		if e.rt != advertRouteTypeFlood {
			continue
		}
		t, ok := parseRelayTS(e.ts)
		if !ok || !t.After(cutoff) {
			continue
		}
		key := e.hash
		if key == "" {
			key = e.ts
		}
		seen[key] = struct{}{}
	}
	return len(seen)
}

// CountFloodAdvertsForNode returns how many distinct FLOOD adverts pubkey
// originated in the last windowHours - the mesh-wide-airtime kind. Zero-hop
// adverts (route_type DIRECT) are excluded, so a nearby observer hearing a
// node's cheap local adverts does not inflate the number.
//
// route_type is filtered in SQL so an advert-spamming node cannot truncate
// the flood count (review feedback on the earlier LIMIT approach). The time
// floor is a DATE-ONLY string with one day of slack: a date prefix compares
// lexically the same across every first_seen format parseRelayTS accepts
// ('T' and ' ' separators alike); the exact window check stays in Go.
//
// The row cap is a pure safety valve on per-request allocation: it applies to
// flood adverts inside the floor window only, and 50000 in ~8 days is ~4 per
// minute - any node past it is unambiguously a spammer whether the count
// saturates or not. (An exact COUNT cannot move into SQL because the precise
// window check needs parseRelayTS over the mixed first_seen formats.)
// floodAdvertRowCap is the production row cap; tests pass a smaller cap
// directly, so there is no mutable package state to race on.
const floodAdvertRowCap = 50000

func (db *DB) CountFloodAdvertsForNode(pubkey string, windowHours float64, rowCap int) (int, error) {
	floor := time.Now().UTC().Add(-time.Duration(windowHours*float64(time.Hour))).AddDate(0, 0, -1).Format("2006-01-02")
	rows, err := db.conn.Query(
		"SELECT COALESCE(first_seen, ''), COALESCE(route_type, -1), COALESCE(hash, '') FROM transmissions WHERE from_pubkey = ? AND payload_type = ? AND route_type = ? AND first_seen >= ? ORDER BY id DESC LIMIT ?",
		pubkey, payloadTypeAdvert, advertRouteTypeFlood, floor, rowCap)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var entries []floodAdvertEntry
	for rows.Next() {
		var e floodAdvertEntry
		if err := rows.Scan(&e.ts, &e.rt, &e.hash); err != nil {
			return 0, err
		}
		entries = append(entries, e)
	}
	return countFloodAdverts(entries, time.Now(), windowHours), nil
}
