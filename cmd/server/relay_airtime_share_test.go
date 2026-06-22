package main

import (
	"math"
	"strings"
	"testing"
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
	// without knowing the assumed SF/BW/CR).
	preset, ok := result["preset"].(map[string]interface{})
	if !ok {
		t.Fatalf("result['preset'] missing or wrong type: %T", result["preset"])
	}
	for _, key := range []string{"freq_hz", "bw_khz", "sf", "cr", "preamble"} {
		if _, ok := preset[key]; !ok {
			t.Errorf("result['preset'] missing %q: %+v", key, preset)
		}
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

	// True ToA acceptance bands. Wide enough (±0.2 pp) to absorb any
	// trivial rounding without admitting the byte proxy (which would
	// give ~95.24 %, 4.76 %).
	const wantAdvert = 83.4754
	const wantAck = 16.5246
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

func zeroPad(n, width int) string {
	s := ""
	for i := 0; i < width; i++ {
		s = string(rune('0'+(n%10))) + s
		n /= 10
	}
	return s
}
