package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"testing"
	"time"
)

// f64p is a small pointer-literal helper for building StoreTx fixtures.
func f64p(v float64) *float64 { return &v }

// insertCharacterizationNode registers pubkey in the nodes table (required
// for db.GetNodeByPubkey to resolve) and returns a fresh in-memory store
// with no packets loaded yet — same construction style as
// setupIssue673Store in issue673_test.go.
func insertCharacterizationNode(t *testing.T, pubkey string) (*PacketStore, *DB) {
	t.Helper()
	db := setupTestDB(t)
	if _, err := db.conn.Exec(
		"INSERT INTO nodes (public_key, name, role) VALUES (?, ?, ?)",
		pubkey, "CharacterizationNode", "repeater",
	); err != nil {
		t.Fatalf("insert node: %v", err)
	}
	return NewPacketStore(db, nil), db
}

// loadPacketsIntoStore installs packets directly into ps's in-memory
// indexes for pubkey, bypassing the DB/Load() path entirely so the fixture
// is fully deterministic (no timing/index-build nondeterminism).
func loadPacketsIntoStore(ps *PacketStore, pubkey string, packets []*StoreTx) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for _, p := range packets {
		ps.packets = append(ps.packets, p)
		ps.byHash[p.Hash] = p
		ps.byTxID[p.ID] = p
	}
	ps.byNode[pubkey] = packets
}

func mustParseRFC3339UTC(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return tm.UTC()
}

// TestNodeAnalyticsFull_CharacterizationAgainstPreRefactorSemantics builds a
// deterministic 6-packet fixture and asserts every field of the full
// /analytics response against values hand-derived from the pre-refactor
// GetNodeAnalytics implementation (parent commit 248c3d0's store.go,
// lines ~9573-9924) -- not from running the new code and copying its
// output. Where the original code's map iteration made an order
// nondeterministic (packetTypeBreakdown, uptimeHeatmap), both actual and
// expected are sorted before comparing; observerCoverage and
// peerInteractions ARE ordering-tested since the API promises a descending
// sort by count -- the fixture deliberately avoids count ties there so a
// direct slice comparison is meaningful.
func TestNodeAnalyticsFull_CharacterizationAgainstPreRefactorSemantics(t *testing.T) {
	pubkey := "char5202test0000000000000000000000000000000000000000000000"
	ps, db := insertCharacterizationNode(t, pubkey)
	defer db.Close()

	pt4, pt5, pt6 := 4, 5, 6

	packets := []*StoreTx{
		{
			ID: 1, Hash: "c1", FirstSeen: "2026-07-31T10:00:00Z",
			PayloadType: &pt4, SNR: f64p(10.0), RSSI: f64p(-80.0),
			ObserverID: "obsX", ObserverName: "Observer X",
			PathJSON:    `["aa"]`,
			DecodedJSON: `{"sender_key":"peerAAA","sender_name":"PeerA"}`,
		},
		{
			ID: 2, Hash: "c2", FirstSeen: "2026-07-31T10:15:00Z",
			PayloadType: &pt4, SNR: f64p(20.0), RSSI: f64p(-70.0),
			ObserverID: "obsX", ObserverName: "Observer X",
			PathJSON:    `["aa","bb"]`,
			DecodedJSON: `{"sender_key":"peerAAA","sender_name":"PeerA-P2","recipient_key":"peerBBB","recipient_short_name":"PeerB-short"}`,
		},
		{
			ID: 3, Hash: "c3", FirstSeen: "2026-07-31T12:30:00Z",
			PayloadType: &pt5,
			ObserverID:  "obsY", ObserverName: "Observer Y",
			PathJSON:    `[]`,
			DecodedJSON: `{"pubkey":"` + pubkey + `","name":"SelfShouldBeExcluded"}`,
		},
		{
			ID: 4, Hash: "c4", FirstSeen: "2026-07-31T13:00:00Z",
			PayloadType: &pt4, SNR: f64p(30.0), RSSI: f64p(-60.0),
			ObserverID: "obsY", ObserverName: "Observer Y",
			PathJSON:    `["aa","bb","cc"]`,
			DecodedJSON: `{"pubkey":"peerCCC","name":"PeerC","recipient_key":"peerBBB","recipient_name":"PeerB-fromP4"}`,
		},
		{
			ID: 5, Hash: "c5", FirstSeen: "2026-07-31T13:30:00Z",
			PayloadType: &pt6, SNR: f64p(10.0), RSSI: f64p(-75.0),
			// No ObserverID: exercises the "excluded from observer
			// aggregation, still counted everywhere else" path.
			PathJSON:    `["aa","bb","cc","dd"]`,
			DecodedJSON: `{"sender_key":"peerAAA","sender_name":"PeerA-dup"}`,
		},
		{
			ID: 6, Hash: "c6", FirstSeen: "2026-07-31T13:45:00Z",
			PayloadType: &pt4, RSSI: f64p(-85.0),
			ObserverID: "obsX", ObserverName: "Observer X",
			PathJSON: `[]`,
			// DecodedJSON deliberately empty: exercises the "skip decode
			// entirely" path, distinct from P3's self-pubkey exclusion.
		},
	}
	loadPacketsIntoStore(ps, pubkey, packets)

	now := time.Date(2026, 7, 31, 15, 0, 0, 0, time.UTC)
	const days = 1

	full, err := ps.getNodeAnalyticsAt(pubkey, days, now)
	if err != nil {
		t.Fatalf("getNodeAnalyticsAt: %v", err)
	}

	// --- timeRange ---
	wantTimeRange := TimeRangeResp{From: "2026-07-30T15:00:00Z", To: "2026-07-31T15:00:00Z", Days: 1}
	if full.TimeRange != wantTimeRange {
		t.Errorf("TimeRange = %+v, want %+v", full.TimeRange, wantTimeRange)
	}

	// --- activityTimeline: bucket counts, chronological order ---
	b1, b2, b3 := "2026-07-31T10:00:00Z", "2026-07-31T12:00:00Z", "2026-07-31T13:00:00Z"
	wantTimeline := []TimeBucket{
		{Bucket: &b1, Count: 2},
		{Bucket: &b2, Count: 1},
		{Bucket: &b3, Count: 3},
	}
	if !reflect.DeepEqual(full.ActivityTimeline, wantTimeline) {
		t.Errorf("ActivityTimeline mismatch:\ngot:  %s\nwant: %s", dumpTimeBuckets(full.ActivityTimeline), dumpTimeBuckets(wantTimeline))
	}

	// --- snrTrend: packet-insertion order, only packets with SNR != nil ---
	wantSnrTrend := []SnrTrendEntry{
		{Timestamp: "2026-07-31T10:00:00Z", SNR: 10.0, RSSI: -80.0, ObserverID: "obsX", ObserverName: "Observer X"},
		{Timestamp: "2026-07-31T10:15:00Z", SNR: 20.0, RSSI: -70.0, ObserverID: "obsX", ObserverName: "Observer X"},
		{Timestamp: "2026-07-31T13:00:00Z", SNR: 30.0, RSSI: -60.0, ObserverID: "obsY", ObserverName: "Observer Y"},
		{Timestamp: "2026-07-31T13:30:00Z", SNR: 10.0, RSSI: -75.0, ObserverID: nil, ObserverName: nil},
	}
	if !reflect.DeepEqual(full.SnrTrend, wantSnrTrend) {
		t.Errorf("SnrTrend mismatch:\ngot:  %+v\nwant: %+v", full.SnrTrend, wantSnrTrend)
	}

	// --- packetTypeBreakdown: map-iteration order, sort before comparing ---
	wantTypes := []PayloadTypeCount{{PayloadType: 4, Count: 4}, {PayloadType: 5, Count: 1}, {PayloadType: 6, Count: 1}}
	gotTypes := append([]PayloadTypeCount(nil), full.PacketTypeBreakdown...)
	sort.Slice(gotTypes, func(i, j int) bool { return gotTypes[i].PayloadType < gotTypes[j].PayloadType })
	if !reflect.DeepEqual(gotTypes, wantTypes) {
		t.Errorf("PacketTypeBreakdown mismatch:\ngot:  %+v\nwant: %+v", gotTypes, wantTypes)
	}

	// --- observerCoverage: packetCount, avgSnr, avgRssi, firstSeen,
	// lastSeen, sorted descending by packetCount (the API's actual
	// promise) -- fixture keeps counts distinct (3 vs 2) so this is a
	// meaningful order assertion, not just a set comparison.
	obsXSnrAvg := (10.0 + 20.0) / 2.0            // P1, P2 (P6 has no SNR)
	obsXRssiAvg := (-80.0 + -70.0 + -85.0) / 3.0 // P1, P2, P6
	obsYSnrAvg := 30.0 / 1.0                     // P4 only (P3 has no SNR)
	obsYRssiAvg := -60.0 / 1.0                   // P4 only
	wantObservers := []NodeObserverStatsResp{
		{
			ObserverID: "obsX", ObserverName: "Observer X", PacketCount: 3,
			AvgSnr: obsXSnrAvg, AvgRssi: obsXRssiAvg,
			FirstSeen: "2026-07-31T10:00:00Z", LastSeen: "2026-07-31T13:45:00Z",
		},
		{
			ObserverID: "obsY", ObserverName: "Observer Y", PacketCount: 2,
			AvgSnr: obsYSnrAvg, AvgRssi: obsYRssiAvg,
			FirstSeen: "2026-07-31T12:30:00Z", LastSeen: "2026-07-31T13:00:00Z",
		},
	}
	if !reflect.DeepEqual(full.ObserverCoverage, wantObservers) {
		t.Errorf("ObserverCoverage mismatch:\ngot:  %+v\nwant: %+v", full.ObserverCoverage, wantObservers)
	}

	// --- hopDistribution: 0,1,2,3,4+ ---
	wantHops := []HopDistEntry{
		{Hops: "0", Count: 2},  // P3, P6
		{Hops: "1", Count: 1},  // P1
		{Hops: "2", Count: 1},  // P2
		{Hops: "3", Count: 1},  // P4
		{Hops: "4+", Count: 1}, // P5
	}
	if !reflect.DeepEqual(full.HopDistribution, wantHops) {
		t.Errorf("HopDistribution mismatch:\ngot:  %+v\nwant: %+v", full.HopDistribution, wantHops)
	}

	// --- peerInteractions: sender_key, recipient_key, pubkey extraction;
	// self-pubkey excluded (P3); descending count, no ties in this
	// fixture (3, 2, 1) so order is a meaningful assertion; name set only
	// at first sighting; lastContact tracks the latest occurrence.
	wantPeers := []PeerInteraction{
		{PeerKey: "peerAAA", PeerName: "PeerA", MessageCount: 3, LastContact: "2026-07-31T13:30:00Z"},       // P1,P2,P5 (sender_key)
		{PeerKey: "peerBBB", PeerName: "PeerB-short", MessageCount: 2, LastContact: "2026-07-31T13:00:00Z"}, // P2,P4 (recipient_key)
		{PeerKey: "peerCCC", PeerName: "PeerC", MessageCount: 1, LastContact: "2026-07-31T13:00:00Z"},       // P4 (pubkey field)
	}
	if !reflect.DeepEqual(full.PeerInteractions, wantPeers) {
		t.Errorf("PeerInteractions mismatch:\ngot:  %+v\nwant: %+v", full.PeerInteractions, wantPeers)
	}

	// --- uptimeHeatmap: UTC day/hour counts, map order -> sort before compare ---
	dow := int(mustParseRFC3339UTC(t, "2026-07-31T10:00:00Z").Weekday())
	wantHeatmap := []HeatmapCell{
		{DayOfWeek: dow, Hour: 10, Count: 2}, // P1,P2
		{DayOfWeek: dow, Hour: 12, Count: 1}, // P3
		{DayOfWeek: dow, Hour: 13, Count: 3}, // P4,P5,P6
	}
	gotHeatmap := append([]HeatmapCell(nil), full.UptimeHeatmap...)
	sort.Slice(gotHeatmap, func(i, j int) bool {
		if gotHeatmap[i].DayOfWeek != gotHeatmap[j].DayOfWeek {
			return gotHeatmap[i].DayOfWeek < gotHeatmap[j].DayOfWeek
		}
		return gotHeatmap[i].Hour < gotHeatmap[j].Hour
	})
	if !reflect.DeepEqual(gotHeatmap, wantHeatmap) {
		t.Errorf("UptimeHeatmap mismatch:\ngot:  %+v\nwant: %+v", gotHeatmap, wantHeatmap)
	}

	// --- computedStats: all 11 fields ---
	// availabilityPct: 3 distinct hour-buckets / 24 window-hours * 100 = 12.5
	// longestSilence: bucket10 -> bucket12 gap = 2h = 7,200,000ms (the
	// larger of the two gaps: 10->12 is 2h, 12->13 is 1h)
	// snrMean/StdDev (population, over 10,20,30,10): mean=17.5,
	// variance=68.75, stddev=sqrt(68.75)=8.29156...  -> rounds to 8.3
	// signalGrade: snrMean(17.5)>15 but snrStdDev(8.29)>=2 -> "A-"
	// relayPct: relayed(len>1 hops: P2,P4,P5)=3 / totalWithPath(P1,P2,P4,P5)=4 * 100 = 75.0
	wantStats := ComputedNodeStats{
		AvailabilityPct:     12.5,
		LongestSilenceMs:    7200000,
		LongestSilenceStart: "2026-07-31T10:00:00Z",
		SignalGrade:         "A-",
		SnrMean:             17.5,
		SnrStdDev:           8.3,
		RelayPct:            75.0,
		TotalPackets:        6,
		UniqueObservers:     2,
		UniquePeers:         3,
		AvgPacketsPerDay:    6.0,
	}
	if !reflect.DeepEqual(full.ComputedStats, wantStats) {
		t.Errorf("ComputedStats mismatch:\ngot:  %+v\nwant: %+v", full.ComputedStats, wantStats)
	}

	// Summary must agree exactly with full on ComputedStats (the whole
	// point of the shared accumulator), including the same uniquePeers/
	// uniqueObservers derivation.
	summary, err := ps.getNodeAnalyticsSummaryAt(pubkey, days, now)
	if err != nil {
		t.Fatalf("getNodeAnalyticsSummaryAt: %v", err)
	}
	if !reflect.DeepEqual(summary.ComputedStats, full.ComputedStats) {
		t.Errorf("summary ComputedStats diverges from full:\nfull:    %+v\nsummary: %+v", full.ComputedStats, summary.ComputedStats)
	}
}

func dumpTimeBuckets(bs []TimeBucket) string {
	out := "["
	for i, b := range bs {
		if i > 0 {
			out += " "
		}
		bucket := "<nil>"
		if b.Bucket != nil {
			bucket = *b.Bucket
		}
		out += fmt.Sprintf("{%s %d}", bucket, b.Count)
	}
	return out + "]"
}

// TestNodeAnalyticsPeerInteractions_Top20Cap preserves the existing (if
// unusual) semantics: PeerInteractions is capped at 20 entries, and
// ComputedStats.UniquePeers reports the length AFTER that cap (i.e. at
// most 20, never the true distinct-peer count above 20) -- for both the
// full endpoint and its summary sibling.
func TestNodeAnalyticsPeerInteractions_Top20Cap(t *testing.T) {
	pubkey := "char-cap-test-pk-0000000000000000000000000000000000000000"
	ps, db := insertCharacterizationNode(t, pubkey)
	defer db.Close()

	pt := 4
	base := time.Date(2026, 7, 31, 10, 0, 0, 0, time.UTC)
	var packets []*StoreTx
	for i := 0; i < 25; i++ {
		peerKey := fmt.Sprintf("peer%02d", i)
		decoded, err := json.Marshal(map[string]string{"sender_key": peerKey, "sender_name": peerKey})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		packets = append(packets, &StoreTx{
			ID:          i + 1,
			Hash:        fmt.Sprintf("cap-hash-%d", i),
			FirstSeen:   base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339),
			PayloadType: &pt,
			DecodedJSON: string(decoded),
		})
	}
	loadPacketsIntoStore(ps, pubkey, packets)

	now := time.Date(2026, 7, 31, 15, 0, 0, 0, time.UTC)

	full, err := ps.getNodeAnalyticsAt(pubkey, 1, now)
	if err != nil {
		t.Fatalf("getNodeAnalyticsAt: %v", err)
	}
	if len(full.PeerInteractions) != 20 {
		t.Errorf("PeerInteractions length = %d, want 20 (cap)", len(full.PeerInteractions))
	}
	if full.ComputedStats.UniquePeers != 20 {
		t.Errorf("full ComputedStats.UniquePeers = %d, want 20 (25 distinct peers, capped)", full.ComputedStats.UniquePeers)
	}

	summary, err := ps.getNodeAnalyticsSummaryAt(pubkey, 1, now)
	if err != nil {
		t.Fatalf("getNodeAnalyticsSummaryAt: %v", err)
	}
	if summary.ComputedStats.UniquePeers != 20 {
		t.Errorf("summary ComputedStats.UniquePeers = %d, want 20", summary.ComputedStats.UniquePeers)
	}
	if full.ComputedStats.UniquePeers != summary.ComputedStats.UniquePeers {
		t.Errorf("full/summary UniquePeers diverge: %d vs %d", full.ComputedStats.UniquePeers, summary.ComputedStats.UniquePeers)
	}
}

// TestNodeAnalyticsAccumulator_SnrStats exercises the Welford-based online
// SNR mean/stddev directly against the accumulator (fix for #2: no more
// unbounded per-packet snrValues slice). Uses Wikipedia's canonical
// population-vs-sample standard deviation example (population stddev 2 vs
// sample/N-1 stddev ~2.138) to catch an accidental switch to sample
// variance, plus large/near-identical values to exercise numerical
// stability -- Welford's running-deviation formulation is specifically
// chosen to avoid the catastrophic cancellation a naive sum-of-squares
// one-pass formula would suffer here.
func TestNodeAnalyticsAccumulator_SnrStats(t *testing.T) {
	cases := []struct {
		name       string
		snrs       []float64
		wantMean   float64
		wantStdDev float64
	}{
		{name: "zero values", snrs: nil, wantMean: 0, wantStdDev: 0},
		{name: "single value", snrs: []float64{12.3}, wantMean: 12.3, wantStdDev: 0},
		{
			name:       "population not sample stddev (wikipedia example)",
			snrs:       []float64{2, 4, 4, 4, 5, 5, 7, 9},
			wantMean:   5.0,
			wantStdDev: 2.0,
		},
		{
			name:       "large identical values (numerical stability)",
			snrs:       []float64{500000.0, 500000.0, 500000.0, 500000.0},
			wantMean:   500000.0,
			wantStdDev: 0.0,
		},
		{
			name:       "large nearby distinct values (numerical stability)",
			snrs:       []float64{500000.1, 500000.2, 500000.3},
			wantMean:   500000.2,
			wantStdDev: 0.1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			acc := newNodeAnalyticsAccumulator("pk", false)
			for _, v := range tc.snrs {
				v := v
				acc.accumulate(&StoreTx{FirstSeen: "2026-07-31T10:00:00Z", SNR: &v})
			}
			stats := acc.finalizeComputedStats(1)
			if stats.SnrMean != tc.wantMean {
				t.Errorf("SnrMean = %v, want %v", stats.SnrMean, tc.wantMean)
			}
			if stats.SnrStdDev != tc.wantStdDev {
				t.Errorf("SnrStdDev = %v, want %v", stats.SnrStdDev, tc.wantStdDev)
			}
		})
	}
}

// TestNodeAnalyticsAccumulator_SnrStatsMatchDisplayMode confirms the
// withDisplayArrays flag doesn't change SNR statistics -- full and summary
// must derive identical numbers from the same Welford state.
func TestNodeAnalyticsAccumulator_SnrStatsMatchDisplayMode(t *testing.T) {
	snrs := []float64{2, 4, 4, 4, 5, 5, 7, 9}

	accFull := newNodeAnalyticsAccumulator("pk", true)
	accSummary := newNodeAnalyticsAccumulator("pk", false)
	for _, v := range snrs {
		v := v
		accFull.accumulate(&StoreTx{FirstSeen: "2026-07-31T10:00:00Z", SNR: &v})
		accSummary.accumulate(&StoreTx{FirstSeen: "2026-07-31T10:00:00Z", SNR: &v})
	}

	full := accFull.finalizeComputedStats(1)
	summary := accSummary.finalizeComputedStats(1)
	if full.SnrMean != summary.SnrMean || full.SnrStdDev != summary.SnrStdDev {
		t.Errorf("SNR stats diverge between display modes: full={%v %v} summary={%v %v}",
			full.SnrMean, full.SnrStdDev, summary.SnrMean, summary.SnrStdDev)
	}
}
