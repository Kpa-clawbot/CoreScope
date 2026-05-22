// store_distance.go — distance-index build/update and distance analytics.

package main

import (
	"encoding/json"
	"log"
	"math"
	"sort"
	"strings"
	"time"
)

// Precomputed distance records for fast analytics aggregation.
type distHopRecord struct {
	FromName   string
	FromPk     string
	ToName     string
	ToPk       string
	Dist       float64
	Type       string // "R↔R", "C↔R", "C↔C"
	SNR        *float64
	Hash       string
	Timestamp  string
	HourBucket string
	tx         *StoreTx
}

// dedupeHopsByPair groups hops by unordered node-pair, keeps the max-distance
// record per pair, and computes obsCount / bestSnr / medianSnr.  limit caps the
// number of returned entries (sorted by distance descending).
func dedupeHopsByPair(hops []distHopRecord, limit int) []map[string]interface{} {
	type pairAgg struct {
		best     *distHopRecord
		obsCount int
		maxSNR   *float64
		snrs     []float64
	}
	pairMap := make(map[string]*pairAgg)
	for i := range hops {
		h := &hops[i]
		pk1, pk2 := h.FromPk, h.ToPk
		if pk1 > pk2 {
			pk1, pk2 = pk2, pk1
		}
		key := pk1 + "|" + pk2
		agg, ok := pairMap[key]
		if !ok {
			agg = &pairAgg{}
			pairMap[key] = agg
		}
		agg.obsCount++
		if h.SNR != nil {
			agg.snrs = append(agg.snrs, *h.SNR)
			if agg.maxSNR == nil || *h.SNR > *agg.maxSNR {
				v := *h.SNR
				agg.maxSNR = &v
			}
		}
		if agg.best == nil || h.Dist > agg.best.Dist {
			agg.best = h
		}
	}
	type pairEntry struct {
		key string
		agg *pairAgg
	}
	pairs := make([]pairEntry, 0, len(pairMap))
	for k, v := range pairMap {
		pairs = append(pairs, pairEntry{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].agg.best.Dist > pairs[j].agg.best.Dist })
	result := make([]map[string]interface{}, 0, min(limit, len(pairs)))
	for i, pe := range pairs {
		if i >= limit {
			break
		}
		h := pe.agg.best
		var medianSNR *float64
		if len(pe.agg.snrs) > 0 {
			sorted := make([]float64, len(pe.agg.snrs))
			copy(sorted, pe.agg.snrs)
			sort.Float64s(sorted)
			mid := len(sorted) / 2
			if len(sorted)%2 == 0 {
				v := (sorted[mid-1] + sorted[mid]) / 2
				medianSNR = &v
			} else {
				v := sorted[mid]
				medianSNR = &v
			}
		}
		result = append(result, map[string]interface{}{
			"fromName": h.FromName, "fromPk": h.FromPk,
			"toName": h.ToName, "toPk": h.ToPk,
			"dist": h.Dist, "type": h.Type,
			"bestSnr": floatPtrOrNil(pe.agg.maxSNR), "medianSnr": floatPtrOrNil(medianSNR),
			"obsCount": pe.agg.obsCount,
			"hash":     h.Hash, "timestamp": h.Timestamp,
		})
	}
	return result
}

type distPathRecord struct {
	Hash      string
	TotalDist float64
	HopCount  int
	Timestamp string
	Hops      []distHopDetail
	tx        *StoreTx
}

type distHopDetail struct {
	FromName string
	FromPk   string
	ToName   string
	ToPk     string
	Dist     float64
}

// rebuildDistIndexMaps recomputes distHopsByTx / distPathsByTx from the current
// distHops / distPaths slices. O(records) — used after a full rebuild or a
// compaction, where the caller already pays an O(records) cost. Must be called
// with s.mu held.
func (s *PacketStore) rebuildDistIndexMaps() {
	s.distHopsByTx = make(map[int][]int, len(s.distHops))
	for i := range s.distHops {
		if tx := s.distHops[i].tx; tx != nil {
			s.distHopsByTx[tx.ID] = append(s.distHopsByTx[tx.ID], i)
		}
	}
	s.distPathsByTx = make(map[int][]int, len(s.distPaths))
	for i := range s.distPaths {
		if tx := s.distPaths[i].tx; tx != nil {
			s.distPathsByTx[tx.ID] = append(s.distPathsByTx[tx.ID], i)
		}
	}
}

// appendDistRecords appends a tx's freshly computed distance records to the
// flat slices and records their positions in the by-tx index. Must be called
// with s.mu held.
func (s *PacketStore) appendDistRecords(txID int, txHops []distHopRecord, txPath *distPathRecord) {
	if len(txHops) > 0 {
		if s.distHopsByTx == nil {
			s.distHopsByTx = make(map[int][]int)
		}
		for i := range txHops {
			s.distHopsByTx[txID] = append(s.distHopsByTx[txID], len(s.distHops))
			s.distHops = append(s.distHops, txHops[i])
		}
	}
	if txPath != nil {
		if s.distPathsByTx == nil {
			s.distPathsByTx = make(map[int][]int)
		}
		s.distPathsByTx[txID] = append(s.distPathsByTx[txID], len(s.distPaths))
		s.distPaths = append(s.distPaths, *txPath)
	}
}

// removeDistRecordsForTxs removes all distance records belonging to the given
// txIDs using the by-tx position index and swap-with-last deletion, so the cost
// is O(records removed) rather than O(total records) (review item #6). Must be
// called with s.mu held.
func (s *PacketStore) removeDistRecordsForTxs(txIDs map[int]bool) {
	if len(txIDs) == 0 {
		return
	}
	// distHops
	if s.distHopsByTx != nil {
		// Collect positions to drop, highest-first so swap-with-last never
		// invalidates a not-yet-processed position.
		var drop []int
		for id := range txIDs {
			drop = append(drop, s.distHopsByTx[id]...)
			delete(s.distHopsByTx, id)
		}
		sort.Sort(sort.Reverse(sort.IntSlice(drop)))
		for _, pos := range drop {
			last := len(s.distHops) - 1
			if pos != last {
				moved := s.distHops[last]
				s.distHops[pos] = moved
				// Update the moved record's index entry: last → pos.
				if moved.tx != nil {
					positions := s.distHopsByTx[moved.tx.ID]
					for j, p := range positions {
						if p == last {
							positions[j] = pos
							break
						}
					}
				}
			}
			s.distHops = s.distHops[:last]
		}
	}
	// distPaths
	if s.distPathsByTx != nil {
		var drop []int
		for id := range txIDs {
			drop = append(drop, s.distPathsByTx[id]...)
			delete(s.distPathsByTx, id)
		}
		sort.Sort(sort.Reverse(sort.IntSlice(drop)))
		for _, pos := range drop {
			last := len(s.distPaths) - 1
			if pos != last {
				moved := s.distPaths[last]
				s.distPaths[pos] = moved
				if moved.tx != nil {
					positions := s.distPathsByTx[moved.tx.ID]
					for j, p := range positions {
						if p == last {
							positions[j] = pos
							break
						}
					}
				}
			}
			s.distPaths = s.distPaths[:last]
		}
	}
}

// updateDistanceIndexForTxs removes old distance records for the given
// transmissions and recomputes them. Builds lookup maps once, amortising the
// cost across all changed txs in a single ingest cycle. Must be called with
// s.mu held.
func (s *PacketStore) updateDistanceIndexForTxs(txs []*StoreTx) {
	// Remove old records for all changed txs first — targeted O(records
	// removed) deletion via the by-tx position index (review item #6).
	if s.distHopsByTx == nil || s.distPathsByTx == nil {
		s.rebuildDistIndexMaps()
	}
	removeIDs := make(map[int]bool, len(txs))
	for _, tx := range txs {
		removeIDs[tx.ID] = true
	}
	s.removeDistRecordsForTxs(removeIDs)

	// Build lookup maps once.
	allNodes, pm := s.getCachedNodesAndPM()
	nodeByPk := make(map[string]*nodeInfo, len(allNodes))
	repeaterSet := make(map[string]bool)
	for i := range allNodes {
		nd := &allNodes[i]
		nodeByPk[nd.PublicKey] = nd
		if strings.Contains(strings.ToLower(nd.Role), "repeater") {
			repeaterSet[nd.PublicKey] = true
		}
	}
	// Per-tx hop resolver shared across the recompute loop (#1197 perf).
	resolveHop, setContext := s.hopResolverPerTx(pm)
	// Recompute distance records for each changed tx.
	for _, tx := range txs {
		// Per-tx context for hop disambiguation (#1197).
		setContext(buildHopContextPubkeys(tx, pm))
		txHops, txPath := computeDistancesForTx(tx, nodeByPk, repeaterSet, resolveHop)
		s.appendDistRecords(tx.ID, txHops, txPath)
	}
}

// buildDistanceIndex precomputes haversine distances for all packets.
// Must be called with s.mu held (Lock).
func (s *PacketStore) buildDistanceIndex() {
	allNodes, pm := s.getCachedNodesAndPM()
	nodeByPk := make(map[string]*nodeInfo, len(allNodes))
	repeaterSet := make(map[string]bool)
	for i := range allNodes {
		n := &allNodes[i]
		nodeByPk[n.PublicKey] = n
		if strings.Contains(strings.ToLower(n.Role), "repeater") {
			repeaterSet[n.PublicKey] = true
		}
	}

	hops := make([]distHopRecord, 0, len(s.packets))
	paths := make([]distPathRecord, 0, len(s.packets)/2)

	// Per-tx hop resolver shared across the per-tx loop (#1197 perf).
	resolveHop, setContext := s.hopResolverPerTx(pm)
	for _, tx := range s.packets {
		// Per-tx context for hop disambiguation (#1197).
		setContext(buildHopContextPubkeys(tx, pm))
		txHops, txPath := computeDistancesForTx(tx, nodeByPk, repeaterSet, resolveHop)
		if len(txHops) > 0 {
			hops = append(hops, txHops...)
		}
		if txPath != nil {
			paths = append(paths, *txPath)
		}
	}

	s.distHops = hops
	s.distPaths = paths
	// Rebuild the by-tx position index so eviction can do targeted removal
	// (review item #6).
	s.rebuildDistIndexMaps()
	log.Printf("[store] Built distance index: %d hop records, %d path records",
		len(s.distHops), len(s.distPaths))
}

func computeDistancesForTx(tx *StoreTx, nodeByPk map[string]*nodeInfo, repeaterSet map[string]bool, resolveHop func(string) *nodeInfo) ([]distHopRecord, *distPathRecord) {
	pathHops := txGetParsedPath(tx)
	if len(pathHops) == 0 {
		return nil, nil
	}

	resolved := make([]*nodeInfo, len(pathHops))
	for i, h := range pathHops {
		resolved[i] = resolveHop(h)
	}

	var senderNode *nodeInfo
	if tx.DecodedJSON != "" {
		var dec map[string]interface{}
		if json.Unmarshal([]byte(tx.DecodedJSON), &dec) == nil {
			if pk, ok := dec["pubKey"].(string); ok && pk != "" {
				senderNode = nodeByPk[pk]
			}
		}
	}

	chain := make([]*nodeInfo, 0, len(pathHops)+1)
	if senderNode != nil && senderNode.HasGPS {
		chain = append(chain, senderNode)
	}
	for _, r := range resolved {
		if r != nil && r.HasGPS {
			chain = append(chain, r)
		}
	}
	if len(chain) < 2 {
		return nil, nil
	}

	hourBucket := ""
	if tx.FirstSeen != "" && len(tx.FirstSeen) >= 13 {
		hourBucket = tx.FirstSeen[:13]
	}

	var hopRecords []distHopRecord
	var hopDetails []distHopDetail
	pathDist := 0.0

	for i := 0; i < len(chain)-1; i++ {
		a, b := chain[i], chain[i+1]
		dist := haversineKm(a.Lat, a.Lon, b.Lat, b.Lon)
		if dist > 300 {
			continue
		}

		aRep := repeaterSet[a.PublicKey]
		bRep := repeaterSet[b.PublicKey]
		var hopType string
		if aRep && bRep {
			hopType = "R↔R"
		} else if !aRep && !bRep {
			hopType = "C↔C"
		} else {
			hopType = "C↔R"
		}

		roundedDist := math.Round(dist*100) / 100
		hopRecords = append(hopRecords, distHopRecord{
			FromName: a.Name, FromPk: a.PublicKey,
			ToName: b.Name, ToPk: b.PublicKey,
			Dist: roundedDist, Type: hopType,
			SNR: tx.SNR, Hash: tx.Hash, Timestamp: tx.FirstSeen,
			HourBucket: hourBucket, tx: tx,
		})
		hopDetails = append(hopDetails, distHopDetail{
			FromName: a.Name, FromPk: a.PublicKey,
			ToName: b.Name, ToPk: b.PublicKey,
			Dist: roundedDist,
		})
		pathDist += dist
	}

	if len(hopRecords) == 0 {
		return nil, nil
	}

	pathRec := &distPathRecord{
		Hash: tx.Hash, TotalDist: math.Round(pathDist*100) / 100,
		HopCount: len(hopDetails), Timestamp: tx.FirstSeen,
		Hops: hopDetails, tx: tx,
	}
	return hopRecords, pathRec
}

func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	return R * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

// GetAnalyticsDistance returns distance analytics. For the default query
// (region="") the steady-state recomputer snapshot (issue #1240) is preferred
// — atomic load, never blocks.
func (s *PacketStore) GetAnalyticsDistance(region string) map[string]interface{} {
	if region == "" {
		s.analyticsRecomputerMu.RLock()
		rc := s.recompDistance
		s.analyticsRecomputerMu.RUnlock()
		if rc != nil {
			if v := rc.Load(); v != nil {
				if m, ok := v.(map[string]interface{}); ok {
					s.cacheMu.Lock()
					s.cacheHits++
					s.cacheMu.Unlock()
					return m
				}
			}
		}
	}
	s.cacheMu.Lock()
	if cached, ok := s.distCache[region]; ok && time.Now().Before(cached.expiresAt) {
		s.cacheHits++
		s.cacheMu.Unlock()
		return cached.data
	}
	s.cacheMisses++
	s.cacheMu.Unlock()

	result := s.computeAnalyticsDistance(region)

	s.cacheMu.Lock()
	s.distCache[region] = &cachedResult{data: result, expiresAt: time.Now().Add(s.rfCacheTTL)}
	s.cacheMu.Unlock()

	return result
}

func (s *PacketStore) computeAnalyticsDistance(region string) map[string]interface{} {
	// #1239: hold s.mu.RLock() only long enough to (a) snapshot the
	// distHops/distPaths slice headers and (b) build the region match
	// set (which reads tx.Observations, a field mutated by ingest under
	// s.mu.Lock). All filtering, sorting, deduping, histogram building
	// and category stats run on locally-captured slices OUTSIDE the
	// lock so concurrent ingest writers are not blocked by readers.
	//
	// Safety: snapshot slice headers are O(1). distHops/distPaths are
	// append-only via re-slice in buildDistanceIndex / updateDistanceIndexForTxs
	// under s.mu.Lock; if the backing array is reallocated after we
	// release the RLock, our snapshot still points at the prior backing
	// array (kept alive by GC) and observes the consistent length we
	// captured. The distHopRecord / distPathRecord values themselves
	// are value types (not pointers to live records) so we cannot read
	// torn writes from them post-release.
	var regionObs map[string]bool
	if region != "" {
		// resolveRegionObservers uses its own mutex (regionObsMu)
		// and is safe to call without s.mu held.
		regionObs = s.resolveRegionObservers(region)
	}

	s.mu.RLock()
	hopsSnap := s.distHops
	pathsSnap := s.distPaths

	// Build region match set INSIDE the lock — touches tx.Observations
	// (slice header mutated by ingest). For non-region calls (the common
	// cold path) we skip this entirely and the RLock hold is microseconds.
	var matchSet map[*StoreTx]bool
	if regionObs != nil {
		matchSet = make(map[*StoreTx]bool)
		seen := make(map[*StoreTx]bool)
		for i := range hopsSnap {
			tx := hopsSnap[i].tx
			if tx == nil || seen[tx] {
				continue
			}
			seen[tx] = true
			for _, obs := range tx.Observations {
				if regionObs[obs.ObserverID] {
					matchSet[tx] = true
					break
				}
			}
		}
		for i := range pathsSnap {
			tx := pathsSnap[i].tx
			if tx == nil || seen[tx] {
				continue
			}
			seen[tx] = true
			for _, obs := range tx.Observations {
				if regionObs[obs.ObserverID] {
					matchSet[tx] = true
					break
				}
			}
		}
	}
	s.mu.RUnlock()

	// Everything below operates on hopsSnap / pathsSnap / matchSet —
	// no s.mu, no s.distHops / s.distPaths access. Safe to run while
	// ingest writers reallocate the underlying store-owned slices.

	// Filter precomputed hop records (copy to avoid mutating precomputed data during sort)
	filteredHops := make([]distHopRecord, 0, len(hopsSnap))
	for i := range hopsSnap {
		if matchSet == nil || matchSet[hopsSnap[i].tx] {
			filteredHops = append(filteredHops, hopsSnap[i])
		}
	}

	// Filter precomputed path records
	filteredPaths := make([]distPathRecord, 0, len(pathsSnap))
	for i := range pathsSnap {
		if matchSet == nil || matchSet[pathsSnap[i].tx] {
			filteredPaths = append(filteredPaths, pathsSnap[i])
		}
	}

	// Build category stats and time series from precomputed data
	catDists := map[string][]float64{"R↔R": {}, "C↔R": {}, "C↔C": {}}
	distByHour := map[string][]float64{}
	for i := range filteredHops {
		h := &filteredHops[i]
		catDists[h.Type] = append(catDists[h.Type], h.Dist)
		if h.HourBucket != "" {
			distByHour[h.HourBucket] = append(distByHour[h.HourBucket], h.Dist)
		}
	}

	topHops := dedupeHopsByPair(filteredHops, 20)

	// Sort and pick top paths
	sort.Slice(filteredPaths, func(i, j int) bool { return filteredPaths[i].TotalDist > filteredPaths[j].TotalDist })
	topPaths := make([]map[string]interface{}, 0)
	for i := range filteredPaths {
		if i >= 20 {
			break
		}
		p := &filteredPaths[i]
		hops := make([]map[string]interface{}, len(p.Hops))
		for j, hd := range p.Hops {
			hops[j] = map[string]interface{}{
				"fromName": hd.FromName, "fromPk": hd.FromPk,
				"toName": hd.ToName, "toPk": hd.ToPk,
				"dist": hd.Dist,
			}
		}
		topPaths = append(topPaths, map[string]interface{}{
			"hash": p.Hash, "totalDist": p.TotalDist,
			"hopCount": p.HopCount, "timestamp": p.Timestamp, "hops": hops,
		})
	}

	// Category stats
	medianF := func(arr []float64) float64 {
		if len(arr) == 0 {
			return 0
		}
		c := make([]float64, len(arr))
		copy(c, arr)
		sort.Float64s(c)
		return c[len(c)/2]
	}
	minF := func(arr []float64) float64 {
		if len(arr) == 0 {
			return 0
		}
		m := arr[0]
		for _, v := range arr[1:] {
			if v < m {
				m = v
			}
		}
		return m
	}
	maxF := func(arr []float64) float64 {
		if len(arr) == 0 {
			return 0
		}
		m := arr[0]
		for _, v := range arr[1:] {
			if v > m {
				m = v
			}
		}
		return m
	}

	catStats := map[string]interface{}{}
	for cat, dists := range catDists {
		if len(dists) == 0 {
			catStats[cat] = map[string]interface{}{"count": 0, "avg": 0, "median": 0, "min": 0, "max": 0}
			continue
		}
		sum := 0.0
		for _, v := range dists {
			sum += v
		}
		avg := sum / float64(len(dists))
		catStats[cat] = map[string]interface{}{
			"count":  len(dists),
			"avg":    math.Round(avg*100) / 100,
			"median": math.Round(medianF(dists)*100) / 100,
			"min":    math.Round(minF(dists)*100) / 100,
			"max":    math.Round(maxF(dists)*100) / 100,
		}
	}

	// Distance histogram
	var distHistogram interface{} = []interface{}{}
	allDists := make([]float64, len(filteredHops))
	for i := range filteredHops {
		allDists[i] = filteredHops[i].Dist
	}
	if len(allDists) > 0 {
		hMin, hMax := minF(allDists), maxF(allDists)
		binCount := 25
		binW := (hMax - hMin) / float64(binCount)
		if binW == 0 {
			binW = 1
		}
		bins := make([]int, binCount)
		for _, d := range allDists {
			idx := int(math.Floor((d - hMin) / binW))
			if idx >= binCount {
				idx = binCount - 1
			}
			if idx < 0 {
				idx = 0
			}
			bins[idx]++
		}
		binArr := make([]map[string]interface{}, binCount)
		for i, c := range bins {
			binArr[i] = map[string]interface{}{
				"x":     math.Round((hMin+float64(i)*binW)*10) / 10,
				"w":     math.Round(binW*10) / 10,
				"count": c,
			}
		}
		distHistogram = map[string]interface{}{"bins": binArr, "min": hMin, "max": hMax}
	}

	// Distance over time
	timeKeys := make([]string, 0, len(distByHour))
	for k := range distByHour {
		timeKeys = append(timeKeys, k)
	}
	sort.Strings(timeKeys)
	distOverTime := make([]map[string]interface{}, 0, len(timeKeys))
	for _, hour := range timeKeys {
		dists := distByHour[hour]
		sum := 0.0
		for _, v := range dists {
			sum += v
		}
		distOverTime = append(distOverTime, map[string]interface{}{
			"hour":  hour,
			"avg":   math.Round(sum/float64(len(dists))*100) / 100,
			"count": len(dists),
		})
	}

	// Summary
	summary := map[string]interface{}{
		"totalHops":  len(filteredHops),
		"totalPaths": len(filteredPaths),
		"avgDist":    0.0,
		"maxDist":    0.0,
	}
	if len(allDists) > 0 {
		sum := 0.0
		for _, v := range allDists {
			sum += v
		}
		summary["avgDist"] = math.Round(sum/float64(len(allDists))*100) / 100
		summary["maxDist"] = math.Round(maxF(allDists)*100) / 100
	}

	return map[string]interface{}{
		"summary":       summary,
		"topHops":       topHops,
		"topPaths":      topPaths,
		"catStats":      catStats,
		"distHistogram": distHistogram,
		"distOverTime":  distOverTime,
	}
}
