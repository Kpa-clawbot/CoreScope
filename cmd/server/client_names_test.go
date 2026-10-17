package main

import (
	"encoding/json"
	"testing"
)

// createClientNodeNames creates client_node_names the way the ingestor does
// (cmd/ingestor/db.go); the server never creates it.
func createClientNodeNames(t *testing.T, db *DB) {
	t.Helper()
	mustExecDB(t, db, `CREATE TABLE client_node_names (
		id INTEGER PRIMARY KEY AUTOINCREMENT, target TEXT NOT NULL, rx_pubkey TEXT NOT NULL,
		observed_at TEXT NOT NULL, ingested_at TEXT NOT NULL, name TEXT NOT NULL,
		lat REAL, lon REAL, pos_acc_m REAL, repeater_clock INTEGER,
		UNIQUE(target, rx_pubkey, observed_at))`)
	mustExecDB(t, db, `CREATE INDEX idx_cnn_target ON client_node_names(target, observed_at)`)
}

// heardDirect records one discover reception of pk: a name only counts once the
// repeater was heard directly (clientNameHeard).
func heardDirect(t *testing.T, db *DB, pk string) {
	t.Helper()
	mustExecDB(t, db, `INSERT INTO client_receptions (rx_pubkey,heard_key,heard_keylen,snr,lat,lon,rx_at,ingested_at,src)
		VALUES ('aa11','`+pk[:16]+`',8,-5,51.0,5.38,'2026-10-16T10:00:00Z','x','discover')`)
}

func insClientName(t *testing.T, db *DB, target, name, observedAt string) {
	t.Helper()
	mustExecDB(t, db, `INSERT INTO client_node_names (target, rx_pubkey, observed_at, ingested_at, name)
		VALUES ('`+target+`','aa11','`+observedAt+`','`+observedAt+`','`+name+`')`)
}

const (
	cnnOnlyPK    = "c0cd110000000000000000000000000000000000000000000000000000000000" // no nodes row
	cnnNoNamePK  = "c1c1220000000000000000000000000000000000000000000000000000000000" // nodes row, empty name
	cnnAdvertPK  = "c2c2330000000000000000000000000000000000000000000000000000000000" // nodes row with a name
	cnnClashNode = "e470110000000000000000000000000000000000000000000000000000000000" // nodes, shares e470
	cnnClashCli  = "e470220000000000000000000000000000000000000000000000000000000000" // client only, shares e470
	cnnHiddenPK  = "c3c3440000000000000000000000000000000000000000000000000000000000"
	cnnBlackPK   = "c4c4550000000000000000000000000000000000000000000000000000000000"
	cnnUnheard   = "c2c2ff0000000000000000000000000000000000000000000000000000000000" // reported, never heard, shares c2c2
)

func seedClientNames(t *testing.T) *Server {
	t.Helper()
	db := seedCoverageDB(t)
	createClientNodeNames(t, db)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, name, role) VALUES ('`+cnnNoNamePK+`','','repeater')`)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, name, role) VALUES ('`+cnnAdvertPK+`','FromAdvert','repeater')`)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, name, role) VALUES ('`+cnnClashNode+`','Aegis','repeater')`)
	insClientName(t, db, cnnOnlyPK, "Old name", "2026-10-01T10:00:00.000Z")
	insClientName(t, db, cnnOnlyPK, "Self Named", "2026-10-16T10:00:00.000Z")
	insClientName(t, db, cnnNoNamePK, "Filled In", "2026-10-16T10:00:00.000Z")
	insClientName(t, db, cnnAdvertPK, "Self Report", "2026-10-16T10:00:00.000Z")
	insClientName(t, db, cnnClashCli, "Other", "2026-10-16T10:00:00.000Z")
	insClientName(t, db, cnnHiddenPK, "🚫Secret", "2026-10-16T10:00:00.000Z")
	insClientName(t, db, cnnBlackPK, "Blacklisted", "2026-10-16T10:00:00.000Z")
	insClientName(t, db, cnnUnheard, "Invented", "2026-10-16T10:00:00.000Z")
	for _, pk := range []string{cnnOnlyPK, cnnNoNamePK, cnnAdvertPK, cnnClashCli, cnnHiddenPK, cnnBlackPK} {
		heardDirect(t, db, pk)
	}
	return &Server{db: db, cfg: &Config{
		NodeBlacklist:      []string{cnnBlackPK},
		HiddenNamePrefixes: []string{"🚫"},
	}}
}

func TestBatchResolveHeardKeysClientNames(t *testing.T) {
	srv := seedClientNames(t)
	got := srv.batchResolveHeardKeys([]string{"c0cd", "c1c122", "c2c2", "e470", "c3c3", "c4c4", cnnOnlyPK})
	cases := map[string][2]string{
		"c0cd":    {cnnOnlyPK, "Self Named"},   // only known from client_node_names, newest observation
		"c1c122":  {cnnNoNamePK, "Filled In"},  // nodes row without a name: client name fills it
		"c2c2":    {cnnAdvertPK, "FromAdvert"}, // the advert name wins; the never-heard c2c2ff report adds no candidate
		"e470":    {"e470", ""},                // one candidate per source: still ambiguous
		"c3c3":    {"c3c3", ""},                // hidden-prefix client name
		"c4c4":    {"c4c4", ""},                // blacklisted pubkey
		cnnOnlyPK: {cnnOnlyPK, "Self Named"},   // full pubkey
	}
	for k, want := range cases {
		if got[k] != want {
			t.Errorf("batchResolveHeardKeys[%q] = %v, want %v", k, got[k], want)
		}
	}
}

func TestResolvePrefixClientNames(t *testing.T) {
	srv := seedClientNames(t)
	resolve := func(pfx string) ResolvePrefixResp {
		var r ResolvePrefixResp
		json.Unmarshal(serveResolve(srv, "/api/nodes/resolve?prefix="+pfx).Body.Bytes(), &r)
		return r
	}
	if r := resolve("c0cd"); r.Pubkey != cnnOnlyPK || r.Name != "Self Named" || r.Ambiguous {
		t.Errorf("client-only node: %+v", r)
	}
	if r := resolve("c1c1"); r.Pubkey != cnnNoNamePK || r.Name != "Filled In" {
		t.Errorf("nameless nodes row: %+v", r)
	}
	if r := resolve("c2c2"); r.Name != "FromAdvert" || r.Ambiguous {
		t.Errorf("advert name must win, and a never-heard report must not make c2c2 ambiguous: %+v", r)
	}
	if r := resolve("c2c2ff"); r.Pubkey != "" || r.Ambiguous {
		t.Errorf("a name for a repeater nobody heard directly must not resolve: %+v", r)
	}
	if r := resolve("e470"); !r.Ambiguous || r.Name != "" || r.Pubkey != "" {
		t.Errorf("collision across sources must be ambiguous: %+v", r)
	}
	if r := resolve("c3c3"); r.Name != "" || r.Pubkey != "" || r.Ambiguous {
		t.Errorf("hidden client name must resolve as not-found: %+v", r)
	}
	if r := resolve("c4c4"); r.Name != "" || r.Pubkey != "" || r.Ambiguous {
		t.Errorf("blacklisted client-named node must resolve as not-found: %+v", r)
	}
}

// A database without the table (an older ingestor) resolves from nodes alone,
// and picks the table up once the ingestor creates it, without a restart.
func TestClientNodeNamesTableAbsentThenCreated(t *testing.T) {
	db := seedCoverageDB(t)
	mustExecDB(t, db, `INSERT INTO nodes (public_key, name, role) VALUES ('`+cnnAdvertPK+`','FromAdvert','repeater')`)
	srv := &Server{db: db, cfg: &Config{}}

	got := srv.batchResolveHeardKeys([]string{"c2c2", "c0cd"})
	if got["c2c2"] != [2]string{cnnAdvertPK, "FromAdvert"} || got["c0cd"] != [2]string{"c0cd", ""} {
		t.Fatalf("without the table: %v", got)
	}

	createClientNodeNames(t, db)
	insClientName(t, db, cnnOnlyPK, "Self Named", "2026-10-16T10:00:00.000Z")
	heardDirect(t, db, cnnOnlyPK)
	if got := srv.batchResolveHeardKeys([]string{"c0cd"}); got["c0cd"] != [2]string{cnnOnlyPK, "Self Named"} {
		t.Fatalf("after the ingestor created the table: %v", got)
	}
}

// Every key costs one query per chunk, whatever the number of keys: 250 keys
// span two chunks of at most 200, each within SQLite's 500-term compound limit.
func TestLookupPrefixesManyKeys(t *testing.T) {
	srv := seedClientNames(t)
	keys := make([]string, 0, 250)
	for i := 0; i < 249; i++ {
		keys = append(keys, "ffff"+string("0123456789abcdef"[i%16])+string("0123456789abcdef"[(i/16)%16]))
	}
	keys = append(keys, "c0cd")
	hits := srv.lookupPrefixes(keys)
	if len(hits) != len(keys) {
		t.Fatalf("hits = %d, want %d (a chunk failed)", len(hits), len(keys))
	}
	if h := hits["c0cd"]; h.pubkey != cnnOnlyPK || h.name != "Self Named" {
		t.Fatalf("c0cd in the second chunk: %+v", h)
	}
}
