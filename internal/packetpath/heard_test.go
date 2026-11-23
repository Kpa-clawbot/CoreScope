package packetpath

import "testing"

func TestHeardKey(t *testing.T) {
	cases := []struct {
		name         string
		routeType    int
		payloadType  byte
		lastHop      string
		advertPubkey string
		wantKey      string
		wantSrc      string
		wantOK       bool
	}{
		{"flood, 2-byte last hop", RouteFlood, 0x05, "BB11", "", "bb11", "rxlog", true},
		{"transport flood counts as flood", RouteTransportFlood, 0x05, "bb11", "", "bb11", "rxlog", true},
		{"3-byte last hop", RouteFlood, 0x05, "bbccdd", "", "bbccdd", "rxlog", true},
		{"1-byte last hop refused", RouteFlood, 0x05, "BB", "", "", "", false},
		{"direct route with a path refused", RouteDirect, 0x05, "bb11", "", "", "", false},
		{"transport direct with a path refused", RouteTransportDirect, 0x05, "bb11", "", "", "", false},
		{"TRACE refused even on flood", RouteFlood, PayloadTRACE, "bb11", "", "", "", false},
		{"0-hop advert on flood", RouteFlood, 0x04, "", "ABCDEF01", "abcdef01", "advert", true},
		{"0-hop advert on direct (sendZeroHop)", RouteDirect, 0x04, "", "abcdef01", "abcdef01", "advert", true},
		{"empty path, no advert pubkey", RouteFlood, 0x05, "", "", "", "", false},
		{"TRACE with empty path refused", RouteFlood, PayloadTRACE, "", "abcdef01", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			key, src, ok := HeardKey(c.routeType, c.payloadType, c.lastHop, c.advertPubkey)
			if key != c.wantKey || src != c.wantSrc || ok != c.wantOK {
				t.Errorf("HeardKey(%d, %#x, %q, %q) = (%q, %q, %v), want (%q, %q, %v)",
					c.routeType, c.payloadType, c.lastHop, c.advertPubkey, key, src, ok, c.wantKey, c.wantSrc, c.wantOK)
			}
		})
	}
}

// Callers pass only the last element of the path, so a 1-byte hop earlier in
// a mixed-size path (["A4","BB11"]) does not matter.
func TestHeardKey_OnlyLastHopLengthCounts(t *testing.T) {
	path := []string{"A4", "BB11"}
	key, src, ok := HeardKey(RouteFlood, 0x05, path[len(path)-1], "")
	if key != "bb11" || src != "rxlog" || !ok {
		t.Errorf("got (%q, %q, %v), want (\"bb11\", \"rxlog\", true)", key, src, ok)
	}
}
