package main

import (
	"database/sql"
	"reflect"
	"testing"
)

// #2146: /api/nodes/{pk}/paths read the stored resolved paths with one INSTR
// query per index hit plus one or two lookups per survivor. The batched
// summary must give the same answers as those per-transmission functions.

const pathsBatchTarget = "aa11aa11"

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

func TestSummarizeStoredResolvedPathsMatchesPerTransmission(t *testing.T) {
	s, snapshots := newPathsBatchStore(t)
	got, err := s.summarizeStoredResolvedPaths(snapshots, pathsBatchTarget)
	if err != nil {
		t.Fatal(err)
	}
	for txID, snap := range snapshots {
		sum := got[txID]
		wantMention := s.confirmResolvedPathContains(txID, pathsBatchTarget)
		if sum.mentions != wantMention {
			t.Errorf("tx %d: mentions = %v, confirmResolvedPathContains = %v", txID, sum.mentions, wantMention)
		}
		want := s.bestResolvedPath(txID, snap)
		var gotRP []*string
		if sum.found {
			gotRP = unmarshalResolvedPath(sum.bestRaw)
		}
		if !reflect.DeepEqual(gotRP, want) {
			t.Errorf("tx %d: best path %v, bestResolvedPath %v", txID, derefAll(gotRP), derefAll(want))
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

func TestSummarizeStoredResolvedPathsBatchesQueries(t *testing.T) {
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
	queries := 0
	s.cacheLoadHook = func(kind string) {
		if kind == "resolvedPathBatch" {
			queries++
		}
	}
	got, err := s.summarizeStoredResolvedPaths(snapshots, pathsBatchTarget)
	if err != nil {
		t.Fatal(err)
	}
	if queries != 3 {
		t.Errorf("want 3 batched queries for %d transmissions, got %d", n, queries)
	}
	if len(got) != n || !got[n].mentions || !got[n].found {
		t.Errorf("want every transmission summarised, got %d", len(got))
	}
}
