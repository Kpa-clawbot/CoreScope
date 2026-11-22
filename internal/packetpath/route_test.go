package packetpath

import "testing"

func TestIsTransportRoute(t *testing.T) {
	if !IsTransportRoute(RouteTransportFlood) {
		t.Error("RouteTransportFlood should be transport")
	}
	if !IsTransportRoute(RouteTransportDirect) {
		t.Error("RouteTransportDirect should be transport")
	}
	if IsTransportRoute(RouteFlood) {
		t.Error("RouteFlood should not be transport")
	}
	if IsTransportRoute(RouteDirect) {
		t.Error("RouteDirect should not be transport")
	}
}

func TestPathBytesAreHops(t *testing.T) {
	if PathBytesAreHops(PayloadTRACE) {
		t.Error("PathBytesAreHops(PayloadTRACE) should be false")
	}
	// All other known payload types should return true.
	otherTypes := []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x0A, 0x0B, 0x0C, 0x0D, 0x0E, 0x0F}
	for _, pt := range otherTypes {
		if !PathBytesAreHops(pt) {
			t.Errorf("PathBytesAreHops(0x%02X) should be true", pt)
		}
	}
}

func TestPathHopsTransmitted(t *testing.T) {
	cases := []struct {
		route int
		pt    byte
		want  bool
	}{
		{RouteFlood, 0x02, true},
		{RouteTransportFlood, 0x04, true},
		{RouteDirect, 0x02, false},          // remaining route (removeSelfFromPath)
		{RouteTransportDirect, 0x04, false}, // remaining route
		{RouteFlood, PayloadTRACE, false},   // path bytes are SNR values
		{RouteDirect, PayloadTRACE, false},
		{-1, 0x02, false}, // unknown route type
	}
	for _, c := range cases {
		if got := PathHopsTransmitted(c.route, c.pt); got != c.want {
			t.Errorf("PathHopsTransmitted(%d, 0x%02X) = %v, want %v", c.route, c.pt, got, c.want)
		}
	}
}

func TestEmptyPathHeardOriginator(t *testing.T) {
	cases := []struct {
		route int
		pt    byte
		want  bool
	}{
		{RouteFlood, 0x02, true},
		{RouteTransportFlood, PayloadANON_REQ, true},
		{RouteDirect, PayloadADVERT, true},          // Mesh::sendZeroHop
		{RouteTransportDirect, PayloadADVERT, true}, // transport overload of sendZeroHop
		{RouteDirect, PayloadANON_REQ, false},       // last leg of sendDirect after removeSelfFromPath
		{RouteDirect, 0x02, false},
		{RouteFlood, PayloadTRACE, false},
		{-1, PayloadADVERT, false},
	}
	for _, c := range cases {
		if got := EmptyPathHeardOriginator(c.route, c.pt); got != c.want {
			t.Errorf("EmptyPathHeardOriginator(%d, 0x%02X) = %v, want %v", c.route, c.pt, got, c.want)
		}
	}
}
