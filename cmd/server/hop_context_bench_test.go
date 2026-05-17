package main

import (
	"encoding/json"
	"fmt"
	"testing"
)

// BenchmarkBuildHopContextPubkeys exercises the hot per-tx context builder
// at a realistic shape: ~50 nodes (mixed role), 6-hop path, sender + observer
// pubkey populated. Required by AGENTS.md hot-path benchmark rule (#1197 r1
// carmack #6).
func BenchmarkBuildHopContextPubkeys(b *testing.B) {
	nodes := make([]nodeInfo, 0, 64)
	for i := 0; i < 50; i++ {
		nodes = append(nodes, nodeInfo{
			PublicKey:        fmt.Sprintf("%012x", i*0x101010101),
			Role:             "repeater",
			Name:             fmt.Sprintf("N%d", i),
			ObservationCount: i * 3,
			Lat:              37.0 + float64(i)*0.01,
			Lon:              -122.0 - float64(i)*0.01,
			HasGPS:           true,
		})
	}
	pm := buildPrefixMap(nodes)

	hops := []string{
		nodes[1].PublicKey[:6], nodes[3].PublicKey[:6], nodes[5].PublicKey[:6],
		nodes[7].PublicKey[:6], nodes[9].PublicKey[:6], nodes[11].PublicKey[:6],
	}
	pathJSON, _ := json.Marshal(hops)
	decoded, _ := json.Marshal(map[string]interface{}{"pubKey": "cc4444444444"})
	tx := &StoreTx{
		PathJSON:    string(pathJSON),
		DecodedJSON: string(decoded),
		ObserverID:  "dd5555555555",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildHopContextPubkeys(tx, pm)
	}
}

// BenchmarkBuildAggregateHopContextPubkeys exercises the aggregate context
// builder at the hot scale called out by #1197 (subpath/topology bulk
// aggregations): ~5k txs sharing a node pool of ~50 prefixes. The aggregate
// builder unions per-tx contexts with its own dedupe map; this benchmark
// gives us a baseline so a future regression (e.g. accidental O(n²) dedupe)
// shows up immediately. No assertion threshold yet — see #1199 item 3.
func BenchmarkBuildAggregateHopContextPubkeys(b *testing.B) {
	const numNodes = 50
	const numTxs = 5000

	nodes := make([]nodeInfo, 0, numNodes)
	for i := 0; i < numNodes; i++ {
		nodes = append(nodes, nodeInfo{
			PublicKey:        fmt.Sprintf("%012x", i*0x101010101),
			Role:             "repeater",
			Name:             fmt.Sprintf("N%d", i),
			ObservationCount: i * 3,
			Lat:              37.0 + float64(i)*0.01,
			Lon:              -122.0 - float64(i)*0.01,
			HasGPS:           true,
		})
	}
	pm := buildPrefixMap(nodes)

	txs := make([]*StoreTx, 0, numTxs)
	for i := 0; i < numTxs; i++ {
		hops := []string{
			nodes[(i+1)%numNodes].PublicKey[:6],
			nodes[(i+3)%numNodes].PublicKey[:6],
			nodes[(i+5)%numNodes].PublicKey[:6],
			nodes[(i+7)%numNodes].PublicKey[:6],
			nodes[(i+9)%numNodes].PublicKey[:6],
			nodes[(i+11)%numNodes].PublicKey[:6],
		}
		pathJSON, _ := json.Marshal(hops)
		decoded, _ := json.Marshal(map[string]interface{}{
			"pubKey": fmt.Sprintf("cc%010x", i),
		})
		txs = append(txs, &StoreTx{
			PathJSON:    string(pathJSON),
			DecodedJSON: string(decoded),
			ObserverID:  fmt.Sprintf("dd%010x", i%32),
		})
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildAggregateHopContextPubkeys(txs, pm)
	}
}

// TestBuildAggregateHopContextPubkeysSmoke is a tiny correctness anchor for
// the aggregate helper: union over per-tx contexts, deduped. Lives next to
// the benchmark so the file ships an assertion (preflight gate). See #1199
// item 3.
func TestBuildAggregateHopContextPubkeysSmoke(t *testing.T) {
	pm := buildPrefixMap([]nodeInfo{{PublicKey: "aabbccddeeff"}})
	d1, _ := json.Marshal(map[string]interface{}{"pubKey": "1111111111"})
	d2, _ := json.Marshal(map[string]interface{}{"pubKey": "2222222222"})
	d3, _ := json.Marshal(map[string]interface{}{"pubKey": "1111111111"}) // dup
	txs := []*StoreTx{
		{DecodedJSON: string(d1)},
		{DecodedJSON: string(d2)},
		{DecodedJSON: string(d3)},
	}
	got := buildAggregateHopContextPubkeys(txs, pm)
	if len(got) != 2 {
		t.Fatalf("expected 2 deduped pubkeys, got %d (%v)", len(got), got)
	}
	// Content assertion — proves dedup actually keeps the right pubkeys
	// (not just any 2). Without this the test would pass even if dedup
	// returned, e.g., one pubkey twice or two unrelated pubkeys. See
	// #1199 r1 review (adv #1).
	wantSet := map[string]bool{"1111111111": true, "2222222222": true}
	gotSet := map[string]bool{}
	for _, pk := range got {
		gotSet[pk] = true
	}
	for pk := range wantSet {
		if !gotSet[pk] {
			t.Fatalf("expected pubkey %q in deduped result, got %v", pk, got)
		}
	}
	for pk := range gotSet {
		if !wantSet[pk] {
			t.Fatalf("unexpected pubkey %q in deduped result, got %v", pk, got)
		}
	}
	if buildAggregateHopContextPubkeys(nil, pm) != nil {
		t.Fatalf("nil tx slice must yield nil")
	}
	if buildAggregateHopContextPubkeys(txs, nil) != nil {
		t.Fatalf("nil prefix map must yield nil")
	}
}
