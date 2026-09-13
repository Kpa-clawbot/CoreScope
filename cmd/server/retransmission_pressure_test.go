package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
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

// A hop prefix is not a node identity. Across observations the same prefix
// is taken to be the same repeater (lower bound). Inside ONE path the same
// prefix twice must be two nodes: a repeater forwards a flood once
// (firmware Mesh.cpp wasSeen/markSeen before routeRecvPacket).
func TestRepeaterUnion_AmbiguousPrefixes(t *testing.T) {
	if got := unionOf(`["12","34","12"]`); got != 3 {
		t.Errorf("same prefix twice in one path = %d, want 3", got)
	}
	if got := unionOf(`["12","34"]`, `["56","12"]`); got != 3 {
		t.Errorf("same prefix in two paths = %d, want 3 (merged, lower bound)", got)
	}
	if got := unionOf(`["12","34","12"]`, `["12"]`, `["12","12","12"]`); got != 4 {
		t.Errorf("max multiplicity across paths = %d, want 4 (12 x3 + 34)", got)
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
		tx.Observations = append(tx.Observations, o)
	}
	return tx
}

func rtxObs(observer, path string) *StoreObs {
	return &StoreObs{ObserverID: observer, PathJSON: path}
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
func BenchmarkComputeRetransmissionPressure(b *testing.B) {
	const nTx, obsPerTx = 50000, 20
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	packets := make([]*StoreTx, 0, nTx)
	for i := 0; i < nTx; i++ {
		obs := make([]*StoreObs, 0, obsPerTx)
		for j := 0; j < obsPerTx; j++ {
			hops := make([]string, 0, 6)
			for h := 0; h < 3+(i+j)%4; h++ {
				hops = append(hops, fmt.Sprintf("%04X", (i*7+h*131+j*(h+1))%4096))
			}
			pj, _ := json.Marshal(hops)
			obs = append(obs, &StoreObs{ObserverID: fmt.Sprintf("obs%02d", j*3%60), PathJSON: string(pj)})
		}
		packets = append(packets, rtxTx(i+1, RouteFlood, PayloadADVERT,
			base.Add(time.Duration(i)*6*time.Second).Format(time.RFC3339), obs...))
	}
	s := rtxStore(packets...)
	runtime.GC() // fixture garbage must not be collected inside the timed loop
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.computeRetransmissionPressure("", TimeWindow{}, time.Hour)
	}
}
