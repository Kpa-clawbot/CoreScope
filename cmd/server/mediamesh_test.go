package main

import (
	"testing"
	"time"
)

// MediaMesh (radio2): observation and node marks loaded from the ingestor's
// tables; see mediamesh.go.

// withMediaMesh resets the in-memory MediaMesh state for one test.
func withMediaMesh(t testing.TB) {
	t.Helper()
	reset := func() {
		mediaMeshMu.Lock()
		mediaMeshObs = map[int]struct{}{}
		mediaMeshMaxObsID = 0
		mediaMeshNodesTS = ""
		mediaMeshMu.Unlock()
		mediaMeshNodes.Store(nil)
	}
	reset()
	t.Cleanup(reset)
}

func seedMediaMesh(t *testing.T, srv *Server, stmts ...string) {
	t.Helper()
	base := []string{
		`CREATE TABLE IF NOT EXISTS observation_mediamesh (observation_id INTEGER PRIMARY KEY)`,
		`CREATE TABLE IF NOT EXISTS node_mediamesh (public_key TEXT PRIMARY KEY, mode TEXT NOT NULL,
			radio TEXT, source TEXT NOT NULL, last_seen TEXT NOT NULL)`,
	}
	for _, s := range append(base, stmts...) {
		if _, err := srv.db.conn.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	withMediaMesh(t)
	refreshMediaMesh(srv.db.conn, true)
}

func mmRecentTS() string { return time.Now().UTC().Add(-time.Hour).Format(time.RFC3339) }

// Observation 2 (tx 1, observer 2) was carried on radio2: it and its
// transmission say so; nothing else does.
func TestPacketsCarryMediaMesh(t *testing.T) {
	srv, router := setupTestServer(t)
	seedMediaMesh(t, srv, `INSERT INTO observation_mediamesh (observation_id) VALUES (2)`)

	var body packetsBody
	getJSON(t, router, "/api/packets?expand=observations", &body)
	if len(body.Packets) == 0 {
		t.Fatal("no packets")
	}
	for _, p := range body.Packets {
		want := p["hash"] == "abc123def4567890"
		if got, _ := p["mediamesh"].(bool); got != want {
			t.Errorf("packet %v mediamesh = %v, want %v", p["hash"], p["mediamesh"], want)
		}
		obs, _ := p["observations"].([]interface{})
		for _, o := range obs {
			m := o.(map[string]interface{})
			id, _ := m["id"].(float64)
			_, has := m["mediamesh"]
			if has != (id == 2) {
				t.Errorf("observation %v mediamesh present = %v", id, has)
			}
		}
	}

	var grouped packetsBody
	getJSON(t, router, "/api/packets?groupByHash=true", &grouped)
	for _, p := range grouped.Packets {
		want := p["hash"] == "abc123def4567890"
		if got, _ := p["mediamesh"].(bool); got != want {
			t.Errorf("grouped %v mediamesh = %v, want %v", p["hash"], p["mediamesh"], want)
		}
	}
}

// Without the ingestor's tables (an older database) nothing is marked and
// nothing fails.
func TestMediaMeshWithoutTables(t *testing.T) {
	srv, router := setupTestServer(t)
	withMediaMesh(t)
	refreshMediaMesh(srv.db.conn, true)
	var body packetsBody
	getJSON(t, router, "/api/packets", &body)
	for _, p := range body.Packets {
		if _, ok := p["mediamesh"]; ok {
			t.Errorf("packet %v marked without tables", p["hash"])
		}
	}
	if n := nodeMediaMeshAt("aabbccdd11223344", time.Now()); n != nil {
		t.Errorf("node marked without tables: %+v", n)
	}
}

func TestNodesCarryMediaMesh(t *testing.T) {
	srv, router := setupTestServer(t)
	old := time.Now().UTC().Add(-8 * 24 * time.Hour).Format(time.RFC3339)
	seedMediaMesh(t, srv,
		`INSERT INTO node_mediamesh VALUES ('AABBCCDD11223344', 'rxtx', '911.15,250,7,5', 'status', '`+mmRecentTS()+`')`,
		`INSERT INTO node_mediamesh VALUES ('eeff00112233aabb', 'off', NULL, 'neighbor', '`+mmRecentTS()+`')`,
		`INSERT INTO node_mediamesh VALUES ('1122334455667788', 'rx', NULL, 'heard', '`+old+`')`,
	)

	var list struct {
		Nodes []map[string]interface{} `json:"nodes"`
	}
	getJSON(t, router, "/api/nodes", &list)
	seen := 0
	for _, n := range list.Nodes {
		mm, has := n["mediamesh"].(map[string]interface{})
		switch n["public_key"] {
		case "aabbccdd11223344":
			seen++
			if !has || mm["mode"] != "rxtx" || mm["radio"] != "911.15,250,7,5" || mm["source"] != "status" {
				t.Errorf("repeater mediamesh = %v", n["mediamesh"])
			}
		default: // off, and evidence older than the window
			if has {
				t.Errorf("%v mediamesh = %v, want none", n["public_key"], mm)
			}
		}
	}
	if seen != 1 {
		t.Fatalf("repeater not in /api/nodes: %v", list.Nodes)
	}

	var detail struct {
		Node map[string]interface{} `json:"node"`
	}
	getJSON(t, router, "/api/nodes/aabbccdd11223344", &detail)
	if mm, _ := detail.Node["mediamesh"].(map[string]interface{}); mm["mode"] != "rxtx" {
		t.Errorf("node detail mediamesh = %v", detail.Node["mediamesh"])
	}
}

// The per-tick refresh picks up new observations and node rows; the full
// reload drops what retention deleted.
func TestRefreshMediaMeshIncrementalAndFull(t *testing.T) {
	srv, _ := setupTestServer(t)
	seedMediaMesh(t, srv)
	if observationMediaMesh(3) {
		t.Fatal("observation 3 marked before insert")
	}
	ts := mmRecentTS()
	if _, err := srv.db.conn.Exec(`INSERT INTO observation_mediamesh VALUES (3)`); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.db.conn.Exec(`INSERT INTO node_mediamesh VALUES ('1122334455667788', 'rx', NULL, 'neighbor', ?)`, ts); err != nil {
		t.Fatal(err)
	}
	refreshMediaMesh(srv.db.conn, false)
	if !observationMediaMesh(3) {
		t.Error("incremental refresh missed observation 3")
	}
	if n := nodeMediaMeshAt("1122334455667788", time.Now()); n == nil || n.Mode != "rx" {
		t.Errorf("incremental refresh node = %+v", n)
	}

	// A newer report turns it off.
	later := time.Now().UTC().Format(time.RFC3339)
	if _, err := srv.db.conn.Exec(`UPDATE node_mediamesh SET mode='off', last_seen=? WHERE public_key='1122334455667788'`, later); err != nil {
		t.Fatal(err)
	}
	refreshMediaMesh(srv.db.conn, false)
	if n := nodeMediaMeshAt("1122334455667788", time.Now()); n != nil {
		t.Errorf("after off: %+v", n)
	}

	if _, err := srv.db.conn.Exec(`DELETE FROM observation_mediamesh`); err != nil {
		t.Fatal(err)
	}
	refreshMediaMesh(srv.db.conn, true)
	if observationMediaMesh(3) {
		t.Error("full reload kept a deleted observation")
	}
}

// Rule 0: the per-packet cost is a set lookup per observation.
func BenchmarkTxMediaMesh(b *testing.B) {
	withMediaMesh(b)
	mediaMeshMu.Lock()
	for i := 0; i < 5000; i++ {
		mediaMeshObs[i*7] = struct{}{}
	}
	mediaMeshMu.Unlock()
	tx := &StoreTx{}
	for i := 0; i < 20; i++ {
		tx.Observations = append(tx.Observations, &StoreObs{ID: 1000000 + i})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = txMediaMesh(tx)
	}
}
