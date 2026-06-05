package main

import (
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
		subpathCache:  make(map[string]*cachedResult),
		spIndex:       make(map[string]int),
		spTxIndex:     make(map[string][]*StoreTx),
		advertPubkeys: make(map[string]int),
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
// given byte count and registers `distinctRelays` synthetic resolved-path
// pubkeys via the resolved-pubkey reverse index — same source that
// distinctRelayCount must read from.
func makeRelayAirtimeTx(id int, payloadType int, payloadBytes int, distinctRelays int, hashPrefix string) *StoreTx {
	pt := payloadType
	tx := &StoreTx{
		ID:          id,
		Hash:        hashPrefix,
		FirstSeen:   "2026-01-01T00:00:00Z",
		PayloadType: &pt,
		RawHex:      strings.Repeat("ab", payloadBytes), // 2 hex chars per byte
	}
	return tx
}

// TestRelayAirtimeShare_ADVERTvsACKDivergence is the locked acceptance test
// from issue #1359:
//   - 1 ADVERT, 200 B, 8 distinct relays  →  score = 200 * 8 = 1600
//   - 1000 ACKs, 10 B each, 0 relays      →  score = 0
//
// Count distribution: ACK 1000/1001 = 99.90%, ADVERT 0.10%.
// Airtime distribution: ADVERT 1600/1600 = 100%, ACK 0%.
//
// This is the headline divergence the dumbbell chart must visualize.
func TestRelayAirtimeShare_ADVERTvsACKDivergence(t *testing.T) {
	packets := make([]*StoreTx, 0, 1001)

	// 1 ADVERT with 200 bytes payload + 8 distinct relays
	advert := makeRelayAirtimeTx(1, PayloadADVERT, 200, 8, "ad000001")
	packets = append(packets, advert)

	// 1000 ACKs with 10 bytes payload + 0 relays
	for i := 0; i < 1000; i++ {
		ack := makeRelayAirtimeTx(100+i, PayloadACK, 10, 0, "")
		// Give each a unique hash so dedup doesn't collapse them.
		ack.Hash = "ac" + zeroPad(i, 6)
		packets = append(packets, ack)
	}

	store := newRelayAirtimeShareTestStore(packets)

	// Wire up the 8 distinct relay pubkeys for the ADVERT through the
	// resolved-pubkey reverse index — the helper distinctRelayCount must
	// read from this source (union across all observations of tx.ID).
	relayPks := []string{
		"relay01", "relay02", "relay03", "relay04",
		"relay05", "relay06", "relay07", "relay08",
	}
	store.addToResolvedPubkeyIndex(advert.ID, relayPks)

	// Sanity check the helper directly.
	if got := store.distinctRelayCount(advert); got != 8 {
		t.Fatalf("distinctRelayCount(ADVERT) = %d, want 8", got)
	}
	if got := store.distinctRelayCount(packets[1]); got != 0 {
		t.Fatalf("distinctRelayCount(ACK) = %d, want 0", got)
	}

	result := store.computeRelayAirtimeShare(TimeWindow{})
	rows, ok := result["rows"].([]map[string]interface{})
	if !ok {
		t.Fatalf("result['rows'] missing or wrong type: %T", result["rows"])
	}
	if len(rows) < 2 {
		t.Fatalf("expected at least 2 rows (ADVERT, ACK), got %d: %+v", len(rows), rows)
	}

	// Index by payload_type name.
	byType := make(map[string]map[string]interface{})
	for _, r := range rows {
		name, _ := r["payload_type"].(string)
		byType[name] = r
	}

	advertRow, hasAdvert := byType["ADVERT"]
	ackRow, hasACK := byType["ACK"]
	if !hasAdvert {
		t.Fatalf("rows missing ADVERT bucket: %+v", rows)
	}
	if !hasACK {
		t.Fatalf("rows missing ACK bucket: %+v", rows)
	}

	// Count percentages: ACK should be ~99.9%, ADVERT ~0.1%.
	ackCountPct, _ := ackRow["count_pct"].(float64)
	advertCountPct, _ := advertRow["count_pct"].(float64)
	if !(ackCountPct > 99.0 && ackCountPct < 100.0) {
		t.Errorf("ACK count_pct = %.4f, want ~99.9", ackCountPct)
	}
	if !(advertCountPct < 1.0 && advertCountPct > 0.0) {
		t.Errorf("ADVERT count_pct = %.4f, want ~0.1", advertCountPct)
	}

	// Airtime percentages: ADVERT should be 100%, ACK 0%.
	advertAirtimePct, _ := advertRow["airtime_pct"].(float64)
	ackAirtimePct, _ := ackRow["airtime_pct"].(float64)
	if advertAirtimePct < 99.5 || advertAirtimePct > 100.001 {
		t.Errorf("ADVERT airtime_pct = %.4f, want 100.0", advertAirtimePct)
	}
	if ackAirtimePct != 0.0 {
		t.Errorf("ACK airtime_pct = %.4f, want 0.0", ackAirtimePct)
	}

	// Raw score check: ADVERT = 200 * 8 = 1600.
	advertScore, _ := advertRow["score"].(int)
	if advertScore != 1600 {
		t.Errorf("ADVERT score = %d, want 1600 (200B × 8 relays)", advertScore)
	}
	ackScore, _ := ackRow["score"].(int)
	if ackScore != 0 {
		t.Errorf("ACK score = %d, want 0 (no relays)", ackScore)
	}

	// Count integer check.
	advertCount, _ := advertRow["count"].(int)
	if advertCount != 1 {
		t.Errorf("ADVERT count = %d, want 1", advertCount)
	}
	ackCount, _ := ackRow["count"].(int)
	if ackCount != 1000 {
		t.Errorf("ACK count = %d, want 1000", ackCount)
	}

	// The divergence: ADVERT should rank #1 by airtime even though its
	// count share is the smallest. This is the whole point of the chart.
	if rows[0]["payload_type"] != "ADVERT" {
		t.Errorf("rows must be sorted by airtime_pct desc; rows[0] payload_type = %v, want ADVERT", rows[0]["payload_type"])
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
