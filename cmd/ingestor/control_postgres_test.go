package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meshcore-analyzer/dbschema/pgschema"
	"github.com/meshcore-analyzer/mbcapqueue"
	"github.com/meshcore-analyzer/prunequeue"
)

// isolateControlTables points s's prune queue and multibyte store at a copy of
// the corescope_control tables in a schema of this test's own, and returns its
// name. The real control schema has a fixed name shared by every test on the
// database, so a prune pass or a snapshot prune there would consume other
// tests' rows (and theirs ours). The copies are LIKE the migrated tables, so
// they carry the same columns and CHECKs.
func isolateControlTables(t testing.TB, s *Store) string {
	t.Helper()
	var main string
	if err := s.db.QueryRow(`SELECT current_schema()`).Scan(&main); err != nil {
		t.Fatal(err)
	}
	ctl := main + "_ctl"
	q := `"` + ctl + `"`
	if _, err := s.db.Exec(`CREATE SCHEMA ` + q); err != nil {
		t.Fatalf("create control schema: %v", err)
	}
	// Registered after openPostgresTestStore's Close, so it runs first.
	t.Cleanup(func() {
		if _, err := s.db.Exec(`DROP SCHEMA ` + q + ` CASCADE`); err != nil {
			t.Errorf("drop control schema: %v", err)
		}
	})
	for _, tbl := range []string{"prune_requests", "prune_results", "mbcap_snapshots"} {
		if _, err := s.db.Exec(`CREATE TABLE ` + q + `.` + tbl + ` (LIKE corescope_control.` + tbl + ` INCLUDING ALL)`); err != nil {
			t.Fatalf("create %s: %v", tbl, err)
		}
	}
	s.pruneQ = prunequeue.NewPG(s.db, ctl)
	s.mbcap = mbcapqueue.NewPG(s.db, ctl)
	return ctl
}

// noQueueFiles fails if anything was created next to dbPath's queue anchor:
// on Postgres the ingestor exchanges nothing through files.
func noQueueFiles(t *testing.T, dbPath string) {
	t.Helper()
	for _, dir := range []string{prunequeue.QueueDir(dbPath), mbcapqueue.QueueDir(dbPath)} {
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s exists (or stat failed: %v); Postgres must not use the file queues", dir, err)
		}
	}
}

// The store chooses its queues once by backend: files next to the SQLite
// database, the corescope_control tables on Postgres.
func TestStoreQueuesFollowBackend(t *testing.T) {
	liteDir := t.TempDir()
	litePath := filepath.Join(liteDir, "meshcore.db")
	lite, err := OpenStore(litePath)
	if err != nil {
		t.Fatal(err)
	}
	defer lite.Close()
	if got, want := lite.pruneQ.Location(), prunequeue.QueueDir(litePath); got != want {
		t.Errorf("SQLite prune queue at %q, want %q", got, want)
	}
	if got, want := lite.mbcap.Location(), mbcapqueue.QueueDir(litePath); got != want {
		t.Errorf("SQLite multibyte store at %q, want %q", got, want)
	}

	pgPath := filepath.Join(t.TempDir(), "meshcore.db")
	pg := openPostgresTestStore(t, pgPath)
	if got := pg.pruneQ.Location(); got != "postgres:corescope_control" {
		t.Errorf("Postgres prune queue at %q", got)
	}
	if got := pg.mbcap.Location(); got != "postgres:corescope_control" {
		t.Errorf("Postgres multibyte store at %q", got)
	}
	noQueueFiles(t, pgPath)
}

// A geo-prune request the server queues on Postgres goes through the
// ingestor's prune pass: nodes deleted, request gone, result readable by the
// server — and no file anywhere.
func TestPostgresPruneRequestEndToEnd(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "meshcore.db")
	s := openPostgresTestStore(t, dbPath)
	ctl := isolateControlTables(t, s)
	if _, err := s.db.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen)
		VALUES ('aaaa', 'gone', 'companion', 1.0, 1.0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
		       ('bbbb', 'kept', 'companion', 2.0, 2.0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	server := prunequeue.NewPG(s.db, ctl)
	id := prunequeue.NewID()
	reqAt := time.Now().UTC().Truncate(time.Second)
	if err := server.WriteRequest(prunequeue.Request{ID: id, RequestedAt: reqAt, Reason: "geo-prune-test", Pubkeys: []string{"aaaa"}}); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}

	s.RunPendingPruneRequests(testPruneFilter)

	if pending, err := server.RequestExists(id); err != nil || pending {
		t.Errorf("request still pending after the pass: %v %v", pending, err)
	}
	res, err := server.ReadResult(id)
	if err != nil || res == nil {
		t.Fatalf("ReadResult: %+v %v", res, err)
	}
	if res.Deleted != 1 || res.Error != "" || !res.RequestedAt.Equal(reqAt) || res.CompletedAt.IsZero() {
		t.Errorf("result = %+v", res)
	}
	var gone, kept int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM nodes WHERE public_key = 'aaaa'`).Scan(&gone); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM nodes WHERE public_key = 'bbbb'`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if gone != 0 || kept != 1 {
		t.Errorf("nodes after prune: aaaa=%d bbbb=%d, want 0 and 1", gone, kept)
	}

	// The pass also ages results out: one completed long ago is dropped.
	old := prunequeue.NewID()
	if _, err := s.db.Exec(`INSERT INTO "`+ctl+`".prune_results (id, created_at, payload) VALUES (?, '2000-01-01T00:00:00.000000000Z', '{}')`, old); err != nil {
		t.Fatal(err)
	}
	s.RunPendingPruneRequests(testPruneFilter)
	if r, err := server.ReadResult(old); r != nil || err != nil {
		t.Errorf("result older than %s survived the pass: %+v %v", pruneResultsMaxAge, r, err)
	}
	if r, _ := server.ReadResult(id); r == nil {
		t.Error("the pass dropped a fresh result")
	}
	noQueueFiles(t, dbPath)
}

// On Postgres an unreadable request cannot be deleted by the ingestor's queue
// API, so it is completed with an error result: it leaves the queue, the
// server's status poll learns why, and it deletes nothing. A valid request
// next to it is still honored.
func TestPostgresUnreadablePruneRequestGetsErrorResult(t *testing.T) {
	s := openPostgresTestStore(t, filepath.Join(t.TempDir(), "meshcore.db"))
	ctl := isolateControlTables(t, s)
	if _, err := s.db.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen)
		VALUES ('aaaa', 'a', 'companion', 1.0, 1.0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z'),
		       ('bbbb', 'b', 'companion', 1.0, 1.0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	server := prunequeue.NewPG(s.db, ctl)
	garbled, misnamed, valid := prunequeue.NewID(), prunequeue.NewID(), prunequeue.NewID()
	insert := func(id, payload string) {
		t.Helper()
		if _, err := s.db.Exec(`INSERT INTO "`+ctl+`".prune_requests (id, created_at, payload) VALUES (?, '2026-01-01T00:00:00.000000000Z', ?)`, id, payload); err != nil {
			t.Fatal(err)
		}
	}
	insert(garbled, "not-json{{{")
	insert(misnamed, `{"id":"`+prunequeue.NewID()+`","pubkeys":["aaaa"]}`)
	if err := server.WriteRequest(prunequeue.Request{ID: valid, Pubkeys: []string{"bbbb"}}); err != nil {
		t.Fatal(err)
	}

	s.RunPendingPruneRequests(testPruneFilter)

	for _, c := range []struct{ id, want string }{{garbled, "invalid character"}, {misnamed, "request names id"}} {
		if pending, err := server.RequestExists(c.id); err != nil || pending {
			t.Errorf("%s still pending: %v %v", c.id, pending, err)
		}
		res, err := server.ReadResult(c.id)
		if err != nil || res == nil {
			t.Fatalf("ReadResult(%s): %+v %v", c.id, res, err)
		}
		if !strings.HasPrefix(res.Error, "unreadable prune request: ") || !strings.Contains(res.Error, c.want) || res.Deleted != 0 || res.ID != c.id {
			t.Errorf("error result for %s = %+v, want an unreadable-request error mentioning %q", c.id, res, c.want)
		}
	}
	if res, err := server.ReadResult(valid); err != nil || res == nil || res.Deleted != 1 || res.Error != "" {
		t.Errorf("valid request next to the bad ones: %+v %v", res, err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM nodes WHERE public_key = 'aaaa'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("the misnamed request deleted its node (count %d, %v)", n, err)
	}
	if ids, err := server.ListPending(); err != nil || len(ids) != 0 {
		t.Errorf("queue after the pass: %v %v", ids, err)
	}
}

// SQLite keeps removing an unreadable marker file, writing no result.
func TestSQLiteUnreadablePruneRequestRemoved(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "meshcore.db")
	s, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id := prunequeue.NewID()
	if _, err := prunequeue.EnsureDir(dbPath); err != nil {
		t.Fatal(err)
	}
	p, _ := prunequeue.RequestPath(dbPath, id)
	if err := os.WriteFile(p, []byte("not-json{{{"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.RunPendingPruneRequests(testPruneFilter)
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("unreadable request file left behind (%v)", err)
	}
	if r, err := prunequeue.ReadResult(dbPath, id); r != nil || err != nil {
		t.Errorf("SQLite wrote a result for an unreadable request: %+v %v", r, err)
	}
}

// The newest snapshot the server inserted on Postgres is applied, and after
// the persist only the newest multibyteSnapshotsKept rows remain.
func TestPostgresMultibyteSnapshotPersistedAndPruned(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "meshcore.db")
	s := openPostgresTestStore(t, dbPath)
	ctl := isolateControlTables(t, s)
	if _, err := s.db.Exec(`INSERT INTO nodes (public_key, name, role, last_seen, multibyte_sup) VALUES ('dd44', 'Delta', 'repeater', '2026-01-01T00:00:00Z', 0)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO inactive_nodes (public_key, name, role, last_seen, multibyte_sup) VALUES ('ee55', 'Echo', 'companion', '2025-12-01T00:00:00Z', 0)`); err != nil {
		t.Fatalf("seed inactive: %v", err)
	}

	server := mbcapqueue.NewPG(s.db, ctl)
	// Older snapshots say otherwise; only the newest may be applied.
	for i := 0; i < 4; i++ {
		if err := server.WriteSnapshot(mbcapqueue.Snapshot{Entries: []mbcapqueue.Entry{{PublicKey: "dd44", Status: "suspected", Evidence: "old"}}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := server.WriteSnapshot(mbcapqueue.Snapshot{Entries: []mbcapqueue.Entry{
		{PublicKey: "dd44", Status: "confirmed", Evidence: "advert"},
		{PublicKey: "ee55", Status: "suspected", Evidence: "path"},
	}}); err != nil {
		t.Fatal(err)
	}

	stats, err := s.RunMultibyteCapPersist()
	if err != nil {
		t.Fatalf("RunMultibyteCapPersist: %v", err)
	}
	if stats.ReadEntries != 2 || stats.UpdatedActive != 1 || stats.UpdatedInactive != 1 {
		t.Errorf("stats = %+v", stats)
	}
	var sup int
	var evid string
	if err := s.db.QueryRow(`SELECT multibyte_sup, multibyte_evidence FROM nodes WHERE public_key = 'dd44'`).Scan(&sup, &evid); err != nil || sup != 2 || evid != "advert" {
		t.Errorf("dd44: sup=%d evid=%q err=%v, want 2/advert", sup, evid, err)
	}
	if err := s.db.QueryRow(`SELECT multibyte_sup, multibyte_evidence FROM inactive_nodes WHERE public_key = 'ee55'`).Scan(&sup, &evid); err != nil || sup != 1 || evid != "path" {
		t.Errorf("ee55: sup=%d evid=%q err=%v, want 1/path", sup, evid, err)
	}

	var rows int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM "` + ctl + `".mbcap_snapshots`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != multibyteSnapshotsKept {
		t.Errorf("%d snapshots left after the persist, want %d", rows, multibyteSnapshotsKept)
	}
	if snap, err := server.ReadSnapshot(); err != nil || len(snap.Entries) != 2 {
		t.Errorf("the prune dropped the newest snapshot: %+v %v", snap, err)
	}
	noQueueFiles(t, dbPath)
}

// No snapshot yet on Postgres is the same quiet no-op as on SQLite.
func TestPostgresMultibyteNoSnapshotIsQuiet(t *testing.T) {
	s := openPostgresTestStore(t, filepath.Join(t.TempDir(), "meshcore.db"))
	isolateControlTables(t, s)
	logs := captureLogs(t)
	stats, err := s.RunMultibyteCapPersist()
	if err != nil || stats.ReadEntries != 0 {
		t.Fatalf("no snapshot: %+v %v", stats, err)
	}
	if out := logs.String(); strings.Contains(out, "multibyte-persist") {
		t.Errorf("no snapshot logged: %s", out)
	}
}

// On Postgres the stats go into the single ingestor_stats row (upserted every
// tick, updated_at = sampledAt), and no stats file is written.
func TestPostgresStatsWriterUpsertsRow(t *testing.T) {
	statsPath := filepath.Join(t.TempDir(), "ingestor-stats.json")
	t.Setenv("CORESCOPE_INGESTOR_STATS", statsPath)
	s := openPostgresTestStore(t, filepath.Join(t.TempDir(), "meshcore.db"))
	stop := StartStatsFileWriter(s, 20*time.Millisecond)
	defer stop()

	// waitFor polls the row until cond holds on its decoded payload.
	waitFor := func(what string, cond func(IngestorStatsSnapshot) bool) (string, IngestorStatsSnapshot) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			var updatedAt, payload string
			err := s.db.QueryRow(`SELECT updated_at, payload FROM ingestor_stats WHERE id = 1`).Scan(&updatedAt, &payload)
			var snap IngestorStatsSnapshot
			if err == nil && json.Unmarshal([]byte(payload), &snap) == nil && cond(snap) {
				return updatedAt, snap
			}
			if time.Now().After(deadline) {
				t.Fatalf("ingestor_stats never showed %s (last err %v, payload %q)", what, err, payload)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	updatedAt, snap := waitFor("a row", func(IngestorStatsSnapshot) bool { return true })
	at, err := time.Parse(time.RFC3339, updatedAt)
	if err != nil || at.Location() != time.UTC || updatedAt != snap.SampledAt {
		t.Errorf("updated_at %q (%v) must be the RFC3339 UTC sampledAt %q", updatedAt, err, snap.SampledAt)
	}
	s.Stats.TransmissionsInserted.Add(7)
	waitFor("the next tick's counters", func(sn IngestorStatsSnapshot) bool { return sn.TxInserted == 7 })
	stop()

	var rows int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ingestor_stats`).Scan(&rows); err != nil || rows != 1 {
		t.Errorf("ingestor_stats holds %d rows (%v), want 1", rows, err)
	}
	if _, err := os.Stat(statsPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("stats file written on Postgres (%v)", err)
	}
	if _, ok := s.WriterStatsSnapshot()["stats_writer"]; !ok {
		t.Error("the stats upsert did not go through WriterExec")
	}
}

// SQLite keeps the stats file and leaves its ingestor_stats table alone.
func TestSQLiteStatsWriterKeepsFile(t *testing.T) {
	dir := t.TempDir()
	statsPath := filepath.Join(dir, "ingestor-stats.json")
	t.Setenv("CORESCOPE_INGESTOR_STATS", statsPath)
	s, err := OpenStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	stop := StartStatsFileWriter(s, 20*time.Millisecond)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(statsPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			stop()
			t.Fatal("no stats file on SQLite")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	var rows int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ingestor_stats`).Scan(&rows); err != nil || rows != 0 {
		t.Errorf("SQLite ingestor_stats holds %d rows (%v), want 0", rows, err)
	}
}

// Opening on Postgres grants the control role; when the role is not there (or
// not set up as the script sets it up) one line says what needs it.
func TestOpenPostgresStoreReportsControlRole(t *testing.T) {
	logs := captureLogs(t)
	s := openPostgresTestStore(t, filepath.Join(t.TempDir(), "meshcore.db"))
	ok, err := pgschema.ControlGrantsOK(context.Background(), s.db)
	if err != nil {
		t.Fatal(err)
	}
	n := strings.Count(logs.String(), controlRoleMissingNote)
	if want := map[bool]int{true: 0, false: 1}[ok]; n != want {
		t.Errorf("control-role note logged %d times with ControlGrantsOK=%v, want %d:\n%s", n, ok, want, logs.String())
	}
}
