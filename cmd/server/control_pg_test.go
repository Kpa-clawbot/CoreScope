package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/dbschema/pgschema"
	"github.com/meshcore-analyzer/prunequeue"
)

// pgControlTestLock serialises every test that touches the cluster-wide
// pgControlRole across processes (two cmd/server runs against one server).
const pgControlTestLock = 0x43534354 // "CSCT"

// pgControlRoleForTest makes pgControlRole exist with exactly the control
// grants for the test's duration. The role name is cluster-wide and fixed
// (pgschema.ControlGrantsOK checks that name), so the test holds an advisory
// lock for its whole run, and drops the role in Cleanup only if it created
// it. Register it before any role that becomes a member, so that role's
// Cleanup (which drops it) runs first.
func pgControlRoleForTest(t *testing.T, seed *sql.DB) {
	t.Helper()
	ctx := context.Background()
	lock, err := seed.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock.ExecContext(ctx, `SELECT pg_advisory_lock(?)`, pgControlTestLock); err != nil {
		lock.Close()
		t.Fatalf("advisory lock: %v", err)
	}
	var existed bool
	if err := seed.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = ?)`, pgControlRole).Scan(&existed); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !existed {
			for _, q := range []string{`DROP OWNED BY ` + pgControlRole, `DROP ROLE ` + pgControlRole} {
				if _, err := seed.Exec(q); err != nil {
					t.Errorf("%s: %v", q, err)
				}
			}
		}
		_, _ = lock.ExecContext(ctx, `SELECT pg_advisory_unlock(?)`, pgControlTestLock)
		lock.Close()
	})
	if !existed {
		if _, err := seed.Exec(`CREATE ROLE ` + pgControlRole + ` NOLOGIN`); err != nil {
			t.Fatalf("create %s: %v", pgControlRole, err)
		}
	}
	if err := pgschema.EnsureControlGrants(ctx, seed); err != nil {
		t.Fatalf("EnsureControlGrants: %v", err)
	}
	if ok, err := pgschema.ControlGrantsOK(ctx, seed); err != nil || !ok {
		t.Fatalf("ControlGrantsOK after EnsureControlGrants: %v %v", ok, err)
	}
}

// pgControlReaderGrants are pgReadOnlyGrants plus membership in the control
// group: what scripts/postgres-roles.sh gives the reader.
var pgControlReaderGrants = append(append([]string{}, pgReadOnlyGrants...), `GRANT `+pgControlRole+` TO {role}`)

// openControlReader opens the server as a reader holding the control
// membership (plus extra grants), closed in Cleanup.
func openControlReader(t *testing.T, seed *sql.DB, extra ...string) *DB {
	t.Helper()
	_, _, dsn := pgTestRole(t, seed, append(append([]string{}, pgControlReaderGrants...), extra...)...)
	d, err := OpenPostgresDB(dsn)
	if err != nil {
		t.Fatalf("OpenPostgresDB with the control membership: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func pgErrCode(t *testing.T, what string, err error, code string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "SQLSTATE "+code) {
		t.Errorf("%s: err = %v, want SQLSTATE %s", what, err, code)
	}
}

func TestPGControlParams(t *testing.T) {
	got := map[string]string{}
	for _, p := range pgControlParams(7) {
		got[p.name] = p.value
	}
	main := map[string]string{}
	for _, p := range pgServerParams(7) {
		main[p.name] = p.value
	}
	if got[pgReadOnlyParam] != "off" || main[pgReadOnlyParam] != "on" {
		t.Fatalf("read-only: control %q, main %q", got[pgReadOnlyParam], main[pgReadOnlyParam])
	}
	for name, v := range main {
		if name != pgReadOnlyParam && got[name] != v {
			t.Errorf("%s: control %q, main %q", name, got[name], v)
		}
	}
	if len(got) != len(main) {
		t.Errorf("control params %v, main %v", got, main)
	}
}

func TestOpenPostgresDBControlMembership(t *testing.T) {
	seed := pgSchemaSeed(t)
	pgControlRoleForTest(t, seed)
	d := openControlReader(t, seed)
	ctx := context.Background()

	if d.ctl == nil || d.pruneQueue() == nil || d.mbcapStore() == nil {
		t.Fatalf("control pool not opened: ctl=%v pruneQ=%v mbcap=%v", d.ctl, d.pruneQ, d.mbcap)
	}
	if got := d.pruneQueue().Location(); got != "postgres:"+pgControlSchema {
		t.Errorf("prune queue location %q", got)
	}
	for pool, want := range map[*sql.DB]string{d.conn: "on", d.ctl: "off"} {
		var got string
		if err := pool.QueryRow(`SHOW default_transaction_read_only`).Scan(&got); err != nil || got != want {
			t.Errorf("default_transaction_read_only = %q (%v), want %q", got, err, want)
		}
	}

	const id = "00000000c0de0001"
	t.Cleanup(func() {
		_, _ = seed.Exec(`DELETE FROM corescope_control.prune_requests WHERE id = ?`, id)
	})
	insertNode := `INSERT INTO nodes (public_key, name) VALUES ('x', 'y')`
	insertReq := `INSERT INTO corescope_control.prune_requests (id, created_at, payload) VALUES ('` + id + `', 'now', '{}')`

	// Main pool: every write fails on the read-only default, control
	// tables included.
	_, err := d.conn.Exec(insertNode)
	pgErrCode(t, "main pool INSERT nodes", err, "25006")
	_, err = d.conn.Exec(insertReq)
	pgErrCode(t, "main pool INSERT prune_requests", err, "25006")

	// Main pool with the default overridden: the role still holds no
	// write privilege on the measurement schema.
	c, err := d.conn.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ExecContext(ctx, `SET default_transaction_read_only = off`); err != nil {
		t.Fatal(err)
	}
	_, err = c.ExecContext(ctx, insertNode)
	pgErrCode(t, "main pool (default overridden) INSERT nodes", err, "42501")
	_, _ = c.ExecContext(ctx, `RESET default_transaction_read_only`)
	c.Close()

	// Control pool: measurement tables refused by privilege; the control
	// tables take INSERT on the two request tables and nothing else.
	_, err = d.ctl.Exec(insertNode)
	pgErrCode(t, "control pool INSERT nodes", err, "42501")
	_, err = d.ctl.Exec(`UPDATE nodes SET name = 'z'`)
	pgErrCode(t, "control pool UPDATE nodes", err, "42501")
	_, err = d.ctl.Exec(`DELETE FROM observations`)
	pgErrCode(t, "control pool DELETE observations", err, "42501")
	_, err = d.ctl.Exec(`INSERT INTO ingestor_stats (id, updated_at, payload) VALUES (1, 'x', '{}')`)
	pgErrCode(t, "control pool INSERT ingestor_stats", err, "42501")
	if _, err := d.ctl.Exec(insertReq); err != nil {
		t.Fatalf("control pool INSERT prune_requests: %v", err)
	}
	_, err = d.ctl.Exec(`UPDATE corescope_control.prune_requests SET payload = '[]' WHERE id = ?`, id)
	pgErrCode(t, "control pool UPDATE prune_requests", err, "42501")
	_, err = d.ctl.Exec(`DELETE FROM corescope_control.prune_requests WHERE id = ?`, id)
	pgErrCode(t, "control pool DELETE prune_requests", err, "42501")
	_, err = d.ctl.Exec(`INSERT INTO corescope_control.prune_results (id, created_at, payload) VALUES (?, 'now', '{}')`, id)
	pgErrCode(t, "control pool INSERT prune_results", err, "42501")

	// The multibyte snapshot goes to the control table.
	const pk = "c0ffee00c0de0001c0ffee00c0de0001"
	t.Cleanup(func() {
		_, _ = seed.Exec(`DELETE FROM corescope_control.mbcap_snapshots WHERE payload LIKE ?`, "%"+pk+"%")
	})
	(&PacketStore{db: d}).publishMultibyteCapSnapshot([]MultiByteCapEntry{{PublicKey: pk, Status: "confirmed", Evidence: "advert"}})
	var n int
	if err := seed.QueryRow(`SELECT count(*) FROM corescope_control.mbcap_snapshots WHERE payload LIKE ?`, "%"+pk+"%").Scan(&n); err != nil || n != 1 {
		t.Errorf("published snapshots holding %s: %d (%v), want 1", pk, n, err)
	}
}

func TestOpenPostgresDBControlMembershipStillRefusesWrites(t *testing.T) {
	cases := []struct {
		name  string
		grant string // run as the seed role; {role} is the reader
		undo  string // run in Cleanup, for grants to the shared control role
		inErr string
	}{
		{"reader with INSERT on a measurement table", `GRANT INSERT ON nodes TO {role}`, "", "nodes"},
		{"reader with UPDATE on ingestor_stats", `GRANT UPDATE ON ingestor_stats TO {role}`, "", "ingestor_stats"},
		{"control role with INSERT on a measurement table",
			`GRANT INSERT ON observers TO ` + pgControlRole, `REVOKE INSERT ON observers FROM ` + pgControlRole,
			"observers (via role " + pgControlRole + ")"},
		{"reader with direct INSERT on a control table",
			`GRANT INSERT ON corescope_control.prune_results TO {role}`, "", "INSERT on corescope_control.prune_results"},
		{"reader with direct UPDATE on a control request table",
			`GRANT UPDATE ON corescope_control.prune_requests TO {role}`, "", "UPDATE on corescope_control.prune_requests"},
		{"reader with CREATE on the control schema",
			`GRANT CREATE ON SCHEMA corescope_control TO {role}`, "", "CREATE on schema corescope_control"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seed := pgSchemaSeed(t)
			pgControlRoleForTest(t, seed)
			if tc.undo != "" {
				t.Cleanup(func() {
					if _, err := seed.Exec(tc.undo); err != nil {
						t.Errorf("%s: %v", tc.undo, err)
					}
				})
			}
			role, password, dsn := pgTestRole(t, seed, append(append([]string{}, pgControlReaderGrants...), tc.grant)...)
			d, err := OpenPostgresDB(dsn)
			if err == nil {
				d.Close()
				t.Fatal("OpenPostgresDB accepted a control member with extra write power")
			}
			msg := err.Error()
			for _, want := range []string{role, tc.inErr} {
				if !strings.Contains(msg, want) {
					t.Errorf("error lacks %q:\n%s", want, msg)
				}
			}
			if strings.Contains(msg, password) {
				t.Errorf("error leaks the password:\n%s", msg)
			}
		})
	}
}

const testPruneAPIKey = "test-secret-key-strong-enough"

func pruneRequest(t *testing.T, r *mux.Router, method, path, body string) (int, map[string]any) {
	t.Helper()
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	req.Header.Set("X-API-Key", testPruneAPIKey)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s %s: body is not a JSON object: %q", method, path, w.Body.String())
	}
	return w.Code, out
}

func pruneTestRouter(t *testing.T, d *DB) *mux.Router {
	t.Helper()
	gf := &GeoFilterConfig{Polygon: [][2]float64{{37.0, -123.0}, {38.0, -123.0}, {38.0, -121.0}, {37.0, -121.0}}}
	srv := NewServer(d, &Config{Port: 3000, APIKey: testPruneAPIKey, GeoFilter: gf}, NewHub())
	r := mux.NewRouter()
	srv.RegisterRoutes(r)
	return r
}

const controlDisabledLog = "geo-prune and multibyte capability publishing are disabled"

func TestOpenPostgresDBWithoutControlDisablesPrune(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(t *testing.T, seed *sql.DB) []string // extra reader grants
		reason string
	}{
		// The role may or may not exist cluster-wide at this point; either
		// way the reason names it.
		{"not a member", func(*testing.T, *sql.DB) []string { return pgReadOnlyGrants }, pgControlRole},
		{"member without inheritance", func(t *testing.T, seed *sql.DB) []string {
			pgControlRoleForTest(t, seed)
			// NOINHERIT before the GRANT: from Postgres 16 a membership's
			// inherit option is fixed when it is granted.
			return append(append([]string{}, pgReadOnlyGrants...), `ALTER ROLE {role} NOINHERIT`, `GRANT `+pgControlRole+` TO {role}`)
		}, "does not inherit " + pgControlRole},
		{"control grants not exact", func(t *testing.T, seed *sql.DB) []string {
			pgControlRoleForTest(t, seed)
			// The extra grant is on a control table, so it does not refuse
			// startup; it does make ControlGrantsOK false.
			if _, err := seed.Exec(`GRANT UPDATE ON corescope_control.prune_requests TO ` + pgControlRole); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := pgschema.EnsureControlGrants(context.Background(), seed); err != nil {
					t.Errorf("repair control grants: %v", err)
				}
			})
			return pgControlReaderGrants
		}, "exactly the control grants"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seed := pgSchemaSeed(t)
			grants := tc.setup(t, seed)
			_, _, dsn := pgTestRole(t, seed, grants...)

			var d *DB
			var err error
			out := captureLog(func() {
				d, err = OpenPostgresDB(dsn)
				if err != nil {
					return
				}
				r := pruneTestRouter(t, d)
				for _, call := range []struct{ method, path, body string }{
					{http.MethodPost, "/api/admin/prune-geo-filter", ""},
					{http.MethodPost, "/api/admin/prune-geo-filter?confirm=true", `{"pubkeys":["aa"]}`},
					{http.MethodGet, "/api/admin/prune-geo-filter/status?id=00000000c0de0002", ""},
					{http.MethodGet, "/api/admin/prune-geo-filter/status?id=00000000c0de0002", ""},
				} {
					code, body := pruneRequest(t, r, call.method, call.path, call.body)
					if code != http.StatusServiceUnavailable || body["error"] != msgControlUnavailable {
						t.Errorf("%s %s: %d %v, want 503 %q", call.method, call.path, code, body, msgControlUnavailable)
					}
				}
				// The analytics cycle skips publishing without logging.
				(&PacketStore{db: d}).publishMultibyteCapSnapshot([]MultiByteCapEntry{{PublicKey: "aa", Status: "confirmed"}})
			})
			if err != nil {
				t.Fatalf("OpenPostgresDB without usable control tables must still start: %v", err)
			}
			t.Cleanup(func() { d.Close() })
			if d.ctl != nil || d.pruneQueue() != nil || d.mbcapStore() != nil {
				t.Fatalf("control features on: ctl=%v pruneQ=%v mbcap=%v", d.ctl, d.pruneQ, d.mbcap)
			}
			// Measurement reads still work.
			if _, err := d.GetStats(); err != nil {
				t.Errorf("GetStats: %v", err)
			}

			if n := strings.Count(out, controlDisabledLog); n != 1 {
				t.Errorf("disabled warning logged %d times, want once:\n%s", n, out)
			}
			if !strings.Contains(out, tc.reason) {
				t.Errorf("warning lacks the reason %q:\n%s", tc.reason, out)
			}
		})
	}
}

// The geo-prune flow end to end on Postgres: the HTTP handlers enqueue
// through the control pool, the ingestor (simulated with the writer's
// prunequeue) completes, and the status endpoint reports its result.
func TestPostgresPruneGeoFilterEndToEnd(t *testing.T) {
	seed := pgSchemaSeed(t)
	pgControlRoleForTest(t, seed)
	for _, q := range []string{
		`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
			VALUES ('aaaa111122223333', 'OutsideNode', 'repeater', 51.5, 4.5, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1)`,
		`INSERT INTO nodes (public_key, name, role, lat, lon, last_seen, first_seen, advert_count)
			VALUES ('cccc111122223333', 'InsideNode', 'repeater', 37.5, -122.0, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1)`,
		`INSERT INTO nodes (public_key, name, role, last_seen, first_seen, advert_count)
			VALUES ('bbbb111122223333', 'NoGPSNode', 'companion', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', 1)`,
	} {
		if _, err := seed.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	d := openControlReader(t, seed)
	r := pruneTestRouter(t, d)

	code, body := pruneRequest(t, r, http.MethodPost, "/api/admin/prune-geo-filter", "")
	if code != http.StatusOK || body["dryRun"] != true || body["count"] != float64(1) {
		t.Fatalf("dry run: %d %v", code, body)
	}

	code, body = pruneRequest(t, r, http.MethodPost, "/api/admin/prune-geo-filter?confirm=true", `{"pubkeys":["aaaa111122223333"]}`)
	if code != http.StatusAccepted || body["accepted"] != true || body["count"] != float64(1) {
		t.Fatalf("confirm: %d %v", code, body)
	}
	id, _ := body["requestId"].(string)
	if id == "" {
		t.Fatalf("no requestId: %v", body)
	}
	t.Cleanup(func() {
		_, _ = seed.Exec(`DELETE FROM corescope_control.prune_requests WHERE id = ?`, id)
		_, _ = seed.Exec(`DELETE FROM corescope_control.prune_results WHERE id = ?`, id)
	})

	// The ingestor's view of the queue: the request, as enqueued.
	ing := prunequeue.NewPG(seed, "")
	req, err := ing.ReadRequest(id)
	if err != nil {
		t.Fatalf("ingestor ReadRequest: %v", err)
	}
	if req.Reason != "geo-prune" || len(req.Pubkeys) != 1 || req.Pubkeys[0] != "aaaa111122223333" {
		t.Fatalf("enqueued request: %+v", req)
	}
	// The server only enqueued: the node is still there.
	var n int
	if err := seed.QueryRow(`SELECT count(*) FROM nodes WHERE public_key = 'aaaa111122223333'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("OutsideNode rows: %d (%v), want 1", n, err)
	}

	status := "/api/admin/prune-geo-filter/status?id=" + id
	code, body = pruneRequest(t, r, http.MethodGet, status, "")
	if code != http.StatusOK || body["status"] != "pending" {
		t.Fatalf("status while pending: %d %v", code, body)
	}

	done := time.Date(2026, 11, 12, 1, 2, 3, 0, time.UTC)
	if err := ing.Complete(prunequeue.Result{ID: id, RequestedAt: req.RequestedAt, CompletedAt: done, Deleted: 1}); err != nil {
		t.Fatalf("ingestor Complete: %v", err)
	}
	code, body = pruneRequest(t, r, http.MethodGet, status, "")
	if code != http.StatusOK || body["status"] != "done" || body["deleted"] != float64(1) || body["completedAt"] != done.Format(time.RFC3339) {
		t.Fatalf("status after completion: %d %v", code, body)
	}

	code, _ = pruneRequest(t, r, http.MethodGet, "/api/admin/prune-geo-filter/status?id=not-hex", "")
	if code != http.StatusBadRequest {
		t.Errorf("invalid id: %d, want 400", code)
	}
	code, _ = pruneRequest(t, r, http.MethodGet, "/api/admin/prune-geo-filter/status?id=00000000c0de0fff", "")
	if code != http.StatusNotFound {
		t.Errorf("unknown id: %d, want 404", code)
	}
}

// On Postgres the ingestor's stats come from the ingestor_stats row, never
// the stats file.
func TestPostgresIngestorStatsFromTable(t *testing.T) {
	db, seed := newPostgresTestDB(t)
	// A stats file that must be ignored.
	file := filepath.Join(t.TempDir(), "stats.json")
	if err := os.WriteFile(file, []byte(`{"sampledAt":"2026-01-01T00:00:00Z","tx_inserted":999}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORESCOPE_INGESTOR_STATS", file)
	resetIngestorIOCache()
	resetSourceLivenessCache()
	t.Cleanup(func() { resetIngestorIOCache(); resetSourceLivenessCache() })
	r := pgRouter(t, db)

	// No row yet: the same empty answers as a missing file.
	code, body := serveJSON(t, r, "/api/perf/write-sources")
	if code != http.StatusOK || string(body["sources"]) != `{}` || string(body["sampleAt"]) != `""` {
		t.Fatalf("write-sources without a row: %d sources=%s sampleAt=%s", code, body["sources"], body["sampleAt"])
	}
	code, body = serveJSON(t, r, "/api/mqtt/status")
	if code != http.StatusOK || string(body["sources"]) != `[]` {
		t.Fatalf("mqtt status without a row: %d %s", code, body["sources"])
	}
	if got := readIngestorIOSampleFrom(db); got != nil {
		t.Fatalf("procIO without a row: %+v", got)
	}
	if got := readIngestorSourceLivenessFrom(db); got != nil {
		t.Fatalf("liveness without a row: %+v", got)
	}

	put := func(txInserted int) string {
		t.Helper()
		now := time.Now().UTC().Format(time.RFC3339)
		payload, err := json.Marshal(map[string]any{
			"sampledAt":   now,
			"tx_inserted": txInserted,
			"backfillUpdates": map[string]int64{
				"path": 3,
			},
			"procIO":          map[string]any{"readBytesPerSec": 42.0, "sampledAt": now},
			"source_liveness": map[string]any{"local": map[string]int64{"lastReceiptUnix": 10, "lastMessageUnix": 11}},
			"source_statuses": []map[string]any{{"name": "local", "broker": "mqtt://u:secret@broker:1883", "connected": true}},
		})
		if err != nil {
			t.Fatal(err)
		}
		updated := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := seed.Exec(`INSERT INTO ingestor_stats (id, updated_at, payload) VALUES (1, ?, ?)
			ON CONFLICT (id) DO UPDATE SET updated_at = EXCLUDED.updated_at, payload = EXCLUDED.payload`, updated, string(payload)); err != nil {
			t.Fatal(err)
		}
		return now
	}
	sampledAt := put(7)

	code, body = serveJSON(t, r, "/api/perf/write-sources")
	var sources map[string]int64
	if err := json.Unmarshal(body["sources"], &sources); err != nil || code != http.StatusOK {
		t.Fatalf("write-sources: %d %s %v", code, body["sources"], err)
	}
	if sources["tx_inserted"] != 7 || sources["backfill_path"] != 3 || string(body["sampleAt"]) != `"`+sampledAt+`"` {
		t.Errorf("write-sources from ingestor_stats: sources=%v sampleAt=%s", sources, body["sampleAt"])
	}

	code, body = serveJSON(t, r, "/api/mqtt/status")
	var statuses []MqttSourceStatus
	if err := json.Unmarshal(body["sources"], &statuses); err != nil || code != http.StatusOK || len(statuses) != 1 {
		t.Fatalf("mqtt status: %d %s %v", code, body["sources"], err)
	}
	if statuses[0].Name != "local" || strings.Contains(statuses[0].Broker, "secret") {
		t.Errorf("mqtt status source: %+v", statuses[0])
	}

	before := readIngestorStatsParseCalls.Load()
	io := readIngestorIOSampleFrom(db)
	if io == nil || io.ReadBytesPerSec != 42 {
		t.Fatalf("procIO from ingestor_stats: %+v", io)
	}
	if again := readIngestorIOSampleFrom(db); again != io {
		t.Errorf("unchanged row decoded again")
	}
	if got := readIngestorStatsParseCalls.Load() - before; got != 1 {
		t.Errorf("parses for two reads of an unchanged row: %d, want 1", got)
	}
	time.Sleep(2 * time.Millisecond) // a distinct updated_at
	put(8)
	if got := readIngestorIOSampleFrom(db); got == nil {
		t.Fatal("procIO after the row changed: nil")
	}
	if got := readIngestorStatsParseCalls.Load() - before; got != 2 {
		t.Errorf("parses after the row changed: %d, want 2", got)
	}

	resetSourceLivenessCache()
	live := readIngestorSourceLivenessFrom(db)
	if live["local"].LastReceiptUnix != 10 || live["local"].LastMessageUnix != 11 {
		t.Errorf("liveness from ingestor_stats: %+v", live)
	}
}
