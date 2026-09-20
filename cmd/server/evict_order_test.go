package main

import (
	"fmt"
	"testing"
	"time"
)

// s.packets is declared "sorted by first_seen ASC (oldest first; newest at
// tail)" (store.go:177), and retention eviction depends on it: it walks from
// the head and stops at the first transmission inside the window, so a slice
// out of order is under-evicted silently rather than noisily wrong.
//
// The background chunk loader broke that. Chunks are windowed on last_seen, so
// a transmission first heard weeks ago and heard again recently rides in with
// a recent chunk carrying its old first_seen; the chunk was put in front with
// `append(localPackets, s.packets...)` and never re-sorted, so a later, older
// chunk prepended in front of it left that ancient row behind newer ones.
//
// Not a corner case: on a production database 2071 of the 236080 transmissions
// in a 14 day window have a first_seen more than a day older than their
// last_seen, 1848 of them more than a week.

// mergeChunkIntoPackets is what keeps that invariant true, so pin it directly.
func TestMergeChunkIntoPackets_KeepsFirstSeenOrder(t *testing.T) {
	at := func(h int) string { return time.Now().UTC().Add(-time.Duration(h) * time.Hour).Format(time.RFC3339) }

	existing := []*StoreTx{
		{ID: 2, Hash: "b", FirstSeen: at(30)},
		{ID: 4, Hash: "d", FirstSeen: at(10)},
	}
	// Interleaves with what is already loaded, and is deliberately not in
	// order: the chunk query sorts, but the merge must not depend on it.
	chunk := []*StoreTx{
		{ID: 3, Hash: "c", FirstSeen: at(20)},
		{ID: 1, Hash: "a", FirstSeen: at(40)},
		{ID: 5, Hash: "e", FirstSeen: at(5)},
	}

	merged := mergeChunkIntoPackets(chunk, existing)

	if len(merged) != 5 {
		t.Fatalf("merged %d packets, want 5", len(merged))
	}
	got := ""
	for _, tx := range merged {
		got += tx.Hash
	}
	if got != "abcde" {
		t.Fatalf("merge order = %q, want %q", got, "abcde")
	}
	for i := 1; i < len(merged); i++ {
		if merged[i-1].FirstSeen > merged[i].FirstSeen {
			t.Fatalf("merge left the slice out of order at index %d", i)
		}
	}
}

// The merge runs under s.mu once per chunk, so it must stay linear. This
// guards against it being "simplified" back into a sort of the whole slice,
// which on a loaded instance means sorting hundreds of thousands of packets
// while ingest waits for the lock.
func BenchmarkMergeChunkIntoPackets(b *testing.B) {
	base := time.Now().UTC().Add(-14 * 24 * time.Hour)
	mk := func(n, stride, off int) []*StoreTx {
		out := make([]*StoreTx, n)
		for i := 0; i < n; i++ {
			out[i] = &StoreTx{
				ID:        i,
				Hash:      fmt.Sprintf("h%06d", i),
				FirstSeen: base.Add(time.Duration(i*stride+off) * time.Second).Format(time.RFC3339),
			}
		}
		return out
	}
	existing := mk(200000, 6, 0)
	chunk := mk(20000, 60, 3)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mergeChunkIntoPackets(chunk, existing)
	}
}
