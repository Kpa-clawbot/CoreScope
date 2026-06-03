package main

import "testing"

// TestSanitizeLogString covers the log-injection defense added to fix
// audit-input-vulns-20260603 (LOW — log injection via newline in advert name).
func TestSanitizeLogString(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain ascii preserved", "alpha-node", "alpha-node"},
		{"unicode preserved", "Иван привет 🦊", "Иван привет 🦊"},
		{"lf stripped", "evil\n[security] forged-line", "evil?[security] forged-line"},
		{"cr stripped", "evil\rfake-log", "evil?fake-log"},
		{"crlf stripped", "a\r\nb", "a??b"},
		{"tab stripped", "a\tb", "a?b"},
		{"nul stripped", "a\x00b", "a?b"},
		{"del stripped", "a\x7fb", "a?b"},
		{"bell stripped", "a\x07b", "a?b"},
		{"empty unchanged", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeLogString(tc.in)
			if got != tc.want {
				t.Fatalf("sanitizeLogString(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
