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
// node's cheap local adverts does not inflate the number. Reads at most the
// 2000 most recent advert rows per node, far beyond any sane advert rate for
// the 7-day window this backs.
func (db *DB) CountFloodAdvertsForNode(pubkey string, windowHours float64) (int, error) {
	rows, err := db.conn.Query(
		"SELECT COALESCE(first_seen, ''), COALESCE(route_type, -1), COALESCE(hash, '') FROM transmissions WHERE from_pubkey = ? AND payload_type = ? ORDER BY id DESC LIMIT 2000",
		pubkey, payloadTypeAdvert)
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
