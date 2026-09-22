package main

import (
	"testing"
)

// Coverage for the direct-RF attribution rule behind the node detail
// "Heard By" card.
//
// The card used to credit every observer that saw traffic *involving* a
// node — originated by it, addressed to it, or relayed through it — and
// printed an SNR/RSSI next to each. Those signal numbers belong to
// whichever node last transmitted the copy the observer received, not to
// the node the row names.
//
// The rule these tests pin, derived from the firmware:
//
//   - Only flood routes carry a travelled path. For ROUTE_TYPE_DIRECT the
//     forwarder removes itself from the front before retransmitting
//     (firmware Mesh.cpp:103 removeSelfFromPath), so path_json is the
//     REMAINING route and says nothing about who was heard.
//   - On a flood, the last hop is the node the observer heard on air
//     (firmware Mesh.cpp:349 — a repeater appends its own hash before
//     retransmitting).
//   - An empty flood path means the originator was heard directly. Only
//     ADVERTs carry the originator's pubkey in the clear.
//   - An ambiguous hop prefix credits nobody. Same gate as
//     resolvePathForObsColdLoad: under-attribute rather than guess.
//
// Fixture pubkeys are lowercase hex placeholders only (AGENTS.md PII rule).

// directHeardNodes are three repeaters, two of which collide on the 1-byte
// prefix "a4" — the shape that produced the original report, where a 433 MHz
// repeater was credited with 868 MHz traffic it could not have relayed.
var directHeardNodes = []nodeInfo{
	{PublicKey: "a433ec0000000000000000000000000000000000000000000000000000000001", Role: "repeater", Name: "collideA"},
	{PublicKey: "a4ef4b0000000000000000000000000000000000000000000000000000000002", Role: "repeater", Name: "collideB"},
	{PublicKey: "bb11220000000000000000000000000000000000000000000000000000000003", Role: "repeater", Name: "unique"},
}

const (
	dhCollideA = "a433ec0000000000000000000000000000000000000000000000000000000001"
	dhUnique   = "bb11220000000000000000000000000000000000000000000000000000000003"
)

func dhRoute(rt int) *int { return &rt }

func dhTx(routeType int, payloadType int, decoded string) *StoreTx {
	return &StoreTx{
		RouteType:   dhRoute(routeType),
		PayloadType: dhRoute(payloadType),
		DecodedJSON: decoded,
	}
}

func TestLastPathHop(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"empty array", "[]", ""},
		{"empty string", "", ""},
		{"single hop", `["A4"]`, "A4"},
		{"two hops", `["A4","F1"]`, "F1"},
		{"two-byte hops", `["A433","1403"]`, "1403"},
		{"long path", `["66","E8","EA","DE","7C","CA"]`, "CA"},
		{"unterminated", `["A4`, ""},
		{"not json", "garbage", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := lastPathHop(tc.in); got != tc.want {
				t.Fatalf("lastPathHop(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDirectHeardNode(t *testing.T) {
	pm := buildPrefixMap(directHeardNodes)
	advert := `{"type":"ADVERT","pubKey":"` + dhUnique + `"}`

	cases := []struct {
		name     string
		tx       *StoreTx
		pathJSON string
		want     string
	}{
		{
			name:     "flood, unique two-byte last hop, credits that node",
			tx:       dhTx(RouteFlood, PayloadTXT_MSG, ""),
			pathJSON: `["A433","BB11"]`,
			want:     dhUnique,
		},
		{
			name:     "transport flood counts as flood",
			tx:       dhTx(RouteTransportFlood, PayloadTXT_MSG, ""),
			pathJSON: `["BB11"]`,
			want:     dhUnique,
		},
		{
			name:     "flood, node is an earlier hop, not the last one, credits nobody here",
			tx:       dhTx(RouteFlood, PayloadTXT_MSG, ""),
			pathJSON: `["BB11","A433"]`,
			want:     dhCollideA,
		},
		{
			name:     "ambiguous one-byte last hop credits nobody",
			tx:       dhTx(RouteFlood, PayloadTXT_MSG, ""),
			pathJSON: `["66","E8","A4"]`,
			want:     "",
		},
		{
			name:     "unknown last hop credits nobody",
			tx:       dhTx(RouteFlood, PayloadTXT_MSG, ""),
			pathJSON: `["9999"]`,
			want:     "",
		},
		{
			name:     "direct route never credits, even when the node is the last entry",
			tx:       dhTx(RouteDirect, PayloadTXT_MSG, ""),
			pathJSON: `["A433","BB11"]`,
			want:     "",
		},
		{
			name:     "transport direct never credits",
			tx:       dhTx(RouteTransportDirect, PayloadTXT_MSG, ""),
			pathJSON: `["BB11"]`,
			want:     "",
		},
		{
			// Mesh::sendZeroHop sets ROUTE_TYPE_DIRECT and path_len = 0, and
			// repeaters send their periodic local advert that way. An ADVERT
			// arriving direct with an empty path cannot have been forwarded,
			// so the observer heard the advertiser itself.
			name:     "zero-hop advert on a direct route credits the advertiser",
			tx:       dhTx(RouteDirect, PayloadADVERT, advert),
			pathJSON: "",
			want:     dhUnique,
		},
		{
			name:     "zero-hop advert on a transport-direct route credits the advertiser",
			tx:       dhTx(RouteTransportDirect, PayloadADVERT, advert),
			pathJSON: `[]`,
			want:     dhUnique,
		},
		{
			// The zero-hop exception is only about adverts. Any other payload
			// arriving direct with an empty path still identifies nobody.
			name:     "direct route, empty path, not an advert, credits nobody",
			tx:       dhTx(RouteDirect, PayloadTXT_MSG, ""),
			pathJSON: "",
			want:     "",
		},
		{
			// A direct route with a path is the REMAINING route, so the last
			// entry is where the packet is going, not who transmitted it.
			name:     "direct advert with a non-empty path credits nobody",
			tx:       dhTx(RouteDirect, PayloadADVERT, advert),
			pathJSON: `["A433","BB11"]`,
			want:     "",
		},
		{
			name:     "flood advert with empty path credits the originator",
			tx:       dhTx(RouteFlood, PayloadADVERT, advert),
			pathJSON: `[]`,
			want:     dhUnique,
		},
		{
			name:     "flood non-advert with empty path credits nobody: originator unknown",
			tx:       dhTx(RouteFlood, PayloadTXT_MSG, ""),
			pathJSON: `[]`,
			want:     "",
		},
		{
			name:     "missing route type credits nobody",
			tx:       &StoreTx{PayloadType: dhRoute(PayloadTXT_MSG)},
			pathJSON: `["BB11"]`,
			want:     "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := &StoreObs{ObserverID: "obs1", PathJSON: tc.pathJSON}
			if got := directHeardNode(tc.tx, obs, pm); got != tc.want {
				t.Fatalf("directHeardNode = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDirectHeardNode_AmbiguousPrefixRegression pins the reported case: a
// long 868 MHz flood path whose one-byte "A4" hop matches two repeaters must
// not credit either of them, however plausible the geo/affinity tiers of
// resolveWithContext would find one.
func TestDirectHeardNode_AmbiguousPrefixRegression(t *testing.T) {
	pm := buildPrefixMap(directHeardNodes)
	tx := dhTx(RouteFlood, PayloadTXT_MSG, "")
	obs := &StoreObs{
		ObserverID: "obs868",
		PathJSON:   `["A4","F1","AE","6A","77","5C","29","ED","4D","3F","6A","E7","6A","C2","68"]`,
	}
	if got := directHeardNode(tx, obs, pm); got != "" {
		t.Fatalf("ambiguous A4 hop credited %q; the prefix has %d candidates and must credit nobody",
			got, len(pm.relayCandidates("a4")))
	}
}

// TestDirectHeardNode_ListenerNeverCredited: an observer that reported
// repeat:off cannot have retransmitted the packet, so it must not survive as
// a last-hop candidate (#1290 parity — relayCandidates already filters it).
func TestDirectHeardNode_ListenerNeverCredited(t *testing.T) {
	pm := buildPrefixMap(directHeardNodes)
	pm.markNonRelay([]string{dhUnique})
	tx := dhTx(RouteFlood, PayloadTXT_MSG, "")
	obs := &StoreObs{ObserverID: "obs1", PathJSON: `["BB11"]`}
	if got := directHeardNode(tx, obs, pm); got != "" {
		t.Fatalf("listener-only node credited as last hop: %q", got)
	}
}

func TestComputeDirectHeardAggregates(t *testing.T) {
	snr := func(v float64) *float64 { return &v }
	tx := dhTx(RouteFlood, PayloadTXT_MSG, "")
	tx.Observations = []*StoreObs{
		{ObserverID: "obsA", ObserverName: "A", PathJSON: `["BB11"]`, SNR: snr(10), RSSI: snr(-50)},
		{ObserverID: "obsA", ObserverName: "A", PathJSON: `["BB11"]`, SNR: snr(20), RSSI: snr(-70)},
		{ObserverID: "obsB", ObserverName: "B", PathJSON: `["BB11","A433"]`, SNR: snr(5)},
		// Ambiguous last hop: contributes to nobody.
		{ObserverID: "obsC", ObserverName: "C", PathJSON: `["A4"]`, SNR: snr(1)},
	}

	idx := buildDirectHeardIndex([]*StoreTx{tx}, buildPrefixMap(directHeardNodes))

	byObs := idx[dhUnique]
	if len(byObs) != 1 {
		t.Fatalf("unique node has %d direct observers, want 1: %#v", len(byObs), byObs)
	}
	a := byObs["obsA"]
	if a == nil || a.Count != 2 {
		t.Fatalf("obsA aggregate = %#v, want Count 2", a)
	}
	if a.SNRCount != 2 || a.SNRSum != 30 {
		t.Fatalf("obsA SNR = %v over %d, want 30 over 2", a.SNRSum, a.SNRCount)
	}
	if a.RSSICount != 2 || a.RSSISum != -120 {
		t.Fatalf("obsA RSSI = %v over %d, want -120 over 2", a.RSSISum, a.RSSICount)
	}

	if got := len(idx[dhCollideA]); got != 1 {
		t.Fatalf("collideA has %d direct observers, want 1 (obsB heard it as last hop)", got)
	}
	for pk, m := range idx {
		if _, ok := m["obsC"]; ok {
			t.Fatalf("observer with an ambiguous last hop was credited to %s", pk)
		}
	}
}

func TestBuildDirectObserverRowsSortedAndAveraged(t *testing.T) {
	byObs := map[string]*directHeardAgg{
		"low":  {ObserverName: "low", Count: 1, SNRSum: 3, SNRCount: 1},
		"high": {ObserverName: "high", Count: 9, SNRSum: 18, SNRCount: 2, RSSISum: -100, RSSICount: 2},
		"none": {ObserverName: "none", Count: 4},
	}
	rows := buildDirectObserverRows(byObs, nil, nil)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	if rows[0].ObserverID != "high" || rows[1].ObserverID != "none" || rows[2].ObserverID != "low" {
		t.Fatalf("rows not sorted by packet count desc: %#v", rows)
	}
	if rows[0].AvgSNR == nil || *rows[0].AvgSNR != 9 {
		t.Fatalf("avgSnr = %v, want 9", rows[0].AvgSNR)
	}
	if rows[0].AvgRSSI == nil || *rows[0].AvgRSSI != -50 {
		t.Fatalf("avgRssi = %v, want -50", rows[0].AvgRSSI)
	}
	if rows[1].AvgSNR != nil || rows[1].AvgRSSI != nil {
		t.Fatalf("observer with no signal samples must report null, got %#v", rows[1])
	}
}

// TestBuildDirectObserverRows_CanRelayTriState mirrors the nodes.js badge:
// nil means "no repeat field ever seen", false means listener, true means
// confirmed repeater (PR #1624).
func TestBuildDirectObserverRows_CanRelayTriState(t *testing.T) {
	byObs := map[string]*directHeardAgg{
		"unknown":  {Count: 3},
		"listener": {Count: 2},
		"repeater": {Count: 1},
	}
	seen := map[string]struct{}{"listener": {}, "repeater": {}}
	nonRelay := map[string]struct{}{"listener": {}}
	rows := buildDirectObserverRows(byObs, nonRelay, seen)

	got := map[string]*bool{}
	for _, r := range rows {
		got[r.ObserverID] = r.CanRelay
	}
	if got["unknown"] != nil {
		t.Fatalf("unknown observer must report nil can_relay, got %v", *got["unknown"])
	}
	if got["listener"] == nil || *got["listener"] {
		t.Fatalf("listener must report can_relay false, got %v", got["listener"])
	}
	if got["repeater"] == nil || !*got["repeater"] {
		t.Fatalf("repeater must report can_relay true, got %v", got["repeater"])
	}
}
