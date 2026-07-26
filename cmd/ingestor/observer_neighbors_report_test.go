package main

import "testing"

// #1865 follow-up (cwichura, PR #1867): TouchObserverNeighborsReport records
// that an observer sends /neighbors reports at all, so the UI can identify
// which observers have the opt-in firmware feature enabled.
func TestTouchObserverNeighborsReport(t *testing.T) {
	store := openNeighborsStore(t)
	if _, err := store.db.Exec(`INSERT INTO observers (id, name) VALUES (?, ?)`, "obs-a", "Observer A"); err != nil {
		t.Fatalf("seed observer: %v", err)
	}

	getAt := func() string {
		var at string
		if err := store.db.QueryRow(`SELECT COALESCE(last_neighbors_report_at, '') FROM observers WHERE id = ?`, "obs-a").Scan(&at); err != nil {
			t.Fatalf("select last_neighbors_report_at: %v", err)
		}
		return at
	}

	if at := getAt(); at != "" {
		t.Fatalf("pre-check: expected empty, got %q", at)
	}

	if err := store.TouchObserverNeighborsReport("obs-a", "2026-07-25T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if at := getAt(); at != "2026-07-25T12:00:00Z" {
		t.Errorf("last_neighbors_report_at = %q, want '2026-07-25T12:00:00Z'", at)
	}

	// Out-of-order older report must not roll the timestamp backwards.
	if err := store.TouchObserverNeighborsReport("obs-a", "2026-07-24T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if at := getAt(); at != "2026-07-25T12:00:00Z" {
		t.Errorf("after stale report, last_neighbors_report_at = %q, want unchanged '2026-07-25T12:00:00Z'", at)
	}

	// A later report advances it, canonicalized to UTC.
	if err := store.TouchObserverNeighborsReport("obs-a", "2026-07-26T15:00:00+02:00"); err != nil {
		t.Fatal(err)
	}
	if at := getAt(); at != "2026-07-26T13:00:00Z" {
		t.Errorf("last_neighbors_report_at = %q, want '2026-07-26T13:00:00Z'", at)
	}

	// Blank/unparseable timestamp is a no-op, never regresses to empty.
	if err := store.TouchObserverNeighborsReport("obs-a", ""); err != nil {
		t.Fatal(err)
	}
	if at := getAt(); at != "2026-07-26T13:00:00Z" {
		t.Errorf("after blank report, last_neighbors_report_at = %q, want unchanged", at)
	}

	// Unknown observer_id matches no row — must not error.
	if err := store.TouchObserverNeighborsReport("no-such-observer", "2026-07-26T00:00:00Z"); err != nil {
		t.Errorf("touch for unknown observer should be a no-op, got error: %v", err)
	}
}

// handleNeighborsReport must touch the observer's last_neighbors_report_at
// even when the report carries no usable scope evidence (no self, no
// responded neighbors) — the mere existence of the report is the signal.
func TestHandleNeighborsReport_TouchesObserverEvenWithoutScopeEvidence(t *testing.T) {
	store := openNeighborsStore(t)
	if _, err := store.db.Exec(`INSERT INTO observers (id, name) VALUES (?, ?)`, "obs-b", "Observer B"); err != nil {
		t.Fatalf("seed observer: %v", err)
	}

	report := map[string]interface{}{
		"timestamp": "2026-07-26T10:00:00Z",
		"neighbors": []interface{}{
			map[string]interface{}{"pubkey": "deadbeef", "scopes": "", "status": "timeout"},
		},
	}
	handleNeighborsReport(store, "test", "obs-b", report)

	var at string
	if err := store.db.QueryRow(`SELECT COALESCE(last_neighbors_report_at, '') FROM observers WHERE id = ?`, "obs-b").Scan(&at); err != nil {
		t.Fatalf("select last_neighbors_report_at: %v", err)
	}
	if at != "2026-07-26T10:00:00Z" {
		t.Errorf("last_neighbors_report_at = %q, want '2026-07-26T10:00:00Z'", at)
	}
}
