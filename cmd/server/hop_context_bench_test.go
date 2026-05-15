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
