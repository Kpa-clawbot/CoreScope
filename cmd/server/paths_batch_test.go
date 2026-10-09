package main

import (
	"database/sql"
	"reflect"
	"testing"
)

// #2146: /api/nodes/{pk}/paths looked up each candidate's canonical resolved
// path with bestResolvedPath, one or two queries per transmission. The batched
// loader must return the same path for every transmission.

func newPathsBatchStore(t *testing.T) (*PacketStore, map[int][]rpObs) {
	t.Helper()
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	t.Cleanup(func() { conn.Close() })
	if _, err := conn.Exec(`CREATE TABLE observations (
		id INTEGER PRIMARY KEY, transmission_id INTEGER, path_json TEXT, resolved_path TEXT)`); err != nil {
		t.Fatal(err)
	}
	rows := []struct {
		id, tx   int
		path, rp interface{}
	}{
		// tx 1 (#810): the longest path has no stored resolved_path.
		{1, 1, `["a","b","c"]`, nil},
		{2, 1, `["a","b"]`, `["aa11aa11","bb22bb22"]`},
		// tx 2: equal lengths, the first in snapshot order wins.
		{3, 2, `["a","b"]`, `["cc33cc33","dd44dd44"]`},
		{4, 2, `["a","c"]`, `["aa11aa11","ee55ee55"]`},
		// tx 3: nothing stored.
		{5, 3, `["a"]`, nil},
		// tx 4: an empty stored path counts as none.
		{6, 4, `["a"]`, ``},
		// tx 5: the match is case-insensitive.
		{7, 5, `["a","b"]`, `["AA11AA11","ff66ff66"]`},
		// tx 6: observation 9 is in the database but not in the store's
		// snapshot; it still counts for the mention, not for the best path.
		{8, 6, `["a","b"]`, `["ff66ff66","bb22bb22"]`},
		{9, 6, `["a","b","c"]`, `["aa11aa11","x","y"]`},
		// tx 7: the longest path wins over an earlier shorter one.
		{10, 7, `["a"]`, `["bb22bb22"]`},
		{11, 7, `["a","b","c"]`, `["cc33cc33","aa11aa11","dd44dd44"]`},
	}
	for _, r := range rows {
		if _, err := conn.Exec(`INSERT INTO observations (id, transmission_id, path_json, resolved_path) VALUES (?,?,?,?)`,
			r.id, r.tx, r.path, r.rp); err != nil {
			t.Fatal(err)
		}
	}
	snapshots := map[int][]rpObs{
		1: {{1, `["a","b","c"]`}, {2, `["a","b"]`}},
		2: {{3, `["a","b"]`}, {4, `["a","c"]`}},
		3: {{5, `["a"]`}},
		4: {{6, `["a"]`}},
		5: {{7, `["a","b"]`}},
		6: {{8, `["a","b"]`}},
		7: {{10, `["a"]`}, {11, `["a","b","c"]`}},
	}
	return &PacketStore{db: &DB{conn: conn}}, snapshots
}

func TestLoadCanonicalResolvedPathsMatchesBestResolvedPath(t *testing.T) {
	s, snapshots := newPathsBatchStore(t)
	got := s.loadCanonicalResolvedPaths(snapshots)
	for txID, snap := range snapshots {
		want := s.bestResolvedPath(txID, snap)
		if !reflect.DeepEqual(got[txID], want) {
			t.Errorf("tx %d: batched %v, bestResolvedPath %v", txID, derefAll(got[txID]), derefAll(want))
		}
		if _, present := got[txID]; present != (want != nil) {
			t.Errorf("tx %d: present in result = %v, want %v", txID, present, want != nil)
		}
	}
}

func derefAll(rp []*string) []string {
	out := make([]string, len(rp))
	for i, p := range rp {
		if p != nil {
			out[i] = *p
		}
	}
	return out
}

func TestLoadCanonicalResolvedPathsBatchesQueries(t *testing.T) {
	conn, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	conn.SetMaxOpenConns(1)
	defer conn.Close()
	if _, err := conn.Exec(`CREATE TABLE observations (
		id INTEGER PRIMARY KEY, transmission_id INTEGER, path_json TEXT, resolved_path TEXT)`); err != nil {
		t.Fatal(err)
	}
	const n = 2*resolvedPathBatchSize + 1
	snapshots := make(map[int][]rpObs, n)
	for i := 1; i <= n; i++ {
		conn.Exec(`INSERT INTO observations (id, transmission_id, path_json, resolved_path) VALUES (?,?,?,?)`,
			i, i, `["a"]`, `["aa11aa11"]`)
		snapshots[i] = []rpObs{{i, `["a"]`}}
	}
	s := &PacketStore{db: &DB{conn: conn}}
	batched, single := 0, 0
	s.cacheLoadHook = func(kind string) {
		switch kind {
		case "resolvedPathBatch":
			batched++
		case "resolvedPath":
			single++
		}
	}
	got := s.loadCanonicalResolvedPaths(snapshots)
	if batched != 3 || single != 0 {
		t.Errorf("want 3 batched and 0 single queries for %d transmissions, got %d and %d", n, batched, single)
	}
	if len(got) != n {
		t.Errorf("want %d canonical paths, got %d", n, len(got))
	}
}

// #2146: an index hit with a canonical path no longer gets the per-
// transmission SQL collision check; the aggregation decides from the
// canonical path, which is what it did after that check anyway. Here the
// longest observation (the canonical one) names A and a shorter one names B:
// both A and B are index hits, only A is on the canonical path.
func TestHandleNodePaths_CanonicalPathDecidesIndexHits(t *testing.T) {
	sc := setupCollisionScenario(t, false)
	mustExec(t, sc.db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen) VALUES (43, 'BEEF', 'hash_2146', ?)`, sc.recent)
	mustExec(t, sc.db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
		VALUES (43, NULL, '["c0","aa"]', ?, ?)`, sc.recentEpoch, `["`+sc.nodeAPK+`","aaaa0000aaaa0000"]`)
	mustExec(t, sc.db, `INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp, resolved_path)
		VALUES (43, NULL, '["c0"]', ?, ?)`, sc.recentEpoch-10, `["`+sc.nodeBPK+`"]`)
	sc.reloadStore(t)

	queries := 0
	sc.srv.store.cacheLoadHook = func(kind string) {
		if kind == "resolvedPath" {
			queries++
		}
	}
	if got := sc.query(t, sc.nodeAPK).TotalTransmissions; got != 1 {
		t.Errorf("A is on the canonical path: want 1 transmission, got %d", got)
	}
	if got := sc.query(t, sc.nodeBPK).TotalTransmissions; got != 0 {
		t.Errorf("B is only on a non-canonical observation: want 0 transmissions, got %d", got)
	}
	if queries != 0 {
		t.Errorf("want no per-transmission resolved_path queries, got %d", queries)
	}
}
