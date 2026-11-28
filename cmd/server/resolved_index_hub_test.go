package main

import (
	"fmt"
	"testing"
	"time"
)

func newHubIndexStore() *PacketStore {
	s := &PacketStore{
		byNode:               make(map[string][]*StoreTx),
		nodeHashes:           newNodeSets(),
		useResolvedPathIndex: true,
	}
	s.initResolvedPathIndex()
	return s
}

// A busy repeater sits in most paths, so its forward list holds most txIDs.
// Re-adding a tx (a later observation of it) must not duplicate the pair, and
// the forward and reverse maps must agree.
func TestResolvedPubkeyIndex_HubDedupAcrossCalls(t *testing.T) {
	s := newHubIndexStore()
	const n = 500
	for id := 1; id <= n; id++ {
		s.addToResolvedPubkeyIndex(id, []string{"hub", fmt.Sprintf("leaf%d", id)})
	}
	// Second observations of every tx, the hub again plus one new pubkey.
	for id := 1; id <= n; id++ {
		s.addToResolvedPubkeyIndex(id, []string{"HUB", "hub", fmt.Sprintf("other%d", id)})
	}
	hub := resolvedPubkeyHash("hub")
	if got := len(s.resolvedPubkeyIndex[hub]); got != n {
		t.Fatalf("hub forward list has %d entries, want %d (no duplicates)", got, n)
	}
	for id := 1; id <= n; id++ {
		if got := len(s.resolvedPubkeyReverse[id]); got != 3 {
			t.Fatalf("tx %d reverse has %d hashes, want 3 (hub, leaf, other)", id, got)
		}
		if !s.txHasResolvedHash(id, hub) {
			t.Fatalf("tx %d: hub not found", id)
		}
	}
	if s.txHasResolvedHash(n+1, hub) {
		t.Fatal("unknown tx reported as containing the hub")
	}
	// Removing a tx and adding it back keeps one entry.
	s.removeFromResolvedPubkeyIndex(7)
	if s.txHasResolvedHash(7, hub) {
		t.Fatal("removed tx still contains the hub")
	}
	s.addToResolvedPubkeyIndex(7, []string{"hub"})
	s.addToResolvedPubkeyIndex(7, []string{"hub"})
	count := 0
	for _, id := range s.resolvedPubkeyIndex[hub] {
		if id == 7 {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("tx 7 appears %d times in the hub list after re-add, want 1", count)
	}
}

// Indexing must stay linear when every tx passes through the same repeaters.
// The duplicate check used to scan the repeater's whole forward list, so a
// startup load of a busy mesh went quadratic: 200K txs through three hubs is
// about 6e10 comparisons that way (tens of seconds), and well under a second
// with a per-tx check. The bound is generous so a slow CI runner still passes.
func TestResolvedPubkeyIndex_HubLoadIsLinear(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	s := newHubIndexStore()
	pks := []string{"hubA", "hubB", "hubC", ""}
	start := time.Now()
	for id := 1; id <= 200_000; id++ {
		pks[3] = fmt.Sprintf("leaf%d", id%5000)
		s.addToResolvedPubkeyIndex(id, pks)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("indexing 200K txs through 3 hubs took %v, want < 5s (quadratic duplicate check?)", d)
	}
	if got := len(s.resolvedPubkeyIndex[resolvedPubkeyHash("hubA")]); got != 200_000 {
		t.Fatalf("hubA has %d entries, want 200000", got)
	}
}

// BenchmarkAddToResolvedPubkeyIndex_Hub: cost per tx while the hub's forward
// list grows to size. Flat across sizes means linear indexing overall.
func BenchmarkAddToResolvedPubkeyIndex_Hub(b *testing.B) {
	for _, size := range []int{10_000, 50_000, 100_000} {
		b.Run(fmt.Sprintf("txs=%dK", size/1000), func(b *testing.B) {
			pks := []string{"hubA", "hubB", "leaf"}
			for i := 0; i < b.N; i++ {
				s := newHubIndexStore()
				for id := 1; id <= size; id++ {
					s.addToResolvedPubkeyIndex(id, pks)
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*size), "ns/tx")
		})
	}
}
