package main

import (
	"strings"
	"time"
)

// repeaterEnrichTTL is the safety-net TTL for the bulk enrichment caches.
// Derived as 2× the recomputer's default tick so the cache is always valid
// between background refreshes. Without a recomputer (tests, edge cases)
// the cache rebuilds on-thread at most once per TTL window.
// Note: if analytics.defaultIntervalSeconds is configured above 600 the
// TTL will expire before the recomputer runs; keep that value < TTL/2.
const repeaterEnrichTTL = 2 * repeaterEnrichmentRecomputerDefaultInterval

// GetRepeaterRelayInfoMap returns a cached pubkey → RepeaterRelayInfo
// map covering EVERY pubkey that currently appears as a path hop in any
// non-advert StoreTx. This is the bulk equivalent of calling
// GetRepeaterRelayInfo(pk, windowHours) once per node.
//
// Why this exists (issue #1257): handleNodes used to call the per-node
// helper inside a per-page loop. Each call grabbed its own RLock and
// re-parsed FirstSeen on every StoreTx indexed under that pubkey's
// byPathHop entry (plus, when the pubkey was >= 2 hex chars, the 1-byte
// prefix bucket — which on busy networks fans out to almost the whole
// non-advert tx set). For the default top-50 page of hot repeaters this
// burned 50 lock acquisitions and hundreds of thousands of timestamp
// parses per request, dominating /api/nodes latency.
//
// The cached map is keyed by lowercase pubkey/hop key (same shape as
// byPathHop). Lookups should use strings.ToLower(pk).
//
// The cache is invalidated by TTL only — never by ingest. Up-to-10min
// freshness is fine for an at-a-glance status column; if a fresher
// signal is ever needed for a non-status caller, expose a non-cached path.
func (s *PacketStore) GetRepeaterRelayInfoMap(windowHours float64) map[string]RepeaterRelayInfo {
	s.repeaterEnrichMu.Lock()
	if s.repeaterRelayCache != nil &&
		time.Since(s.repeaterRelayAt) < repeaterEnrichTTL &&
		s.repeaterRelayCacheWin == windowHours {
		cached := s.repeaterRelayCache
		s.repeaterEnrichMu.Unlock()
		return cached
	}
	s.repeaterEnrichMu.Unlock()

	result := s.computeRepeaterRelayInfoMap(windowHours)

	s.repeaterEnrichMu.Lock()
	s.repeaterRelayCache = result
	s.repeaterRelayCacheWin = windowHours
	s.repeaterRelayAt = time.Now()
	s.repeaterEnrichMu.Unlock()
	return result
}

// computeRepeaterRelayInfoMap walks byPathHop once under a single RLock,
// pre-parses every FirstSeen timestamp once (not once-per-pubkey-bucket),
// and emits one RepeaterRelayInfo per hop key.
//
// Time-complexity invariant: O(unique-tx-in-byPathHop + total-key-bucket
// entries). Memory: one map entry per byPathHop key. Both are bounded by
// the same eviction policy that bounds byPathHop itself.
func (s *PacketStore) computeRepeaterRelayInfoMap(windowHours float64) map[string]RepeaterRelayInfo {
	s.mu.RLock()

	// Snapshot the slices (header copy) so we can release the lock before
	// the expensive parse pass. Slice headers point at the live underlying
	// arrays but those are append-only-by-id; the worst-case race here is
	// that ingest grows a slice we already snapshotted (we miss the new
	// tail), which is acceptable for a 15s-TTL status read.
	snap := make(map[string][]*StoreTx, len(s.byPathHop))
	for k, list := range s.byPathHop {
		snap[k] = list
	}

	// Build a tx-id-keyed pre-parsed cache so the inner loop doesn't
	// re-parse the same FirstSeen N times when the same tx is indexed
	// under multiple hop keys (very common — every hop on a path indexes
	// the tx).
	type parsedTx struct {
		t  time.Time
		ok bool
		pt int
	}
	parseCache := make(map[int]parsedTx, 1<<14)
	for _, list := range snap {
		for _, tx := range list {
			if tx == nil {
				continue
			}
			if _, ok := parseCache[tx.ID]; ok {
				continue
			}
			pt := -1
			if tx.PayloadType != nil {
				pt = *tx.PayloadType
			}
			t, ok := parseRelayTS(tx.FirstSeen)
			parseCache[tx.ID] = parsedTx{t: t, ok: ok, pt: pt}
		}
	}
	s.mu.RUnlock()

	now := time.Now().UTC()
	cutoff1h := now.Add(-1 * time.Hour)
	cutoff24h := now.Add(-24 * time.Hour)
	var windowCutoff time.Time
	if windowHours > 0 {
		windowCutoff = now.Add(-time.Duration(windowHours * float64(time.Hour)))
	}

	out := make(map[string]RepeaterRelayInfo, len(snap))
	for key, list := range snap {
		info := RepeaterRelayInfo{WindowHours: windowHours}
		// When key looks like a full pubkey (>= 2 hex chars), also fold
		// in the matching 1-byte raw-prefix bucket to mirror
		// GetRepeaterRelayInfo's behavior. We dedup by tx ID.
		var seen map[int]bool
		if len(key) >= 2 {
			prefix := key[:2]
			if prefix != key {
				if extra := snap[prefix]; len(extra) > 0 {
					seen = make(map[int]bool, len(list)+len(extra))
				}
			}
		}
		visit := func(txs []*StoreTx) {
			for _, tx := range txs {
				if tx == nil {
					continue
				}
				if seen != nil {
					if seen[tx.ID] {
						continue
					}
					seen[tx.ID] = true
				}
				p, ok := parseCache[tx.ID]
				if !ok {
					continue
				}
				if p.pt == payloadTypeAdvert {
					continue
				}
				if !p.ok {
					continue
				}
				if p.t.After(cutoff24h) {
					info.RelayCount24h++
					if p.t.After(cutoff1h) {
						info.RelayCount1h++
					}
				}
				if info.LastRelayed == "" || tx.FirstSeen > info.LastRelayed {
					info.LastRelayed = tx.FirstSeen
					if windowHours > 0 && p.t.After(windowCutoff) {
						info.RelayActive = true
					} else if windowHours > 0 {
						info.RelayActive = false
					}
				}
			}
		}
		visit(list)
		if seen != nil {
			prefix := key[:2]
			if prefix != key {
				visit(snap[prefix])
			}
		}
		out[key] = info
	}
	return out
}

// GetRepeaterUsefulnessScoreMap returns a cached pubkey → 0..1 score
// for every pubkey appearing in byPathHop. Bulk equivalent of
// GetRepeaterUsefulnessScore. See GetRepeaterRelayInfoMap for the
// motivation (#1257).
func (s *PacketStore) GetRepeaterUsefulnessScoreMap() map[string]float64 {
	s.repeaterEnrichMu.Lock()
	if s.repeaterUsefulCache != nil && time.Since(s.repeaterUsefulAt) < repeaterEnrichTTL {
		cached := s.repeaterUsefulCache
		s.repeaterEnrichMu.Unlock()
		return cached
	}
	s.repeaterEnrichMu.Unlock()

	result := s.computeRepeaterUsefulnessScoreMap()

	s.repeaterEnrichMu.Lock()
	s.repeaterUsefulCache = result
	s.repeaterUsefulAt = time.Now()
	s.repeaterEnrichMu.Unlock()
	return result
}

func (s *PacketStore) computeRepeaterUsefulnessScoreMap() map[string]float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	totalNonAdvert := 0
	for pt, list := range s.byPayloadType {
		if pt == payloadTypeAdvert {
			continue
		}
		totalNonAdvert += len(list)
	}
	out := make(map[string]float64, len(s.byPathHop))
	if totalNonAdvert == 0 {
		return out
	}
	denom := float64(totalNonAdvert)
	for key, list := range s.byPathHop {
		relayed := 0
		for _, tx := range list {
			if tx == nil {
				continue
			}
			if tx.PayloadType != nil && *tx.PayloadType == payloadTypeAdvert {
				continue
			}
			relayed++
		}
		if relayed == 0 {
			continue
		}
		score := float64(relayed) / denom
		if score < 0 {
			score = 0
		} else if score > 1 {
			score = 1
		}
		out[key] = score
	}
	return out
}

// lookupRelayInfo is a small helper to make handleNodes' map lookup
// case-insensitive (byPathHop keys are lowercase; pubkeys arriving from
// the DB row may be either case).
func lookupRelayInfo(m map[string]RepeaterRelayInfo, pubkey string) (RepeaterRelayInfo, bool) {
	if v, ok := m[pubkey]; ok {
		return v, true
	}
	if lc := strings.ToLower(pubkey); lc != pubkey {
		if v, ok := m[lc]; ok {
			return v, true
		}
	}
	return RepeaterRelayInfo{}, false
}

// lookupUsefulnessScore mirrors lookupRelayInfo for the score map.
func lookupUsefulnessScore(m map[string]float64, pubkey string) float64 {
	if v, ok := m[pubkey]; ok {
		return v
	}
	if lc := strings.ToLower(pubkey); lc != pubkey {
		if v, ok := m[lc]; ok {
			return v
		}
	}
	return 0
}
