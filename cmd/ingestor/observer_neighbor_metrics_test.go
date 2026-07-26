package main

import "testing"

// #1865 follow-up: dborup spotted that the raw /neighbors payload also
// carries snr/heard_secs_ago per neighbor, previously dropped entirely.
// observer_neighbor_metrics is an APPEND-ONLY history (unlike
// observer_neighbors' current-only snapshot), inspired by the existing
// RF Health tab's observer_metrics pattern.

func floatPtr(f float64) *float64 { return &f }
func intPtr(i int) *int           { return &i }

func countObserverNeighborMetrics(t *testing.T, store *Store, observerID, pubkey string) int {
	t.Helper()
	var n int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM observer_neighbor_metrics WHERE observer_id = ? AND neighbor_pubkey = ?`,
		observerID, pubkey).Scan(&n); err != nil {
		t.Fatalf("count observer_neighbor_metrics: %v", err)
	}
	return n
}

func TestRecordObserverNeighborMetrics_Basic(t *testing.T) {
	store := openNeighborsStore(t)
	seedObserverForNeighbors(t, store, "obs-metrics-1")

	entries := []ObserverNeighborEntry{
		{Pubkey: "aaaa000000000000000000000000000000000000000000000000000000000001", Status: "responded", SNR: floatPtr(10.5), HeardSecsAgo: intPtr(75)},
		{Pubkey: "bbbb000000000000000000000000000000000000000000000000000000000002", Status: "timeout", SNR: floatPtr(-8.75), HeardSecsAgo: intPtr(77)},
	}
	if err := store.RecordObserverNeighborMetrics("obs-metrics-1", entries, "2026-07-26T12:00:00Z"); err != nil {
		t.Fatal(err)
	}

	var snr float64
	var heardSecsAgo int
	if err := store.db.QueryRow(`SELECT snr, heard_secs_ago FROM observer_neighbor_metrics WHERE observer_id = ? AND neighbor_pubkey = ? AND timestamp = ?`,
		"obs-metrics-1", "aaaa000000000000000000000000000000000000000000000000000000000001", "2026-07-26T12:00:00Z").Scan(&snr, &heardSecsAgo); err != nil {
		t.Fatalf("select: %v", err)
	}
	if snr != 10.5 || heardSecsAgo != 75 {
		t.Errorf("snr=%v heardSecsAgo=%v, want 10.5/75", snr, heardSecsAgo)
	}
	// Timeout entries still carry snr -- must be recorded too, not just
	// scope-query "responded" entries.
	if n := countObserverNeighborMetrics(t, store, "obs-metrics-1", "bbbb000000000000000000000000000000000000000000000000000000000002"); n != 1 {
		t.Errorf("expected 1 row for the timeout neighbor's SNR reading, got %d", n)
	}
}

func TestRecordObserverNeighborMetrics_AccumulatesAcrossReports(t *testing.T) {
	store := openNeighborsStore(t)
	seedObserverForNeighbors(t, store, "obs-metrics-2")
	pk := "cccc000000000000000000000000000000000000000000000000000000000003"

	if err := store.RecordObserverNeighborMetrics("obs-metrics-2", []ObserverNeighborEntry{
		{Pubkey: pk, Status: "responded", SNR: floatPtr(5)},
	}, "2026-07-26T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordObserverNeighborMetrics("obs-metrics-2", []ObserverNeighborEntry{
		{Pubkey: pk, Status: "responded", SNR: floatPtr(6)},
	}, "2026-07-26T13:00:00Z"); err != nil {
		t.Fatal(err)
	}
	// Unlike ReplaceObserverNeighbors, this is a time-series -- both rows
	// must survive, not just the latest.
	if n := countObserverNeighborMetrics(t, store, "obs-metrics-2", pk); n != 2 {
		t.Fatalf("expected 2 accumulated history rows, got %d", n)
	}
}

func TestRecordObserverNeighborMetrics_OutOfOrderStillRecorded(t *testing.T) {
	store := openNeighborsStore(t)
	seedObserverForNeighbors(t, store, "obs-metrics-3")
	pk := "dddd000000000000000000000000000000000000000000000000000000000004"

	// Newer report first, then an older out-of-order one -- unlike
	// ReplaceObserverNeighbors' snapshot guard, BOTH are valid history at
	// their own timestamp and must both be recorded.
	if err := store.RecordObserverNeighborMetrics("obs-metrics-3", []ObserverNeighborEntry{
		{Pubkey: pk, Status: "responded", SNR: floatPtr(9)},
	}, "2026-07-26T14:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordObserverNeighborMetrics("obs-metrics-3", []ObserverNeighborEntry{
		{Pubkey: pk, Status: "responded", SNR: floatPtr(3)},
	}, "2026-07-26T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if n := countObserverNeighborMetrics(t, store, "obs-metrics-3", pk); n != 2 {
		t.Errorf("expected both the newer and the out-of-order older reading recorded, got %d rows", n)
	}
}

func TestRecordObserverNeighborMetrics_SkipsEntriesWithoutSNR(t *testing.T) {
	store := openNeighborsStore(t)
	seedObserverForNeighbors(t, store, "obs-metrics-4")
	pk := "eeee000000000000000000000000000000000000000000000000000000000005"

	if err := store.RecordObserverNeighborMetrics("obs-metrics-4", []ObserverNeighborEntry{
		{Pubkey: pk, Status: "timeout", SNR: nil},
	}, "2026-07-26T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if n := countObserverNeighborMetrics(t, store, "obs-metrics-4", pk); n != 0 {
		t.Errorf("expected no row when SNR is nil, got %d", n)
	}
}

func TestPruneOldNeighborMetrics(t *testing.T) {
	store := openNeighborsStore(t)
	seedObserverForNeighbors(t, store, "obs-metrics-5")
	pk := "ffff000000000000000000000000000000000000000000000000000000000006"

	old := "2020-01-01T00:00:00Z"
	recent := "2026-07-26T12:00:00Z"
	if err := store.RecordObserverNeighborMetrics("obs-metrics-5", []ObserverNeighborEntry{{Pubkey: pk, SNR: floatPtr(1)}}, old); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordObserverNeighborMetrics("obs-metrics-5", []ObserverNeighborEntry{{Pubkey: pk, SNR: floatPtr(2)}}, recent); err != nil {
		t.Fatal(err)
	}
	if n, err := store.PruneOldNeighborMetrics(30); err != nil {
		t.Fatal(err)
	} else if n != 1 {
		t.Fatalf("expected 1 row pruned, got %d", n)
	}
	if n := countObserverNeighborMetrics(t, store, "obs-metrics-5", pk); n != 1 {
		t.Errorf("expected 1 row remaining after prune, got %d", n)
	}
}

func TestHandleNeighborsReport_RecordsSnrHistory(t *testing.T) {
	store := openNeighborsStore(t)
	seedObserverForNeighbors(t, store, "obs-metrics-6")

	report := map[string]interface{}{
		"timestamp": "2026-07-26T16:45:06.000000+00:00",
		"neighbors": []interface{}{
			map[string]interface{}{"pubkey": "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20", "snr": 10.0, "heard_secs_ago": 75.0, "scopes": "", "status": "timeout"},
			map[string]interface{}{"pubkey": "2102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20", "snr": 14.0, "heard_secs_ago": 83.0, "scopes": "dk", "status": "responded"},
		},
	}
	handleNeighborsReport(store, "test", "obs-metrics-6", report)

	var snr float64
	var heardSecsAgo int
	if err := store.db.QueryRow(`SELECT snr, heard_secs_ago FROM observer_neighbor_metrics WHERE observer_id = ? AND neighbor_pubkey = ?`,
		"obs-metrics-6", "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20").Scan(&snr, &heardSecsAgo); err != nil {
		t.Fatalf("select: %v", err)
	}
	if snr != 10.0 || heardSecsAgo != 75 {
		t.Errorf("snr=%v heardSecsAgo=%v, want 10.0/75 (timeout entry must still record SNR)", snr, heardSecsAgo)
	}
}
