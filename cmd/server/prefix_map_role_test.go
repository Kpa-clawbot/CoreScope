package main

import (
	"encoding/json"
	"testing"
)

func TestCanAppearInPath(t *testing.T) {
	cases := []struct {
		role string
		want bool
	}{
		{"repeater", true},
		{"Repeater", true},
		{"REPEATER", true},
		{"room_server", true},
		{"Room_Server", true},
		{"room", true},
		{"companion", false},
		{"sensor", false},
		{"", false},
		{"unknown", false},
	}
	for _, tc := range cases {
		if got := canAppearInPath(tc.role); got != tc.want {
			t.Errorf("canAppearInPath(%q) = %v, want %v", tc.role, got, tc.want)
		}
	}
}

func TestBuildPrefixMap_ExcludesCompanions(t *testing.T) {
	nodes := []nodeInfo{
		{PublicKey: "7a1234abcdef", Role: "companion", Name: "MyCompanion"},
	}
	pm := buildPrefixMap(nodes)
	if len(pm.m) != 0 {
		t.Fatalf("expected empty prefix map, got %d entries", len(pm.m))
	}
}

func TestBuildPrefixMap_ExcludesSensors(t *testing.T) {
	nodes := []nodeInfo{
		{PublicKey: "7a1234abcdef", Role: "sensor", Name: "MySensor"},
	}
	pm := buildPrefixMap(nodes)
	if len(pm.m) != 0 {
		t.Fatalf("expected empty prefix map, got %d entries", len(pm.m))
	}
}

func TestResolveWithContext_NilWhenOnlyCompanionMatchesPrefix(t *testing.T) {
	nodes := []nodeInfo{
		{PublicKey: "7a1234abcdef", Role: "companion", Name: "MyCompanion"},
	}
	pm := buildPrefixMap(nodes)
	r, _, _ := pm.resolveWithContext("7a", nil, nil)
	if r != nil {
		t.Fatalf("expected nil, got %+v", r)
	}
}

func TestResolveWithContext_NilWhenOnlySensorMatchesPrefix(t *testing.T) {
	nodes := []nodeInfo{
		{PublicKey: "7a1234abcdef", Role: "sensor", Name: "MySensor"},
	}
	pm := buildPrefixMap(nodes)
	r, _, _ := pm.resolveWithContext("7a", nil, nil)
	if r != nil {
		t.Fatalf("expected nil for sensor-only prefix, got %+v", r)
	}
}

func TestResolveWithContext_PrefersRepeaterOverCompanionAtSamePrefix(t *testing.T) {
	nodes := []nodeInfo{
		{PublicKey: "7a1234abcdef", Role: "companion", Name: "MyCompanion"},
		{PublicKey: "7a5678901234", Role: "repeater", Name: "MyRepeater"},
	}
	pm := buildPrefixMap(nodes)
	r, _, _ := pm.resolveWithContext("7a", nil, nil)
	if r == nil {
		t.Fatal("expected non-nil result")
	}
	if r.Name != "MyRepeater" {
		t.Fatalf("expected MyRepeater, got %s", r.Name)
	}
}

func TestResolveWithContext_PrefersRoomServerOverCompanionAtSamePrefix(t *testing.T) {
	nodes := []nodeInfo{
		{PublicKey: "ab1234abcdef", Role: "companion", Name: "MyCompanion"},
		{PublicKey: "ab5678901234", Role: "room_server", Name: "MyRoom"},
	}
	pm := buildPrefixMap(nodes)
	r, _, _ := pm.resolveWithContext("ab", nil, nil)
	if r == nil {
		t.Fatal("expected non-nil result")
	}
	if r.Name != "MyRoom" {
		t.Fatalf("expected MyRoom, got %s", r.Name)
	}
}

func TestResolve_NilWhenOnlyCompanionMatchesPrefix(t *testing.T) {
	nodes := []nodeInfo{
		{PublicKey: "7a1234abcdef", Role: "companion", Name: "MyCompanion"},
	}
	pm := buildPrefixMap(nodes)
	r := pm.resolve("7a")
	if r != nil {
		t.Fatalf("expected nil from resolve() for companion-only prefix, got %+v", r)
	}
}

func TestResolve_NilWhenOnlySensorMatchesPrefix(t *testing.T) {
	nodes := []nodeInfo{
		{PublicKey: "7a1234abcdef", Role: "sensor", Name: "MySensor"},
	}
	pm := buildPrefixMap(nodes)
	r := pm.resolve("7a")
	if r != nil {
		t.Fatalf("expected nil from resolve() for sensor-only prefix, got %+v", r)
	}
}

func TestResolveWithContext_PicksRepeaterEvenWhenCompanionHasGPS(t *testing.T) {
	// Adversarial: companion has GPS, repeater doesn't. Role filter should
	// exclude companion entirely, so repeater wins despite lacking GPS.
	nodes := []nodeInfo{
		{PublicKey: "7a1234abcdef", Role: "companion", Name: "GPSCompanion", Lat: 37.0, Lon: -122.0, HasGPS: true},
		{PublicKey: "7a5678901234", Role: "repeater", Name: "NoGPSRepeater", Lat: 0, Lon: 0, HasGPS: false},
	}
	pm := buildPrefixMap(nodes)
	r, _, _ := pm.resolveWithContext("7a", nil, nil)
	if r == nil {
		t.Fatal("expected non-nil result")
	}
	if r.Name != "NoGPSRepeater" {
		t.Fatalf("expected NoGPSRepeater (role filter excludes companion), got %s", r.Name)
	}
}

func TestComputeDistancesForTx_CompanionNeverInResolvedChain(t *testing.T) {
	// Integration test: a path with a prefix matching both a companion and a
	// repeater. The resolveHop function (using buildPrefixMap) should only
	// return the repeater.
	nodes := []nodeInfo{
		{PublicKey: "7a1234abcdef", Role: "companion", Name: "BadCompanion", Lat: 37.0, Lon: -122.0, HasGPS: true},
		{PublicKey: "7a5678901234", Role: "repeater", Name: "GoodRepeater", Lat: 38.0, Lon: -123.0, HasGPS: true},
		{PublicKey: "bb1111111111", Role: "repeater", Name: "OtherRepeater", Lat: 39.0, Lon: -124.0, HasGPS: true},
	}
	pm := buildPrefixMap(nodes)

	nodeByPk := make(map[string]*nodeInfo)
	for i := range nodes {
		nodeByPk[nodes[i].PublicKey] = &nodes[i]
	}
	repeaterSet := map[string]bool{
		"7a5678901234": true,
		"bb1111111111": true,
	}

	// Build a synthetic StoreTx with a path ["7a", "bb"] and a sender with GPS
	senderPK := "cc0000000000"
	sender := nodeInfo{PublicKey: senderPK, Role: "repeater", Name: "Sender", Lat: 36.0, Lon: -121.0, HasGPS: true}
	nodeByPk[senderPK] = &sender

	pathJSON, _ := json.Marshal([]string{"7a", "bb"})
	decoded, _ := json.Marshal(map[string]interface{}{"pubKey": senderPK})

	tx := &StoreTx{
		PathJSON:    string(pathJSON),
		DecodedJSON: string(decoded),
		FirstSeen:   "2026-04-30T12:00",
	}

	resolveHop := func(hop string) *nodeInfo {
		return pm.resolve(hop)
	}

	hops, pathRec := computeDistancesForTx(tx, nodeByPk, repeaterSet, resolveHop)

	// Verify BadCompanion's pubkey never appears in hops
	badPK := "7a1234abcdef"
	for i, h := range hops {
		if h.FromPk == badPK || h.ToPk == badPK {
			t.Fatalf("hop[%d] contains BadCompanion pubkey: from=%s to=%s", i, h.FromPk, h.ToPk)
		}
	}

	// Verify BadCompanion's pubkey never appears in pathRec
	if pathRec == nil {
		t.Fatal("expected non-nil path record (3 GPS nodes in chain)")
	}
	for i, hop := range pathRec.Hops {
		if hop.FromPk == badPK || hop.ToPk == badPK {
			t.Fatalf("pathRec.Hops[%d] contains BadCompanion pubkey: from=%s to=%s", i, hop.FromPk, hop.ToPk)
		}
	}

	// Verify GoodRepeater IS in the chain (proves the prefix was resolved to the right node)
	goodPK := "7a5678901234"
	foundGood := false
	for _, hop := range pathRec.Hops {
		if hop.FromPk == goodPK || hop.ToPk == goodPK {
			foundGood = true
			break
		}
	}
	if !foundGood {
		t.Fatal("expected GoodRepeater (7a5678901234) in pathRec.Hops but not found")
	}
}

func TestResolveWithContext_Tier3_PicksHigherObservationCount(t *testing.T) {
	// Two GPS-having repeater candidates for the same prefix, no useful context.
	// Tier 3 should pick the one with higher observation count rather than
	// slice/insertion order.
	nodes := []nodeInfo{
		{PublicKey: "abcd11111111", Role: "repeater", Name: "StaleEarly", Lat: 37.0, Lon: -122.0, HasGPS: true, ObservationCount: 3},
		{PublicKey: "abcd22222222", Role: "repeater", Name: "ActiveLate", Lat: 38.0, Lon: -123.0, HasGPS: true, ObservationCount: 250},
	}
	pm := buildPrefixMap(nodes)
	r, _, _ := pm.resolveWithContext("abcd", nil, nil)
	if r == nil {
		t.Fatal("expected non-nil result")
	}
	if r.Name != "ActiveLate" {
		t.Fatalf("tier-3 tiebreak should pick higher observation count; got %s (obs=%d), want ActiveLate (obs=250)", r.Name, r.ObservationCount)
	}
}

func TestBuildHopContextPubkeys_IncludesSenderAndUnambiguousAnchors(t *testing.T) {
	// Sender + unambiguous anchor "bb" (single candidate) should both end up
	// in the context list. Ambiguous prefix "ab" (multiple candidates) should
	// NOT be added — only unambiguous prefixes count as anchors.
	nodes := []nodeInfo{
		{PublicKey: "ab1111111111", Role: "repeater", Name: "AmbA", Lat: 37.0, Lon: -122.0, HasGPS: true, ObservationCount: 5},
		{PublicKey: "ab2222222222", Role: "repeater", Name: "AmbB", Lat: 38.0, Lon: -123.0, HasGPS: true, ObservationCount: 5},
		{PublicKey: "bb3333333333", Role: "repeater", Name: "Anchor", Lat: 37.5, Lon: -122.5, HasGPS: true, ObservationCount: 10},
	}
	pm := buildPrefixMap(nodes)
	senderPK := "cc4444444444"
	observerPK := "dd5555555555"
	pathJSON, _ := json.Marshal([]string{"ab", "bb"})
	decoded, _ := json.Marshal(map[string]interface{}{"pubKey": senderPK})
	tx := &StoreTx{
		PathJSON:    string(pathJSON),
		DecodedJSON: string(decoded),
		ObserverID:  observerPK,
	}

	got := buildHopContextPubkeys(tx, pm)

	hasSender := false
	hasAnchor := false
	hasObserver := false
	for _, pk := range got {
		if pk == senderPK {
			hasSender = true
		}
		if pk == "bb3333333333" {
			hasAnchor = true
		}
		if pk == observerPK {
			hasObserver = true
		}
		// Ambiguous-prefix candidates must NOT leak into context — only
		// unambiguous (single-candidate) prefixes count as anchors.
		if pk == "ab1111111111" || pk == "ab2222222222" {
			t.Errorf("ambiguous-prefix candidate leaked into context: %s (full=%v)", pk, got)
		}
	}
	if !hasSender {
		t.Errorf("expected sender pubkey %s in context, got %v", senderPK, got)
	}
	if !hasAnchor {
		t.Errorf("expected unambiguous-prefix anchor bb3333333333 in context, got %v", got)
	}
	if !hasObserver {
		t.Errorf("expected observer pubkey %s in context, got %v", observerPK, got)
	}
}

func TestResolveWithContext_Tier3_TiebreakDeterministicByPubkey(t *testing.T) {
	// Three candidates with identical observation counts. Result must be
	// deterministic regardless of slice insertion order: lexicographically
	// smallest PublicKey wins. Three candidates (rather than two reversed)
	// so map iteration / slice order in buildPrefixMap can't accidentally
	// match the assertion. See #1197 (adversarial r1 #8).
	a := nodeInfo{PublicKey: "abcd11111111", Role: "repeater", Name: "A", Lat: 37.0, Lon: -122.0, HasGPS: true, ObservationCount: 100}
	b := nodeInfo{PublicKey: "abcd22222222", Role: "repeater", Name: "B", Lat: 38.0, Lon: -123.0, HasGPS: true, ObservationCount: 100}
	c := nodeInfo{PublicKey: "abcd33333333", Role: "repeater", Name: "C", Lat: 39.0, Lon: -124.0, HasGPS: true, ObservationCount: 100}

	// Property: across every permutation of insertion order, the resolver
	// must pick the lex-smallest pubkey.
	perms := [][]nodeInfo{
		{a, b, c}, {a, c, b}, {b, a, c}, {b, c, a}, {c, a, b}, {c, b, a},
	}
	for i, p := range perms {
		pm := buildPrefixMap(p)
		r, _, _ := pm.resolveWithContext("abcd", nil, nil)
		if r == nil {
			t.Fatalf("perm %d: expected non-nil result", i)
		}
		if r.PublicKey != "abcd11111111" {
			t.Fatalf("perm %d (%v): expected lex-smallest abcd11111111, got %s", i, p, r.PublicKey)
		}
	}
}

func TestResolveWithContext_Tier3_TiebreakNoGPS(t *testing.T) {
	// Same as above but no GPS — exercises the priority-4 path.
	a := nodeInfo{PublicKey: "ee11", Role: "repeater", Name: "A", ObservationCount: 7}
	b := nodeInfo{PublicKey: "ee22", Role: "repeater", Name: "B", ObservationCount: 7}
	pm1 := buildPrefixMap([]nodeInfo{a, b})
	r1, _, _ := pm1.resolveWithContext("ee", nil, nil)
	pm2 := buildPrefixMap([]nodeInfo{b, a})
	r2, _, _ := pm2.resolveWithContext("ee", nil, nil)
	if r1 == nil || r2 == nil {
		t.Fatal("expected non-nil results")
	}
	if r1.PublicKey != r2.PublicKey || r1.PublicKey != "ee11" {
		t.Fatalf("non-deterministic priority-4 tiebreak: r1=%s r2=%s want ee11", r1.PublicKey, r2.PublicKey)
	}
}

func TestResolveWithContext_Tier2_PicksGeographicallyCloserCandidate(t *testing.T) {
	// Two GPS-having candidates for a prefix; a context pubkey near one of
	// them. Tier 2 (geo proximity) must pick the closer one — verifies tier 2
	// is not dead code on distance paths.
	nodes := []nodeInfo{
		{PublicKey: "ee1111111111", Role: "repeater", Name: "Far", Lat: 47.6, Lon: -122.3, HasGPS: true, ObservationCount: 5},
		{PublicKey: "ee2222222222", Role: "repeater", Name: "Near", Lat: 34.05, Lon: -118.25, HasGPS: true, ObservationCount: 5},
		// Context anchor near "Near" (Los Angeles)
		{PublicKey: "ff9999999999", Role: "repeater", Name: "LAAnchor", Lat: 34.1, Lon: -118.3, HasGPS: true, ObservationCount: 50},
	}
	pm := buildPrefixMap(nodes)
	r, method, _ := pm.resolveWithContext("ee", []string{"ff9999999999"}, nil)
	if r == nil {
		t.Fatal("expected non-nil result")
	}
	if r.Name != "Near" {
		t.Fatalf("tier-2 geo proximity should pick Near (LA); got %s via method=%s", r.Name, method)
	}
}
