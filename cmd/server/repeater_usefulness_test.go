package main

import (
	"sync"
	"testing"
)

// TestRepeaterUsefulness_BasicShare verifies that usefulness_score is
// relay_count_24h / total_non_advert_traffic_24h. With 1 of 4 relayed
// packets going through the repeater, score should be 0.25.
//
// Issue #672. We are intentionally implementing the *traffic share*
// dimension of the composite score from the issue body — bridge,
// coverage, redundancy are deferred to follow-up work. This is the
// "Traffic" axis of the table in #672.
func TestRepeaterUsefulness_BasicShare(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	pubkey := "aabbccdd11223344"
	store := NewPacketStore(db, nil)

	// 4 non-advert packets total in last hour. The repeater appears in
	// the resolved path of exactly one of them.
	pt := 1
	for i := 0; i < 4; i++ {
		tx := &StoreTx{RawHex: "0100", PayloadType: &pt, FirstSeen: recentTS(0)}
		// Only first packet has our repeater in its path.
		if i == 0 {
			store.mu.Lock()
			tx.ID = len(store.packets) + 1
			tx.Hash = "uf-hit"
			store.packets = append(store.packets, tx)
			store.byHash[tx.Hash] = tx
			store.byTxID[tx.ID] = tx
			store.byPayloadType[pt] = append(store.byPayloadType[pt], tx)
			store.byPathHop[pubkey] = append(store.byPathHop[pubkey], tx)
			store.mu.Unlock()
		} else {
			addTestPacket(store, tx)
		}
	}

	score := store.GetRepeaterUsefulnessScore(pubkey)
	// 1 relay / 4 total = 0.25
	if score < 0.24 || score > 0.26 {
		t.Errorf("expected usefulness ~0.25, got %f", score)
	}
}

// TestRepeaterUsefulness_NoTraffic verifies score is 0 when there is
// no non-advert traffic to share.
func TestRepeaterUsefulness_NoTraffic(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()
	store := NewPacketStore(db, nil)
	score := store.GetRepeaterUsefulnessScore("deadbeefcafebabe")
	if score != 0 {
		t.Errorf("expected 0 for empty store, got %f", score)
	}
}

// TestRepeaterUsefulness_AdvertsExcluded verifies that ADVERT packets
// (payload_type=4) are excluded from both numerator and denominator —
// adverts don't count as forwarded traffic.
func TestRepeaterUsefulness_AdvertsExcluded(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	pubkey := "11aa22bb33cc44dd"
	store := NewPacketStore(db, nil)

	// 2 non-advert packets, both with our repeater in path → score = 1.0
	pt := 1
	for i := 0; i < 2; i++ {
		tx := &StoreTx{RawHex: "0100", PayloadType: &pt, FirstSeen: recentTS(0)}
		store.mu.Lock()
		tx.ID = len(store.packets) + 1
		tx.Hash = "uf-non-advert"
		if i == 1 {
			tx.Hash = "uf-non-advert-2"
		}
		store.packets = append(store.packets, tx)
		store.byHash[tx.Hash] = tx
		store.byTxID[tx.ID] = tx
		store.byPayloadType[pt] = append(store.byPayloadType[pt], tx)
		store.byPathHop[pubkey] = append(store.byPathHop[pubkey], tx)
		store.mu.Unlock()
	}
	// Add 100 adverts — these must be ignored.
	advertPT := payloadTypeAdvert
	for i := 0; i < 100; i++ {
		tx := &StoreTx{RawHex: "0400", PayloadType: &advertPT, FirstSeen: recentTS(0)}
		addTestPacket(store, tx)
	}

	score := store.GetRepeaterUsefulnessScore(pubkey)
	if score < 0.99 || score > 1.01 {
		t.Errorf("expected usefulness ~1.0 (adverts excluded), got %f", score)
	}
}

// TestGetRepeaterNodeStatsBatch_Parity verifies that GetRepeaterNodeStatsBatch
// returns the same usefulness score as GetRepeaterUsefulnessScore for a given
// pubkey, so both paths stay in sync.
func TestGetRepeaterNodeStatsBatch_Parity(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	pubkey := "aabbccdd11223344"
	store := NewPacketStore(db, nil)

	pt := 1
	for i := 0; i < 4; i++ {
		tx := &StoreTx{RawHex: "0100", PayloadType: &pt, FirstSeen: recentTS(0)}
		if i == 0 {
			store.mu.Lock()
			tx.ID = len(store.packets) + 1
			tx.Hash = "batch-parity-hit"
			store.packets = append(store.packets, tx)
			store.byHash[tx.Hash] = tx
			store.byTxID[tx.ID] = tx
			store.byPayloadType[pt] = append(store.byPayloadType[pt], tx)
			store.byPathHop[pubkey] = append(store.byPathHop[pubkey], tx)
			store.mu.Unlock()
		} else {
			addTestPacket(store, tx)
		}
	}

	want := store.GetRepeaterUsefulnessScore(pubkey)
	batch := store.GetRepeaterNodeStatsBatch([]string{pubkey}, 24)
	got := batch[pubkey].Score
	if got != want {
		t.Errorf("batch score %f != single score %f", got, want)
	}
}

// TestGetRepeaterNodeStatsBatch_EmptyInput verifies that an empty pubkey list
// returns an empty map without panicking.
func TestGetRepeaterNodeStatsBatch_EmptyInput(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()
	store := NewPacketStore(db, nil)
	result := store.GetRepeaterNodeStatsBatch([]string{}, 24)
	if len(result) != 0 {
		t.Errorf("expected empty map for empty input, got %d entries", len(result))
	}
}

// TestGetRepeaterNodeStatsBatch_MissingPubkey verifies that a pubkey with no
// indexed packets gets a zero score and empty relay info rather than panicking.
func TestGetRepeaterNodeStatsBatch_MissingPubkey(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()
	store := NewPacketStore(db, nil)

	pt := 1
	tx := &StoreTx{RawHex: "0100", PayloadType: &pt, FirstSeen: recentTS(0)}
	addTestPacket(store, tx)

	result := store.GetRepeaterNodeStatsBatch([]string{"deadbeefcafebabe"}, 24)
	stats := result["deadbeefcafebabe"]
	if stats.Score != 0 {
		t.Errorf("expected score 0 for unknown pubkey, got %f", stats.Score)
	}
	if stats.Info.RelayCount24h != 0 {
		t.Errorf("expected RelayCount24h 0 for unknown pubkey, got %d", stats.Info.RelayCount24h)
	}
}

// TestGetRepeaterNodeStatsBatch_Race verifies that concurrent calls to
// GetRepeaterNodeStatsBatch and GetRepeaterUsefulnessScore do not trigger
// the race detector — all StoreTx reads must happen under the store lock.
func TestGetRepeaterNodeStatsBatch_Race(t *testing.T) {
	db := setupCapabilityTestDB(t)
	defer db.conn.Close()

	pubkey := "aabbccdd11223344"
	store := NewPacketStore(db, nil)

	pt := 1
	for i := 0; i < 10; i++ {
		tx := &StoreTx{RawHex: "0100", PayloadType: &pt, FirstSeen: recentTS(0)}
		store.mu.Lock()
		tx.ID = len(store.packets) + 1
		tx.Hash = "race-" + string(rune('a'+i))
		store.packets = append(store.packets, tx)
		store.byHash[tx.Hash] = tx
		store.byTxID[tx.ID] = tx
		store.byPayloadType[pt] = append(store.byPayloadType[pt], tx)
		store.byPathHop[pubkey] = append(store.byPathHop[pubkey], tx)
		store.mu.Unlock()
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store.GetRepeaterNodeStatsBatch([]string{pubkey}, 24)
		}()
	}
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store.GetRepeaterUsefulnessScore(pubkey)
		}()
	}
	wg.Wait()
}
