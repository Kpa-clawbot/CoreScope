package main

import "testing"

func TestIfNoneMatchHits(t *testing.T) {
	cases := []struct {
		name   string
		req    string
		stored string
		want   bool
	}{
		{"empty request never matches", "", `"abc"`, false},
		{"empty stored never matches", `"abc"`, "", false},
		{"strong exact match", `"abc"`, `"abc"`, true},
		{"weak request, strong stored (CF rewrite case)", `W/"abc"`, `"abc"`, true},
		{"strong request, weak stored", `"abc"`, `W/"abc"`, true},
		{"both weak", `W/"abc"`, `W/"abc"`, true},
		{"wildcard matches", `*`, `"abc"`, true},
		{"different hashes don't match", `"def"`, `"abc"`, false},
		{"comma list, second matches", `"x", W/"abc"`, `"abc"`, true},
		{"comma list, none matches", `"x", "y"`, `"abc"`, false},
		{"leading/trailing whitespace in list entry", ` "abc" `, `"abc"`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ifNoneMatchHits(tc.req, tc.stored)
			if got != tc.want {
				t.Errorf("ifNoneMatchHits(%q, %q) = %v, want %v", tc.req, tc.stored, got, tc.want)
			}
		})
	}
}
