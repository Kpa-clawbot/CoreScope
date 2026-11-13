package main

import (
	"database/sql"
	"fmt"
	"github.com/meshcore-analyzer/dbschema"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Dual-backend equivalence for every query in db.go: the same rows are seeded
// into SQLite and Postgres, and each DB method must return deeply equal
// results from both. The seed is deliberately awkward — NULL names, iatas and
// last_seen values, tied sort keys, equal-length paths, mixed-case and
// non-ASCII names, LIKE metacharacters — because those are exactly the places
// where the two engines disagree unless the SQL pins the behaviour down.
//
// Not covered here: OpenDB, GetDBSizeStats and GetDBSizeStatsTyped, which are
// SQLite-only by design (file sizes, PRAGMAs).

const (
	pgqPkA = "aa11000000000000000000000000000000000000000000000000000000000001"
	pgqPkB = "bb22000000000000000000000000000000000000000000000000000000000002"
	pgqPkC = "cc33000000000000000000000000000000000000000000000000000000000003"
	pgqPkD = "dd44000000000000000000000000000000000000000000000000000000000004"
	pgqPkE = "ee55000000000000000000000000000000000000000000000000000000000005"
	pgqPkF = "abcdef0011000000000000000000000000000000000000000000000000000006"
	pgqPkG = "abcdef0022000000000000000000000000000000000000000000000000000007"
	pgqPkH = "ff66000000000000000000000000000000000000000000000000000000000008"
)

func pgqISO(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// seedPgDBQueries fills both backends and returns the "now" the rows are
// relative to (whole seconds, UTC).
func seedPgDBQueries(t *testing.T, d *dualDB) time.Time {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	ago := func(d time.Duration) string { return pgqISO(now.Add(-d)) }
	nowE := now.Unix()

	// Nodes: two share last_seen (pkA, pkC), two share advert_count (pkA,
	// pkB), two have NULL names, one NULL last_seen, one non-ASCII name, one
	// name full of LIKE metacharacters, two share an 8-hex-char prefix.
	nodeSQL := `INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen,
		advert_count, battery_mv, temperature_c, foreign_advert, default_scope, configured_scope, configured_scope_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	for _, n := range [][]any{
		{pgqPkA, "Alpha Repeater", "repeater", 37.3, -121.9, ago(time.Hour), ago(240 * time.Hour), 5, 3700, 21.5, 0, "west", "west,east", ago(time.Hour)},
		{pgqPkB, nil, "companion", nil, nil, nil, ago(216 * time.Hour), 5, nil, nil, 0, nil, nil, nil},
		{pgqPkC, "alpha companion", "companion", 37.4, -122.0, ago(time.Hour), ago(192 * time.Hour), 2, nil, nil, 1, nil, nil, nil},
		{pgqPkD, "Ünïcode Node", "room", 51.5, -0.1, ago(48 * time.Hour), ago(480 * time.Hour), 9, nil, nil, 0, nil, nil, nil},
		{pgqPkE, `back\slash_50%`, "sensor", 0.0, 0.0, ago(3 * time.Hour), ago(48 * time.Hour), 0, nil, nil, 0, nil, nil, nil},
		{pgqPkF, "Twin One", "repeater", 37.5, -122.1, ago(30 * time.Minute), ago(100 * time.Hour), 3, nil, nil, 0, nil, nil, nil},
		{pgqPkG, "Twin Two", "repeater", 37.6, -122.2, "2020-01-01T00:00:00Z", "2019-01-01T00:00:00Z", 1, nil, nil, 0, nil, nil, nil},
		{pgqPkH, nil, nil, nil, nil, nil, nil, 0, nil, nil, 0, nil, nil, nil},
	} {
		d.exec(t, nodeSQL, n...)
	}

	// Observers: obs1/OBS3 tie on last_seen, obs2 has NULL name/iata/last_seen,
	// OBS3's iata needs UPPER(TRIM()), obs4 is inactive.
	obsSQL := `INSERT INTO observers (idx, id, name, iata, last_seen, first_seen, packet_count, model, firmware,
		client_version, radio, battery_mv, uptime_secs, noise_floor, inactive, last_packet_at,
		clock_skew_seconds, clock_skew_count_24h, clock_last_naive_at, can_relay, can_relay_seen)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	for _, o := range [][]any{
		{1, "obs1", "Observer One", "SJC", ago(5 * time.Minute), ago(800 * time.Hour), 120, "T-Beam", "1.16", "cs-1", "906.875,250,11,5", 4000, 86400, -110.5, 0, ago(5 * time.Minute), 3, 2, ago(2 * time.Hour), 1, 1},
		{2, "obs2", nil, nil, nil, nil, 0, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, 0, nil, 0, 1},
		{3, "OBS3", "Observer Three", " sfo ", ago(5 * time.Minute), ago(700 * time.Hour), 7, nil, nil, nil, nil, nil, nil, nil, 0, nil, nil, 0, nil, 1, 0},
		{4, "obs4", "Observer Four", "SJC", ago(900 * time.Hour), ago(1000 * time.Hour), 1, nil, nil, nil, nil, nil, nil, nil, 1, nil, nil, 0, nil, 1, 0},
		{5, "obs5", "Observer Five", "SFO", ago(time.Hour), ago(600 * time.Hour), 40, nil, nil, nil, nil, 3800, nil, -100.25, 0, nil, nil, 0, nil, 1, 1},
	} {
		d.exec(t, obsSQL, o...)
	}

	// Transmissions: adverts, channel messages (tx 2/3/10 tie on first_seen,
	// tx 9 is a mixed-case channel name starting "Enc"), encrypted channels,
	// and every route type with each scope_name state (NULL, '', named).
	txSQL := `INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, payload_version,
		decoded_json, from_pubkey, channel_hash, scope_name) VALUES (?,?,?,?,?,?,?,?,?,?,?)`
	for _, tx := range [][]any{
		{1, "1100aabbccddeeff", "h0000000000000001", ago(30 * time.Minute), 1, 4, 1, `{"type":"ADVERT","name":"Alpha Repeater"}`, pgqPkA, nil, nil},
		{2, "1500aabbccddeeff", "h0000000000000002", ago(20 * time.Minute), 1, 5, 1, `{"text":"Bob: hello","sender":"Bob","sender_timestamp":1700000000}`, nil, "public", nil},
		{3, "1500aabbccddee00", "h0000000000000003", ago(20 * time.Minute), 1, 5, 1, `{"text":"Ann: yo"}`, nil, "#test", nil},
		{4, "1500aabbccdd0000", "h0000000000000004", ago(15 * time.Minute), 1, 5, 1, `{"type":"GRP_TXT"}`, nil, "enc_3A", nil},
		{5, "1400aabbccdd0000", "h0000000000000005", ago(5 * time.Minute), 0, 5, 1, `{"type":"GRP_TXT"}`, nil, "enc_7F", "west"},
		{6, "1000aabbccdd0000", "h0000000000000006", ago(40 * time.Minute), 0, 4, 1, `{"type":"ADVERT"}`, pgqPkC, nil, "west"},
		{7, "0b00aabbccdd0000", "h0000000000000007", ago(2 * time.Hour), 3, 2, 1, `{"type":"TXT_MSG"}`, nil, nil, ""},
		{8, "1000aabbccdd0001", "h0000000000000008", ago(72 * time.Hour), 0, 4, 1, `{"type":"ADVERT"}`, pgqPkB, nil, nil},
		{9, "1500aabbccdd0002", "h0000000000000009", ago(25 * time.Minute), 1, 5, 1, `{"text":"Eve: secret"}`, nil, "Enclave", nil},
		{10, "1500aabbccdd0003", "h0000000000000010", ago(20 * time.Minute), 1, 5, 1, `{"text":"Cat: meow"}`, nil, "public", nil},
		{11, "1000aabbccdd0004", "h0000000000000011", ago(6 * time.Hour), 0, 4, 1, `{"type":"ADVERT"}`, pgqPkF, nil, "east"},
		{12, "1100aabbccdd0005", "h0000000000000012", ago(90 * time.Minute), 1, 4, 1, `{"type":"ADVERT"}`, pgqPkD, nil, nil},
		{13, "0b00aabbccdd0006", "h0000000000000013", ago(3 * time.Hour), 3, 2, 1, `{"type":"TXT_MSG"}`, nil, nil, "east"}, // ties west in 24h
	} {
		d.exec(t, txSQL, tx...)
	}

	// Observations: tx1 has two observations tied on path length AND
	// timestamp; tx2 and tx3 tie on their latest observation; every
	// direction state appears (NULL, 'rx', 'tx').
	oSQL := `INSERT INTO observations (id, transmission_id, observer_idx, direction, snr, rssi, path_json, timestamp, raw_hex)
		VALUES (?,?,?,?,?,?,?,?,?)`
	for _, o := range [][]any{
		{1, 1, 1, nil, 5.5, -90.0, `["aa"]`, nowE - 1790, "1100aabbccddeeff"},
		{2, 1, 3, "rx", 3.25, -95.0, `["bb"]`, nowE - 1790, "1101aabbccddeeff"},
		{3, 1, 2, "tx", nil, nil, nil, nowE - 1780, nil},
		{4, 2, 1, "rx", 7.0, -80.0, `["aa","bb"]`, nowE - 1190, nil},
		{5, 2, 5, nil, 6.0, -85.0, `["cc","dd"]`, nowE - 1100, ""},
		{6, 3, 3, "rx", 1.0, -99.0, `[]`, nowE - 1100, nil},
		{7, 4, 2, "tx", nil, nil, nil, nowE - 900, nil},
		{8, 5, 1, "rx", 2.0, -97.0, `["aa"]`, nowE - 300, nil},
		{9, 5, 5, "rx", 2.5, -96.0, `["bb"]`, nowE - 300, nil},
		{10, 6, 5, nil, 4.0, -91.0, `["aa","bb","cc"]`, nowE - 2390, nil},
		{11, 7, 3, "rx", -1.5, -110.0, nil, nowE - 7190, nil},
		{12, 8, 1, "rx", 0.5, -100.0, `[]`, nowE - 259190, nil},
		{13, 9, 3, "rx", 2.0, -98.0, `["ab"]`, nowE - 1490, nil},
		{14, 11, 4, "rx", 3.0, -94.0, `[]`, nowE - 21590, nil},
		{15, 12, 1, "rx", 3.5, -93.0, `["aa"]`, nowE - 5390, nil},
	} {
		d.exec(t, oSQL, o...)
	}

	// Dropped packets: two tie on dropped_at, one has NULL dropped_at.
	dSQL := `INSERT INTO dropped_packets (id, hash, raw_hex, reason, observer_id, observer_name, node_pubkey, node_name, dropped_at)
		VALUES (?,?,?,?,?,?,?,?,?)`
	for _, r := range [][]any{
		{1, "h1", "00ff", "bad signature", "obs1", "Observer One", pgqPkA, "Alpha Repeater", "2026-11-01 10:00:00"},
		{2, "h2", nil, "bad signature", "obs1", "Observer One", pgqPkB, nil, "2026-11-01 10:00:00"},
		{3, "h3", "aa", "too short", "obs2", nil, pgqPkA, "Alpha Repeater", "2026-11-02 09:00:00"},
		{4, nil, nil, "unknown", nil, nil, nil, nil, nil},
		{5, "h5", nil, "bad signature", "obs1", nil, nil, nil, "2026-11-01 10:00:00.25"},
		{6, "h6", nil, "bad signature", "obs1", nil, nil, nil, "2026-11-01T11:00:00+02:00"},
		{7, "h7", nil, "bad signature", "obs1", nil, nil, nil, "garbage"},
	} {
		d.exec(t, dSQL, r...)
	}

	// observer_metrics: obs1 every 5 minutes for 3 hours with a counter reset
	// part-way; obs2 with NULL values; OBS3 ties obs1 on max noise floor; a
	// metrics-only observer with no observers row.
	mSQL := `INSERT INTO observer_metrics (observer_id, timestamp, noise_floor, tx_air_secs, rx_air_secs,
		recv_errors, battery_mv, packets_sent, packets_recv) VALUES (?,?,?,?,?,?,?,?,?)`
	base := now.Truncate(5 * time.Minute).Add(-3 * time.Hour)
	for i := 0; i <= 36; i++ {
		tx := i * 10
		if i >= 20 {
			tx = (i - 20) * 3 // reboot
		}
		d.exec(t, mSQL, "obs1", pgqISO(base.Add(time.Duration(i)*5*time.Minute)),
			-110.0-float64(i%4)*0.5, tx, i*20, i, 3900-i, i*2, i*30)
	}
	for _, ago := range []time.Duration{2 * time.Hour, time.Hour, 10 * time.Minute} {
		d.exec(t, mSQL, "obs2", pgqISO(now.Add(-ago)), nil, nil, nil, nil, nil, nil, nil)
	}
	d.exec(t, mSQL, "OBS3", pgqISO(now.Add(-30*time.Minute)), -110.0, nil, nil, nil, nil, nil, nil)
	d.exec(t, mSQL, "OBS3", pgqISO(now.Add(-25*time.Minute)), -115.5, nil, nil, nil, 3500, nil, nil)
	d.exec(t, mSQL, "ghost", pgqISO(now.Add(-20*time.Minute)), -120.0, 5, 6, 0, nil, 1, 2)
	d.exec(t, mSQL, "ghost", pgqISO(now.Add(-15*time.Minute)), -119.0, 8, 9, 1, nil, 2, 4)

	// The content-hash migration row exists on a migrated Postgres database but
	// not in the bare SQLite end-state schema; give both the same state.
	d.exec(t, `DELETE FROM _async_migrations WHERE name = ?`, contentHashMigrationName)
	d.exec(t, `INSERT INTO _async_migrations (name, status) VALUES (?, 'done')`, contentHashMigrationName)

	return now
}

// pgqNonEmpty fails when a result that the seed should populate is empty —
// two empty results are trivially equal and prove nothing.
func pgqNonEmpty(t *testing.T, name string, v any) {
	t.Helper()
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			t.Fatalf("%s: nil result", name)
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.Slice, reflect.Map:
		if rv.Len() == 0 {
			t.Fatalf("%s: empty result", name)
		}
	}
}

// same runs f on both backends, requires identical results and, unless
// mayBeEmpty, a non-empty one.
func same[T any](t *testing.T, d *dualDB, name string, mayBeEmpty bool, f func(*DB) (T, error)) T {
	t.Helper()
	v := requireSame(t, d, name, f)
	if !mayBeEmpty {
		pgqNonEmpty(t, name, v)
	}
	return v
}

func TestPgDBPacketQueries(t *testing.T) {
	d := newDualTestDB(t)
	now := seedPgDBQueries(t, d)

	queries := []struct {
		name  string
		q     PacketQuery
		empty bool
	}{
		{name: "default", q: PacketQuery{}},
		{name: "type", q: PacketQuery{Type: intPtr(5)}},
		{name: "route", q: PacketQuery{Route: intPtr(0)}},
		{name: "observer", q: PacketQuery{Observer: "obs1"}},
		{name: "observers+excludeTx", q: PacketQuery{Observer: "obs1, obs2,OBS3", ExcludeTx: true}},
		{name: "excludeTx", q: PacketQuery{ExcludeTx: true}},
		{name: "hash", q: PacketQuery{Hash: "H0000000000000002"}},
		{name: "since rfc3339", q: PacketQuery{Since: pgqISO(now.Add(-time.Hour))}},
		{name: "until rfc3339", q: PacketQuery{Until: pgqISO(now.Add(-20 * time.Minute))}},
		{name: "since plain", q: PacketQuery{Since: now.Add(-time.Hour).Format("2006-01-02")}},
		{name: "until plain", q: PacketQuery{Until: "2099-01-01"}},
		{name: "node pubkey", q: PacketQuery{Node: pgqPkA}},
		{name: "node name", q: PacketQuery{Node: "Alpha Repeater"}},
		{name: "channel", q: PacketQuery{Channel: "public"}},
		{name: "region", q: PacketQuery{Region: "SJC"}},
		{name: "asc page", q: PacketQuery{Order: "ASC", Limit: 3, Offset: 2}},
		{name: "page", q: PacketQuery{Limit: 4, Offset: 4}},
		{name: "nothing", q: PacketQuery{Hash: "nope"}, empty: true},
	}
	for _, tc := range queries {
		t.Run("QueryPackets/"+tc.name, func(t *testing.T) {
			r := same(t, d, "QueryPackets", false, func(db *DB) (*PacketResult, error) { return db.QueryPackets(tc.q) })
			if !tc.empty {
				pgqNonEmpty(t, "QueryPackets.Packets", r.Packets)
			}
		})
		t.Run("QueryGroupedPackets/"+tc.name, func(t *testing.T) {
			r := same(t, d, "QueryGroupedPackets", false, func(db *DB) (*PacketResult, error) { return db.QueryGroupedPackets(tc.q) })
			if !tc.empty {
				pgqNonEmpty(t, "QueryGroupedPackets.Packets", r.Packets)
			}
		})
	}

	t.Run("grouped distinct_iatas", func(t *testing.T) {
		r := same(t, d, "grouped", false, func(db *DB) (*PacketResult, error) {
			return db.QueryGroupedPackets(PacketQuery{Hash: "h0000000000000001"})
		})
		if got := r.Packets[0]["distinct_iatas"]; !reflect.DeepEqual(got, []string{"SJC", "sfo"}) {
			t.Fatalf("distinct_iatas = %#v", got)
		}
	})
	t.Run("GetTransmissionByID", func(t *testing.T) {
		for _, id := range []int{1, 7, 10, 999} {
			same(t, d, fmt.Sprint("GetTransmissionByID ", id), id == 999, func(db *DB) (map[string]any, error) { return db.GetTransmissionByID(id) })
		}
	})
	t.Run("GetPacketByHash", func(t *testing.T) {
		same(t, d, "GetPacketByHash", false, func(db *DB) (map[string]any, error) { return db.GetPacketByHash("H0000000000000005") })
	})
	t.Run("GetObservationsForHash", func(t *testing.T) {
		same(t, d, "GetObservationsForHash", false, func(db *DB) ([]map[string]any, error) {
			return db.GetObservationsForHash("h0000000000000001"), nil
		})
	})
	t.Run("ObservationRawHexForHash", func(t *testing.T) {
		same(t, d, "ObservationRawHexForHash", false, func(db *DB) (map[int]string, error) {
			return db.ObservationRawHexForHash("h0000000000000001"), nil
		})
	})
	t.Run("GetTraces", func(t *testing.T) {
		same(t, d, "GetTraces", false, func(db *DB) ([]map[string]any, error) { return db.GetTraces("h0000000000000001") })
		same(t, d, "GetTraces none", true, func(db *DB) ([]map[string]any, error) { return db.GetTraces("none") })
	})
	t.Run("GetRecentTransmissionsForNode", func(t *testing.T) {
		same(t, d, "GetRecentTransmissionsForNode", false, func(db *DB) ([]map[string]any, error) {
			return db.GetRecentTransmissionsForNode(pgqPkA, 10)
		})
	})
	t.Run("GetNewTransmissionsSince", func(t *testing.T) {
		same(t, d, "GetNewTransmissionsSince", false, func(db *DB) ([]map[string]any, error) { return db.GetNewTransmissionsSince(3, 5) })
	})
	t.Run("max ids", func(t *testing.T) {
		got := requireSame(t, d, "max ids", func(db *DB) ([2]int, error) {
			return [2]int{db.GetMaxTransmissionID(), db.GetMaxObservationID()}, nil
		})
		if got != [2]int{13, 15} {
			t.Fatalf("max ids = %v", got)
		}
	})
	t.Run("QueryMultiNodePackets", func(t *testing.T) {
		same(t, d, "QueryMultiNodePackets", false, func(db *DB) (*PacketResult, error) {
			return db.QueryMultiNodePackets([]string{pgqPkA, "alpha companion", pgqPkF}, 10, 0, "ASC", pgqISO(now.Add(-48*time.Hour)), pgqISO(now))
		})
		same(t, d, "QueryMultiNodePackets all", false, func(db *DB) (*PacketResult, error) {
			return db.QueryMultiNodePackets([]string{pgqPkA, pgqPkB, pgqPkC}, 0, 0, "", "", "")
		})
	})
	t.Run("GetChannelMessages", func(t *testing.T) {
		type page struct {
			Msgs  []map[string]any
			Total int
		}
		for _, region := range []string{"", "SJC", "sfo"} {
			got := same(t, d, "GetChannelMessages "+region, false, func(db *DB) (page, error) {
				m, n, err := db.GetChannelMessages("public", 10, 0, region)
				return page{m, n}, err
			})
			pgqNonEmpty(t, "GetChannelMessages "+region, got.Msgs)
		}
	})
}

func TestPgDBNodeQueries(t *testing.T) {
	d := newDualTestDB(t)
	seedPgDBQueries(t, d)

	type nodesPage struct {
		Nodes  []map[string]any
		Total  int
		Counts map[string]int
	}
	cases := []struct {
		name  string
		q     NodeQuery
		empty bool
	}{
		{name: "default", q: NodeQuery{}},
		{name: "sort name", q: NodeQuery{SortBy: "name"}},
		{name: "sort lastSeen", q: NodeQuery{SortBy: "lastSeen"}},
		{name: "sort packetCount", q: NodeQuery{SortBy: "packetCount"}},
		{name: "page", q: NodeQuery{SortBy: "packetCount", Limit: 3, Offset: 2}},
		{name: "role", q: NodeQuery{Role: "repeater"}},
		{name: "search lower", q: NodeQuery{Search: "alpha"}},
		{name: "search upper", q: NodeQuery{Search: "ALPHA"}},
		{name: "search mixed", q: NodeQuery{Search: "aLpHa C"}},
		{name: "search non-ascii exact", q: NodeQuery{Search: "Ünï"}},
		{name: "search non-ascii folded", q: NodeQuery{Search: "ünï"}, empty: true}, // SQLite folds ASCII only
		{name: "search backslash", q: NodeQuery{Search: `k\s`}},
		{name: "search underscore wildcard", q: NodeQuery{Search: "h_5"}},
		{name: "search percent", q: NodeQuery{Search: "%"}},
		{name: "before", q: NodeQuery{Before: pgqISO(time.Now().Add(-200 * time.Hour))}},
		{name: "lastHeard", q: NodeQuery{LastHeard: "24h", SortBy: "name"}},
		{name: "region", q: NodeQuery{Region: "sjc,SFO"}},
		{name: "region pubkeys", q: NodeQuery{RegionPubkeys: []string{pgqPkC, pgqPkA, "nope"}}},
		{name: "region pubkeys empty", q: NodeQuery{RegionPubkeys: []string{}}, empty: true},
	}
	for _, tc := range cases {
		t.Run("GetNodes/"+tc.name, func(t *testing.T) {
			got := same(t, d, "GetNodes", false, func(db *DB) (nodesPage, error) {
				n, total, counts, err := db.GetNodes(tc.q)
				return nodesPage{n, total, counts}, err
			})
			if !tc.empty {
				pgqNonEmpty(t, "GetNodes.Nodes", got.Nodes)
			}
		})
	}
	t.Run("GetNodes search is ASCII case-insensitive", func(t *testing.T) {
		got := same(t, d, "GetNodes", false, func(db *DB) (nodesPage, error) {
			n, total, counts, err := db.GetNodes(NodeQuery{Search: "ALPHA", SortBy: "name"})
			return nodesPage{n, total, counts}, err
		})
		if got.Total != 2 {
			t.Fatalf("search ALPHA matched %d nodes, want 2", got.Total)
		}
	})

	for _, q := range []string{"alpha", "AA11", "abcdef", "Twin", `\`, "nothing-here"} {
		t.Run("SearchNodes/"+q, func(t *testing.T) {
			same(t, d, "SearchNodes", q == "nothing-here", func(db *DB) ([]map[string]any, error) { return db.SearchNodes(q, 10) })
		})
	}
	type prefixResult struct {
		Node      map[string]any
		Ambiguous bool
	}
	for _, p := range []string{"abcdef00", "ABCDEF0011", "aa110000", "AA110000", "00000000"} {
		t.Run("GetNodeByPrefix/"+p, func(t *testing.T) {
			got := requireSame(t, d, "GetNodeByPrefix", func(db *DB) (prefixResult, error) {
				n, amb, err := db.GetNodeByPrefix(p)
				return prefixResult{n, amb}, err
			})
			if p == "AA110000" && (got.Node == nil || got.Node["public_key"] != pgqPkA) {
				t.Fatalf("upper-case prefix did not resolve: %#v", got)
			}
		})
	}
	t.Run("GetNodeByPubkey", func(t *testing.T) {
		same(t, d, "GetNodeByPubkey", false, func(db *DB) (map[string]any, error) { return db.GetNodeByPubkey(pgqPkA) })
	})
	t.Run("GetNodeLocations", func(t *testing.T) {
		same(t, d, "GetNodeLocations", false, func(db *DB) (map[string]map[string]any, error) { return db.GetNodeLocations(), nil })
		same(t, d, "GetNodeLocationsByKeys", false, func(db *DB) (map[string]map[string]any, error) {
			return db.GetNodeLocationsByKeys([]string{strings.ToUpper(pgqPkA), pgqPkB}), nil
		})
	})
	t.Run("GetNodePubkeysInArea", func(t *testing.T) {
		lo, hi, lonLo, lonHi := 30.0, 60.0, -125.0, 1.0
		same(t, d, "GetNodePubkeysInArea", false, func(db *DB) ([]string, error) {
			return db.GetNodePubkeysInArea(AreaEntry{LatMin: &lo, LatMax: &hi, LonMin: &lonLo, LonMax: &lonHi})
		})
	})
	t.Run("GetNodesForGeoPrune", func(t *testing.T) {
		same(t, d, "GetNodesForGeoPrune", false, func(db *DB) ([]NodeForGeoPrune, error) { return db.GetNodesForGeoPrune() })
	})
	t.Run("counts", func(t *testing.T) {
		type counts struct {
			Active   int
			Stats    *Stats
			Roles    map[string]int
			AllRoles map[string]int
			Rows     *SqliteRowCounts
			Network  map[string]any
			HashDone bool
		}
		got := requireSame(t, d, "counts", func(db *DB) (counts, error) {
			active, err := db.CountActiveNodes()
			if err != nil {
				return counts{}, err
			}
			stats, err := db.GetStats()
			if err != nil {
				return counts{}, err
			}
			network, err := db.GetNetworkStatus((&Config{}).GetHealthThresholds())
			return counts{active, stats, db.GetRoleCounts(), db.GetAllRoleCounts(), db.cachedRowCounts(), network, db.hashMigrationComplete()}, err
		})
		if !got.HashDone || got.Active == 0 || got.Stats.TotalTransmissions != 13 || got.Stats.PacketsLastHour == 0 || got.Rows.Observations != 15 {
			t.Fatalf("unexpected counts: %+v %+v %+v", got, got.Stats, got.Rows)
		}
	})
}

func TestPgDBObserverAndChannelQueries(t *testing.T) {
	d := newDualTestDB(t)
	now := seedPgDBQueries(t, d)

	t.Run("GetObservers", func(t *testing.T) {
		same(t, d, "GetObservers", false, func(db *DB) ([]Observer, error) { return db.GetObservers() })
	})
	t.Run("relay pubkeys", func(t *testing.T) {
		same(t, d, "GetNonRelayObserverPubkeys", false, func(db *DB) ([]string, error) { return db.GetNonRelayObserverPubkeys() })
		same(t, d, "GetCanRelaySeenObserverPubkeys", false, func(db *DB) ([]string, error) { return db.GetCanRelaySeenObserverPubkeys() })
	})
	t.Run("GetObserverByID", func(t *testing.T) {
		same(t, d, "GetObserverByID", false, func(db *DB) (*Observer, error) { return db.GetObserverByID("obs1") })
		same(t, d, "GetObserverByID nulls", false, func(db *DB) (*Observer, error) { return db.GetObserverByID("obs2") })
	})
	t.Run("GetObserverIdsForRegion", func(t *testing.T) {
		same(t, d, "GetObserverIdsForRegion", false, func(db *DB) ([]string, error) { return db.GetObserverIdsForRegion("sjc, SFO") })
	})
	t.Run("GetDistinctIATAs", func(t *testing.T) {
		same(t, d, "GetDistinctIATAs", false, func(db *DB) ([]string, error) { return db.GetDistinctIATAs() })
	})
	t.Run("GetObserverPacketCounts", func(t *testing.T) {
		same(t, d, "GetObserverPacketCounts", false, func(db *DB) (map[string]int, error) {
			return db.GetObserverPacketCounts(now.Add(-time.Hour).Unix()), nil
		})
	})
	for _, region := range []string{"", "SJC", "sfo", "All"} {
		t.Run("GetChannels/"+region, func(t *testing.T) {
			same(t, d, "GetChannels", false, func(db *DB) ([]map[string]any, error) { return db.GetChannels(region) })
		})
		t.Run("GetEncryptedChannels/"+region, func(t *testing.T) {
			same(t, d, "GetEncryptedChannels", region == "sfo", func(db *DB) ([]map[string]any, error) { return db.GetEncryptedChannels(region) })
		})
	}
	t.Run("GetChannels excludes enc_ case-insensitively", func(t *testing.T) {
		got := same(t, d, "GetChannels", false, func(db *DB) ([]map[string]any, error) { return db.GetChannels() })
		for _, c := range got {
			if c["hash"] == "Enclave" {
				t.Fatalf("Enclave listed: SQLite's LIKE 'enc_%%' is case-insensitive and excludes it")
			}
		}
	})
	t.Run("dropped packets", func(t *testing.T) {
		for _, f := range [][2]string{{"", ""}, {"obs1", ""}, {"", pgqPkA}, {"obs1", pgqPkB}} {
			same(t, d, "GetDroppedPackets "+f[0]+"/"+f[1], false, func(db *DB) ([]map[string]any, error) {
				return db.GetDroppedPackets(50, f[0], f[1])
			})
		}
		n := requireSame(t, d, "GetSignatureDropCount", func(db *DB) (int64, error) { return db.GetSignatureDropCount(), nil })
		if n != 7 {
			t.Fatalf("GetSignatureDropCount = %d", n)
		}
		got := same(t, d, "GetDroppedPackets", false, func(db *DB) ([]map[string]any, error) { return db.GetDroppedPackets(50, "", "") })
		var at []any
		for _, r := range got {
			at = append(at, r["dropped_at"])
		}
		// DATETIME text as the SQLite driver returns it (zero time when unparseable).
		want := []any{"0001-01-01T00:00:00Z", "2026-11-02T09:00:00Z", "2026-11-01T11:00:00+02:00",
			"2026-11-01T10:00:00.25Z", "2026-11-01T10:00:00Z", "2026-11-01T10:00:00Z", nil}
		if !reflect.DeepEqual(at, want) {
			t.Fatalf("dropped_at = %#v", at)
		}
	})
}

func TestPgDBMetricsQueries(t *testing.T) {
	d := newDualTestDB(t)
	now := seedPgDBQueries(t, d)

	type metrics struct {
		Samples []MetricsSample
		Reboots []string
	}
	for _, res := range []string{"5m", "1h", "1d"} {
		for _, w := range [][2]string{{"", ""}, {pgqISO(now.Add(-2 * time.Hour)), pgqISO(now.Add(-30 * time.Minute))}} {
			t.Run("GetObserverMetrics/"+res+"/"+w[0], func(t *testing.T) {
				same(t, d, "GetObserverMetrics", false, func(db *DB) (metrics, error) {
					s, r, err := db.GetObserverMetrics("obs1", w[0], w[1], res, 300)
					return metrics{s, r}, err
				})
				same(t, d, "GetObserverMetrics nulls", false, func(db *DB) (metrics, error) {
					s, r, err := db.GetObserverMetrics("obs2", w[0], w[1], res, 300)
					return metrics{s, r}, err
				})
			})
		}
	}
	t.Run("GetMetricsSummary", func(t *testing.T) {
		got := same(t, d, "GetMetricsSummary", false, func(db *DB) ([]MetricsSummaryRow, error) {
			return db.GetMetricsSummary(pgqISO(now.Add(-24*time.Hour)), now)
		})
		if len(got) != 4 {
			t.Fatalf("GetMetricsSummary rows = %d, want 4", len(got))
		}
	})
	for _, window := range []string{"1h", "24h", "7d", "bogus"} {
		t.Run("GetScopeStats/"+window, func(t *testing.T) {
			got := same(t, d, "GetScopeStats", false, func(db *DB) (*ScopeStatsResponse, error) { return db.GetScopeStats(window) })
			pgqNonEmpty(t, "TimeSeries", got.TimeSeries)
			pgqNonEmpty(t, "ByRegion", got.ByRegion)
		})
	}
}

// The strftime/to_char pairs reach the API as strings, so they must be
// byte-identical on both backends — and correct, so each is also checked
// against the expected text.
func TestPgDBTimeFormats(t *testing.T) {
	d := newDualTestDB(t)

	epochs := []struct {
		e    int64
		want string
	}{
		{0, "1970-01-01T00:00:00.000Z"},
		{1709210096, "2024-02-29T12:34:56.000Z"},   // leap day
		{1767225599, "2025-12-31T23:59:59.000Z"},   // last second of a year
		{1767225600, "2026-01-01T00:00:00.000Z"},   // first second of the next
		{253402300799, "9999-12-31T23:59:59.000Z"}, // largest four-digit year
	}
	d.exec(t, `INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (1, '00', 'hfmt', '2026-01-01T00:00:00Z')`)
	d.exec(t, `INSERT INTO observers (idx, id, name, iata) VALUES (1, 'fobs', 'Fmt', 'SJC')`)
	for i, e := range epochs {
		d.exec(t, `INSERT INTO observations (id, transmission_id, observer_idx, path_json, timestamp) VALUES (?, 1, 1, ?, ?)`,
			i+1, fmt.Sprintf(`["%02x"]`, i), e.e)
	}
	t.Run("epoch ISO", func(t *testing.T) {
		got := requireSame(t, d, "epochISO", func(db *DB) ([]string, error) {
			return pgqStrings(db, `SELECT `+db.sqlEpochISO("timestamp")+` FROM observations ORDER BY id`)
		})
		for i, e := range epochs {
			if got[i] != e.want {
				t.Errorf("epoch %d: got %q, want %q", e.e, got[i], e.want)
			}
		}
	})
	t.Run("GetTraces", func(t *testing.T) {
		same(t, d, "GetTraces", false, func(db *DB) ([]map[string]any, error) { return db.GetTraces("hfmt") })
	})
	t.Run("grouped latest", func(t *testing.T) {
		r := same(t, d, "QueryGroupedPackets", false, func(db *DB) (*PacketResult, error) { return db.QueryGroupedPackets(PacketQuery{}) })
		if got := r.Packets[0]["latest"]; got != "9999-12-31T23:59:59.000Z" {
			t.Fatalf("latest = %v", got)
		}
	})
	t.Run("observations", func(t *testing.T) {
		same(t, d, "GetObservationsForHash", false, func(db *DB) ([]map[string]any, error) { return db.GetObservationsForHash("hfmt"), nil })
	})

	// Text timestamps (observer_metrics.timestamp, transmissions.first_seen)
	// in every shape SQLite's date functions accept that CoreScope could hold.
	texts := []string{
		"1970-01-01T00:00:00Z",
		"2024-02-29T23:59:59Z",
		"2025-12-31T23:59:59Z",
		"2026-01-01T00:00:00Z",
		"2026-03-01T10:07:30.123Z",
		"2026-03-01T10:04:59Z",
		"2026-03-01T10:05:00Z",
		"2026-03-01T17:59:59Z",
		"2026-03-01T18:00:00Z",
		"2026-03-01T01:30:00+02:00", // crosses midnight back into Feb 28
		"2026-03-01 10:07:00",
		"2026-03-01T10:07",
		"2026-03-01",
		"not-a-time",
		"",
	}
	for i, s := range texts {
		d.exec(t, `INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (?, '00', ?, ?)`, 100+i, fmt.Sprint("ht", i), s)
	}
	want := map[timeBucket][]string{
		bucketHour: {"1970-01-01T00:00:00Z", "2024-02-29T23:00:00Z", "2025-12-31T23:00:00Z", "2026-01-01T00:00:00Z",
			"2026-03-01T10:00:00Z", "2026-03-01T10:00:00Z", "2026-03-01T10:00:00Z", "2026-03-01T17:00:00Z", "2026-03-01T18:00:00Z",
			"2026-02-28T23:00:00Z", "2026-03-01T10:00:00Z", "2026-03-01T10:00:00Z", "2026-03-01T00:00:00Z", "", ""},
		bucketDay: {"1970-01-01T00:00:00Z", "2024-02-29T00:00:00Z", "2025-12-31T00:00:00Z", "2026-01-01T00:00:00Z",
			"2026-03-01T00:00:00Z", "2026-03-01T00:00:00Z", "2026-03-01T00:00:00Z", "2026-03-01T00:00:00Z", "2026-03-01T00:00:00Z",
			"2026-02-28T00:00:00Z", "2026-03-01T00:00:00Z", "2026-03-01T00:00:00Z", "2026-03-01T00:00:00Z", "", ""},
		bucket5Min: {"1970-01-01T00:00:00Z", "2024-02-29T23:55:00Z", "2025-12-31T23:55:00Z", "2026-01-01T00:00:00Z",
			"2026-03-01T10:05:00Z", "2026-03-01T10:00:00Z", "2026-03-01T10:05:00Z", "2026-03-01T17:55:00Z", "2026-03-01T18:00:00Z",
			"2026-02-28T23:30:00Z", "2026-03-01T10:05:00Z", "2026-03-01T10:05:00Z", "2026-03-01T00:00:00Z", "", ""},
		bucket6Hour: {"1970-01-01T00:00:00Z", "2024-02-29T18:00:00Z", "2025-12-31T18:00:00Z", "2026-01-01T00:00:00Z",
			"2026-03-01T06:00:00Z", "2026-03-01T06:00:00Z", "2026-03-01T06:00:00Z", "2026-03-01T12:00:00Z", "2026-03-01T18:00:00Z",
			"2026-02-28T18:00:00Z", "2026-03-01T06:00:00Z", "2026-03-01T06:00:00Z", "2026-03-01T00:00:00Z", "", ""},
	}
	for _, b := range []timeBucket{bucket5Min, bucketHour, bucket6Hour, bucketDay} {
		t.Run(fmt.Sprint("text bucket ", b), func(t *testing.T) {
			got := requireSame(t, d, "textBucket", func(db *DB) ([]string, error) {
				return pgqStrings(db, `SELECT `+db.sqlTimeBucket("first_seen", b)+` FROM transmissions WHERE id >= 100 ORDER BY id`)
			})
			if !reflect.DeepEqual(got, want[b]) {
				t.Errorf("bucket %v:\n got  %q\n want %q", b, got, want[b])
			}
		})
	}

	// The same shapes through GetObserverMetrics' hourly and daily buckets
	// (unparseable values excluded: they make the method fail on both).
	for i, s := range texts[:13] {
		d.exec(t, `INSERT INTO observer_metrics (observer_id, timestamp, noise_floor, tx_air_secs) VALUES ('fobs', ?, ?, ?)`, s, -100.0-float64(i), i)
	}
	for _, res := range []string{"5m", "1h", "1d"} {
		t.Run("GetObserverMetrics "+res, func(t *testing.T) {
			same(t, d, "GetObserverMetrics", false, func(db *DB) ([]MetricsSample, error) {
				s, _, err := db.GetObserverMetrics("fobs", "", "", res, 300)
				return s, err
			})
		})
	}
}

// pgqStrings runs a one-column query and returns its rows, NULL as "".
func pgqStrings(db *DB, query string) ([]string, error) {
	rows, err := db.conn.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s sql.NullString
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s.String)
	}
	return out, rows.Err()
}

// --- benchmark (SQLite: the production backend whose speed must not regress) ---

// pgqBenchDB builds a read-only SQLite DB with n transmissions, each heard by
// four of ten observers with paths of varying length.
func pgqBenchDB(b *testing.B, n int) *DB {
	b.Helper()
	path := b.TempDir() + "/bench.db"
	rw, err := sql.Open("sqlite3", path+"?_journal_mode=WAL")
	if err != nil {
		b.Fatal(err)
	}
	defer rw.Close()
	rw.SetMaxOpenConns(1)
	if _, err := rw.Exec(dbschema.SQLiteEndState); err != nil {
		b.Fatal(err)
	}
	tx, err := rw.Begin()
	if err != nil {
		b.Fatal(err)
	}
	iatas := []string{"SJC", "SFO", "OAK", "", "LAX"}
	for i := 1; i <= 10; i++ {
		if _, err := tx.Exec(`INSERT INTO observers (idx, id, name, iata) VALUES (?, ?, ?, ?)`,
			i, fmt.Sprintf("obs%02d", i), fmt.Sprintf("Observer %d", i), iatas[i%len(iatas)]); err != nil {
			b.Fatal(err)
		}
	}
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	obsID := 0
	for i := 1; i <= n; i++ {
		first := base.Add(time.Duration(i) * 7 * time.Second)
		if _, err := tx.Exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, decoded_json, channel_hash)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, i, "1500aabbccddeeff0011", fmt.Sprintf("h%015d", i), first.Format(time.RFC3339),
			i%4, []int{4, 5, 2}[i%3], `{"text":"x: y"}`, "public"); err != nil {
			b.Fatal(err)
		}
		for k := 0; k < 4; k++ {
			obsID++
			path := `[` + strings.TrimSuffix(strings.Repeat(`"ab",`, (i+k)%5), ",") + `]`
			if _, err := tx.Exec(`INSERT INTO observations (id, transmission_id, observer_idx, direction, snr, rssi, path_json, timestamp)
				VALUES (?, ?, ?, 'rx', 5.5, -90, ?, ?)`, obsID, i, (i+k*3)%10+1, path, first.Unix()+int64(k*2)); err != nil {
				b.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	if _, err := rw.Exec("ANALYZE"); err != nil {
		b.Fatal(err)
	}
	db, err := OpenDB(path)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	return db
}

func BenchmarkPgDBQueryGroupedPacketsSQLite(b *testing.B) {
	db := pgqBenchDB(b, 5000)
	for _, bc := range []struct {
		name string
		q    PacketQuery
	}{
		{"page1", PacketQuery{Limit: 50}},
		{"observer", PacketQuery{Limit: 50, Observer: "obs03"}},
		{"type", PacketQuery{Limit: 50, Type: intPtr(5)}},
	} {
		b.Run(bc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				if _, err := db.QueryGroupedPackets(bc.q); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
