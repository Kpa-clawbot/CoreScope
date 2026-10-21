package main

import (
	"sort"
	"testing"
	"time"
)

// GetBulkHealth's region filter keeps a node when one of its transmissions
// (an advert from it, or a packet whose path it relayed) was heard by an
// observer in the region. It matches the region's observations to the node
// sets by transmission ID, so this fixture gives observations IDs that differ
// from their transmissions' IDs: a filter keyed by the wrong ID fails here.
func TestGetBulkHealthRegionKeepsNodesHeardThere(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	now := time.Now().UTC()
	ts, epoch := now.Add(-time.Hour).Format(time.RFC3339), now.Add(-time.Hour).Unix()

	const (
		a = "aaaa000000000001" // heard only in EDI
		b = "bbbb000000000002" // heard only in GLA
		c = "cccc000000000003" // adverts in GLA, and relays a packet heard in EDI
	)
	exec := func(q string, args ...interface{}) {
		t.Helper()
		if _, err := db.conn.Exec(q, args...); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count) VALUES ('obsE', 'Edinburgh', 'EDI', ?, ?, 1)`, ts, ts)
	exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count) VALUES ('obsG', 'Glasgow', 'GLA', ?, ?, 1)`, ts, ts)
	for _, pk := range []string{a, b, c} {
		exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count) VALUES (?, ?, 'repeater', 56.0, -3.5, ?, ?, 1)`, pk, "R-"+pk[:4], ts, ts)
	}
	advert := func(id int, pk string) {
		exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, decoded_json, from_pubkey) VALUES (?, 'AA00', ?, ?, 1, 4, ?, ?)`,
			id, "h"+pk, ts, `{"pubKey":"`+pk+`","name":"R","type":"ADVERT"}`, pk)
	}
	advert(10, a)
	advert(20, b)
	advert(30, c)
	// A channel message heard in EDI that c relayed.
	exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, decoded_json) VALUES (40, 'CC00', 'hchan', ?, 1, 5, '{"type":"CHAN","text":"x"}')`, ts)

	// Observation IDs 1-4 never equal a transmission ID (10-40). observer_idx
	// is the observers rowid: 1 = obsE (EDI), 2 = obsG (GLA).
	exec(`INSERT INTO observations (id, transmission_id, observer_idx, snr, rssi, path_json, timestamp) VALUES (1, 10, 1, 5, -90, '[]', ?)`, epoch)
	exec(`INSERT INTO observations (id, transmission_id, observer_idx, snr, rssi, path_json, timestamp) VALUES (2, 20, 2, 5, -90, '[]', ?)`, epoch)
	exec(`INSERT INTO observations (id, transmission_id, observer_idx, snr, rssi, path_json, timestamp) VALUES (3, 30, 2, 5, -90, '[]', ?)`, epoch)
	exec(`INSERT INTO observations (id, transmission_id, observer_idx, snr, rssi, path_json, timestamp, resolved_path) VALUES (4, 40, 1, 5, -90, '["cc"]', ?, ?)`, epoch, `["`+c+`"]`)

	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	keys := func(region string) []string {
		var out []string
		for _, r := range store.GetBulkHealth(50, region, "") {
			out = append(out, r["public_key"].(string))
		}
		sort.Strings(out)
		return out
	}
	for _, tc := range []struct {
		region string
		want   []string
	}{
		{"EDI", []string{a, c}},
		{"GLA", []string{b, c}},
	} {
		got := keys(tc.region)
		if len(got) != len(tc.want) {
			t.Errorf("region %s: got %v, want %v", tc.region, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("region %s: got %v, want %v", tc.region, got, tc.want)
				break
			}
		}
	}
}
