package main

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
)

func unmarshalResolvedPathLocal(s string) []*string {
	if s == "" {
		return nil
	}
	var out []*string
	if json.Unmarshal([]byte(s), &out) != nil {
		return nil
	}
	return out
}

// TestResolvePathPureFunction is a unit test for the pure resolvePath
// helper. Asserts:
//   - unique-prefix hops resolve to the full pubkey
//   - ambiguous-prefix hops resolve to nil
//   - unknown-prefix hops resolve to nil
//   - return slice length equals input hop count
//
// Regression gate for #1547 (resolved_path stopped being written).
func TestResolvePathPureFunction(t *testing.T) {
	idx := prefixIndex{
		// "aa" → exactly one pubkey
		"aa":         {"aaaaaaaaaa"},
		"aaaaaaaaaa": {"aaaaaaaaaa"},
		// "bb" → exactly one pubkey
		"bb":         {"bbbbbbbbbb"},
		"bbbbbbbbbb": {"bbbbbbbbbb"},
		// "cc" → ambiguous (2 candidates)
		"cc":         {"cccccccccc", "ccdddddddd"},
		"cccccccccc": {"cccccccccc"},
	}

	got := resolvePath([]string{"aa", "cc", "ff", "bb"}, idx)
	if len(got) != 4 {
		t.Fatalf("expected len 4, got %d", len(got))
	}
	if got[0] == nil || *got[0] != "aaaaaaaaaa" {
		t.Errorf("hop[0] aa: want aaaaaaaaaa, got %v", deref(got[0]))
	}
	if got[1] != nil {
		t.Errorf("hop[1] cc: want nil (ambiguous), got %v", deref(got[1]))
	}
	if got[2] != nil {
		t.Errorf("hop[2] ff: want nil (unknown), got %v", deref(got[2]))
	}
	if got[3] == nil || *got[3] != "bbbbbbbbbb" {
		t.Errorf("hop[3] bb: want bbbbbbbbbb, got %v", deref(got[3]))
	}
}

// TestResolvePathEmptyHops asserts empty/no-path produces nil.
func TestResolvePathEmptyHops(t *testing.T) {
	if got := resolvePath(nil, prefixIndex{}); got != nil {
		t.Errorf("nil hops: want nil, got %v", got)
	}
	if got := resolvePath([]string{}, prefixIndex{}); got != nil {
		t.Errorf("empty hops: want nil, got %v", got)
	}
}

// TestMarshalResolvedPathRoundtrip asserts the JSON shape matches the
// server's marshal/unmarshal contract: `[]*string` with nulls for
// unresolved hops.
func TestMarshalResolvedPathRoundtrip(t *testing.T) {
	a := "aaaaaaaaaa"
	b := "bbbbbbbbbb"
	in := []*string{&a, nil, &b}
	s := marshalResolvedPath(in)
	want := `["aaaaaaaaaa",null,"bbbbbbbbbb"]`
	if s != want {
		t.Errorf("marshal: want %s, got %s", want, s)
	}
}

// TestInsertTransmissionWritesResolvedPath is the integration test that
// gates the regression introduced by PR #1289 (issue #1547).
//
// Setup: seed two nodes + one observer + invoke InsertTransmission with
// a PacketData whose PathJSON references one of the seeded nodes by
// unique 1-byte (2-hex) prefix.
//
// Assert: the inserted observations row has a non-NULL resolved_path
// whose JSON-decoded length equals the hop count, and the resolved
// element matches the seeded node's full pubkey.
func TestInsertTransmissionWritesResolvedPath(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ingest.db")

	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	// Seed nodes with unique 1-byte prefixes.
	if _, err := store.db.Exec(
		`INSERT INTO nodes (public_key, name) VALUES (?, ?), (?, ?)`,
		"aaaaaaaaaa", "from-node",
		"bbbbbbbbbb", "first-hop",
	); err != nil {
		t.Fatal(err)
	}

	// Seed one observer (needed so InsertTransmission resolves observer_idx).
	if err := store.UpsertObserver("obs-1", "observer-1", "", nil); err != nil {
		t.Fatalf("UpsertObserver: %v", err)
	}

	// Force the prefix index to be (re)built from the seeded nodes so
	// the InsertTransmission path has something to resolve against.
	if err := store.RefreshPrefixIndex(); err != nil {
		t.Fatalf("RefreshPrefixIndex: %v", err)
	}

	pkt := &PacketData{
		RawHex:      "deadbeef",
		Timestamp:   "2026-06-01T00:00:00Z",
		ObserverID:  "obs-1",
		Hash:        "h-1547",
		RouteType:   0,
		PayloadType: int(payloadADVERT),
		PathJSON:    `["bb"]`,
		DecodedJSON: "{}",
		FromPubkey:  "aaaaaaaaaa",
	}
	if _, err := store.InsertTransmission(pkt); err != nil {
		t.Fatalf("InsertTransmission: %v", err)
	}

	var rp sql.NullString
	if err := store.db.QueryRow(
		`SELECT resolved_path FROM observations WHERE transmission_id = (SELECT id FROM transmissions WHERE hash = ?)`,
		"h-1547",
	).Scan(&rp); err != nil {
		t.Fatalf("query: %v", err)
	}
	if !rp.Valid || rp.String == "" {
		t.Fatalf("expected non-nil resolved_path, got NULL/empty (regression: #1547)")
	}
	got := unmarshalResolvedPathLocal(rp.String)
	if len(got) != 1 {
		t.Fatalf("resolved_path length: want 1, got %d (value=%s)", len(got), rp.String)
	}
	if got[0] == nil || *got[0] != "bbbbbbbbbb" {
		t.Errorf("resolved_path[0]: want bbbbbbbbbb, got %v (raw=%s)", deref(got[0]), rp.String)
	}
}

func deref(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// TestMarshalResolvedPathAllNilReturnsEmpty is a regression gate for
// the data-loss clobber bug surfaced in PR #1548 review.
//
// When resolvePath fails to resolve ANY hop (every element nil),
// marshalResolvedPath previously emitted "[null,null,...]" — a
// non-empty string that bypassed nilIfEmpty and then OVERWROTE the
// existing resolved_path via the COALESCE(excluded, current) UPSERT
// on re-ingest. The fix returns "" so nilIfEmpty produces SQL NULL and
// the COALESCE preserves the existing good value.
func TestMarshalResolvedPathAllNilReturnsEmpty(t *testing.T) {
	cases := []struct {
		name string
		in   []*string
	}{
		{"one-nil", []*string{nil}},
		{"two-nils", []*string{nil, nil}},
		{"three-nils", []*string{nil, nil, nil}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := marshalResolvedPath(tc.in)
			if got != "" {
				t.Errorf("all-nil input must return \"\" (so nilIfEmpty → SQL NULL → COALESCE preserves existing); got %q", got)
			}
		})
	}

	// Mixed (at least one non-nil) MUST still marshal normally so we
	// don't lose partial resolutions.
	a := "aaaaaaaaaa"
	mixed := marshalResolvedPath([]*string{&a, nil})
	if mixed != `["aaaaaaaaaa",null]` {
		t.Errorf("partial resolution must still serialize; got %q", mixed)
	}
}

// TestInsertTransmissionDoesNotClobberResolvedPathOnAllNil is the
// integration-level regression test for the data-loss bug.
//
// Setup: insert a transmission whose first ingest resolves cleanly to
// a known pubkey. Then re-ingest the SAME transmission after the
// prefix index has been cleared (simulating an empty NeighborGraph /
// all-nil resolution path) and assert the previously stored
// resolved_path is PRESERVED (NOT overwritten to "[null]" or NULL).
//
// Pre-fix behavior: marshalResolvedPath emitted "[null]", nilIfEmpty
// kept it non-NULL, and COALESCE(excluded.resolved_path, resolved_path)
// clobbered the original "bbbbbbbbbb".
func TestInsertTransmissionDoesNotClobberResolvedPathOnAllNil(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "ingest.db")

	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	if _, err := store.db.Exec(
		`INSERT INTO nodes (public_key, name) VALUES (?, ?), (?, ?)`,
		"aaaaaaaaaa", "from-node",
		"bbbbbbbbbb", "first-hop",
	); err != nil {
		t.Fatal(err)
	}
	if err := store.UpsertObserver("obs-1", "observer-1", "", nil); err != nil {
		t.Fatalf("UpsertObserver: %v", err)
	}
	if err := store.RefreshPrefixIndex(); err != nil {
		t.Fatalf("RefreshPrefixIndex: %v", err)
	}

	pkt := &PacketData{
		RawHex:      "deadbeef",
		Timestamp:   "2026-06-01T00:00:00Z",
		ObserverID:  "obs-1",
		Hash:        "h-clobber",
		RouteType:   0,
		PayloadType: int(payloadADVERT),
		PathJSON:    `["bb"]`,
		DecodedJSON: "{}",
		FromPubkey:  "aaaaaaaaaa",
	}
	if _, err := store.InsertTransmission(pkt); err != nil {
		t.Fatalf("first InsertTransmission: %v", err)
	}

	// Sanity: first write populated resolved_path.
	var first sql.NullString
	if err := store.db.QueryRow(
		`SELECT resolved_path FROM observations WHERE transmission_id = (SELECT id FROM transmissions WHERE hash = ?)`,
		"h-clobber",
	).Scan(&first); err != nil {
		t.Fatalf("first query: %v", err)
	}
	if !first.Valid || first.String == "" {
		t.Fatalf("precondition failed: first ingest left resolved_path NULL/empty; cannot test clobber")
	}
	wantPreserved := first.String

	// Now wipe the prefix index so re-ingest produces an all-nil
	// resolution — exactly the scenario where the bug clobbers data.
	store.prefixIdx.store(prefixIndex{})

	if _, err := store.InsertTransmission(pkt); err != nil {
		t.Fatalf("re-ingest InsertTransmission: %v", err)
	}

	var after sql.NullString
	if err := store.db.QueryRow(
		`SELECT resolved_path FROM observations WHERE transmission_id = (SELECT id FROM transmissions WHERE hash = ?)`,
		"h-clobber",
	).Scan(&after); err != nil {
		t.Fatalf("post-reingest query: %v", err)
	}
	if !after.Valid {
		t.Fatalf("data loss: resolved_path was NULL'd by re-ingest (was %q)", wantPreserved)
	}
	if after.String != wantPreserved {
		t.Errorf("data loss: resolved_path was clobbered by all-nil re-ingest\n  before: %s\n  after:  %s", wantPreserved, after.String)
	}
}
