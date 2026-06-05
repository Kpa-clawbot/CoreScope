package main

import (
	"sort"
	"time"
)

// relay_airtime_share.go — issue #1359
//
// Implements the "Relay Airtime Share" analytics metric:
//   score(packet) = payload_bytes × COUNT(DISTINCT repeater_pubkey
//                                         across all observations of that packet)
//
// Aggregated by payload_type. Originator TX is deliberately excluded — a
// never-relayed direct message scores 0, which is the correct framing for a
// "relay amplification" metric.
//
// In-memory only; no SQL, no new index, no schema change. The resolved-pubkey
// reverse index (populated under s.mu via addToResolvedPubkeyIndex from every
// observation's resolved_path) is the source of distinct relays per
// transmission — len(resolvedPubkeyReverse[tx.ID]) IS the union of distinct
// repeater pubkeys, deduplicated cross-observation. Critical: this is NOT the
// length of any single observation's resolved_path (the bug-trap from
// #1358's follow-up SQL hint).

// distinctRelayCount returns the number of distinct repeater pubkeys that
// forwarded `tx`, unioned across ALL observations of that transmission_id.
//
// Source: the resolved-pubkey reverse index — populated by
// indexResolvedPathHops / addToResolvedPubkeyIndex from every observation's
// resolved_path. Each entry is one distinct pubkey hash for THIS tx (the
// indexer dedups (hash, txID) pairs before appending).
//
// Caller MUST hold s.mu at least RLock.
func (s *PacketStore) distinctRelayCount(tx *StoreTx) int {
	if tx == nil || !s.useResolvedPathIndex {
		return 0
	}
	return len(s.resolvedPubkeyReverse[tx.ID])
}

// computeRelayAirtimeShare aggregates relay-airtime-share per payload_type.
//
// Returns:
//
//	{
//	  "rows":        [{payload_type, type, count, count_pct, score, airtime_pct}, ...] sorted by airtime_pct desc,
//	  "total_count": int,
//	  "total_score": int,
//	  "window":      window label,
//	  "cached":      false (overwritten by cached wrapper),
//	}
func (s *PacketStore) computeRelayAirtimeShare(window TimeWindow) map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ptNames := payloadTypeNames

	type bucket struct {
		count int
		score int
	}
	buckets := make(map[int]*bucket)
	seenHash := make(map[string]bool, len(s.packets))
	totalCount := 0
	totalScore := 0

	for _, tx := range s.packets {
		if tx == nil || tx.PayloadType == nil {
			continue
		}
		if !window.Includes(tx.FirstSeen) {
			continue
		}
		// Dedup per-hash: each distinct packet counted once. ACKs in the
		// test fixture have unique hashes so this only collapses true
		// re-observations of the same packet.
		if tx.Hash != "" {
			if seenHash[tx.Hash] {
				continue
			}
			seenHash[tx.Hash] = true
		}
		pt := *tx.PayloadType
		b := buckets[pt]
		if b == nil {
			b = &bucket{}
			buckets[pt] = b
		}
		b.count++
		totalCount++

		// payload bytes from RawHex (2 hex chars per byte).
		payloadBytes := len(tx.RawHex) / 2
		relays := s.distinctRelayCount(tx)
		score := payloadBytes * relays
		b.score += score
		totalScore += score
	}

	rows := make([]map[string]interface{}, 0, len(buckets))
	for pt, b := range buckets {
		name := ptNames[pt]
		if name == "" {
			name = "UNK"
		}
		var countPct, airtimePct float64
		if totalCount > 0 {
			countPct = float64(b.count) / float64(totalCount) * 100.0
		}
		if totalScore > 0 {
			airtimePct = float64(b.score) / float64(totalScore) * 100.0
		}
		rows = append(rows, map[string]interface{}{
			"payload_type": name,
			"type":         pt,
			"count":        b.count,
			"count_pct":    countPct,
			"score":        b.score,
			"airtime_pct":  airtimePct,
		})
	}

	// Sort descending by airtime_pct; tiebreak count desc, then name asc
	// for deterministic ordering.
	sort.SliceStable(rows, func(i, j int) bool {
		ai, _ := rows[i]["airtime_pct"].(float64)
		aj, _ := rows[j]["airtime_pct"].(float64)
		if ai != aj {
			return ai > aj
		}
		ci, _ := rows[i]["count"].(int)
		cj, _ := rows[j]["count"].(int)
		if ci != cj {
			return ci > cj
		}
		ni, _ := rows[i]["payload_type"].(string)
		nj, _ := rows[j]["payload_type"].(string)
		return ni < nj
	})

	label := ""
	if !window.IsZero() {
		label = window.Label
	}
	return map[string]interface{}{
		"rows":        rows,
		"total_count": totalCount,
		"total_score": totalScore,
		"window":      label,
		"cached":      false,
	}
}

// GetRelayAirtimeShareWithWindow is the cached wrapper around
// computeRelayAirtimeShare. Reuses the existing rfCache + rfCacheTTL pool
// (shared with RF / topology / distance analytics — no new cache layer per
// #1359 spec).
func (s *PacketStore) GetRelayAirtimeShareWithWindow(window TimeWindow) map[string]interface{} {
	cacheKey := "relay-airtime-share|"
	if !window.IsZero() {
		cacheKey += window.CacheKey()
	}
	s.cacheMu.Lock()
	if cached, ok := s.rfCache[cacheKey]; ok && time.Now().Before(cached.expiresAt) {
		s.cacheHits++
		s.cacheMu.Unlock()
		// Shallow copy with cached=true so the JSON client can tell.
		m := cached.data
		out := make(map[string]interface{}, len(m)+1)
		for k, v := range m {
			out[k] = v
		}
		out["cached"] = true
		return out
	}
	s.cacheMisses++
	s.cacheMu.Unlock()

	result := s.computeRelayAirtimeShare(window)

	s.cacheMu.Lock()
	s.rfCache[cacheKey] = &cachedResult{data: result, expiresAt: time.Now().Add(s.rfCacheTTL)}
	s.cacheMu.Unlock()

	return result
}
