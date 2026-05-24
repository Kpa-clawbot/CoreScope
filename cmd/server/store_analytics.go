// store_analytics.go — RF, topology, hash-size, hash-collision, multi-byte and node-health analytics.

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// GetAnalyticsRF returns full RF analytics computed from in-memory observations.
func (s *PacketStore) GetAnalyticsRF(region string) map[string]interface{} {
	return s.GetAnalyticsRFWithWindow(region, TimeWindow{})
}

// GetAnalyticsRFWithWindow returns RF analytics bounded by an optional
// time window (issue #842). Zero TimeWindow = all data (backwards compatible).
// For the default query (region="", zero window) the steady-state recomputer
// snapshot (issue #1240) is preferred — atomic load, never blocks.
func (s *PacketStore) GetAnalyticsRFWithWindow(region string, window TimeWindow) map[string]interface{} {
	if region == "" && window.IsZero() {
		s.analyticsRecomputerMu.RLock()
		rc := s.recompRF
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
	cacheKey := region
	if !window.IsZero() {
		cacheKey = region + "|" + window.CacheKey()
	}
	s.cacheMu.Lock()
	if cached, ok := s.rfCache[cacheKey]; ok && time.Now().Before(cached.expiresAt) {
		s.cacheHits++
		s.cacheMu.Unlock()
		return cached.data
	}
	s.cacheMisses++
	s.cacheMu.Unlock()

	result := s.computeAnalyticsRF(region, window)

	s.cacheMu.Lock()
	s.rfCache[cacheKey] = &cachedResult{data: result, expiresAt: time.Now().Add(s.rfCacheTTL)}
	s.cacheMu.Unlock()

	return result
}

func (s *PacketStore) computeAnalyticsRF(region string, window TimeWindow) map[string]interface{} {
	// Resolve region→observer set before taking s.mu so a cache-miss SQL
	// query does not run under the read lock (review item #2).
	var regionObs map[string]bool
	if region != "" {
		regionObs = s.resolveRegionObservers(region)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	ptNames := payloadTypeNames

	// Collect all observations matching the region
	estCap := s.totalObs
	if estCap > 2000000 {
		estCap = 2000000
	}
	snrVals := make([]float64, 0, estCap/2)
	rssiVals := make([]float64, 0, estCap/2)
	packetSizes := make([]int, 0, len(s.packets))
	seenSizeHashes := make(map[string]bool, len(s.packets))
	seenTypeHashes := make(map[string]bool, len(s.packets))
	typeBuckets := map[int]int{}
	hourBuckets := map[string]int{}
	seenHourHash := make(map[string]bool, len(s.packets)) // dedup packets-per-hour by hash+hour
	snrByType := map[string]*struct{ vals []float64 }{}
	sigTime := map[string]*struct {
		snrs  []float64
		count int
	}{}
	scatterAll := make([]struct{ snr, rssi float64 }, 0, estCap/4)
	totalObs := 0
	regionalHashes := make(map[string]bool, len(s.packets))
	var minTimestamp, maxTimestamp string

	if regionObs != nil {
		// Regional: iterate observations from matching observers
		for obsID := range regionObs {
			obsList := s.byObserver[obsID]
			for _, obs := range obsList {
				if !window.Includes(obs.Timestamp) {
					continue
				}
				totalObs++
				tx := s.byTxID[obs.TransmissionID]
				hash := ""
				if tx != nil {
					hash = tx.Hash
				}
				if hash != "" {
					regionalHashes[hash] = true
				}

				ts := obs.Timestamp
				if ts != "" {
					if minTimestamp == "" || ts < minTimestamp {
						minTimestamp = ts
					}
					if ts > maxTimestamp {
						maxTimestamp = ts
					}
				}

				// SNR/RSSI
				if obs.SNR != nil {
					snrVals = append(snrVals, *obs.SNR)
					typeName := "UNK"
					if tx != nil && tx.PayloadType != nil {
						if n, ok := ptNames[*tx.PayloadType]; ok {
							typeName = n
						} else {
							typeName = fmt.Sprintf("UNK(%d)", *tx.PayloadType)
						}
					}
					if snrByType[typeName] == nil {
						snrByType[typeName] = &struct{ vals []float64 }{}
					}
					snrByType[typeName].vals = append(snrByType[typeName].vals, *obs.SNR)

					if obs.RSSI != nil {
						scatterAll = append(scatterAll, struct{ snr, rssi float64 }{*obs.SNR, *obs.RSSI})
					}

					// Signal over time
					if len(ts) >= 13 {
						hr := ts[:13]
						if sigTime[hr] == nil {
							sigTime[hr] = &struct {
								snrs  []float64
								count int
							}{}
						}
						sigTime[hr].snrs = append(sigTime[hr].snrs, *obs.SNR)
						sigTime[hr].count++
					}
				}
				if obs.RSSI != nil {
					rssiVals = append(rssiVals, *obs.RSSI)
				}

				// Packets per hour (unique by hash per hour)
				if len(ts) >= 13 {
					hr := ts[:13]
					hk := hash + "|" + hr
					if hash == "" || !seenHourHash[hk] {
						if hash != "" {
							seenHourHash[hk] = true
						}
						hourBuckets[hr]++
					}
				}

				// Packet sizes (unique by hash)
				if hash != "" && !seenSizeHashes[hash] && tx != nil && tx.RawHex != "" {
					seenSizeHashes[hash] = true
					packetSizes = append(packetSizes, len(tx.RawHex)/2)
				}

				// Payload type distribution (unique by hash)
				if hash != "" && !seenTypeHashes[hash] && tx != nil && tx.PayloadType != nil {
					seenTypeHashes[hash] = true
					typeBuckets[*tx.PayloadType]++
				}
			}
		}
	} else {
		// No region: iterate all transmissions and their observations
		for _, tx := range s.packets {
			// Window filter: skip transmissions outside the requested window.
			// We use tx.FirstSeen as the bounding timestamp; per-obs window
			// filter below handles cases where individual obs timestamps differ.
			if !window.Includes(tx.FirstSeen) {
				continue
			}
			hash := tx.Hash
			if hash != "" {
				regionalHashes[hash] = true
				if !seenSizeHashes[hash] && tx.RawHex != "" {
					seenSizeHashes[hash] = true
					packetSizes = append(packetSizes, len(tx.RawHex)/2)
				}
				if !seenTypeHashes[hash] && tx.PayloadType != nil {
					seenTypeHashes[hash] = true
					typeBuckets[*tx.PayloadType]++
				}
			}

			// Pre-resolve type name once per transmission
			typeName := "UNK"
			if tx.PayloadType != nil {
				if n, ok := ptNames[*tx.PayloadType]; ok {
					typeName = n
				} else {
					typeName = fmt.Sprintf("UNK(%d)", *tx.PayloadType)
				}
			}

			if len(tx.Observations) > 0 {
				for _, obs := range tx.Observations {
					totalObs++
					ts := obs.Timestamp
					if ts != "" {
						if minTimestamp == "" || ts < minTimestamp {
							minTimestamp = ts
						}
						if ts > maxTimestamp {
							maxTimestamp = ts
						}
					}

					if obs.SNR != nil {
						snr := *obs.SNR
						snrVals = append(snrVals, snr)
						entry := snrByType[typeName]
						if entry == nil {
							entry = &struct{ vals []float64 }{}
							snrByType[typeName] = entry
						}
						entry.vals = append(entry.vals, snr)

						if obs.RSSI != nil {
							scatterAll = append(scatterAll, struct{ snr, rssi float64 }{snr, *obs.RSSI})
						}

						if len(ts) >= 13 {
							hr := ts[:13]
							st := sigTime[hr]
							if st == nil {
								st = &struct {
									snrs  []float64
									count int
								}{}
								sigTime[hr] = st
							}
							st.snrs = append(st.snrs, snr)
							st.count++
						}
					}
					if obs.RSSI != nil {
						rssiVals = append(rssiVals, *obs.RSSI)
					}

					if len(ts) >= 13 {
						hr := ts[:13]
						hk := hash + "|" + hr
						if hash == "" || !seenHourHash[hk] {
							if hash != "" {
								seenHourHash[hk] = true
							}
							hourBuckets[hr]++
						}
					}
				}
			} else {
				// Legacy: transmission without observations
				totalObs++
				if tx.SNR != nil {
					snrVals = append(snrVals, *tx.SNR)
				}
				if tx.RSSI != nil {
					rssiVals = append(rssiVals, *tx.RSSI)
				}
				ts := tx.FirstSeen
				if ts != "" {
					if minTimestamp == "" || ts < minTimestamp {
						minTimestamp = ts
					}
					if ts > maxTimestamp {
						maxTimestamp = ts
					}
				}
				if len(ts) >= 13 {
					hourBuckets[ts[:13]]++
				}
			}
		}
	}

	// Stats helpers
	stddevF64 := func(arr []float64, avg float64) float64 {
		if len(arr) == 0 {
			return 0
		}
		sum := 0.0
		for _, v := range arr {
			d := v - avg
			sum += d * d
		}
		return math.Sqrt(sum / float64(len(arr)))
	}
	minF64 := func(arr []float64) float64 {
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
	maxF64 := func(arr []float64) float64 {
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
	minInt := func(arr []int) int {
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
	maxInt := func(arr []int) int {
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

	// Sort snrVals and rssiVals once; reuse sorted order for min/max/median
	// instead of copying+sorting per stat call (#366).
	sort.Float64s(snrVals)
	sort.Float64s(rssiVals)

	snrAvg := 0.0
	if len(snrVals) > 0 {
		sum := 0.0
		for _, v := range snrVals {
			sum += v
		}
		snrAvg = sum / float64(len(snrVals))
	}
	rssiAvg := 0.0
	if len(rssiVals) > 0 {
		sum := 0.0
		for _, v := range rssiVals {
			sum += v
		}
		rssiAvg = sum / float64(len(rssiVals))
	}

	// Packets per hour
	type hourCount struct {
		Hour  string `json:"hour"`
		Count int    `json:"count"`
	}
	hourKeys := make([]string, 0, len(hourBuckets))
	for k := range hourBuckets {
		hourKeys = append(hourKeys, k)
	}
	sort.Strings(hourKeys)
	packetsPerHour := make([]hourCount, len(hourKeys))
	for i, k := range hourKeys {
		packetsPerHour[i] = hourCount{Hour: k, Count: hourBuckets[k]}
	}

	// Payload types
	type ptEntry struct {
		Type  int    `json:"type"`
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	payloadTypes := make([]ptEntry, 0, len(typeBuckets))
	for t, c := range typeBuckets {
		name := ptNames[t]
		if name == "" {
			name = fmt.Sprintf("UNK(%d)", t)
		}
		payloadTypes = append(payloadTypes, ptEntry{Type: t, Name: name, Count: c})
	}
	sort.Slice(payloadTypes, func(i, j int) bool { return payloadTypes[i].Count > payloadTypes[j].Count })

	// SNR by type
	type snrTypeEntry struct {
		Name  string  `json:"name"`
		Count int     `json:"count"`
		Avg   float64 `json:"avg"`
		Min   float64 `json:"min"`
		Max   float64 `json:"max"`
	}
	snrByTypeArr := make([]snrTypeEntry, 0, len(snrByType))
	for name, d := range snrByType {
		sum := 0.0
		for _, v := range d.vals {
			sum += v
		}
		snrByTypeArr = append(snrByTypeArr, snrTypeEntry{
			Name: name, Count: len(d.vals),
			Avg: sum / float64(len(d.vals)),
			Min: minF64(d.vals), Max: maxF64(d.vals),
		})
	}
	sort.Slice(snrByTypeArr, func(i, j int) bool { return snrByTypeArr[i].Count > snrByTypeArr[j].Count })

	// Signal over time
	type sigTimeEntry struct {
		Hour   string  `json:"hour"`
		Count  int     `json:"count"`
		AvgSnr float64 `json:"avgSnr"`
	}
	sigKeys := make([]string, 0, len(sigTime))
	for k := range sigTime {
		sigKeys = append(sigKeys, k)
	}
	sort.Strings(sigKeys)
	signalOverTime := make([]sigTimeEntry, len(sigKeys))
	for i, k := range sigKeys {
		d := sigTime[k]
		sum := 0.0
		for _, v := range d.snrs {
			sum += v
		}
		signalOverTime[i] = sigTimeEntry{Hour: k, Count: d.count, AvgSnr: sum / float64(d.count)}
	}

	// Scatter (downsample to 500)
	type scatterPoint struct {
		SNR  float64 `json:"snr"`
		RSSI float64 `json:"rssi"`
	}
	scatterStep := 1
	if len(scatterAll) > 500 {
		scatterStep = len(scatterAll) / 500
	}
	scatterData := make([]scatterPoint, 0, 500)
	for i, p := range scatterAll {
		if i%scatterStep == 0 {
			scatterData = append(scatterData, scatterPoint{SNR: p.snr, RSSI: p.rssi})
		}
	}

	// Histograms
	buildHistogramF64 := func(values []float64, bins int) map[string]interface{} {
		if len(values) == 0 {
			return map[string]interface{}{"bins": []interface{}{}, "min": 0, "max": 0}
		}
		mn, mx := minF64(values), maxF64(values)
		rng := mx - mn
		if rng == 0 {
			rng = 1
		}
		binWidth := rng / float64(bins)
		counts := make([]int, bins)
		for _, v := range values {
			idx := int((v - mn) / binWidth)
			if idx >= bins {
				idx = bins - 1
			}
			counts[idx]++
		}
		binArr := make([]map[string]interface{}, bins)
		for i, c := range counts {
			binArr[i] = map[string]interface{}{"x": mn + float64(i)*binWidth, "w": binWidth, "count": c}
		}
		return map[string]interface{}{"bins": binArr, "min": mn, "max": mx}
	}
	buildHistogramInt := func(values []int, bins int) map[string]interface{} {
		if len(values) == 0 {
			return map[string]interface{}{"bins": []interface{}{}, "min": 0, "max": 0}
		}
		mn, mx := float64(minInt(values)), float64(maxInt(values))
		rng := mx - mn
		if rng == 0 {
			rng = 1
		}
		binWidth := rng / float64(bins)
		counts := make([]int, bins)
		for _, v := range values {
			idx := int((float64(v) - mn) / binWidth)
			if idx >= bins {
				idx = bins - 1
			}
			counts[idx]++
		}
		binArr := make([]map[string]interface{}, bins)
		for i, c := range counts {
			binArr[i] = map[string]interface{}{"x": mn + float64(i)*binWidth, "w": binWidth, "count": c}
		}
		return map[string]interface{}{"bins": binArr, "min": mn, "max": mx}
	}

	snrHistogram := buildHistogramF64(snrVals, 20)
	rssiHistogram := buildHistogramF64(rssiVals, 20)
	sizeHistogram := buildHistogramInt(packetSizes, 25)

	// Time span from min/max timestamps tracked during first pass
	timeSpanHours := 0.0
	if minTimestamp != "" && maxTimestamp != "" && minTimestamp != maxTimestamp {
		// Parse only 2 timestamps instead of 1.2M
		parseTS := func(ts string) (time.Time, bool) {
			t, err := time.Parse("2006-01-02 15:04:05", ts)
			if err != nil {
				t, err = time.Parse(time.RFC3339, ts)
			}
			if err != nil {
				return time.Time{}, false
			}
			return t, true
		}
		if tMin, ok := parseTS(minTimestamp); ok {
			if tMax, ok := parseTS(maxTimestamp); ok {
				timeSpanHours = float64(tMax.UnixMilli()-tMin.UnixMilli()) / 3600000.0
			}
		}
	}

	// Avg packet size
	avgPktSize := 0
	if len(packetSizes) > 0 {
		sum := 0
		for _, v := range packetSizes {
			sum += v
		}
		avgPktSize = sum / len(packetSizes)
	}

	// snrVals and rssiVals are already sorted — read min/max/median directly.
	snrStats := map[string]interface{}{"min": 0.0, "max": 0.0, "avg": 0.0, "median": 0.0, "stddev": 0.0}
	if len(snrVals) > 0 {
		snrStats = map[string]interface{}{
			"min": snrVals[0], "max": snrVals[len(snrVals)-1],
			"avg": snrAvg, "median": snrVals[len(snrVals)/2],
			"stddev": stddevF64(snrVals, snrAvg),
		}
	}
	rssiStats := map[string]interface{}{"min": 0.0, "max": 0.0, "avg": 0.0, "median": 0.0, "stddev": 0.0}
	if len(rssiVals) > 0 {
		rssiStats = map[string]interface{}{
			"min": rssiVals[0], "max": rssiVals[len(rssiVals)-1],
			"avg": rssiAvg, "median": rssiVals[len(rssiVals)/2],
			"stddev": stddevF64(rssiVals, rssiAvg),
		}
	}

	return map[string]interface{}{
		"totalPackets":       len(snrVals),
		"totalAllPackets":    totalObs,
		"totalTransmissions": len(regionalHashes),
		"snr":                snrStats,
		"rssi":               rssiStats,
		"snrValues":          snrHistogram,
		"rssiValues":         rssiHistogram,
		"packetSizes":        sizeHistogram,
		"minPacketSize":      minInt(packetSizes),
		"maxPacketSize":      maxInt(packetSizes),
		"avgPacketSize":      avgPktSize,
		"packetsPerHour":     packetsPerHour,
		"payloadTypes":       payloadTypes,
		"snrByType":          snrByTypeArr,
		"signalOverTime":     signalOverTime,
		"scatterData":        scatterData,
		"timeSpanHours":      timeSpanHours,
	}
}

func (s *PacketStore) GetAnalyticsTopology(region string) map[string]interface{} {
	return s.GetAnalyticsTopologyWithWindow(region, TimeWindow{})
}

// GetAnalyticsTopologyWithWindow — see issue #842.
// For the default query (region="", zero window) the steady-state recomputer
// snapshot (issue #1240) is preferred — atomic load, never blocks.
func (s *PacketStore) GetAnalyticsTopologyWithWindow(region string, window TimeWindow) map[string]interface{} {
	if region == "" && window.IsZero() {
		s.analyticsRecomputerMu.RLock()
		rc := s.recompTopology
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
	cacheKey := region
	if !window.IsZero() {
		cacheKey = region + "|" + window.CacheKey()
	}
	s.cacheMu.Lock()
	if cached, ok := s.topoCache[cacheKey]; ok && time.Now().Before(cached.expiresAt) {
		s.cacheHits++
		s.cacheMu.Unlock()
		return cached.data
	}
	s.cacheMisses++
	s.cacheMu.Unlock()

	result := s.computeAnalyticsTopology(region, window)

	s.cacheMu.Lock()
	s.topoCache[cacheKey] = &cachedResult{data: result, expiresAt: time.Now().Add(s.rfCacheTTL)}
	s.cacheMu.Unlock()

	return result
}

func (s *PacketStore) computeAnalyticsTopology(region string, window TimeWindow) map[string]interface{} {
	// Resolve region→observer set and refresh the node cache before taking
	// s.mu so neither's cache-miss SQL query runs under the read lock
	// (review item #2). Both use their own mutexes, not s.mu.
	var regionObs map[string]bool
	if region != "" {
		regionObs = s.resolveRegionObservers(region)
	}
	allNodes, pm := s.getCachedNodesAndPM()
	_ = allNodes // only pm is needed for topology

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Materialize the filtered tx slice ONCE — both the context-build pass
	// and the main aggregation pass need the same window+region predicate.
	// Two scans of s.packets re-running identical predicates is wasteful at
	// the 30k+ packet hot-path scale (#1199 item 2). One filter, two passes
	// over the result.
	filteredTxs := make([]*StoreTx, 0, len(s.packets))
	for _, tx := range s.packets {
		if !window.Includes(tx.FirstSeen) {
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
		filteredTxs = append(filteredTxs, tx)
	}

	// Pre-pass: build the full hop-disambiguation context from all in-window
	// txs BEFORE any resolveHop call. The earlier shape — populating
	// contextPubkeys lazily during the main scan and reading it from a
	// closure — was correct only because the current code never calls
	// resolveHop inside the scan loop. A future maintainer who adds such a
	// call inside the loop would silently get partial context AND a
	// stale-cached result for any hop seen before the context grew. Two
	// explicit passes remove the hazard. See #1197 (carmack/adversarial r1).
	var contextPubkeys []string
	{
		seen := make(map[string]struct{}, 64)
		for _, tx := range filteredTxs {
			for _, pk := range buildHopContextPubkeys(tx, pm) {
				if _, ok := seen[pk]; ok {
					continue
				}
				seen[pk] = struct{}{}
				contextPubkeys = append(contextPubkeys, pk)
			}
		}
	}

	hopCache := make(map[string]*nodeInfo)
	graph := s.graph.Load() // hoist out of resolver closure (PR #1208 carmack #1)
	resolveHop := func(hop string) *nodeInfo {
		if cached, ok := hopCache[hop]; ok {
			return cached
		}
		r, _, _ := pm.resolveWithContext(hop, contextPubkeys, graph)
		hopCache[hop] = r
		return r
	}

	hopCounts := map[int]int{}
	var allHopsList []int
	hopSnr := map[int][]float64{}
	hopFreq := map[string]int{}
	pairFreq := map[string]int{}
	observerMap := map[string]string{} // observer_id → observer_name
	perObserver := map[string]map[string]*struct{ minDist, maxDist, count int }{}

	for _, tx := range filteredTxs {
		hops := txGetParsedPath(tx)
		if len(hops) == 0 {
			continue
		}

		n := len(hops)
		hopCounts[n]++
		allHopsList = append(allHopsList, n)
		if tx.SNR != nil {
			hopSnr[n] = append(hopSnr[n], *tx.SNR)
		}
		for _, h := range hops {
			hopFreq[h]++
		}
		for i := 0; i < len(hops)-1; i++ {
			a, b := hops[i], hops[i+1]
			if a > b {
				a, b = b, a
			}
			pairFreq[a+"|"+b]++
		}

		obsID := tx.ObserverID
		if obsID != "" {
			observerMap[obsID] = tx.ObserverName
		}
		if _, ok := perObserver[obsID]; !ok {
			perObserver[obsID] = map[string]*struct{ minDist, maxDist, count int }{}
		}
		for i, h := range hops {
			dist := n - i
			entry := perObserver[obsID][h]
			if entry == nil {
				entry = &struct{ minDist, maxDist, count int }{dist, dist, 0}
				perObserver[obsID][h] = entry
			}
			if dist < entry.minDist {
				entry.minDist = dist
			}
			if dist > entry.maxDist {
				entry.maxDist = dist
			}
			entry.count++
		}
	}

	// Hop distribution
	hopDist := make([]map[string]interface{}, 0)
	for h, c := range hopCounts {
		if h <= 25 {
			hopDist = append(hopDist, map[string]interface{}{"hops": h, "count": c})
		}
	}
	sort.Slice(hopDist, func(i, j int) bool {
		return hopDist[i]["hops"].(int) < hopDist[j]["hops"].(int)
	})

	avgHops := 0.0
	if len(allHopsList) > 0 {
		sum := 0
		for _, v := range allHopsList {
			sum += v
		}
		avgHops = float64(sum) / float64(len(allHopsList))
	}
	medianHops := 0
	if len(allHopsList) > 0 {
		sorted := make([]int, len(allHopsList))
		copy(sorted, allHopsList)
		sort.Ints(sorted)
		medianHops = sorted[len(sorted)/2]
	}
	maxHops := 0
	for _, v := range allHopsList {
		if v > maxHops {
			maxHops = v
		}
	}

	// pmLookup resolves a hop hex string to its prefix-map candidates,
	// applying the same truncation used during map construction.
	pmLookup := func(hop string) []nodeInfo {
		key := strings.ToLower(hop)
		if len(key) > maxPrefixLen {
			key = key[:maxPrefixLen]
		}
		return pm.m[key]
	}

	// --- Dedup pass: merge hop prefixes that resolve unambiguously to the same node ---
	// Only merge when pm.m[hop] has exactly 1 candidate (unique_prefix).
	// Ambiguous short prefixes (efiten's concern: 1-byte collisions) stay separate.
	{
		type dedupInfo struct {
			totalCount int
			longestHop string
		}
		byPubkey := map[string]*dedupInfo{} // pubkey → merged info
		ambiguous := map[string]int{}       // hop → count (kept as-is)
		for h, c := range hopFreq {
			candidates := pmLookup(h)
			if len(candidates) == 1 {
				pk := strings.ToLower(candidates[0].PublicKey)
				if info, ok := byPubkey[pk]; ok {
					info.totalCount += c
					if len(h) > len(info.longestHop) {
						info.longestHop = h
					}
				} else {
					byPubkey[pk] = &dedupInfo{totalCount: c, longestHop: h}
				}
			} else {
				ambiguous[h] = c
			}
		}
		// Rebuild hopFreq
		hopFreq = make(map[string]int, len(byPubkey)+len(ambiguous))
		for _, info := range byPubkey {
			hopFreq[info.longestHop] = info.totalCount
		}
		for h, c := range ambiguous {
			hopFreq[h] = c
		}
	}

	// --- Dedup pass for pairs: merge by resolved pubkey pair ---
	{
		type pairDedupInfo struct {
			totalCount int
			longestA   string
			longestB   string
		}
		byPubkeyPair := map[string]*pairDedupInfo{} // "pkA|pkB" (sorted) → merged info
		ambiguousPairs := map[string]int{}
		for p, c := range pairFreq {
			parts := strings.SplitN(p, "|", 2)
			candA := pmLookup(parts[0])
			candB := pmLookup(parts[1])
			if len(candA) == 1 && len(candB) == 1 {
				pkA := strings.ToLower(candA[0].PublicKey)
				pkB := strings.ToLower(candB[0].PublicKey)
				// Canonicalize by sorted pubkey
				if pkA > pkB {
					pkA, pkB = pkB, pkA
					parts[0], parts[1] = parts[1], parts[0]
				}
				key := pkA + "|" + pkB
				if info, ok := byPubkeyPair[key]; ok {
					info.totalCount += c
					if len(parts[0]) > len(info.longestA) {
						info.longestA = parts[0]
					}
					if len(parts[1]) > len(info.longestB) {
						info.longestB = parts[1]
					}
				} else {
					byPubkeyPair[key] = &pairDedupInfo{totalCount: c, longestA: parts[0], longestB: parts[1]}
				}
			} else {
				ambiguousPairs[p] = c
			}
		}
		// Rebuild pairFreq
		pairFreq = make(map[string]int, len(byPubkeyPair)+len(ambiguousPairs))
		for _, info := range byPubkeyPair {
			a, b := info.longestA, info.longestB
			if a > b {
				a, b = b, a
			}
			pairFreq[a+"|"+b] = info.totalCount
		}
		for p, c := range ambiguousPairs {
			pairFreq[p] = c
		}
	}

	// Top repeaters
	type freqEntry struct {
		hop   string
		count int
	}
	freqList := make([]freqEntry, 0, len(hopFreq))
	for h, c := range hopFreq {
		freqList = append(freqList, freqEntry{h, c})
	}
	sort.Slice(freqList, func(i, j int) bool { return freqList[i].count > freqList[j].count })
	topRepeaters := make([]map[string]interface{}, 0)
	for i, e := range freqList {
		if i >= 20 {
			break
		}
		r := resolveHop(e.hop)
		entry := map[string]interface{}{"hop": e.hop, "count": e.count, "name": nil, "pubkey": nil}
		if r != nil {
			entry["name"] = r.Name
			entry["pubkey"] = r.PublicKey
		}
		topRepeaters = append(topRepeaters, entry)
	}

	// Top pairs
	pairList := make([]freqEntry, 0, len(pairFreq))
	for p, c := range pairFreq {
		pairList = append(pairList, freqEntry{p, c})
	}
	sort.Slice(pairList, func(i, j int) bool { return pairList[i].count > pairList[j].count })
	topPairs := make([]map[string]interface{}, 0)
	for i, e := range pairList {
		if i >= 15 {
			break
		}
		parts := strings.SplitN(e.hop, "|", 2)
		rA := resolveHop(parts[0])
		rB := resolveHop(parts[1])
		entry := map[string]interface{}{
			"hopA": parts[0], "hopB": parts[1], "count": e.count,
			"nameA": nil, "nameB": nil, "pubkeyA": nil, "pubkeyB": nil,
		}
		if rA != nil {
			entry["nameA"] = rA.Name
			entry["pubkeyA"] = rA.PublicKey
		}
		if rB != nil {
			entry["nameB"] = rB.Name
			entry["pubkeyB"] = rB.PublicKey
		}
		topPairs = append(topPairs, entry)
	}

	// Hops vs SNR
	hopsVsSnr := make([]map[string]interface{}, 0)
	for h, snrs := range hopSnr {
		if h > 20 {
			continue
		}
		sum := 0.0
		for _, v := range snrs {
			sum += v
		}
		hopsVsSnr = append(hopsVsSnr, map[string]interface{}{
			"hops": h, "count": len(snrs), "avgSnr": sum / float64(len(snrs)),
		})
	}
	sort.Slice(hopsVsSnr, func(i, j int) bool {
		return hopsVsSnr[i]["hops"].(int) < hopsVsSnr[j]["hops"].(int)
	})

	// Observers list
	observers := make([]map[string]interface{}, 0)
	for id, name := range observerMap {
		n := name
		if n == "" {
			n = id
		}
		observers = append(observers, map[string]interface{}{"id": id, "name": n})
	}

	// Per-observer reachability
	perObserverReach := map[string]interface{}{}
	for obsID, nodes := range perObserver {
		obsName := observerMap[obsID]
		if obsName == "" {
			obsName = obsID
		}
		byDist := map[int][]map[string]interface{}{}
		for hop, data := range nodes {
			d := data.minDist
			if d > 15 {
				continue
			}
			r := resolveHop(hop)
			entry := map[string]interface{}{
				"hop": hop, "name": nil, "pubkey": nil,
				"count": data.count, "distRange": nil,
			}
			if r != nil {
				entry["name"] = r.Name
				entry["pubkey"] = r.PublicKey
			}
			if data.minDist != data.maxDist {
				entry["distRange"] = fmt.Sprintf("%d-%d", data.minDist, data.maxDist)
			}
			byDist[d] = append(byDist[d], entry)
		}
		rings := make([]map[string]interface{}, 0)
		for dist, nodeList := range byDist {
			sort.Slice(nodeList, func(i, j int) bool {
				return nodeList[i]["count"].(int) > nodeList[j]["count"].(int)
			})
			rings = append(rings, map[string]interface{}{"hops": dist, "nodes": nodeList})
		}
		sort.Slice(rings, func(i, j int) bool {
			return rings[i]["hops"].(int) < rings[j]["hops"].(int)
		})
		perObserverReach[obsID] = map[string]interface{}{
			"observer_name": obsName,
			"rings":         rings,
		}
	}

	// Cross-observer: build from perObserver
	crossObserver := map[string][]map[string]interface{}{}
	bestPath := map[string]map[string]interface{}{}
	for obsID, nodes := range perObserver {
		obsName := observerMap[obsID]
		if obsName == "" {
			obsName = obsID
		}
		for hop, data := range nodes {
			crossObserver[hop] = append(crossObserver[hop], map[string]interface{}{
				"observer_id": obsID, "observer_name": obsName,
				"minDist": data.minDist, "count": data.count,
			})
			if bp, ok := bestPath[hop]; !ok || data.minDist < bp["minDist"].(int) {
				bestPath[hop] = map[string]interface{}{
					"minDist": data.minDist, "observer_id": obsID, "observer_name": obsName,
				}
			}
		}
	}

	// Multi-observer nodes
	multiObsNodes := make([]map[string]interface{}, 0)
	for hop, obs := range crossObserver {
		if len(obs) <= 1 {
			continue
		}
		sort.Slice(obs, func(i, j int) bool {
			return obs[i]["minDist"].(int) < obs[j]["minDist"].(int)
		})
		r := resolveHop(hop)
		entry := map[string]interface{}{
			"hop": hop, "name": nil, "pubkey": nil, "observers": obs,
		}
		if r != nil {
			entry["name"] = r.Name
			entry["pubkey"] = r.PublicKey
		}
		multiObsNodes = append(multiObsNodes, entry)
	}
	sort.Slice(multiObsNodes, func(i, j int) bool {
		return len(multiObsNodes[i]["observers"].([]map[string]interface{})) >
			len(multiObsNodes[j]["observers"].([]map[string]interface{}))
	})
	if len(multiObsNodes) > 50 {
		multiObsNodes = multiObsNodes[:50]
	}

	// Best path list
	bestPathList := make([]map[string]interface{}, 0, len(bestPath))
	for hop, data := range bestPath {
		r := resolveHop(hop)
		entry := map[string]interface{}{
			"hop": hop, "name": nil, "pubkey": nil,
			"minDist": data["minDist"], "observer_id": data["observer_id"],
			"observer_name": data["observer_name"],
		}
		if r != nil {
			entry["name"] = r.Name
			entry["pubkey"] = r.PublicKey
		}
		bestPathList = append(bestPathList, entry)
	}
	sort.Slice(bestPathList, func(i, j int) bool {
		return bestPathList[i]["minDist"].(int) < bestPathList[j]["minDist"].(int)
	})
	if len(bestPathList) > 50 {
		bestPathList = bestPathList[:50]
	}

	// Use DB 7-day active node count (matches /api/stats totalNodes)
	uniqueNodes := 0
	if s.db != nil {
		if stats, err := s.db.GetStats(); err == nil {
			uniqueNodes = stats.TotalNodes
		}
	}

	return map[string]interface{}{
		"uniqueNodes":      uniqueNodes,
		"avgHops":          avgHops,
		"medianHops":       medianHops,
		"maxHops":          maxHops,
		"hopDistribution":  hopDist,
		"topRepeaters":     topRepeaters,
		"topPairs":         topPairs,
		"hopsVsSnr":        hopsVsSnr,
		"observers":        observers,
		"perObserverReach": perObserverReach,
		"multiObsNodes":    multiObsNodes,
		"bestPathList":     bestPathList,
	}
}

// GetAnalyticsHashSizes returns hash-size analytics. For the default query
// (region="") the steady-state recomputer snapshot (issue #1240) is preferred.
func (s *PacketStore) GetAnalyticsHashSizes(region string) map[string]interface{} {
	if region == "" {
		s.analyticsRecomputerMu.RLock()
		rc := s.recompHashSizes
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
	if cached, ok := s.hashCache[region]; ok && time.Now().Before(cached.expiresAt) {
		s.cacheHits++
		s.cacheMu.Unlock()
		return cached.data
	}
	s.cacheMisses++
	s.cacheMu.Unlock()

	result := s.computeAnalyticsHashSizes(region)

	// Multi-byte capability is a NODE property (derived from each node's own
	// adverts), not a function of the observing region. The region filter
	// should only control which nodes appear in the analytics list, not the
	// evidence used to classify their capability. Always compute capability
	// against the GLOBAL advert dataset so a region-filtered view doesn't
	// downgrade every adopter to "unknown" just because the confirming
	// advert was heard by an out-of-region observer (#bug: meshat.se/JKG
	// showed 14 unknown vs 0 unknown unfiltered).
	globalAdopterHS := make(map[string]int)
	if region == "" {
		if mbNodes, ok := result["multiByteNodes"].([]map[string]interface{}); ok {
			for _, n := range mbNodes {
				pk, _ := n["pubkey"].(string)
				hs, _ := n["hashSize"].(int)
				if pk != "" && hs >= 2 {
					globalAdopterHS[pk] = hs
				}
			}
		}
	} else {
		// Pull the global multiByteNodes set without the region filter.
		// Use a separate compute call (not the cached path) to avoid
		// recursive locking on hashCache and to keep this side-effect free.
		globalRes := s.computeAnalyticsHashSizes("")
		if mbNodes, ok := globalRes["multiByteNodes"].([]map[string]interface{}); ok {
			for _, n := range mbNodes {
				pk, _ := n["pubkey"].(string)
				hs, _ := n["hashSize"].(int)
				if pk != "" && hs >= 2 {
					globalAdopterHS[pk] = hs
				}
			}
		}
	}
	result["multiByteCapability"] = s.computeMultiByteCapability(globalAdopterHS)

	s.cacheMu.Lock()
	s.hashCache[region] = &cachedResult{data: result, expiresAt: time.Now().Add(s.rfCacheTTL)}
	s.cacheMu.Unlock()

	return result
}

func (s *PacketStore) computeAnalyticsHashSizes(region string) map[string]interface{} {
	// Resolve region→observer set before taking s.mu so a cache-miss SQL
	// query does not run under the read lock (review item #2).
	var regionObs map[string]bool
	if region != "" {
		regionObs = s.resolveRegionObservers(region)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// #804: derive each node's HOME region from zero-hop direct adverts (the
	// most authoritative location signal — those packets cannot have been
	// relayed). When non-empty, multi-byte node attribution prefers this
	// over observer-region. Falls back to observer-region when unknown.
	nodeHomeRegion := s.computeNodeHomeRegions()
	attributionMethod := "observer"
	if region != "" && len(nodeHomeRegion) > 0 {
		attributionMethod = "repeater"
	}

	allNodes, pm := s.getCachedNodesAndPM()

	// Build pubkey→role map for filtering by node type.
	nodeRoleByPK := make(map[string]string, len(allNodes))
	for _, n := range allNodes {
		nodeRoleByPK[n.PublicKey] = n.Role
	}

	distribution := map[string]int{"1": 0, "2": 0, "3": 0}
	byHour := map[string]map[string]int{}
	byNode := map[string]map[string]interface{}{}
	uniqueHops := map[string]map[string]interface{}{}
	total := 0

	for _, tx := range s.packets {
		if tx.RawHex == "" {
			continue
		}

		// Parse header and path byte
		if len(tx.RawHex) < 4 {
			continue
		}
		header, err := strconv.ParseUint(tx.RawHex[:2], 16, 8)
		if err != nil {
			continue
		}
		routeType := header & 0x03
		pathByteIdx := 1
		if routeType == 0 || routeType == 3 {
			pathByteIdx = 5
		}
		hexStart := pathByteIdx * 2
		hexEnd := hexStart + 2
		if hexEnd > len(tx.RawHex) {
			continue
		}
		actualPathByte, err := strconv.ParseUint(tx.RawHex[hexStart:hexEnd], 16, 8)
		if err != nil {
			continue
		}

		hashSize := int((actualPathByte>>6)&0x3) + 1
		if hashSize > 3 {
			continue
		}

		// #804: pre-extract originator pubkey for ADVERT packets so we can
		// (a) relax observer-region filter when the originator's HOME region
		//     matches the requested region (a flood relay heard outside the
		//     home region must still attribute to the home), and
		// (b) reuse the parsed values below without re-parsing.
		var advertPK, advertName string
		var advertParsed bool
		if tx.PayloadType != nil && *tx.PayloadType == PayloadADVERT && tx.DecodedJSON != "" {
			var d map[string]interface{}
			if json.Unmarshal([]byte(tx.DecodedJSON), &d) == nil {
				if v, ok := d["pubKey"].(string); ok {
					advertPK = v
				} else if v, ok := d["public_key"].(string); ok {
					advertPK = v
				}
				if n, ok := d["name"].(string); ok {
					advertName = n
				}
				advertParsed = advertPK != ""
			}
		}

		if regionObs != nil {
			match := false
			for _, obs := range tx.Observations {
				if regionObs[obs.ObserverID] {
					match = true
					break
				}
			}
			// #804: allow ADVERTs from a node whose HOME region matches the
			// requested region even if no observer in that region heard this
			// particular packet (e.g. flood relay heard only by an out-of-
			// region observer). Conservative: only ADVERTs (the source is
			// known by pubkey) and only when home is established.
			if !match && advertParsed {
				if home, ok := nodeHomeRegion[advertPK]; ok && iataMatchesRegion(home, region) {
					match = true
				}
			}
			if !match {
				continue
			}
		}

		// Track originator from advert packets (including zero-hop adverts,
		// keyed by pubKey so same-name nodes don't merge).
		if advertParsed {
			pk := advertPK
			name := advertName
			if name == "" {
				if len(pk) >= 8 {
					name = pk[:8]
				} else {
					name = pk
				}
			}
			// Skip zero-hop direct adverts for hash_size — the
			// path byte is locally generated and unreliable.
			// Still count the packet and update lastSeen.
			isZeroHop := (routeType == uint64(RouteDirect) || routeType == uint64(RouteTransportDirect)) && (actualPathByte&0x3F) == 0
			if byNode[pk] == nil {
				role := nodeRoleByPK[pk] // empty if unknown
				initHS := hashSize
				if isZeroHop {
					initHS = 0
				}
				byNode[pk] = map[string]interface{}{
					"hashSize": initHS, "packets": 0,
					"lastSeen": tx.FirstSeen, "name": name,
					"role": role,
				}
			}
			byNode[pk]["packets"] = byNode[pk]["packets"].(int) + 1
			if !isZeroHop {
				byNode[pk]["hashSize"] = hashSize
			}
			byNode[pk]["lastSeen"] = tx.FirstSeen
		}

		// Distribution/hourly/uniqueHops only for packets with relay hops
		hops := txGetParsedPath(tx)
		if len(hops) == 0 {
			continue
		}
		total++

		sizeKey := strconv.Itoa(hashSize)
		distribution[sizeKey]++

		// Hourly buckets
		if len(tx.FirstSeen) >= 13 {
			hour := tx.FirstSeen[:13]
			if byHour[hour] == nil {
				byHour[hour] = map[string]int{"1": 0, "2": 0, "3": 0}
			}
			byHour[hour][sizeKey]++
		}

		// Track unique hops with their sizes
		for _, hop := range hops {
			if uniqueHops[hop] == nil {
				hopLower := strings.ToLower(hop)
				candidates := pm.m[hopLower]
				var matchName, matchPk interface{}
				if len(candidates) > 0 {
					matchName = candidates[0].Name
					matchPk = candidates[0].PublicKey
				}
				uniqueHops[hop] = map[string]interface{}{
					"size": (len(hop) + 1) / 2, "count": 0,
					"name": matchName, "pubkey": matchPk,
				}
			}
			uniqueHops[hop]["count"] = uniqueHops[hop]["count"].(int) + 1
		}
	}

	// Sort hourly data
	hourKeys := make([]string, 0, len(byHour))
	for k := range byHour {
		hourKeys = append(hourKeys, k)
	}
	sort.Strings(hourKeys)
	hourly := make([]map[string]interface{}, 0, len(hourKeys))
	for _, hour := range hourKeys {
		sizes := byHour[hour]
		hourly = append(hourly, map[string]interface{}{
			"hour": hour, "1": sizes["1"], "2": sizes["2"], "3": sizes["3"],
		})
	}

	// Top hops by frequency
	type hopEntry struct {
		hex  string
		data map[string]interface{}
	}
	hopList := make([]hopEntry, 0, len(uniqueHops))
	for hex, data := range uniqueHops {
		hopList = append(hopList, hopEntry{hex, data})
	}
	sort.Slice(hopList, func(i, j int) bool {
		return hopList[i].data["count"].(int) > hopList[j].data["count"].(int)
	})
	topHops := make([]map[string]interface{}, 0)
	for i, e := range hopList {
		if i >= 50 {
			break
		}
		topHops = append(topHops, map[string]interface{}{
			"hex": e.hex, "size": e.data["size"], "count": e.data["count"],
			"name": e.data["name"], "pubkey": e.data["pubkey"],
		})
	}

	// Multi-byte nodes
	multiByteNodes := make([]map[string]interface{}, 0)
	for pk, data := range byNode {
		// #804: when a region filter is active, prefer the repeater's HOME
		// region over the observer that happened to relay it. Falls back to
		// the (already-applied) observer-region filter when the node's home
		// region is unknown.
		if region != "" {
			if home, ok := nodeHomeRegion[pk]; ok && !iataMatchesRegion(home, region) {
				continue
			}
		}
		if data["hashSize"].(int) > 1 {
			multiByteNodes = append(multiByteNodes, map[string]interface{}{
				"name": data["name"], "hashSize": data["hashSize"],
				"packets": data["packets"], "lastSeen": data["lastSeen"],
				"pubkey": pk, "role": data["role"],
			})
		}
	}
	sort.Slice(multiByteNodes, func(i, j int) bool {
		return multiByteNodes[i]["packets"].(int) > multiByteNodes[j]["packets"].(int)
	})

	// Distribution by repeaters: count unique REPEATER nodes per hash size
	distributionByRepeaters := map[string]int{"1": 0, "2": 0, "3": 0}
	for pk, data := range byNode {
		role, _ := data["role"].(string)
		if !strings.Contains(strings.ToLower(role), "repeater") {
			continue
		}
		// #804: same repeater-region preference as multiByteNodes.
		if region != "" {
			if home, ok := nodeHomeRegion[pk]; ok && !iataMatchesRegion(home, region) {
				continue
			}
		}
		hs := data["hashSize"].(int)
		key := strconv.Itoa(hs)
		distributionByRepeaters[key]++
	}

	return map[string]interface{}{
		"total":                   total,
		"distribution":            distribution,
		"distributionByRepeaters": distributionByRepeaters,
		"hourly":                  hourly,
		"topHops":                 topHops,
		"multiByteNodes":          multiByteNodes,
		"attributionMethod":       attributionMethod,
	}
}

// hashSizeNodeInfo holds per-node hash size tracking data.
type hashSizeNodeInfo struct {
	HashSize     int
	AllSizes     map[int]bool
	Seq          []int
	Inconsistent bool
}

// GetAnalyticsHashCollisions returns pre-computed hash collision analysis.
// This moves the O(n²) distance computation from the frontend to the server.
// For the default query (region="") the steady-state recomputer snapshot
// (issue #1240) is preferred — atomic load, never blocks.
func (s *PacketStore) GetAnalyticsHashCollisions(region string) map[string]interface{} {
	if region == "" {
		s.analyticsRecomputerMu.RLock()
		rc := s.recompHashCollisions
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
	if cached, ok := s.collisionCache[region]; ok && time.Now().Before(cached.expiresAt) {
		s.cacheHits++
		s.cacheMu.Unlock()
		return cached.data
	}
	s.cacheMisses++
	s.cacheMu.Unlock()

	result := s.computeHashCollisions(region)

	s.cacheMu.Lock()
	s.collisionCache[region] = &cachedResult{data: result, expiresAt: time.Now().Add(s.collisionCacheTTL)}
	s.cacheMu.Unlock()

	return result
}

// collisionNode is a lightweight node representation for collision analysis.
type collisionNode struct {
	PublicKey            string  `json:"public_key"`
	Name                 string  `json:"name"`
	Role                 string  `json:"role"`
	Lat                  float64 `json:"lat"`
	Lon                  float64 `json:"lon"`
	HashSize             int     `json:"hash_size"`
	HashSizeInconsistent bool    `json:"hash_size_inconsistent"`
	HashSizesSeen        []int   `json:"hash_sizes_seen,omitempty"`
}

// collisionEntry represents a prefix collision with pre-computed distances.
type collisionEntry struct {
	Prefix         string          `json:"prefix"`
	ByteSize       int             `json:"byte_size"`
	Appearances    int             `json:"appearances"`
	Nodes          []collisionNode `json:"nodes"`
	MaxDistKm      float64         `json:"max_dist_km"`
	Classification string          `json:"classification"`
	WithCoords     int             `json:"with_coords"`
}

// prefixCellInfo holds per-prefix-cell data for the matrix view.
type prefixCellInfo struct {
	Nodes []collisionNode `json:"nodes"`
}

// twoByteCellInfo holds per-first-byte-group data for 2-byte matrix.
type twoByteCellInfo struct {
	GroupNodes     []collisionNode            `json:"group_nodes"`
	TwoByteMap     map[string][]collisionNode `json:"two_byte_map"`
	MaxCollision   int                        `json:"max_collision"`
	CollisionCount int                        `json:"collision_count"`
}

func (s *PacketStore) computeHashCollisions(region string) map[string]interface{} {
	// Get all nodes from DB
	nodes := s.getAllNodes()
	hashInfo := s.GetNodeHashSizeInfo()

	// If region is specified, filter to only nodes seen by regional observers
	if region != "" {
		regionObs := s.resolveRegionObservers(region)
		if regionObs != nil {
			s.mu.RLock()
			regionNodePKs := make(map[string]bool)
			for _, tx := range s.packets {
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
				// Collect node public keys from advert packets
				if tx.DecodedJSON != "" {
					var d map[string]interface{}
					if json.Unmarshal([]byte(tx.DecodedJSON), &d) == nil {
						if pk, ok := d["pubKey"].(string); ok && pk != "" {
							regionNodePKs[pk] = true
						}
						if pk, ok := d["public_key"].(string); ok && pk != "" {
							regionNodePKs[pk] = true
						}
					}
				}
				// Include observers themselves as nodes in the region
				for _, obs := range tx.Observations {
					if obs.ObserverID != "" {
						regionNodePKs[obs.ObserverID] = true
					}
				}
			}
			s.mu.RUnlock()

			// Filter nodes to only those seen in the region
			filtered := make([]nodeInfo, 0, len(regionNodePKs))
			for _, n := range nodes {
				if regionNodePKs[n.PublicKey] {
					filtered = append(filtered, n)
				}
			}
			nodes = filtered
		}
	}

	// Build collision nodes with hash info
	var allCNodes []collisionNode
	for _, n := range nodes {
		cn := collisionNode{
			PublicKey: n.PublicKey,
			Name:      n.Name,
			Role:      n.Role,
			Lat:       n.Lat,
			Lon:       n.Lon,
		}
		if info, ok := hashInfo[n.PublicKey]; ok && info != nil {
			cn.HashSize = info.HashSize
			cn.HashSizeInconsistent = info.Inconsistent
			if len(info.AllSizes) > 1 {
				sizes := make([]int, 0, len(info.AllSizes))
				for sz := range info.AllSizes {
					sizes = append(sizes, sz)
				}
				sort.Ints(sizes)
				cn.HashSizesSeen = sizes
			}
		}
		allCNodes = append(allCNodes, cn)
	}

	// Inconsistent nodes
	var inconsistentNodes []collisionNode
	for _, cn := range allCNodes {
		if cn.HashSizeInconsistent && (cn.Role == "repeater" || cn.Role == "room_server") {
			inconsistentNodes = append(inconsistentNodes, cn)
		}
	}
	if inconsistentNodes == nil {
		inconsistentNodes = make([]collisionNode, 0)
	}

	// Compute collisions for each byte size (1, 2, 3)
	collisionsBySize := make(map[string]interface{})
	for _, bytes := range []int{1, 2, 3} {
		// Filter nodes relevant to this byte size.
		// - Exclude hash_size==0 nodes: no adverts seen, so actual hash
		//   size is unknown. Including them in every bucket inflates
		//   collision counts.
		// - Exclude companions: they are mobile/temporary and don't form
		//   the mesh backbone, so collisions with them aren't meaningful.
		// (Fixes #441)
		var nodesForByte []collisionNode
		for _, cn := range allCNodes {
			if cn.HashSize == bytes && cn.Role == "repeater" {
				nodesForByte = append(nodesForByte, cn)
			}
		}

		// Build prefix map
		prefixMap := make(map[string][]collisionNode)
		for _, cn := range nodesForByte {
			if len(cn.PublicKey) < bytes*2 {
				continue
			}
			prefix := strings.ToUpper(cn.PublicKey[:bytes*2])
			prefixMap[prefix] = append(prefixMap[prefix], cn)
		}

		// Compute collisions with pairwise distances
		var collisions []collisionEntry
		for prefix, pnodes := range prefixMap {
			if len(pnodes) <= 1 {
				continue
			}
			// Pairwise distance
			var withCoords []collisionNode
			for _, cn := range pnodes {
				if cn.Lat != 0 || cn.Lon != 0 {
					withCoords = append(withCoords, cn)
				}
			}
			var maxDistKm float64
			classification := "unknown"
			if len(withCoords) >= 2 {
				for i := 0; i < len(withCoords); i++ {
					for j := i + 1; j < len(withCoords); j++ {
						d := haversineKm(withCoords[i].Lat, withCoords[i].Lon, withCoords[j].Lat, withCoords[j].Lon)
						if d > maxDistKm {
							maxDistKm = d
						}
					}
				}
				if maxDistKm < 50 {
					classification = "local"
				} else if maxDistKm < 200 {
					classification = "regional"
				} else {
					classification = "distant"
				}
			} else {
				classification = "incomplete"
			}
			collisions = append(collisions, collisionEntry{
				Prefix:         prefix,
				ByteSize:       bytes,
				Appearances:    len(pnodes),
				Nodes:          pnodes,
				MaxDistKm:      maxDistKm,
				Classification: classification,
				WithCoords:     len(withCoords),
			})
		}
		if collisions == nil {
			collisions = make([]collisionEntry, 0)
		}

		// Sort: local first, then regional, distant, incomplete
		classOrder := map[string]int{"local": 0, "regional": 1, "distant": 2, "incomplete": 3, "unknown": 4}
		sort.Slice(collisions, func(i, j int) bool {
			oi, oj := classOrder[collisions[i].Classification], classOrder[collisions[j].Classification]
			if oi != oj {
				return oi < oj
			}
			return collisions[i].Appearances > collisions[j].Appearances
		})

		// Stats
		nodeCount := len(nodesForByte)
		usingThisSize := 0
		for _, cn := range allCNodes {
			if cn.HashSize == bytes {
				usingThisSize++
			}
		}
		uniquePrefixes := len(prefixMap)
		collisionCount := len(collisions)
		var spaceSize int
		switch bytes {
		case 1:
			spaceSize = 256
		case 2:
			spaceSize = 65536
		case 3:
			spaceSize = 16777216
		}
		pctUsed := 0.0
		if spaceSize > 0 {
			pctUsed = float64(uniquePrefixes) / float64(spaceSize) * 100
		}

		// For 1-byte and 2-byte, include the full prefix cell data for matrix rendering
		var oneByteCells map[string][]collisionNode
		var twoByteCells map[string]*twoByteCellInfo
		if bytes == 1 {
			oneByteCells = make(map[string][]collisionNode)
			for i := 0; i < 256; i++ {
				hex := strings.ToUpper(fmt.Sprintf("%02x", i))
				oneByteCells[hex] = prefixMap[hex]
				if oneByteCells[hex] == nil {
					oneByteCells[hex] = make([]collisionNode, 0)
				}
			}
		} else if bytes == 2 {
			twoByteCells = make(map[string]*twoByteCellInfo)
			for i := 0; i < 256; i++ {
				hex := strings.ToUpper(fmt.Sprintf("%02x", i))
				cell := &twoByteCellInfo{
					GroupNodes: make([]collisionNode, 0),
					TwoByteMap: make(map[string][]collisionNode),
				}
				twoByteCells[hex] = cell
			}
			for _, cn := range nodesForByte {
				if len(cn.PublicKey) < 4 {
					continue
				}
				firstHex := strings.ToUpper(cn.PublicKey[:2])
				twoHex := strings.ToUpper(cn.PublicKey[:4])
				cell := twoByteCells[firstHex]
				if cell == nil {
					continue
				}
				cell.GroupNodes = append(cell.GroupNodes, cn)
				cell.TwoByteMap[twoHex] = append(cell.TwoByteMap[twoHex], cn)
			}
			for _, cell := range twoByteCells {
				for _, ns := range cell.TwoByteMap {
					if len(ns) > 1 {
						cell.CollisionCount++
						if len(ns) > cell.MaxCollision {
							cell.MaxCollision = len(ns)
						}
					}
				}
			}
		}

		sizeData := map[string]interface{}{
			"stats": map[string]interface{}{
				"total_nodes":     len(allCNodes),
				"nodes_for_byte":  nodeCount,
				"using_this_size": usingThisSize,
				"unique_prefixes": uniquePrefixes,
				"collision_count": collisionCount,
				"space_size":      spaceSize,
				"pct_used":        pctUsed,
			},
			"collisions": collisions,
		}
		if oneByteCells != nil {
			sizeData["one_byte_cells"] = oneByteCells
		}
		if twoByteCells != nil {
			sizeData["two_byte_cells"] = twoByteCells
		}
		collisionsBySize[strconv.Itoa(bytes)] = sizeData
	}

	return map[string]interface{}{
		"inconsistent_nodes": inconsistentNodes,
		"by_size":            collisionsBySize,
	}
}

// computeHashSizeInfoFn is the function called by GetNodeHashSizeInfo to
// do the expensive scan. Overridable in tests via variable swap.
var computeHashSizeInfoFn = (*PacketStore).computeNodeHashSizeInfo

// GetNodeHashSizeInfo returns cached per-node hash size data, recomputing at most every 30s.
// A singleflight pattern prevents thundering-herd cache misses: if a recompute is already
// in progress, concurrent callers wait for it rather than launching their own scans.
func (s *PacketStore) GetNodeHashSizeInfo() map[string]*hashSizeNodeInfo {
	const ttl = 30 * time.Second

	// Fast path: cache is warm.
	s.hashSizeInfoMu.Lock()
	if s.hashSizeInfoCache != nil && time.Since(s.hashSizeInfoAt) < ttl {
		cached := s.hashSizeInfoCache
		s.hashSizeInfoMu.Unlock()
		return cached
	}

	// Singleflight: if a recompute is already in progress, wait for it.
	if s.hashSizeInFlt != nil {
		ch := s.hashSizeInFlt
		s.hashSizeInfoMu.Unlock()
		<-ch
		// Re-acquire to read cache. Return it if still valid.
		// If the leader panicked (leaving cache nil), fall through with lock
		// still held to become the new leader.
		s.hashSizeInfoMu.Lock()
		cached := s.hashSizeInfoCache
		if cached != nil {
			s.hashSizeInfoMu.Unlock()
			return cached
		}
		// Cache is nil; lock still held — fall through to leader path.
	}

	// We are the leader: publish the channel before releasing the lock.
	done := make(chan struct{})
	s.hashSizeInFlt = done
	s.hashSizeInfoMu.Unlock()

	// Recompute outside the lock (this is the expensive O(N) scan).
	var result map[string]*hashSizeNodeInfo
	defer func() {
		s.hashSizeInfoMu.Lock()
		if result != nil {
			s.hashSizeInfoCache = result
			s.hashSizeInfoAt = time.Now()
		}
		s.hashSizeInFlt = nil
		s.hashSizeInfoMu.Unlock()
		close(done)
	}()
	result = computeHashSizeInfoFn(s)
	return result
}

// computeNodeHashSizeInfo scans advert packets to compute per-node hash size data.
// Only adverts from the last 7 days are considered so that legitimate config
// changes during testing don't create permanent false positives.
func (s *PacketStore) computeNodeHashSizeInfo() map[string]*hashSizeNodeInfo {
	// Snapshot the advert list under a brief read lock so the CPU-bound parse
	// pass runs without holding s.mu (same pattern as computeRepeaterRelayInfoMap).
	s.mu.RLock()
	adverts := s.byPayloadType[4]
	snapAdverts := make([]*StoreTx, len(adverts))
	copy(snapAdverts, adverts)
	s.mu.RUnlock()

	info := make(map[string]*hashSizeNodeInfo)

	cutoff := time.Now().UTC().Add(-7 * 24 * time.Hour).Format("2006-01-02T15:04:05.000Z")

	for _, tx := range snapAdverts {
		// Skip adverts older than 7 days to avoid false positives from
		// historical config changes during testing.
		if tx.FirstSeen != "" && tx.FirstSeen < cutoff {
			continue
		}
		if tx.RawHex == "" || tx.DecodedJSON == "" {
			continue
		}
		if len(tx.RawHex) < 4 {
			continue
		}
		header, err := strconv.ParseUint(tx.RawHex[:2], 16, 8)
		if err != nil {
			continue
		}
		routeType := int(header & 0x03)
		// Transport routes (0, 3) have 4 transport code bytes before the path
		// byte, so the path byte is at offset 5 instead of 1.
		pbOffset := 1
		if routeType == RouteTransportFlood || routeType == RouteTransportDirect {
			pbOffset = 5
		}
		if len(tx.RawHex) < (pbOffset+1)*2 {
			continue
		}
		pathByte, err := strconv.ParseUint(tx.RawHex[pbOffset*2:pbOffset*2+2], 16, 8)
		if err != nil {
			continue
		}
		// Direct zero-hop adverts (route types 2 and 3) use path byte 0x00
		// locally and can misreport multibyte hash mode as 1-byte.
		if (routeType == RouteDirect || routeType == RouteTransportDirect) && (pathByte&0x3F) == 0 {
			continue
		}
		hs := int((pathByte>>6)&0x3) + 1

		var d map[string]interface{}
		if json.Unmarshal([]byte(tx.DecodedJSON), &d) != nil {
			continue
		}
		pk := ""
		if v, ok := d["pubKey"].(string); ok {
			pk = v
		} else if v, ok := d["public_key"].(string); ok {
			pk = v
		}
		if pk == "" {
			continue
		}

		ni := info[pk]
		if ni == nil {
			ni = &hashSizeNodeInfo{AllSizes: make(map[int]bool)}
			info[pk] = ni
		}
		ni.AllSizes[hs] = true
		ni.Seq = append(ni.Seq, hs)
	}

	// Post-process: use latest advert hash size and compute flip-flop flag.
	// The most recent advert reflects the node's current hash size
	// configuration. The upstream firmware bug causing stale path bytes in
	// flood adverts was fixed (meshcore-dev/MeshCore#2154).
	for _, ni := range info {
		// Use the most recent advert's hash size (last in chronological order).
		ni.HashSize = ni.Seq[len(ni.Seq)-1]

		// Flip-flop (inconsistent) flag: need >= 3 observations,
		// >= 2 unique sizes, and >= 2 transitions in the sequence.
		if len(ni.Seq) < 3 || len(ni.AllSizes) < 2 {
			continue
		}
		transitions := 0
		for i := 1; i < len(ni.Seq); i++ {
			if ni.Seq[i] != ni.Seq[i-1] {
				transitions++
			}
		}
		ni.Inconsistent = transitions >= 2
	}

	return info
}

// EnrichNodeWithHashSize populates hash_size, hash_size_inconsistent, and
// hash_sizes_seen on a node map using precomputed hash size info.
func EnrichNodeWithHashSize(node map[string]interface{}, info *hashSizeNodeInfo) {
	if info == nil {
		return
	}
	node["hash_size"] = info.HashSize
	node["hash_size_inconsistent"] = info.Inconsistent
	if len(info.AllSizes) > 1 {
		sizes := make([]int, 0, len(info.AllSizes))
		for s := range info.AllSizes {
			sizes = append(sizes, s)
		}
		sort.Ints(sizes)
		node["hash_sizes_seen"] = sizes
	}
}

// EnrichNodeWithMultiByte adds multi-byte capability fields to a node map.
func EnrichNodeWithMultiByte(node map[string]interface{}, entry *MultiByteCapEntry) {
	if entry == nil {
		return
	}
	node["multi_byte_status"] = entry.Status
	node["multi_byte_evidence"] = entry.Evidence
	node["multi_byte_max_hash_size"] = entry.MaxHashSize
}

// GetMultiByteCapMap returns a cached pubkey → MultiByteCapEntry map.
// Uses singleflight to coalesce concurrent callers and a 30s TTL.
func (s *PacketStore) GetMultiByteCapMap() map[string]*MultiByteCapEntry {
	const ttl = 30 * time.Second

	// Fast path: cache is warm.
	s.hashSizeInfoMu.Lock()
	if s.multiByteCapCache != nil && time.Since(s.multiByteCapAt) < ttl {
		cached := s.multiByteCapCache
		s.hashSizeInfoMu.Unlock()
		return cached
	}

	// Singleflight: if a recompute is already in progress, wait for it.
	if s.multiByteCapInFlt != nil {
		ch := s.multiByteCapInFlt
		s.hashSizeInfoMu.Unlock()
		<-ch
		// Re-acquire to read cache. Return it if still valid.
		// If the leader panicked (leaving cache nil), fall through with lock
		// still held to become the new leader.
		s.hashSizeInfoMu.Lock()
		cached := s.multiByteCapCache
		if cached != nil {
			s.hashSizeInfoMu.Unlock()
			return cached
		}
		// Cache is nil; lock still held — fall through to leader path.
	}

	// We are the leader: publish the channel before releasing the lock.
	done := make(chan struct{})
	s.multiByteCapInFlt = done
	s.hashSizeInfoMu.Unlock()

	var result map[string]*MultiByteCapEntry
	defer func() {
		s.hashSizeInfoMu.Lock()
		if result != nil {
			s.multiByteCapCache = result
			s.multiByteCapAt = time.Now()
		}
		s.multiByteCapInFlt = nil
		s.hashSizeInfoMu.Unlock()
		close(done)
	}()

	// Get adopter hash sizes from analytics for cross-referencing
	analyticsData := s.GetAnalyticsHashSizes("")
	adopterSizes := make(map[string]int)
	if nodes, ok := analyticsData["nodes"].(map[string]map[string]interface{}); ok {
		for pk, data := range nodes {
			if hs, ok := data["hashSize"].(int); ok {
				adopterSizes[pk] = hs
			}
		}
	}

	caps := s.computeMultiByteCapability(adopterSizes)
	result = make(map[string]*MultiByteCapEntry, len(caps))
	for i := range caps {
		result[caps[i].PublicKey] = &caps[i]
	}
	return result
}

// MultiByteCapEntry represents a node's inferred multi-byte capability.
type MultiByteCapEntry struct {
	PublicKey   string `json:"pubkey"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	Status      string `json:"status"`   // "confirmed", "suspected", "unknown"
	Evidence    string `json:"evidence"` // "advert", "path", ""
	MaxHashSize int    `json:"maxHashSize"`
	LastSeen    string `json:"lastSeen"`
}

// computeMultiByteCapability determines multi-byte capability for each
// node (repeaters, companions, rooms, sensors) using two methods:
//
//  1. Confirmed: the node has advertised with hash_size >= 2 (from advert
//     path byte). This is 100% reliable because the full public key is
//     received in adverts — no prefix collision ambiguity.
//
//  2. Suspected: the node's prefix appears as a hop in a packet whose path
//     header indicates hash_size >= 2. This is <100% reliable because
//     2-byte prefixes can collide — two different nodes may share the same
//     prefix. If one is confirmed multi-byte and the other is not, the
//     non-confirmed one could be a false positive.
//
//  3. Unknown: node has only been seen with 1-byte adverts and no
//     multi-byte path appearances. Could be pre-1.14 firmware or 1.14+
//     with default (1-byte) settings.
//
// Caller must hold NO locks — this method acquires mu.RLock internally.
func (s *PacketStore) computeMultiByteCapability(adopterHashSizes map[string]int) []MultiByteCapEntry {
	// Get hash size info from adverts (has its own locking)
	hashInfo := s.GetNodeHashSizeInfo()

	// Get all nodes for name/role lookup
	allNodes := s.getAllNodes()
	nodeByPK := make(map[string]nodeInfo, len(allNodes))
	for _, n := range allNodes {
		nodeByPK[n.PublicKey] = n
	}

	// Build set of confirmed multi-byte pubkeys (advert hash_size >= 2)
	confirmed := make(map[string]int) // pubkey → max hash size from adverts
	for pk, info := range hashInfo {
		maxHS := 1
		for sz := range info.AllSizes {
			if sz > maxHS {
				maxHS = sz
			}
		}
		if maxHS >= 2 {
			confirmed[pk] = maxHS
		}
	}

	// Scan path-hop index for suspected multi-byte nodes.
	// For each repeater, check if any packet in byPathHop has that
	// node as a hop with hash_size >= 2 in the path header.
	s.mu.RLock()

	// Build prefix→pubkey mapping for repeaters
	type prefixEntry struct {
		pubkey string
		prefix string
	}
	nodePrefixes := make(map[string][]prefixEntry) // prefix → entries
	for pk := range nodeByPK {
		// Generate 1-byte, 2-byte, 3-byte prefixes
		pkLower := strings.ToLower(pk)
		for byteLen := 1; byteLen <= 3; byteLen++ {
			hexLen := byteLen * 2
			if len(pkLower) >= hexLen {
				pfx := pkLower[:hexLen]
				nodePrefixes[pfx] = append(nodePrefixes[pfx], prefixEntry{pk, pfx})
			}
		}
	}

	suspected := make(map[string]int) // pubkey → max hash size from path appearances
	for pfx, entries := range nodePrefixes {
		txList := s.byPathHop[pfx]
		for _, tx := range txList {
			if tx.RawHex == "" || len(tx.RawHex) < 4 {
				continue
			}
			// Skip TRACE packets (payload_type 8) — they carry hash size in
			// TRACE flags, not the repeater's compile-time PATH_HASH_SIZE.
			// Pre-1.14 repeaters can forward multi-byte TRACEs, creating
			// false positives for "suspected" capability. See #714.
			if tx.PayloadType != nil && *tx.PayloadType == 8 {
				continue
			}
			header, err := strconv.ParseUint(tx.RawHex[:2], 16, 8)
			if err != nil {
				continue
			}
			routeType := header & 0x03
			pathByteIdx := 1
			if routeType == 0 || routeType == 3 {
				pathByteIdx = 5
			}
			hexStart := pathByteIdx * 2
			hexEnd := hexStart + 2
			if hexEnd > len(tx.RawHex) {
				continue
			}
			actualPathByte, err := strconv.ParseUint(tx.RawHex[hexStart:hexEnd], 16, 8)
			if err != nil {
				continue
			}
			hs := int((actualPathByte>>6)&0x3) + 1
			if hs < 2 {
				continue
			}
			// This packet uses multi-byte hashes and contains this prefix as a hop
			for _, e := range entries {
				if hs > suspected[e.pubkey] {
					suspected[e.pubkey] = hs
				}
			}
			break // one match is enough per prefix
		}
	}
	s.mu.RUnlock()

	// Build result for all nodes — fetch last_seen from DB
	dbLastSeen := make(map[string]string)
	rows, err := s.db.conn.Query("SELECT public_key, last_seen FROM nodes")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var pk string
			var ls sql.NullString
			rows.Scan(&pk, &ls)
			if ls.Valid {
				dbLastSeen[pk] = ls.String
			}
		}
	}

	var result []MultiByteCapEntry
	for pk, n := range nodeByPK {
		entry := MultiByteCapEntry{
			PublicKey:   pk,
			Name:        n.Name,
			Role:        n.Role,
			MaxHashSize: 1,
			LastSeen:    dbLastSeen[pk],
		}

		if maxHS, ok := confirmed[pk]; ok {
			entry.Status = "confirmed"
			entry.Evidence = "advert"
			entry.MaxHashSize = maxHS
		} else if maxHS, ok := adopterHashSizes[pk]; ok && maxHS >= 2 {
			// Adopter data (from computeAnalyticsHashSizes) shows hash_size >= 2
			// from advert analysis — this is advert-based evidence, so confirmed.
			entry.Status = "confirmed"
			entry.Evidence = "advert"
			entry.MaxHashSize = maxHS
		} else if maxHS, ok := suspected[pk]; ok {
			entry.Status = "suspected"
			entry.Evidence = "path"
			entry.MaxHashSize = maxHS
		} else {
			entry.Status = "unknown"
		}

		// Check advert hash info for max even if not confirmed multi-byte
		if info, ok := hashInfo[pk]; ok && entry.MaxHashSize == 1 {
			for sz := range info.AllSizes {
				if sz > entry.MaxHashSize {
					entry.MaxHashSize = sz
				}
			}
		}

		result = append(result, entry)
	}

	// Sort: confirmed first, then suspected, then unknown; within each group by name
	statusOrder := map[string]int{"confirmed": 0, "suspected": 1, "unknown": 2}
	sort.Slice(result, func(i, j int) bool {
		oi, oj := statusOrder[result[i].Status], statusOrder[result[j].Status]
		if oi != oj {
			return oi < oj
		}
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})

	return result
}

func (s *PacketStore) GetBulkHealth(limit int, region string) []map[string]interface{} {
	// Resolve region→observer set before taking s.mu so a cache-miss SQL
	// query does not run under the read lock (review item #2).
	var regionObs map[string]bool
	if region != "" {
		regionObs = s.resolveRegionObservers(region)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Region filtering
	var regionNodeKeys map[string]bool
	if region != "" {
		if regionObs != nil {
			regionalHashes := make(map[string]bool)
			for obsID := range regionObs {
				obsList := s.byObserver[obsID]
				for _, o := range obsList {
					tx := s.byTxID[o.TransmissionID]
					if tx != nil {
						regionalHashes[tx.Hash] = true
					}
				}
			}
			regionNodeKeys = make(map[string]bool)
			for pk, hashes := range s.nodeHashes {
				for h := range hashes {
					if regionalHashes[h] {
						regionNodeKeys[pk] = true
						break
					}
				}
			}
		}
	}

	// Get nodes from DB
	queryLimit := limit
	if regionNodeKeys != nil {
		queryLimit = 500
	}
	rows, err := s.db.conn.Query("SELECT public_key, name, role, lat, lon FROM nodes ORDER BY last_seen DESC LIMIT ?", queryLimit)
	if err != nil {
		return []map[string]interface{}{}
	}
	defer rows.Close()

	type dbNode struct {
		pk, name, role string
		lat, lon       interface{}
	}
	var nodes []dbNode
	for rows.Next() {
		var pk string
		var name, role sql.NullString
		var lat, lon sql.NullFloat64
		rows.Scan(&pk, &name, &role, &lat, &lon)
		if regionNodeKeys != nil && !regionNodeKeys[pk] {
			continue
		}
		nodes = append(nodes, dbNode{
			pk: pk, name: nullStrVal(name), role: nullStrVal(role),
			lat: nullFloat(lat), lon: nullFloat(lon),
		})
		if regionNodeKeys == nil && len(nodes) >= limit {
			break
		}
	}
	if regionNodeKeys != nil && len(nodes) > limit {
		nodes = nodes[:limit]
	}

	todayStart := time.Now().UTC().Truncate(24 * time.Hour).Format(time.RFC3339)
	results := make([]map[string]interface{}, 0, len(nodes))

	for _, n := range nodes {
		packets := s.byNode[n.pk]
		var packetsToday int
		var snrSum float64
		var snrCount int
		var lastHeard string
		observerStats := map[string]*struct {
			name                       string
			snrSum, rssiSum            float64
			snrCount, rssiCount, count int
		}{}
		totalObservations := 0

		for _, pkt := range packets {
			totalObservations += pkt.ObservationCount
			if totalObservations == 0 {
				totalObservations = 1
			}
			if pkt.FirstSeen > todayStart {
				packetsToday++
			}
			if pkt.SNR != nil {
				snrSum += *pkt.SNR
				snrCount++
			}
			if lastHeard == "" || pkt.FirstSeen > lastHeard {
				lastHeard = pkt.FirstSeen
			}
			obsID := pkt.ObserverID
			if obsID != "" {
				obs := observerStats[obsID]
				if obs == nil {
					obs = &struct {
						name                       string
						snrSum, rssiSum            float64
						snrCount, rssiCount, count int
					}{name: pkt.ObserverName}
					observerStats[obsID] = obs
				}
				obs.count++
				if pkt.SNR != nil {
					obs.snrSum += *pkt.SNR
					obs.snrCount++
				}
				if pkt.RSSI != nil {
					obs.rssiSum += *pkt.RSSI
					obs.rssiCount++
				}
			}
		}

		observerRows := make([]map[string]interface{}, 0)
		for id, o := range observerStats {
			var avgSnr, avgRssi interface{}
			if o.snrCount > 0 {
				avgSnr = o.snrSum / float64(o.snrCount)
			}
			if o.rssiCount > 0 {
				avgRssi = o.rssiSum / float64(o.rssiCount)
			}
			observerRows = append(observerRows, map[string]interface{}{
				"observer_id": id, "observer_name": o.name,
				"avgSnr": avgSnr, "avgRssi": avgRssi, "packetCount": o.count,
			})
		}
		sort.Slice(observerRows, func(i, j int) bool {
			return observerRows[i]["packetCount"].(int) > observerRows[j]["packetCount"].(int)
		})

		var avgSnr interface{}
		if snrCount > 0 {
			avgSnr = snrSum / float64(snrCount)
		}
		var lhVal interface{}
		if lastHeard != "" {
			lhVal = lastHeard
		}

		results = append(results, map[string]interface{}{
			"public_key": n.pk,
			"name":       nilIfEmpty(n.name),
			"role":       nilIfEmpty(n.role),
			"lat":        n.lat,
			"lon":        n.lon,
			"stats": map[string]interface{}{
				"totalTransmissions": len(packets),
				"totalObservations":  totalObservations,
				"totalPackets":       len(packets),
				"packetsToday":       packetsToday,
				"avgSnr":             avgSnr,
				"lastHeard":          lhVal,
			},
			"observers": observerRows,
		})
	}

	return results
}

// GetNodeHealth returns health info for a single node using in-memory data.
func (s *PacketStore) GetNodeHealth(pubkey string) (map[string]interface{}, error) {
	// Fetch node info from DB (fast single-row lookup)
	node, err := s.db.GetNodeByPubkey(pubkey)
	if err != nil {
		return nil, err
	}
	// If the node isn't in the DB (e.g. companion that never advertised),
	// check if we have any packet data for it. If so, build a partial response.
	if node == nil {
		s.mu.RLock()
		hasPackets := len(s.byNode[pubkey]) > 0
		s.mu.RUnlock()
		if !hasPackets {
			return nil, nil
		}
		// Build a synthetic node stub so the rest of the function works
		node = map[string]interface{}{
			"public_key": pubkey,
			"name":       "Unknown",
			"role":       "unknown",
		}
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	packets := s.byNode[pubkey]
	todayStart := time.Now().UTC().Truncate(24 * time.Hour).Format(time.RFC3339)

	var packetsToday int
	var snrSum float64
	var snrCount int
	var totalHops, hopCount int
	var lastHeard string
	totalObservations := 0

	observerStats := map[string]*struct {
		name                       string
		snrSum, rssiSum            float64
		snrCount, rssiCount, count int
	}{}

	for _, pkt := range packets {
		totalObservations += pkt.ObservationCount
		if pkt.FirstSeen > todayStart {
			packetsToday++
		}
		if pkt.SNR != nil {
			snrSum += *pkt.SNR
			snrCount++
		}
		if lastHeard == "" || pkt.FirstSeen > lastHeard {
			lastHeard = pkt.FirstSeen
		}
		// Hop counting
		hops := txGetParsedPath(pkt)
		if len(hops) > 0 {
			totalHops += len(hops)
			hopCount++
		}
		// Observer stats
		obsID := pkt.ObserverID
		if obsID != "" {
			obs := observerStats[obsID]
			if obs == nil {
				obs = &struct {
					name                       string
					snrSum, rssiSum            float64
					snrCount, rssiCount, count int
				}{name: pkt.ObserverName}
				observerStats[obsID] = obs
			}
			obs.count++
			if pkt.SNR != nil {
				obs.snrSum += *pkt.SNR
				obs.snrCount++
			}
			if pkt.RSSI != nil {
				obs.rssiSum += *pkt.RSSI
				obs.rssiCount++
			}
		}
	}

	observerRows := make([]map[string]interface{}, 0)
	for id, o := range observerStats {
		var avgSnr, avgRssi interface{}
		if o.snrCount > 0 {
			avgSnr = o.snrSum / float64(o.snrCount)
		}
		if o.rssiCount > 0 {
			avgRssi = o.rssiSum / float64(o.rssiCount)
		}
		observerRows = append(observerRows, map[string]interface{}{
			"observer_id": id, "observer_name": o.name,
			"avgSnr": avgSnr, "avgRssi": avgRssi, "packetCount": o.count,
		})
	}
	sort.Slice(observerRows, func(i, j int) bool {
		return observerRows[i]["packetCount"].(int) > observerRows[j]["packetCount"].(int)
	})

	var avgSnr interface{}
	if snrCount > 0 {
		avgSnr = snrSum / float64(snrCount)
	}
	avgHops := 0
	if hopCount > 0 {
		avgHops = int(math.Round(float64(totalHops) / float64(hopCount)))
	}
	var lhVal interface{}
	if lastHeard != "" {
		lhVal = lastHeard
	}

	// Recent packets (up to 20, newest first — read from tail of oldest-first slice)
	recentLimit := 20
	if len(packets) < recentLimit {
		recentLimit = len(packets)
	}
	recentPackets := make([]map[string]interface{}, 0, recentLimit)
	for i := len(packets) - 1; i >= len(packets)-recentLimit; i-- {
		p := s.txToMapWithRP(packets[i])
		delete(p, "observations")
		recentPackets = append(recentPackets, p)
	}

	return map[string]interface{}{
		"node":      node,
		"observers": observerRows,
		"stats": map[string]interface{}{
			"totalTransmissions": len(packets),
			"totalObservations":  totalObservations,
			"totalPackets":       len(packets),
			"packetsToday":       packetsToday,
			"avgSnr":             avgSnr,
			"avgHops":            avgHops,
			"lastHeard":          lhVal,
		},
		"recentPackets": recentPackets,
	}, nil
}

// GetNodeAnalytics computes analytics for a single node using in-memory byNode index.
func (s *PacketStore) GetNodeAnalytics(pubkey string, days int) (*NodeAnalyticsResponse, error) {
	node, err := s.db.GetNodeByPubkey(pubkey)
	if err != nil || node == nil {
		return nil, err
	}

	fromTime := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	fromISO := fromTime.Format(time.RFC3339)
	toISO := time.Now().Format(time.RFC3339)

	// Recompute clock skew BEFORE taking the store RLock — getNodeClockSkewLocked
	// (called later under the lock) only reads cached skew data; the heavy
	// O(n²) compute must not run under the lock (#10).
	s.clockSkew.Recompute(s)

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Collect packets from byNode index (time-filtered).
	// Raw JSON text search is intentionally avoided: a GRP_TXT packet whose message
	// text contains a node's pubkey is not a packet *for* that node.
	indexed := s.byNode[pubkey]
	var packets []*StoreTx
	for _, p := range indexed {
		if p.FirstSeen > fromISO {
			packets = append(packets, p)
		}
	}

	// Activity timeline (hourly buckets)
	timelineBuckets := map[string]int{}
	for _, p := range packets {
		if len(p.FirstSeen) >= 13 {
			bucket := p.FirstSeen[:13] + ":00:00Z"
			timelineBuckets[bucket]++
		}
	}
	bucketKeys := make([]string, 0, len(timelineBuckets))
	for k := range timelineBuckets {
		bucketKeys = append(bucketKeys, k)
	}
	sort.Strings(bucketKeys)
	activityTimeline := make([]TimeBucket, 0, len(bucketKeys))
	for _, k := range bucketKeys {
		b := k
		activityTimeline = append(activityTimeline, TimeBucket{Bucket: &b, Count: timelineBuckets[k]})
	}

	// SNR trend
	snrTrend := make([]SnrTrendEntry, 0)
	for _, p := range packets {
		if p.SNR != nil {
			snrTrend = append(snrTrend, SnrTrendEntry{
				Timestamp:    p.FirstSeen,
				SNR:          floatPtrOrNil(p.SNR),
				RSSI:         floatPtrOrNil(p.RSSI),
				ObserverID:   strOrNil(p.ObserverID),
				ObserverName: strOrNil(p.ObserverName),
			})
		}
	}

	// Packet type breakdown
	typeBuckets := map[int]int{}
	for _, p := range packets {
		if p.PayloadType != nil {
			typeBuckets[*p.PayloadType]++
		}
	}
	packetTypeBreakdown := make([]PayloadTypeCount, 0, len(typeBuckets))
	for pt, cnt := range typeBuckets {
		packetTypeBreakdown = append(packetTypeBreakdown, PayloadTypeCount{PayloadType: pt, Count: cnt})
	}

	// Observer coverage
	type obsAccum struct {
		name                       string
		snrSum, rssiSum            float64
		snrCount, rssiCount, count int
		first, last                string
	}
	obsMap := map[string]*obsAccum{}
	for _, p := range packets {
		if p.ObserverID == "" {
			continue
		}
		o := obsMap[p.ObserverID]
		if o == nil {
			o = &obsAccum{name: p.ObserverName, first: p.FirstSeen, last: p.FirstSeen}
			obsMap[p.ObserverID] = o
		}
		o.count++
		if p.SNR != nil {
			o.snrSum += *p.SNR
			o.snrCount++
		}
		if p.RSSI != nil {
			o.rssiSum += *p.RSSI
			o.rssiCount++
		}
		if p.FirstSeen < o.first {
			o.first = p.FirstSeen
		}
		if p.FirstSeen > o.last {
			o.last = p.FirstSeen
		}
	}
	observerCoverage := make([]NodeObserverStatsResp, 0, len(obsMap))
	for id, o := range obsMap {
		var avgSnr, avgRssi interface{}
		if o.snrCount > 0 {
			avgSnr = o.snrSum / float64(o.snrCount)
		}
		if o.rssiCount > 0 {
			avgRssi = o.rssiSum / float64(o.rssiCount)
		}
		observerCoverage = append(observerCoverage, NodeObserverStatsResp{
			ObserverID:   id,
			ObserverName: o.name,
			PacketCount:  o.count,
			AvgSnr:       avgSnr,
			AvgRssi:      avgRssi,
			FirstSeen:    o.first,
			LastSeen:     o.last,
		})
	}
	sort.Slice(observerCoverage, func(i, j int) bool {
		return observerCoverage[i].PacketCount > observerCoverage[j].PacketCount
	})

	// Hop distribution
	hopCounts := map[string]int{}
	totalWithPath := 0
	relayedCount := 0
	for _, p := range packets {
		hops := txGetParsedPath(p)
		if len(hops) > 0 {
			key := fmt.Sprintf("%d", len(hops))
			if len(hops) >= 4 {
				key = "4+"
			}
			hopCounts[key]++
			totalWithPath++
			if len(hops) > 1 {
				relayedCount++
			}
		} else {
			hopCounts["0"]++
		}
	}
	hopDistribution := make([]HopDistEntry, 0)
	for _, h := range []string{"0", "1", "2", "3", "4+"} {
		if c, ok := hopCounts[h]; ok {
			hopDistribution = append(hopDistribution, HopDistEntry{Hops: h, Count: c})
		}
	}

	// Peer interactions
	type peerAccum struct {
		key, name   string
		count       int
		lastContact string
	}
	peerMap := map[string]*peerAccum{}
	for _, p := range packets {
		if p.DecodedJSON == "" {
			continue
		}
		var decoded map[string]interface{}
		if json.Unmarshal([]byte(p.DecodedJSON), &decoded) != nil {
			continue
		}
		type candidate struct{ key, name string }
		var candidates []candidate
		if sk, ok := decoded["sender_key"].(string); ok && sk != "" && sk != pubkey {
			sn, _ := decoded["sender_name"].(string)
			if sn == "" {
				sn, _ = decoded["sender_short_name"].(string)
			}
			candidates = append(candidates, candidate{sk, sn})
		}
		if rk, ok := decoded["recipient_key"].(string); ok && rk != "" && rk != pubkey {
			rn, _ := decoded["recipient_name"].(string)
			if rn == "" {
				rn, _ = decoded["recipient_short_name"].(string)
			}
			candidates = append(candidates, candidate{rk, rn})
		}
		if pk, ok := decoded["pubkey"].(string); ok && pk != "" && pk != pubkey {
			nm, _ := decoded["name"].(string)
			candidates = append(candidates, candidate{pk, nm})
		}
		for _, c := range candidates {
			if c.key == "" {
				continue
			}
			pm := peerMap[c.key]
			if pm == nil {
				pn := c.name
				if pn == "" && len(c.key) >= 12 {
					pn = c.key[:12]
				}
				pm = &peerAccum{key: c.key, name: pn, lastContact: p.FirstSeen}
				peerMap[c.key] = pm
			}
			pm.count++
			if p.FirstSeen > pm.lastContact {
				pm.lastContact = p.FirstSeen
			}
		}
	}
	peerSlice := make([]PeerInteraction, 0, len(peerMap))
	for _, pm := range peerMap {
		peerSlice = append(peerSlice, PeerInteraction{
			PeerKey: pm.key, PeerName: pm.name,
			MessageCount: pm.count, LastContact: pm.lastContact,
		})
	}
	sort.Slice(peerSlice, func(i, j int) bool {
		return peerSlice[i].MessageCount > peerSlice[j].MessageCount
	})
	if len(peerSlice) > 20 {
		peerSlice = peerSlice[:20]
	}

	// Uptime heatmap
	heatBuckets := map[string]*HeatmapCell{}
	for _, p := range packets {
		t, err := time.Parse(time.RFC3339, p.FirstSeen)
		if err != nil {
			t, err = time.Parse("2006-01-02 15:04:05", p.FirstSeen)
			if err != nil {
				continue
			}
		}
		dow := int(t.UTC().Weekday())
		hr := t.UTC().Hour()
		k := fmt.Sprintf("%d:%d", dow, hr)
		if heatBuckets[k] == nil {
			heatBuckets[k] = &HeatmapCell{DayOfWeek: dow, Hour: hr}
		}
		heatBuckets[k].Count++
	}
	uptimeHeatmap := make([]HeatmapCell, 0, len(heatBuckets))
	for _, cell := range heatBuckets {
		uptimeHeatmap = append(uptimeHeatmap, *cell)
	}

	// Computed stats
	totalPackets := len(packets)
	distinctHours := len(activityTimeline)
	totalHours := float64(days) * 24
	availabilityPct := 0.0
	if totalHours > 0 {
		availabilityPct = round(float64(distinctHours)*100.0/totalHours, 1)
		if availabilityPct > 100 {
			availabilityPct = 100
		}
	}

	var avgPacketsPerDay float64
	if days > 0 {
		avgPacketsPerDay = round(float64(totalPackets)/float64(days), 1)
	}

	// Longest silence
	var longestSilenceMs int
	var longestSilenceStart interface{}
	if len(activityTimeline) >= 2 {
		for i := 1; i < len(activityTimeline); i++ {
			var t1Str, t2Str string
			if activityTimeline[i-1].Bucket != nil {
				t1Str = *activityTimeline[i-1].Bucket
			}
			if activityTimeline[i].Bucket != nil {
				t2Str = *activityTimeline[i].Bucket
			}
			t1, e1 := time.Parse(time.RFC3339, t1Str)
			t2, e2 := time.Parse(time.RFC3339, t2Str)
			if e1 == nil && e2 == nil {
				gap := int(t2.Sub(t1).Milliseconds())
				if gap > longestSilenceMs {
					longestSilenceMs = gap
					longestSilenceStart = t1Str
				}
			}
		}
	}

	// Signal grade & SNR stats
	var snrMean, snrStdDev float64
	if len(snrTrend) > 0 {
		var sum float64
		for _, e := range snrTrend {
			if v, ok := e.SNR.(float64); ok {
				sum += v
			}
		}
		snrMean = sum / float64(len(snrTrend))
		if len(snrTrend) > 1 {
			var sqSum float64
			for _, e := range snrTrend {
				if v, ok := e.SNR.(float64); ok {
					sqSum += (v - snrMean) * (v - snrMean)
				}
			}
			snrStdDev = math.Sqrt(sqSum / float64(len(snrTrend)))
		}
	}

	signalGrade := "D"
	if snrMean > 15 && snrStdDev < 2 {
		signalGrade = "A"
	} else if snrMean > 15 {
		signalGrade = "A-"
	} else if snrMean > 12 && snrStdDev < 3 {
		signalGrade = "B+"
	} else if snrMean > 8 {
		signalGrade = "B"
	} else if snrMean > 3 {
		signalGrade = "C"
	}

	var relayPct float64
	if totalWithPath > 0 {
		relayPct = round(float64(relayedCount)*100.0/float64(totalWithPath), 1)
	}

	// Compute clock skew (already under RLock).
	clockSkew := s.getNodeClockSkewLocked(pubkey)

	return &NodeAnalyticsResponse{
		Node:                node,
		TimeRange:           TimeRangeResp{From: fromISO, To: toISO, Days: days},
		ActivityTimeline:    activityTimeline,
		SnrTrend:            snrTrend,
		PacketTypeBreakdown: packetTypeBreakdown,
		ObserverCoverage:    observerCoverage,
		HopDistribution:     hopDistribution,
		PeerInteractions:    peerSlice,
		UptimeHeatmap:       uptimeHeatmap,
		ComputedStats: ComputedNodeStats{
			AvailabilityPct:     availabilityPct,
			LongestSilenceMs:    longestSilenceMs,
			LongestSilenceStart: longestSilenceStart,
			SignalGrade:         signalGrade,
			SnrMean:             round(snrMean, 1),
			SnrStdDev:           round(snrStdDev, 1),
			RelayPct:            relayPct,
			TotalPackets:        totalPackets,
			UniqueObservers:     len(observerCoverage),
			UniquePeers:         len(peerSlice),
			AvgPacketsPerDay:    avgPacketsPerDay,
		},
		ClockSkew: clockSkew,
	}, nil
}
