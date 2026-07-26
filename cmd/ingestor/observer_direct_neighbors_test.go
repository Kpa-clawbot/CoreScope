package main

import "testing"

// #1865 follow-up "Direct Neighbors" panel: observer_neighbors is a
// CURRENT-ONLY snapshot of an observer's firmware-reported zero-hop
// neighbors, replaced wholesale on every /neighbors report.

func seedObserverForNeighbors(t *testing.T, store *Store, id string) {
	t.Helper()
	if _, err := store.db.Exec(`INSERT INTO observers (id, name) VALUES (?, ?)`, id, "obs_"+id); err != nil {
		t.Fatalf("seed observer %s: %v", id, err)
	}
}

func countObserverNeighbors(t *testing.T, store *Store, observerID string) int {
	t.Helper()
	var n int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM observer_neighbors WHERE observer_id = ?`, observerID).Scan(&n); err != nil {
		t.Fatalf("count observer_neighbors: %v", err)
	}
	return n
}

func TestReplaceObserverNeighbors_Basic(t *testing.T) {
	store := openNeighborsStore(t)
	seedObserverForNeighbors(t, store, "obs-nb-1")

	entries := []ObserverNeighborEntry{
		{Pubkey: "aaaa000000000000000000000000000000000000000000000000000000000001", Scopes: "#dk", Status: "responded"},
		{Pubkey: "bbbb000000000000000000000000000000000000000000000000000000000002", Status: "timeout"},
	}
	if err := store.ReplaceObserverNeighbors("obs-nb-1", entries, "2026-07-26T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if n := countObserverNeighbors(t, store, "obs-nb-1"); n != 2 {
		t.Fatalf("expected 2 neighbor rows, got %d", n)
	}

	var status string
	if err := store.db.QueryRow(`SELECT status FROM observer_neighbors WHERE observer_id = ? AND neighbor_pubkey = ?`,
		"obs-nb-1", "bbbb000000000000000000000000000000000000000000000000000000000002").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "timeout" {
		t.Errorf("status = %q, want 'timeout'", status)
	}
}

func TestReplaceObserverNeighbors_ShrinkingSetDropsStaleRows(t *testing.T) {
	store := openNeighborsStore(t)
	seedObserverForNeighbors(t, store, "obs-nb-2")

	first := []ObserverNeighborEntry{
		{Pubkey: "cccc000000000000000000000000000000000000000000000000000000000003", Status: "responded"},
		{Pubkey: "dddd000000000000000000000000000000000000000000000000000000000004", Status: "responded"},
	}
	if err := store.ReplaceObserverNeighbors("obs-nb-2", first, "2026-07-26T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if n := countObserverNeighbors(t, store, "obs-nb-2"); n != 2 {
		t.Fatalf("expected 2 rows after first report, got %d", n)
	}

	// One neighbor dropped off -- the next report only carries one entry.
	second := []ObserverNeighborEntry{
		{Pubkey: "cccc000000000000000000000000000000000000000000000000000000000003", Status: "responded"},
	}
	if err := store.ReplaceObserverNeighbors("obs-nb-2", second, "2026-07-26T13:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if n := countObserverNeighbors(t, store, "obs-nb-2"); n != 1 {
		t.Fatalf("expected 1 row after shrinking report, got %d (stale neighbor not dropped)", n)
	}
}

func TestReplaceObserverNeighbors_OutOfOrderReportIsIgnored(t *testing.T) {
	store := openNeighborsStore(t)
	seedObserverForNeighbors(t, store, "obs-nb-3")

	newer := []ObserverNeighborEntry{
		{Pubkey: "eeee000000000000000000000000000000000000000000000000000000000005", Status: "responded"},
	}
	// TouchObserverNeighborsReport must run first in production (main.go
	// sequences it that way) -- replicate that here since Replace's guard
	// reads the post-touch value.
	if err := store.TouchObserverNeighborsReport("obs-nb-3", "2026-07-26T14:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceObserverNeighbors("obs-nb-3", newer, "2026-07-26T14:00:00Z"); err != nil {
		t.Fatal(err)
	}

	// An older, out-of-order report arrives after the newer one. Touch is a
	// no-op (MAX keeps the newer value); Replace must also refuse to
	// overwrite the newer neighbor snapshot with stale data.
	older := []ObserverNeighborEntry{
		{Pubkey: "ffff000000000000000000000000000000000000000000000000000000000006", Status: "responded"},
	}
	if err := store.TouchObserverNeighborsReport("obs-nb-3", "2026-07-26T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceObserverNeighbors("obs-nb-3", older, "2026-07-26T10:00:00Z"); err != nil {
		t.Fatal(err)
	}

	var pk string
	if err := store.db.QueryRow(`SELECT neighbor_pubkey FROM observer_neighbors WHERE observer_id = ?`, "obs-nb-3").Scan(&pk); err != nil {
		t.Fatal(err)
	}
	if pk != "eeee000000000000000000000000000000000000000000000000000000000005" {
		t.Errorf("neighbor_pubkey = %q, want the newer report's entry (stale report must not win)", pk)
	}
	if n := countObserverNeighbors(t, store, "obs-nb-3"); n != 1 {
		t.Errorf("expected exactly 1 row, got %d", n)
	}
}

func TestHandleNeighborsReport_PopulatesDirectNeighbors(t *testing.T) {
	store := openNeighborsStore(t)
	seedObserverForNeighbors(t, store, "obs-nb-4")

	report := map[string]interface{}{
		"timestamp": "2026-07-26T15:00:00Z",
		"neighbors": []interface{}{
			map[string]interface{}{"pubkey": "1111000000000000000000000000000000000000000000000000000000000011", "scopes": "dk,eu", "status": "responded"},
			map[string]interface{}{"pubkey": "2222000000000000000000000000000000000000000000000000000000000022", "scopes": "", "status": "timeout"},
		},
	}
	handleNeighborsReport(store, "test", "obs-nb-4", report)

	if n := countObserverNeighbors(t, store, "obs-nb-4"); n != 2 {
		t.Fatalf("expected 2 direct-neighbor rows, got %d", n)
	}
	var scopes string
	if err := store.db.QueryRow(`SELECT scopes FROM observer_neighbors WHERE observer_id = ? AND neighbor_pubkey = ?`,
		"obs-nb-4", "1111000000000000000000000000000000000000000000000000000000000011").Scan(&scopes); err != nil {
		t.Fatal(err)
	}
	if scopes != "#dk,#eu" {
		t.Errorf("scopes = %q, want normalized '#dk,#eu'", scopes)
	}
}
