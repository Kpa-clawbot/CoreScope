package main

import (
	"errors"
	"time"

	"github.com/meshcore-analyzer/packetpath"
)

// floodAdvertEntry is one advert transmission originated by a node, reduced to
// what the windowed flood-advert count needs: first-seen timestamp, known route evidence
// and packet hash (for dedup across re-ingests / multi-observer rows).
type floodAdvertEntry struct {
	ts   string
	mask uint8
	hash string
}

// countFloodAdverts counts distinct adverts with known flood evidence whose
// first-seen lies within the past windowHours. Entries
// with unparseable timestamps are skipped, matching relay-liveness behaviour;
// entries without a hash fall back to their timestamp as the dedup key.
func countFloodAdverts(entries []floodAdvertEntry, now time.Time, windowHours float64) int {
	cutoff := now.Add(-time.Duration(windowHours * float64(time.Hour)))
	seen := map[string]struct{}{}
	for _, e := range entries {
		if e.mask&packetpath.AdvertFlood == 0 {
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

// CountFloodAdvertsForNode counts distinct adverts with recorded flood evidence,
// including mixed and transport-flood frames, independently of the first route.
// It is a lower bound while backfill is pending or evidence writes have failed.
// With no evidence table the count is unavailable, not a proven zero.
//
// SQL filters known flood evidence before the row cap. The date-only floor has
// one day of slack for mixed timestamp formats; the exact window stays in Go.
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
	if !db.advertEvidencePresent() {
		return 0, errors.New("advert route evidence unavailable")
	}
	floor := time.Now().UTC().Add(-time.Duration(windowHours*float64(time.Hour))).AddDate(0, 0, -1).Format("2006-01-02")
	rows, err := db.conn.Query(
		db.nativeSQL(`SELECT COALESCE(first_seen, ''), CAST(?1 AS INTEGER), COALESCE(hash, '') FROM transmissions t
 WHERE from_pubkey=?2 AND payload_type=?3 AND first_seen>=?4
 AND EXISTS(SELECT 1 FROM advert_route_evidence e WHERE e.tx_id=t.id AND e.bit=?5)
 ORDER BY id DESC LIMIT ?6`, `SELECT COALESCE(first_seen, ''), `+db.parameter(1)+`::integer, COALESCE(hash, '') FROM transmissions t
 WHERE from_pubkey=`+db.parameter(2)+` AND payload_type=`+db.parameter(3)+` AND first_seen>=`+db.parameter(4)+`
 AND EXISTS(SELECT 1 FROM advert_route_evidence e WHERE e.tx_id=t.id AND e.bit=`+db.parameter(5)+`)
 ORDER BY id DESC LIMIT `+db.parameter(6)),
		packetpath.AdvertFlood, pubkey, payloadTypeAdvert, floor, packetpath.AdvertFlood, rowCap)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var entries []floodAdvertEntry
	for rows.Next() {
		var e floodAdvertEntry
		if err := rows.Scan(&e.ts, &e.mask, &e.hash); err != nil {
			return 0, err
		}
		entries = append(entries, e)
	}
	return countFloodAdverts(entries, time.Now(), windowHours), rows.Err()
}
