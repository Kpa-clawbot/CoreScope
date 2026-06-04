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

// ─── #1560: context-aware resolution tests ─────────────────────────────────
//
// These exercise the post-fix behavior of resolveHopWithContext +
// resolvePathWithContext. Until the green commit lands they MUST fail
// on assertions (the stub falls back to naive `len==1` and returns nil
// on every >1-candidate prefix), proving the gate is real.

// build5NodeAmbiguousIndex returns a prefixIndex where 3 of 5 nodes
// share the 1-byte prefix 0x5c. Pubkeys are the "fingerprints":
//
//	A = "5c000000000000000000000000000000aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
//	B = "5c000000000000000000000000000000bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
//	C = "5c000000000000000000000000000000cccccccccccccccccccccccccccccccc"
//	D = "dd000000000000000000000000000000dddddddddddddddddddddddddddddddd"
//	E = "ee000000000000000000000000000000eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
func build5NodeAmbiguousIndex() (idx prefixIndex, A, B, C, D, E string) {
	A = "5c000000000000000000000000000000aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	B = "5c000000000000000000000000000000bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	C = "5c000000000000000000000000000000cccccccccccccccccccccccccccccccc"
	D = "dd000000000000000000000000000000dddddddddddddddddddddddddddddddd"
	E = "ee000000000000000000000000000000eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	idx = prefixIndex{
		// 1-byte: 5c → A,B,C (collision); dd → D; ee → E
		"5c": {A, B, C},
		"dd": {D},
		"ee": {E},
		// full-key entries (so exact-match lookups still resolve)
		A: {A}, B: {B}, C: {C}, D: {D}, E: {E},
	}
	return
}

// TestResolveHopWithContext_OneByteCollision_AdjacencyResolves
// asserts the dominant production case (#1560): three nodes share the
// 1-byte prefix 0x5c, but NeighborGraph adjacency narrows to exactly
// one. The naive resolver returns nil; the context-aware resolver
// MUST return the right pubkey.
func TestResolveHopWithContext_OneByteCollision_AdjacencyResolves(t *testing.T) {
	idx, A, B, C, D, E := build5NodeAmbiguousIndex()
	g := NewNeighborGraph()
	// chain: A↔B, B↔C, C↔D, D↔E
	g.AddEdge(A, B)
	g.AddEdge(B, C)
	g.AddEdge(C, D)
	g.AddEdge(D, E)

	// Anchored on A, the only 5c neighbor of A is B.
	got := resolveHopWithContext("5c", A, g, idx, nil)
	if got == nil {
		t.Fatalf("anchor=A, hop=5c: want B (%s), got <nil>", B)
	}
	if *got != B {
		t.Errorf("anchor=A, hop=5c: want %s, got %s", B, *got)
	}

	// Anchored on B, the only 5c neighbors of B are A and C — but A is
	// the originator anchor in a path-walk; here we just assert that
	// 2 surviving candidates → nil (cannot disambiguate further).
	got = resolveHopWithContext("5c", B, g, idx, nil)
	if got != nil {
		t.Errorf("anchor=B, hop=5c: ambiguous (A and C both adjacent); want <nil>, got %s", *got)
	}
}

// TestResolvePathWithContext_TwoHopChainAnchoredOnFromNode covers the
// canonical 1-byte collision case end-to-end: path = [5c, 5c],
// from_node = A → expect [B, C].
func TestResolvePathWithContext_TwoHopChainAnchoredOnFromNode(t *testing.T) {
	idx, A, B, C, _, _ := build5NodeAmbiguousIndex()
	g := NewNeighborGraph()
	g.AddEdge(A, B)
	g.AddEdge(B, C)

	got := resolvePathWithContext([]string{"5c", "5c"}, A, g, idx)
	if len(got) != 2 {
		t.Fatalf("len(got)=%d, want 2 (raw=%v)", len(got), got)
	}
	if got[0] == nil || *got[0] != B {
		t.Errorf("hop[0]: want %s, got %v", B, deref(got[0]))
	}
	if got[1] == nil || *got[1] != C {
		t.Errorf("hop[1]: want %s, got %v", C, deref(got[1]))
	}
}

// TestResolveHopWithContext_NoAdjacencyContext_ReturnsNil asserts the
// negative gate: 3 nodes with shared prefix, no edges between them in
// the graph, hop=[5c] with no usable anchor → nil. Guards against an
// over-eager resolver that just picks the first candidate.
func TestResolveHopWithContext_NoAdjacencyContext_ReturnsNil(t *testing.T) {
	idx, _, _, _, _, _ := build5NodeAmbiguousIndex()
	g := NewNeighborGraph() // empty: no edges
	got := resolveHopWithContext("5c", "", g, idx, nil)
	if got != nil {
		t.Errorf("no anchor + empty graph: want <nil>, got %s", *got)
	}

	// With an anchor that's not adjacent to any candidate, also nil.
	got = resolveHopWithContext("5c", "deadbeefdeadbeef", g, idx, nil)
	if got != nil {
		t.Errorf("non-adjacent anchor: want <nil>, got %s", *got)
	}
}

// TestResolvePathWithContext_AdvertAnchoring asserts ADVERT-style
// anchoring: from_pubkey is the originator, hop[0] is one of its
// 1-byte-prefix neighbors → resolved.
func TestResolvePathWithContext_AdvertAnchoring(t *testing.T) {
	idx, A, B, _, _, _ := build5NodeAmbiguousIndex()
	g := NewNeighborGraph()
	g.AddEdge(A, B) // only B is adjacent to A among the 5c candidates

	got := resolvePathWithContext([]string{"5c"}, A, g, idx)
	if len(got) != 1 {
		t.Fatalf("len(got)=%d, want 1", len(got))
	}
	if got[0] == nil || *got[0] != B {
		t.Errorf("ADVERT anchored on A, hop=5c: want %s, got %v", B, deref(got[0]))
	}
}

// TestResolvePathWithContext_RegressionMultiByteStillWorks asserts no
// regression in the 2/3/4-byte prefix path that PR #1548 already
// handled — unique prefixes resolve regardless of graph context.
func TestResolvePathWithContext_RegressionMultiByteStillWorks(t *testing.T) {
	idx, _, _, _, D, E := build5NodeAmbiguousIndex()
	// dd and ee are unique 1-byte prefixes — naive path still works.
	got := resolvePathWithContext([]string{"dd", "ee"}, "", nil, idx)
	if len(got) != 2 {
		t.Fatalf("len(got)=%d, want 2", len(got))
	}
	if got[0] == nil || *got[0] != D {
		t.Errorf("hop[0] dd: want %s, got %v", D, deref(got[0]))
	}
	if got[1] == nil || *got[1] != E {
		t.Errorf("hop[1] ee: want %s, got %v", E, deref(got[1]))
	}
}

// TestResolvePathWithContext_AllNilContractPreserved asserts the
// all-nil → empty-string clobber-guard contract from PR #1548 still
// holds: an unresolvable path through the context resolver, when fed
// to marshalResolvedPath, MUST yield "" (so nilIfEmpty → SQL NULL
// → COALESCE preserves existing).
func TestResolvePathWithContext_AllNilContractPreserved(t *testing.T) {
	// Empty index → every hop nil.
	got := resolvePathWithContext([]string{"5c", "dd"}, "", nil, prefixIndex{})
	if len(got) != 2 {
		t.Fatalf("len(got)=%d, want 2", len(got))
	}
	for i, p := range got {
		if p != nil {
			t.Errorf("hop[%d]: want <nil>, got %s", i, *p)
		}
	}
	if s := marshalResolvedPath(got); s != "" {
		t.Errorf("all-nil marshal: want \"\", got %q (clobber-guard regression)", s)
	}
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
