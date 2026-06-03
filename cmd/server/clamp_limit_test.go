package main

import "testing"

// TestClampLimit covers the uniform list-endpoint limit-clamp helper added to
// fix audit-input-vulns-20260603 (MEDIUM).
func TestClampLimit(t *testing.T) {
	const def = 50
	const max = 500
	cases := []struct {
		name string
		raw  string
		want int
	}{
		{"empty returns default", "", def},
		{"non-numeric returns default", "abc", def},
		{"negative returns default", "-1", def},
		{"zero returns default", "0", def},
		{"mid-range value preserved", "100", 100},
		{"value at cap preserved", "500", 500},
		{"over-cap clamped to max", "999999999", max},
		{"just over cap clamped", "501", max},
		{"whitespace garbage returns default", " 100 ", def},
		{"float-shaped returns default", "10.5", def},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := clampLimit(tc.raw, def, max)
			if got != tc.want {
				t.Fatalf("clampLimit(%q, %d, %d) = %d, want %d", tc.raw, def, max, got, tc.want)
			}
		})
	}
}
