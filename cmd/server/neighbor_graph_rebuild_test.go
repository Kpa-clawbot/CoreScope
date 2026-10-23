package main

import (
	"testing"
	"time"
)

// #2221: the request path must never rebuild an existing neighbor graph.
// On a production store the build takes ~11 s, and getNeighborGraph held
// neighborMu while doing it, so whoever arrived after the TTL waited for it.

func rebuildTestStore() *PacketStore {
	nodes := []nodeInfo{
		{Role: "repeater", PublicKey: "aaaa1111", Name: "NodeX"},
		{Role: "repeater", PublicKey: "r1aabbcc", Name: "R1"},
		{Role: "repeater", PublicKey: "obs00001", Name: "Observer"},
	}
	tx := ngMakeTx(1, 4, ngFromNodeJSON("aaaa1111"), []*StoreObs{
		ngMakeObs("obs00001", `["r1aa"]`, nowStr, nil),
	})
	return ngTestStore(nodes, []*StoreTx{tx})
}

func TestGetNeighborGraph_ServesStaleGraphWithoutRebuild(t *testing.T) {
	stale := NewNeighborGraph()
	stale.builtAt = time.Now().Add(-2 * neighborGraphTTL)
	srv := &Server{store: rebuildTestStore(), neighborGraph: stale}

	if got := srv.getNeighborGraph(); got != stale {
		t.Fatal("getNeighborGraph rebuilt a stale graph on the request path")
	}
}

func TestGetNeighborGraph_BuildsWhenNone(t *testing.T) {
	srv := &Server{store: rebuildTestStore()}

	g := srv.getNeighborGraph()
	if g == nil || len(g.Neighbors("aaaa1111")) == 0 {
		t.Fatal("getNeighborGraph with no graph yet did not build one from the store")
	}
}

func TestRecomputeNeighborGraphCache_RebuildsFreshGraph(t *testing.T) {
	fresh := NewNeighborGraph()
	fresh.builtAt = time.Now()
	srv := &Server{
		store:         rebuildTestStore(),
		neighborGraph: fresh,
		computeNeighborGraphResponseFn: func(int, float64, string, string) NeighborGraphResponse {
			return NeighborGraphResponse{}
		},
	}

	srv.recomputeNeighborGraphCache()

	g := srv.getNeighborGraph()
	if g == fresh {
		t.Fatal("recompute kept a graph that was not stale yet; it must rebuild on every tick")
	}
	if len(g.Neighbors("aaaa1111")) == 0 {
		t.Fatal("rebuilt graph is missing the store's X↔R1 edge")
	}
}

func TestRecomputeNeighborGraphCache_KeepsGraphWithoutStore(t *testing.T) {
	injected := makeTestGraph(newEdge("aaaa", "bbbb", "bb", 50, time.Now()))
	srv := &Server{
		neighborGraph: injected,
		computeNeighborGraphResponseFn: func(int, float64, string, string) NeighborGraphResponse {
			return NeighborGraphResponse{}
		},
	}

	srv.recomputeNeighborGraphCache()

	if srv.getNeighborGraph() != injected {
		t.Fatal("recompute without a store replaced the graph with an empty one")
	}
}
