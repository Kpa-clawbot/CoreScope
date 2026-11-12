package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func xffReq(peer string, xff ...string) *http.Request {
	r := httptest.NewRequest("GET", "/api/planner/x", nil)
	r.RemoteAddr = peer
	for _, v := range xff {
		r.Header.Add("X-Forwarded-For", v)
	}
	return r
}

func TestClientIPRightmost(t *testing.T) {
	trusted := []string{"10.0.0.0/8", "2001:db8:ffff::/48"}
	cases := []struct {
		name         string
		trusted      []string
		peer         string
		xff          []string
		want         string
		wantDistinct bool
		wantLeftmost string // what /ws resolves for the same request
	}{
		{"single proxy", trusted, "10.0.0.2:1", []string{"203.0.113.20"}, "203.0.113.20", true, "203.0.113.20"},
		{"chain of two trusted proxies", trusted, "10.0.0.2:1", []string{"203.0.113.20, 10.0.0.9"}, "203.0.113.20", true, "203.0.113.20"},
		{"spoofed leftmost", trusted, "10.0.0.2:1", []string{"1.2.3.4, 203.0.113.20"}, "203.0.113.20", true, "1.2.3.4"},
		{"spoofed leftmost across header lines", trusted, "10.0.0.2:1", []string{"1.2.3.4", "203.0.113.20"}, "203.0.113.20", true, "1.2.3.4"},
		{"spoofed leftmost behind two proxies", trusted, "10.0.0.2:1", []string{"1.2.3.4, 203.0.113.20, 10.0.0.9"}, "203.0.113.20", true, "1.2.3.4"},
		{"all entries trusted", trusted, "10.0.0.2:1", []string{"10.0.0.5, 10.0.0.9"}, "10.0.0.2", true, "10.0.0.5"},
		{"no header", trusted, "10.0.0.2:1", nil, "10.0.0.2", true, "10.0.0.2"},
		{"garbage only", trusted, "10.0.0.2:1", []string{"not-an-ip"}, "10.0.0.2", true, "10.0.0.2"},
		{"garbage rightmost", trusted, "10.0.0.2:1", []string{"203.0.113.20, junk"}, "10.0.0.2", true, "203.0.113.20"},
		{"garbage leftmost is never reached", trusted, "10.0.0.2:1", []string{"junk, 203.0.113.20"}, "203.0.113.20", true, "10.0.0.2"},
		{"ipv6 client", trusted, "10.0.0.2:1", []string{"2001:db8::7"}, "2001:db8::7", true, "2001:db8::7"},
		{"ipv6 proxy chain", trusted, "[2001:db8:ffff::1]:1", []string{"198.51.100.4, 2001:db8:ffff::2"}, "198.51.100.4", true, "198.51.100.4"},
		{"untrusted peer ignores header", trusted, "203.0.113.30:1", []string{"198.51.100.1"}, "203.0.113.30", true, "203.0.113.30"},
		{"private peer, no trusted proxies: not distinct", nil, "10.0.0.2:1", []string{"203.0.113.20"}, "10.0.0.2", false, "10.0.0.2"},
		{"loopback peer, no trusted proxies: not distinct", nil, "127.0.0.1:1", []string{"203.0.113.20"}, "127.0.0.1", false, "127.0.0.1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := &wsLimiter{trustedProxies: parseCIDRList(tc.trusted, "test")}
			ip, distinct := l.clientIPRightmost(xffReq(tc.peer, tc.xff...))
			if ip == nil || ip.String() != tc.want || distinct != tc.wantDistinct {
				t.Errorf("rightmost = %v distinct=%v, want %s distinct=%v", ip, distinct, tc.want, tc.wantDistinct)
			}
			// /ws keeps resolving the leftmost entry.
			ws, _ := l.clientIP(xffReq(tc.peer, tc.xff...))
			if ws == nil || ws.String() != tc.wantLeftmost {
				t.Errorf("/ws clientIP = %v, want %s", ws, tc.wantLeftmost)
			}
		})
	}
}

func TestClientLimiterKeysOnRightmostUntrusted(t *testing.T) {
	c := &clientLimiter{ipr: &wsLimiter{trustedProxies: parseCIDRList([]string{"10.0.0.0/8"}, "test")}, tag: "t", what: "w"}
	// A client rotating its own leftmost entry must stay in one bucket.
	a := c.clientKey(xffReq("10.0.0.2:1", "1.1.1.1, 203.0.113.20"))
	b := c.clientKey(xffReq("10.0.0.2:1", "2.2.2.2, 203.0.113.20"))
	if a != "203.0.113.20" || a != b {
		t.Errorf("keys %q, %q: want both 203.0.113.20", a, b)
	}
}
