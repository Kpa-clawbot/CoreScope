package main

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/packetpath"
)

// redactTestAdvert builds an ADVERT frame (route, path bytes) whose appdata
// has the given flags; lat/lon are written when flags carry the location bit.
// It returns the frame hex and the decoded JSON the ingestor would store.
func redactTestAdvert(t testing.TB, route byte, transport []byte, pathByte byte, hops []byte, flags byte, lat, lon float64, name string) (string, string) {
	t.Helper()
	payload := make([]byte, 0, 140)
	for i := 0; i < 32; i++ {
		payload = append(payload, byte(0xA0+i)^name[0]) // one pubkey per name
	}
	payload = binary.LittleEndian.AppendUint32(payload, 1760000000)
	for i := 0; i < 64; i++ {
		payload = append(payload, byte(i*3+1))
	}
	payload = append(payload, flags)
	if flags&advertLocationFlag != 0 {
		payload = binary.LittleEndian.AppendUint32(payload, uint32(int32(lat*1e6)))
		payload = binary.LittleEndian.AppendUint32(payload, uint32(int32(lon*1e6)))
	}
	payload = append(payload, name...)
	frame := []byte{PayloadADVERT<<2 | route}
	frame = append(frame, transport...)
	frame = append(frame, pathByte)
	frame = append(frame, hops...)
	frame = append(frame, payload...)
	raw := strings.ToUpper(hex.EncodeToString(frame))
	pkt, err := DecodePacket(raw, false)
	if err != nil {
		t.Fatalf("test advert does not decode: %v", err)
	}
	valid := true
	pkt.Payload.SignatureValid = &valid
	b, err := json.Marshal(pkt.Payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw, string(b)
}

func redactTestFuzzer(t testing.TB) *locationFuzzer {
	return newTestLocFuzzer(t, 1000, strings.Repeat("k", 40))
}

func intp(v int) *int { return &v }

func decodedKeys(t *testing.T, s string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("served decoded_json is not JSON: %v: %s", err, s)
	}
	return m
}

func TestRedactLocatedAdvert(t *testing.T) {
	f := redactTestFuzzer(t)
	raw, dec := redactTestAdvert(t, RouteFlood, nil, 0x41, []byte{0xAB, 0xCD}, 0x92, 48.137154, 11.576124, "Rpt")

	outRaw, outDec, hidden := redactLocatedAdvert(f, intp(PayloadADVERT), raw, dec)
	if outRaw != "" || hidden == nil {
		t.Fatalf("located advert kept its frame: raw=%q hidden=%v", outRaw, hidden)
	}
	m := decodedKeys(t, outDec)
	for _, k := range locatedAdvertHiddenKeys {
		if _, ok := m[k]; ok {
			t.Errorf("served decoded_json keeps %q", k)
		}
	}
	for _, k := range []string{"type", "pubKey", "name", "flags", "timestamp", "signatureValid"} {
		if _, ok := m[k]; !ok {
			t.Errorf("served decoded_json lost %q", k)
		}
	}
	var want, got map[string]interface{}
	json.Unmarshal([]byte(dec), &want)
	json.Unmarshal([]byte(outDec), &got)
	for _, k := range locatedAdvertHiddenKeys {
		delete(want, k)
	}
	if fmt.Sprint(want) != fmt.Sprint(got) {
		t.Errorf("served decoded_json is not the stored one minus the hidden keys:\n got %v\nwant %v", got, want)
	}
	if strings.Contains(outDec, "48.13") || strings.Contains(outDec, "11.57") {
		t.Errorf("served decoded_json still names the position: %s", outDec)
	}
	// The kept head is the header, path byte and path bytes only, and still
	// answers the hash-size analytics exactly as the frame did.
	if hidden.head != raw[:8] || hidden.bytes != len(raw)/2 {
		t.Errorf("hidden frame = %+v, want head %q and %d bytes", *hidden, raw[:8], len(raw)/2)
	}
	if packetpath.HashSize(hidden.head) != packetpath.HashSize(raw) {
		t.Errorf("hash size from head %d, from frame %d", packetpath.HashSize(hidden.head), packetpath.HashSize(raw))
	}
	rt1, pb1, ok1 := packetpath.PathByte(hidden.head)
	rt2, pb2, ok2 := packetpath.PathByte(raw)
	if rt1 != rt2 || pb1 != pb2 || ok1 != ok2 {
		t.Errorf("PathByte from head (%d,%#x,%v) != from frame (%d,%#x,%v)", rt1, pb1, ok1, rt2, pb2, ok2)
	}

	// Feature off: identity, same strings.
	r2, d2, h2 := redactLocatedAdvert(nil, intp(PayloadADVERT), raw, dec)
	if r2 != raw || d2 != dec || h2 != nil {
		t.Error("feature off changed a located advert")
	}
}

func TestRedactLocatedAdvertCases(t *testing.T) {
	f := redactTestFuzzer(t)
	located, locatedDec := redactTestAdvert(t, RouteFlood, nil, 0x00, nil, 0x92, 48.1, 11.5, "A")
	zero, zeroDec := redactTestAdvert(t, RouteFlood, nil, 0x00, nil, 0x92, 0, 0, "Z")
	noLoc, noLocDec := redactTestAdvert(t, RouteFlood, nil, 0x00, nil, 0x82, 0, 0, "N")
	tport, tportDec := redactTestAdvert(t, RouteTransportFlood, []byte{1, 2, 3, 4}, 0x81, []byte{0xAA, 0xBB, 0xCC}, 0x92, 48.1, 11.5, "T")
	// Truncated frames (header + path byte 0x00 + n payload bytes).
	trunc := func(n int) string {
		return strings.ToUpper(hex.EncodeToString(append([]byte{PayloadADVERT<<2 | RouteFlood, 0}, make([]byte, n)...)))
	}
	// The 102-byte frame of the security review: signature, no flags byte.
	cut102 := located[:2*102]

	cases := []struct {
		name     string
		pt       *int
		raw, dec string
		hide     bool
		head     string
	}{
		{"located", intp(PayloadADVERT), located, locatedDec, true, located[:4]},
		{"located at 0,0", intp(PayloadADVERT), zero, zeroDec, true, zero[:4]},
		{"no location keeps its bytes", intp(PayloadADVERT), noLoc, noLocDec, false, ""},
		{"transport route head", intp(PayloadADVERT), tport, tportDec, true, tport[:18]},
		{"payload type from the header", nil, located, locatedDec, true, located[:4]},
		{"frame only", intp(PayloadADVERT), located, "", true, located[:4]},
		{"JSON only, lat key", intp(PayloadADVERT), "", `{"type":"ADVERT","lat":1,"lon":2,"signature":"00"}`, true, ""},
		{"JSON only, untyped", nil, "", `{"type":"ADVERT","lat":1,"lon":2}`, true, ""},
		{"JSON flag with spaces", intp(PayloadADVERT), "", `{"type":"ADVERT", "flags": {"hasLocation" : true}}`, true, ""},
		{"JSON flag false", intp(PayloadADVERT), "", `{"type":"ADVERT","flags":{"hasLocation":false},"signature":"00"}`, false, ""},
		{"JSON escaped key", intp(PayloadADVERT), "", `{"type":"ADVERT","l\u0061t":1}`, true, ""},
		{"JSON duplicate signature", intp(PayloadADVERT), "", `{"signature":"00","lat":1,"signature":"11"}`, true, ""},
		{"JSON not an object", intp(PayloadADVERT), "", `["lat"]`, true, ""},
		{"unreadable JSON", intp(PayloadADVERT), noLoc, `{"type":`, true, noLoc[:4]},
		{"odd-length frame", intp(PayloadADVERT), noLoc + "0", noLocDec, true, noLoc[:4]},
		{"bad hex at the flags", intp(PayloadADVERT), noLoc[:2*102] + "ZZ" + noLoc[2*103:], noLocDec, true, noLoc[:4]},
		{"truncated in the signature", intp(PayloadADVERT), trunc(60), `{"type":"ADVERT","error":"too short for advert","raw":"00"}`, true, "1100"},
		{"truncated, first signature byte", intp(PayloadADVERT), trunc(37), "", true, "1100"},
		{"truncated, 102-byte frame", intp(PayloadADVERT), cut102, "", true, located[:4]},
		{"truncated, 102-byte frame, untyped", nil, cut102, "", true, located[:4]},
		{"truncated before the signature", intp(PayloadADVERT), trunc(36), `{"type":"ADVERT","error":"too short for advert"}`, false, ""},
		{"truncated in the path", intp(PayloadADVERT), "1102AB", "", false, ""},
		{"not an advert", intp(PayloadGRP_TXT), located, locatedDec, false, ""},
		{"untyped non-advert", nil, "1500AABB", `{"type":"GRP_TXT"}`, false, ""},
		{"nothing stored", intp(PayloadADVERT), "", "", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			raw, dec, hidden := redactLocatedAdvert(f, c.pt, c.raw, c.dec)
			if !c.hide {
				if hidden != nil || raw != c.raw || dec != c.dec {
					t.Fatalf("changed a packet it must keep: raw=%q dec=%q hidden=%v", raw, dec, hidden)
				}
				return
			}
			if hidden == nil || raw != "" {
				t.Fatalf("not hidden: raw=%q hidden=%v", raw, hidden)
			}
			if hidden.head != c.head {
				t.Errorf("head %q, want %q", hidden.head, c.head)
			}
			if dec != "" {
				m := decodedKeys(t, dec)
				for _, k := range locatedAdvertHiddenKeys {
					if _, ok := m[k]; ok {
						t.Errorf("served decoded_json keeps %q: %s", k, dec)
					}
				}
			}
		})
	}
}

func TestRedactPacketMapAndObservationFrame(t *testing.T) {
	f := redactTestFuzzer(t)
	raw, dec := redactTestAdvert(t, RouteFlood, nil, 0x00, nil, 0x92, 48.1, 11.5, "A")
	m := map[string]interface{}{"hash": "h", "raw_hex": raw, "decoded_json": dec, "payload_type": PayloadADVERT}
	if !redactPacketMap(f, m) || m["raw_hex"] != nil || m["location_hidden"] != true {
		t.Fatalf("DB row not redacted: %v", m)
	}
	if s, _ := m["decoded_json"].(string); strings.Contains(s, `"lat"`) || strings.Contains(s, `"signature"`) {
		t.Errorf("DB row decoded_json keeps the position: %s", s)
	}
	other := map[string]interface{}{"raw_hex": "1500AA", "decoded_json": `{}`, "payload_type": PayloadGRP_TXT}
	if redactPacketMap(f, other) || other["raw_hex"] != "1500AA" || other["location_hidden"] != nil {
		t.Errorf("non-advert DB row changed: %v", other)
	}
	off := map[string]interface{}{"raw_hex": raw, "decoded_json": dec, "payload_type": PayloadADVERT}
	if redactPacketMap(nil, off) || off["raw_hex"] != raw {
		t.Error("feature off changed a DB row")
	}
	if redactObservationFrame(f, raw) != "" {
		t.Error("an observation frame of a located advert is served")
	}
	if redactObservationFrame(f, "1500AA") != "1500AA" || redactObservationFrame(nil, raw) != raw {
		t.Error("observation frame changed where it must not")
	}
}

// TestRedactStoreLoadAndRewrite: with the feature on the store holds only
// the served form after Load, keeps the frame header for the analytics, and
// redacts a rewrite-feed update of the same transmission too. Off, the load
// is unchanged.
func TestRedactStoreLoadAndRewrite(t *testing.T) {
	raw, dec := redactTestAdvert(t, RouteFlood, nil, 0x41, []byte{0xAB, 0xCD}, 0x92, 48.137154, 11.576124, "Rpt")
	for _, on := range []bool{false, true} {
		t.Run(fmt.Sprintf("on=%v", on), func(t *testing.T) {
			db, w := txRewriteFixture(t, true)
			if _, err := w.Exec(`UPDATE transmissions SET payload_type=4, raw_hex=?, decoded_json=? WHERE id=1`, raw, dec); err != nil {
				t.Fatal(err)
			}
			if on {
				db.setLocationFuzzer(redactTestFuzzer(t))
			}
			s := NewPacketStore(db, &PacketStoreConfig{})
			if err := s.Load(); err != nil {
				t.Fatal(err)
			}
			tx := s.byTxID[1]
			if tx == nil {
				t.Fatal("fixture transmission not loaded")
			}
			if !on {
				if tx.RawHex != raw || tx.DecodedJSON != dec || tx.locationHidden() {
					t.Fatal("feature off changed the loaded advert")
				}
				return
			}
			if tx.RawHex != "" || !tx.locationHidden() || strings.Contains(tx.DecodedJSON, `"lat"`) {
				t.Fatalf("store holds the exact advert: raw=%q dec=%s", tx.RawHex, tx.DecodedJSON)
			}
			if tx.frameHead() != raw[:8] || tx.frameBytes() != len(raw)/2 {
				t.Errorf("frame head %q / %d bytes, want %q / %d", tx.frameHead(), tx.frameBytes(), raw[:8], len(raw)/2)
			}
			if d := tx.ParsedDecoded(); d["pubKey"] == nil || d["signatureValid"] != true {
				t.Errorf("redaction lost pubKey or signatureValid: %v", d)
			}
			m := txToMap(tx)
			if m["raw_hex"] != nil || m["location_hidden"] != true {
				t.Errorf("served map: raw_hex=%v location_hidden=%v", m["raw_hex"], m["location_hidden"])
			}

			// The rewrite feed brings a JSON with the position again.
			if _, err := w.Exec(`BEGIN; UPDATE transmissions SET decoded_json=? WHERE id=1;
				INSERT INTO tx_rewrite_feed(tx_id) VALUES(1); COMMIT`, strings.Replace(dec, `"name":"Rpt"`, `"name":"Rpt2"`, 1)); err != nil {
				t.Fatal(err)
			}
			if err := s.pollTxRewrites(500); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(tx.DecodedJSON, "Rpt2") {
				t.Fatalf("rewrite not applied: %s", tx.DecodedJSON)
			}
			if strings.Contains(tx.DecodedJSON, `"lat"`) || strings.Contains(tx.DecodedJSON, `"signature"`) || tx.RawHex != "" {
				t.Errorf("rewrite put the exact advert back: %s", tx.DecodedJSON)
			}
		})
	}
}

// BenchmarkRedactLocatedAdvert: the ingest/poll cost over 30K packets of
// which about 10% are located adverts (the rest a non-advert type), and the
// two kinds alone.
func BenchmarkRedactLocatedAdvert(b *testing.B) {
	f := redactTestFuzzer(b)
	raw, dec := redactTestAdvert(b, RouteFlood, nil, 0x42, []byte{0xAB, 0xCD, 0xEF, 0x01}, 0x92, 48.137154, 11.576124, "Repeater-Name")
	const n = 30000
	type row struct {
		pt       *int
		raw, dec string
	}
	advert := row{intp(PayloadADVERT), raw, dec}
	other := row{intp(PayloadGRP_TXT), "1500AABBCC", `{"type":"GRP_TXT","channelHashHex":"AB"}`}
	for _, c := range []struct {
		name  string
		every int // one advert every this many packets; 0: none
	}{{"mixed-10pct", 10}, {"adverts", 1}, {"non-adverts", 0}} {
		rows := make([]row, n)
		for i := range rows {
			rows[i] = other
			if c.every > 0 && i%c.every == 0 {
				rows[i] = advert
			}
		}
		b.Run(c.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				for _, r := range rows {
					redactLocatedAdvert(f, r.pt, r.raw, r.dec)
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*n), "ns/packet")
		})
	}
}

// TestTopLevelMembers: the member walk splits valid objects exactly (the
// rebuilt object decodes to the same value) and refuses everything else.
func TestTopLevelMembers(t *testing.T) {
	for _, s := range []string{
		`{}`, ` { } `,
		`{"a":1}`,
		`{ "a" : "x,}]\"{" , "b":[1,{"c":"]}"},[]], "d":{"e":{"f":null}},"g":true,"h":-1.5e3 }`,
		`{"k\"ey":"v\\","lat":0,"lon":0}`,
		"{\n\t\"name\": \"\\u00e9\",\r\n\"flags\":{\"hasLocation\":true}\n}",
	} {
		members, ok := topLevelMembers(s)
		if !ok {
			t.Fatalf("refused valid %q", s)
		}
		parts := make([]string, len(members))
		for i, m := range members {
			parts[i] = m.text
		}
		var want, got interface{}
		json.Unmarshal([]byte(s), &want)
		rebuilt := "{" + strings.Join(parts, ",") + "}"
		if err := json.Unmarshal([]byte(rebuilt), &got); err != nil || fmt.Sprint(want) != fmt.Sprint(got) {
			t.Errorf("%q rebuilt as %q (%v)", s, rebuilt, err)
		}
	}
	for _, s := range []string{``, `{`, `{"a":}`, `[1]`, `"x"`, `null`, `{"a":1}x`} {
		if _, ok := topLevelMembers(s); ok {
			t.Errorf("accepted %q", s)
		}
	}
}
