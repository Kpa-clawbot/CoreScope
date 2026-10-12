package main

import (
	"strings"
	"testing"
	"time"
)

// A /neighbors report can come from any MQTT publisher, so the values it
// carries must be bounded. These tests cover the three guards: a timestamp
// far in the future (which would lock last-write-wins), a non-hex pubkey, and
// an oversized scope list.

func TestNeighborsReportFutureTimestampIsDropped(t *testing.T) {
	store := openNeighborsStore(t)
	pk := "ee00000000000000000000000000000000000000000000000000000000000001"
	seedNode(t, store, pk)

	fixed := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	reportNow = func() time.Time { return fixed }
	t.Cleanup(func() { reportNow = time.Now })

	// A report stamped a year ahead must not be written...
	if err := store.UpdateNodeConfiguredScope(pk, "evil", "2027-10-07T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if sc, _ := configuredScope(t, store, pk); sc.Valid && sc.String != "" {
		t.Fatalf("future-stamped report was stored: %q", sc.String)
	}
	// ...and a genuine report with a normal timestamp must still land after it.
	if err := store.UpdateNodeConfiguredScope(pk, "eu", "2026-10-07T11:59:00Z"); err != nil {
		t.Fatal(err)
	}
	if sc, _ := configuredScope(t, store, pk); sc.String != "#eu" {
		t.Fatalf("genuine report blocked: configured_scope = %q, want '#eu'", sc.String)
	}
	// Ordinary clock skew (inside maxReportFuture) is still accepted.
	if err := store.UpdateNodeConfiguredScope(pk, "de", "2026-10-07T12:03:00Z"); err != nil {
		t.Fatal(err)
	}
	if sc, _ := configuredScope(t, store, pk); sc.String != "#de" {
		t.Fatalf("slightly-ahead report rejected: configured_scope = %q, want '#de'", sc.String)
	}
}

func TestNeighborsReportIgnoresNonHexPubkeys(t *testing.T) {
	store := openNeighborsStore(t)
	pk := "ee00000000000000000000000000000000000000000000000000000000000002"
	seedNode(t, store, pk)

	msg := map[string]interface{}{
		"timestamp": "2026-10-06T12:00:00Z",
		"origin_id": "not-a-pubkey",
		"self":      map[string]interface{}{"scopes": "eu"},
		"neighbors": []interface{}{
			map[string]interface{}{"pubkey": "../etc/passwd", "status": "responded", "scopes": "eu"},
			map[string]interface{}{"pubkey": strings.ToUpper(pk), "status": "responded", "scopes": "dk"},
		},
	}
	handleNeighborsReport(store, "test", "not-a-pubkey-either", msg)

	// The valid neighbor was written; nothing errored on the junk keys.
	if sc, _ := configuredScope(t, store, pk); sc.String != "#dk" {
		t.Fatalf("valid neighbor not written: %q", sc.String)
	}
	var n int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM nodes WHERE configured_scope IS NOT NULL AND configured_scope != ''`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 node with a configured scope, got %d", n)
	}
}

func TestNeighborsReportOversizedScopeIsDropped(t *testing.T) {
	store := openNeighborsStore(t)
	pk := "ee00000000000000000000000000000000000000000000000000000000000003"
	seedNode(t, store, pk)

	huge := strings.Repeat("region,", 200) // ~1.6 KB after normalisation
	if err := store.UpdateNodeConfiguredScope(pk, huge, "2026-10-06T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if sc, _ := configuredScope(t, store, pk); sc.Valid && sc.String != "" {
		t.Fatalf("oversized scope list was stored (%d bytes)", len(sc.String))
	}
	// A normal list is unaffected.
	if err := store.UpdateNodeConfiguredScope(pk, "eu,dk,dk-aarhus", "2026-10-06T12:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if sc, _ := configuredScope(t, store, pk); sc.String != "#eu,#dk,#dk-aarhus" {
		t.Fatalf("normal scope list mangled: %q", sc.String)
	}
}
