package packetpath

import "testing"

func TestContentHashAllRoutesKeepBaselineGolden(t *testing.T) {
	// Packet.cpp and Packet.h at MeshCore a366955: only routes 0/3 carry
	// transport codes; those codes and hop bytes do not enter packet identity.
	for _, raw := range []string{
		"1401020304000102030405060708090a0b0c0d0e0f",
		"15000102030405060708090a0b0c0d0e0f",
		"16000102030405060708090a0b0c0d0e0f",
		"1701020304000102030405060708090a0b0c0d0e0f",
		"1502aabb0102030405060708090a0b0c0d0e0f",
		"1541aabb0102030405060708090a0b0c0d0e0f",
		"1581aabbcc0102030405060708090a0b0c0d0e0f",
	} {
		if got := ContentHash(raw); got != "fae0c9e6d357a814" {
			t.Fatalf("hash %s=%s", raw, got)
		}
	}
}

func TestContentHashMalformedFallbackPreserved(t *testing.T) {
	for _, raw := range []string{"", "a", "zz", "00112233", "150faabb", "invalid-long-raw-frame"} {
		want := raw
		if len(want) > 16 {
			want = want[:16]
		}
		if got := ContentHash(raw); got != want {
			t.Fatalf("malformed fallback %q=%q; want %q", raw, got, want)
		}
	}
}

func TestContentHashTraceAndAnonymousRequestGoldens(t *testing.T) {
	for _, test := range []struct{ raw, want string }{
		{"250001020304", "240ed67f297f83f9"},
		{"2501aa01020304", "dd79cbe99f3fc8f5"},
		{"1d0001020304", "14d68bbc63e0902d"},
	} {
		if got := ContentHash(test.raw); got != test.want {
			t.Fatalf("hash %s=%s; want %s", test.raw, got, test.want)
		}
	}
}
