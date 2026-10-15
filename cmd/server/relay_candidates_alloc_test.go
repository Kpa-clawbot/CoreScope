package main

import (
	"encoding/hex"
	"math/rand"
	"strings"
	"testing"
)

// relayCandidates runs once per hop of every observation the cold loader
// resolves. With any listener-only observer known it built a filtered copy
// of the candidate nodeInfo structs on every call: about 1 GB allocated over
// one startup on a week of a busy network. The filtered lists are built once
// when the listeners are marked.
func TestRelayCandidatesDoesNotAllocate(t *testing.T) {
	pm := buildPrefixMap([]nodeInfo{
		{PublicKey: "a4aa000000000000000000000000000000000000000000000000000000000001", Role: "repeater"},
		{PublicKey: "a4bb000000000000000000000000000000000000000000000000000000000002", Role: "repeater"},
		{PublicKey: "c1cc000000000000000000000000000000000000000000000000000000000003", Role: "repeater"},
	})
	pm.markNonRelay([]string{"A4BB000000000000000000000000000000000000000000000000000000000002"})

	if got := pm.relayCandidates("A4"); len(got) != 1 || got[0].PublicKey[:4] != "a4aa" {
		t.Fatalf("relayCandidates(A4) = %v, want only the a4aa relay", got)
	}
	if got := pm.relayCandidates("a4bb"); len(got) != 0 {
		t.Fatalf("relayCandidates(a4bb) = %v, want none (the only match is a listener)", got)
	}
	if got := pm.relayCandidates("c1"); len(got) != 1 {
		t.Fatalf("relayCandidates(c1) = %v, want one", got)
	}
	for _, hop := range []string{"a4", "a4bb", "c1", "ff"} {
		if n := testing.AllocsPerRun(100, func() { _ = pm.relayCandidates(hop) }); n != 0 {
			t.Errorf("relayCandidates(%q) allocates %.0f times per call, want 0", hop, n)
		}
	}
}

// Marking more listeners later still filters them.
func TestRelayCandidatesAfterSecondMark(t *testing.T) {
	pm := buildPrefixMap([]nodeInfo{
		{PublicKey: "a4aa000000000000000000000000000000000000000000000000000000000001", Role: "repeater"},
		{PublicKey: "a4bb000000000000000000000000000000000000000000000000000000000002", Role: "repeater"},
	})
	pm.markNonRelay([]string{"a4aa000000000000000000000000000000000000000000000000000000000001"})
	pm.markNonRelay([]string{"a4bb000000000000000000000000000000000000000000000000000000000002"})
	if got := pm.relayCandidates("a4"); len(got) != 0 {
		t.Fatalf("relayCandidates(a4) = %v, want none after both are marked", got)
	}
}

// markNonRelayFixture is 5,000 repeaters with random keys, three of which
// are marked as listeners, the size of a busy regional network.
func markNonRelayFixture() (*prefixMap, []string) {
	rng := rand.New(rand.NewSource(1290))
	nodes := make([]nodeInfo, 5000)
	for i := range nodes {
		b := make([]byte, 32)
		rng.Read(b)
		nodes[i] = nodeInfo{PublicKey: hex.EncodeToString(b), Role: "repeater"}
	}
	return buildPrefixMap(nodes), []string{nodes[7].PublicKey, nodes[1234].PublicKey, strings.ToUpper(nodes[4321].PublicKey)}
}

// markNonRelay runs on every node cache rebuild, as often as every 30 s. It
// used to copy every prefix's list to find the few that hold a listener;
// those can only be the listeners' own prefixes, so only they are visited.
func TestMarkNonRelayVisitsOnlyListenerPrefixes(t *testing.T) {
	_, listeners := markNonRelayFixture()
	n := testing.AllocsPerRun(5, func() {
		pm, _ := markNonRelayFixture()
		pm.markNonRelay(listeners)
	}) - testing.AllocsPerRun(5, func() { markNonRelayFixture() })
	// Three listeners have at most 8 prefixes each, plus the maps themselves.
	if n > 80 {
		t.Errorf("markNonRelay allocates %.0f times for 3 listeners among 5,000 repeaters, want at most 80", n)
	}
}

// Every prefix gives what filtering its list on the spot would.
func TestMarkNonRelayMatchesFilteringEachList(t *testing.T) {
	pm, listeners := markNonRelayFixture()
	pm.markNonRelay(listeners)
	isListener := map[string]bool{}
	for _, pk := range listeners {
		isListener[strings.ToLower(pk)] = true
	}
	for key, candidates := range pm.m {
		var want []nodeInfo
		for _, c := range candidates {
			if !isListener[strings.ToLower(c.PublicKey)] {
				want = append(want, c)
			}
		}
		got := pm.relayCandidates(key)
		if len(got) != len(want) {
			t.Fatalf("relayCandidates(%q) has %d candidates, want %d", key, len(got), len(want))
		}
		for i := range want {
			if got[i].PublicKey != want[i].PublicKey {
				t.Fatalf("relayCandidates(%q)[%d] = %s, want %s", key, i, got[i].PublicKey, want[i].PublicKey)
			}
		}
	}
}

func BenchmarkMarkNonRelay5000(b *testing.B) {
	pm, listeners := markNonRelayFixture()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pm.nonRelay, pm.relay = nil, nil // as on a fresh rebuild
		pm.markNonRelay(listeners)
	}
}

// relayCandidates is the other side of markNonRelay's work: one lookup per
// 2-character prefix, the hop width most paths use, so 256 lookups per op.
func BenchmarkRelayCandidates256(b *testing.B) {
	pm, listeners := markNonRelayFixture()
	pm.markNonRelay(listeners)
	const digits = "0123456789abcdef"
	hops := make([]string, 0, 256)
	for _, x := range digits {
		for _, y := range digits {
			hops = append(hops, string(x)+string(y))
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, h := range hops {
			_ = pm.relayCandidates(h)
		}
	}
}
