package main

import (
	"testing"
	"time"
)

// Subpaths are counted per request from the stored paths rather than kept in
// a key -> transmissions index. These tests pin the counts the index gave:
// every contiguous run of 2-8 hops, once per occurrence, keyed in lower case.

func setupSubpathCountDB(t *testing.T) *DB {
	t.Helper()
	db := setupTestDB(t)
	now := time.Now().UTC()
	seen := now.Add(-time.Hour).Format(time.RFC3339)
	epoch := now.Add(-time.Hour).Unix()
	db.conn.Exec(`INSERT INTO observers (id, name, iata, last_seen, first_seen, packet_count)
		VALUES ('obs1', 'Observer One', 'SJC', ?, '2025-01-01T00:00:00Z', 100)`, seen)
	paths := []string{
		`["AA","bb","aa","bb"]`, // mixed case; "aa,bb" twice in one path
		`["aa","bb"]`,
		`["01","02","03","04","05","06","07","08","09"]`, // longer than 8
		`["cc"]`, // one hop: no subpaths, not a path for the totals
	}
	for i, pj := range paths {
		db.conn.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, decoded_json)
			VALUES ('01', ?, ?, 1, 4, '{}')`, "subpath_count_"+string(rune('a'+i)), seen)
		db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, snr, rssi, path_json, timestamp)
			VALUES (?, 1, 10.0, -90, ?, ?)`, i+1, pj, epoch)
	}
	return db
}

func subpathCounts(t *testing.T, r map[string]interface{}) (map[string]int, int) {
	t.Helper()
	out := map[string]int{}
	for _, sp := range r["subpaths"].([]map[string]interface{}) {
		out[sp["path"].(string)] = sp["count"].(int)
	}
	return out, r["totalPaths"].(int)
}

func loadedSubpathStore(t *testing.T) *PacketStore {
	t.Helper()
	db := setupSubpathCountDB(t)
	t.Cleanup(func() { db.Close() })
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if !store.WaitIndexesReady(5 * time.Second) {
		t.Fatal("indexes never became ready")
	}
	return store
}

func TestSubpathCountsPerOccurrenceCaseFolded(t *testing.T) {
	store := loadedSubpathStore(t)
	counts, total := subpathCounts(t, store.GetAnalyticsSubpaths("", 2, 2, 100))
	if total != 3 {
		t.Errorf("totalPaths = %d, want 3 (the one-hop path is not counted)", total)
	}
	if counts["aa → bb"] != 3 {
		t.Errorf(`"aa → bb" = %d, want 3 (twice in the first path, once in the second): %v`, counts["aa → bb"], counts)
	}
	if counts["bb → aa"] != 1 {
		t.Errorf(`"bb → aa" = %d, want 1`, counts["bb → aa"])
	}
}

func TestSubpathCountsStopAtEightHops(t *testing.T) {
	store := loadedSubpathStore(t)
	counts, _ := subpathCounts(t, store.GetAnalyticsSubpaths("", 8, 20, 100))
	if len(counts) != 2 {
		t.Errorf("runs of 8+ hops: %v, want the two 8-hop runs of the 9-hop path only", counts)
	}
}

func TestSubpathBulkMatchesSingle(t *testing.T) {
	store := loadedSubpathStore(t)
	groups := []subpathGroup{{2, 2, 50}, {3, 3, 50}, {4, 8, 50}}
	bulk := store.GetAnalyticsSubpathsBulk("", groups)
	store.cacheMu.Lock()
	store.subpathCache = map[string]*cachedResult{}
	store.cacheMu.Unlock()
	for i, g := range groups {
		b, bt := subpathCounts(t, bulk[i])
		s, st := subpathCounts(t, store.GetAnalyticsSubpaths("", g.MinLen, g.MaxLen, g.Limit))
		if bt != st || len(b) != len(s) {
			t.Fatalf("group %v: bulk %d paths %v, single %d paths %v", g, bt, b, st, s)
		}
		for k, v := range s {
			if b[k] != v {
				t.Errorf("group %v: %q bulk %d single %d", g, k, b[k], v)
			}
		}
	}
}

func TestSubpathDetailCountsOccurrences(t *testing.T) {
	store := loadedSubpathStore(t)
	for _, hops := range [][]string{{"aa", "bb"}, {"AA", "BB"}} {
		d := store.GetSubpathDetail(hops)
		if got := d["totalMatches"].(int); got != 3 {
			t.Errorf("detail %v: totalMatches = %d, want 3", hops, got)
		}
	}
	if got := store.GetSubpathDetail([]string{"01", "02", "03", "04", "05", "06", "07", "08", "09"})["totalMatches"].(int); got != 0 {
		t.Errorf("a 9-hop run is not a subpath; totalMatches = %d, want 0", got)
	}
	if got := store.GetSubpathDetail([]string{"zz", "yy"})["totalMatches"].(int); got != 0 {
		t.Errorf("unknown subpath: totalMatches = %d, want 0", got)
	}
}

// The totals the percentages divide by are counted with the subpaths, so
// evicted transmissions drop out of them.
func TestEvictStaleDropsEvictedPathsFromTotals(t *testing.T) {
	now := time.Now().UTC()
	store := makeTestStore(10, now.Add(-48*time.Hour), 0)
	identity := func(h string) string { return h }
	groups := []subpathGroup{{MinLen: 2, MaxLen: 8, Limit: 10}}
	if _, total := store.topNamedSubpaths(groups, identity); total != 10 {
		t.Fatalf("fixture: totalPaths = %d, want 10", total)
	}
	for i := 5; i < 10; i++ {
		store.packets[i].FirstSeen = now.Add(-time.Hour).Format(time.RFC3339)
	}
	store.retentionHours = 24
	if n := store.EvictStale(); n != 5 {
		t.Fatalf("evicted %d, want 5", n)
	}
	if _, total := store.topNamedSubpaths(groups, identity); total != 5 {
		t.Errorf("totalPaths = %d after evicting 5 of 10, want 5", total)
	}
}
