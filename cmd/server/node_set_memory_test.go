package main

import (
	"fmt"
	"testing"
)

// nodeHashes is the set behind byNode's de-dup: for every node, the packets
// already listed under it. Keyed by packet ID rather than by the packets'
// 16-character hashes, the same membership takes markedly less memory; the
// limit below sits between the two.
func TestNodeSetRetainedHeap(t *testing.T) {
	const nodes, perNode = 2000, 250 // 500k (node, packet) pairs
	txs := make([]*StoreTx, perNode*4)
	for i := range txs {
		txs[i] = &StoreTx{ID: i + 1, Hash: fmt.Sprintf("%016x", i+1)}
	}
	pks := make([]string, nodes)
	for i := range pks {
		pks[i] = fmt.Sprintf("%064x", i)
	}
	mb := retainedHeapMB(func() interface{} {
		s := &PacketStore{byNode: map[string][]*StoreTx{}, nodeHashes: newNodeSets()}
		for n, pk := range pks {
			for j := 0; j < perNode; j++ {
				s.addToByNode(txs[(n+j)%len(txs)], pk)
			}
		}
		s.byNode = nil // measure the set alone
		return s
	})
	t.Logf("node sets for 500k (node, packet) pairs keep %.1f MB", mb)
	const limitMB = 21
	if mb > limitMB {
		t.Fatalf("node sets keep %.1f MB for 500k pairs, limit %d MB", mb, limitMB)
	}
}

// Adding the same packet under a node twice lists it once.
func TestAddToByNodeDedup(t *testing.T) {
	s := &PacketStore{byNode: map[string][]*StoreTx{}, nodeHashes: newNodeSets()}
	tx := &StoreTx{ID: 7, Hash: "abc"}
	if !s.addToByNode(tx, "pk") {
		t.Fatal("first add should report a new node")
	}
	if s.addToByNode(tx, "pk") || len(s.byNode["pk"]) != 1 {
		t.Fatalf("second add listed it again: %d", len(s.byNode["pk"]))
	}
	if !s.nodeHasTx("pk", tx) || s.nodeHasTx("pk", &StoreTx{ID: 8, Hash: "def"}) {
		t.Fatal("nodeHasTx disagrees with what was added")
	}
}
