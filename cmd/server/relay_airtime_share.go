package main

import (
	"log"
	"sort"
	"time"

	"github.com/meshcore-analyzer/lora"
)

// relay_airtime_share.go — issues #1359 + #1768
//
// Implements the "Relay Airtime Share" analytics metric:
//   score(packet) = TimeOnAir(payload_bytes, preset)
//                   × COUNT(DISTINCT repeater_pubkey across observations)
//
// #1768 swapped the original byte-only proxy (`bytes × relays`) for
// closed-form LoRa Time-on-Air. The byte proxy underweighted small
// frames by ~3-4× because the additive preamble + fixed-symbol
// intercept does NOT cancel under per-type normalization. ToA fixes
// the headline divergence the dumbbell chart is supposed to show.
//
// The PHY preset is config-driven (analytics.loraPreset in
// config.example.json); defaults match the actual deployment
// preset 869.6 MHz / BW 62.5 kHz / SF 8 / CR 4/5, with the
// SF-dependent preamble pulled from internal/lora.PreambleForSF.
//
// Aggregated by payload_type. Originator TX is deliberately excluded — a
// never-relayed direct message scores 0, which is the correct framing for a
// "relay amplification" metric. In-memory only; no SQL, no new index.

// defaultLoRaPreset is the canonical fallback when config is absent.
// Matches the reporter's `get radio` output `869.6179809, 62.5, 8, 5`.
func defaultLoRaPreset() lora.Preset {
	return lora.Preset{
		FreqHz:   869.6e6,
		BWkHz:    62.5,
		SF:       8,
		CR:       5,
		Preamble: lora.PreambleForSF(8),
	}
}

// resolveLoRaPreset returns the effective preset, falling back to
// defaults for any unset / zero / out-of-range field.
//
// Out-of-range SF / CR are NOT silently clamped on a per-field basis
// (the prior behaviour produced a confusing hybrid preset, partially
// operator-supplied and partially defaulted). Instead we keep the
// default for the offending field AND log a single WARN at resolve time
// naming the field plus the actual vs. effective value. There is no
// startup-time analytics-config validation gate today, so refusal-to-
// start is not an option — the WARN is the gate. Zero / unset fields
// fall back silently as before (the operator opted out of overriding
// that param).
func (s *PacketStore) resolveLoRaPreset() lora.Preset {
	p := defaultLoRaPreset()
	if s == nil || s.config == nil || s.config.Analytics == nil || s.config.Analytics.LoRaPreset == nil {
		return p
	}
	cfg := s.config.Analytics.LoRaPreset
	if cfg.FreqHz > 0 {
		p.FreqHz = cfg.FreqHz
	}
	if cfg.BWkHz > 0 {
		p.BWkHz = cfg.BWkHz
	}
	if cfg.SF != 0 {
		if cfg.SF >= 6 && cfg.SF <= 12 {
			p.SF = cfg.SF
			p.Preamble = lora.PreambleForSF(cfg.SF)
		} else {
			log.Printf("[analytics.loraPreset] WARN: sf=%d out of range [6,12], using default sf=%d", cfg.SF, p.SF)
		}
	}
	if cfg.CR != 0 {
		if cfg.CR >= 5 && cfg.CR <= 8 {
			p.CR = cfg.CR
		} else {
			log.Printf("[analytics.loraPreset] WARN: cr=%d out of range [5,8], using default cr=%d", cfg.CR, p.CR)
		}
	}
	return p
}

// presetResponse shapes the preset for the API response and the
// analytics caption (issue #1768 — operators can't interpret an
// "Airtime %" headline without knowing what PHY assumptions it bakes
// in). All four free params plus the derived preamble are surfaced.
type presetResponse struct {
	FreqHz   float64 `json:"freq_hz"`
	BWkHz    float64 `json:"bw_khz"`
	SF       int     `json:"sf"`
	CR       int     `json:"cr"`
	Preamble int     `json:"preamble"`
}

func presetJSON(p lora.Preset) presetResponse {
	return presetResponse{
		FreqHz:   p.FreqHz,
		BWkHz:    p.BWkHz,
		SF:       p.SF,
		CR:       p.CR,
		Preamble: p.Preamble,
	}
}

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

// AirtimeForTransmissions sums LoRa Time-on-Air × distinct-resolved-repeater
// count (the same score formula as computeRelayAirtimeShare / issue #1768)
// for an arbitrary, caller-supplied set of transmission IDs — e.g. one
// wardriving session's messages, rather than every packet in a time window.
//
// Returns ok=false when the resolved-path index is unavailable, OR when
// NONE of the requested IDs are currently held in memory — the store is
// memory-bounded (maxMemoryMB/maxPackets, see store.go), so a transmission
// well within the SQL window can still have been evicted from RAM. In that
// case a silent 0 would look like "genuinely never relayed" when it's
// really "don't know" — callers should omit the field, not show a zero.
// A PARTIAL match (some found, some evicted) still returns ok=true with
// the known subset's total; the alternative (only ok when ALL match) would
// make the feature return nothing at all once retention exceeds the
// in-memory window, which every other airtime metric already tolerates.
func (s *PacketStore) AirtimeForTransmissions(txIDs []int64) (total time.Duration, ok bool) {
	if s == nil || !s.useResolvedPathIndex || len(txIDs) == 0 {
		return 0, false
	}
	want := make(map[int]bool, len(txIDs))
	for _, id := range txIDs {
		want[int(id)] = true
	}

	s.mu.RLock()
	defer s.mu.RUnlock()
	preset := s.resolveLoRaPreset()
	matched := 0
	for id := range want {
		tx := s.byTxID[id]
		if tx == nil {
			continue
		}
		matched++
		payloadBytes := len(tx.RawHex) / 2
		relays := s.distinctRelayCount(tx)
		total += lora.TimeOnAir(payloadBytes, preset) * time.Duration(relays)
	}
	if matched == 0 {
		return 0, false
	}
	return total, true
}

// computeRelayAirtimeShare aggregates relay-airtime-share per payload_type.
//
// Returns:
//
//	{
//	  "rows":        [{payload_type, type, count, count_pct, score, airtime_pct}, ...] sorted by airtime_pct desc,
//	  "total_count": int,
//	  "total_score": int64 (nanoseconds of LoRa Time-on-Air × repeater-count, summed across packets),
//	  "window":      window label,
//	  "cached":      false (overwritten by cached wrapper),
//	}
func (s *PacketStore) computeRelayAirtimeShare(window TimeWindow) map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ptNames := payloadTypeNames
	preset := s.resolveLoRaPreset()

	type bucket struct {
		count int
		score int64 // sum of ToA(payload) × relays, in nanoseconds
	}
	buckets := make(map[int]*bucket)
	seenHash := make(map[string]bool, len(s.packets))
	totalCount := 0
	var totalScore int64

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

		// payload bytes from RawHex (2 hex chars per byte). Score is
		// LoRa Time-on-Air (nanoseconds) × distinct relays — see
		// resolveLoRaPreset for the assumed PHY block (issue #1768).
		payloadBytes := len(tx.RawHex) / 2
		relays := s.distinctRelayCount(tx)
		toa := lora.TimeOnAir(payloadBytes, preset)
		score := int64(toa) * int64(relays)
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
		"preset":      presetJSON(preset),
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
