package main

import (
	"testing"
)

// --- pathLen edge cases (hand-rolled comma scanner vs json.Unmarshal) ---

func TestPathLen_EdgeCases(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"empty string", "", 0},
		{"empty array", "[]", 0},
		{"empty array whitespace", "  [ ]  ", 0},
		{"single string", `["aa"]`, 1},
		{"two strings", `["aa","bb"]`, 2},
		{"whitespace around commas", `[ "aa" , "bb" , "cc" ]`, 3},
		{"nested arrays", `[["a"],["b"]]`, 2},
		{"nested array with inner commas", `[["a","x"],["b","y","z"]]`, 2},
		{"objects", `[{"x":1}]`, 1},
		{"objects with inner commas", `[{"x":1,"y":2},{"z":3}]`, 2},
		{"escaped quote in string", `["a\"b","c"]`, 2},
		{"comma inside string literal", `["a,b","c"]`, 2},
		{"bracket inside string literal", `["a]b","c"]`, 2},
		{"leading whitespace", "  [\"aa\",\"bb\"]", 2},
		{"malformed unbalanced", `["aa","bb"`, 0},
		{"malformed garbage", `not json at all`, 0},
		{"malformed open string", `["aa]`, 0},
		{"not an array (object)", `{"x":1}`, 0},
		{"number array", `[1,2,3]`, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := pathLen(c.in)
			if got != c.want {
				t.Errorf("pathLen(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

// TestPathLen_TrailingComma documents the trailing-comma behavior. A trailing
// comma is invalid JSON, so json.Unmarshal would reject it. pathLen's
// fast-path scanner counts elements by top-level commas — `["a",]` is a plain
// array literal with a balanced depth, so it counts 2. The strict parser is
// only consulted for non-array-literal or unbalanced input, so this input
// never reaches it. This test pins the observed behavior so a future change
// is a deliberate choice, not a silent regression.
func TestPathLen_TrailingComma(t *testing.T) {
	got := pathLen(`["a","b",]`)
	if got != 3 {
		t.Errorf("pathLen trailing-comma: got %d, want 3 (fast-path comma count)", got)
	}
}

// TestPathLen_MatchesParsePathJSON cross-checks pathLen against the
// json.Unmarshal-based parsePathJSON for inputs both can handle (flat string
// arrays). They must agree.
func TestPathLen_MatchesParsePathJSON(t *testing.T) {
	inputs := []string{
		"",
		"[]",
		`["aa"]`,
		`["aa","bb","cc"]`,
		`[ "aa" , "bb" ]`,
		`["a,b","c"]`,
		`["a\"b"]`,
	}
	for _, in := range inputs {
		want := len(parsePathJSON(in))
		got := pathLen(in)
		if got != want {
			t.Errorf("pathLen(%q)=%d but len(parsePathJSON)=%d", in, got, want)
		}
	}
}

// --- prefetchResolvedPathsForTxs ---

func TestPrefetchResolvedPathsForTxs_EmptyPage(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}

	got := store.prefetchResolvedPathsForTxs(nil)
	if got == nil {
		t.Fatal("expected non-nil empty map for nil tx IDs")
	}
	if len(got) != 0 {
		t.Errorf("expected empty result for nil tx IDs, got %d", len(got))
	}

	got = store.prefetchResolvedPathsForTxs([]int{})
	if len(got) != 0 {
		t.Errorf("expected empty result for empty tx IDs, got %d", len(got))
	}
}

func TestPrefetchResolvedPathsForTxs_NormalPage(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}

	// seedTestData inserts: tx1 (obs 1 & 2 have resolved_path), tx2 (obs has
	// none), tx3 (obs has resolved_path).
	rpByTx := store.prefetchResolvedPathsForTxs([]int{1, 2, 3})

	tx1 := rpByTx[1]
	if len(tx1) != 2 {
		t.Fatalf("tx1: expected 2 observations with resolved_path, got %d", len(tx1))
	}
	// tx2's only observation has NULL resolved_path → no entry.
	if _, ok := rpByTx[2]; ok {
		t.Errorf("tx2 should have no resolved-path entries")
	}
	if len(rpByTx[3]) != 1 {
		t.Errorf("tx3: expected 1 observation with resolved_path, got %d", len(rpByTx[3]))
	}

	// Verify a concrete resolved path value. Find tx1's longest-path obs.
	var tx1Obs *StoreTx
	store.mu.RLock()
	tx1Obs = store.byTxID[1]
	store.mu.RUnlock()
	if tx1Obs == nil {
		t.Fatal("tx1 not in store")
	}
	found := false
	for _, obs := range tx1Obs.Observations {
		rp, ok := tx1[obs.ID]
		if !ok {
			continue
		}
		if len(rp) == 2 && rp[0] != nil && *rp[0] == "aabbccdd11223344" {
			found = true
		}
	}
	if !found {
		t.Error("expected to find resolved path [aabbccdd11223344, eeff00112233aabb] for a tx1 obs")
	}
}

func TestPrefetchResolvedPathsForTxs_WarmsLRU(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}

	rpByTx := store.prefetchResolvedPathsForTxs([]int{1, 3})

	// Every obsID returned by the prefetch must now be present in the LRU.
	store.lruMu.RLock()
	defer store.lruMu.RUnlock()
	if store.apiResolvedPathLRU == nil {
		t.Skip("LRU not enabled in this store config")
	}
	for txID, byObs := range rpByTx {
		for obsID := range byObs {
			if _, ok := store.apiResolvedPathLRU[obsID]; !ok {
				t.Errorf("obs %d (tx %d) not warmed into LRU after prefetch", obsID, txID)
			}
		}
	}
}

// TestQueryPackets_ConsistentWithWarmCache verifies the 3-phase split does not
// change output: results must match whether or not the resolved-path cache is
// pre-warmed before the query.
func TestQueryPackets_ConsistentWithWarmCache(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	q := PacketQuery{Limit: 50, Order: "DESC"}

	// Cold store: cache not pre-warmed.
	coldStore := NewPacketStore(db, nil)
	if err := coldStore.Load(); err != nil {
		t.Fatal(err)
	}
	coldResult := coldStore.QueryPackets(q)

	// Warm store: pre-warm the resolved-path cache before querying.
	warmStore := NewPacketStore(db, nil)
	if err := warmStore.Load(); err != nil {
		t.Fatal(err)
	}
	warmStore.mu.RLock()
	allTxIDs := make([]int, 0, len(warmStore.packets))
	for _, tx := range warmStore.packets {
		allTxIDs = append(allTxIDs, tx.ID)
	}
	warmStore.mu.RUnlock()
	_ = warmStore.prefetchResolvedPathsForTxs(allTxIDs) // pre-warm
	warmResult := warmStore.QueryPackets(q)

	if coldResult.Total != warmResult.Total {
		t.Fatalf("total mismatch: cold=%d warm=%d", coldResult.Total, warmResult.Total)
	}
	if len(coldResult.Packets) != len(warmResult.Packets) {
		t.Fatalf("packet count mismatch: cold=%d warm=%d",
			len(coldResult.Packets), len(warmResult.Packets))
	}
	for i := range coldResult.Packets {
		c, w := coldResult.Packets[i], warmResult.Packets[i]
		if c["hash"] != w["hash"] {
			t.Errorf("packet %d hash mismatch: cold=%v warm=%v", i, c["hash"], w["hash"])
		}
		cRP, cOK := c["resolved_path"]
		wRP, wOK := w["resolved_path"]
		if cOK != wOK {
			t.Errorf("packet %d resolved_path presence mismatch: cold=%v warm=%v", i, cOK, wOK)
		}
		// Compare resolved_path content shallowly.
		cArr, _ := cRP.([]interface{})
		wArr, _ := wRP.([]interface{})
		if len(cArr) != len(wArr) {
			t.Errorf("packet %d resolved_path length mismatch: cold=%d warm=%d",
				i, len(cArr), len(wArr))
		}
	}
}

// --- dist-index eviction (appendDistRecords / removeDistRecordsForTxs) ---

// checkDistIndexConsistent asserts every position in distHopsByTx/distPathsByTx
// points at a record whose tx.ID matches the map key, and that every record in
// the flat slices is referenced exactly once by the index.
func checkDistIndexConsistent(t *testing.T, s *PacketStore) {
	t.Helper()
	// distHops
	hopSeen := make(map[int]bool)
	for txID, positions := range s.distHopsByTx {
		for _, pos := range positions {
			if pos < 0 || pos >= len(s.distHops) {
				t.Fatalf("distHopsByTx[%d] has out-of-range position %d (len=%d)",
					txID, pos, len(s.distHops))
			}
			rec := s.distHops[pos]
			if rec.tx == nil || rec.tx.ID != txID {
				gotID := -1
				if rec.tx != nil {
					gotID = rec.tx.ID
				}
				t.Fatalf("distHops[%d] belongs to tx %d but indexed under tx %d",
					pos, gotID, txID)
			}
			if hopSeen[pos] {
				t.Fatalf("distHops position %d referenced twice in index", pos)
			}
			hopSeen[pos] = true
		}
	}
	if len(hopSeen) != len(s.distHops) {
		t.Fatalf("distHopsByTx covers %d positions but distHops has %d records",
			len(hopSeen), len(s.distHops))
	}
	// distPaths
	pathSeen := make(map[int]bool)
	for txID, positions := range s.distPathsByTx {
		for _, pos := range positions {
			if pos < 0 || pos >= len(s.distPaths) {
				t.Fatalf("distPathsByTx[%d] has out-of-range position %d (len=%d)",
					txID, pos, len(s.distPaths))
			}
			rec := s.distPaths[pos]
			if rec.tx == nil || rec.tx.ID != txID {
				gotID := -1
				if rec.tx != nil {
					gotID = rec.tx.ID
				}
				t.Fatalf("distPaths[%d] belongs to tx %d but indexed under tx %d",
					pos, gotID, txID)
			}
			if pathSeen[pos] {
				t.Fatalf("distPaths position %d referenced twice in index", pos)
			}
			pathSeen[pos] = true
		}
	}
	if len(pathSeen) != len(s.distPaths) {
		t.Fatalf("distPathsByTx covers %d positions but distPaths has %d records",
			len(pathSeen), len(s.distPaths))
	}
}

func makeDistHops(tx *StoreTx, n int) []distHopRecord {
	hops := make([]distHopRecord, n)
	for i := range hops {
		hops[i] = distHopRecord{Hash: tx.Hash, tx: tx, Dist: float64(i + 1)}
	}
	return hops
}

func TestDistIndex_AppendAndRemoveSubset(t *testing.T) {
	s := &PacketStore{}

	txA := &StoreTx{ID: 10, Hash: "hashA"}
	txB := &StoreTx{ID: 20, Hash: "hashB"}
	txC := &StoreTx{ID: 30, Hash: "hashC"}

	s.appendDistRecords(txA.ID, makeDistHops(txA, 2), &distPathRecord{Hash: txA.Hash, tx: txA})
	s.appendDistRecords(txB.ID, makeDistHops(txB, 3), &distPathRecord{Hash: txB.Hash, tx: txB})
	s.appendDistRecords(txC.ID, makeDistHops(txC, 1), &distPathRecord{Hash: txC.Hash, tx: txC})

	if len(s.distHops) != 6 {
		t.Fatalf("expected 6 hop records, got %d", len(s.distHops))
	}
	if len(s.distPaths) != 3 {
		t.Fatalf("expected 3 path records, got %d", len(s.distPaths))
	}
	checkDistIndexConsistent(t, s)

	// Evict the middle tx (B). A and C must survive intact, no stale indexes.
	s.removeDistRecordsForTxs(map[int]bool{txB.ID: true})

	if len(s.distHops) != 3 {
		t.Fatalf("after removing txB expected 3 hop records, got %d", len(s.distHops))
	}
	if len(s.distPaths) != 2 {
		t.Fatalf("after removing txB expected 2 path records, got %d", len(s.distPaths))
	}
	if _, ok := s.distHopsByTx[txB.ID]; ok {
		t.Error("txB still present in distHopsByTx after eviction")
	}
	if _, ok := s.distPathsByTx[txB.ID]; ok {
		t.Error("txB still present in distPathsByTx after eviction")
	}
	checkDistIndexConsistent(t, s)

	// Survivors keep all their records.
	if got := len(s.distHopsByTx[txA.ID]); got != 2 {
		t.Errorf("txA should keep 2 hop records, got %d", got)
	}
	if got := len(s.distHopsByTx[txC.ID]); got != 1 {
		t.Errorf("txC should keep 1 hop record, got %d", got)
	}

	// No surviving hop record belongs to the evicted tx.
	for _, rec := range s.distHops {
		if rec.tx != nil && rec.tx.ID == txB.ID {
			t.Error("found a surviving distHop record belonging to evicted txB")
		}
	}
	for _, rec := range s.distPaths {
		if rec.tx != nil && rec.tx.ID == txB.ID {
			t.Error("found a surviving distPath record belonging to evicted txB")
		}
	}
}

func TestDistIndex_RemoveAll(t *testing.T) {
	s := &PacketStore{}
	txA := &StoreTx{ID: 1, Hash: "a"}
	txB := &StoreTx{ID: 2, Hash: "b"}
	s.appendDistRecords(txA.ID, makeDistHops(txA, 2), &distPathRecord{tx: txA})
	s.appendDistRecords(txB.ID, makeDistHops(txB, 2), &distPathRecord{tx: txB})

	s.removeDistRecordsForTxs(map[int]bool{txA.ID: true, txB.ID: true})

	if len(s.distHops) != 0 || len(s.distPaths) != 0 {
		t.Fatalf("expected all records removed, got hops=%d paths=%d",
			len(s.distHops), len(s.distPaths))
	}
	checkDistIndexConsistent(t, s)
}

func TestDistIndex_RemoveEmptySetIsNoop(t *testing.T) {
	s := &PacketStore{}
	txA := &StoreTx{ID: 1, Hash: "a"}
	s.appendDistRecords(txA.ID, makeDistHops(txA, 2), &distPathRecord{tx: txA})

	s.removeDistRecordsForTxs(map[int]bool{})
	s.removeDistRecordsForTxs(nil)

	if len(s.distHops) != 2 || len(s.distPaths) != 1 {
		t.Fatalf("empty/nil removal must be a no-op, got hops=%d paths=%d",
			len(s.distHops), len(s.distPaths))
	}
	checkDistIndexConsistent(t, s)
}

// TestDistIndex_RemoveLastTxNoSwap exercises the path where the evicted records
// are at the tail of the slice (pos == last → no swap needed).
func TestDistIndex_RemoveLastTxNoSwap(t *testing.T) {
	s := &PacketStore{}
	txA := &StoreTx{ID: 1, Hash: "a"}
	txB := &StoreTx{ID: 2, Hash: "b"}
	s.appendDistRecords(txA.ID, makeDistHops(txA, 2), &distPathRecord{tx: txA})
	s.appendDistRecords(txB.ID, makeDistHops(txB, 2), &distPathRecord{tx: txB})

	// txB's records are at the tail — removal hits the pos==last branch.
	s.removeDistRecordsForTxs(map[int]bool{txB.ID: true})

	if len(s.distHops) != 2 {
		t.Fatalf("expected 2 surviving hops, got %d", len(s.distHops))
	}
	checkDistIndexConsistent(t, s)
	if got := len(s.distHopsByTx[txA.ID]); got != 2 {
		t.Errorf("txA should keep 2 hops, got %d", got)
	}
}

// TestQueryPackets_LimitOffsetClamp verifies an absurd limit is capped at
// maxQueryLimit and a negative offset is clamped to 0 (rather than panicking
// when the page is sliced). Covers both QueryPackets and QueryGroupedPackets.
func TestQueryPackets_LimitOffsetClamp(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	seedTestData(t, db)

	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}

	// Absurd limit must not produce a page larger than the cap.
	huge := store.QueryPackets(PacketQuery{Limit: 10_000_000, Order: "DESC"})
	if len(huge.Packets) > maxQueryLimit {
		t.Errorf("QueryPackets: page %d exceeds cap %d", len(huge.Packets), maxQueryLimit)
	}

	// Negative offset must be treated as 0, not panic when slicing the page.
	neg := store.QueryPackets(PacketQuery{Limit: 50, Offset: -5, Order: "DESC"})
	base := store.QueryPackets(PacketQuery{Limit: 50, Offset: 0, Order: "DESC"})
	if len(neg.Packets) != len(base.Packets) {
		t.Errorf("negative offset not clamped: got %d packets, offset 0 gives %d",
			len(neg.Packets), len(base.Packets))
	}

	// Same clamps on the grouped path.
	gHuge := store.QueryGroupedPackets(PacketQuery{Limit: 10_000_000, Order: "DESC"})
	if len(gHuge.Packets) > maxQueryLimit {
		t.Errorf("QueryGroupedPackets: page %d exceeds cap %d", len(gHuge.Packets), maxQueryLimit)
	}
	store.QueryGroupedPackets(PacketQuery{Limit: 50, Offset: -5, Order: "DESC"}) // must not panic
}
