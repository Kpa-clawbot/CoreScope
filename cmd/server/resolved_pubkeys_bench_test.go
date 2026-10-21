package main

import (
	"fmt"
	"strings"
	"testing"
)

// One resolved_path per observation: 5 relays and 3 unresolved hops, drawn
// from a pool of 2,000 relays, as on a busy instance.
func BenchmarkResolvedPubkeysFromJSON(b *testing.B) {
	const pool = 2000
	paths := make([]string, 256)
	for i := range paths {
		hops := make([]string, 0, 8)
		for h := 0; h < 5; h++ {
			hops = append(hops, fmt.Sprintf(`"%064x"`, (i*7+h*131)%pool))
		}
		hops = append(hops, "null", "null", "null")
		paths[i] = "[" + strings.Join(hops, ",") + "]"
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = resolvedPubkeysFromJSON(paths[i%len(paths)])
	}
}

// An eviction batch of 1,000 packets with 3 observations each.
func BenchmarkResolvedPubkeysForEvictionBatch(b *testing.B) {
	s, ids := evictionResolvedStore(b, 1000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.resolvedPubkeysForEvictionBatch(ids)
	}
}
