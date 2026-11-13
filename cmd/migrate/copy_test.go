package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/dbschema/pgschema"
	"github.com/meshcore-analyzer/dbx/dbxtest"
)

// pgSeedMigrations is what pgschema's baseline records in _migrations; a
// fully booted SQLite ingestor records the same names.
var pgSeedMigrations = []string{
	"advert_count_unique_v1", "backfill_default_scope_v1", "backfill_path_json_from_raw_hex_v1",
	"backfill_transport_codes_v1", "channel_hash_casing_v1", "channel_hash_v1",
	"cleanup_legacy_null_hash_ts", "dropped_packets_v1", "foreign_advert_v1", "from_pubkey_v1",
	"node_telemetry_v1", "nodes_configured_scope_v1", "nodes_default_scope_v1", "noise_floor_real_v1",
	"obs_observer_ts_idx_v1", "obs_timestamp_index_v1", "observations_raw_hex_v1",
	"observer_metrics_packets_v1", "observer_metrics_ts_idx", "observer_metrics_v1",
	"observer_radios_seed_v1", "observers_clock_naive_v1", "observers_inactive_v1",
	"observers_last_packet_at_v1", "scope_name_v1", "transport_codes_v1",
}

var pgSeedAsync = []string{"advert_route_evidence_v1", "content_hash_payload_bits_v1", "obs_observer_ts_idx_v1", "tx_last_seen_backfill_v1"}

// bigID is 2^53+1: the smallest integer a float64 cannot hold, so any
// conversion through float on the way would show.
const bigID int64 = 9007199254740993

func openWriter(t testing.TB, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", dbschema.WriterDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func mustExec(t testing.TB, db interface {
	Exec(string, ...any) (sql.Result, error)
}, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

// newSource creates a fully migrated SQLite database at path holding rows in
// every table, and closes it.
func newSource(t testing.TB, path string, migrations []string) {
	t.Helper()
	db := openWriter(t, path)
	mustExec(t, db, dbschema.SQLiteEndState)
	for _, m := range migrations {
		mustExec(t, db, `INSERT INTO _migrations (name) VALUES (?)`, m)
	}
	for _, m := range pgSeedAsync {
		mustExec(t, db, `INSERT INTO _async_migrations (name, status, started_at, ended_at) VALUES (?, 'done', '2025-01-02 03:04:05', '2025-01-02 03:05:00')`, m)
	}
	mustExec(t, db, `INSERT INTO _async_migrations (name, status, started_at, ended_at, error) VALUES ('legacy_v0', 'done', '2024-01-01 00:00:00', NULL, 'retried once: ünïcode')`)

	// Packets: large ids with gaps, one past 2^53.
	txIDs := []int64{1, 2, 1000, bigID}
	for i, id := range txIDs {
		var decoded any = fmt.Sprintf(`{"text":"Zürich 🚀 日本 %d"}`, i)
		if i == 1 {
			decoded = nil
		}
		mustExec(t, db, `INSERT INTO transmissions (id, raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json, from_pubkey, last_seen, created_at, code1, code2, channel_hash, scope_name)
			VALUES (?, ?, ?, '2025-03-01T10:00:00Z', 1, 5, 0, ?, ?, ?, '2025-03-01 10:00:00', ?, NULL, 'ab', NULL)`,
			id, fmt.Sprintf("DEADBEEF%02d", i), fmt.Sprintf("hash%d", i), decoded, fmt.Sprintf("pk%d", i), 1740823200+int64(i), nil)
	}
	// Observers with gaps in idx.
	for i, idx := range []int64{3, 7, 42} {
		mustExec(t, db, `INSERT INTO observers (idx, id, name, iata, last_seen, first_seen, packet_count, battery_mv, noise_floor, inactive, clock_skew_seconds, can_relay, can_relay_seen)
			VALUES (?, ?, ?, 'SEA', '2025-03-01T10:00:00Z', NULL, ?, NULL, ?, 0, ?, 1, 0)`,
			idx, fmt.Sprintf("OBS%d", i), "Obsérver ✓ "+fmt.Sprint(i), i*10, -112.5+float64(i), -int64(i))
	}
	// Observations: FK to transmissions, many rows so small -batch values
	// split them across COPY calls / transactions. Floats that do not
	// round-trip through decimal at low precision.
	floats := []float64{0.1, -1e-9, 1e300, -0.0, 12.25, math.SmallestNonzeroFloat64}
	id := int64(10)
	for i := 0; i < 60; i++ {
		id += int64(1 + i%5) // gaps
		var path any = fmt.Sprintf(`["A%d","B%d"]`, i, i)
		var score any = i
		if i%7 == 0 {
			path, score = nil, nil
		}
		// The ingestor writes float64 scores into the INTEGER-declared
		// column: 0.75 stays REAL, 3.0 is stored as integer 3.
		switch i {
		case 1:
			score = 0.75 // id 13
		case 2:
			score = 3.0 // id 16
		}
		mustExec(t, db, `INSERT INTO observations (id, transmission_id, observer_idx, direction, snr, rssi, score, path_json, timestamp, resolved_path, raw_hex)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?)`,
			id, txIDs[i%len(txIDs)], []int64{3, 7, 42}[i%3], "rx", floats[i%len(floats)], -90.5-float64(i), score, path, 1740823200+int64(i), nil)
	}
	mustExec(t, db, `INSERT INTO observation_radios (observation_id, radio_id) VALUES (11, 'r0'), (?, 'r1')`, bigID)
	mustExec(t, db, `INSERT INTO dropped_packets (id, hash, raw_hex, reason, observer_id, observer_name, node_pubkey, node_name, dropped_at)
		VALUES (5, 'h', NULL, 'bad sig', 'OBS0', NULL, 'pk', 'Nöde', '2025-03-01 10:00:00'),
		       (900, NULL, 'FF', 'dup', NULL, NULL, NULL, NULL, 1740823200)`)
	mustExec(t, db, `INSERT INTO tx_rewrite_feed (id, tx_id, created_at) VALUES (3, 1, 1740823200), (77, ?, 1740823201)`, bigID)
	mustExec(t, db, `INSERT INTO advert_route_evidence (id, tx_id, bit) VALUES (1, 1, 1), (2, 1, 2), (50, ?, 1)`, bigID)
	mustExec(t, db, `INSERT INTO advert_evidence_backfill (id, tx_cursor, obs_cursor) VALUES (1, ?, 71)`, bigID)
	mustExec(t, db, `INSERT INTO key_backfill_state (kind, run_fp, cursor, upper, rewritten, started_at) VALUES ('channel', 'fp1', 10, 20, 3, 1740823200)`)
	mustExec(t, db, `INSERT INTO key_backfill_done (kind, key_fp, done_at) VALUES ('channel', 'b', 1), ('channel', 'a', 2), ('region', 'a', 3)`)
	mustExec(t, db, `INSERT INTO scope_match_totals (id, since_unix, unique_matches, explicit_over_derived, ambiguous, none_matches, updated_unix) VALUES (1, 1, 2, 3, 4, 5, 6)`)

	// Nodes (text primary keys, NULLs, unicode).
	for _, tbl := range []string{"nodes", "inactive_nodes"} {
		mustExec(t, db, `INSERT INTO `+tbl+` (public_key, name, role, lat, lon, last_seen, first_seen, advert_count, battery_mv, temperature_c, foreign_advert, default_scope, configured_scope, configured_scope_at, multibyte_sup, multibyte_evidence)
			VALUES ('ff01', 'Gipfel ⛰', 'repeater', 47.3769, 8.5417, '2025-03-01T10:00:00Z', '2024-01-01T00:00:00Z', 12, 4100, -3.5, 0, '#zh', NULL, NULL, 1, '{"x":1}'),
			       ('0a02', NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, NULL, 0, NULL),
			       ('Zz03', '日本', 'companion', -33.8688, 151.2093, '2025-03-01T10:00:00Z', NULL, 0, NULL, 0.1, 1, NULL, '#syd', '2025-03-01T10:00:00Z', 0, NULL)`)
	}
	mustExec(t, db, `INSERT INTO neighbor_edges (node_a, node_b, count, last_seen) VALUES ('ff01', '0a02', 3, '2025-03-01'), ('0a02', 'ff01', NULL, NULL)`)
	mustExec(t, db, `INSERT INTO observer_metrics (observer_id, timestamp, noise_floor, tx_air_secs, rx_air_secs, recv_errors, battery_mv, packets_sent, packets_recv)
		VALUES ('OBS0', '2025-03-01T10:00:00Z', -110.25, 1, 2, 0, 4000, 10, 20), ('OBS0', '2025-03-01T10:05:00Z', NULL, NULL, NULL, NULL, NULL, NULL, NULL)`)
	mustExec(t, db, `INSERT INTO observer_radios (id, observer_id, radio_id, radio, first_seen, last_seen) VALUES (4, 'OBS0', '', 'sx1262', '2025-01-01', '2025-03-01'), (9, 'OBS1', 'r1', 'sx1276', '2025-01-01', '2025-03-01')`)
	mustExec(t, db, `INSERT INTO observer_neighbor_reports (observer_id, received_at, reported_at, neighbor_count, queried_neighbors, self_scopes, self_default_scope, neighbors_json)
		VALUES ('OBS0', '2025-03-01', NULL, 2, NULL, '#a,#b', '#a', '[{"k":"ff01"}]')`)

	// Client uploads.
	mustExec(t, db, `INSERT INTO client_observers (pubkey, name, last_seen) VALUES ('c1', 'Phöne', '2025-03-01'), ('c2', NULL, NULL)`)
	mustExec(t, db, `INSERT INTO client_receptions (id, rx_pubkey, heard_key, heard_keylen, rssi, snr, lat, lon, pos_acc_m, rx_at, ingested_at, src)
		VALUES (2, 'c1', 'ff', 1, -100, 5.25, 1.5, -2.5, NULL, '2025-03-01', '2025-03-01', 'app'), (8, 'c1', 'ff01', 2, NULL, NULL, 0.1, 0.2, 3.5, '2025-03-02', '2025-03-02', 'app')`)
	mustExec(t, db, `INSERT INTO client_rf_samples (id, rx_pubkey, sampled_at, ingested_at, lat, lon, pos_acc_m, stationary, uptime_secs, battery_mv, last_snr, recv_errors)
		VALUES (1, 'c1', '2025-03-01', '2025-03-01', 1, 2, NULL, 1, ?, 3900, -1e-9, NULL)`, int64(math.MaxInt64))
	mustExec(t, db, `INSERT INTO client_rx_observations (id, rx_pubkey, rx_at, ingested_at, pkt_hash, route_type, payload_type, code1, code2, scope_name, hash_size, hop_count, path_json, forwarder, snr, rssi, lat, lon, pos_acc_m)
		VALUES (6, 'c1', '2025-03-01', '2025-03-01', 'hash0', 1, 4, NULL, NULL, '#zh', 1, 2, '["A"]', 'ff', 7.5, -95, 47.1, 8.2, NULL)`)
	mustExec(t, db, `INSERT INTO client_node_names (id, target, rx_pubkey, observed_at, ingested_at, name, lat, lon, pos_acc_m, repeater_clock)
		VALUES (3, 'ff01', 'c1', '2025-03-01', '2025-03-01', 'Gipfel ⛰', NULL, NULL, NULL, -5)`)
	mustExec(t, db, `INSERT INTO node_declared_regions (id, target, rx_pubkey, observed_at, ingested_at, regions_csv, truncated, lat, lon, pos_acc_m, repeater_clock)
		VALUES (12, 'ff01', 'c1', '2025-03-01', '2025-03-01', '#zh,#ch', 0, 47.0, 8.0, NULL, NULL)`)
	db.Close()
}

// run invokes a subcommand in-process and returns its exit code and output.
func run(t testing.TB, args ...string) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	code := runSubcommand(args[0], args[1:], &buf)
	t.Logf("corescope-migrate %s -> exit %d\n%s", args[0], code, buf.String())
	return code, buf.String()
}

// pgTarget returns an isolated Postgres schema and a URL that reaches it.
func pgTarget(t testing.TB) (*sql.DB, string) {
	t.Helper()
	db := dbxtest.OpenPostgres(t)
	var schema string
	if err := db.QueryRow(`SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(os.Getenv("CORESCOPE_TEST_PG_DSN"))
	if err != nil {
		t.Fatal("unparseable CORESCOPE_TEST_PG_DSN")
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	return db, u.String()
}

func count(t testing.TB, db *sql.DB, table string) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + quoteIdent(table)).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func wantOK(t *testing.T, code int, out string) {
	t.Helper()
	if code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", code, out)
	}
}

func wantFail(t *testing.T, code int, out string, substrs ...string) {
	t.Helper()
	if code == 0 {
		t.Fatalf("exit 0, want failure:\n%s", out)
	}
	for _, s := range substrs {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q:\n%s", s, out)
		}
	}
}

func srcMigrations() []string {
	return append(append([]string(nil), pgSeedMigrations...), "legacy_sqlite_only_v1")
}

// TestSeedCoversEveryTable keeps the fixture honest: every table the copy
// moves must hold rows, so a table added to the schema without seed data
// fails here instead of going untested.
func TestSeedCoversEveryTable(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src.db")
	newSource(t, src, srcMigrations())
	db, err := sql.Open("sqlite3", sqliteReadOnlyDSN(src))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := loadSchema(t.Context(), db, sqliteBackend)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range s.sortedNames() {
		if skipReason(n) != "" {
			continue
		}
		if count(t, db, n) == 0 {
			t.Errorf("seed leaves table %s empty", n)
		}
	}
}

func TestCopySQLiteToSQLite(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.db"), filepath.Join(dir, "dst.db")
	newSource(t, src, srcMigrations())

	code, out := run(t, "copy", "-from", "sqlite:"+src, "-to", "sqlite:"+dst, "-batch", "7")
	wantOK(t, code, out)
	for _, s := range []string{"verify: all", "copied observations", "skipping sqlite_sequence", "WARNING: stop every writer"} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q", s)
		}
	}
	if _, err := os.Stat(dst + ".partial"); !os.IsNotExist(err) {
		t.Errorf(".partial left behind after success: %v", err)
	}

	db := openWriter(t, dst)
	if err := dbschema.AssertReady(db); err != nil {
		t.Errorf("copy is not ready: %v", err)
	}
	var seq int64
	if err := db.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name = 'transmissions'`).Scan(&seq); err != nil || seq != bigID {
		t.Errorf("sqlite_sequence(transmissions) = %d, %v; want %d", seq, err, bigID)
	}
	var obsMax int64
	db.QueryRow(`SELECT MAX(id) FROM observations`).Scan(&obsMax)
	if err := db.QueryRow(`SELECT seq FROM sqlite_sequence WHERE name = 'observations'`).Scan(&seq); err != nil || seq != obsMax {
		t.Errorf("sqlite_sequence(observations) = %d, %v; want %d", seq, err, obsMax)
	}
	// The integer stored in the DATETIME column survives as an integer.
	var typ string
	db.QueryRow(`SELECT typeof(dropped_at) FROM dropped_packets WHERE id = 900`).Scan(&typ)
	if typ != "integer" {
		t.Errorf("typeof(dropped_at) = %s, want integer", typ)
	}
	db.Close()

	code, out = run(t, "verify", "-a", "sqlite:"+src, "-b", "sqlite:"+dst)
	wantOK(t, code, out)
	assertScores(t, dst)
}

func TestVerifyReportsFirstDifference(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.db"), filepath.Join(dir, "dst.db")
	newSource(t, src, srcMigrations())
	code, out := run(t, "copy", "-from", "sqlite:"+src, "-to", "sqlite:"+dst)
	wantOK(t, code, out)

	db := openWriter(t, dst)
	mustExec(t, db, `UPDATE observations SET snr = 0.30000000000000004 WHERE id = 16`)
	mustExec(t, db, `DELETE FROM key_backfill_done WHERE kind = 'channel' AND key_fp = 'b'`)
	db.Close()

	code, out = run(t, "verify", "-a", "sqlite:"+src, "-b", "sqlite:"+dst)
	wantFail(t, code, out,
		"verify observations", "MISMATCH", "first difference at id=16: column snr",
		"first difference at kind=\"channel\", key_fp=\"b\": row exists on side a only",
		"2 of ")
	if !strings.Contains(out, "verify nodes") || !strings.Contains(out, "ok rows") {
		t.Errorf("unchanged tables should still report ok")
	}
}

func TestCopyRefusesExistingSQLiteTarget(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.db"), filepath.Join(dir, "dst.db")
	newSource(t, src, srcMigrations())
	if err := os.WriteFile(dst, []byte("precious"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out := run(t, "copy", "-from", "sqlite:"+src, "-to", "sqlite:"+dst)
	wantFail(t, code, out, "already exists; refusing to overwrite")
	if b, _ := os.ReadFile(dst); string(b) != "precious" {
		t.Errorf("target file was modified")
	}
}

func TestCopyRefusesLeftoverPartial(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.db"), filepath.Join(dir, "dst.db")
	newSource(t, src, srcMigrations())
	if err := os.WriteFile(dst+".partial", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out := run(t, "copy", "-from", "sqlite:"+src, "-to", "sqlite:"+dst)
	wantFail(t, code, out, "did not finish", "rm -f '"+dst+".partial'")
}

func TestCopyRefusesUnreadySource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	db := openWriter(t, src)
	mustExec(t, db, `CREATE TABLE nodes (public_key TEXT PRIMARY KEY)`)
	db.Close()
	code, out := run(t, "copy", "-from", "sqlite:"+src, "-to", "sqlite:"+filepath.Join(dir, "dst.db"))
	wantFail(t, code, out, "is not ready", "start the ingestor once on the source")
	if _, err := os.Stat(filepath.Join(dir, "dst.db.partial")); !os.IsNotExist(err) {
		t.Errorf("target work file created for an unready source")
	}
}

func TestCopyRefusesPendingAsyncMigration(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	newSource(t, src, srcMigrations())
	db := openWriter(t, src)
	mustExec(t, db, `UPDATE _async_migrations SET status = 'pending_async' WHERE name = 'tx_last_seen_backfill_v1'`)
	db.Close()
	code, out := run(t, "copy", "-from", "sqlite:"+src, "-to", "sqlite:"+filepath.Join(dir, "dst.db"))
	wantFail(t, code, out, "unfinished async migrations: tx_last_seen_backfill_v1 (pending_async)",
		"start the ingestor once on the source so its migrations finish")
}

func TestCopyTypeFailureNamesLocation(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.db"), filepath.Join(dir, "dst.db")
	newSource(t, src, srcMigrations())
	db := openWriter(t, src)
	mustExec(t, db, `UPDATE observations SET score = 'abc' WHERE id = 16`)
	db.Close()
	code, out := run(t, "copy", "-from", "sqlite:"+src, "-to", "sqlite:"+dst)
	wantFail(t, code, out, "table observations, row id=16, column score", `text "abc" cannot be stored in an integer column`,
		"was NOT created", "rm -f '"+dst+".partial'")
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("target path exists after a failed copy")
	}
}

func TestRedactsPasswords(t *testing.T) {
	ref, err := parseDBRef("postgres://alice:S3cretPw9@db.example:5432/cs?sslmode=disable&password=Other9pw")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ref.display, "S3cretPw9") || strings.Contains(ref.display, "Other9pw") {
		t.Errorf("display leaks a password: %s", ref.display)
	}
	if !strings.Contains(ref.display, "alice:xxxxx@db.example") {
		t.Errorf("display = %s", ref.display)
	}

	src := filepath.Join(t.TempDir(), "src.db")
	newSource(t, src, srcMigrations())
	// Nothing listens on port 1: the connection error path must not leak.
	code, out := run(t, "copy", "-from", "sqlite:"+src, "-to", "postgres://alice:S3cretPw9@127.0.0.1:1/cs?connect_timeout=2")
	wantFail(t, code, out)
	code2, out2 := run(t, "verify", "-a", "postgres://alice:S3cretPw9@127.0.0.1:1/cs?connect_timeout=2", "-b", "sqlite:"+src)
	wantFail(t, code2, out2)
	code3, out3 := run(t, "verify", "-a", "postgres://alice:S3cretPw9@[::1/cs", "-b", "sqlite:"+src)
	wantFail(t, code3, out3, "unparseable postgres URL")
	for _, o := range []string{out, out2, out3} {
		if strings.Contains(o, "S3cretPw9") {
			t.Errorf("output leaks the password:\n%s", o)
		}
	}
}

func TestNormalize(t *testing.T) {
	ok := []struct {
		cls  colClass
		in   any
		want any
	}{
		{clsInt, int64(bigID), bigID},
		{clsInt, nil, nil},
		{clsInt, float64(3), int64(3)},
		{clsFloat, int64(3), 3.0},
		{clsFloat, 0.75, 0.75},
		{clsFloat, 0.1, 0.1},
		{clsFloat, int64(5), 5.0},
		{clsText, "Zürich", "Zürich"},
		{clsText, int64(1740823200), "1740823200"},
		{clsBlob, []byte{1}, []byte{1}},
	}
	for _, c := range ok {
		got, err := normalize(c.cls, c.in, true)
		if err != nil || fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("normalize(%s, %#v) = %#v, %v; want %#v", c.cls, c.in, got, err, c.want)
		}
	}
	bad := []struct {
		cls colClass
		in  any
	}{
		{clsInt, "abc"},
		{clsInt, "12"},
		{clsInt, 1.5},
		{clsInt, []byte("x")},
		{clsFloat, "x"},
		{clsFloat, bigID},
		{clsText, []byte("x")},
		{clsText, "a\x00b"},
		{clsText, "\xff"},
	}
	for _, c := range bad {
		if got, err := normalize(c.cls, c.in, true); err == nil {
			t.Errorf("normalize(%s, %#v) = %#v, want error", c.cls, c.in, got)
		}
	}
	// A SQLite target follows affinity: TEXT keeps bytes Postgres would
	// reject, and INTEGER keeps a fraction as REAL.
	if _, err := normalize(clsText, "\xff", false); err != nil {
		t.Errorf("SQLite target rejected invalid UTF-8: %v", err)
	}
	if got, err := normalize(clsInt, 0.75, false); err != nil || got != 0.75 {
		t.Errorf("SQLite target: normalize(integer, 0.75) = %v, %v; want 0.75", got, err)
	}
	if string(appendValue(nil, math.Copysign(0, -1))) != string(appendValue(nil, 0.0)) {
		t.Errorf("-0 and 0 encode differently")
	}
}

func TestFKOrder(t *testing.T) {
	s := &schema{tables: map[string]*table{
		"a": {name: "a", parents: []string{"c"}},
		"b": {name: "b"},
		"c": {name: "c", parents: []string{"b", "c"}},
	}}
	got, err := fkOrder(s, []string{"a", "b", "c"})
	if err != nil || strings.Join(got, ",") != "b,c,a" {
		t.Fatalf("fkOrder = %v, %v", got, err)
	}
	s.tables["b"].parents = []string{"a"}
	if _, err := fkOrder(s, []string{"a", "b", "c"}); err == nil {
		t.Fatal("cycle not detected")
	}
}

func TestUsageErrors(t *testing.T) {
	if code, _ := run(t, "copy", "-from", "sqlite:x.db"); code != 2 {
		t.Errorf("missing -to: exit %d, want 2", code)
	}
	if code, out := run(t, "copy", "-from", "mysql://x", "-to", "sqlite:y"); code != 1 || !strings.Contains(out, "unrecognised database argument") {
		t.Errorf("bad scheme: exit %d", code)
	}
}

// ── Postgres ─────────────────────────────────────────────────────────────

func TestCopyRoundTripThroughPostgres(t *testing.T) {
	pg, pgURL := pgTarget(t)
	dir := t.TempDir()
	src, back := filepath.Join(dir, "src.db"), filepath.Join(dir, "back.db")
	newSource(t, src, srcMigrations())

	code, out := run(t, "copy", "-from", "sqlite:"+src, "-to", pgURL, "-batch", "7")
	wantOK(t, code, out)
	if strings.Contains(out, "tolerated") {
		t.Errorf("source holds every seeded bookkeeping row; nothing should be tolerated")
	}
	if pw, ok := mustURL(t, pgURL).User.Password(); ok && len(pw) >= minScrubLen && strings.Contains(out, pw) {
		t.Errorf("output contains the Postgres password")
	}

	// Values that are easy to get wrong arrive exactly.
	var id int64
	if err := pg.QueryRow(`SELECT MAX(id) FROM transmissions`).Scan(&id); err != nil || id != bigID {
		t.Errorf("MAX(transmissions.id) = %d, %v", id, err)
	}
	var started string
	pg.QueryRow(`SELECT started_at FROM _async_migrations WHERE name = 'advert_route_evidence_v1'`).Scan(&started)
	if started != "2025-01-02 03:04:05" {
		t.Errorf("seeded _async_migrations row not overwritten from source: started_at=%q", started)
	}
	var droppedAt string
	pg.QueryRow(`SELECT dropped_at FROM dropped_packets WHERE id = 900`).Scan(&droppedAt)
	if droppedAt != "1740823200" {
		t.Errorf("dropped_at = %q", droppedAt)
	}

	var s13, s16 float64
	if err := pg.QueryRow(`SELECT score FROM observations WHERE id = 13`).Scan(&s13); err != nil || s13 != 0.75 {
		t.Errorf("observations.score id=13 in Postgres = %v, %v; want 0.75", s13, err)
	}
	if err := pg.QueryRow(`SELECT score FROM observations WHERE id = 16`).Scan(&s16); err != nil || s16 != 3 {
		t.Errorf("observations.score id=16 in Postgres = %v, %v; want 3", s16, err)
	}

	// Identity sequences continue after the copied ids.
	var newID int64
	if err := pg.QueryRow(`INSERT INTO transmissions (raw_hex, hash, first_seen) VALUES ('00', 'new', 'now') RETURNING id`).Scan(&newID); err != nil {
		t.Fatalf("insert without id: %v", err)
	}
	if newID <= bigID {
		t.Errorf("new transmissions id %d, want > %d", newID, bigID)
	}
	var obsMax, obsNew int64
	pg.QueryRow(`SELECT MAX(id) FROM observations`).Scan(&obsMax)
	if err := pg.QueryRow(`INSERT INTO observations (transmission_id, timestamp) VALUES (1, 0) RETURNING id`).Scan(&obsNew); err != nil || obsNew <= obsMax {
		t.Errorf("new observations id %d (%v), want > %d", obsNew, err, obsMax)
	}
	var idxMax, idxNew int64
	pg.QueryRow(`SELECT MAX(idx) FROM observers`).Scan(&idxMax)
	if err := pg.QueryRow(`INSERT INTO observers (id) VALUES ('NEW') RETURNING idx`).Scan(&idxNew); err != nil || idxNew <= idxMax {
		t.Errorf("new observers idx %d (%v), want > %d", idxNew, err, idxMax)
	}
	mustExec(t, pg, `DELETE FROM observations WHERE id = ?`, obsNew)
	mustExec(t, pg, `DELETE FROM transmissions WHERE id = ?`, newID)
	mustExec(t, pg, `DELETE FROM observers WHERE idx = ?`, idxNew)

	code, out = run(t, "verify", "-a", "sqlite:"+src, "-b", pgURL)
	wantOK(t, code, out)

	code, out = run(t, "copy", "-from", pgURL, "-to", "sqlite:"+back, "-batch", "7")
	wantOK(t, code, out)

	// The SQLite that went through Postgres equals the original, digest for
	// digest, in both directions.
	code, out = run(t, "verify", "-a", "sqlite:"+src, "-b", "sqlite:"+back)
	wantOK(t, code, out)
	if strings.Contains(out, "tolerated") || strings.Contains(out, "digest a=") {
		t.Errorf("round trip is not digest-identical")
	}
	code, _ = run(t, "verify", "-a", "sqlite:"+back, "-b", "sqlite:"+src)
	if code != 0 {
		t.Errorf("reverse verify failed")
	}
	assertScores(t, back)
}

// assertScores checks the two score values seeded by newSource came back
// with their SQLite storage classes intact.
func assertScores(t *testing.T, path string) {
	t.Helper()
	db := openWriter(t, path)
	for _, c := range []struct {
		id         int
		typ, value string
	}{{13, "real", "0.75"}, {16, "integer", "3"}} {
		var typ, v string
		if err := db.QueryRow(`SELECT typeof(score), CAST(score AS TEXT) FROM observations WHERE id = ?`, c.id).Scan(&typ, &v); err != nil {
			t.Fatal(err)
		}
		if typ != c.typ || v != c.value {
			t.Errorf("observations.score id=%d is %s %s, want %s %s", c.id, typ, v, c.typ, c.value)
		}
	}
}

func mustURL(t testing.TB, s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal("unparseable URL")
	}
	return u
}

func TestCopyRefusesNonEmptyPostgresTarget(t *testing.T) {
	pg, pgURL := pgTarget(t)
	if err := pgschema.Apply(t.Context(), pg, t.Logf); err != nil {
		t.Fatal(err)
	}
	mustExec(t, pg, `INSERT INTO nodes (public_key, name) VALUES ('keep', 'mine')`)
	src := filepath.Join(t.TempDir(), "src.db")
	newSource(t, src, srcMigrations())

	code, out := run(t, "copy", "-from", "sqlite:"+src, "-to", pgURL)
	wantFail(t, code, out, "target is not empty (table nodes has rows); refusing to overwrite")
	if n := count(t, pg, "nodes"); n != 1 {
		t.Errorf("nodes has %d rows, want the 1 pre-existing", n)
	}
	if n := count(t, pg, "transmissions"); n != 0 {
		t.Errorf("transmissions has %d rows after refusal", n)
	}
}

// A failure mid-copy rolls the whole target transaction back, leaving an
// empty target that a rerun accepts.
func TestPostgresFailureRollsBackAndRerunWorks(t *testing.T) {
	pg, pgURL := pgTarget(t)
	src := filepath.Join(t.TempDir(), "src.db")
	newSource(t, src, srcMigrations())
	db := openWriter(t, src)
	mustExec(t, db, `UPDATE observations SET score = 'abc' WHERE id = 16`)
	db.Close()

	code, out := run(t, "copy", "-from", "sqlite:"+src, "-to", pgURL, "-batch", "7")
	wantFail(t, code, out, "table observations, row id=16, column score", "rolled back", "rerun the same command")
	for _, tbl := range []string{"transmissions", "observations", "nodes"} {
		if n := count(t, pg, tbl); n != 0 {
			t.Errorf("%s has %d rows after a failed copy", tbl, n)
		}
	}

	db = openWriter(t, src)
	mustExec(t, db, `UPDATE observations SET score = 4 WHERE id = 16`)
	db.Close()
	code, out = run(t, "copy", "-from", "sqlite:"+src, "-to", pgURL, "-batch", "7")
	wantOK(t, code, out)
}

// Text Postgres cannot store fails with its location instead of a bare
// driver error.
func TestPostgresRejectsNULText(t *testing.T) {
	_, pgURL := pgTarget(t)
	src := filepath.Join(t.TempDir(), "src.db")
	newSource(t, src, srcMigrations())
	db := openWriter(t, src)
	mustExec(t, db, `UPDATE nodes SET name = CAST(X'610062' AS TEXT) WHERE public_key = 'ff01'`)
	db.Close()
	code, out := run(t, "copy", "-from", "sqlite:"+src, "-to", pgURL)
	wantFail(t, code, out, `table nodes, row public_key="ff01", column name`, "NUL byte")
}

// A source missing a migration name pgschema seeds still copies: the extra
// seeded bookkeeping row on the target is reported and tolerated.
func TestPostgresSeededBookkeepingTolerated(t *testing.T) {
	_, pgURL := pgTarget(t)
	src := filepath.Join(t.TempDir(), "src.db")
	newSource(t, src, pgSeedMigrations[1:])
	code, out := run(t, "copy", "-from", "sqlite:"+src, "-to", pgURL)
	wantOK(t, code, out)
	if !strings.Contains(out, "b also holds 1 schema-seeded bookkeeping row(s) a lacks; tolerated") {
		t.Errorf("tolerated seed row not reported")
	}
}

// ── -allow-pending-migrations ────────────────────────────────────────────

const allowWarning = "WARNING: -allow-pending-migrations"

func TestMissingAsyncTableNeedsFlag(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.db"), filepath.Join(dir, "dst.db")
	newSource(t, src, srcMigrations())
	db := openWriter(t, src)
	mustExec(t, db, `DROP TABLE _async_migrations`)
	db.Close()

	code, out := run(t, "copy", "-from", "sqlite:"+src, "-to", "sqlite:"+dst)
	wantFail(t, code, out, "has no _async_migrations table", "start the ingestor once on the source",
		"or pass -allow-pending-migrations")
	if _, err := os.Stat(dst + ".partial"); !os.IsNotExist(err) {
		t.Errorf("work file created for a refused source")
	}

	code, out = run(t, "copy", "-allow-pending-migrations", "-from", "sqlite:"+src, "-to", "sqlite:"+dst)
	wantOK(t, code, out)
	for _, s := range []string{allowWarning, "has no _async_migrations table", "will NOT run those backfills",
		"source has no _async_migrations; the target keeps its own rows"} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q", s)
		}
	}
	code, out = run(t, "verify", "-a", "sqlite:"+src, "-b", "sqlite:"+dst)
	wantOK(t, code, out)
}

// A data table and a nullable column the source never got are read as empty
// and NULL with the flag; a missing NOT NULL column still fails.
func TestAbsentTableAndColumnNeedFlag(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.db"), filepath.Join(dir, "dst.db")
	newSource(t, src, srcMigrations())
	db := openWriter(t, src)
	mustExec(t, db, `DROP TABLE scope_match_totals`)
	mustExec(t, db, `ALTER TABLE transmissions DROP COLUMN code2`)
	db.Close()

	code, out := run(t, "copy", "-from", "sqlite:"+src, "-to", "sqlite:"+dst)
	wantFail(t, code, out, "source has no table scope_match_totals", "pass -allow-pending-migrations")
	if _, err := os.Stat(dst + ".partial"); !os.IsNotExist(err) {
		t.Fatalf("a refused copy left %s.partial behind", dst)
	}

	code, out = run(t, "copy", "-allow-pending-migrations", "-from", "sqlite:"+src, "-to", "sqlite:"+dst)
	wantOK(t, code, out)
	for _, s := range []string{
		"source has no table scope_match_totals (the migration creating it never ran); the target table stays empty",
		"source table transmissions has no column code2 (the migration adding it never ran); it is copied as NULL",
		"verify transmissions: side a has no column code2; compared as NULL",
		"table is missing on side a; b's table is empty, so they agree",
	} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q", s)
		}
	}
	code, out = run(t, "verify", "-a", "sqlite:"+src, "-b", "sqlite:"+dst)
	wantOK(t, code, out)

	// A value b holds where a has no column is a real difference.
	db = openWriter(t, dst)
	mustExec(t, db, `UPDATE transmissions SET code2 = 'x' WHERE id = 2`)
	mustExec(t, db, `INSERT INTO scope_match_totals VALUES (1, 1, 1, 1, 1, 1, 1)`)
	db.Close()
	code, out = run(t, "verify", "-a", "sqlite:"+src, "-b", "sqlite:"+dst)
	wantFail(t, code, out, "first difference at id=2: column code2 is NULL on side a", "MISMATCH rows a=0 b=1 — table is missing on side a")

	src2, dst2 := filepath.Join(dir, "src2.db"), filepath.Join(dir, "dst2.db")
	newSource(t, src2, srcMigrations())
	db = openWriter(t, src2)
	mustExec(t, db, `ALTER TABLE client_rx_observations DROP COLUMN hop_count`)
	db.Close()
	code, out = run(t, "copy", "-allow-pending-migrations", "-from", "sqlite:"+src2, "-to", "sqlite:"+dst2)
	wantFail(t, code, out, "source table client_rx_observations lacks column hop_count, which is NOT NULL in the target")
}

func TestPendingAsyncRowNeedsFlag(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.db"), filepath.Join(dir, "dst.db")
	newSource(t, src, srcMigrations())
	db := openWriter(t, src)
	mustExec(t, db, `UPDATE _async_migrations SET status = 'pending_async' WHERE name = 'tx_last_seen_backfill_v1'`)
	db.Close()

	code, out := run(t, "copy", "-from", "sqlite:"+src, "-to", "sqlite:"+dst)
	wantFail(t, code, out, "unfinished async migrations: tx_last_seen_backfill_v1 (pending_async)", "or pass -allow-pending-migrations")

	code, out = run(t, "copy", "-allow-pending-migrations", "-from", "sqlite:"+src, "-to", "sqlite:"+dst)
	wantOK(t, code, out)
	for _, s := range []string{allowWarning, "tx_last_seen_backfill_v1 (pending_async)",
		"a has 1 unfinished async migration row(s) that copy does not carry over; tolerated"} {
		if !strings.Contains(out, s) {
			t.Errorf("output lacks %q", s)
		}
	}
	// The pending row is not copied: the SQLite copy's ingestor runs it.
	db = openWriter(t, dst)
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM _async_migrations WHERE name = 'tx_last_seen_backfill_v1'`).Scan(&n)
	if n != 0 {
		t.Errorf("pending row copied to the target")
	}
}

func TestPendingAsyncRowKeepsPostgresSeed(t *testing.T) {
	pg, pgURL := pgTarget(t)
	src := filepath.Join(t.TempDir(), "src.db")
	newSource(t, src, srcMigrations())
	db := openWriter(t, src)
	mustExec(t, db, `UPDATE _async_migrations SET status = 'pending_async' WHERE name = 'tx_last_seen_backfill_v1'`)
	db.Close()

	code, out := run(t, "copy", "-allow-pending-migrations", "-from", "sqlite:"+src, "-to", pgURL)
	wantOK(t, code, out)
	var status string
	pg.QueryRow(`SELECT status FROM _async_migrations WHERE name = 'tx_last_seen_backfill_v1'`).Scan(&status)
	if status != "done" {
		t.Errorf("Postgres seed overwritten: status = %q, want done", status)
	}
	code, out = run(t, "verify", "-a", "sqlite:"+src, "-b", pgURL)
	wantOK(t, code, out)
}

// prepareE2EFixture is CI's path: a temp copy of the committed E2E fixture
// migrated with the legacy `-db` mode.
func prepareE2EFixture(t *testing.T) string {
	src := filepath.Join(t.TempDir(), "e2e.db")
	copyFile(t, locateFixture(t), src)
	if err := legacyMigrate(src, t.Logf); err != nil {
		t.Fatalf("legacy -db migration: %v", err)
	}
	return src
}

func TestCopyE2EFixtureToPostgres(t *testing.T) {
	_, pgURL := pgTarget(t)
	src := prepareE2EFixture(t)
	code, out := run(t, "copy", "-from", "sqlite:"+src, "-to", pgURL)
	wantFail(t, code, out, "or pass -allow-pending-migrations")

	code, out = run(t, "copy", "-allow-pending-migrations", "-from", "sqlite:"+src, "-to", pgURL)
	wantOK(t, code, out)
	if !strings.Contains(out, allowWarning) {
		t.Errorf("no warning for the fixture's missing async migrations")
	}
	code, out = run(t, "verify", "-a", "sqlite:"+src, "-b", pgURL)
	wantOK(t, code, out)
}

func TestCopyE2EFixtureToSQLite(t *testing.T) {
	src := prepareE2EFixture(t)
	dst := filepath.Join(t.TempDir(), "copy.db")
	code, out := run(t, "copy", "-allow-pending-migrations", "-from", "sqlite:"+src, "-to", "sqlite:"+dst)
	wantOK(t, code, out)
	code, out = run(t, "verify", "-a", "sqlite:"+src, "-b", "sqlite:"+dst)
	wantOK(t, code, out)
}

// ── file and schema safety ───────────────────────────────────────────────

func TestDanglingSymlinkRefused(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.db"), filepath.Join(dir, "dst.db")
	newSource(t, src, srcMigrations())
	elsewhere := filepath.Join(dir, "elsewhere.db")

	for _, link := range []string{dst + ".partial", dst + ".partial-wal", dst} {
		if err := os.Symlink(elsewhere, link); err != nil {
			t.Fatal(err)
		}
		code, out := run(t, "copy", "-from", "sqlite:"+src, "-to", "sqlite:"+dst)
		wantFail(t, code, out)
		if _, err := os.Lstat(elsewhere); !os.IsNotExist(err) {
			t.Fatalf("copy wrote through the dangling symlink %s", link)
		}
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTargetAppearingBeforePublishIsKept(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "src.db"), filepath.Join(dir, "dst.db")
	newSource(t, src, srcMigrations())
	beforePublish = func() {
		if err := os.WriteFile(dst, []byte("theirs"), 0o644); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() { beforePublish = nil })

	code, out := run(t, "copy", "-from", "sqlite:"+src, "-to", "sqlite:"+dst)
	wantFail(t, code, out, "appeared during the copy; it was not touched",
		"complete and verified in "+dst+".partial but was NOT published", "mv -n")
	if b, _ := os.ReadFile(dst); string(b) != "theirs" {
		t.Errorf("existing target overwritten")
	}
	// The verified work file is intact and usable.
	code, out = run(t, "verify", "-a", "sqlite:"+src, "-b", "sqlite:"+dst+".partial")
	wantOK(t, code, out)
}

func TestCleanupCommandIsShellQuoted(t *testing.T) {
	if got := shellQuote("/tmp/it's here"); got != `'/tmp/it'\''s here'` {
		t.Errorf("shellQuote = %s", got)
	}
	dir := filepath.Join(t.TempDir(), "it's")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	src, dst := filepath.Join(dir, "src.db"), filepath.Join(dir, "dst.db")
	newSource(t, src, srcMigrations())
	if err := os.WriteFile(dst+".partial", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out := run(t, "copy", "-from", "sqlite:"+src, "-to", "sqlite:"+dst)
	wantFail(t, code, out, "rm -f "+shellQuote(dst+".partial")+" ")
	if !strings.Contains(out, `it'\''s`) {
		t.Errorf("embedded quote not escaped")
	}
}

func TestUsageHasNoPasswordURLs(t *testing.T) {
	for _, cmd := range []string{"copy", "verify"} {
		code, out := run(t, cmd)
		if code != 2 {
			t.Errorf("%s with no flags: exit %d, want 2", cmd, code)
		}
		for _, s := range []string{"PGPASSWORD", ".pgpass", "postgres://corescope@db.example/corescope"} {
			if !strings.Contains(out, s) {
				t.Errorf("%s usage lacks %q", cmd, s)
			}
		}
		if m := regexp.MustCompile(`postgres(ql)?://[^\s/@]*:[^\s/@]*@`).FindString(out); m != "" {
			t.Errorf("%s usage shows a URL with a password: %s", cmd, m)
		}
	}
}

func TestPostgresForeignSchemaRefused(t *testing.T) {
	pg, pgURL := pgTarget(t)
	mustExec(t, pg, `CREATE TABLE someone_elses (id BIGINT PRIMARY KEY)`)
	src := filepath.Join(t.TempDir(), "src.db")
	newSource(t, src, srcMigrations())

	code, out := run(t, "copy", "-from", "sqlite:"+src, "-to", pgURL)
	wantFail(t, code, out, "holds 1 tables or views but no CoreScope schema_version", "Refusing to create anything")
	for _, tbl := range []string{"schema_version", "transmissions"} {
		var n int
		pg.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = ?`, tbl).Scan(&n)
		if n != 0 {
			t.Errorf("%s was created in a foreign schema", tbl)
		}
	}
}
