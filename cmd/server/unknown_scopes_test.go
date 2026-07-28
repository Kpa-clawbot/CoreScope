package main

import "testing"

func strPtr(s string) *string { return &s }

// TestComputeUnknownScopes covers the core signal: a scope not in the
// configured hashRegions list, reported by multiple distinct neighbors,
// counted once per distinct neighbor (not once per row) and capped to 5
// examples.
func TestComputeUnknownScopes(t *testing.T) {
	entries := []AllObserverNeighborsEntry{
		{NeighborPubkey: "pk1", NeighborName: strPtr("Neighbor One"), Scopes: strPtr("*,#eu,#dk,#dk-storkbh")},
		{NeighborPubkey: "pk2", NeighborName: strPtr("Neighbor Two"), Scopes: strPtr("#dk,#dk-storkbh")},
		// Same neighbor reported again via a different observer -- must
		// count once, not twice.
		{NeighborPubkey: "pk1", NeighborName: strPtr("Neighbor One"), Scopes: strPtr("#dk-storkbh")},
		// #dk and #eu are configured -- must not appear as unknown.
		{NeighborPubkey: "pk3", NeighborName: strPtr("Neighbor Three"), Scopes: strPtr("#dk,#eu")},
	}
	configured := []string{"dk", "eu"} // regionutil.Normalize adds the leading #

	got := computeUnknownScopes(entries, configured)
	if len(got) != 1 {
		t.Fatalf("got %d unknown scopes, want 1 (#dk-storkbh): %+v", len(got), got)
	}
	entry := got[0]
	if entry.Scope != "#dk-storkbh" {
		t.Errorf("Scope = %q, want #dk-storkbh", entry.Scope)
	}
	if entry.Count != 2 {
		t.Errorf("Count = %d, want 2 (Neighbor One + Neighbor Two, deduplicated across pk1's two rows)", entry.Count)
	}
	if len(entry.Examples) != 2 {
		t.Fatalf("got %d examples, want 2: %+v", len(entry.Examples), entry.Examples)
	}
	// Examples are sorted for determinism.
	if entry.Examples[0] != "Neighbor One" || entry.Examples[1] != "Neighbor Two" {
		t.Errorf("Examples = %+v, want [Neighbor One, Neighbor Two]", entry.Examples)
	}
}

// TestComputeUnknownScopes_WildcardExcluded confirms "*" never appears as
// an unknown scope -- it's firmware's catch-all marker, not a real region.
func TestComputeUnknownScopes_WildcardExcluded(t *testing.T) {
	entries := []AllObserverNeighborsEntry{
		{NeighborPubkey: "pk1", Scopes: strPtr("*")},
	}
	got := computeUnknownScopes(entries, nil)
	if len(got) != 0 {
		t.Errorf("got %+v, want empty -- '*' must never surface as an unknown scope", got)
	}
}

// TestComputeUnknownScopes_MissingHashPrefixNormalized confirms a scope
// entry without a leading '#' (shouldn't happen in practice, but the
// ingestor stores whatever firmware sends) still gets compared correctly
// against the #-prefixed configured set.
func TestComputeUnknownScopes_MissingHashPrefixNormalized(t *testing.T) {
	entries := []AllObserverNeighborsEntry{
		{NeighborPubkey: "pk1", Scopes: strPtr("dk-storkbh")}, // no leading #
	}
	got := computeUnknownScopes(entries, nil)
	if len(got) != 1 || got[0].Scope != "#dk-storkbh" {
		t.Fatalf("got %+v, want a single #dk-storkbh entry (# added)", got)
	}
}

// TestComputeUnknownScopes_NilOrEmptyScopesSkipped confirms rows with no
// scope data (nil Scopes, or a nil/blank string) don't panic or produce
// spurious entries -- this is the "timeout" / "no reply" case.
func TestComputeUnknownScopes_NilOrEmptyScopesSkipped(t *testing.T) {
	entries := []AllObserverNeighborsEntry{
		{NeighborPubkey: "pk1", Scopes: nil},
		{NeighborPubkey: "pk2", Scopes: strPtr("")},
	}
	got := computeUnknownScopes(entries, nil)
	if len(got) != 0 {
		t.Errorf("got %+v, want empty", got)
	}
}

// TestComputeUnknownScopes_CapsExamplesAtFive confirms a widely-reported
// unknown scope doesn't balloon the response -- Examples caps at 5, but
// Count still reflects the true total.
func TestComputeUnknownScopes_CapsExamplesAtFive(t *testing.T) {
	var entries []AllObserverNeighborsEntry
	names := []string{"A", "B", "C", "D", "E", "F", "G"}
	for i, n := range names {
		entries = append(entries, AllObserverNeighborsEntry{
			NeighborPubkey: "pk" + string(rune('0'+i)),
			NeighborName:   strPtr(n),
			Scopes:         strPtr("#widespread"),
		})
	}
	got := computeUnknownScopes(entries, nil)
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if got[0].Count != 7 {
		t.Errorf("Count = %d, want 7 (true total, not capped)", got[0].Count)
	}
	if len(got[0].Examples) != 5 {
		t.Errorf("got %d examples, want 5 (capped)", len(got[0].Examples))
	}
}

// TestComputeUnknownScopes_FallsBackToPubkeyWhenNameUnresolved confirms an
// unresolved neighbor's pubkey is used as its display label (and dedupe
// key) instead of a nil-pointer panic.
func TestComputeUnknownScopes_FallsBackToPubkeyWhenNameUnresolved(t *testing.T) {
	entries := []AllObserverNeighborsEntry{
		{NeighborPubkey: "deadbeef00", NeighborName: nil, Scopes: strPtr("#unlisted")},
	}
	got := computeUnknownScopes(entries, nil)
	if len(got) != 1 || len(got[0].Examples) != 1 || got[0].Examples[0] != "deadbeef00" {
		t.Fatalf("got %+v, want a single entry with example 'deadbeef00'", got)
	}
}
