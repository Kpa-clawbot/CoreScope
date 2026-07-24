package main

import (
	"math"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/lora"
)

// newRelayAirtimeShareTestStore builds a minimal PacketStore for testing
// computeRelayAirtimeShare without any DB or background workers.
func newRelayAirtimeShareTestStore(packets []*StoreTx) *PacketStore {
	ps := &PacketStore{
		packets:        packets,
		byHash:         make(map[string]*StoreTx),
		byTxID:         make(map[int]*StoreTx),
		byObsID:        make(map[int]*StoreObs),
		byObserver:     make(map[string][]*StoreObs),
		byNode:         make(map[string][]*StoreTx),
		byPathHop:      make(map[string][]*StoreTx),
		nodeHashes:     make(map[string]map[string]bool),
		byPayloadType:  make(map[int][]*StoreTx),
		rfCache:        make(map[string]*cachedResult),
		topoCache:      make(map[string]*cachedResult),
		hashCache:      make(map[string]*cachedResult),
		collisionCache: make(map[string]*cachedResult),
		chanCache:      make(map[string]*cachedResult),
		distCache:      make(map[string]*cachedResult),
		subpathCache:   make(map[string]*cachedResult),
		spIndex:        make(map[string]int),
		spTxIndex:      make(map[string][]*StoreTx),
		advertPubkeys:  make(map[string]int),
	}
	ps.useResolvedPathIndex = true
	ps.initResolvedPathIndex()
	for _, tx := range packets {
		ps.byTxID[tx.ID] = tx
		if tx.Hash != "" {
			ps.byHash[tx.Hash] = tx
		}
		if tx.PayloadType != nil {
			pt := *tx.PayloadType
			ps.byPayloadType[pt] = append(ps.byPayloadType[pt], tx)
		}
	}
	return ps
}

// makeRelayAirtimeTx builds a synthetic transmission with rawHex sized for the
// given byte count.
func makeRelayAirtimeTx(id int, payloadType int, payloadBytes int, distinctRelays int, hashPrefix string) *StoreTx {
	pt := payloadType
	return &StoreTx{
		ID:          id,
		Hash:        hashPrefix,
		FirstSeen:   "2026-01-01T00:00:00Z",
		PayloadType: &pt,
		RawHex:      strings.Repeat("ab", payloadBytes),
	}
}

// TestRelayAirtimeShare_ADVERTvsACKDivergence (issue #1768 v2 of #1359 test):
//   - 1 ADVERT, 200 B, 8 distinct relays
//   - 1000 ACKs,  10 B, 0  distinct relays
//
// The ACK score is still 0 (no relays); ADVERT carries 100 % of airtime.
// The headline divergence (ADVERT ranks #1 by airtime despite tiny count
// share) survives the switch from byte-proxy to true ToA. Adds a check
// that the JSON response surfaces the active preset (issue #1768 requires
// the preset in the caption, so it must reach the client).
func TestRelayAirtimeShare_ADVERTvsACKDivergence(t *testing.T) {
	packets := make([]*StoreTx, 0, 1001)
	advert := makeRelayAirtimeTx(1, PayloadADVERT, 200, 8, "ad000001")
	packets = append(packets, advert)
	for i := 0; i < 1000; i++ {
		ack := makeRelayAirtimeTx(100+i, PayloadACK, 10, 0, "")
		ack.Hash = "ac" + zeroPad(i, 6)
		packets = append(packets, ack)
	}
	store := newRelayAirtimeShareTestStore(packets)
	relayPks := []string{"r01", "r02", "r03", "r04", "r05", "r06", "r07", "r08"}
	store.addToResolvedPubkeyIndex(advert.ID, relayPks)

	if got := store.distinctRelayCount(advert); got != 8 {
		t.Fatalf("distinctRelayCount(ADVERT) = %d, want 8", got)
	}

	result := store.computeRelayAirtimeShare(TimeWindow{})

	// New: preset must be in the response so the client can render the
	// caption per issue #1768 (caller cannot interpret "Airtime %"
	// without knowing the assumed SF/BW/CR). The shape is a typed
	// presetResponse struct (PR #1776 review round-1: no
	// map[string]interface{} in API surfaces).
	preset, ok := result["preset"].(presetResponse)
	if !ok {
		t.Fatalf("result['preset'] missing or wrong type: %T", result["preset"])
	}
	if preset.SF == 0 || preset.BWkHz == 0 || preset.CR == 0 || preset.Preamble == 0 || preset.FreqHz == 0 {
		t.Errorf("result['preset'] has zero fields: %+v", preset)
	}

	rows, ok := result["rows"].([]map[string]interface{})
	if !ok || len(rows) < 2 {
		t.Fatalf("unexpected rows: %T %+v", result["rows"], result["rows"])
	}
	byType := make(map[string]map[string]interface{})
	for _, r := range rows {
		name, _ := r["payload_type"].(string)
		byType[name] = r
	}
	advertRow := byType["ADVERT"]
	ackRow := byType["ACK"]
	if advertRow == nil || ackRow == nil {
		t.Fatalf("missing rows: %+v", rows)
	}

	advertAirtimePct, _ := advertRow["airtime_pct"].(float64)
	ackAirtimePct, _ := ackRow["airtime_pct"].(float64)
	if advertAirtimePct < 99.5 || advertAirtimePct > 100.001 {
		t.Errorf("ADVERT airtime_pct = %.4f, want 100.0", advertAirtimePct)
	}
	if ackAirtimePct != 0.0 {
		t.Errorf("ACK airtime_pct = %.4f, want 0.0", ackAirtimePct)
	}

	// Count-side assertions prove the divergence story: ACK dominates
	// by raw packet count (≈99.9%) but ADVERT dominates by airtime
	// (100%). Without these the test only proves half the chart.
	advertCount, _ := advertRow["count"].(int)
	ackCount, _ := ackRow["count"].(int)
	if advertCount != 1 {
		t.Errorf("ADVERT count = %d, want 1", advertCount)
	}
	if ackCount != 1000 {
		t.Errorf("ACK count = %d, want 1000", ackCount)
	}
	advertCountPct, _ := advertRow["count_pct"].(float64)
	ackCountPct, _ := ackRow["count_pct"].(float64)
	// 1 of 1001 = 0.0999 %; 1000 of 1001 = 99.9001 %.
	if math.Abs(advertCountPct-(1.0/1001.0*100.0)) > 0.001 {
		t.Errorf("ADVERT count_pct = %.4f, want %.4f", advertCountPct, 1.0/1001.0*100.0)
	}
	if math.Abs(ackCountPct-(1000.0/1001.0*100.0)) > 0.001 {
		t.Errorf("ACK count_pct = %.4f, want %.4f", ackCountPct, 1000.0/1001.0*100.0)
	}
	// The headline: ACK is ≫ ADVERT by count but 0 by airtime. That
	// inversion is the whole reason the dumbbell exists.
	if !(ackCountPct > advertCountPct && advertAirtimePct > ackAirtimePct) {
		t.Errorf("expected count/airtime inversion: ackCountPct=%.4f advertCountPct=%.4f advertAirtimePct=%.4f ackAirtimePct=%.4f",
			ackCountPct, advertCountPct, advertAirtimePct, ackAirtimePct)
	}
	if rows[0]["payload_type"] != "ADVERT" {
		t.Errorf("rows[0] = %v, want ADVERT (sort by airtime desc)", rows[0]["payload_type"])
	}
}

// TestRelayAirtimeShare_ToAReplacesByteProxy is the issue #1768 acceptance
// gate: airtime_pct must follow true LoRa Time-on-Air, NOT bytes.
//
// Setup: 1 ADVERT (200 B, 1 relay) and 1 ACK (10 B, 1 relay) with the
// default EU preset (869.6 MHz / BW 62.5 kHz / SF 8 / CR 4/5,
// preamble 32 per firmware preambleLengthForSF).
//
// Old byte-proxy would give ADVERT 200/(200+10) = 95.24 %.
// True ToA per #1768 closed form:
//
//	T_sym = 256 / 62500 = 4.096 ms
//	ADVERT (PL=200): symbols = 36.25 + (8 + ceil((1600-32+44)/32)*5)
//	               = 36.25 + (8 + 51*5) = 299.25 → 1225.728 ms
//	ACK    (PL=10):  symbols = 36.25 + (8 + ceil((80-32+44)/32)*5)
//	               = 36.25 + (8 + 3*5) = 59.25 → 242.688 ms
//	ADVERT share = 1225.728 / (1225.728 + 242.688) = 0.83476 → 83.48 %
//
// 83 % vs 95 % is the whole point: small frames are no longer crushed
// against large ones because the additive preamble + fixed-overhead
// intercept finally enters the score. A test that still passes against
// the byte proxy would fail to gate the regression — we explicitly
// assert away from 95 %.
func TestRelayAirtimeShare_ToAReplacesByteProxy(t *testing.T) {
	advert := makeRelayAirtimeTx(1, PayloadADVERT, 200, 1, "ad000001")
	ack := makeRelayAirtimeTx(2, PayloadACK, 10, 1, "ac000001")

	store := newRelayAirtimeShareTestStore([]*StoreTx{advert, ack})
	store.addToResolvedPubkeyIndex(advert.ID, []string{"relay-A"})
	store.addToResolvedPubkeyIndex(ack.ID, []string{"relay-B"})

	result := store.computeRelayAirtimeShare(TimeWindow{})
	rows, ok := result["rows"].([]map[string]interface{})
	if !ok || len(rows) != 2 {
		t.Fatalf("unexpected rows: %T %+v", result["rows"], result["rows"])
	}
	byType := make(map[string]map[string]interface{})
	for _, r := range rows {
		name, _ := r["payload_type"].(string)
		byType[name] = r
	}
	advertPct, _ := byType["ADVERT"]["airtime_pct"].(float64)
	ackPct, _ := byType["ACK"]["airtime_pct"].(float64)

	// True ToA acceptance bands derived INDEPENDENTLY from the
	// implementation under test, by computing each row's ToA via
	// lora.TimeOnAir on the same preset and forming the share by hand.
	// (The prior `wantAck = 16.5246` constant was just `100 - wantAdvert`
	// — a tautology that would silently pass if shares stopped summing
	// to 100. Driving from lora.TimeOnAir means a regression there
	// surfaces here.)
	preset := defaultLoRaPreset()
	advertToA := float64(lora.TimeOnAir(200, preset))
	ackToA := float64(lora.TimeOnAir(10, preset))
	wantAdvert := advertToA / (advertToA + ackToA) * 100.0
	wantAck := ackToA / (advertToA + ackToA) * 100.0
	// Sanity guards on the independent calculation. Tolerances ±0.05 pp
	// around the AN1200.13 hand-computed values keep this honest if the
	// upstream lora package ever drifts.
	if math.Abs(wantAdvert-83.4754) > 0.05 {
		t.Fatalf("independent ADVERT ToA share drifted: got %.4f want ~83.4754", wantAdvert)
	}
	if math.Abs(wantAck-16.5246) > 0.05 {
		t.Fatalf("independent ACK ToA share drifted: got %.4f want ~16.5246", wantAck)
	}
	if math.Abs(advertPct-wantAdvert) > 0.2 {
		t.Errorf("ADVERT airtime_pct = %.4f, want %.4f (true ToA)", advertPct, wantAdvert)
	}
	if math.Abs(ackPct-wantAck) > 0.2 {
		t.Errorf("ACK airtime_pct = %.4f, want %.4f (true ToA)", ackPct, wantAck)
	}

	// Negative gate: the OLD byte-proxy answer must NOT come back.
	// 95 % vs 5 % means we're still on the bytes×relays code path.
	if advertPct > 90.0 {
		t.Errorf("ADVERT airtime_pct = %.4f looks like byte proxy (≈95.24); ToA path missing", advertPct)
	}
}

// TestAirtimeForTransmissions covers the wardriving-session use case: an
// arbitrary caller-supplied set of transmission IDs (not a time window),
// summing ToA × distinct-relay-count per transmission.
func TestAirtimeForTransmissions(t *testing.T) {
	tx1 := makeRelayAirtimeTx(601, PayloadGRP_TXT, 20, 3, "wd001")
	tx2 := makeRelayAirtimeTx(602, PayloadGRP_TXT, 20, 2, "wd002")
	tx3 := makeRelayAirtimeTx(603, PayloadGRP_TXT, 20, 0, "wd003") // never relayed — contributes 0
	store := newRelayAirtimeShareTestStore([]*StoreTx{tx1, tx2, tx3})
	store.addToResolvedPubkeyIndex(tx1.ID, []string{"r1", "r2", "r3"})
	store.addToResolvedPubkeyIndex(tx2.ID, []string{"r1", "r2"})

	total, ok := store.AirtimeForTransmissions([]int64{int64(tx1.ID), int64(tx2.ID), int64(tx3.ID)})
	if !ok {
		t.Fatal("AirtimeForTransmissions ok=false, want true")
	}

	preset := store.resolveLoRaPreset()
	toa := lora.TimeOnAir(20, preset)
	want := toa*3 + toa*2 // tx3 contributes toa*0 = 0
	if total != want {
		t.Errorf("total = %v, want %v", total, want)
	}

	// A session that ONLY contains the never-relayed message must airtime 0,
	// not be mistaken for "unknown".
	onlyTx3, ok := store.AirtimeForTransmissions([]int64{int64(tx3.ID)})
	if !ok || onlyTx3 != 0 {
		t.Errorf("AirtimeForTransmissions(tx3 only) = %v, ok=%v, want 0, true", onlyTx3, ok)
	}
}

// TestAirtimeForTransmissions_UnknownIDs confirms that when NONE of the
// requested transmission IDs are held in memory (e.g. evicted from the
// memory-bounded store despite still being in the SQL retention window),
// the result is ok=false — a silent 0 would look like "genuinely never
// relayed" instead of "don't know".
func TestAirtimeForTransmissions_UnknownIDs(t *testing.T) {
	store := newRelayAirtimeShareTestStore(nil)
	total, ok := store.AirtimeForTransmissions([]int64{999})
	if ok {
		t.Fatal("ok = true, want false — no requested transmission was found in memory")
	}
	if total != 0 {
		t.Errorf("total = %v, want 0", total)
	}
}

// TestAirtimeForTransmissions_PartialMatch confirms a partial match (some
// transmissions found, some evicted) still returns ok=true with the known
// subset's total — the alternative (require ALL to match) would make the
// feature return nothing once retention exceeds the in-memory window.
func TestAirtimeForTransmissions_PartialMatch(t *testing.T) {
	tx1 := makeRelayAirtimeTx(701, PayloadGRP_TXT, 20, 3, "pm1")
	store := newRelayAirtimeShareTestStore([]*StoreTx{tx1})
	store.addToResolvedPubkeyIndex(tx1.ID, []string{"r1", "r2", "r3"})

	total, ok := store.AirtimeForTransmissions([]int64{int64(tx1.ID), 999})
	if !ok {
		t.Fatal("ok = false, want true — at least one transmission (tx1) was found")
	}
	preset := store.resolveLoRaPreset()
	want := lora.TimeOnAir(20, preset) * 3
	if total != want {
		t.Errorf("total = %v, want %v (evicted id 999 contributes nothing, not an error)", total, want)
	}
}

// TestAirtimeForTransmissions_IndexDisabled confirms ok=false (omit the
// field) rather than a misleading zero when the resolved-path index isn't
// available at all.
func TestAirtimeForTransmissions_IndexDisabled(t *testing.T) {
	store := newRelayAirtimeShareTestStore(nil)
	store.useResolvedPathIndex = false
	if _, ok := store.AirtimeForTransmissions([]int64{1}); ok {
		t.Error("ok = true, want false when the resolved-path index is disabled")
	}
}

// TestAirtimeForTransmissions_EmptyInput confirms an empty ID list (e.g. a
// session that somehow has zero transmissions) returns ok=false rather
// than a vacuous zero.
func TestAirtimeForTransmissions_EmptyInput(t *testing.T) {
	store := newRelayAirtimeShareTestStore(nil)
	if _, ok := store.AirtimeForTransmissions(nil); ok {
		t.Error("ok = true, want false for an empty transmission-ID list")
	}
}

func zeroPad(n, width int) string {
	s := ""
	for i := 0; i < width; i++ {
		s = string(rune('0'+(n%10))) + s
		n /= 10
	}
	return s
}
