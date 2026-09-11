package main

import "testing"

// TestNodeScopeConfigState pins the classification behind the /api/nodes
// scope_config_state field (#2001). The four declared states are the audit's
// own vocabulary; the two extra ones exist because /api/nodes has to say
// something about a repeater the audit omits entirely — one that has never
// answered a declared-regions request.
func TestNodeScopeConfigState(t *testing.T) {
	tests := []struct {
		name        string
		declaredCSV string
		hasDeclared bool
		transported []string
		want        string
	}{
		{"named regions and wildcard", "be,*", true, nil, ScopeConfigFull},
		{"wildcard only", "*", true, nil, ScopeConfigNoScopes},
		{"named regions only", "be", true, nil, ScopeConfigNoUnscoped},
		{"answered with an empty list", "", true, nil, ScopeConfigNoFlood},
		{"never asked, but observed carrying a scope", "", false, []string{"#be"}, ScopeConfigObserved},
		{"never asked, nothing scoped observed", "", false, nil, ScopeConfigNone},
		// The declared answer is the repeater's own word about its config and
		// outranks what we happened to see it carry: a node that answered with
		// an empty list stays no-flood even though traffic says otherwise.
		// That contradiction is the audit's finding to report, not a reason to
		// re-label the node here.
		{"empty answer outranks observed traffic", "", true, []string{"#be"}, ScopeConfigNoFlood},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := nodeScopeConfigState(tt.declaredCSV, tt.hasDeclared, tt.transported)
			if got != tt.want {
				t.Errorf("nodeScopeConfigState(%q, %v, %v) = %q, want %q",
					tt.declaredCSV, tt.hasDeclared, tt.transported, got, tt.want)
			}
		})
	}
}

// TestNodeScopeConfigStateIgnoresWhitespaceAndCase pins the two shapes a
// declared list actually arrives in. regions_csv is written by two different
// collectors, and one of them spaces its entries, so " be , * " has to
// classify as full rather than as a pair of unrecognised names.
func TestNodeScopeConfigStateIgnoresWhitespaceAndCase(t *testing.T) {
	if got := nodeScopeConfigState(" be , * ", true, nil); got != ScopeConfigFull {
		t.Errorf("spaced list = %q, want %q", got, ScopeConfigFull)
	}
	if got := nodeScopeConfigState("#be", true, nil); got != ScopeConfigNoUnscoped {
		t.Errorf("hash-prefixed region = %q, want %q", got, ScopeConfigNoUnscoped)
	}
}

// TestNodeScopeConfigStateAcceptsHashPrefixedWildcard pins the second spelling
// of the wildcard. The ingestor treats "*" and "#*" as the same thing
// (cmd/ingestor/region_keys.go:94), so a collector storing the prefixed form
// must not have it counted as a named region — that would turn a fully
// configured repeater into "no unscoped", which reads as a fault.
func TestNodeScopeConfigStateAcceptsHashPrefixedWildcard(t *testing.T) {
	if got := nodeScopeConfigState("#be,#*", true, nil); got != ScopeConfigFull {
		t.Errorf("nodeScopeConfigState(\"#be,#*\") = %q, want %q", got, ScopeConfigFull)
	}
	if got := nodeScopeConfigState("#*", true, nil); got != ScopeConfigNoScopes {
		t.Errorf("nodeScopeConfigState(\"#*\") = %q, want %q", got, ScopeConfigNoScopes)
	}
}
