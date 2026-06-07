package main

import "testing"

// resolver that only resolves the exact tokens it's told are unique.
func testResolver(unique map[string]string) func(string) string {
	return func(tok string) string {
		if pk, ok := unique[tok]; ok {
			return pk
		}
		return "" // ambiguous / unknown → skip
	}
}

func TestAttributeDirections_PredecessorAndSuccessor(t *testing.T) {
	// path A(aa) -> N(01fa) -> B(bb): we hear A, B hears us.
	unique := map[string]string{"AA": "aa00", "BB": "bb00"}
	rows := []pathRow{{
		observerPK: "obs1", payloadType: 5,
		path: []string{"AA", "01FA", "BB"},
	}}
	d := attributeDirections(rows, map[string]bool{"01FA": true}, "01fa326b", testResolver(unique))
	if d.we["aa00"] != 1 {
		t.Fatalf("we_hear[aa00]=%d want 1", d.we["aa00"])
	}
	if d.they["bb00"] != 1 {
		t.Fatalf("they_hear[bb00]=%d want 1", d.they["bb00"])
	}
	if d.relay != 1 {
		t.Fatalf("relay=%d want 1", d.relay)
	}
}

func TestAttributeDirections_LastHopObserverAndAdvertFirstHop(t *testing.T) {
	snr := 4.0
	rows := []pathRow{
		// N is last hop → observer heard us directly (+snr).
		{observerPK: "obsx", payloadType: 5, path: []string{"AA", "01FA"}, snr: &snr},
		// N is first hop of an ADVERT (type 4) → we heard the originator.
		{observerPK: "obsy", payloadType: 4, fromPubkey: "origin1", path: []string{"01FA", "CC"}},
	}
	d := attributeDirections(rows, map[string]bool{"01FA": true}, "01fa326b",
		testResolver(map[string]string{"CC": "cc00"}))
	if d.obs["obsx"] == nil || d.obs["obsx"].count != 1 {
		t.Fatalf("observer obsx not counted")
	}
	if d.obs["obsx"].snrN != 1 || d.obs["obsx"].snrSum != 4.0 {
		t.Fatalf("observer snr not aggregated")
	}
	if d.they["obsx"] != 1 {
		t.Fatalf("they_hear[obsx]=%d want 1", d.they["obsx"])
	}
	if d.we["origin1"] != 1 {
		t.Fatalf("we_hear[origin1]=%d want 1 (advert first-hop)", d.we["origin1"])
	}
	if d.they["cc00"] != 1 {
		t.Fatalf("they_hear[cc00]=%d want 1 (successor)", d.they["cc00"])
	}
}

func TestAttributeDirections_AmbiguousSkippedAndSelfIgnored(t *testing.T) {
	// No observer, so the last-hop observer branch can't fire — this isolates
	// the resolve logic. ZZ is unresolved (ambiguous → skipped); the trailing
	// 01FA resolves to self (ourPK) and must be ignored as a successor.
	rows := []pathRow{{observerPK: "", payloadType: 5, path: []string{"ZZ", "01FA", "01FA"}}}
	d := attributeDirections(rows, map[string]bool{"01FA": true}, "01fa326b",
		testResolver(map[string]string{"01FA": "01fa326b"}))
	if len(d.we) != 0 || len(d.they) != 0 {
		t.Fatalf("ambiguous/self should yield no edges, got we=%v they=%v", d.we, d.they)
	}
}

func TestAttributeDirections_LastHopWithObserverCountsObserver(t *testing.T) {
	// Guards the case the previous test deliberately excludes: when our token is
	// the last hop AND an observer is present, that observer heard us directly.
	rows := []pathRow{{observerPK: "obs1", payloadType: 5, path: []string{"ZZ", "01FA"}}}
	d := attributeDirections(rows, map[string]bool{"01FA": true}, "01fa326b",
		testResolver(map[string]string{}))
	if d.they["obs1"] != 1 || d.obs["obs1"] == nil || d.obs["obs1"].count != 1 {
		t.Fatalf("last-hop observer should be counted, got they=%v", d.they)
	}
}

func TestReliableTokens(t *testing.T) {
	// pm where "01fa" is unique but "01" is shared (collision).
	nodes := []nodeInfo{
		{PublicKey: "01fa326b0000", Role: "repeater"},
		{PublicKey: "0188aaaa0000", Role: "repeater"},
	}
	pm := buildPrefixMap(nodes)
	toks := reliableTokens("01fa326b0000", pm)
	if !toks["01FA"] {
		t.Fatalf("expected 01FA reliable, got %v", toks)
	}
	if toks["01"] {
		t.Fatalf("1-byte 01 must be excluded (collision), got %v", toks)
	}
}
