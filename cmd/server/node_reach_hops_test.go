package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"testing"

	"github.com/meshcore-analyzer/dbschema"
)

// canon makes pathRow sets comparable regardless of row order.
func canon(rows []pathRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, fmt.Sprintf("%s|%s|%d|%v|%v|%.1f", r.observerPK, r.fromPubkey, r.payloadType, r.path, r.snrValid, r.snr))
	}
	sort.Strings(out)
	return out
}

func newHopIndexedTestDB(t *testing.T) *DB {
	t.Helper()
	db := newReachScanTestDB(t)
	db.conn.SetMaxOpenConns(1)
	// Extra rows: mixed hop widths, a path hitting two tokens, one row outside
	// the window, and one lower-case hop (LIKE ignores ASCII case, so the index
	// must too). Inserted BEFORE the schema so the backfill path is what
	// indexes them, as on an upgraded DB.
	for _, s := range []string{
		`INSERT INTO transmissions (id, from_pubkey, payload_type) VALUES (4,'',5),(5,'',5),(6,'',5),(7,'',5)`,
		`INSERT INTO observations (id, transmission_id, observer_idx, snr, path_json, timestamp) VALUES
			(4,4,1,1.0,'["01FA","01FAB2"]',2000),
			(5,5,1,2.0,'["01FAB2","DD"]',2000),
			(6,6,1,3.0,'["01FA"]',10),
			(7,7,1,4.0,'["01fab2"]',2000)`,
	} {
		if _, err := db.conn.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// The indexed scan must return exactly what the LIKE scan returns, for upper-
// and lower-case tokens alike.
func TestScanReachRows_IndexedMatchesLike(t *testing.T) {
	for _, toks := range []map[string]bool{
		{"01FA": true, "01FAB2": true},
		{"01fa": true, "01fab2": true},
	} {
		t.Run(fmt.Sprint(toks), func(t *testing.T) { indexedMatchesLike(t, toks) })
	}
}

func indexedMatchesLike(t *testing.T, toks map[string]bool) {
	db := newHopIndexedTestDB(t)
	defer db.conn.Close()
	srv := &Server{db: db}

	if srv.reachHopIndexCovers(context.Background(), 500) {
		t.Fatal("no hop schema (index off) must not count as coverage")
	}
	like, err := srv.scanReachRows(context.Background(), toks, 500) // no hop schema → LIKE
	if err != nil {
		t.Fatal(err)
	}

	if err := dbschema.EnsureObservationHopsSchema(db.conn, nil); err != nil {
		t.Fatal(err)
	}
	// The backfill's chunk function over all rows, then claim full coverage.
	if _, err := dbschema.IndexObservationHops(db.conn, 0, math.MaxInt64); err != nil {
		t.Fatal(err)
	}
	if srv.reachHopIndexCovers(context.Background(), 500) {
		// seeded coverage is "now": window starting at 500 is NOT covered yet.
		t.Fatal("coverage seeded at now must not cover an old window before the backfill")
	}
	db.conn.Exec(`UPDATE observation_hops_coverage SET since = 0`)
	if !srv.reachHopIndexCovers(context.Background(), 500) {
		t.Fatal("coverage since=0 must cover the window")
	}

	idx, err := srv.scanReachRows(context.Background(), toks, 500)
	if err != nil {
		t.Fatal(err)
	}
	a, b := canon(like), canon(idx)
	if len(a) != 5 { // obs 1,2,4,5,7 — not 3 (no token) and not 6 (before window)
		t.Fatalf("LIKE scan returned %d rows, want 5: %v", len(a), a)
	}
	if fmt.Sprint(a) != fmt.Sprint(b) {
		t.Fatalf("indexed scan differs from LIKE scan:\nlike    %v\nindexed %v", a, b)
	}
}

// Review #2214 round 2: the coverage check and the indexed scan are separate
// queries, and the ingestor removes the index (hopIndexDays set to 0) in one
// transaction between them for a request that is in flight. That request must
// still get its answer, from the substring scan; and a failure in both paths
// still surfaces as an error (#1631), not as an empty reach.
func TestScanReachRows_IndexRemovedMidRequestFallsBackToLike(t *testing.T) {
	db := newHopIndexedTestDB(t)
	defer db.conn.Close()
	srv := &Server{db: db}
	toks := map[string]bool{"01FA": true, "01FAB2": true}
	like, err := srv.scanReachRows(context.Background(), toks, 500)
	if err != nil {
		t.Fatal(err)
	}
	if err := dbschema.EnsureObservationHopsSchema(db.conn, nil); err != nil {
		t.Fatal(err)
	}
	db.conn.Exec(`UPDATE observation_hops_coverage SET since = 0`)
	// The state the in-flight request sees: the coverage row its check read,
	// and no table left to scan.
	if _, err := db.conn.Exec(`DROP TABLE observation_hops`); err != nil {
		t.Fatal(err)
	}
	if !srv.reachHopIndexCovers(context.Background(), 500) {
		t.Fatal("setup: the coverage check must still pass")
	}
	rows, err := srv.scanReachRows(context.Background(), toks, 500)
	if err != nil {
		t.Fatalf("scan with the hop table gone: %v, want the LIKE scan's answer", err)
	}
	if a, b := canon(like), canon(rows); fmt.Sprint(a) != fmt.Sprint(b) {
		t.Fatalf("fallback differs from the LIKE scan:\nlike     %v\nfallback %v", a, b)
	}
	if _, err := db.conn.Exec(`DROP TABLE observations`); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.scanReachRows(context.Background(), toks, 500); err == nil {
		t.Fatal("both scans failed, but scanReachRows returned no error")
	}
}

// A window that starts before the coverage mark must use the LIKE scan, so a
// half-finished backfill can never silently drop old observations.
func TestScanReachRows_PartialCoverageFallsBackToLike(t *testing.T) {
	db := newHopIndexedTestDB(t)
	defer db.conn.Close()
	srv := &Server{db: db}
	if err := dbschema.EnsureObservationHopsSchema(db.conn, nil); err != nil {
		t.Fatal(err)
	}
	// Index is empty (backfill not run) and since = now: a correct reader must
	// ignore it and still find rows.
	rows, err := srv.scanReachRows(context.Background(), map[string]bool{"01FA": true}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 { // obs 1,2,4,6 contain exactly "01FA"
		t.Fatalf("fallback scan returned %d rows, want 4", len(rows))
	}
}
