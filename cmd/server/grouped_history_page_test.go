package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestGroupedHistoryPagePreservesFiltersAndDetails(t *testing.T) {
	db, writer := openPostgresValueFixture(t)
	now := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := writer.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO observers(rowid,id,name,iata) VALUES(1,'history-a','Observer A','AAA'),(2,'history-b','Observer B','BBB')`)
	for id, age := range []time.Duration{48 * time.Hour, time.Minute, 2 * time.Minute, 12 * time.Hour} {
		exec(`INSERT INTO transmissions(id,raw_hex,hash,first_seen,last_seen,payload_type,scope_name) VALUES($1,'AA',$2,$3,0,4,'history-scope')`, id+1, fmt.Sprintf("history-%d", id+1), now.Add(-age).Format(time.RFC3339))
	}
	exec(`INSERT INTO observations(id,transmission_id,observer_idx,path_json,timestamp,snr) VALUES
		(1,1,1,'["aa","bb","cc"]',$1,3),
		(2,1,2,'["dd"]',$2,2),
		(3,1,1,'["aa"]',$3,1),
		(4,2,1,'[]',$4,4),
		(5,4,1,'[]',$2,5)`, now.Add(-25*time.Hour).Unix(), now.Add(-5*time.Minute).Unix(), now.Add(-26*time.Hour).Unix(), now.Add(-72*time.Hour).Unix())
	q := PacketQuery{Since: now.Add(-24 * time.Hour).Format(time.RFC3339), Observer: "history-a", Limit: 1, Offset: 1}
	result, err := db.QueryGroupedPackets(q)
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || len(result.Packets) != 1 {
		t.Fatalf("filtered page/total = %#v", result)
	}
	row := result.Packets[0]
	// Equal latest timestamps are paged deterministically by descending ID.
	// The since/observer predicates match independently, as before; details
	// still include every observation, including ones outside that window.
	if row["hash"] != "history-1" || row["count"] != 3 || row["observer_count"] != 2 || row["observer_id"] != "history-a" || row["path_json"] != `["aa","bb","cc"]` || row["scope_name"] != "history-scope" {
		t.Fatalf("history details changed or tie page unstable: %#v", row)
	}
	if got := strings.Join(row["distinct_iatas"].([]string), ","); got != "AAA,BBB" {
		t.Fatalf("IATAs restricted to filter window: %q", got)
	}
	if row["latest"] != now.Add(-5*time.Minute).Format("2006-01-02T15:04:05.000Z") {
		t.Fatalf("latest changed: %#v", row["latest"])
	}
	all, err := db.QueryGroupedPackets(PacketQuery{Limit: 10})
	if err != nil || all.Total != 4 || len(all.Packets) != 4 {
		t.Fatalf("all history: %#v / %v", all, err)
	}
	if all.Packets[0]["hash"] != "history-3" || all.Packets[0]["latest"] != now.Add(-2*time.Minute).Format(time.RFC3339) {
		t.Fatalf("no-observation first_seen fallback changed: %#v", all.Packets[0])
	}
	if all.Packets[3]["hash"] != "history-2" {
		t.Fatalf("ordered by first_seen/denormalized last_seen instead of observations: %#v", all.Packets)
	}
	end, err := db.QueryGroupedPackets(PacketQuery{Limit: 1, Offset: 10})
	if err != nil || end.Total != 4 || len(end.Packets) != 0 {
		t.Fatalf("empty page lost total: %#v / %v", end, err)
	}
}

func TestPostgresGroupedHistoryHydratesOnlyPage(t *testing.T) {
	postgresOnly(t)
	db := startupMemoryFixture(t, 1000, 65, false)
	sql := db.groupedPacketSelectSQL("", 0)
	var raw []byte
	if err := db.conn.QueryRow("EXPLAIN (ANALYZE, VERBOSE, FORMAT JSON) "+sql, 2, 10).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	type planNode struct {
		Type      string     `json:"Node Type"`
		Rows      float64    `json:"Actual Rows"`
		Loops     float64    `json:"Actual Loops"`
		Output    []string   `json:"Output"`
		SortKey   []string   `json:"Sort Key"`
		Relation  string     `json:"Relation Name"`
		Direction string     `json:"Scan Direction"`
		Condition string     `json:"Index Cond"`
		Plans     []planNode `json:"Plans"`
	}
	var plans []struct{ Plan planNode }
	if err := json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 {
		t.Fatalf("invalid plan: %v", err)
	}
	pending := []planNode{plans[0].Plan}
	foundBestPath := false
	foundLatestSeek := false
	for len(pending) > 0 {
		n := pending[0]
		pending = append(pending[1:], n.Plans...)
		if strings.Contains(strings.Join(n.SortKey, " "), "length(") {
			foundBestPath = true
			if n.Loops > 2 {
				t.Fatalf("two-row page hydrated %g best-path groups", n.Loops)
			}
		}
		if n.Type == "Aggregate" && strings.Contains(strings.Join(n.Output, " "), "max(to_char(") {
			t.Fatal("latest still formats every observation before aggregating")
		}
		if n.Relation == "observations" {
			t.Logf("observation scan: %s direction=%s rows=%g loops=%g condition=%s", n.Type, n.Direction, n.Rows, n.Loops, n.Condition)
		}
		condition := strings.ReplaceAll(n.Condition, `"`, "")
		if n.Relation == "observations" && strings.Contains(n.Type, "Index") && n.Direction == "Backward" && strings.Contains(condition, "timestamp IS NOT NULL") {
			foundLatestSeek = true
			if n.Rows != 1 || !strings.Contains(condition, "transmission_id") {
				t.Fatalf("latest lookup scanned observations instead of seeking one: rows=%g, condition=%s", n.Rows, n.Condition)
			}
		}
	}
	if !foundBestPath {
		t.Fatal("plan did not exercise best-path selection")
	}
	if !foundLatestSeek {
		t.Fatal("latest lookup did not use the existing timestamp index")
	}
}

func TestGroupedHistoryLegacyV2(t *testing.T) {
	dsn := postgresTestDSN(t)
	createTestDBAt(t, dsn, 2)
	db, err := openFixtureReader(t, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	result, err := db.QueryGroupedPackets(PacketQuery{Limit: 1})
	if err != nil || result.Total != 2 || len(result.Packets) != 1 {
		t.Fatalf("legacy page: %#v / %v", result, err)
	}
	row := result.Packets[0]
	if row["hash"] != "h0002" || row["latest"] != "2026-01-01T00:02:00Z" || row["count"] != 1 || row["observer_id"] != "obs1" {
		t.Fatalf("legacy timestamp or observer changed: %#v", row)
	}
}

func BenchmarkGroupedPacketHistory(b *testing.B) {
	db := startupMemoryFixture(b, 6000, 65, false)
	for _, limit := range []int{1, 5000} {
		b.Run(fmt.Sprintf("page-%d", limit), func(b *testing.B) {
			q := PacketQuery{Since: time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339), Limit: limit}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				result, err := db.QueryGroupedPackets(q)
				if err != nil || result.Total != 6000 || len(result.Packets) != limit {
					b.Fatalf("history query: %v / %v", result, err)
				}
			}
		})
	}
}
