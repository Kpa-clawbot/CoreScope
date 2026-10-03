package packetpath

import (
	"strings"
	"testing"
)

func TestAdvertRouteEvidence(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want uint8
	}{
		{"1100aa", 1}, {"1101ffaa", 1}, {"1140aa", 1}, {"100102030400aa", 1},
		{"1200aa", 2}, {"130102030400aa", 2},
		{"1240aa", 2}, {"1280aa", 2}, {"130102030440aa", 2}, {"130102030480aa", 2}, {"12c0aa", 0}, {"1201ffaa", 0},
		{"130102030401ffaa", 0}, {"13zz00000000aa", 0}, {"16ff", 0},
		{"1100zz", 0}, {"1101", 0}, {"1101ff", 0}, {"11c0aa", 0},
		{"1100a", 0}, {"1100", 0}, {"1001020304", 0}, {"100102030400", 0},
		{"", 0}, {"1200" + strings.Repeat("aa", 255), 0},
		{"1100" + strings.Repeat("aa", 185), 0},
		{"1100" + strings.Repeat("aa", 184), 1},
	} {
		if got := AdvertRouteEvidence(tc.raw); got != tc.want {
			t.Errorf("%s: got %d want %d", tc.raw, got, tc.want)
		}
	}
	for mask, want := range []string{"other", "flood", "zero_hop", "mixed"} {
		if got := AdvertKind(uint8(mask)); got != want {
			t.Errorf("mask %d: %s want %s", mask, got, want)
		}
	}
}

func BenchmarkAdvertRouteEvidence(b *testing.B) {
	raw := "1100" + strings.Repeat("ab", 118)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		AdvertRouteEvidence(raw)
	}
}
