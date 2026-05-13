package main

import (
	"testing"
)

// Issue #770: the region filter dropdown's "All" option was being sent to the
// backend as ?region=All. The backend then tried to match observers with IATA
// code "ALL", which never exists, producing an empty channel/packet list.
//
// "All" / "ALL" / "all" / "" must all be treated as "no region filter".
func TestNormalizeRegionCodes_AllIsNoFilter(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"literal All (frontend dropdown label)", "All"},
		{"upper ALL", "ALL"},
		{"lower all", "all"},
		{"All with whitespace", "  All  "},
		{"All in csv with empty siblings", "All,"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeRegionCodes(tc.in)
			if got != nil {
				t.Errorf("normalizeRegionCodes(%q) = %v, want nil (no filter)", tc.in, got)
			}
		})
	}
}

// Real region codes must still pass through unchanged (case-folded to upper).
// This locks in that the "All" handling does not regress legitimate filters.
func TestNormalizeRegionCodes_RealCodesPreserved(t *testing.T) {
	got := normalizeRegionCodes("sjc,PDX")
	if len(got) != 2 || got[0] != "SJC" || got[1] != "PDX" {
		t.Errorf("normalizeRegionCodes(\"sjc,PDX\") = %v, want [SJC PDX]", got)
	}
}
