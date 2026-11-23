package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// benchRankFixture returns a snapshot of n ranked nodes (about 1 in 4 shares
// a degree with a neighbour, so ties are exercised) and the matching node rows.
func benchRankFixture(n int) (*degreeSnapshot, []nodeInfo) {
	deg := make(map[string]int, n)
	nodes := make([]nodeInfo, 0, n)
	roles := []string{"repeater", "room", "companion", "sensor"}
	for i := 0; i < n; i++ {
		pk := fmt.Sprintf("%064x", i+1)
		deg[pk] = 1 + (i*7)%97
		nodes = append(nodes, nodeInfo{PublicKey: pk, Name: fmt.Sprintf("Node-%05d", i), Role: roles[i%4]})
	}
	return &degreeSnapshot{at: time.Now(), deg: deg}, nodes
}

func BenchmarkNeighborRankBuildView10k(b *testing.B) {
	snap, nodes := benchRankFixture(10000)
	visible := func(*nodeInfo) bool { return true }
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buildNeighborRankView(snap, "", nodes, visible)
	}
}

func rankServerWithView(snap *degreeSnapshot, nodes []nodeInfo) *Server {
	st := &PacketStore{nodeCache: nodes, nodeCacheTime: time.Now()}
	s := &Server{cfg: &Config{}, store: st}
	s.reach.degreeSnap = snap
	s.reach.rankView = buildNeighborRankView(snap, s.nodeVisibilityGen(), nodes, func(*nodeInfo) bool { return true })
	return s
}

func BenchmarkNeighborRankServeCachedPageQ10k(b *testing.B) {
	snap, nodes := benchRankFixture(10000)
	s := rankServerWithView(snap, nodes)
	req := httptest.NewRequest("GET", "/api/nodes/neighbor-rank?q=node-004&role=repeater&limit=50", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rr := httptest.NewRecorder()
		s.handleNeighborRank(rr, req)
		if rr.Code != http.StatusOK {
			b.Fatal(rr.Code)
		}
	}
}

func TestNeighborRankCachedPageUnder5msFor10kNodes(t *testing.T) {
	snap, nodes := benchRankFixture(10000)
	s := rankServerWithView(snap, nodes)
	for _, query := range []string{"", "q=node-004&role=repeater"} {
		req := httptest.NewRequest("GET", "/api/nodes/neighbor-rank?"+query, nil)
		best := time.Hour
		for i := 0; i < 5; i++ { // best of 5: robust against a noisy scheduler
			rr := httptest.NewRecorder()
			start := time.Now()
			s.handleNeighborRank(rr, req)
			if d := time.Since(start); d < best {
				best = d
			}
			if rr.Code != http.StatusOK {
				t.Fatalf("status %d", rr.Code)
			}
		}
		if best > 5*time.Millisecond {
			t.Errorf("cached page %q for 10k nodes took %v, want < 5ms", query, best)
		}
	}
}
