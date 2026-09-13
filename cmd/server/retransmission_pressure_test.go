package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- repeaterUnion: the per-transmission count (issue #1699) ---

func unionOf(paths ...string) int {
	var u repeaterUnion
	u.reset()
	for _, p := range paths {
		u.addPath(p)
	}
	return u.count()
}

// The reporter's worked example from the #1699 thread: observer A saw
// [A] and [A,B,C], observer B saw [A,D]. Four repeaters took part.
func TestRepeaterUnion_ReporterExample(t *testing.T) {
	got := unionOf(`["A1"]`, `["A1","B2","C3"]`, `["A1","D4"]`)
	if got != 4 {
		t.Fatalf("union = %d, want 4", got)
	}
}

func TestRepeaterUnion_OverlappingPathsCountOnce(t *testing.T) {
	got := unionOf(`["FD35","95F8","3363"]`, `["FD35","95F8","3363"]`, `["FD35","95F8"]`, `["FD35","95F8","4F47"]`)
	if got != 4 {
		t.Fatalf("union = %d, want 4 (FD35, 95F8, 3363, 4F47)", got)
	}
}

// A hop prefix counts once per flood, wherever and however often it appears.
// Inside one path a repeated 2- or 3-byte prefix is on live data almost
// always one known node forwarding twice (the firmware dedup table is a
// cyclic 160-slot buffer, SimpleMeshTables.h:9,52-57), so it is one
// repeater. A repeated 1-byte prefix may be two nodes, but counting it once
// keeps the value a lower bound, like the cross-observation merge.
func TestRepeaterUnion_PrefixCountsOncePerFlood(t *testing.T) {
	if got := unionOf(`["12","34","12"]`); got != 2 {
		t.Errorf("1-byte prefix twice in one path = %d, want 2", got)
	}
	if got := unionOf(`["AB01","CD02","AB01"]`); got != 2 {
		t.Errorf("2-byte prefix twice in one path = %d, want 2", got)
	}
	if got := unionOf(`["AB01C3","CD02D4","AB01C3"]`); got != 2 {
		t.Errorf("3-byte prefix twice in one path = %d, want 2", got)
	}
	if got := unionOf(`["12","34"]`, `["56","12"]`); got != 3 {
		t.Errorf("same prefix in two paths = %d, want 3 (merged, lower bound)", got)
	}
	if got := unionOf(`["12","34","12"]`, `["12"]`, `["12","12","12"]`); got != 2 {
		t.Errorf("repeats within and across paths = %d, want 2 (12, 34)", got)
	}
}

func TestRepeaterUnion_HashSizeAndCaseAreDistinctKeys(t *testing.T) {
	if got := unionOf(`["ab"]`, `["AB"]`); got != 1 {
		t.Errorf("case variants = %d, want 1", got)
	}
	// A 1-byte "AB" and a 2-byte "AB00" are different hash widths.
	if got := unionOf(`["AB"]`, `["AB00"]`); got != 2 {
		t.Errorf("1-byte vs 2-byte = %d, want 2", got)
	}
}

func TestRepeaterUnion_EmptyAndMalformed(t *testing.T) {
	if got := unionOf("", `[]`); got != 0 {
		t.Errorf("empty paths = %d, want 0", got)
	}
	if got := unionOf(`["ZZ","A1"]`); got != 1 {
		t.Errorf("non-hex hop must be ignored, got %d want 1", got)
	}
}

func TestRepeaterUnion_ResetClearsState(t *testing.T) {
	var u repeaterUnion
	u.reset()
	u.addPath(`["A1","B2"]`)
	u.reset()
	u.addPath(`["C3"]`)
	if got := u.count(); got != 1 {
		t.Fatalf("after reset count = %d, want 1", got)
	}
}

func TestRepeaterUnion_GrowsPastInitialTable(t *testing.T) {
	var u repeaterUnion
	u.reset()
	const distinct = 1000 // well past repeaterUnionMinSlots/2
	for i := 0; i < distinct; i += 4 {
		u.addPath(fmt.Sprintf(`["%04X","%04X","%04X","%04X"]`, i, i+1, i+2, i+3))
	}
	u.addPath(`["0000","0001"]`) // already present, must not add
	if got := u.count(); got != distinct {
		t.Fatalf("count after growth = %d, want %d", got, distinct)
	}
	u.reset()
	u.addPath(`["0000","ABCD"]`)
	if got := u.count(); got != 2 {
		t.Fatalf("count after reset on grown table = %d, want 2", got)
	}
}

// --- computeRetransmissionPressure ---

func rtxInt(v int) *int { return &v }

func rtxTx(id int, route, payload int, firstSeen string, obs ...*StoreObs) *StoreTx {
	tx := &StoreTx{
		ID:          id,
		Hash:        fmt.Sprintf("h%06d", id),
		FirstSeen:   firstSeen,
		RouteType:   rtxInt(route),
		PayloadType: rtxInt(payload),
	}
	for _, o := range obs {
		o.TransmissionID = id
		if o.Timestamp == "" {
			o.Timestamp = firstSeen
		}
		tx.Observations = append(tx.Observations, o)
	}
	return tx
}

// rtxObs is an observation heard at the transmission's first_seen.
func rtxObs(observer, path string) *StoreObs {
	return &StoreObs{ObserverID: observer, PathJSON: path}
}

func rtxObsAt(observer, path, ts string) *StoreObs {
	return &StoreObs{ObserverID: observer, PathJSON: path, Timestamp: ts}
}

func rtxStore(packets ...*StoreTx) *PacketStore {
	return &PacketStore{packets: packets}
}

func bucketByStart(t *testing.T, r RetransmissionResponse, start string) RetransmissionBucket {
	t.Helper()
	for _, b := range r.Buckets {
		if b.Start == start {
			return b
		}
	}
	t.Fatalf("no bucket %s in %+v", start, r.Buckets)
	return RetransmissionBucket{}
}

func TestComputeRetransmissionPressure_RouteAndPayloadFilter(t *testing.T) {
	ts := "2026-09-13T10:10:00Z"
	s := rtxStore(
		rtxTx(1, RouteFlood, PayloadADVERT, ts, rtxObs("o1", `["A1","B2"]`), rtxObs("o2", `["A1","C3"]`)),
		rtxTx(2, RouteTransportFlood, PayloadGRP_TXT, ts, rtxObs("o1", `["D4"]`)),
		// Flood heard only straight from the originator: a packet with 0
		// observed repeaters, kept in the denominator.
		rtxTx(3, RouteFlood, PayloadGRP_TXT, ts, rtxObs("o1", `[]`)),
		// Direct routes carry the remaining route, not the forwarders.
		rtxTx(4, RouteDirect, PayloadTXT_MSG, ts, rtxObs("o1", `["E5","F6"]`)),
		rtxTx(5, RouteTransportDirect, PayloadTXT_MSG, ts, rtxObs("o1", `["E5"]`)),
		// TRACE path bytes are SNR values, never forwarder hashes.
		rtxTx(6, RouteFlood, PayloadTRACE, ts, rtxObs("o1", `["E5","F6"]`)),
		// Missing route type cannot be classified.
		&StoreTx{ID: 7, FirstSeen: ts, Observations: []*StoreObs{rtxObs("o1", `["E5"]`)}},
	)
	r := s.computeRetransmissionPressure("", TimeWindow{}, time.Hour)
	if r.BucketSeconds != 3600 {
		t.Errorf("bucket_seconds = %d, want 3600", r.BucketSeconds)
	}
	if r.Summary.Packets != 3 {
		t.Fatalf("summary packets = %d, want 3 (tx 1,2,3)", r.Summary.Packets)
	}
	b := bucketByStart(t, r, "2026-09-13T10:00:00Z")
	if b.Packets != 3 || b.RepeaterSum != 4 {
		t.Fatalf("bucket = %+v, want packets 3 repeater_sum 4 (3+1+0)", b)
	}
	if want := 4.0 / 3.0; b.AvgRepeaters < want-1e-9 || b.AvgRepeaters > want+1e-9 {
		t.Errorf("avg_repeaters = %v, want %v", b.AvgRepeaters, want)
	}
	if b.Observers != 2 {
		t.Errorf("observers = %d, want 2", b.Observers)
	}
	if r.Summary.NoRepeaterPackets != 1 {
		t.Errorf("no_repeater_packets = %d, want 1", r.Summary.NoRepeaterPackets)
	}
}

func TestComputeRetransmissionPressure_Bucketing(t *testing.T) {
	s := rtxStore(
		rtxTx(1, RouteFlood, PayloadADVERT, "2026-09-13T10:05:00Z", rtxObs("o1", `["A1","B2"]`)),
		rtxTx(2, RouteFlood, PayloadADVERT, "2026-09-13T10:55:59Z", rtxObs("o2", `["A1"]`)),
		rtxTx(3, RouteFlood, PayloadADVERT, "2026-09-13T11:00:00Z", rtxObs("o1", `["A1","B2","C3","D4"]`)),
		rtxTx(4, RouteFlood, PayloadADVERT, "not-a-time", rtxObs("o1", `["A1"]`)),
	)
	r := s.computeRetransmissionPressure("", TimeWindow{}, time.Hour)
	if len(r.Buckets) != 2 {
		t.Fatalf("buckets = %+v, want 2", r.Buckets)
	}
	if r.Buckets[0].Start != "2026-09-13T10:00:00Z" || r.Buckets[1].Start != "2026-09-13T11:00:00Z" {
		t.Fatalf("bucket order/starts wrong: %+v", r.Buckets)
	}
	b10 := r.Buckets[0]
	if b10.Packets != 2 || b10.RepeaterSum != 3 || b10.AvgRepeaters != 1.5 || b10.Observers != 2 {
		t.Errorf("10:00 bucket = %+v, want packets 2 sum 3 avg 1.5 observers 2", b10)
	}
	b11 := r.Buckets[1]
	if b11.Packets != 1 || b11.RepeaterSum != 4 || b11.Observers != 1 {
		t.Errorf("11:00 bucket = %+v", b11)
	}
	if r.Summary.Packets != 3 || r.Summary.Observers != 2 {
		t.Errorf("summary = %+v, want packets 3 observers 2", r.Summary)
	}
	if want := 7.0 / 3.0; r.Summary.AvgRepeaters < want-1e-9 || r.Summary.AvgRepeaters > want+1e-9 {
		t.Errorf("summary avg = %v, want %v", r.Summary.AvgRepeaters, want)
	}

	r15 := s.computeRetransmissionPressure("", TimeWindow{}, 15*time.Minute)
	if len(r15.Buckets) != 3 {
		t.Fatalf("15m buckets = %+v, want 3", r15.Buckets)
	}
	bucketByStart(t, r15, "2026-09-13T10:00:00Z")
	bucketByStart(t, r15, "2026-09-13T10:45:00Z")
	bucketByStart(t, r15, "2026-09-13T11:00:00Z")
	if r15.BucketSeconds != 900 {
		t.Errorf("bucket_seconds = %d, want 900", r15.BucketSeconds)
	}
}

func TestComputeRetransmissionPressure_WindowFilter(t *testing.T) {
	s := rtxStore(
		rtxTx(1, RouteFlood, PayloadADVERT, "2026-09-12T10:00:00Z", rtxObs("o1", `["A1"]`)),
		rtxTx(2, RouteFlood, PayloadADVERT, "2026-09-13T10:00:00Z", rtxObs("o1", `["A1","B2"]`)),
		rtxTx(3, RouteFlood, PayloadADVERT, "2026-09-14T10:00:00Z", rtxObs("o1", `["A1","B2","C3"]`)),
	)
	w := TimeWindow{Since: "2026-09-13T00:00:00Z", Until: "2026-09-13T23:59:59Z"}
	r := s.computeRetransmissionPressure("", w, time.Hour)
	if r.Summary.Packets != 1 || len(r.Buckets) != 1 || r.Buckets[0].RepeaterSum != 2 {
		t.Fatalf("window result = %+v, want only tx 2", r)
	}
}

// transmissions.hash is UNIQUE, so a re-hearing of the same content hours
// later is appended to the existing transmission. Each flood event is
// counted on its own, in the bucket of its first observation. Observations
// are listed newest first, the order the cold load appends them in.
func TestComputeRetransmissionPressure_SplitsFloodEvents(t *testing.T) {
	s := rtxStore(rtxTx(1, RouteFlood, PayloadGRP_TXT, "2026-09-13T10:00:00Z",
		rtxObsAt("o3", `["D4"]`, "2026-09-13T12:00:00.000Z"),
		rtxObsAt("o2", `["A1","C3"]`, "2026-09-13T10:00:20.000Z"),
		rtxObsAt("o1", `["A1","B2"]`, "2026-09-13T10:00:00.000Z"),
	))
	r := s.computeRetransmissionPressure("", TimeWindow{}, time.Hour)
	if r.Summary.Packets != 2 || len(r.Buckets) != 2 {
		t.Fatalf("result = %+v, want 2 flood events in 2 buckets", r)
	}
	if b := bucketByStart(t, r, "2026-09-13T10:00:00Z"); b.Packets != 1 || b.RepeaterSum != 3 || b.Observers != 2 {
		t.Errorf("10:00 bucket = %+v, want packets 1 sum 3 (A1,B2,C3) observers 2", b)
	}
	if b := bucketByStart(t, r, "2026-09-13T12:00:00Z"); b.Packets != 1 || b.RepeaterSum != 1 || b.Observers != 1 {
		t.Errorf("12:00 bucket = %+v, want packets 1 sum 1 (D4) observers 1", b)
	}
	if r.Summary.AvgRepeaters != 2 {
		t.Errorf("summary avg = %v, want 2", r.Summary.AvgRepeaters)
	}
}

// An event ends when the gap to the previous observation exceeds the settle
// time (5 minutes). The gap is measured observation to observation, so a
// slow flood whose steps each stay under it remains one event.
func TestComputeRetransmissionPressure_EventSettleGap(t *testing.T) {
	events := func(ts ...string) int {
		var obs []*StoreObs
		for i, x := range ts {
			obs = append(obs, rtxObsAt(fmt.Sprintf("o%d", i), `["A1"]`, x))
		}
		return rtxStore(rtxTx(1, RouteFlood, PayloadADVERT, ts[0], obs...)).
			computeRetransmissionPressure("", TimeWindow{}, time.Hour).Summary.Packets
	}
	if got := events("2026-09-13T10:00:00Z", "2026-09-13T10:05:00Z"); got != 1 {
		t.Errorf("gap of exactly 5m = %d events, want 1", got)
	}
	if got := events("2026-09-13T10:00:00Z", "2026-09-13T10:05:01Z"); got != 2 {
		t.Errorf("gap of 5m01s = %d events, want 2", got)
	}
	if got := events("2026-09-13T10:00:00Z", "2026-09-13T10:04:00Z", "2026-09-13T10:08:00Z", "2026-09-13T10:12:00Z"); got != 1 {
		t.Errorf("4m steps over 12m = %d events, want 1", got)
	}
}

// The window selects flood events by their first observation, not by the
// transmission's first_seen: a hash first heard weeks ago and flooded again
// today counts today.
func TestComputeRetransmissionPressure_WindowSelectsEvents(t *testing.T) {
	s := rtxStore(rtxTx(1, RouteFlood, PayloadADVERT, "2026-08-20T10:00:00Z",
		rtxObsAt("o1", `["A1","B2"]`, "2026-08-20T10:00:00Z"),
		rtxObsAt("o1", `["C3"]`, "2026-09-13T10:00:00Z"),
	))
	r := s.computeRetransmissionPressure("", TimeWindow{Since: "2026-09-13T00:00:00Z"}, time.Hour)
	if r.Summary.Packets != 1 || len(r.Buckets) != 1 || r.Buckets[0].RepeaterSum != 1 {
		t.Fatalf("result = %+v, want only the 2026-09-13 event (C3)", r)
	}
}

// Flood events that start before the store's retention floor are dropped,
// for every request shape: the store keeps old observations only for hashes
// that were heard again recently, so what it holds before the floor is not
// the traffic of that period.
func TestComputeRetransmissionPressure_RetentionFloor(t *testing.T) {
	now := time.Now().UTC()
	ago := func(d time.Duration) string { return now.Add(-d).Format("2006-01-02T15:04:05.000Z") }
	s := rtxStore(
		// Weeks-old first event, recent second event: only the second counts.
		rtxTx(1, RouteFlood, PayloadADVERT, ago(30*24*time.Hour),
			rtxObsAt("o1", `["A1","B2"]`, ago(30*24*time.Hour)),
			rtxObsAt("o1", `["C3"]`, ago(time.Hour))),
		// An event that starts before the floor is dropped as a whole, not
		// counted from its first observation after the floor.
		rtxTx(2, RouteFlood, PayloadADVERT, ago(24*time.Hour+time.Minute),
			rtxObsAt("o1", `["D4"]`, ago(24*time.Hour+time.Minute)),
			rtxObsAt("o2", `["E5"]`, ago(24*time.Hour-time.Minute))),
		rtxTx(3, RouteFlood, PayloadADVERT, ago(2*time.Hour), rtxObsAt("o1", `["F6","A7"]`, ago(2*time.Hour))),
	)
	s.retentionHours = 24
	r := s.computeRetransmissionPressure("", TimeWindow{}, time.Hour)
	if r.Summary.Packets != 2 || r.Summary.AvgRepeaters != 1.5 {
		t.Fatalf("summary = %+v, want 2 events (C3; F6,A7) avg 1.5", r.Summary)
	}
	if got := s.computeRetransmissionPressure("", TimeWindow{Since: ago(40 * 24 * time.Hour)}, time.Hour).Summary.Packets; got != 2 {
		t.Errorf("window reaching past the floor = %d events, want 2", got)
	}
	s.retentionHours = 0
	if got := s.computeRetransmissionPressure("", TimeWindow{}, time.Hour).Summary.Packets; got != 4 {
		t.Errorf("unlimited retention = %d events, want 4", got)
	}
}

func TestComputeRetransmissionPressure_RegionFilter(t *testing.T) {
	s := rtxStore(
		rtxTx(1, RouteFlood, PayloadADVERT, "2026-09-13T10:00:00Z",
			rtxObs("brussels", `["A1","B2"]`), rtxObs("amsterdam", `["A1","C3","D4"]`)),
		rtxTx(2, RouteFlood, PayloadADVERT, "2026-09-13T10:10:00Z",
			rtxObs("amsterdam", `["E5"]`)),
	)
	s.regionObsCache = map[string]map[string]bool{"BRU": {"brussels": true}}
	s.regionObsCacheTime = time.Now()

	r := s.computeRetransmissionPressure("BRU", TimeWindow{}, time.Hour)
	if r.Region != "BRU" {
		t.Errorf("region = %q, want BRU", r.Region)
	}
	if r.Summary.Packets != 1 {
		t.Fatalf("packets = %d, want 1 (tx 2 has no BRU observation)", r.Summary.Packets)
	}
	if got := r.Buckets[0].RepeaterSum; got != 2 {
		t.Errorf("repeater_sum = %d, want 2 (only the brussels path counts)", got)
	}
	if got := r.Buckets[0].Observers; got != 1 {
		t.Errorf("observers = %d, want 1", got)
	}
}

// Events are split on all observations before the region filter, so a flood
// the region heard at its start and end stays one event even when the
// region's own observations are further apart than the settle time.
func TestComputeRetransmissionPressure_RegionDoesNotSplitEvents(t *testing.T) {
	s := rtxStore(rtxTx(1, RouteFlood, PayloadADVERT, "2026-09-13T10:00:00Z",
		rtxObsAt("brussels", `["A1"]`, "2026-09-13T10:00:00Z"),
		rtxObsAt("amsterdam", `["B2"]`, "2026-09-13T10:04:00Z"),
		rtxObsAt("brussels", `["A1","C3"]`, "2026-09-13T10:08:00Z"),
	))
	s.regionObsCache = map[string]map[string]bool{"BRU": {"brussels": true}}
	s.regionObsCacheTime = time.Now()

	r := s.computeRetransmissionPressure("BRU", TimeWindow{}, time.Hour)
	if r.Summary.Packets != 1 || r.Buckets[0].RepeaterSum != 2 {
		t.Fatalf("region result = %+v, want 1 event with A1, C3", r)
	}
}

// A region with no known observers is not filtered, the same as
// /api/analytics/rf and the other analytics endpoints (resolveRegionObservers
// returns nil). Pinned so a change is a deliberate, documented one.
func TestComputeRetransmissionPressure_UnknownRegionIsNotFiltered(t *testing.T) {
	s := rtxStore(rtxTx(1, RouteFlood, PayloadADVERT, "2026-09-13T10:00:00Z",
		rtxObs("brussels", `["A1","B2"]`), rtxObs("amsterdam", `["C3"]`)))
	s.regionObsCache = map[string]map[string]bool{"XXX": nil}
	s.regionObsCacheTime = time.Now()

	r := s.computeRetransmissionPressure("XXX", TimeWindow{}, time.Hour)
	if r.Summary.Packets != 1 || r.Summary.Observers != 2 || r.Buckets[0].RepeaterSum != 3 {
		t.Fatalf("unknown region = %+v, want the network-wide result", r)
	}
}

func TestComputeRetransmissionPressure_OneByteShare(t *testing.T) {
	ts := "2026-09-13T10:00:00Z"
	s := rtxStore(
		rtxTx(1, RouteFlood, PayloadADVERT, ts, rtxObs("o1", `["A1","B2"]`)),
		rtxTx(2, RouteFlood, PayloadADVERT, ts, rtxObs("o1", `["A1B2","C3D4"]`)),
		rtxTx(3, RouteFlood, PayloadADVERT, ts, rtxObs("o1", `[]`), rtxObs("o2", `["A1B2C3"]`)),
		rtxTx(4, RouteFlood, PayloadADVERT, ts, rtxObs("o1", `[]`)),
	)
	r := s.computeRetransmissionPressure("", TimeWindow{}, time.Hour)
	if r.Summary.OneBytePackets != 1 {
		t.Errorf("one_byte_packets = %d, want 1", r.Summary.OneBytePackets)
	}
}

func TestComputeRetransmissionPressure_EmptyStoreHasNonNilBuckets(t *testing.T) {
	r := rtxStore().computeRetransmissionPressure("", TimeWindow{}, time.Hour)
	b, _ := json.Marshal(r)
	if !strings.Contains(string(b), `"buckets":[]`) {
		t.Fatalf("empty result must encode buckets as [], got %s", b)
	}
}

func TestParseRetransmissionBucket(t *testing.T) {
	cases := map[string]time.Duration{
		"":    time.Hour,
		"5m":  5 * time.Minute,
		"15m": 15 * time.Minute,
		"1h":  time.Hour,
		"6h":  6 * time.Hour,
		"1d":  24 * time.Hour,
		"7m":  time.Hour,
		"abc": time.Hour,
	}
	for in, want := range cases {
		if got := parseRetransmissionBucket(in); got != want {
			t.Errorf("parseRetransmissionBucket(%q) = %v, want %v", in, got, want)
		}
	}
}

// --- caching ---

func TestGetRetransmissionPressure_DefaultShapeServedFromRecomputer(t *testing.T) {
	s := rtxStore(rtxTx(1, RouteFlood, PayloadADVERT, "2026-09-13T10:00:00Z", rtxObs("o1", `["A1"]`)))
	sentinel := RetransmissionResponse{BucketSeconds: 3600, Buckets: []RetransmissionBucket{}, Window: "sentinel"}
	rc := newAnalyticsRecomputer("retransmissions", time.Hour, func() interface{} { return sentinel })
	rc.runOnce()
	s.recompRetransmissions = rc

	if got := s.GetRetransmissionPressure("", TimeWindow{}, time.Hour); got.Window != "sentinel" {
		t.Fatalf("default shape must come from the recomputer snapshot, got %+v", got)
	}
	if got := s.GetRetransmissionPressure("", TimeWindow{}, 15*time.Minute); got.Window == "sentinel" {
		t.Fatalf("non-default bucket must not be served from the default snapshot")
	}
}

func TestGetRetransmissionPressure_TTLCacheAndInvalidation(t *testing.T) {
	s := rtxStore(rtxTx(1, RouteFlood, PayloadADVERT, "2026-09-13T10:00:00Z", rtxObs("o1", `["A1"]`)))
	s.rfCacheTTL = time.Hour
	w := TimeWindow{Since: "2026-09-13T00:00:00Z", Label: "fixture"}

	first := s.GetRetransmissionPressure("", w, time.Hour)
	if first.Summary.Packets != 1 {
		t.Fatalf("first = %+v", first)
	}
	// Mutate the store behind the cache: a cached read must not see it.
	s.packets = append(s.packets, rtxTx(2, RouteFlood, PayloadADVERT, "2026-09-13T10:30:00Z", rtxObs("o1", `["B2"]`)))
	if got := s.GetRetransmissionPressure("", w, time.Hour); got.Summary.Packets != 1 {
		t.Fatalf("expected cache hit with 1 packet, got %d", got.Summary.Packets)
	}
	s.applyCacheInvalidation(cacheInvalidation{hasNewPaths: true})
	if got := s.GetRetransmissionPressure("", w, time.Hour); got.Summary.Packets != 2 {
		t.Fatalf("after hasNewPaths invalidation expected 2 packets, got %d", got.Summary.Packets)
	}
}

func TestGetRetransmissionPressure_EvictionClearsCache(t *testing.T) {
	s := rtxStore(rtxTx(1, RouteFlood, PayloadADVERT, "2026-09-13T10:00:00Z", rtxObs("o1", `["A1"]`)))
	s.rfCacheTTL = time.Hour
	w := TimeWindow{Since: "2026-09-13T00:00:00Z", Label: "fixture"}

	s.GetRetransmissionPressure("", w, time.Hour)
	s.packets = append(s.packets, rtxTx(2, RouteFlood, PayloadADVERT, "2026-09-13T10:30:00Z", rtxObs("o1", `["B2"]`)))
	s.invalidateCachesFor(cacheInvalidation{eviction: true})
	if got := s.GetRetransmissionPressure("", w, time.Hour); got.Summary.Packets != 2 {
		t.Fatalf("after eviction invalidation expected 2 packets, got %d", got.Summary.Packets)
	}
}

func TestGetRetransmissionPressure_CacheEntryExpires(t *testing.T) {
	s := rtxStore(rtxTx(1, RouteFlood, PayloadADVERT, "2026-09-13T10:00:00Z", rtxObs("o1", `["A1"]`)))
	s.rfCacheTTL = time.Hour
	w := TimeWindow{Since: "2026-09-13T00:00:00Z", Label: "fixture"}

	s.GetRetransmissionPressure("", w, time.Hour)
	s.packets = append(s.packets, rtxTx(2, RouteFlood, PayloadADVERT, "2026-09-13T10:30:00Z", rtxObs("o1", `["B2"]`)))
	if got := s.GetRetransmissionPressure("", w, time.Hour); got.Summary.Packets != 1 {
		t.Fatalf("fresh entry must be served from cache, got %d packets", got.Summary.Packets)
	}
	if len(s.retransCache) != 1 {
		t.Fatalf("cache entries = %d, want 1", len(s.retransCache))
	}
	for _, e := range s.retransCache {
		e.expiresAt = time.Now().Add(-time.Second)
	}
	if got := s.GetRetransmissionPressure("", w, time.Hour); got.Summary.Packets != 2 {
		t.Fatalf("expired entry must be recomputed, got %d packets", got.Summary.Packets)
	}
}

// Concurrent requests for the same uncached shape share one compute: every
// caller gets the result of that single pass (same bucket backing array).
func TestGetRetransmissionPressure_CollapsesConcurrentMisses(t *testing.T) {
	s := rtxStore(rtxTx(1, RouteFlood, PayloadADVERT, "2026-09-13T10:00:00Z", rtxObs("o1", `["A1"]`)))
	s.rfCacheTTL = time.Hour
	w := TimeWindow{Since: "2026-09-13T00:00:00Z", Label: "fixture"}

	const n = 16
	results := make([]RetransmissionResponse, n)
	var wg sync.WaitGroup
	s.mu.Lock() // park every compute on the store lock
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = s.GetRetransmissionPressure("", w, time.Hour)
		}(i)
	}
	time.Sleep(100 * time.Millisecond)
	s.mu.Unlock()
	wg.Wait()
	for i, r := range results {
		if len(r.Buckets) != 1 {
			t.Fatalf("result %d = %+v", i, r)
		}
		if &r.Buckets[0] != &results[0].Buckets[0] {
			t.Fatalf("result %d came from a separate compute; concurrent misses must share one", i)
		}
	}
}

// StartAnalyticsRecomputers must wire the #1659 readiness gate on the
// retransmissions recomputer: a pass before the cold load completes keeps
// the default shape at 503.
func TestStartAnalyticsRecomputers_RetransmissionsGatedOnLoadComplete(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	stop := store.StartAnalyticsRecomputers(time.Hour)
	defer stop()

	if !store.recompRetransmissions.IsWarmingUp_1659() {
		t.Fatal("a pass before LoadComplete must not open the retransmissions gate")
	}
	store.signalStartupLoadDone()
	store.recompRetransmissions.runOnce()
	if store.recompRetransmissions.IsWarmingUp_1659() {
		t.Fatal("a pass after LoadComplete must open the retransmissions gate")
	}
}

// --- handler ---

func TestHandleAnalyticsRetransmissions(t *testing.T) {
	_, router := setupTestServer(t)
	req := httptest.NewRequest("GET", "/api/analytics/retransmissions?bucket=1d", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", w.Code, w.Body.String())
	}
	var body RetransmissionResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, w.Body.String())
	}
	if body.BucketSeconds != 86400 {
		t.Errorf("bucket_seconds = %d, want 86400", body.BucketSeconds)
	}
	// Seed: tx1 flood ADVERT paths ["aa","bb"] + ["aa"] = 2 repeaters,
	// tx2 flood GRP_TXT path [] = 0, tx3 flood ADVERT ["cc"] = 1.
	if body.Summary.Packets != 3 {
		t.Fatalf("summary.packets = %d, want 3 (%s)", body.Summary.Packets, w.Body.String())
	}
	sum := 0
	for _, b := range body.Buckets {
		sum += b.RepeaterSum
	}
	if sum != 3 {
		t.Errorf("total repeater_sum = %d, want 3", sum)
	}
	for _, key := range []string{`"bucket_seconds"`, `"summary"`, `"avg_repeaters"`, `"observers"`, `"one_byte_packets"`, `"no_repeater_packets"`, `"buckets"`} {
		if !strings.Contains(w.Body.String(), key) {
			t.Errorf("response missing %s: %s", key, w.Body.String())
		}
	}
}

func TestHandleAnalyticsRetransmissions_WarmupGate(t *testing.T) {
	srv, router := setupTestServer(t)
	rc := newAnalyticsRecomputer("retransmissions", time.Hour, func() interface{} { return nil })
	rc.noteWarmupStart_1659()
	rc.setWarmupReadyGate_1659(func() bool { return false })
	srv.store.recompRetransmissions = rc

	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/analytics/retransmissions", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("default shape during warmup: status = %d, want 503", w.Code)
	}
	w = httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest("GET", "/api/analytics/retransmissions?window=24h", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("windowed shape bypasses the gate: status = %d, want 200", w.Code)
	}
}

// --- benchmark (perf proof for AGENTS.md rule 0) ---

// BenchmarkComputeRetransmissionPressure sizes the fixture on live
// magnitudes (2026-09-13): ~15k flood transmissions/day, ~20 observations
// each, ~5.3 hops per path. 50k tx x 20 obs = 1M observations, roughly
// 3.3 days of flood traffic; a 14-day store scales linearly (x4.3).
//
// Observations carry timestamps in the store's format, newest first per
// transmission, which is the order the cold load appends them in
// (ORDER BY o.timestamp DESC).
func retransmissionBenchStore() *PacketStore {
	const nTx, obsPerTx = 50000, 20
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	packets := make([]*StoreTx, 0, nTx)
	for i := 0; i < nTx; i++ {
		first := base.Add(time.Duration(i) * 6 * time.Second)
		obs := make([]*StoreObs, 0, obsPerTx)
		for j := 0; j < obsPerTx; j++ {
			hops := make([]string, 0, 6)
			for h := 0; h < 3+(i+j)%4; h++ {
				hops = append(hops, fmt.Sprintf("%04X", (i*7+h*131+j*(h+1))%4096))
			}
			pj, _ := json.Marshal(hops)
			ts := first.Add(time.Duration(obsPerTx-1-j) * time.Second).Format("2006-01-02T15:04:05.000Z")
			obs = append(obs, &StoreObs{ObserverID: fmt.Sprintf("obs%02d", j*3%60), PathJSON: string(pj), Timestamp: ts})
		}
		packets = append(packets, rtxTx(i+1, RouteFlood, PayloadADVERT, first.Format(time.RFC3339), obs...))
	}
	return rtxStore(packets...)
}

// BenchmarkComputeRetransmissionPressure measures a recompute pass on a
// store whose observation timestamps were already parsed (the steady state:
// StoreObs.ParsedTime caches per observation).
func BenchmarkComputeRetransmissionPressure(b *testing.B) {
	s := retransmissionBenchStore()
	s.computeRetransmissionPressure("", TimeWindow{}, time.Hour)
	runtime.GC() // fixture garbage must not be collected inside the timed loop
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.computeRetransmissionPressure("", TimeWindow{}, time.Hour)
	}
}

// BenchmarkComputeRetransmissionPressureColdTimestamps measures the first
// pass after startup, when no observation timestamp has been parsed yet.
func BenchmarkComputeRetransmissionPressureColdTimestamps(b *testing.B) {
	s := retransmissionBenchStore()
	runtime.GC()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		for _, tx := range s.packets {
			for _, o := range tx.Observations {
				o.tsParseOnce = sync.Once{}
				o.tsParsed, o.tsParsedOK = time.Time{}, false
			}
		}
		b.StartTimer()
		s.computeRetransmissionPressure("", TimeWindow{}, time.Hour)
	}
}
