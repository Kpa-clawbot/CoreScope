package main

import (
	"sort"
	"strings"
	"testing"
	"time"
)

func makeRp(s string) *string { return &s }

func TestRelayIndexInsertPaths_SingleNode(t *testing.T) {
	idx := make(map[string][]int64)
	pk := "aabbccdd11223344"
	millis := time.Now().Add(-30 * time.Minute).UnixMilli()
	relayIndexInsertPaths(idx, millis, []*string{makeRp(pk)})
	if len(idx[pk]) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(idx[pk]))
	}
	if idx[pk][0] != millis {
		t.Errorf("timestamp mismatch: got %d, want %d", idx[pk][0], millis)
	}
}

func TestRelayIndexInsertPaths_SortedOrder(t *testing.T) {
	idx := make(map[string][]int64)
	pk := "aabbccdd11223344"
	ms1 := time.Now().Add(-2 * time.Hour).UnixMilli()
	ms2 := time.Now().Add(-30 * time.Minute).UnixMilli()

	// Insert newer first, expect sorted ascending
	relayIndexInsertPaths(idx, ms2, []*string{makeRp(pk)})
	relayIndexInsertPaths(idx, ms1, []*string{makeRp(pk)})

	if len(idx[pk]) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(idx[pk]))
	}
	if !sort.SliceIsSorted(idx[pk], func(i, j int) bool { return idx[pk][i] < idx[pk][j] }) {
		t.Error("relayTimes slice not sorted ascending")
	}
}

func TestRelayIndexInsertPaths_MultipleNodes(t *testing.T) {
	idx := make(map[string][]int64)
	pk1 := "aabbccdd11223344"
	pk2 := "eeff001122334455"
	millis := time.Now().Add(-10 * time.Minute).UnixMilli()
	relayIndexInsertPaths(idx, millis, []*string{makeRp(pk1), makeRp(pk2)})
	if len(idx[pk1]) != 1 {
		t.Errorf("pk1: expected 1 entry, got %d", len(idx[pk1]))
	}
	if len(idx[pk2]) != 1 {
		t.Errorf("pk2: expected 1 entry, got %d", len(idx[pk2]))
	}
}

func TestRelayIndexInsertPaths_NilPaths(t *testing.T) {
	idx := make(map[string][]int64)
	relayIndexInsertPaths(idx, time.Now().UnixMilli(), nil) // must not panic
	if len(idx) != 0 {
		t.Error("expected empty index for nil paths")
	}
}

func TestRelayIndexInsertPaths_DuplicatePubkey(t *testing.T) {
	idx := make(map[string][]int64)
	pk := "aabbccdd11223344"
	millis := time.Now().UnixMilli()
	relayIndexInsertPaths(idx, millis, []*string{makeRp(pk), makeRp(pk)}) // same pubkey twice
	if len(idx[pk]) != 1 {
		t.Errorf("duplicate pubkey should produce only 1 entry, got %d", len(idx[pk]))
	}
}

func TestRelayIndexInsertPaths_LowercasesKey(t *testing.T) {
	idx := make(map[string][]int64)
	pkUpper := "AABBCCDD11223344"
	pkLower := strings.ToLower(pkUpper)
	millis := time.Now().UnixMilli()
	relayIndexInsertPaths(idx, millis, []*string{makeRp(pkUpper)})
	if len(idx[pkLower]) != 1 {
		t.Errorf("expected index keyed by lowercase, found %d entries at lowercase key", len(idx[pkLower]))
	}
	if len(idx[pkUpper]) != 0 {
		t.Errorf("expected no entry at uppercase key")
	}
}

func TestRelayIndexRemovePaths_RemovesEntry(t *testing.T) {
	idx := make(map[string][]int64)
	pk := "aabbccdd11223344"
	millis := time.Now().Add(-1 * time.Hour).UnixMilli()
	paths := []*string{makeRp(pk)}

	relayIndexInsertPaths(idx, millis, paths)
	if len(idx[pk]) != 1 {
		t.Fatal("setup: expected 1 entry")
	}
	relayIndexRemovePaths(idx, millis, paths)
	if _, ok := idx[pk]; ok {
		t.Error("expected key deleted after last entry removed")
	}
}

func TestRelayIndexRemovePaths_PartialRemove(t *testing.T) {
	idx := make(map[string][]int64)
	pk := "aabbccdd11223344"
	ms1 := time.Now().Add(-2 * time.Hour).UnixMilli()
	ms2 := time.Now().Add(-30 * time.Minute).UnixMilli()
	paths := []*string{makeRp(pk)}

	relayIndexInsertPaths(idx, ms1, paths)
	relayIndexInsertPaths(idx, ms2, paths)
	relayIndexRemovePaths(idx, ms1, paths)

	if len(idx[pk]) != 1 {
		t.Errorf("expected 1 entry after removing one, got %d", len(idx[pk]))
	}
}

func TestRelayMetrics_Counts(t *testing.T) {
	now := time.Now().UnixMilli()
	times := []int64{
		now - 90*60*1000, // 90 min ago — inside 24h, outside 1h
		now - 30*60*1000, // 30 min ago — inside both
		now - 10*60*1000, // 10 min ago — inside both
	}
	c1h, c24h, lastRelayed := relayMetrics(times, now)
	if c1h != 2 {
		t.Errorf("relay_count_1h: expected 2, got %d", c1h)
	}
	if c24h != 3 {
		t.Errorf("relay_count_24h: expected 3, got %d", c24h)
	}
	wantLast := time.UnixMilli(times[2]).UTC().Format(time.RFC3339)
	if lastRelayed != wantLast {
		t.Errorf("last_relayed: got %q, want %q", lastRelayed, wantLast)
	}
}

func TestRelayMetrics_EmptySlice(t *testing.T) {
	c1h, c24h, lastRelayed := relayMetrics(nil, time.Now().UnixMilli())
	if c1h != 0 || c24h != 0 || lastRelayed != "" {
		t.Errorf("empty slice: expected zeros and empty string, got %d %d %q", c1h, c24h, lastRelayed)
	}
}

func TestRelayMetrics_AllOutsideWindow(t *testing.T) {
	now := time.Now().UnixMilli()
	times := []int64{now - 30*24*60*60*1000} // 30 days ago
	c1h, c24h, _ := relayMetrics(times, now)
	if c1h != 0 || c24h != 0 {
		t.Errorf("expected 0/0 for old entry, got %d/%d", c1h, c24h)
	}
}

func TestRelayTimesWiredIntoIngest(t *testing.T) {
	srv, _ := setupTestServer(t)

	srv.store.mu.RLock()
	hopKeys := len(srv.store.byPathHop)
	relayKeys := len(srv.store.relayTimes)
	srv.store.mu.RUnlock()

	if hopKeys == 0 {
		t.Skip("no path-hop data in test store — skipping relay wiring test")
	}
	if srv.store.relayTimes == nil {
		t.Fatal("relayTimes map is nil after load")
	}
	if relayKeys == 0 {
		t.Fatalf("relayTimes not populated: byPathHop has %d keys but relayTimes has 0", hopKeys)
	}
	if relayKeys > hopKeys {
		t.Errorf("relayTimes has more keys (%d) than byPathHop (%d) — relay index should be a subset", relayKeys, hopKeys)
	}
	t.Logf("byPathHop keys: %d, relayTimes keys: %d", hopKeys, relayKeys)
}

func TestGetBulkHealthRepeaterRelayFields(t *testing.T) {
	srv, _ := setupTestServer(t)

	_, err := srv.db.conn.Exec(`INSERT OR IGNORE INTO nodes (public_key, name, role, last_seen, first_seen, advert_count)
		VALUES ('relay662test0001', 'TestRepeater662', 'repeater', datetime('now'), datetime('now'), 1)`)
	if err != nil {
		t.Fatalf("insert test node: %v", err)
	}

	pk := "relay662test0001"
	recentMs := time.Now().UnixMilli() - 10*60*1000 // 10 min ago
	srv.store.mu.Lock()
	srv.store.relayTimes[pk] = []int64{recentMs}
	srv.store.mu.Unlock()

	results := srv.store.GetBulkHealth(200, "")

	var found map[string]interface{}
	for _, r := range results {
		if r["public_key"] == pk {
			found = r
			break
		}
	}
	if found == nil {
		t.Fatal("test repeater not found in GetBulkHealth results")
	}

	stats, ok := found["stats"].(map[string]interface{})
	if !ok {
		t.Fatal("missing stats map in result")
	}

	if v, ok := stats["relay_count_1h"].(int); !ok || v != 1 {
		t.Errorf("relay_count_1h: expected 1, got %v", stats["relay_count_1h"])
	}
	if v, ok := stats["relay_count_24h"].(int); !ok || v != 1 {
		t.Errorf("relay_count_24h: expected 1, got %v", stats["relay_count_24h"])
	}
	if _, ok := stats["last_relayed"].(string); !ok {
		t.Errorf("last_relayed: expected string, got %T", stats["last_relayed"])
	}
}

func TestGetBulkHealthCompanionNoRelayFields(t *testing.T) {
	srv, _ := setupTestServer(t)

	_, err := srv.db.conn.Exec(`INSERT OR IGNORE INTO nodes (public_key, name, role, last_seen, first_seen, advert_count)
		VALUES ('comp662test0001', 'TestCompanion662', 'companion', datetime('now'), datetime('now'), 1)`)
	if err != nil {
		t.Fatalf("insert test node: %v", err)
	}

	pk := "comp662test0001"
	srv.store.mu.Lock()
	srv.store.relayTimes[pk] = []int64{time.Now().UnixMilli() - 5*60*1000}
	srv.store.mu.Unlock()

	results := srv.store.GetBulkHealth(200, "")
	for _, r := range results {
		if r["public_key"] == pk {
			stats, _ := r["stats"].(map[string]interface{})
			if _, present := stats["relay_count_24h"]; present {
				t.Error("relay_count_24h should be absent for companion nodes")
			}
			return
		}
	}
	t.Fatal("test companion not found in GetBulkHealth results")
}

func TestGetBulkHealthRepeaterNoRelayActivity(t *testing.T) {
	srv, _ := setupTestServer(t)

	_, err := srv.db.conn.Exec(`INSERT OR IGNORE INTO nodes (public_key, name, role, last_seen, first_seen, advert_count)
		VALUES ('relay662idle001', 'IdleRepeater662', 'repeater', datetime('now'), datetime('now'), 1)`)
	if err != nil {
		t.Fatalf("insert test node: %v", err)
	}

	results := srv.store.GetBulkHealth(200, "")
	for _, r := range results {
		if r["public_key"] == "relay662idle001" {
			stats, _ := r["stats"].(map[string]interface{})
			if v, ok := stats["relay_count_24h"].(int); !ok || v != 0 {
				t.Errorf("relay_count_24h: expected 0, got %v", stats["relay_count_24h"])
			}
			if _, present := stats["last_relayed"]; present {
				t.Error("last_relayed should be absent when no relay activity")
			}
			return
		}
	}
	t.Fatal("idle repeater not found in results")
}

func TestGetNodeHealthRepeaterRelayFields(t *testing.T) {
	srv, _ := setupTestServer(t)

	pk := "relay662node0001"
	_, err := srv.db.conn.Exec(`INSERT OR IGNORE INTO nodes (public_key, name, role, last_seen, first_seen, advert_count)
		VALUES ('relay662node0001', 'TestRepeaterNode662', 'repeater', datetime('now'), datetime('now'), 1)`)
	if err != nil {
		t.Fatalf("insert test node: %v", err)
	}

	recentMs := time.Now().UnixMilli() - 15*60*1000 // 15 min ago
	srv.store.mu.Lock()
	srv.store.relayTimes[pk] = []int64{recentMs}
	srv.store.mu.Unlock()

	result, err := srv.store.GetNodeHealth(pk)
	if err != nil {
		t.Fatalf("GetNodeHealth error: %v", err)
	}
	if result == nil {
		t.Fatal("GetNodeHealth returned nil")
	}

	stats, ok := result["stats"].(map[string]interface{})
	if !ok {
		t.Fatal("missing stats map in GetNodeHealth result")
	}

	if v, ok := stats["relay_count_1h"].(int); !ok || v != 1 {
		t.Errorf("relay_count_1h: expected 1, got %v", stats["relay_count_1h"])
	}
	if v, ok := stats["relay_count_24h"].(int); !ok || v != 1 {
		t.Errorf("relay_count_24h: expected 1, got %v", stats["relay_count_24h"])
	}
	if _, ok := stats["last_relayed"].(string); !ok {
		t.Errorf("last_relayed: expected string, got %T", stats["last_relayed"])
	}
}
