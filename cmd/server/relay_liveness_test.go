package main

import (
	"sort"
	"strings"
	"testing"
	"time"
)

func TestAddTxToRelayTimeIndex_SingleNode(t *testing.T) {
	idx := make(map[string][]int64)
	pk := "aabbccdd11223344"
	ts := time.Now().Add(-30 * time.Minute).UTC()
	addTxToRelayTimeIndex(idx, ts.Format(time.RFC3339), []string{pk})
	if len(idx[pk]) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(idx[pk]))
	}
	wantMs := ts.UnixMilli()
	// RFC3339 has second precision, so allow ±1000ms
	if diff := idx[pk][0] - wantMs; diff < -1000 || diff > 1000 {
		t.Errorf("timestamp mismatch: got %d, want ~%d", idx[pk][0], wantMs)
	}
}

func TestAddTxToRelayTimeIndex_SortedOrder(t *testing.T) {
	idx := make(map[string][]int64)
	pk := "aabbccdd11223344"
	t1 := time.Now().Add(-2 * time.Hour).UTC()
	t2 := time.Now().Add(-30 * time.Minute).UTC()

	// Insert newer first, expect sorted ascending
	addTxToRelayTimeIndex(idx, t2.Format(time.RFC3339), []string{pk})
	addTxToRelayTimeIndex(idx, t1.Format(time.RFC3339), []string{pk})

	if len(idx[pk]) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(idx[pk]))
	}
	if !sort.SliceIsSorted(idx[pk], func(i, j int) bool { return idx[pk][i] < idx[pk][j] }) {
		t.Error("relayTimes slice not sorted ascending")
	}
}

func TestAddTxToRelayTimeIndex_MultipleNodes(t *testing.T) {
	idx := make(map[string][]int64)
	pk1 := "aabbccdd11223344"
	pk2 := "eeff001122334455"
	ts := time.Now().Add(-10 * time.Minute).UTC()
	addTxToRelayTimeIndex(idx, ts.Format(time.RFC3339), []string{pk1, pk2})
	if len(idx[pk1]) != 1 {
		t.Errorf("pk1: expected 1 entry, got %d", len(idx[pk1]))
	}
	if len(idx[pk2]) != 1 {
		t.Errorf("pk2: expected 1 entry, got %d", len(idx[pk2]))
	}
}

func TestAddTxToRelayTimeIndex_NilResolvedPath(t *testing.T) {
	idx := make(map[string][]int64)
	addTxToRelayTimeIndex(idx, time.Now().UTC().Format(time.RFC3339), nil) // must not panic
	if len(idx) != 0 {
		t.Error("expected empty index for nil pubkeys")
	}
}

func TestAddTxToRelayTimeIndex_DuplicatePubkeyInPath(t *testing.T) {
	idx := make(map[string][]int64)
	pk := "aabbccdd11223344"
	ts := time.Now().UTC()
	addTxToRelayTimeIndex(idx, ts.Format(time.RFC3339), []string{pk, pk}) // same pubkey twice
	if len(idx[pk]) != 1 {
		t.Errorf("duplicate pubkey should produce only 1 entry, got %d", len(idx[pk]))
	}
}

func TestRemoveFromRelayTimeIndex_RemovesEntry(t *testing.T) {
	idx := make(map[string][]int64)
	pk := "aabbccdd11223344"
	ts := time.Now().Add(-1 * time.Hour).UTC()
	firstSeen := ts.Format(time.RFC3339)

	addTxToRelayTimeIndex(idx, firstSeen, []string{pk})
	if len(idx[pk]) != 1 {
		t.Fatal("setup: expected 1 entry")
	}
	removeFromRelayTimeIndex(idx, firstSeen, []string{pk})
	if _, ok := idx[pk]; ok {
		t.Error("expected key deleted after last entry removed")
	}
}

func TestRemoveFromRelayTimeIndex_PartialRemove(t *testing.T) {
	idx := make(map[string][]int64)
	pk := "aabbccdd11223344"
	t1 := time.Now().Add(-2 * time.Hour).UTC()
	t2 := time.Now().Add(-30 * time.Minute).UTC()
	fs1 := t1.Format(time.RFC3339)
	fs2 := t2.Format(time.RFC3339)

	addTxToRelayTimeIndex(idx, fs1, []string{pk})
	addTxToRelayTimeIndex(idx, fs2, []string{pk})
	removeFromRelayTimeIndex(idx, fs1, []string{pk})

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

func TestAddTxToRelayTimeIndex_LowercasesKey(t *testing.T) {
	idx := make(map[string][]int64)
	pkUpper := "AABBCCDD11223344"
	pkLower := strings.ToLower(pkUpper)
	ts := time.Now().UTC()
	addTxToRelayTimeIndex(idx, ts.Format(time.RFC3339), []string{pkUpper})
	if len(idx[pkLower]) != 1 {
		t.Errorf("expected index keyed by lowercase, found %d entries at lowercase key", len(idx[pkLower]))
	}
	if len(idx[pkUpper]) != 0 {
		t.Errorf("expected no entry at uppercase key")
	}
}
