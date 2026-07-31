package main

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"time"
)

// nodeAnalyticsObsAccum and nodeAnalyticsPeerAccum are the per-observer and
// per-peer running totals GetNodeAnalytics used to build inline — extracted
// verbatim (not renamed in meaning) so the full endpoint's own output is
// unchanged, only where the loop body lives.
type nodeAnalyticsObsAccum struct {
	name                       string
	snrSum, rssiSum            float64
	snrCount, rssiCount, count int
	first, last                string
}

type nodeAnalyticsPeerAccum struct {
	key, name   string
	count       int
	lastContact string
}

// nodeAnalyticsAccumulator is the single per-packet pass shared by the full
// GET /api/nodes/{pubkey}/analytics endpoint and its light
// /analytics/summary sibling (Fase 5.2a). txGetParsedPath and decoded_json
// unmarshaling — the two genuinely expensive per-packet operations — each
// run exactly once per packet regardless of which caller is asking.
// withDisplayArrays only controls whether the heavier client-facing DTOs
// (activityTimeline, snrTrend, packetTypeBreakdown, observerCoverage,
// hopDistribution, peerInteractions, uptimeHeatmap) — none of which any
// ComputedNodeStats field depends on — also get built alongside the numbers
// both endpoints share.
type nodeAnalyticsAccumulator struct {
	pubkey            string
	withDisplayArrays bool

	// Always accumulated: every ComputedNodeStats field derives from these.
	totalPackets    int
	timelineBuckets map[string]int
	// snrCount/snrRunningMean/snrM2 are Welford's online mean/variance
	// state (constant memory, no per-packet SNR history kept) — see
	// accumulate() and finalizeComputedStats() for the update/read steps.
	snrCount       int
	snrRunningMean float64
	snrM2          float64
	totalWithPath  int
	relayedCount   int
	observerSeen   map[string]struct{}
	peerSeen       map[string]struct{}

	// Only accumulated when withDisplayArrays is true — the full endpoint's
	// exclusive display data; ComputedNodeStats never reads from these.
	snrTrend    []SnrTrendEntry
	typeBuckets map[int]int
	obsDetail   map[string]*nodeAnalyticsObsAccum
	hopCounts   map[string]int
	peerDetail  map[string]*nodeAnalyticsPeerAccum
	heatBuckets map[string]*HeatmapCell
}

func newNodeAnalyticsAccumulator(pubkey string, withDisplayArrays bool) *nodeAnalyticsAccumulator {
	a := &nodeAnalyticsAccumulator{
		pubkey:            pubkey,
		withDisplayArrays: withDisplayArrays,
		timelineBuckets:   map[string]int{},
		observerSeen:      map[string]struct{}{},
		peerSeen:          map[string]struct{}{},
	}
	if withDisplayArrays {
		a.snrTrend = make([]SnrTrendEntry, 0)
		a.typeBuckets = map[int]int{}
		a.obsDetail = map[string]*nodeAnalyticsObsAccum{}
		a.hopCounts = map[string]int{}
		a.peerDetail = map[string]*nodeAnalyticsPeerAccum{}
		a.heatBuckets = map[string]*HeatmapCell{}
	}
	return a
}

// accumulate folds one packet into the accumulator — the seven per-packet
// steps GetNodeAnalytics used to run as separate loops over `packets`,
// merged into one pass.
func (a *nodeAnalyticsAccumulator) accumulate(p *StoreTx) {
	a.totalPackets++

	// Activity timeline bucket.
	if len(p.FirstSeen) >= 13 {
		a.timelineBuckets[p.FirstSeen[:13]+":00:00Z"]++
	}

	// SNR — folded into Welford's online mean/M2 in constant memory (mean/
	// stddev need these, never the raw history); the richer per-packet
	// trend entry only when building display arrays.
	if p.SNR != nil {
		a.snrCount++
		delta := *p.SNR - a.snrRunningMean
		a.snrRunningMean += delta / float64(a.snrCount)
		delta2 := *p.SNR - a.snrRunningMean
		a.snrM2 += delta * delta2
		if a.withDisplayArrays {
			a.snrTrend = append(a.snrTrend, SnrTrendEntry{
				Timestamp:    p.FirstSeen,
				SNR:          floatPtrOrNil(p.SNR),
				RSSI:         floatPtrOrNil(p.RSSI),
				ObserverID:   strOrNil(p.ObserverID),
				ObserverName: strOrNil(p.ObserverName),
			})
		}
	}

	// Packet type breakdown — no ComputedNodeStats field depends on this.
	if a.withDisplayArrays && p.PayloadType != nil {
		a.typeBuckets[*p.PayloadType]++
	}

	// Observer coverage — distinct-ID set always tracked (uniqueObservers);
	// the richer per-observer accumulation only for the display array.
	if p.ObserverID != "" {
		if a.withDisplayArrays {
			o := a.obsDetail[p.ObserverID]
			if o == nil {
				o = &nodeAnalyticsObsAccum{name: p.ObserverName, first: p.FirstSeen, last: p.FirstSeen}
				a.obsDetail[p.ObserverID] = o
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
		} else {
			a.observerSeen[p.ObserverID] = struct{}{}
		}
	}

	// Hop / relay classification — txGetParsedPath runs exactly once here
	// regardless of mode (it also self-memoizes on the packet); only the
	// display array's hopCounts bucket needs the extra bookkeeping.
	hops := txGetParsedPath(p)
	if len(hops) > 0 {
		a.totalWithPath++
		if len(hops) > 1 {
			a.relayedCount++
		}
		if a.withDisplayArrays {
			key := fmt.Sprintf("%d", len(hops))
			if len(hops) >= 4 {
				key = "4+"
			}
			a.hopCounts[key]++
		}
	} else if a.withDisplayArrays {
		a.hopCounts["0"]++
	}

	// Peer interactions — json.Unmarshal runs exactly once here regardless
	// of mode; only the display array needs per-peer name/count/lastContact.
	if p.DecodedJSON != "" {
		var decoded map[string]interface{}
		if json.Unmarshal([]byte(p.DecodedJSON), &decoded) == nil {
			for _, c := range nodeAnalyticsPeerCandidates(decoded, a.pubkey) {
				if c.key == "" {
					continue
				}
				if a.withDisplayArrays {
					pm := a.peerDetail[c.key]
					if pm == nil {
						pn := c.name
						if pn == "" && len(c.key) >= 12 {
							pn = c.key[:12]
						}
						pm = &nodeAnalyticsPeerAccum{key: c.key, name: pn, lastContact: p.FirstSeen}
						a.peerDetail[c.key] = pm
					}
					pm.count++
					if p.FirstSeen > pm.lastContact {
						pm.lastContact = p.FirstSeen
					}
				} else {
					a.peerSeen[c.key] = struct{}{}
				}
			}
		}
	}

	// Uptime heatmap — no ComputedNodeStats field depends on this.
	if a.withDisplayArrays {
		t, err := time.Parse(time.RFC3339, p.FirstSeen)
		if err != nil {
			t, err = time.Parse("2006-01-02 15:04:05", p.FirstSeen)
		}
		if err == nil {
			dow := int(t.UTC().Weekday())
			hr := t.UTC().Hour()
			k := fmt.Sprintf("%d:%d", dow, hr)
			if a.heatBuckets[k] == nil {
				a.heatBuckets[k] = &HeatmapCell{DayOfWeek: dow, Hour: hr}
			}
			a.heatBuckets[k].Count++
		}
	}
}

type nodeAnalyticsPeerCandidate struct{ key, name string }

// nodeAnalyticsPeerCandidates extracts up to three peer candidates
// (sender/recipient/pubkey fields) from one packet's already-decoded JSON,
// excluding pubkey itself — same field names and name-fallback rules
// GetNodeAnalytics always used, extracted verbatim so full and summary
// agree on who counts as a peer.
func nodeAnalyticsPeerCandidates(decoded map[string]interface{}, pubkey string) []nodeAnalyticsPeerCandidate {
	var candidates []nodeAnalyticsPeerCandidate
	if sk, ok := decoded["sender_key"].(string); ok && sk != "" && sk != pubkey {
		sn, _ := decoded["sender_name"].(string)
		if sn == "" {
			sn, _ = decoded["sender_short_name"].(string)
		}
		candidates = append(candidates, nodeAnalyticsPeerCandidate{sk, sn})
	}
	if rk, ok := decoded["recipient_key"].(string); ok && rk != "" && rk != pubkey {
		rn, _ := decoded["recipient_name"].(string)
		if rn == "" {
			rn, _ = decoded["recipient_short_name"].(string)
		}
		candidates = append(candidates, nodeAnalyticsPeerCandidate{rk, rn})
	}
	if pk, ok := decoded["pubkey"].(string); ok && pk != "" && pk != pubkey {
		nm, _ := decoded["name"].(string)
		candidates = append(candidates, nodeAnalyticsPeerCandidate{pk, nm})
	}
	return candidates
}

// finalizeComputedStats derives all 11 ComputedNodeStats fields — identical
// whether or not withDisplayArrays was set, since every field is sourced
// from the "always accumulated" state above.
func (a *nodeAnalyticsAccumulator) finalizeComputedStats(days int) ComputedNodeStats {
	bucketKeys := make([]string, 0, len(a.timelineBuckets))
	for k := range a.timelineBuckets {
		bucketKeys = append(bucketKeys, k)
	}
	sort.Strings(bucketKeys)

	distinctHours := len(bucketKeys)
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
		avgPacketsPerDay = round(float64(a.totalPackets)/float64(days), 1)
	}

	var longestSilenceMs int
	var longestSilenceStart interface{}
	if len(bucketKeys) >= 2 {
		for i := 1; i < len(bucketKeys); i++ {
			t1, e1 := time.Parse(time.RFC3339, bucketKeys[i-1])
			t2, e2 := time.Parse(time.RFC3339, bucketKeys[i])
			if e1 == nil && e2 == nil {
				gap := int(t2.Sub(t1).Milliseconds())
				if gap > longestSilenceMs {
					longestSilenceMs = gap
					longestSilenceStart = bucketKeys[i-1]
				}
			}
		}
	}

	// Population variance (divide by N, matching the endpoint's original
	// two-pass semantics — not the N-1 sample variance) from Welford's M2:
	// for snrCount==1, M2 is exactly 0 (the first update sets mean to that
	// value with zero deviation), so stddev is 0 without a separate branch.
	var snrMean, snrStdDev float64
	if a.snrCount > 0 {
		snrMean = a.snrRunningMean
		snrStdDev = math.Sqrt(a.snrM2 / float64(a.snrCount))
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
	if a.totalWithPath > 0 {
		relayPct = round(float64(a.relayedCount)*100.0/float64(a.totalWithPath), 1)
	}

	uniqueObservers := len(a.observerSeen)
	uniquePeers := len(a.peerSeen)
	if a.withDisplayArrays {
		uniqueObservers = len(a.obsDetail)
		uniquePeers = len(a.peerDetail)
	}
	if uniquePeers > 20 {
		uniquePeers = 20
	}

	return ComputedNodeStats{
		AvailabilityPct:     availabilityPct,
		LongestSilenceMs:    longestSilenceMs,
		LongestSilenceStart: longestSilenceStart,
		SignalGrade:         signalGrade,
		SnrMean:             round(snrMean, 1),
		SnrStdDev:           round(snrStdDev, 1),
		RelayPct:            relayPct,
		TotalPackets:        a.totalPackets,
		UniqueObservers:     uniqueObservers,
		UniquePeers:         uniquePeers,
		AvgPacketsPerDay:    avgPacketsPerDay,
	}
}

// finalizeDisplayArrays builds the full endpoint's seven heavy client
// arrays from the accumulator. Only meaningful when withDisplayArrays was
// true — the summary endpoint never calls this.
func (a *nodeAnalyticsAccumulator) finalizeDisplayArrays() (
	activityTimeline []TimeBucket,
	snrTrend []SnrTrendEntry,
	packetTypeBreakdown []PayloadTypeCount,
	observerCoverage []NodeObserverStatsResp,
	hopDistribution []HopDistEntry,
	peerInteractions []PeerInteraction,
	uptimeHeatmap []HeatmapCell,
) {
	bucketKeys := make([]string, 0, len(a.timelineBuckets))
	for k := range a.timelineBuckets {
		bucketKeys = append(bucketKeys, k)
	}
	sort.Strings(bucketKeys)
	activityTimeline = make([]TimeBucket, 0, len(bucketKeys))
	for _, k := range bucketKeys {
		b := k
		activityTimeline = append(activityTimeline, TimeBucket{Bucket: &b, Count: a.timelineBuckets[k]})
	}

	snrTrend = a.snrTrend

	packetTypeBreakdown = make([]PayloadTypeCount, 0, len(a.typeBuckets))
	for pt, cnt := range a.typeBuckets {
		packetTypeBreakdown = append(packetTypeBreakdown, PayloadTypeCount{PayloadType: pt, Count: cnt})
	}

	observerCoverage = make([]NodeObserverStatsResp, 0, len(a.obsDetail))
	for id, o := range a.obsDetail {
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

	hopDistribution = make([]HopDistEntry, 0)
	for _, h := range []string{"0", "1", "2", "3", "4+"} {
		if c, ok := a.hopCounts[h]; ok {
			hopDistribution = append(hopDistribution, HopDistEntry{Hops: h, Count: c})
		}
	}

	peerInteractions = make([]PeerInteraction, 0, len(a.peerDetail))
	for _, pm := range a.peerDetail {
		peerInteractions = append(peerInteractions, PeerInteraction{
			PeerKey: pm.key, PeerName: pm.name,
			MessageCount: pm.count, LastContact: pm.lastContact,
		})
	}
	sort.Slice(peerInteractions, func(i, j int) bool {
		return peerInteractions[i].MessageCount > peerInteractions[j].MessageCount
	})
	if len(peerInteractions) > 20 {
		peerInteractions = peerInteractions[:20]
	}

	uptimeHeatmap = make([]HeatmapCell, 0, len(a.heatBuckets))
	for _, cell := range a.heatBuckets {
		uptimeHeatmap = append(uptimeHeatmap, *cell)
	}

	return
}

// filterNodePacketsLocked returns pubkey's packets first seen after
// fromISO, from the byNode index — shared by GetNodeAnalytics and
// GetNodeAnalyticsSummary. Raw JSON text search is intentionally avoided: a
// GRP_TXT packet whose message text contains a node's pubkey is not a
// packet *for* that node (issue673). Caller must hold s.mu (at least
// RLock).
func (s *PacketStore) filterNodePacketsLocked(pubkey, fromISO string) []*StoreTx {
	indexed := s.byNode[pubkey]
	var packets []*StoreTx
	for _, p := range indexed {
		if p.FirstSeen > fromISO {
			packets = append(packets, p)
		}
	}
	return packets
}

// GetNodeAnalyticsSummary is the light sibling of GetNodeAnalytics (Fase
// 5.2a): same node lookup/404 semantics and the same time-windowed packet
// set, but shares nodeAnalyticsAccumulator with withDisplayArrays=false —
// none of the seven heavy display arrays are ever built, and clock skew is
// never computed (this endpoint never calls getNodeClockSkewLocked).
// ComputedStats is guaranteed identical to what GetNodeAnalytics produces
// for the same pubkey/days/now, since both share the same private "at a
// given instant" implementation and accumulate()/finalizeComputedStats().
func (s *PacketStore) GetNodeAnalyticsSummary(pubkey string, days int) (*NodeAnalyticsSummaryResponse, error) {
	return s.getNodeAnalyticsSummaryAt(pubkey, days, time.Now())
}

// getNodeAnalyticsSummaryAt does the real work for GetNodeAnalyticsSummary,
// taking `now` explicitly instead of calling time.Now() itself. This is the
// seam that lets a test drive GetNodeAnalytics and GetNodeAnalyticsSummary
// off literally the same instant (for the full/summary parity assertion)
// without any package-level mutable clock: no global var, no mutex around a
// test hook, no test-only state in the production call path — the public
// method above is the only production caller, and it always passes a real
// time.Now(), read exactly once.
func (s *PacketStore) getNodeAnalyticsSummaryAt(pubkey string, days int, now time.Time) (*NodeAnalyticsSummaryResponse, error) {
	node, err := s.db.GetNodeByPubkey(pubkey)
	if err != nil || node == nil {
		return nil, err
	}

	fromISO := now.Add(-time.Duration(days) * 24 * time.Hour).Format(time.RFC3339)
	toISO := now.Format(time.RFC3339)

	s.mu.RLock()
	defer s.mu.RUnlock()

	packets := s.filterNodePacketsLocked(pubkey, fromISO)

	acc := newNodeAnalyticsAccumulator(pubkey, false)
	for _, p := range packets {
		acc.accumulate(p)
	}

	return &NodeAnalyticsSummaryResponse{
		TimeRange:     TimeRangeResp{From: fromISO, To: toISO, Days: days},
		ComputedStats: acc.finalizeComputedStats(days),
	}, nil
}
