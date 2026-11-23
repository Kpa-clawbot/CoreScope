package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/prunequeue"
)

// testPruneFilter is a square around (2,2): (1,1) is outside it.
var testPruneFilter = &GeoFilterConfig{Polygon: [][2]float64{{1.5, 1.5}, {1.5, 2.5}, {2.5, 2.5}, {2.5, 1.5}}}

// seedPruneNodes inserts the nodes the geo-guard tests ask about.
func seedPruneNodes(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.db.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen) VALUES
		('out1', 'outside', 'companion', 1.0, 1.0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
		('out2', 'outside', 'companion', 3.0, 3.0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
		('in01', 'inside', 'repeater', 2.0, 2.0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
		('zero', 'no fix', 'repeater', 0.0, 0.0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
		('nopo', 'no position', 'repeater', NULL, NULL, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// nodeSet is nodeKeys as a set.
func nodeSet(t *testing.T, s *Store) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, pk := range nodeKeys(t, s) {
		out[pk] = true
	}
	return out
}

// A prune request is only a list of candidates: the ingestor deletes just the
// nodes its own geo_filter puts outside, and says why it kept the others.
func TestPruneRequestRecheckedAgainstIngestorGeoFilter(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedPruneNodes(t, s)

	id := prunequeue.NewID()
	if err := prunequeue.WriteRequest(dbPath, prunequeue.Request{ID: id, Reason: "geo-prune\nFAKE LINE",
		Pubkeys: []string{"out1", "out2", "out1", "in01", "zero", "nopo", "ghost"}}); err != nil {
		t.Fatal(err)
	}
	logs := captureLogs(t)
	s.RunPendingPruneRequests(testPruneFilter)

	res, err := prunequeue.ReadResult(dbPath, id)
	if err != nil || res == nil {
		t.Fatalf("ReadResult: %+v %v", res, err)
	}
	if res.Deleted != 2 || res.Error != "" || res.Skipped != 4 {
		t.Errorf("result = %+v, want 2 deleted, 4 skipped and no error", res)
	}
	wantReasons := map[string]int64{pruneSkipNotFound: 1, pruneSkipNoPosition: 1, pruneSkipInside: 2}
	if !reflect.DeepEqual(res.SkipReasons, wantReasons) {
		t.Errorf("skip reasons = %v, want %v", res.SkipReasons, wantReasons)
	}
	got := nodeSet(t, s)
	for pk, want := range map[string]bool{"out1": false, "out2": false, "in01": true, "zero": true, "nopo": true} {
		if got[pk] != want {
			t.Errorf("node %s present=%v, want %v", pk, got[pk], want)
		}
	}
	want := "deleted 2 node(s)"
	sum := "4 skipped (1 not a node, 1 no position, 2 inside the ingestor's geo_filter)"
	if out := logs.String(); !strings.Contains(out, want) || !strings.Contains(out, sum) {
		t.Errorf("log lacks %q / %q:\n%s", want, sum, out)
	}
	if out := logs.String(); strings.Contains(out, "\nFAKE LINE") {
		t.Errorf("request reason injected a log line:\n%s", out)
	}
}

// Without a polygon of its own the ingestor deletes nothing, whatever the
// request names; a bounding-box-only filter is not one (the server refuses to
// preview a prune for it).
func TestPruneRequestWithoutIngestorPolygonDeletesNothing(t *testing.T) {
	lo, hi := 1.5, 2.5
	for name, gf := range map[string]*GeoFilterConfig{
		"none":    nil,
		"two-pts": {Polygon: [][2]float64{{0, 0}, {1, 1}}},
		"bbox":    {LatMin: &lo, LatMax: &hi, LonMin: &lo, LonMax: &hi},
	} {
		t.Run(name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "test.db")
			s, err := OpenStore(dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			seedPruneNodes(t, s)
			id := prunequeue.NewID()
			if err := prunequeue.WriteRequest(dbPath, prunequeue.Request{ID: id, Pubkeys: []string{"out1", "out2"}}); err != nil {
				t.Fatal(err)
			}
			logs := captureLogs(t)
			s.RunPendingPruneRequests(gf)
			res, err := prunequeue.ReadResult(dbPath, id)
			if err != nil || res == nil || res.Deleted != 0 || res.Error != "" || res.Skipped != 2 || res.SkipReasons[pruneSkipNoFilter] != 2 {
				t.Fatalf("result = %+v %v, want 0 deleted", res, err)
			}
			if n := len(nodeSet(t, s)); n != 5 {
				t.Errorf("%d nodes left, want all 5", n)
			}
			if out := logs.String(); !strings.Contains(out, "2 skipped (2 "+pruneSkipNoFilter+")") {
				t.Errorf("log lacks the no-filter skip:\n%s", out)
			}
		})
	}
}

func TestGeoPruneOutcomeSkipSummary(t *testing.T) {
	if got := (geoPruneOutcome{}).skipSummary(); got != "" {
		t.Errorf("empty outcome summary = %q", got)
	}
	o := geoPruneOutcome{Skipped: map[string]int64{pruneSkipInside: 3, pruneSkipNotFound: 1}}
	if got, want := o.skipSummary(), "4 skipped (1 not a node, 3 inside the ingestor's geo_filter)"; got != want {
		t.Errorf("summary = %q, want %q", got, want)
	}
}

// More than prunequeue.MaxPendingList pending: the oldest are honored, the
// excess is completed with an error and deletes nothing.
func TestPruneQueueOverflowRejectsExcess(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedPruneNodes(t, s)

	const extra = 5
	base := time.Now().Add(-time.Hour)
	ids := make([]string, prunequeue.MaxPendingList+extra)
	for i := range ids {
		ids[i] = prunequeue.NewID()
		pk := "none"
		if i == len(ids)-1 {
			pk = "out1" // the newest would delete a node if honored
		}
		if err := prunequeue.WriteRequest(dbPath, prunequeue.Request{ID: ids[i], Pubkeys: []string{pk}}); err != nil {
			t.Fatal(err)
		}
		p, _ := prunequeue.RequestPath(dbPath, ids[i])
		at := base.Add(time.Duration(i) * time.Second)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}

	s.RunPendingPruneRequests(testPruneFilter)

	for i, id := range ids {
		res, err := prunequeue.ReadResult(dbPath, id)
		if err != nil || res == nil {
			t.Fatalf("request %d: no result (%v)", i, err)
		}
		wantErr := ""
		if i >= prunequeue.MaxPendingList {
			wantErr = pruneTooManyPending
		}
		if res.Error != wantErr || res.Deleted != 0 {
			t.Errorf("request %d result = %+v, want error %q", i, res, wantErr)
		}
	}
	if !nodeSet(t, s)["out1"] {
		t.Error("a rejected request deleted its node")
	}
	if n, err := s.pruneQ.PendingCount(); err != nil || n != 0 {
		t.Errorf("%d still pending (%v)", n, err)
	}
}

// The same overflow on Postgres, where the rows' created_at orders the queue.
func TestPostgresPruneQueueOverflowRejectsExcess(t *testing.T) {
	s := openPostgresTestStore(t, filepath.Join(t.TempDir(), "meshcore.db"))
	ctl := isolateControlTables(t, s)
	seedPruneNodes(t, s)
	server := prunequeue.NewPG(s.db, ctl)

	ids := make([]string, prunequeue.MaxPendingList+3)
	for i := range ids {
		ids[i] = prunequeue.NewID()
		pk := "out2"
		if i == 0 {
			pk = "out1" // the oldest is honored
		}
		payload := fmt.Sprintf(`{"id":%q,"pubkeys":[%q]}`, ids[i], pk)
		at := fmt.Sprintf("2026-01-01T00:%02d:%02d.000000000Z", i/60, i%60)
		if _, err := s.db.Exec(`INSERT INTO "`+ctl+`".prune_requests (id, created_at, payload) VALUES (?, ?, ?)`, ids[i], at, payload); err != nil {
			t.Fatal(err)
		}
	}

	s.RunPendingPruneRequests(testPruneFilter)

	for i, id := range ids {
		res, err := server.ReadResult(id)
		if err != nil || res == nil {
			t.Fatalf("request %d: no result (%v)", i, err)
		}
		if rejected := res.Error == pruneTooManyPending; rejected != (i >= prunequeue.MaxPendingList) {
			t.Errorf("request %d result = %+v", i, res)
		}
	}
	got := nodeSet(t, s)
	if got["out1"] || got["out2"] {
		t.Errorf("honored requests did not delete their nodes: %v", got)
	}
	if n, err := server.PendingCount(); err != nil || n != 0 {
		t.Errorf("%d still pending (%v)", n, err)
	}
}

// Each pass reads geo_filter from the ingestor's config file anew: a polygon
// saved between two passes applies to the second one without a restart. An
// unreadable file falls back to the startup value, warning once.
func TestPruneGeoFilterReReadEachPass(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	cfgPath := filepath.Join(dir, "config.json")
	writeCfg := func(gf *GeoFilterConfig) {
		t.Helper()
		b, err := json.Marshal(map[string]any{"dbPath": dbPath, "geo_filter": gf})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(cfgPath, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedPruneNodes(t, s)
	request := func(pk string) string {
		t.Helper()
		id := prunequeue.NewID()
		if err := prunequeue.WriteRequest(dbPath, prunequeue.Request{ID: id, Pubkeys: []string{pk}}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	deleted := func(id string) int64 {
		t.Helper()
		res, err := prunequeue.ReadResult(dbPath, id)
		if err != nil || res == nil {
			t.Fatalf("ReadResult: %+v %v", res, err)
		}
		return res.Deleted
	}

	writeCfg(testPruneFilter) // (2,2) inside
	src := newGeoFilterSource(cfgPath, testPruneFilter)
	first := request("in01")
	s.RunPendingPruneRequests(src.Current())
	if n := deleted(first); n != 0 {
		t.Fatalf("first pass deleted %d inside the filter", n)
	}

	// The filter moves away from (2,2): the same node is now outside.
	writeCfg(&GeoFilterConfig{Polygon: [][2]float64{{9, 9}, {9, 10}, {10, 10}, {10, 9}}})
	second := request("in01")
	s.RunPendingPruneRequests(src.Current())
	if n := deleted(second); n != 1 {
		t.Fatalf("second pass deleted %d, want the node the new polygon excludes", n)
	}

	// Unparseable: the startup filter applies.
	if err := os.WriteFile(cfgPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	logs := captureLogs(t)
	for i := 0; i < 2; i++ {
		if got := src.Current(); got != testPruneFilter {
			t.Fatalf("fallback = %+v, want the startup filter", got)
		}
	}
	if n := strings.Count(logs.String(), "re-reading geo_filter failed"); n != 1 {
		t.Errorf("warned %d times, want once:\n%s", n, logs.String())
	}
}
