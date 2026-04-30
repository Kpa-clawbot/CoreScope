package main

import (
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

func TestBuildPrefixMap_IncludesRepeaters(t *testing.T) {
	nodes := []nodeInfo{
		{PublicKey: "7a1234abcdef", Role: "repeater", Name: "MyRepeater"},
	}
	pm := buildPrefixMap(nodes)
	if _, ok := pm.m["7a"]; !ok {
		t.Fatal("expected prefix '7a' in map for repeater")
	}
}

func TestBuildPrefixMap_IncludesRoomServers(t *testing.T) {
	nodes := []nodeInfo{
		{PublicKey: "ab1234567890", Role: "room_server", Name: "MyRoom"},
	}
	pm := buildPrefixMap(nodes)
	if _, ok := pm.m["ab"]; !ok {
		t.Fatal("expected prefix 'ab' in map for room_server")
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
