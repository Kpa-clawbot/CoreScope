package main

import (
	"encoding/json"
	"fmt"
	"testing"
)

// TestAdvertPubkeyTracking verifies that advertPubkeys is maintained
// incrementally during ingest and eviction, and that GetPerfStoreStats
// returns the correct count without per-request JSON parsing.
func TestAdvertPubkeyTracking(t *testing.T) {
	ps := NewPacketStore(nil, nil)
	ps.mu.Lock()

	// Helper to create an ADVERT StoreTx with a given pubkey.
	pt4 := 4
	mkAdvert := func(id int, pubkey string) *StoreTx {
		d := map[string]interface{}{"pubKey": pubkey}
		j, _ := json.Marshal(d)
		return &StoreTx{
			ID:          id,
			Hash:        fmt.Sprintf("hash%d", id),
			PayloadType: &pt4,
			DecodedJSON: string(j),
		}
	}

	// Add 3 adverts: 2 distinct pubkeys
	tx1 := mkAdvert(1, "pk_alpha")
	tx2 := mkAdvert(2, "pk_beta")
	tx3 := mkAdvert(3, "pk_alpha") // duplicate pubkey

	for _, tx := range []*StoreTx{tx1, tx2, tx3} {
		ps.packets = append(ps.packets, tx)
		ps.byHash[tx.Hash] = tx
		ps.byTxID[tx.ID] = tx
		ps.byPayloadType[4] = append(ps.byPayloadType[4], tx)
		ps.trackAdvertPubkey(tx)
	}
	ps.mu.Unlock()

	// GetPerfStoreStats should report 2 distinct pubkeys
	stats := ps.GetPerfStoreStats()
	indexes := stats["indexes"].(map[string]interface{})
	got := indexes["advertByObserver"].(int)
	if got != 2 {
		t.Errorf("advertByObserver = %d, want 2", got)
	}

	// GetPerfStoreStatsTyped should agree
	typed := ps.GetPerfStoreStatsTyped()
	if typed.Indexes.AdvertByObserver != 2 {
		t.Errorf("typed AdvertByObserver = %d, want 2", typed.Indexes.AdvertByObserver)
	}

	// Evict tx3 (pk_alpha duplicate) — count should stay 2
	ps.mu.Lock()
	ps.untrackAdvertPubkey(tx3)
	ps.mu.Unlock()

	stats2 := ps.GetPerfStoreStats()
	idx2 := stats2["indexes"].(map[string]interface{})
	if idx2["advertByObserver"].(int) != 2 {
		t.Errorf("after evicting duplicate: advertByObserver = %d, want 2", idx2["advertByObserver"].(int))
	}

	// Evict tx1 (last pk_alpha) — count should drop to 1
	ps.mu.Lock()
	ps.untrackAdvertPubkey(tx1)
	ps.mu.Unlock()

	stats3 := ps.GetPerfStoreStats()
	idx3 := stats3["indexes"].(map[string]interface{})
	if idx3["advertByObserver"].(int) != 1 {
		t.Errorf("after evicting last pk_alpha: advertByObserver = %d, want 1", idx3["advertByObserver"].(int))
	}

	// Evict tx2 (last remaining) — count should be 0
	ps.mu.Lock()
	ps.untrackAdvertPubkey(tx2)
	ps.mu.Unlock()

	stats4 := ps.GetPerfStoreStats()
	idx4 := stats4["indexes"].(map[string]interface{})
	if idx4["advertByObserver"].(int) != 0 {
		t.Errorf("after evicting all: advertByObserver = %d, want 0", idx4["advertByObserver"].(int))
	}
}

// TestAdvertPubkeyPublicKeyField tests the "public_key" JSON field variant.
func TestAdvertPubkeyPublicKeyField(t *testing.T) {
	ps := NewPacketStore(nil, nil)
	ps.mu.Lock()
	pt4 := 4
	d, _ := json.Marshal(map[string]interface{}{"public_key": "pk_legacy"})
	tx := &StoreTx{ID: 1, Hash: "h1", PayloadType: &pt4, DecodedJSON: string(d)}
	ps.trackAdvertPubkey(tx)
	ps.mu.Unlock()

	stats := ps.GetPerfStoreStats()
	idx := stats["indexes"].(map[string]interface{})
	if idx["advertByObserver"].(int) != 1 {
		t.Errorf("public_key field: advertByObserver = %d, want 1", idx["advertByObserver"].(int))
	}
}

// TestAdvertPubkeyNonAdvert ensures non-ADVERT packets don't affect the count.
func TestAdvertPubkeyNonAdvert(t *testing.T) {
	ps := NewPacketStore(nil, nil)
	ps.mu.Lock()
	pt2 := 2
	d, _ := json.Marshal(map[string]interface{}{"pubKey": "pk_text"})
	tx := &StoreTx{ID: 1, Hash: "h1", PayloadType: &pt2, DecodedJSON: string(d)}
	ps.trackAdvertPubkey(tx)
	ps.mu.Unlock()

	stats := ps.GetPerfStoreStats()
	idx := stats["indexes"].(map[string]interface{})
	if idx["advertByObserver"].(int) != 0 {
		t.Errorf("non-ADVERT should not be tracked: advertByObserver = %d, want 0", idx["advertByObserver"].(int))
	}
}

// BenchmarkGetPerfStoreStats benchmarks the perf stats endpoint with many adverts.
// Before the fix, this did O(N) JSON unmarshals per call.
// After the fix, it's O(1) — just len(map).
func BenchmarkGetPerfStoreStats(b *testing.B) {
	ps := NewPacketStore(nil, nil)
	ps.mu.Lock()
	pt4 := 4
	for i := 0; i < 5000; i++ {
		pk := fmt.Sprintf("pk_%04d", i%200) // 200 distinct pubkeys
		d, _ := json.Marshal(map[string]interface{}{"pubKey": pk})
		tx := &StoreTx{
			ID:          i + 1,
			Hash:        fmt.Sprintf("hash%d", i+1),
			PayloadType: &pt4,
			DecodedJSON: string(d),
		}
		ps.packets = append(ps.packets, tx)
		ps.byHash[tx.Hash] = tx
		ps.byTxID[tx.ID] = tx
		ps.byPayloadType[4] = append(ps.byPayloadType[4], tx)
		ps.trackAdvertPubkey(tx)
	}
	ps.mu.Unlock()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ps.GetPerfStoreStats()
	}
}

// BenchmarkGetPerfStoreStatsTyped benchmarks the typed variant.
func BenchmarkGetPerfStoreStatsTyped(b *testing.B) {
	ps := NewPacketStore(nil, nil)
	ps.mu.Lock()
	pt4 := 4
	for i := 0; i < 5000; i++ {
		pk := fmt.Sprintf("pk_%04d", i%200)
		d, _ := json.Marshal(map[string]interface{}{"pubKey": pk})
		tx := &StoreTx{
			ID:          i + 1,
			Hash:        fmt.Sprintf("hash%d", i+1),
			PayloadType: &pt4,
			DecodedJSON: string(d),
		}
		ps.packets = append(ps.packets, tx)
		ps.byHash[tx.Hash] = tx
		ps.byTxID[tx.ID] = tx
		ps.byPayloadType[4] = append(ps.byPayloadType[4], tx)
		ps.trackAdvertPubkey(tx)
	}
	ps.mu.Unlock()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ps.GetPerfStoreStatsTyped()
	}
}
