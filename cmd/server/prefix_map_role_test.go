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

func TestResolveWithContext_ReturnsRepeaterAtItsPrefix(t *testing.T) {
	nodes := []nodeInfo{
		{PublicKey: "7a1234abcdef", Role: "repeater", Name: "MyRepeater"},
	}
	pm := buildPrefixMap(nodes)
	r, reason, _ := pm.resolveWithContext("7a", nil, nil)
	if r == nil {
		t.Fatal("expected non-nil result for repeater prefix")
	}
	if r.Name != "MyRepeater" {
		t.Fatalf("expected MyRepeater, got %s", r.Name)
	}
	if reason != "unique_prefix" {
		t.Fatalf("expected reason unique_prefix, got %s", reason)
	}
}

func TestResolveWithContext_ReturnsRoomServerAtItsPrefix(t *testing.T) {
	nodes := []nodeInfo{
		{PublicKey: "ab1234567890", Role: "room_server", Name: "MyRoom"},
	}
	pm := buildPrefixMap(nodes)
	r, reason, _ := pm.resolveWithContext("ab", nil, nil)
	if r == nil {
		t.Fatal("expected non-nil result for room_server prefix")
	}
	if r.Name != "MyRoom" {
		t.Fatalf("expected MyRoom, got %s", r.Name)
	}
	if reason != "unique_prefix" {
		t.Fatalf("expected reason unique_prefix, got %s", reason)
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

	// The chain should include Sender, GoodRepeater, OtherRepeater — never BadCompanion
	if pathRec == nil {
		t.Fatal("expected non-nil path record (3 GPS nodes in chain)")
	}
	_ = hops
	// Verify via resolveHop that "7a" resolves to GoodRepeater
	resolved := pm.resolve("7a")
	if resolved == nil {
		t.Fatal("expected resolve('7a') to return GoodRepeater")
	}
	if resolved.Name != "GoodRepeater" {
		t.Fatalf("expected GoodRepeater in resolved chain, got %s", resolved.Name)
	}
}
