// store_subpath.go — subpath-index build/maintenance and subpath analytics.

package main

import (
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"
)

// addTxToSubpathIndex extracts all raw subpaths (lengths 2–8) from tx and
// increments their counts in the index.  Returns true if the tx contributed
// (path had ≥ 2 hops).
func addTxToSubpathIndex(idx map[string]int, tx *StoreTx) bool {
	return addTxToSubpathIndexFull(idx, nil, tx)
}

// addTxToSubpathIndexFull is like addTxToSubpathIndex but also appends
// tx to txIdx for each subpath key (if txIdx is non-nil).
func addTxToSubpathIndexFull(idx map[string]int, txIdx map[string][]*StoreTx, tx *StoreTx) bool {
	hops := txGetParsedPath(tx)
	if len(hops) < 2 {
		return false
	}
	maxL := min(8, len(hops))
	for l := 2; l <= maxL; l++ {
		for start := 0; start <= len(hops)-l; start++ {
			key := strings.ToLower(strings.Join(hops[start:start+l], ","))
			idx[key]++
			if txIdx != nil {
				txIdx[key] = append(txIdx[key], tx)
			}
		}
	}
	return true
}

// removeTxFromSubpathIndex is the inverse of addTxToSubpathIndex — it
// decrements counts for all raw subpaths of tx.  Returns true if the tx
// had a path.
func removeTxFromSubpathIndex(idx map[string]int, tx *StoreTx) bool {
	return removeTxFromSubpathIndexFull(idx, nil, tx)
}

// removeTxFromSubpathIndexFull is like removeTxFromSubpathIndex but also
// removes tx from txIdx for each subpath key (if txIdx is non-nil).
func removeTxFromSubpathIndexFull(idx map[string]int, txIdx map[string][]*StoreTx, tx *StoreTx) bool {
	hops := txGetParsedPath(tx)
	if len(hops) < 2 {
		return false
	}
	maxL := min(8, len(hops))
	for l := 2; l <= maxL; l++ {
		for start := 0; start <= len(hops)-l; start++ {
			key := strings.ToLower(strings.Join(hops[start:start+l], ","))
			idx[key]--
			if idx[key] <= 0 {
				delete(idx, key)
			}
			if txIdx != nil {
				txs := txIdx[key]
				for i, t := range txs {
					if t == tx {
						txIdx[key] = append(txs[:i], txs[i+1:]...)
						break
					}
				}
				if len(txIdx[key]) == 0 {
					delete(txIdx, key)
				}
			}
		}
	}
	return true
}

// buildSubpathIndex scans all packets and populates spIndex + spTotalPaths.
// Must be called with s.mu held.
func (s *PacketStore) buildSubpathIndex() {
	s.spIndex = make(map[string]int, 4096)
	s.spTxIndex = make(map[string][]*StoreTx, 4096)
	s.spTotalPaths = 0
	for _, tx := range s.packets {
		if addTxToSubpathIndexFull(s.spIndex, s.spTxIndex, tx) {
			s.spTotalPaths++
		}
	}
	log.Printf("[store] Built subpath index: %d unique raw subpaths from %d paths",
		len(s.spIndex), s.spTotalPaths)
}

func (s *PacketStore) GetAnalyticsSubpaths(region string, minLen, maxLen, limit int) map[string]interface{} {
	cacheKey := fmt.Sprintf("%s|%d|%d|%d", region, minLen, maxLen, limit)

	s.cacheMu.Lock()
	if cached, ok := s.subpathCache[cacheKey]; ok && time.Now().Before(cached.expiresAt) {
		s.cacheHits++
		s.cacheMu.Unlock()
		return cached.data
	}
	s.cacheMisses++
	s.cacheMu.Unlock()

	result := s.computeAnalyticsSubpaths(region, minLen, maxLen, limit)

	s.cacheMu.Lock()
	s.subpathCache[cacheKey] = &cachedResult{data: result, expiresAt: time.Now().Add(s.rfCacheTTL)}
	s.cacheMu.Unlock()

	return result
}

// GetAnalyticsSubpathsBulk returns multiple length-range buckets from a single
// scan of the subpath index, avoiding repeated iterations.
func (s *PacketStore) GetAnalyticsSubpathsBulk(region string, groups []subpathGroup) []map[string]interface{} {
	// For region queries or when there are few groups, fall back to individual calls
	// which benefit from per-key caching.
	if region != "" {
		results := make([]map[string]interface{}, len(groups))
		for i, g := range groups {
			results[i] = s.GetAnalyticsSubpaths(region, g.MinLen, g.MaxLen, g.Limit)
		}
		return results
	}

	// Check if all groups are cached.
	allCached := true
	cachedResults := make([]map[string]interface{}, len(groups))
	s.cacheMu.Lock()
	for i, g := range groups {
		cacheKey := fmt.Sprintf("|%d|%d|%d", g.MinLen, g.MaxLen, g.Limit)
		if cached, ok := s.subpathCache[cacheKey]; ok && time.Now().Before(cached.expiresAt) {
			cachedResults[i] = cached.data
		} else {
			allCached = false
			break
		}
	}
	if allCached {
		s.cacheHits += int64(len(groups))
		s.cacheMu.Unlock()
		return cachedResults
	}
	s.cacheMu.Unlock()

	// Single scan: bucket by hop length into per-group accumulators.
	s.mu.RLock()
	_, pm := s.getCachedNodesAndPM()
	// Aggregate hop-disambiguation context across all packets so the
	// resolver's tiers 1 and 2 light up even on this bulk-aggregate path
	// (the index iterates raw subpath strings, not per-tx). See #1197.
	contextPubkeys := buildAggregateHopContextPubkeys(s.packets, pm)
	hopCache := make(map[string]*nodeInfo)
	graph := s.graph.Load() // hoist out of resolver closure (PR #1208 carmack #1)
	resolveHop := func(hop string) string {
		if cached, ok := hopCache[hop]; ok {
			if cached != nil {
				return cached.Name
			}
			return hop
		}
		r, _, _ := pm.resolveWithContext(hop, contextPubkeys, graph)
		hopCache[hop] = r
		if r != nil {
			return r.Name
		}
		return hop
	}

	perGroup := make([]map[string]*subpathAccum, len(groups))
	for i := range groups {
		perGroup[i] = make(map[string]*subpathAccum)
	}

	for rawKey, count := range s.spIndex {
		hops := strings.Split(rawKey, ",")
		hopLen := len(hops)

		// Resolve hop names once, reuse across groups.
		var named []string
		var namedKey string
		resolved := false

		for gi, g := range groups {
			if hopLen < g.MinLen || hopLen > g.MaxLen {
				continue
			}
			if !resolved {
				named = make([]string, hopLen)
				for i, h := range hops {
					named[i] = resolveHop(h)
				}
				namedKey = strings.Join(named, " → ")
				resolved = true
			}
			entry := perGroup[gi][namedKey]
			if entry == nil {
				entry = &subpathAccum{raw: rawKey}
				perGroup[gi][namedKey] = entry
			}
			entry.count += count
		}
	}
	totalPaths := s.spTotalPaths
	s.mu.RUnlock()

	results := make([]map[string]interface{}, len(groups))
	for i, g := range groups {
		results[i] = s.rankSubpaths(perGroup[i], totalPaths, g.Limit)
	}

	// Cache individual results for future single-key lookups too.
	s.cacheMu.Lock()
	for i, g := range groups {
		cacheKey := fmt.Sprintf("|%d|%d|%d", g.MinLen, g.MaxLen, g.Limit)
		s.subpathCache[cacheKey] = &cachedResult{data: results[i], expiresAt: time.Now().Add(s.rfCacheTTL)}
	}
	s.cacheMu.Unlock()

	return results
}

// subpathAccum holds a running count for a single named subpath.
type subpathAccum struct {
	count int
	raw   string // first raw-hop key seen (used for rawHops in the API response)
}

func (s *PacketStore) computeAnalyticsSubpaths(region string, minLen, maxLen, limit int) map[string]interface{} {
	// Refresh the node cache and resolve the region→observer set before
	// taking s.mu so neither's cache-miss SQL query runs under the read
	// lock (review item #1/#2). Both use their own mutexes, not s.mu.
	_, pm := s.getCachedNodesAndPM()
	var regionObs map[string]bool
	if region != "" {
		regionObs = s.resolveRegionObservers(region)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Aggregate hop-disambiguation context across all packets — bulk
	// aggregator over s.spIndex / per-tx fallback both need it. See #1197.
	contextPubkeys := buildAggregateHopContextPubkeys(s.packets, pm)
	hopCache := make(map[string]*nodeInfo)
	graph := s.graph.Load() // hoist out of resolver closure (PR #1208 carmack #1)
	resolveHop := func(hop string) string {
		if cached, ok := hopCache[hop]; ok {
			if cached != nil {
				return cached.Name
			}
			return hop
		}
		r, _, _ := pm.resolveWithContext(hop, contextPubkeys, graph)
		hopCache[hop] = r
		if r != nil {
			return r.Name
		}
		return hop
	}

	// For region queries fall back to packet iteration (region filtering
	// requires per-transmission observer checks).
	if region != "" {
		return s.computeSubpathsSlow(regionObs, minLen, maxLen, limit, resolveHop)
	}

	// Fast path: read from precomputed raw-hop subpath index.
	// Resolve raw hop prefixes to names and merge counts.
	namedCounts := make(map[string]*subpathAccum, len(s.spIndex))
	for rawKey, count := range s.spIndex {
		hops := strings.Split(rawKey, ",")
		hopLen := len(hops)
		if hopLen < minLen || hopLen > maxLen {
			continue
		}
		named := make([]string, hopLen)
		for i, h := range hops {
			named[i] = resolveHop(h)
		}
		namedKey := strings.Join(named, " → ")
		entry := namedCounts[namedKey]
		if entry == nil {
			entry = &subpathAccum{raw: rawKey}
			namedCounts[namedKey] = entry
		}
		entry.count += count
	}

	return s.rankSubpaths(namedCounts, s.spTotalPaths, limit)
}

// computeSubpathsSlow is the original O(N) packet-iteration path, used only
// for region-filtered queries where we must check per-transmission observers.
// regionObs is the pre-resolved observer-ID set for the region; the caller
// resolves it before acquiring s.mu so no SQL runs under the read lock.
func (s *PacketStore) computeSubpathsSlow(regionObs map[string]bool, minLen, maxLen, limit int, resolveHop func(string) string) map[string]interface{} {
	subpathCounts := make(map[string]*subpathAccum)
	totalPaths := 0

	for _, tx := range s.packets {
		hops := txGetParsedPath(tx)
		if len(hops) < 2 {
			continue
		}
		if regionObs != nil {
			match := false
			for _, obs := range tx.Observations {
				if regionObs[obs.ObserverID] {
					match = true
					break
				}
			}
			if !match {
				continue
			}
		}
		totalPaths++

		named := make([]string, len(hops))
		for i, h := range hops {
			named[i] = resolveHop(h)
		}

		for l := minLen; l <= maxLen && l <= len(named); l++ {
			for start := 0; start <= len(named)-l; start++ {
				sub := strings.Join(named[start:start+l], " → ")
				raw := strings.Join(hops[start:start+l], ",")
				entry := subpathCounts[sub]
				if entry == nil {
					entry = &subpathAccum{raw: raw}
					subpathCounts[sub] = entry
				}
				entry.count++
			}
		}
	}

	return s.rankSubpaths(subpathCounts, totalPaths, limit)
}

// rankSubpaths sorts accumulated subpath counts by frequency, truncates to
// limit, and builds the API response map.
func (s *PacketStore) rankSubpaths(counts map[string]*subpathAccum, totalPaths, limit int) map[string]interface{} {
	type subpathEntry struct {
		path  string
		count int
		raw   string
	}
	ranked := make([]subpathEntry, 0, len(counts))
	for path, data := range counts {
		ranked = append(ranked, subpathEntry{path, data.count, data.raw})
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].count > ranked[j].count })
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}

	subpaths := make([]map[string]interface{}, 0, len(ranked))
	for _, e := range ranked {
		pct := 0.0
		if totalPaths > 0 {
			pct = math.Round(float64(e.count)/float64(totalPaths)*1000) / 10
		}
		subpaths = append(subpaths, map[string]interface{}{
			"path":    e.path,
			"rawHops": strings.Split(e.raw, ","),
			"count":   e.count,
			"hops":    len(strings.Split(e.path, " → ")),
			"pct":     pct,
		})
	}

	return map[string]interface{}{
		"subpaths":   subpaths,
		"totalPaths": totalPaths,
	}
}

func (s *PacketStore) GetSubpathDetail(rawHops []string) map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()

	_, pm := s.getCachedNodesAndPM()

	// Build the subpath key the same way the index does (lowercase, comma-joined)
	spKey := strings.ToLower(strings.Join(rawHops, ","))

	// Direct lookup instead of scanning all packets
	matchedTxs := s.spTxIndex[spKey]

	// Hop-disambiguation context: union over the matched txs that produced
	// this subpath. This is the right scope — those are the packets that
	// witnessed the requested hop sequence. See #1197.
	contextPubkeys := buildAggregateHopContextPubkeys(matchedTxs, pm)
	// Hoist atomic graph.Load() once for the whole request (PR #1208
	// carmack #1) — used by the rawHops loop AND the matched-tx loop below.
	graph := s.graph.Load()

	// Resolve the requested hops
	nodes := make([]map[string]interface{}, len(rawHops))
	for i, hop := range rawHops {
		r, _, _ := pm.resolveWithContext(hop, contextPubkeys, graph)
		entry := map[string]interface{}{"hop": hop, "name": hop, "lat": nil, "lon": nil, "pubkey": nil}
		if r != nil {
			entry["name"] = r.Name
			entry["pubkey"] = r.PublicKey
			if r.HasGPS {
				entry["lat"] = r.Lat
				entry["lon"] = r.Lon
			}
		}
		nodes[i] = entry
	}

	hourBuckets := make([]int, 24)
	var snrSum, rssiSum float64
	var snrCount, rssiCount int
	observers := map[string]int{}
	parentPaths := map[string]int{}
	matchCount := len(matchedTxs)
	var firstSeen, lastSeen string

	for _, tx := range matchedTxs {
		ts := tx.FirstSeen
		if ts != "" {
			if firstSeen == "" || ts < firstSeen {
				firstSeen = ts
			}
			if lastSeen == "" || ts > lastSeen {
				lastSeen = ts
			}
			t, err := time.Parse(time.RFC3339, ts)
			if err != nil {
				t, err = time.Parse("2006-01-02 15:04:05", ts)
			}
			if err == nil {
				hourBuckets[t.Hour()]++
			}
		}
		if tx.SNR != nil {
			snrSum += *tx.SNR
			snrCount++
		}
		if tx.RSSI != nil {
			rssiSum += *tx.RSSI
			rssiCount++
		}
		if tx.ObserverName != "" {
			observers[tx.ObserverName]++
		}

		// Full parent path (resolved). Per-tx context so the resolver picks
		// the right candidate when prefixes are ambiguous. See #1197.
		txCtx := buildHopContextPubkeys(tx, pm)
		hops := txGetParsedPath(tx)
		resolved := make([]string, len(hops))
		for i, h := range hops {
			r, _, _ := pm.resolveWithContext(h, txCtx, graph)
			if r != nil {
				resolved[i] = r.Name
			} else {
				resolved[i] = h
			}
		}
		fullPath := strings.Join(resolved, " → ")
		parentPaths[fullPath]++
	}

	var avgSnr, avgRssi interface{}
	if snrCount > 0 {
		avgSnr = snrSum / float64(snrCount)
	}
	if rssiCount > 0 {
		avgRssi = rssiSum / float64(rssiCount)
	}

	topParents := make([]map[string]interface{}, 0)
	for path, count := range parentPaths {
		topParents = append(topParents, map[string]interface{}{"path": path, "count": count})
	}
	sort.Slice(topParents, func(i, j int) bool {
		return topParents[i]["count"].(int) > topParents[j]["count"].(int)
	})
	if len(topParents) > 15 {
		topParents = topParents[:15]
	}

	topObs := make([]map[string]interface{}, 0)
	for name, count := range observers {
		topObs = append(topObs, map[string]interface{}{"name": name, "count": count})
	}
	sort.Slice(topObs, func(i, j int) bool {
		return topObs[i]["count"].(int) > topObs[j]["count"].(int)
	})
	if len(topObs) > 10 {
		topObs = topObs[:10]
	}

	return map[string]interface{}{
		"hops":             rawHops,
		"nodes":            nodes,
		"totalMatches":     matchCount,
		"firstSeen":        firstSeen,
		"lastSeen":         lastSeen,
		"signal":           map[string]interface{}{"avgSnr": avgSnr, "avgRssi": avgRssi, "samples": snrCount},
		"hourDistribution": hourBuckets,
		"parentPaths":      topParents,
		"observers":        topObs,
	}
}
