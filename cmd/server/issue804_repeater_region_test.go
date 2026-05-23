package main

import (
	"testing"
	"time"
)

// TestIssue804_AnalyticsAttributesByRepeaterRegion verifies that analytics
// (specifically GetAnalyticsHashSizes) attribute multi-byte nodes to the
// REPEATER's home region, not the observer that happened to hear the relay.
//
// Scenario from #804:
//   - PDX-Repeater is a multi-byte (hashSize=2) repeater whose ZERO-HOP direct
//     adverts are only heard by obs-PDX (a PDX observer). That zero-hop direct
//     advert is the most reliable home-region signal — it cannot have been
//     relayed.
//   - A flood advert from PDX-Repeater (hashSize=2) propagates and is heard by
//     obs-SJC (a SJC observer) via a multi-hop relay path.
//   - When the user asks for region=SJC analytics, the PDX-Repeater MUST NOT
//     pollute SJC's multiByteNodes — it lives in PDX.
//   - The result should also expose attributionMethod="repeater" so the API
//     consumer knows which method was used.
//
// Pre-fix behavior: PDX-Repeater appears in SJC's multiByteNodes because the
// filter is observer-based. This test fails on the pre-fix code at the
// "want PDX-Repeater EXCLUDED" assertion.
func TestIssue804_AnalyticsAttributesByRepeaterRegion(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	recent := now.Add(-1 * time.Hour).Format(time.RFC3339)
	recentEpoch := now.Add(-1 * time.Hour).Unix()

	// Observers: one in PDX, one in SJC
	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obs-pdx', 'Obs PDX', 'PDX', ?, '2026-01-01T00:00:00Z', 100)`, recent)
	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obs-sjc', 'Obs SJC', 'SJC', ?, '2026-01-01T00:00:00Z', 100)`, recent)

	// PDX-Repeater node (lives in Portland)
	pdxPK := "pdx0000000000001"
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role)
		VALUES (?, 'PDX-Repeater', 'repeater')`, pdxPK)

	// SJC-Repeater node (lives in San Jose) — sanity baseline
	sjcPK := "sjc0000000000001"
	db.conn.Exec(`INSERT INTO nodes (public_key, name, role)
		VALUES (?, 'SJC-Repeater', 'repeater')`, sjcPK)

	pdxDecoded := `{"pubKey":"` + pdxPK + `","name":"PDX-Repeater","type":"ADVERT","flags":{"isRepeater":true}}`
	sjcDecoded := `{"pubKey":"` + sjcPK + `","name":"SJC-Repeater","type":"ADVERT","flags":{"isRepeater":true}}`

	// 1) PDX-Repeater zero-hop DIRECT advert heard only by obs-PDX.
	//    Establishes PDX as the repeater's home region.
	//    raw_hex header 0x12 = route_type 2 (direct), payload_type 4
	//    pathByte 0x40 (hashSize bits=01 → 2, hop_count=0)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('1240aabbccdd', 'pdx_zh_direct', ?, 2, 4, ?)`, recent, pdxDecoded)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (1, 1, 12.0, -85, '[]', ?)`, recentEpoch)

	// 2) PDX-Repeater FLOOD advert with hashSize=2 (reliable).
	//    Heard ONLY by obs-SJC via a relay path (this is the polluting case).
	//    raw_hex header 0x11 = route_type 1 (flood), payload_type 4
	//    pathByte 0x41 (hashSize bits=01 → 2, hop_count=1)
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('1141aabbccdd', 'pdx_flood', ?, 1, 4, ?)`, recent, pdxDecoded)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (2, 2, 8.0, -95, '["aa11"]', ?)`, recentEpoch)

	// 3) SJC-Repeater zero-hop DIRECT advert heard only by obs-SJC.
	//    Establishes SJC as the repeater's home region.
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('1240ccddeeff', 'sjc_zh_direct', ?, 2, 4, ?)`, recent, sjcDecoded)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (3, 2, 14.0, -82, '[]', ?)`, recentEpoch)

	// 4) SJC-Repeater FLOOD advert with hashSize=2, heard by obs-SJC.
	db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
		VALUES ('1141ccddeeff', 'sjc_flood', ?, 1, 4, ?)`, recent, sjcDecoded)
	db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
		VALUES (4, 2, 11.0, -88, '["cc22"]', ?)`, recentEpoch)

	store := NewPacketStore(db, nil)
	store.Load()

	t.Run("region=SJC excludes PDX-Repeater (heard but not home)", func(t *testing.T) {
		result := store.GetAnalyticsHashSizes("SJC")

		mb, ok := result["multiByteNodes"].([]map[string]interface{})
		if !ok {
			t.Fatal("expected multiByteNodes slice")
		}

		var foundPDX, foundSJC bool
		for _, n := range mb {
			pk, _ := n["pubkey"].(string)
			if pk == pdxPK {
				foundPDX = true
			}
			if pk == sjcPK {
				foundSJC = true
			}
		}

		if foundPDX {
			t.Errorf("PDX-Repeater leaked into SJC analytics — region attribution still observer-based (#804 not fixed)")
		}
		if !foundSJC {
			t.Errorf("SJC-Repeater missing from SJC analytics — fix over-filtered")
		}
	})

	t.Run("API exposes attributionMethod", func(t *testing.T) {
		result := store.GetAnalyticsHashSizes("SJC")
		method, ok := result["attributionMethod"].(string)
		if !ok {
			t.Fatal("expected attributionMethod string field on result")
		}
		if method != "repeater" {
			t.Errorf("attributionMethod = %q, want %q", method, "repeater")
		}
	})

	t.Run("region=PDX excludes SJC-Repeater", func(t *testing.T) {
		result := store.GetAnalyticsHashSizes("PDX")
		mb, _ := result["multiByteNodes"].([]map[string]interface{})

		var foundPDX, foundSJC bool
		for _, n := range mb {
			pk, _ := n["pubkey"].(string)
			if pk == pdxPK {
				foundPDX = true
			}
			if pk == sjcPK {
				foundSJC = true
			}
		}
		if !foundPDX {
			t.Errorf("PDX-Repeater missing from PDX analytics")
		}
		if foundSJC {
			t.Errorf("SJC-Repeater leaked into PDX analytics")
		}
	})
}
