package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// A small real startup budget leaves the older matching packets only in SQL.
// Both backend matrices run this fixture through their read-only connection.
func TestAreaFilterMemoryLimitedSQLPagination(t *testing.T) {
	db, writer := openPostgresValueFixture(t)
	for _, query := range []string{
		`INSERT INTO nodes(public_key,lat,lon) VALUES
			('area-inside',11,11),('area-outside',20,20),
			('area-no-gps',NULL,NULL),('area-zero',0,0),('7',11,11)`,
		`INSERT INTO observers(rowid,id,name,iata) VALUES
			(1,'area-observer-a','Synthetic observer A','AAA'),
			(2,'area-observer-b','Synthetic observer B','BBB')`,
	} {
		if _, err := writer.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	decoded := []string{
		`{"pubKey":"area-inside"}`,
		`{"pubKey":"area-outside"}`,
		`{"public_key":"area-inside"}`,
		`{"pubKey":"area-no-gps"}`,
		`{"public_key":"","pubKey":"area-inside"}`,
		`{"public_key":"area-outside","pubKey":"area-inside"}`,
		`{"channel":"public"}`,
		`{"public_key":7,"pubKey":"area-inside"}`,
		`not json`,
		`{"pubKey":"area-inside"}`,
		`{"public_key":7,"pubKey":7}`,
		`{"pubKey":"area-zero"}`,
		`{"pubKey":"area-inside"}`,
		`{"pubKey":"area-outside"}`,
		`{"pubKey":"AREA-INSIDE"}`,
	}
	stamp := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	// Payload bytes force an actual memory stop without thousands of rows.
	raw := strings.Repeat("aa", 128*1024)
	copyTestRows(t, writer, "transmissions", []string{"id", "raw_hex", "hash", "first_seen", "last_seen", "route_type", "payload_type", "decoded_json"}, len(decoded), func(i int) []any {
		seen := stamp.Add(time.Duration(i) * time.Minute)
		return []any{i + 1, raw, fmt.Sprintf("area-%02d", i+1), seen.Format(time.RFC3339), seen.Unix(), 1, PayloadADVERT, decoded[i]}
	})
	copyTestRows(t, writer, "observations", []string{"id", "transmission_id", "observer_idx", "direction", "path_json", "timestamp"}, len(decoded), func(i int) []any {
		observer := 1
		if i == 9 {
			observer = 2
		}
		return []any{i + 1, i + 1, observer, "rx", "[]", stamp.Add(time.Duration(i) * time.Minute).Unix()}
	})
	lower, upper := 10.0, 12.0
	cfg := &Config{
		Areas: map[string]AreaEntry{
			"POLY":  {Polygon: [][2]float64{{10, 10}, {10, 12}, {12, 12}, {12, 10}}},
			"BOX":   {LatMin: &lower, LatMax: &upper, LonMin: &lower, LonMax: &upper},
			"EMPTY": {Polygon: [][2]float64{{30, 30}, {30, 32}, {32, 32}, {32, 30}}},
		},
		// The instance node-display filter is independent of a packet's area.
		GeoFilter: &GeoFilterConfig{Polygon: [][2]float64{{30, 30}, {30, 32}, {32, 32}, {32, 30}}},
	}
	limited := NewPacketStore(db, &PacketStoreConfig{MaxMemoryMB: 1, RetentionHours: 24})
	limited.config = cfg
	if err := limited.RunStartupLoad(100); err != nil {
		t.Fatal(err)
	}
	assertStartupBudget(t, limited, 1)
	if !limited.startupMemoryLimited || len(limited.packets) == 0 || limited.byHash["area-01"] != nil {
		t.Fatalf("fixture must leave old matching history only in SQL: limited=%v, cached=%d", limited.startupMemoryLimited, len(limited.packets))
	}
	complete := NewPacketStore(db, &PacketStoreConfig{})
	complete.config = cfg
	if err := complete.Load(); err != nil {
		t.Fatal(err)
	}
	if len(complete.packets) != len(decoded) {
		t.Fatalf("in-memory reference loaded %d/%d packets", len(complete.packets), len(decoded))
	}
	all := make([]string, len(decoded))
	for i := range all {
		all[i] = fmt.Sprintf("area-%02d", len(decoded)-i)
	}
	inside := []string{"area-13", "area-10", "area-08", "area-05", "area-03", "area-01"}
	insideAAA := []string{"area-13", "area-08", "area-05", "area-03", "area-01"}
	ptype, route := PayloadADVERT, 1
	for _, tc := range []struct {
		name string
		q    PacketQuery
		want []string
	}{
		{"polygon", PacketQuery{Area: "POLY"}, inside},
		{"box", PacketQuery{Area: "BOX"}, inside},
		{"region", PacketQuery{Area: "POLY", Region: "AAA"}, insideAAA},
		{"combined-bindings", PacketQuery{Area: "POLY", Region: "AAA", Observer: "area-observer-a", Type: &ptype, Route: &route}, insideAAA},
		{"unknown-region", PacketQuery{Area: "POLY", Region: "UNKNOWN"}, nil},
		// Preserve the existing nil-set contract: these do not restrict packets.
		{"unknown-area", PacketQuery{Area: "UNKNOWN"}, all},
		{"empty-area", PacketQuery{Area: "EMPTY"}, all},
		{"no-area", PacketQuery{}, all},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, source := range []struct {
				name  string
				store *PacketStore
			}{{"memory", complete}, {"sql-fallback", limited}} {
				for _, grouped := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/grouped-%t", source.name, grouped), func(t *testing.T) {
						query := source.store.QueryPackets
						if grouped {
							query = source.store.QueryGroupedPackets
						}
						q := tc.q // Deliberately no Since: the memory limit must trigger SQL.
						q.Limit = 2
						for offset := 0; offset <= len(tc.want); offset += q.Limit {
							q.Offset = offset
							result := query(q)
							if result.Total != len(tc.want) {
								t.Errorf("offset %d total=%d, want %d", offset, result.Total, len(tc.want))
							}
							got := make([]string, 0, len(result.Packets))
							for _, packet := range result.Packets {
								hash, _ := packet["hash"].(string)
								got = append(got, hash)
							}
							wantPage := tc.want[offset:min(offset+q.Limit, len(tc.want))]
							if !slices.Equal(got, wantPage) {
								t.Errorf("offset %d hashes=%v, want %v", offset, got, wantPage)
							}
						}
					})
				}
			}
		})
	}
}

func TestAreaFilterSQLLargeNodeSet(t *testing.T) {
	db := setupTestDBv2(t)
	copyTestRows(t, db.conn, "nodes", []string{"public_key", "lat", "lon"}, 10000, func(i int) []any {
		return []any{fmt.Sprintf("area-node-%05d", i), 11.0, 11.0}
	})
	mustExecDB(t, db, `INSERT INTO transmissions(raw_hex,hash,first_seen,payload_type,decoded_json) VALUES
		('AA','large-area-match','2026-01-01T00:00:00Z',4,'{"pubKey":"area-node-00001"}'),
		('BB','large-area-outside','2026-01-01T00:01:00Z',4,'{"pubKey":"area-outside"}')`)
	s := newTestStoreWithDB(t, db, &Config{Areas: map[string]AreaEntry{
		"LARGE": {Polygon: [][2]float64{{10, 10}, {10, 12}, {12, 12}, {12, 10}}},
	}})
	s.startupMemoryLimited = true
	s.oldestLoaded = "2026-01-01T00:01:00Z"
	ptype := PayloadADVERT
	for _, grouped := range []bool{false, true} {
		t.Run(fmt.Sprintf("grouped-%t", grouped), func(t *testing.T) {
			query := s.QueryPackets
			if grouped {
				query = s.QueryGroupedPackets
			}
			result := query(PacketQuery{Area: "LARGE", Type: &ptype, Limit: 1})
			if result.Total != 1 || len(result.Packets) != 1 || result.Packets[0]["hash"] != "large-area-match" {
				t.Fatalf("10K node keys: total=%d, packets=%v", result.Total, result.Packets)
			}
		})
	}
}
