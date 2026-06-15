package main

import (
	"strings"
	"testing"

	"github.com/meshcore-analyzer/packetpath"
)

func TestClientReceptionsTableExists(t *testing.T) {
	s := newTestStore(t)
	cols := map[string]bool{}
	rows, err := s.db.Query(`PRAGMA table_info(client_receptions)`)
	if err != nil {
		t.Fatalf("PRAGMA failed: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		cols[name] = true
	}
	for _, want := range []string{"id", "rx_pubkey", "heard_key", "heard_keylen", "rssi", "snr", "lat", "lon", "pos_acc_m", "rx_at", "ingested_at", "src"} {
		if !cols[want] {
			t.Errorf("missing column %q in client_receptions", want)
		}
	}
}

func crF(f float64) *float64 { return &f }
func crI(i int) *int         { return &i }

func TestDeriveHeardKey(t *testing.T) {
	full := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	k, l, src, ok := deriveHeardKey("rx", packetpath.RouteFlood, nil, strings.ToUpper(full), true)
	if !ok || l != 32 || src != "advert" || k != full {
		t.Fatalf("0-hop advert: got k=%q l=%d src=%q ok=%v", k, l, src, ok)
	}
	k, l, src, ok = deriveHeardKey("rx", packetpath.RouteFlood, []string{"aa", "bbccdd"}, "", false)
	if !ok || k != "bbccdd" || l != 3 || src != "rxlog" {
		t.Fatalf("flood path: got k=%q l=%d src=%q ok=%v", k, l, src, ok)
	}
	// DIRECT route: path[last] is the route's far end, not the transmitter — must be rejected.
	if _, _, _, ok = deriveHeardKey("rx", packetpath.RouteDirect, []string{"aa", "bbccdd"}, "", false); ok {
		t.Fatalf("direct-route path must be rejected")
	}
	if _, _, _, ok = deriveHeardKey("rx", packetpath.RouteTransportDirect, []string{"aa", "bbccdd"}, "", false); ok {
		t.Fatalf("transport-direct-route path must be rejected")
	}
	if _, _, _, ok = deriveHeardKey("rx", packetpath.RouteFlood, []string{"aa", "bb"}, "", false); ok {
		t.Fatalf("1-byte last hop should be rejected")
	}
	if _, _, _, ok = deriveHeardKey("tx", packetpath.RouteFlood, []string{"aabbcc"}, "", false); ok {
		t.Fatalf("tx must be rejected")
	}
	if _, _, _, ok = deriveHeardKey("rx", packetpath.RouteFlood, nil, "", false); ok {
		t.Fatalf("no hops + non-advert must be rejected")
	}
}

func TestBuildClientReception(t *testing.T) {
	acc := 8.0
	rec, ok := buildClientReception("companionpk", "rx", packetpath.RouteFlood, []string{"aa", "bbccdd"}, "", false,
		crF(-7.5), crI(-92), 51.05, 3.72, &acc, "2026-06-09T12:00:00Z", "2026-06-09T12:00:01Z")
	if !ok || rec.HeardKey != "bbccdd" || rec.HeardKeyLen != 3 || rec.Src != "rxlog" {
		t.Fatalf("bad reception: %+v ok=%v", rec, ok)
	}
	if _, ok := buildClientReception("c", "rx", packetpath.RouteDirect, []string{"bbccdd"}, "", false,
		crF(-7.5), crI(-92), 51.05, 3.72, nil, "t", "t"); ok {
		t.Fatal("direct-route path must be rejected (not the transmitter)")
	}
	if _, ok := buildClientReception("c", "rx", packetpath.RouteFlood, []string{"bbccdd"}, "", false, nil, nil, 99.0, 3.72, nil, "t", "t"); ok {
		t.Fatal("out-of-range lat must be rejected")
	}
}

func TestInsertClientReceptionRoundTripAndIdempotent(t *testing.T) {
	s := newTestStore(t)
	rec := &ClientReception{
		RxPubkey: "companionpk", HeardKey: "bbccdd", HeardKeyLen: 3, RSSI: crI(-92),
		Lat: 51.05, Lon: 3.72, RxAt: "2026-06-09T12:00:00Z", IngestedAt: "2026-06-09T12:00:01Z", Src: "rxlog",
	}
	if ins, err := s.InsertClientReception(rec); err != nil || !ins {
		t.Fatalf("first insert: ins=%v err=%v", ins, err)
	}
	if ins, err := s.InsertClientReception(rec); err != nil || ins {
		t.Fatalf("second insert should be a no-op: ins=%v err=%v", ins, err)
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM client_receptions`).Scan(&n)
	if n != 1 {
		t.Fatalf("expected 1 row, got %d", n)
	}
}

func TestHandleClientPacketAdvertWritesReception(t *testing.T) {
	s := newTestStore(t)
	advertHex := "11451000D818206D3AAC152C8A91F89957E6D30CA51F36E28790228971C473B755F244F718754CF5EE4A2FD58D944466E42CDED140C66D0CC590183E32BAF40F112BE8F3F2BDF6012B4B2793C52F1D36F69EE054D9A05593286F78453E56C0EC4A3EB95DDA2A7543FCCC00B939CACC009278603902FC12BCF84B706120526F6F6620536F6C6172"
	msg := map[string]interface{}{
		"raw":       advertHex,
		"direction": "rx",
		"timestamp": "2026-06-09T12:00:00Z",
		"origin":    "MyMob",
		"SNR":       -7.0,
		"RSSI":      -92.0,
		"gps":       map[string]interface{}{"lat": 51.05, "lon": 3.72, "acc_m": 8.0},
	}
	handleClientPacket(s, "test", "companionpk", msg, nil)

	var obsName string
	s.db.QueryRow(`SELECT name FROM client_observers WHERE pubkey='companionpk'`).Scan(&obsName)
	if obsName != "MyMob" {
		t.Fatalf("expected client_observers name 'MyMob', got %q", obsName)
	}

	// This fixture is a relayed advert (non-empty path), so by the capture HARD
	// RULE we record the directly-heard LAST hop (multibyte), not the originator.
	// The 0-hop advert→full-pubkey branch is covered by TestDeriveHeardKey.
	var n, keylen int
	var src string
	if err := s.db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(heard_keylen),0), COALESCE(MAX(src),'') FROM client_receptions WHERE rx_pubkey='companionpk'`).Scan(&n, &keylen, &src); err != nil {
		t.Fatal(err)
	}
	if n != 1 || keylen < 2 || src != "rxlog" {
		t.Fatalf("expected 1 rxlog reception (multibyte last hop), got n=%d keylen=%d src=%q", n, keylen, src)
	}

	// No GPS → no row.
	handleClientPacket(s, "test", "companion2", map[string]interface{}{"raw": advertHex, "direction": "rx"}, nil)
	var n2 int
	s.db.QueryRow(`SELECT COUNT(*) FROM client_receptions WHERE rx_pubkey='companion2'`).Scan(&n2)
	if n2 != 0 {
		t.Fatalf("packet without gps must be dropped, got %d rows", n2)
	}
}
