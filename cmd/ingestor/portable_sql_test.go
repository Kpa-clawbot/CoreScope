package main

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// Pins for the portable rewrites of the ingestor's runtime SQL (Postgres
// support). Each test asserts the rewritten statement leaves SQLite with the
// rows the SQLite-only original produced.

// sqlValues is the input matrix for the MAX/MIN rewrites: NULL, the empty
// string COALESCE substitutes, both timestamp layouts the columns have held,
// and integers, which SQLite orders before any text.
var sqlValues = []any{nil, "", "a", "b", "2026-01-01T00:00:00Z", "2026-01-01 00:00:00", "2099-01-01T00:00:00Z", int64(1), int64(2)}

func openMemDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func loadValues(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	if _, err := db.Exec(`CREATE TABLE ` + table + ` (v)`); err != nil {
		t.Fatal(err)
	}
	for _, v := range sqlValues {
		if _, err := db.Exec(`INSERT INTO `+table+` (v) VALUES (?)`, v); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSQLMaxMin2MatchSQLite(t *testing.T) {
	db := openMemDB(t)
	loadValues(t, db, "a")
	loadValues(t, db, "b")
	rows, err := db.Query(`SELECT quote(a.v), quote(b.v),
		quote(MAX(a.v, b.v)), quote(` + sqlMax2("a.v", "b.v") + `),
		quote(MIN(a.v, b.v)), quote(` + sqlMin2("a.v", "b.v") + `)
		FROM a, b`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var a, b, wantMax, gotMax, wantMin, gotMin string
		if err := rows.Scan(&a, &b, &wantMax, &gotMax, &wantMin, &gotMin); err != nil {
			t.Fatal(err)
		}
		if gotMax != wantMax {
			t.Errorf("max(%s, %s): got %s, SQLite gives %s", a, b, gotMax, wantMax)
		}
		if gotMin != wantMin {
			t.Errorf("min(%s, %s): got %s, SQLite gives %s", a, b, gotMin, wantMin)
		}
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n != len(sqlValues)*len(sqlValues) {
		t.Fatalf("compared %d pairs, want %d", n, len(sqlValues)*len(sqlValues))
	}
}

// The column holds text; the parameters are what Go binds (string or nil).
func TestClampedMaxMatchesSQLite(t *testing.T) {
	db := openMemDB(t)
	loadValues(t, db, "c")
	params := []any{nil, "", "a", "m", "2026-01-01T00:00:00Z", "2099-01-01T00:00:00Z"}
	for _, hi := range params {
		for _, lo := range params {
			rows, err := db.Query(`SELECT quote(v), quote(MAX(MIN(COALESCE(v, ''), ?), ?)), quote(`+
				clampedMaxSQL("c.v")+`) FROM c`, hi, lo, hi, lo)
			if err != nil {
				t.Fatal(err)
			}
			for rows.Next() {
				var v, want, got string
				if err := rows.Scan(&v, &want, &got); err != nil {
					t.Fatal(err)
				}
				if got != want {
					t.Errorf("col=%s hi=%v lo=%v: got %s, SQLite gives %s", v, hi, lo, got, want)
				}
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
		}
	}
}

func TestSQLiteDatetimeNowMatchesEngine(t *testing.T) {
	db := openMemDB(t)
	for i := 0; i < 3; i++ {
		var engine string
		if err := db.QueryRow(`SELECT datetime('now')`).Scan(&engine); err != nil {
			t.Fatal(err)
		}
		ours := sqliteDatetimeNow()
		if engine == ours {
			return
		}
		// A second boundary fell between the two reads; try again.
		if len(engine) != len(ours) {
			t.Fatalf("layout differs: engine %q, ours %q", engine, ours)
		}
	}
	t.Fatal("sqliteDatetimeNow never matched datetime('now')")
}

func tableColumns(t *testing.T, db *sql.DB, table string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	sort.Strings(cols)
	return cols
}

func TestNodeColumnsMatchSchema(t *testing.T) {
	s := newTestStore(t)
	want := append([]string(nil), nodeColumns...)
	sort.Strings(want)
	for _, table := range []string{"nodes", "inactive_nodes"} {
		if got := tableColumns(t, s.db, table); !reflect.DeepEqual(got, want) {
			t.Errorf("%s columns %v, nodeColumns %v: MoveStaleNodes would drop or fail on the difference", table, got, want)
		}
	}
}

func selectRow(t *testing.T, db *sql.DB, table, key string) []any {
	t.Helper()
	vals := make([]any, len(nodeColumns))
	ptrs := make([]any, len(vals))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	quoted := make([]string, len(nodeColumns))
	for i, c := range nodeColumns {
		quoted[i] = "quote(" + c + ")"
	}
	if err := db.QueryRow(`SELECT `+strings.Join(quoted, ", ")+` FROM `+table+` WHERE public_key = ?`, key).Scan(ptrs...); err != nil {
		t.Fatalf("read %s %s: %v", table, key, err)
	}
	return vals
}

// MoveStaleNodes must carry every column across, by name, and replace a row
// already in inactive_nodes for the same key (the INSERT OR REPLACE semantics).
func TestMoveStaleNodesCopiesEveryColumn(t *testing.T) {
	s := newTestStore(t)
	const pk = "deadbeef1234567890abcdef12345678"
	// Reorder inactive_nodes' columns: a positional SELECT * would scramble them.
	reversed := make([]string, len(nodeColumns))
	for i, c := range nodeColumns {
		reversed[len(nodeColumns)-1-i] = c
	}
	var ddl []string
	for _, c := range reversed {
		switch c {
		case "public_key":
			ddl = append(ddl, c+" TEXT PRIMARY KEY")
		case "multibyte_sup":
			ddl = append(ddl, c+" INTEGER NOT NULL DEFAULT 0")
		default:
			ddl = append(ddl, c)
		}
	}
	if _, err := s.db.Exec(`DROP TABLE inactive_nodes`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TABLE inactive_nodes (` + strings.Join(ddl, ", ") + `)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO inactive_nodes (public_key, name, advert_count) VALUES (?, 'stale copy', 99)`, pk); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen,
		advert_count, battery_mv, temperature_c, foreign_advert, default_scope, configured_scope,
		configured_scope_at, multibyte_sup, multibyte_evidence)
		VALUES (?, 'Old', 'repeater', 1.5, -2.25, '2020-01-02T00:00:00Z', '2019-01-01T00:00:00Z',
		7, 3700, 21.5, 1, 'de', 'de,fr', '2020-01-01T00:00:00Z', 2, '{"x":1}')`, pk); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO nodes (public_key, last_seen) VALUES ('fresh', '2099-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	want := selectRow(t, s.db, "nodes", pk)

	moved, err := s.MoveStaleNodes(7)
	if err != nil {
		t.Fatal(err)
	}
	if moved != 1 {
		t.Fatalf("moved %d, want 1", moved)
	}
	if got := selectRow(t, s.db, "inactive_nodes", pk); !reflect.DeepEqual(got, want) {
		t.Errorf("inactive_nodes row\n got %v\nwant %v", got, want)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM inactive_nodes`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("inactive_nodes rows = %d (err %v), want 1", n, err)
	}
}

// The transmission id now comes from RETURNING, not LastInsertId; the
// observation must still reference the row the insert created.
func TestInsertTransmissionReturningID(t *testing.T) {
	forEachBackendSQL(t, func(t *testing.T, s *Store) {
		if err := s.UpsertObserver("obs-1", "Observer", "", nil); err != nil {
			t.Fatal(err)
		}
		// Leave a gap in the id sequence so a wrong id cannot match by accident.
		if _, err := s.db.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen) VALUES ('00', 'gap', '2026-01-01T00:00:00Z')`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`DELETE FROM transmissions WHERE hash = 'gap'`); err != nil {
			t.Fatal(err)
		}
		for _, hash := range []string{"h-one", "h-two"} {
			isNew, err := s.InsertTransmission(&PacketData{Hash: hash, RawHex: "1100aa", PayloadType: 2, ObserverID: "obs-1", PathJSON: "[]", Timestamp: "2026-01-02T00:00:00Z"})
			if err != nil || !isNew {
				t.Fatalf("insert %s: new=%v err=%v", hash, isNew, err)
			}
			var txID, obsTx int64
			if err := s.db.QueryRow(`SELECT id FROM transmissions WHERE hash = ?`, hash).Scan(&txID); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow(`SELECT transmission_id FROM observations o JOIN transmissions t ON t.id = o.transmission_id WHERE t.hash = ?`, hash).Scan(&obsTx); err != nil {
				t.Fatalf("observation for %s does not reference its transmission: %v", hash, err)
			}
			if obsTx != txID {
				t.Fatalf("%s: observation.transmission_id %d, transmissions.id %d", hash, obsTx, txID)
			}
		}
	})
}

func TestObservationRadioUpsertReplaces(t *testing.T) {
	s := newTestStore(t)
	s.recordObservationRadio(42, "radio-a")
	s.recordObservationRadio(42, "radio-b")
	s.recordObservationRadio(43, "radio-a")
	rows, err := s.db.Query(`SELECT observation_id, radio_id FROM observation_radios ORDER BY observation_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var id int64
		var radio string
		if err := rows.Scan(&id, &radio); err != nil {
			t.Fatal(err)
		}
		got = append(got, radio)
	}
	if want := []string{"radio-b", "radio-a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("observation_radios %v, want %v", got, want)
	}
	if s.Stats.WriteErrors.Load() != 0 {
		t.Fatalf("write errors: %d", s.Stats.WriteErrors.Load())
	}
}

// A second sample for the same (observer, timestamp) replaces every column,
// NULLs included, as INSERT OR REPLACE did.
func TestObserverMetricsUpsertReplacesAllColumns(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.stmtUpsertMetrics.Exec("obs-1", "2026-01-01T00:00:00Z", -110.5, 1, 2, 3, 3700, 4, 5); err != nil {
		t.Fatal(err)
	}
	if _, err := s.stmtUpsertMetrics.Exec("obs-1", "2026-01-01T00:00:00Z", nil, 10, nil, nil, nil, nil, 50); err != nil {
		t.Fatal(err)
	}
	var n int
	var row string
	if err := s.db.QueryRow(`SELECT COUNT(*), group_concat(quote(noise_floor) || ',' || quote(tx_air_secs) || ',' || quote(rx_air_secs) || ',' ||
		quote(recv_errors) || ',' || quote(battery_mv) || ',' || quote(packets_sent) || ',' || quote(packets_recv))
		FROM observer_metrics`).Scan(&n, &row); err != nil {
		t.Fatal(err)
	}
	if n != 1 || row != "NULL,10,NULL,NULL,NULL,NULL,50" {
		t.Fatalf("observer_metrics: %d row(s) %q, want 1 row NULL,10,NULL,NULL,NULL,NULL,50", n, row)
	}
}

// Starting a new run over an old one resets every column, started_at
// included: REPLACE deleted the row, so the default applied again.
func TestKeyBackfillStartReplacesState(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`INSERT INTO key_backfill_state (kind, run_fp, cursor, upper, rewritten, started_at) VALUES ('channel', 'old', 5, 9, 3, 1)`); err != nil {
		t.Fatal(err)
	}
	b := &keyBackfiller{store: s}
	cursor, upper, rewritten, err := b.resumeOrStart(context.Background(), "channel", "new")
	if err != nil {
		t.Fatal(err)
	}
	if cursor != 0 || upper != 0 || rewritten != 0 {
		t.Fatalf("resumeOrStart = %d,%d,%d, want 0,0,0", cursor, upper, rewritten)
	}
	var fp string
	var c, u, r, started int64
	if err := s.db.QueryRow(`SELECT run_fp, cursor, upper, rewritten, started_at FROM key_backfill_state WHERE kind = 'channel'`).Scan(&fp, &c, &u, &r, &started); err != nil {
		t.Fatal(err)
	}
	if fp != "new" || c != 0 || u != 0 || r != 0 || started < time.Now().Add(-time.Hour).Unix() {
		t.Fatalf("state = %s,%d,%d,%d started_at=%d, want new,0,0,0 started_at≈now", fp, c, u, r, started)
	}
	// The same fingerprint resumes without touching the row.
	if _, err := s.db.Exec(`UPDATE key_backfill_state SET cursor = 4 WHERE kind = 'channel'`); err != nil {
		t.Fatal(err)
	}
	if cursor, _, _, err = b.resumeOrStart(context.Background(), "channel", "new"); err != nil || cursor != 4 {
		t.Fatalf("resume cursor=%d err=%v, want 4", cursor, err)
	}
}

// Recording a key twice keeps the first record, as INSERT OR IGNORE did.
func TestFinishRunIgnoresRecordedKeys(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := finishRun(ctx, s.db, "channel", "fp", []string{"k1", "k2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE key_backfill_done SET done_at = 1`); err != nil {
		t.Fatal(err)
	}
	if err := finishRun(ctx, s.db, "channel", "fp", []string{"k1", "k2", "k3"}); err != nil {
		t.Fatal(err)
	}
	var n, old int
	if err := s.db.QueryRow(`SELECT COUNT(*), SUM(done_at = 1) FROM key_backfill_done`).Scan(&n, &old); err != nil {
		t.Fatal(err)
	}
	if n != 3 || old != 2 {
		t.Fatalf("key_backfill_done rows=%d kept=%d, want 3 rows with the 2 originals kept", n, old)
	}
}

func TestNeighborEdgeUpsertKeepsSQLiteMaxSemantics(t *testing.T) {
	s := newTestStore(t)
	for _, tc := range []struct {
		existing, incoming any
		want               string
	}{
		{nil, "2026-01-01T00:00:00Z", "NULL"}, // two-arg MAX is NULL when either side is
		{"2026-01-02T00:00:00Z", "2026-01-01T00:00:00Z", "'2026-01-02T00:00:00Z'"},
		{"2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z", "'2026-01-02T00:00:00Z'"},
		{"", "2026-01-01T00:00:00Z", "'2026-01-01T00:00:00Z'"},
	} {
		if _, err := s.db.Exec(`DELETE FROM neighbor_edges`); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(`INSERT INTO neighbor_edges (node_a, node_b, count, last_seen) VALUES ('a', 'b', 4, ?)`, tc.existing); err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.Exec(neighborEdgeUpsertSQL, "a", "b", tc.incoming); err != nil {
			t.Fatal(err)
		}
		var count int
		var last string
		if err := s.db.QueryRow(`SELECT count, quote(last_seen) FROM neighbor_edges`).Scan(&count, &last); err != nil {
			t.Fatal(err)
		}
		if count != 5 || last != tc.want {
			t.Errorf("existing=%v incoming=%v: count=%d last_seen=%s, want 5 %s", tc.existing, tc.incoming, count, last, tc.want)
		}
	}
}

func TestAsyncMigrationTimestampsUseSQLiteLayout(t *testing.T) {
	s := newTestStore(t)
	const name = "portable_ts_v1"
	if err := s.RunAsyncMigration(context.Background(), name, func(context.Context, *sql.DB) error { return nil }); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, s, name, "done", 2*time.Second)
	// A retry resets started_at through the runtime UPDATE.
	if _, err := s.db.Exec(`UPDATE _async_migrations SET status = 'failed', started_at = 'x' WHERE name = ?`, name); err != nil {
		t.Fatal(err)
	}
	if err := s.RunAsyncMigration(context.Background(), name, func(context.Context, *sql.DB) error { return nil }); err != nil {
		t.Fatal(err)
	}
	waitForStatus(t, s, name, "done", 2*time.Second)
	s.backfillWg.Wait()
	var started, ended, engine string
	if err := s.db.QueryRow(`SELECT started_at, ended_at, datetime('now') FROM _async_migrations WHERE name = ?`, name).Scan(&started, &ended, &engine); err != nil {
		t.Fatal(err)
	}
	layout := regexp.MustCompile(`^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d$`)
	for _, v := range []string{started, ended} {
		if !layout.MatchString(v) || v > engine {
			t.Errorf("timestamp %q: want datetime('now') layout, not after %q", v, engine)
		}
	}
}

func TestAdvertEvidenceInsertBindsIntegers(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.db.Exec(`INSERT INTO transmissions (raw_hex, hash, first_seen) VALUES ('1100aa', 'ev', '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	var txID int64
	if err := s.db.QueryRow(`SELECT id FROM transmissions WHERE hash = 'ev'`).Scan(&txID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if _, err := s.stmtInsertAdvertEvidence.Exec(txID, uint8(1), txID, uint8(1)); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	var types string
	if err := s.db.QueryRow(`SELECT COUNT(*), typeof(tx_id) || ',' || typeof(bit) FROM advert_route_evidence`).Scan(&n, &types); err != nil {
		t.Fatal(err)
	}
	if n != 1 || types != "integer,integer" {
		t.Fatalf("advert_route_evidence rows=%d types=%s, want 1 integer,integer", n, types)
	}
}

func TestObserverCanRelaySeen(t *testing.T) {
	forEachBackendSQL(t, func(t *testing.T, s *Store) {
		relayOff := false
		relayOn := true
		read := func() (canRelay, seen int) {
			t.Helper()
			if err := s.db.QueryRow(`SELECT can_relay, can_relay_seen FROM observers WHERE id = 'o'`).Scan(&canRelay, &seen); err != nil {
				t.Fatal(err)
			}
			return
		}
		steps := []struct {
			apply           func() error
			relay, wantSeen int
		}{
			{func() error { return s.UpsertObserver("o", "O", "", nil) }, 1, 0},
			{func() error { return s.UpsertObserver("o", "O", "", &ObserverMeta{}) }, 1, 0},
			{func() error { return s.UpsertObserver("o", "O", "", &ObserverMeta{CanRelay: &relayOff}) }, 0, 1},
			{func() error { return s.UpsertObserver("o", "O", "", nil) }, 0, 1},
			{func() error { return s.UpsertObserverRetained("o", "O", "", &ObserverMeta{CanRelay: &relayOn}) }, 1, 1},
			{func() error { return s.UpsertObserverRetained("o", "O", "", nil) }, 1, 1},
		}
		for i, st := range steps {
			if err := st.apply(); err != nil {
				t.Fatal(err)
			}
			if relay, seen := read(); relay != st.relay || seen != st.wantSeen {
				t.Fatalf("step %d: can_relay=%d can_relay_seen=%d, want %d %d", i, relay, seen, st.relay, st.wantSeen)
			}
		}
		// A brand-new observer that reports the flag is seen from its first row.
		if err := s.UpsertObserver("p", "P", "", &ObserverMeta{CanRelay: &relayOff}); err != nil {
			t.Fatal(err)
		}
		var relay, seen int
		if err := s.db.QueryRow(`SELECT can_relay, can_relay_seen FROM observers WHERE id = 'p'`).Scan(&relay, &seen); err != nil || relay != 0 || seen != 1 {
			t.Fatalf("new observer can_relay=%d seen=%d err=%v, want 0 1", relay, seen, err)
		}
	})
}

// The clamp in the node and observer upserts: a future value left by an old
// bug is pulled back to ingest time, and last_seen otherwise never moves
// backwards.
func TestLastSeenClampOnUpserts(t *testing.T) {
	forEachBackendSQL(t, func(t *testing.T, s *Store) {
		const pk = "aabbccdd1234567890abcdef12345678"
		if err := s.UpsertNode(pk, "N", "repeater", nil, nil, "2026-01-02T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
		if err := s.UpsertNode(pk, "N", "repeater", nil, nil, "2026-01-01T00:00:00Z"); err != nil {
			t.Fatal(err)
		}
		var ls string
		if err := s.db.QueryRow(`SELECT last_seen FROM nodes WHERE public_key = ?`, pk).Scan(&ls); err != nil || ls != "2026-01-02T00:00:00Z" {
			t.Fatalf("node last_seen went backwards: %q err=%v", ls, err)
		}
		for _, table := range []string{"nodes", "observers"} {
			key := map[string]string{"nodes": "public_key", "observers": "id"}[table]
			id := map[string]string{"nodes": pk, "observers": "o"}[table]
			if table == "observers" {
				if err := s.UpsertObserverAt("o", "O", "", nil, "2026-01-01T00:00:00Z"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.db.Exec(`UPDATE `+table+` SET last_seen = '2999-01-01T00:00:00Z' WHERE `+key+` = ?`, id); err != nil {
				t.Fatal(err)
			}
			var err error
			if table == "nodes" {
				err = s.UpsertNode(pk, "", "", nil, nil, "2026-01-03T00:00:00Z")
			} else {
				err = s.UpsertObserverAt("o", "", "", nil, "2026-01-03T00:00:00Z")
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow(`SELECT last_seen FROM `+table+` WHERE `+key+` = ?`, id).Scan(&ls); err != nil {
				t.Fatal(err)
			}
			if ls >= "2999" || ls < "2026-01-03T00:00:00Z" {
				t.Errorf("%s last_seen = %q, want clamped to ingest time", table, ls)
			}
		}
	})
}

// The multibyte probe now goes through dbschema.TableHasColumn; on SQLite it
// must answer what the PRAGMA table_info scan answered.
func TestHasMultibyteSupColumnsProbe(t *testing.T) {
	s := newTestStore(t)
	if !s.hasMultibyteSupColumns() {
		t.Fatal("hasMultibyteSupColumns = false on a migrated store")
	}
	legacy := &Store{db: openMemDB(t)}
	if _, err := legacy.db.Exec(`CREATE TABLE nodes (public_key TEXT PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatal(err)
	}
	if legacy.hasMultibyteSupColumns() {
		t.Fatal("hasMultibyteSupColumns = true on a nodes table without the column")
	}
	if missing := (&Store{db: openMemDB(t)}); missing.hasMultibyteSupColumns() {
		t.Fatal("hasMultibyteSupColumns = true with no nodes table")
	}
}

// forEachBackendSQL runs fn on a SQLite store prepared with each backend's
// statements. The Postgres forms are portable, so SQLite can check that they
// leave the same rows as the SQLite forms.
func forEachBackendSQL(t *testing.T, fn func(t *testing.T, s *Store)) {
	for _, pg := range []bool{false, true} {
		t.Run(map[bool]string{false: "sqlite_sql", true: "postgres_sql"}[pg], func(t *testing.T) {
			s := newTestStore(t)
			if pg {
				s.pg = true
				if err := s.prepareStatements(); err != nil {
					t.Fatal(err)
				}
			}
			fn(t, s)
		})
	}
}

// recordingDriver records the SQL each Prepare receives, so a test can see
// exactly what prepareStatements prepares for a backend.
type recordingDriver struct{ prepared *[]string }
type recordingConn struct{ prepared *[]string }
type recordingStmt struct{}

func (d recordingDriver) Open(string) (driver.Conn, error) { return recordingConn(d), nil }
func (c recordingConn) Prepare(q string) (driver.Stmt, error) {
	*c.prepared = append(*c.prepared, q)
	return recordingStmt{}, nil
}
func (recordingConn) Close() error              { return nil }
func (recordingConn) Begin() (driver.Tx, error) { return nil, errors.New("not supported") }
func (recordingStmt) Close() error              { return nil }
func (recordingStmt) NumInput() int             { return -1 }
func (recordingStmt) Exec([]driver.Value) (driver.Result, error) {
	return nil, errors.New("not supported")
}
func (recordingStmt) Query([]driver.Value) (driver.Rows, error) {
	return nil, errors.New("not supported")
}

func preparedSQL(t *testing.T, pg bool) string {
	t.Helper()
	var prepared []string
	db := sql.OpenDB(recordingConnector{recordingDriver{&prepared}})
	defer db.Close()
	s := &Store{db: db, pg: pg}
	if err := s.prepareStatements(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(prepared, "\n;\n")
}

type recordingConnector struct{ d recordingDriver }

func (c recordingConnector) Connect(context.Context) (driver.Conn, error) { return c.d.Open("") }
func (c recordingConnector) Driver() driver.Driver                        { return c.d }

// The SQLite hot path keeps its original SQL byte for byte: the portable forms
// cost it ~17% CPU (BenchmarkInsertTransmissionHotPath). Do not "simplify" the
// per-backend choice away without re-running that benchmark.
func TestSQLiteHotPathSQLUnchanged(t *testing.T) {
	const originalInsert = "\n\t\tINSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json, channel_hash, scope_name, from_pubkey, last_seen, code1, code2)\n\t\tVALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)\n\t"
	originalClamps := []string{
		"last_seen = MAX(MIN(COALESCE(last_seen, ''), ?), ?)",           // nodes upsert
		"last_seen = MAX(MIN(COALESCE(last_seen, ''), ?), ?),",          // observers upsert
		"last_seen      = MAX(MIN(COALESCE(last_seen, ''), ?), ?),",     // observers UPDATE
		"last_packet_at = MAX(MIN(COALESCE(last_packet_at, ''), ?), ?)", // observers UPDATE
	}

	sqlite := preparedSQL(t, false)
	if !strings.Contains(sqlite, "\n;\n"+originalInsert+"\n;\n") {
		t.Errorf("SQLite transmissions insert is not the original statement")
	}
	for _, c := range originalClamps {
		if !strings.Contains(sqlite, c) {
			t.Errorf("SQLite statements lack the original clamp %q", c)
		}
	}
	if n := strings.Count(sqlite, "MAX(MIN(COALESCE("); n != 4 {
		t.Errorf("SQLite statements carry %d original clamps, want 4", n)
	}
	if strings.Contains(sqlite, "AS clamp)") || strings.Contains(sqlite, originalInsert+"RETURNING") {
		t.Errorf("SQLite statements carry a Postgres form")
	}

	pg := preparedSQL(t, true)
	if strings.Contains(pg, "MAX(MIN(") {
		t.Errorf("Postgres statements carry SQLite's two-argument MAX/MIN")
	}
	if n := strings.Count(pg, "AS clamp)"); n != 4 {
		t.Errorf("Postgres statements carry %d clampedMaxSQL clamps, want 4", n)
	}
	if !strings.Contains(pg, originalInsert+"RETURNING id\n") {
		t.Errorf("Postgres transmissions insert does not return its id")
	}
}
