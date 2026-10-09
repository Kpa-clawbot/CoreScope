package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/meshcore-analyzer/dbschema"
	"github.com/meshcore-analyzer/pgutil"
	"github.com/meshcore-analyzer/pgutil/pgtest"
)

func TestPostgresCursorStopsAtLimitWithoutSorting(t *testing.T) {
	db := setupTestDB(t)
	copyTestRows(t, db.conn, "transmissions", []string{"id", "raw_hex", "hash", "first_seen"}, 20000, func(i int) []any {
		return []any{i + 1, "AA", fmt.Sprintf("cursor-%d", i), "2026-01-01T00:00:00Z"}
	})
	if _, err := db.conn.Exec(`ANALYZE transmissions`); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := db.conn.QueryRow(`EXPLAIN (ANALYZE, FORMAT JSON) `+newTransmissionsSinceSQL, 10000, 100).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	type planNode struct {
		Type  string            `json:"Node Type"`
		Rows  float64           `json:"Actual Rows"`
		Plans []json.RawMessage `json:"Plans"`
	}
	var plans []struct{ Plan json.RawMessage }
	if err := json.Unmarshal([]byte(raw), &plans); err != nil {
		t.Fatal(err)
	}
	pending := []json.RawMessage{plans[0].Plan}
	for len(pending) > 0 {
		var node planNode
		if err := json.Unmarshal(pending[0], &node); err != nil {
			t.Fatal(err)
		}
		pending = append(pending[1:], node.Plans...)
		if strings.Contains(node.Type, "Sort") || (strings.Contains(node.Type, "Scan") && node.Rows > 100) {
			t.Fatalf("cursor scanned or sorted more than its bounded page: %s rows=%g", node.Type, node.Rows)
		}
	}
	rows, err := db.GetNewTransmissionsSince(10000, 100)
	if err != nil || len(rows) != 100 || rows[0]["id"] != 10001 {
		t.Fatalf("cursor page: len=%d, %v", len(rows), err)
	}
}

func TestPostgresNegativeOffsetsPreserveFirstPage(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	for name, query := range map[string]func() (*PacketResult, error){
		"packets": func() (*PacketResult, error) { return db.QueryPackets(PacketQuery{Limit: 2, Offset: -1}) },
		"groups":  func() (*PacketResult, error) { return db.QueryGroupedPackets(PacketQuery{Limit: 2, Offset: -1}) },
		"multiple nodes": func() (*PacketResult, error) {
			return db.QueryMultiNodePackets([]string{"aabbccdd11223344"}, 2, -1, "DESC", "", "")
		},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := query()
			if err != nil || len(result.Packets) != 2 {
				t.Fatalf("negative offset must return the first page: %+v, %v", result, err)
			}
		})
	}
	nodes, _, _, err := db.GetNodes(NodeQuery{Limit: 2, Offset: -1})
	if err != nil || len(nodes) != 2 {
		t.Fatalf("negative node offset must return the first page: %d, %v", len(nodes), err)
	}
}

func TestPostgresCanonicalSchemaAPIs(t *testing.T) {
	dsn := pgtest.NewSchema(t)
	writer, err := openFixtureSQL(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if err := dbschema.Apply(writer, nil); err != nil {
		t.Fatal(err)
	}
	seedTestData(t, &DB{conn: writer})
	if _, err := writer.Exec(`UPDATE transmissions SET last_seen=EXTRACT(EPOCH FROM first_seen::timestamptz)::bigint`); err != nil {
		t.Fatal(err)
	}
	db, err := openFixtureReader(t, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := dbschema.AssertReady(db.conn); err != nil {
		t.Fatal(err)
	}
	stats, err := db.GetStats()
	if err != nil || stats.TotalTransmissions != 3 || stats.TotalObservations != 4 {
		t.Fatalf("canonical statistics: %+v, %v", stats, err)
	}
	pt := 4
	q := PacketQuery{Limit: 5, Type: &pt, Observer: "obs1,obs2", Region: "SJC", Since: "2020-01-01", Until: "2099-01-01", Order: "DESC"}
	packets, err := db.QueryPackets(q)
	if err != nil || len(packets.Packets) != 2 {
		t.Fatalf("filtered packets: %+v, %v", packets, err)
	}
	grouped, err := db.QueryGroupedPackets(q)
	if err != nil || len(grouped.Packets) != 2 {
		t.Fatalf("filtered groups: %+v, %v", grouped, err)
	}
	nodes, count, _, err := db.GetNodes(NodeQuery{Limit: 5, Role: "repeater", Search: "test", RegionPubkeys: []string{"aabbccdd11223344"}})
	if err != nil || count != 1 || len(nodes) != 1 {
		t.Fatalf("filtered nodes: %d/%d, %v", len(nodes), count, err)
	}
	messages, count, err := db.GetChannelMessages("#test", 5, 0, "SJC,SFO")
	if err != nil || count != 1 || len(messages) != 1 {
		t.Fatalf("channel messages: %d/%d, %v", len(messages), count, err)
	}
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if len(store.packets) != 3 || store.totalObs != 4 {
		t.Fatalf("canonical load: %d transmissions, %d observations", len(store.packets), store.totalObs)
	}
}

func TestPostgresOpenRejectsLegacyFileWithoutLeakingInput(t *testing.T) {
	legacy := filepath.Join(t.TempDir(), "private-measurements.db")
	db, err := OpenDB(legacy)
	if db != nil {
		db.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "PostgreSQL") {
		t.Fatalf("expected actionable PostgreSQL migration error, got %v", err)
	}
	if strings.Contains(err.Error(), legacy) {
		t.Fatal("database input leaked in connection error")
	}
}

func TestPostgresReaderRejectsWriterCredential(t *testing.T) {
	db, err := OpenDB(pgtest.NewSchema(t))
	if db != nil {
		db.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "reader role") {
		t.Fatalf("elevated telemetry credential was not refused: %v", err)
	}
}

func TestPostgresScopeAuditSnapshotKeepsConcurrentCommitOut(t *testing.T) {
	dsn := pgtest.NewSchema(t)
	writer, err := openFixtureSQL(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Exec(`CREATE TABLE transmissions(id BIGINT PRIMARY KEY, first_seen TEXT, scope_name TEXT, route_type INTEGER);
		CREATE TABLE observations(id BIGINT PRIMARY KEY, transmission_id BIGINT, path_json TEXT);
		INSERT INTO transmissions VALUES(1,'2026-01-02T00:00:00Z','scope',1);
		INSERT INTO observations VALUES(1,1,'["aabb"]')`); err != nil {
		t.Fatal(err)
	}
	conn, err := pgutil.Open(pgtest.ReadOnly(t, dsn), true)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	db := &DB{conn: conn}
	tx, err := db.beginScopeAuditSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	meta, err := scopeAuditWindowMeta(tx, "2026-01-01T00:00:00Z")
	if err != nil || len(meta) != 1 {
		t.Fatalf("initial metadata: %d, %v", len(meta), err)
	}
	if _, err := writer.Exec(`INSERT INTO transmissions VALUES(2,'2026-01-03T00:00:00Z','scope',1);
		INSERT INTO observations VALUES(2,2,'["aabb"]')`); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(scopeAuditForwarderScanQuery, "2026-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for rows.Next() {
		var id int64
		var hop string
		if err := rows.Scan(&id, &hop); err != nil {
			t.Fatal(err)
		}
		if id != 1 {
			t.Fatal("scope snapshot included a later commit absent from metadata")
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if count != 1 {
		t.Fatalf("hop rows: %d, want 1", count)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var visible int
	if err := conn.QueryRow(`SELECT COUNT(*) FROM transmissions`).Scan(&visible); err != nil || visible != 2 {
		t.Fatalf("fresh snapshot did not see committed row: %d, %v", visible, err)
	}
}

func TestPostgresReaderUsesRestrictedRole(t *testing.T) {
	dsn := pgtest.NewSchema(t)
	writer, err := pgutil.Open(dsn, false)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	_, err = writer.Exec(`CREATE TABLE transmissions (id BIGINT PRIMARY KEY, hash TEXT);
		CREATE TABLE observations (id BIGINT PRIMARY KEY, timestamp BIGINT, observer_idx BIGINT);
		CREATE TABLE nodes (public_key TEXT, name TEXT, role TEXT, last_seen TEXT);
		CREATE TABLE observers (rowid BIGINT GENERATED BY DEFAULT AS IDENTITY UNIQUE, id TEXT, inactive INTEGER);
		INSERT INTO transmissions VALUES (7, 'stable-hash')`)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := OpenDB(pgtest.ReadOnly(t, dsn))
	if err != nil {
		t.Fatalf("open PostgreSQL reader: %v", err)
	}
	defer reader.Close()
	if got := reader.GetMaxTransmissionID(); got != 7 {
		t.Fatalf("max transmission = %d, want 7", got)
	}
	if _, err := reader.conn.Exec(`INSERT INTO transmissions VALUES (8, 'forbidden')`); err == nil {
		t.Fatal("telemetry reader accepted a write")
	}
	if _, err := reader.conn.Exec(`SET default_transaction_read_only = off`); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.conn.Exec(`DELETE FROM transmissions`); err == nil {
		t.Fatal("reader can write after changing its session flag")
	}
}
