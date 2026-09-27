package main

import (
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/lora"
)

func advertAirtimeTx(id int, route *int, raw string) *StoreTx {
	tx := makeRelayAirtimeTx(id, PayloadADVERT, 120, 0, fmt.Sprintf("advert-%d", id))
	tx.RouteType = route
	tx.RawHex = raw
	return tx
}

func TestRelayAirtimeShare_AdvertRouting(t *testing.T) {
	route := func(n int) *int { return &n }
	for _, tc := range []struct {
		name            string
		route           *int
		raw, path, want string
	}{
		{"flood without hops", route(1), "1100aa", "[]", "flood"},
		{"transport flood without hops", route(0), "100000000000aa", "[]", "flood"},
		{"flood with hops", route(1), "1101ffaa", "[]", "flood"},
		{"direct zero hop", route(2), "1200aa", `["ff"]`, "zero_hop"},
		{"transport direct zero hop", route(3), "130102030400aa", `["ff"]`, "zero_hop"},
		{"two byte zero count", route(2), "1240aa", "[]", "other"},
		{"three byte zero count", route(2), "1280aa", "[]", "other"},
		{"direct nonempty original path", route(2), "1201ffaa", "[]", "other"},
		{"transport direct nonempty original path", route(3), "130000000001ffaa", "[]", "other"},
		{"unknown route", nil, "1200aa", "[]", "other"},
		{"invalid route", route(9), "1200aa", "[]", "other"},
		{"missing raw", route(2), "", "[]", "other"},
		{"missing path byte", route(2), "12", "[]", "other"},
		{"truncated transport", route(3), "130000", "[]", "other"},
		{"missing transport path byte", route(3), "1300000000", "[]", "other"},
		{"invalid header", route(2), "zz00aa", "[]", "other"},
		{"conflicting raw route", route(2), "1100aa", "[]", "other"},
		{"conflicting raw payload", route(2), "1600aa", "[]", "other"},
		{"invalid path hex", route(2), "12zzaa", "[]", "other"},
		{"invalid transport hex", route(3), "13zz00000000aa", "[]", "other"},
		{"reserved path encoding", route(2), "12c0aa", "[]", "other"},
		{"odd raw hex", route(2), "1200a", "[]", "other"},
		{"no payload bytes", route(2), "1200", "[]", "other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := advertAirtimeTx(1, tc.route, tc.raw)
			tx.PathJSON = tc.path // Longest observation's display path is not the original path.
			result := newRelayAirtimeShareTestStore([]*StoreTx{tx}).computeRelayAirtimeShare(TimeWindow{})
			rows := result["rows"].([]map[string]interface{})
			if len(rows) != 1 || rows[0]["advert_kind"] != tc.want {
				t.Fatalf("advert_kind = %v, want %q", rows, tc.want)
			}
			if rows[0]["payload_type"] != "ADVERT" || rows[0]["type"] != PayloadADVERT || rows[0]["count"] != 1 {
				t.Fatalf("legacy advert fields changed: %v", rows[0])
			}
		})
	}
}

func TestRelayAirtimeShare_AdvertSplitConservesMetric(t *testing.T) {
	flood, direct := RouteFlood, RouteDirect
	packets := []*StoreTx{
		advertAirtimeTx(1, &flood, "1100"+strings.Repeat("ab", 118)),
		advertAirtimeTx(2, &direct, "1200"+strings.Repeat("ab", 118)),
		advertAirtimeTx(3, &direct, "1200"+strings.Repeat("ab", 118)),
		advertAirtimeTx(4, nil, strings.Repeat("ab", 120)),
		makeRelayAirtimeTx(5, PayloadACK, 10, 0, "ack"),
	}
	store := newRelayAirtimeShareTestStore(packets)
	store.addToResolvedPubkeyIndex(1, []string{"relay-a", "relay-b", "relay-a"})
	store.addToResolvedPubkeyIndex(3, []string{"relay-c"}) // Do not force direct evidence to zero.
	store.addToResolvedPubkeyIndex(4, []string{"relay-d"})
	store.addToResolvedPubkeyIndex(5, []string{"relay-e"})
	result := store.computeRelayAirtimeShare(TimeWindow{})
	rows := result["rows"].([]map[string]interface{})
	if len(rows) != 4 {
		t.Fatalf("got %d buckets, want flood, zero-hop, other and ACK: %v", len(rows), rows)
	}
	toa := int64(lora.TimeOnAir(120, defaultLoRaPreset()))
	ackToA := int64(lora.TimeOnAir(10, defaultLoRaPreset()))
	wantScore := 4*toa + ackToA
	if result["total_count"] != 5 || result["total_score"] != wantScore {
		t.Fatalf("totals changed: %v", result)
	}
	countSum, scoreSum, countPctSum, airtimePctSum := 0, int64(0), 0.0, 0.0
	for _, row := range rows {
		count := row["count"].(int)
		score := row["score"].(int64)
		countSum += count
		scoreSum += score
		countPctSum += row["count_pct"].(float64)
		airtimePctSum += row["airtime_pct"].(float64)
		if math.Abs(row["count_pct"].(float64)-float64(count)/5*100) > 1e-9 || math.Abs(row["airtime_pct"].(float64)-float64(score)/float64(wantScore)*100) > 1e-9 {
			t.Fatalf("incorrect percentage: %v", row)
		}
		switch row["advert_kind"] {
		case "flood":
			if count != 1 || score != 2*toa {
				t.Fatalf("flood: %v", row)
			}
		case "zero_hop":
			if count != 2 || score != toa {
				t.Fatalf("zero-hop: %v", row)
			}
		case "other":
			if count != 1 || score != toa {
				t.Fatalf("other: %v", row)
			}
		default:
			if row["payload_type"] != "ACK" || row["type"] != PayloadACK || count != 1 || score != ackToA {
				t.Fatalf("non-advert changed: %v", row)
			}
		}
	}
	if countSum != 5 || scoreSum != wantScore || math.Abs(countPctSum-100) > 1e-9 || math.Abs(airtimePctSum-100) > 1e-9 {
		t.Fatalf("shares not conserved: %d %d %f %f", countSum, scoreSum, countPctSum, airtimePctSum)
	}
}

func TestRelayAirtimeShare_AdvertWindowDedupCacheAndZeroRelays(t *testing.T) {
	flood, direct := RouteFlood, RouteDirect
	first := advertAirtimeTx(1, &direct, "1200aa")
	duplicate := advertAirtimeTx(2, &flood, "1100aa")
	duplicate.Hash = first.Hash
	outside := advertAirtimeTx(3, &flood, "1100aa")
	outside.FirstSeen = "2025-12-01T00:00:00Z"
	ack := makeRelayAirtimeTx(4, PayloadACK, 10, 1, "ack")
	store := newRelayAirtimeShareTestStore([]*StoreTx{first, duplicate, outside, ack})
	store.rfCacheTTL = time.Minute
	store.addToResolvedPubkeyIndex(2, []string{"relay-a"})
	store.addToResolvedPubkeyIndex(3, []string{"relay-b"})
	store.addToResolvedPubkeyIndex(4, []string{"relay-c"})
	window := TimeWindow{Since: "2026-01-01T00:00:00Z", Until: "2026-01-02T00:00:00Z", Label: "test"}
	result := store.GetRelayAirtimeShareWithWindow(window)
	cached := store.GetRelayAirtimeShareWithWindow(window)
	if result["cached"] != false || cached["cached"] != true || result["window"] != "test" || !reflect.DeepEqual(result["rows"], cached["rows"]) {
		t.Fatalf("cache contract changed: %v / %v", result, cached)
	}
	rows := result["rows"].([]map[string]interface{})
	if len(rows) != 2 || result["total_count"] != 2 || rows[1]["advert_kind"] != "zero_hop" || rows[1]["count"] != 1 || rows[1]["score"] != int64(0) || rows[1]["airtime_pct"] != float64(0) || rows[1]["count_pct"] != float64(50) {
		t.Fatalf("zero relay row/window/dedup changed: %v", result)
	}
	if store.GetRelayAirtimeShareWithWindow(TimeWindow{})["total_count"] != 3 {
		t.Fatal("time-window caches collided")
	}
}

func TestRelayAirtimeShare_AdvertStableTies(t *testing.T) {
	flood, direct := RouteFlood, RouteDirect
	store := newRelayAirtimeShareTestStore([]*StoreTx{advertAirtimeTx(1, &direct, "1200aa"), advertAirtimeTx(2, nil, "000000"), advertAirtimeTx(3, &flood, "1100aa")})
	for i := 0; i < 25; i++ {
		rows := store.computeRelayAirtimeShare(TimeWindow{})["rows"].([]map[string]interface{})
		if len(rows) != 3 {
			t.Fatalf("got %d rows, want 3", len(rows))
		}
		for j, want := range []string{"flood", "other", "zero_hop"} {
			if rows[j]["advert_kind"] != want {
				t.Fatalf("unstable tie order: %v", rows)
			}
		}
	}
}

// Same workload is measured before/after #2041; construction is outside timing.
func BenchmarkRelayAirtimeShare30K(b *testing.B) {
	packets := make([]*StoreTx, 30000)
	for i := range packets {
		pt := PayloadACK
		if i%3 == 0 {
			pt = PayloadADVERT
		}
		tx := makeRelayAirtimeTx(i+1, pt, 120, 0, fmt.Sprintf("packet-%d", i))
		route := i % 4
		tx.RouteType = &route
		header := fmt.Sprintf("%02x", pt<<2|route)
		if route == 0 || route == 3 {
			header += "01020304"
		}
		tx.RawHex = header + "00" + strings.Repeat("ab", 120-len(header)/2-1)
		packets[i] = tx
	}
	store := newRelayAirtimeShareTestStore(packets)
	for _, tx := range packets {
		if tx.ID%4 != 0 {
			store.addToResolvedPubkeyIndex(tx.ID, []string{"relay-a", "relay-b", "relay-c"})
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.computeRelayAirtimeShare(TimeWindow{})
	}
}
