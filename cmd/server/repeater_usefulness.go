package main

import (
	"strings"
	"time"
)

// RepeaterNodeInfo is the combined relay+usefulness data for one node,
// returned by GetRepeaterBatchInfo.
type RepeaterNodeInfo struct {
	RepeaterRelayInfo
	UsefulnessScore float64 `json:"usefulnessScore"`
}

// GetRepeaterBatchInfo enriches a set of pubkeys with relay and usefulness
// data in a single lock acquisition. It is equivalent to calling
// GetRepeaterRelayInfo + GetRepeaterUsefulnessScore per node but acquires
// the store read lock once and computes the shared totalNonAdvert denominator
// once, making the per-node cost O(len(byPathHop[key])) instead of
// O(len(byPayloadType)+len(byPathHop[key])) repeated N times.
func (s *PacketStore) GetRepeaterBatchInfo(pubkeys []string, windowHours float64) map[string]RepeaterNodeInfo {
	result := make(map[string]RepeaterNodeInfo, len(pubkeys))
	if len(pubkeys) == 0 {
		return result
	}

	type entry struct {
		ts string
		pt int
	}
	type keyEntries struct {
		key    string
		orig   string
		prefix string
		list   []entry
	}

	s.mu.RLock()

	// Compute shared denominator once.
	totalNonAdvert := 0
	for pt, list := range s.byPayloadType {
		if pt != payloadTypeAdvert {
			totalNonAdvert += len(list)
		}
	}

	// Collect per-key entries while the lock is held.
	perKey := make([]keyEntries, 0, len(pubkeys))
	for _, orig := range pubkeys {
		if orig == "" {
			continue
		}
		key := strings.ToLower(orig)
		txList := s.byPathHop[key]
		var prefixList []*StoreTx
		if len(key) >= 2 {
			prefix := key[:2]
			if prefix != key {
				prefixList = s.byPathHop[prefix]
			}
		}

		// De-dupe by tx ID and copy out the fields we need.
		uniq := make(map[int]struct{}, len(txList)+len(prefixList))
		for _, tx := range txList {
			if tx != nil {
				uniq[tx.ID] = struct{}{}
			}
		}
		for _, tx := range prefixList {
			if tx != nil {
				uniq[tx.ID] = struct{}{}
			}
		}
		scratch := make([]entry, 0, len(uniq))
		seen := make(map[int]bool, len(uniq))
		collect := func(list []*StoreTx) {
			for _, tx := range list {
				if tx == nil || seen[tx.ID] {
					continue
				}
				seen[tx.ID] = true
				pt := -1
				if tx.PayloadType != nil {
					pt = *tx.PayloadType
				}
				scratch = append(scratch, entry{ts: tx.FirstSeen, pt: pt})
			}
		}
		collect(txList)
		collect(prefixList)
		perKey = append(perKey, keyEntries{key: key, orig: orig, list: scratch})
	}

	s.mu.RUnlock()

	now := time.Now().UTC()
	cutoff1h := now.Add(-1 * time.Hour)
	cutoff24h := now.Add(-24 * time.Hour)
	var windowCutoff time.Time
	if windowHours > 0 {
		windowCutoff = now.Add(-time.Duration(windowHours * float64(time.Hour)))
	}

	for _, ke := range perKey {
		info := RepeaterRelayInfo{WindowHours: windowHours}
		relayed := 0

		var latest time.Time
		var latestRaw string
		for _, e := range ke.list {
			if e.pt == payloadTypeAdvert {
				continue
			}
			relayed++
			t, ok := parseRelayTS(e.ts)
			if !ok {
				continue
			}
			if t.After(latest) {
				latest = t
				latestRaw = e.ts
			}
			if t.After(cutoff24h) {
				info.RelayCount24h++
				if t.After(cutoff1h) {
					info.RelayCount1h++
				}
			}
		}
		if latestRaw != "" {
			info.LastRelayed = latestRaw
			if windowHours > 0 && latest.After(windowCutoff) {
				info.RelayActive = true
			}
		}

		score := 0.0
		if totalNonAdvert > 0 && relayed > 0 {
			score = float64(relayed) / float64(totalNonAdvert)
			if score > 1 {
				score = 1
			}
		}
		result[ke.orig] = RepeaterNodeInfo{RepeaterRelayInfo: info, UsefulnessScore: score}
	}
	return result
}

// GetRepeaterUsefulnessScore returns a 0..1 score representing what
// fraction of non-advert traffic in the store passes through this
// repeater as a relay hop. Issue #672 (Traffic axis only — bridge,
// coverage, and redundancy axes are deferred to follow-up work).
//
// Numerator:   count of non-advert StoreTx entries indexed under
//              pubkey in byPathHop.
// Denominator: total non-advert StoreTx entries in the store
//              (sum of byPayloadType for all keys != payloadTypeAdvert).
//
// Returns 0 when there is no non-advert traffic, the pubkey is empty,
// or the repeater never appears as a relay hop. Scores are clamped to
// [0,1] for defensive bounds.
//
// Cost: O(N) over byPayloadType keys (typically <20) plus the per-hop
// slice for pubkey. Cheap relative to the per-request enrichment loop
// in handleNodes; if it ever shows up in profiles, denominator can be
// memoized off store invalidation.
func (s *PacketStore) GetRepeaterUsefulnessScore(pubkey string) float64 {
	if pubkey == "" {
		return 0
	}
	key := strings.ToLower(pubkey)

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Denominator: total non-advert packets.
	totalNonAdvert := 0
	for pt, list := range s.byPayloadType {
		if pt == payloadTypeAdvert {
			continue
		}
		totalNonAdvert += len(list)
	}
	if totalNonAdvert == 0 {
		return 0
	}

	// Numerator: this repeater's non-advert hop appearances.
	relayed := 0
	for _, tx := range s.byPathHop[key] {
		if tx == nil {
			continue
		}
		if tx.PayloadType != nil && *tx.PayloadType == payloadTypeAdvert {
			continue
		}
		relayed++
	}

	score := float64(relayed) / float64(totalNonAdvert)
	if score < 0 {
		return 0
	}
	if score > 1 {
		return 1
	}
	return score
}
