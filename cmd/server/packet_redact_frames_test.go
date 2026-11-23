package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/gorilla/mux"
)

// The kept head is its own allocation: a substring would keep the whole
// exact frame (position and signature) reachable from the store, and
// estimateStoreTxBytes would not see it.
func TestRedactHeadDoesNotRetainFrame(t *testing.T) {
	f := redactTestFuzzer(t)
	raw, dec := redactTestAdvert(t, RouteFlood, nil, 0x42, []byte{1, 2, 3, 4}, 0x92, 48.1, 11.5, "Rpt")
	raw = string([]byte(raw)) // a fresh backing array of its own
	_, outDec, hidden := redactLocatedAdvert(f, intp(PayloadADVERT), raw, dec)
	if hidden == nil || hidden.head == "" {
		t.Fatal("not hidden")
	}
	within := func(sub, of string) bool {
		if len(sub) == 0 || len(of) == 0 {
			return false
		}
		p, lo := uintptr(unsafe.Pointer(unsafe.StringData(sub))), uintptr(unsafe.Pointer(unsafe.StringData(of)))
		return p >= lo && p < lo+uintptr(len(of))
	}
	if within(hidden.head, raw) {
		t.Error("the head shares the frame's backing array: the exact frame stays reachable")
	}
	if within(outDec, dec) {
		t.Error("the served JSON shares the stored JSON's backing array")
	}
	tx := &StoreTx{hidden: hidden, DecodedJSON: outDec}
	without := estimateStoreTxBytes(&StoreTx{DecodedJSON: outDec})
	if got := estimateStoreTxBytes(tx); got != without+int64(hiddenFrameBaseBytes+len(hidden.head)) {
		t.Errorf("estimateStoreTxBytes = %d, want %d (the head is counted)", got, without+int64(hiddenFrameBaseBytes+len(hidden.head)))
	}
}

func TestRedactFrameFieldsServed(t *testing.T) {
	f := redactTestFuzzer(t)
	tport, tportDec := redactTestAdvert(t, RouteTransportFlood, []byte{1, 2, 3, 4}, 0x42, []byte{0xAA, 0xBB, 0xCC, 0xDD}, 0x92, 48.1, 11.5, "T")
	_, _, hidden := redactLocatedAdvert(f, intp(PayloadADVERT), tport, tportDec)
	if hidden == nil {
		t.Fatal("not hidden")
	}
	// header(1) + transport codes(4) + path byte(1) + 2 hops of 2 bytes.
	if want := tport[:2*10]; hidden.head != want || hidden.bytes != len(tport)/2 {
		t.Errorf("head %q / %d bytes, want %q / %d", hidden.head, hidden.bytes, want, len(tport)/2)
	}
	m := map[string]interface{}{}
	markLocationHidden(m, &StoreTx{hidden: hidden})
	if m["location_hidden"] != true || m["frame_bytes"] != len(tport)/2 || m["frame_head"] != tport[:20] {
		t.Errorf("served fields %v", m)
	}
	resp := mapSliceToObservations([]map[string]interface{}{m})[0]
	if !resp.LocationHidden || resp.FrameBytes != len(tport)/2 || resp.FrameHead != tport[:20] {
		t.Errorf("typed observation %+v", resp)
	}
	plain := map[string]interface{}{}
	markLocationHidden(plain, &StoreTx{RawHex: "1100"})
	if len(plain) != 0 {
		t.Errorf("a packet that is not hidden gained %v", plain)
	}
	if b, _ := json.Marshal(mapSliceToObservations([]map[string]interface{}{{"id": 1}})[0]); strings.Contains(string(b), "frame_") || strings.Contains(string(b), "location_hidden") {
		t.Errorf("typed shape of a plain observation names the hidden fields: %s", b)
	}
}

// redactParityDB seeds located adverts over a flood route with 2-byte hops,
// a transport-flood route (head offset 5) and a direct zero-hop route that
// declares its hash size, each heard by one observer, plus a channel message.
func redactParityDB(t *testing.T) (*DB, []string) {
	t.Helper()
	db := setupTestDB(t)
	now := time.Now().UTC()
	if _, err := db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen) VALUES ('obs1', 'Obs', 'SJC', ?, ?)`,
		now.Format(time.RFC3339), now.Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	type pkt struct{ raw, dec, path string }
	flood, floodDec := redactTestAdvert(t, RouteFlood, nil, 0x42, []byte{0xAB, 0xCD, 0xEF, 0x01}, 0x92, 48.137154, 11.576124, "Flood")
	tport, tportDec := redactTestAdvert(t, RouteTransportFlood, []byte{1, 2, 3, 4}, 0x81, []byte{0x12, 0x34, 0x56}, 0x92, 48.2, 11.6, "Transport")
	direct, directDec := redactTestAdvert(t, RouteDirect, nil, 0x40, nil, 0x92, 48.3, 11.7, "Direct")
	pkts := []pkt{
		{flood, floodDec, `["ABCD","EF01"]`},
		{tport, tportDec, `["123456"]`},
		{direct, directDec, `[]`},
	}
	var raws []string
	for i, p := range pkts {
		seen := now.Add(time.Duration(i-10) * time.Minute)
		res, err := db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json)
			VALUES (?, ?, ?, ?, 4, 1, ?)`, p.raw, ComputeContentHash(p.raw), seen.Format(time.RFC3339), int(mustHexByte(t, p.raw)&3), p.dec)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		if _, err := db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, direction, snr, rssi, path_json, timestamp, raw_hex)
			VALUES (?, 1, 'rx', 5.5, -90, ?, ?, ?)`, id, p.path, seen.Unix(), p.raw); err != nil {
			t.Fatal(err)
		}
		raws = append(raws, p.raw)
	}
	msg := "1542ABCDEF01" + strings.Repeat("5A", 40)
	res, err := db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json)
		VALUES (?, ?, ?, 1, 5, 1, '{"type":"GRP_TXT","channelHashHex":"5A"}')`, msg, ComputeContentHash(msg), now.Add(-time.Minute).Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if _, err := db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, direction, snr, rssi, path_json, timestamp, raw_hex)
		VALUES (?, 1, 'rx', 4, -95, '["ABCD","EF01"]', ?, ?)`, id, now.Add(-time.Minute).Unix(), msg); err != nil {
		t.Fatal(err)
	}
	return db, raws
}

func mustHexByte(t *testing.T, s string) byte {
	b, ok := hexByte(s, 0)
	if !ok {
		t.Fatalf("not hex: %q", s)
	}
	return b
}

// TestRedactAnalyticsParity: hiding located adverts changes no analytics
// number: hash sizes (per node and the distribution), RF packet sizes and
// relay airtime come out identical with privacy on and off.
func TestRedactAnalyticsParity(t *testing.T) {
	results := map[bool]map[string]string{}
	for _, on := range []bool{false, true} {
		db, raws := redactParityDB(t)
		if on {
			db.setLocationFuzzer(redactTestFuzzer(t))
		}
		s := NewPacketStore(db, nil)
		if err := s.Load(); err != nil {
			t.Fatal(err)
		}
		hidden := 0
		for _, tx := range s.packets {
			if tx.locationHidden() {
				hidden++
			}
		}
		if want := map[bool]int{false: 0, true: len(raws)}[on]; hidden != want {
			t.Fatalf("on=%v: %d hidden adverts, want %d", on, hidden, want)
		}
		got := map[string]string{}
		put := func(name string, v interface{}) {
			b, err := json.Marshal(v)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			got[name] = string(b)
		}
		s.mu.RLock()
		put("nodeHashSizeInfo", s.computeNodeHashSizeInfo())
		s.mu.RUnlock()
		hs := s.computeAnalyticsHashSizes("", "")
		// multiByteNodes and topHops are sorted by count only; order ties so
		// the comparison is of the numbers, not of map iteration order.
		for _, k := range [][2]string{{"multiByteNodes", "pubkey"}, {"topHops", "hex"}} {
			if l, ok := hs[k[0]].([]map[string]interface{}); ok {
				sort.SliceStable(l, func(i, j int) bool { return fmt.Sprint(l[i][k[1]]) < fmt.Sprint(l[j][k[1]]) })
			}
		}
		put("hashSizes", hs)
		rf := s.computeAnalyticsRF("", "", TimeWindow{})
		put("rf.packetSizes", []interface{}{rf["packetSizes"], rf["sizeDistribution"], rf["avgPacketSize"], rf["minPacketSize"], rf["maxPacketSize"]})
		put("relayAirtime", s.computeRelayAirtimeShare(TimeWindow{}))
		results[on] = got
	}
	for name, off := range results[false] {
		if on := results[true][name]; on != off {
			t.Errorf("%s differs with privacy on:\n off %s\n  on %s", name, off, on)
		}
	}
	if !strings.Contains(results[false]["hashSizes"], `"2"`) {
		t.Errorf("fixture produced no 2-byte hash size; parity proves nothing: %s", results[false]["hashSizes"])
	}
}

// TestRedactTruncatedAdvertNeverServed: a located advert cut inside its
// signature (102 bytes, no flags byte) loads hidden and is served without
// any frame or signature byte by /api/packets/{hash} (observations
// included) and by the live-feed builder.
func TestRedactTruncatedAdvertNeverServed(t *testing.T) {
	full, _ := redactTestAdvert(t, RouteFlood, nil, 0x00, nil, 0x92, 48.137154, 11.576124, "Cut")
	cut := full[:2*102]
	sig := strings.ToLower(full[2*(2+36) : 2*102]) // the 64 signature bytes it holds
	pkt, err := DecodePacket(cut, false)
	if err != nil {
		t.Fatal(err)
	}
	decB, _ := json.Marshal(pkt.Payload)
	dec := string(decB)
	if !strings.Contains(strings.ToLower(dec), sig[:32]) {
		t.Fatalf("fixture: the decoder no longer keeps the payload of a short advert: %s", dec)
	}

	db := setupTestDB(t)
	db.hasObsRawHex = true
	cfg := &Config{Port: 3000}
	cfg.setLocationFuzzer(redactTestFuzzer(t))
	srv := NewServer(db, cfg, NewHub())
	now := time.Now().UTC()
	for _, id := range []string{"obsA", "obsB"} {
		if _, err := db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen) VALUES (?, ?, 'SJC', ?, ?)`,
			id, id, now.Format(time.RFC3339), now.Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	insert := func(hash string, seen time.Time) {
		res, err := db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json)
			VALUES (?, ?, ?, 1, 4, 1, ?)`, cut, hash, seen.Format(time.RFC3339), dec)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		for i := 0; i < 2; i++ {
			if _, err := db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, direction, snr, rssi, path_json, timestamp, raw_hex)
				VALUES (?, ?, 'rx', 5, -90, '[]', ?, ?)`, id, i+1, seen.Unix()+int64(i), cut); err != nil {
				t.Fatal(err)
			}
		}
	}
	insert("c0ffee0000000001", now.Add(-time.Hour))
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	srv.store = store
	tx := store.byHash["c0ffee0000000001"]
	if tx == nil || !tx.locationHidden() || tx.RawHex != "" {
		t.Fatalf("truncated advert loaded exact: %+v", tx)
	}
	if tx.frameBytes() != 102 || tx.frameHead() != cut[:4] {
		t.Errorf("frame %d bytes / head %q, want 102 / %q", tx.frameBytes(), tx.frameHead(), cut[:4])
	}

	router := mux.NewRouter()
	srv.RegisterRoutes(router)
	req := httptest.NewRequest("GET", "/api/packets/c0ffee0000000001", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("packet detail: %d %s", w.Code, w.Body.String())
	}
	body := strings.ToLower(w.Body.String())
	if strings.Contains(body, sig[:32]) || strings.Contains(body, strings.ToLower(cut[:40])) {
		t.Errorf("packet detail serves frame or signature bytes: %s", w.Body.String())
	}
	var detail struct {
		Packet       map[string]interface{}   `json:"packet"`
		Observations []map[string]interface{} `json:"observations"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Packet["location_hidden"] != true || detail.Packet["raw_hex"] != nil || detail.Packet["frame_bytes"] != float64(102) {
		t.Errorf("packet %v", detail.Packet)
	}
	if len(detail.Observations) != 2 {
		t.Fatalf("%d observations, want 2", len(detail.Observations))
	}
	for _, o := range detail.Observations {
		if o["raw_hex"] != nil || o["location_hidden"] != true || o["frame_head"] != cut[:4] {
			t.Errorf("observation %v", o)
		}
	}

	// The live feed: a new copy arrives through the poll path.
	insert("c0ffee0000000002", now)
	maps, _, err := store.ingestNewFromDB(store.maxTxID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(maps) == 0 {
		t.Fatal("poll broadcast nothing")
	}
	b, err := json.Marshal(maps)
	if err != nil {
		t.Fatal(err)
	}
	feed := strings.ToLower(string(b))
	if strings.Contains(feed, sig[:32]) || strings.Contains(feed, strings.ToLower(cut[:40])) {
		t.Errorf("live feed serves frame or signature bytes: %s", b)
	}
	if !strings.Contains(feed, `"location_hidden":true`) || !strings.Contains(feed, `"frame_bytes":102`) {
		t.Errorf("live feed does not mark the hidden advert: %s", b)
	}
}
