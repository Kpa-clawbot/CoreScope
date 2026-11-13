package pgschema

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/meshcore-analyzer/dbx/dbxtest"
)

// logRecorder collects Apply's log lines; safe for concurrent Apply calls.
type logRecorder struct {
	mu    sync.Mutex
	lines []string
}

func (r *logRecorder) logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, fmt.Sprintf(format, args...))
}

func (r *logRecorder) count(substr string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, l := range r.lines {
		if strings.Contains(l, substr) {
			n++
		}
	}
	return n
}

func versionRows(t *testing.T, db *sql.DB) []int {
	t.Helper()
	rows, err := db.Query(`SELECT version FROM schema_version ORDER BY version`)
	if err != nil {
		t.Fatalf("read schema_version: %v", err)
	}
	defer rows.Close()
	var vs []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		vs = append(vs, v)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return vs
}

func relationExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var ok bool
	if err := db.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relname = ?)`, name).Scan(&ok); err != nil {
		t.Fatalf("probe %s: %v", name, err)
	}
	return ok
}

func TestEmbeddedMigrationsMatchLatestVersion(t *testing.T) {
	migs, err := loadMigrations(migrationFS)
	if err != nil {
		t.Fatal(err)
	}
	if len(migs) == 0 || migs[len(migs)-1].version != LatestVersion {
		t.Fatalf("embedded migrations end at %v, LatestVersion = %d", migs, LatestVersion)
	}
	if migs[0].name != "0001_baseline.sql" {
		t.Fatalf("version 1 is %q, want 0001_baseline.sql", migs[0].name)
	}
}

func TestLoadMigrationsRejectsBadSets(t *testing.T) {
	body := &fstest.MapFile{Data: []byte("SELECT 1")}
	cases := map[string]fstest.MapFS{
		"gap":       {"migrations/0001_a.sql": body, "migrations/0003_c.sql": body},
		"duplicate": {"migrations/0001_a.sql": body, "migrations/0001_b.sql": body},
		"unpadded":  {"migrations/1_a.sql": body},
		"no name":   {"migrations/0001.sql": body},
		"zero":      {"migrations/0000_a.sql": body},
	}
	for name, fsys := range cases {
		if _, err := loadMigrations(fsys); err == nil {
			t.Errorf("%s: loadMigrations accepted it", name)
		}
	}
	migs, err := loadMigrations(fstest.MapFS{"migrations/0002_b.sql": body, "migrations/0001_a.sql": body})
	if err != nil || len(migs) != 2 || migs[0].version != 1 || migs[1].version != 2 {
		t.Fatalf("valid set: got %v, %v", migs, err)
	}
}

func TestCheckServerVersionNum(t *testing.T) {
	for num, ok := range map[string]bool{
		"150007": false,
		"90624":  false,
		"160000": true,
		"170002": true,
		"180000": true,
		"":       false,
		"17.2":   false,
	} {
		if err := checkServerVersionNum(num); (err == nil) != ok {
			t.Errorf("checkServerVersionNum(%q) = %v, want ok=%v", num, err, ok)
		}
	}
}

func TestApplyCreatesSchemaAndIsIdempotent(t *testing.T) {
	db := dbxtest.OpenPostgres(t)
	ctx := context.Background()

	if err := AssertReady(ctx, db); err == nil || !strings.Contains(err.Error(), "start the ingestor") {
		t.Fatalf("AssertReady on empty schema = %v, want start-the-ingestor error", err)
	}

	var rec logRecorder
	if err := Apply(ctx, db, rec.logf); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := versionRows(t, db); len(got) != 1 || got[0] != 1 {
		t.Fatalf("schema_version = %v, want [1]", got)
	}
	var appliedAt string
	if err := db.QueryRow(`SELECT applied_at FROM schema_version`).Scan(&appliedAt); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d$`).MatchString(appliedAt) {
		t.Errorf("applied_at = %q, want YYYY-MM-DD HH:MM:SS", appliedAt)
	}

	var tables int
	if err := db.QueryRow(`SELECT count(*) FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_type = 'BASE TABLE'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if tables != 26 { // 25 SQLite tables + schema_version
		t.Errorf("base tables = %d, want 26", tables)
	}
	for _, rel := range []string{"packets_v", "idx_observations_dedup", "idx_observers_iata_norm", "idx_tx_last_seen_zero"} {
		if !relationExists(t, db, rel) {
			t.Errorf("%s missing after Apply", rel)
		}
	}
	if err := AssertReady(ctx, db); err != nil {
		t.Fatalf("AssertReady after Apply: %v", err)
	}
	if n := rec.count("applied migration"); n != 1 {
		t.Fatalf("first Apply logged %d applications, want 1", n)
	}

	var again logRecorder
	if err := Apply(ctx, db, again.logf); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	if n := again.count("applied migration"); n != 0 {
		t.Fatalf("second Apply logged %d applications, want 0", n)
	}
	if got := versionRows(t, db); len(got) != 1 {
		t.Fatalf("schema_version after second Apply = %v, want [1]", got)
	}
}

func TestApplyRefusesNewerSchema(t *testing.T) {
	db := dbxtest.OpenPostgres(t)
	ctx := context.Background()
	if err := Apply(ctx, db, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO schema_version (version, applied_at) VALUES (?, 'x')`, LatestVersion+1); err != nil {
		t.Fatal(err)
	}
	err := Apply(ctx, db, nil)
	if err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("Apply over a newer schema = %v, want refusal", err)
	}
}

func TestApplyConcurrentAppliesOnce(t *testing.T) {
	db := dbxtest.OpenPostgres(t)
	ctx := context.Background()
	var rec logRecorder
	var wg sync.WaitGroup
	errs := make([]error, 2)
	start := make(chan struct{})
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = Apply(ctx, db, rec.logf)
		}()
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("Apply #%d: %v", i, err)
		}
	}
	if n := rec.count("applied migration"); n != 1 {
		t.Errorf("concurrent Apply logged %d applications, want exactly 1", n)
	}
	if got := versionRows(t, db); len(got) != 1 || got[0] != 1 {
		t.Errorf("schema_version = %v, want [1]", got)
	}
}

func TestApplyRollsBackFailedMigration(t *testing.T) {
	db := dbxtest.OpenPostgres(t)
	ctx := context.Background()
	migs := []migration{
		{version: 1, name: "0001_ok.sql", sql: `CREATE TABLE first_tbl (id BIGINT);`},
		{version: 2, name: "0002_bad.sql", sql: `CREATE TABLE second_tbl (id BIGINT);
			CREATE INDEX second_idx ON second_tbl(id);
			SELECT 1/0;`},
	}
	err := apply(ctx, db, nil, migs)
	if err == nil || !strings.Contains(err.Error(), "0002_bad.sql") {
		t.Fatalf("apply = %v, want an error naming 0002_bad.sql", err)
	}
	if !relationExists(t, db, "first_tbl") {
		t.Error("migration 1 was rolled back with migration 2")
	}
	for _, rel := range []string{"second_tbl", "second_idx"} {
		if relationExists(t, db, rel) {
			t.Errorf("%s survived the failed migration", rel)
		}
	}
	if got := versionRows(t, db); len(got) != 1 || got[0] != 1 {
		t.Errorf("schema_version = %v, want [1]", got)
	}

	// The lock was released: a corrected migration list applies cleanly.
	migs[1].sql = `CREATE TABLE second_tbl (id BIGINT);`
	if err := apply(ctx, db, nil, migs); err != nil {
		t.Fatalf("apply after fix: %v", err)
	}
	if got := versionRows(t, db); len(got) != 2 {
		t.Errorf("schema_version after fix = %v, want [1 2]", got)
	}
}

func TestBaselineSeedsMigrationMarkers(t *testing.T) {
	db := dbxtest.OpenPostgres(t)
	if err := Apply(context.Background(), db, nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"advert_route_evidence_v1", "content_hash_payload_bits_v1", "obs_observer_ts_idx_v1", "tx_last_seen_backfill_v1"} {
		var status string
		var ended sql.NullString
		if err := db.QueryRow(`SELECT status, ended_at FROM _async_migrations WHERE name = ?`, name).Scan(&status, &ended); err != nil {
			t.Errorf("_async_migrations %s: %v", name, err)
			continue
		}
		if status != "done" || !ended.Valid {
			t.Errorf("_async_migrations %s = (%q, %v), want done with ended_at", name, status, ended)
		}
	}
	var one int
	if err := db.QueryRow(`SELECT 1 FROM _migrations WHERE name = 'backfill_path_json_from_raw_hex_v1'`).Scan(&one); err != nil {
		t.Errorf("_migrations backfill_path_json_from_raw_hex_v1: %v", err)
	}
}

// The defaults and the view must produce text byte-identical to SQLite's,
// whatever the session time zone.
func TestBaselineTextMatchesSQLite(t *testing.T) {
	db := dbxtest.OpenPostgres(t)
	ctx := context.Background()
	if err := Apply(ctx, db, nil); err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SET TimeZone = 'Pacific/Auckland'`); err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC().Add(-time.Second)

	var txID int64
	if err := conn.QueryRowContext(ctx, `INSERT INTO transmissions (raw_hex, hash, first_seen) VALUES ('aa', 'h1', 'x') RETURNING id`).Scan(&txID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO observations (transmission_id, timestamp) VALUES (?, 1700000000)`, txID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO dropped_packets (reason) VALUES ('r')`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO key_backfill_done (kind, key_fp) VALUES ('channel', 'k')`); err != nil {
		t.Fatal(err)
	}

	var viewTS, createdAt, droppedAt string
	var doneAt int64
	if err := conn.QueryRowContext(ctx, `SELECT timestamp, created_at FROM packets_v`).Scan(&viewTS, &createdAt); err != nil {
		t.Fatal(err)
	}
	if viewTS != "2023-11-14 22:13:20" { // SQLite: datetime(1700000000, 'unixepoch')
		t.Errorf("packets_v.timestamp = %q, want 2023-11-14 22:13:20", viewTS)
	}
	if err := conn.QueryRowContext(ctx, `SELECT dropped_at FROM dropped_packets`).Scan(&droppedAt); err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(ctx, `SELECT done_at FROM key_backfill_done`).Scan(&doneAt); err != nil {
		t.Fatal(err)
	}
	after := time.Now().UTC().Add(time.Second)
	for col, v := range map[string]string{"transmissions.created_at": createdAt, "dropped_packets.dropped_at": droppedAt} {
		ts, err := time.Parse(time.DateTime, v)
		if err != nil || ts.Before(before.Truncate(time.Second)) || ts.After(after) {
			t.Errorf("%s = %q, want UTC now as YYYY-MM-DD HH:MM:SS (%v)", col, v, err)
		}
	}
	if doneAt < before.Unix() || doneAt > after.Unix() {
		t.Errorf("key_backfill_done.done_at = %d, want epoch now", doneAt)
	}

	// TEXT sorts by byte, as SQLite's BINARY collation does.
	if _, err := conn.ExecContext(ctx, `INSERT INTO _migrations (name) VALUES ('a_lower'), ('B_upper')`); err != nil {
		t.Fatal(err)
	}
	var first string
	if err := conn.QueryRowContext(ctx, `SELECT name FROM _migrations WHERE name IN ('a_lower', 'B_upper') ORDER BY name LIMIT 1`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	if first != "B_upper" {
		t.Errorf("ORDER BY name put %q first, want byte order (B_upper)", first)
	}
}
