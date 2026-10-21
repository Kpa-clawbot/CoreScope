package main

import (
	"fmt"
	"runtime"
	"testing"
)

func evictionResolvedStore(t testing.TB, n int) (*PacketStore, []int) {
	t.Helper()
	db := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	pk := func(i int) string { return fmt.Sprintf("%064x", i) }
	ids := make([]int, 0, n)
	for i := 1; i <= n; i++ {
		if _, err := db.conn.Exec(`INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (?, '00', ?, '2026-01-01T00:00:00Z')`, i, fmt.Sprintf("h%015d", i)); err != nil {
			t.Fatal(err)
		}
		for o := 0; o < 3; o++ {
			rp := `["` + pk(i%9) + `",null,"` + pk(o+20) + `","` + pk(i%9) + `"]`
			if _, err := db.conn.Exec(`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path) VALUES (?, 1, '["aa","bb","cc","dd"]', 0, ?)`, i, rp); err != nil {
				t.Fatal(err)
			}
		}
		ids = append(ids, i)
	}
	return &PacketStore{db: db}, ids
}

// Eviction looks up the resolved pubkeys of every packet it drops, once a
// minute in steady state. It decoded each resolved_path into a pointer per
// hop and built a de-dup map per packet: about 130MB of allocations per
// five minutes on a busy week.
func TestResolvedPubkeysForEvictionBatch(t *testing.T) {
	s, ids := evictionResolvedStore(t, 300)
	got := s.resolvedPubkeysForEvictionBatch(ids)
	want := []string{fmt.Sprintf("%064x", 1), fmt.Sprintf("%064x", 20), fmt.Sprintf("%064x", 21), fmt.Sprintf("%064x", 22)}
	if fmt.Sprint(got[1]) != fmt.Sprint(want) {
		t.Fatalf("tx 1: %v, want %v (first-seen order, de-duplicated)", got[1], want)
	}
	if len(got) != 300 {
		t.Fatalf("%d packets resolved, want 300", len(got))
	}
	var a, b runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&a)
	_ = s.resolvedPubkeysForEvictionBatch(ids)
	runtime.ReadMemStats(&b)
	per := float64(b.Mallocs-a.Mallocs) / float64(len(ids))
	t.Logf("%.1f allocations per evicted packet (3 observations, 4 hops each)", per)
	if per > 20 {
		t.Fatalf("%.1f allocations per evicted packet, limit 20", per)
	}
}
