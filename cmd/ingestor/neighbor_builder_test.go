package main

import (
	"path/filepath"
	"testing"
)

// TestNeighborEdgesBuilderUpsertsFromObservations enforces issue
// #1287 Option 4: the INGESTOR builds neighbor_edges from raw
// observations/transmissions and persists them. Server is read-only.
//
// Synthesize a tiny DB with one ADVERT observation whose path[0]
// uniquely resolves to a known node, then assert the builder writes
// the expected edge.
func TestNeighborEdgesBuilderUpsertsFromObservations(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "build.db")

	// Open via the ingestor's normal opener so applySchema and
	// dbschema.Apply both run (the builder requires neighbor_edges +
	// observers.iata etc.).
	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	// Seed two nodes whose pubkey prefixes will be used as hops.
	if _, err := store.db.Exec(
		`INSERT INTO nodes (public_key, name) VALUES (?, ?), (?, ?)`,
		"aaaaaaaaaa", "from-node",
		"bbbbbbbbbb", "first-hop",
	); err != nil {
		t.Fatal(err)
	}

	// Seed one observer.
	if _, err := store.db.Exec(
		`INSERT INTO observers (id, name) VALUES (?, ?)`,
		"obs-1", "observer-1",
	); err != nil {
		t.Fatal(err)
	}
	var obsRowid int64
	if err := store.db.QueryRow(`SELECT rowid FROM observers WHERE id = ?`, "obs-1").Scan(&obsRowid); err != nil {
		t.Fatal(err)
	}

	// Insert one ADVERT transmission with from_pubkey = aaaaa…
	res, err := store.db.Exec(
		`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json, from_pubkey)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"", "h1", "2026-01-01T00:00:00Z", 0, payloadADVERT, 0, "{}", "aaaaaaaaaa",
	)
	if err != nil {
		t.Fatal(err)
	}
	txID, _ := res.LastInsertId()

	// Insert one observation whose path[0] = "bb" (2-hex prefix unique
	// to bbbbb… in the nodes table). Expected edge: a↔b.
	if _, err := store.db.Exec(
		`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp) VALUES (?, ?, ?, ?)`,
		txID, obsRowid, `["bb"]`, int64(1735689600),
	); err != nil {
		t.Fatal(err)
	}

	n, err := store.buildAndPersistNeighborEdges()
	if err != nil {
		t.Fatalf("buildAndPersistNeighborEdges: %v", err)
	}
	if n == 0 {
		t.Fatal("expected at least 1 edge upserted, got 0")
	}

	var got int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM neighbor_edges WHERE node_a = ? AND node_b = ?`, "aaaaaaaaaa", "bbbbbbbbbb").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("expected the a↔b edge to be persisted; got %d rows", got)
	}
}

// (test ends here)

// TestNeighborEdgesBuilderUpsertsFromAnonReqEphemeralPubKey verifies #1777:
// ANON_REQ transmissions (payload type 7) carry the sender's full
// ephemeral pubkey in decoded_json ("ephemeralPubKey"), not in the
// from_pubkey column (which is only populated for ADVERT at write time,
// see db.go's #1143 comment). The builder must fall back to parsing
// decoded_json for ANON_REQ, exactly as it already does for ADVERT.
func TestNeighborEdgesBuilderUpsertsFromAnonReqEphemeralPubKey(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "build.db")

	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	if _, err := store.db.Exec(
		`INSERT INTO nodes (public_key, name) VALUES (?, ?), (?, ?)`,
		"aaaaaaaaaa", "sender",
		"bbbbbbbbbb", "first-hop",
	); err != nil {
		t.Fatal(err)
	}

	if _, err := store.db.Exec(
		`INSERT INTO observers (id, name) VALUES (?, ?)`,
		"obs-1", "observer-1",
	); err != nil {
		t.Fatal(err)
	}
	var obsRowid int64
	if err := store.db.QueryRow(`SELECT rowid FROM observers WHERE id = ?`, "obs-1").Scan(&obsRowid); err != nil {
		t.Fatal(err)
	}

	// ANON_REQ transmission: from_pubkey left NULL (as real ingest does —
	// only ADVERT populates it at write time), sender identity carried in
	// decoded_json.ephemeralPubKey instead.
	res, err := store.db.Exec(
		`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"", "h2", "2026-01-01T00:00:00Z", 0, payloadAnonReq, 0, `{"ephemeralPubKey":"aaaaaaaaaa"}`,
	)
	if err != nil {
		t.Fatal(err)
	}
	txID, _ := res.LastInsertId()

	if _, err := store.db.Exec(
		`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp) VALUES (?, ?, ?, ?)`,
		txID, obsRowid, `["bb"]`, int64(1735689600),
	); err != nil {
		t.Fatal(err)
	}

	n, err := store.buildAndPersistNeighborEdges()
	if err != nil {
		t.Fatalf("buildAndPersistNeighborEdges: %v", err)
	}
	if n == 0 {
		t.Fatal("expected at least 1 edge upserted, got 0")
	}

	var got int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM neighbor_edges WHERE node_a = ? AND node_b = ?`, "aaaaaaaaaa", "bbbbbbbbbb").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("expected the sender\u2194first-hop edge from ANON_REQ to be persisted (#1777); got %d rows", got)
	}
}

// TestNeighborEdgesBuilderExcludesOtherNonAdvertTypes verifies #1777's
// scope boundary: a plain REQ (payload type 2, not ADVERT or ANON_REQ)
// must NOT produce an originator↔path[0] edge, even if from_pubkey happens
// to be set — REQ's src is only a 1-byte truncated hash of the originator,
// not a full pubkey, and was explicitly rejected as an edge source in the
// #1777 discussion (collision odds ~1/256).
func TestNeighborEdgesBuilderExcludesOtherNonAdvertTypes(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "build.db")

	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	if _, err := store.db.Exec(
		`INSERT INTO nodes (public_key, name) VALUES (?, ?), (?, ?)`,
		"aaaaaaaaaa", "sender",
		"bbbbbbbbbb", "first-hop",
	); err != nil {
		t.Fatal(err)
	}

	if _, err := store.db.Exec(
		`INSERT INTO observers (id, name) VALUES (?, ?)`,
		"obs-1", "observer-1",
	); err != nil {
		t.Fatal(err)
	}
	var obsRowid int64
	if err := store.db.QueryRow(`SELECT rowid FROM observers WHERE id = ?`, "obs-1").Scan(&obsRowid); err != nil {
		t.Fatal(err)
	}

	const payloadREQ = 2
	res, err := store.db.Exec(
		`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json, from_pubkey)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"", "h3", "2026-01-01T00:00:00Z", 0, payloadREQ, 0, "{}", "aaaaaaaaaa",
	)
	if err != nil {
		t.Fatal(err)
	}
	txID, _ := res.LastInsertId()

	if _, err := store.db.Exec(
		`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp) VALUES (?, ?, ?, ?)`,
		txID, obsRowid, `["bb"]`, int64(1735689600),
	); err != nil {
		t.Fatal(err)
	}

	if _, err := store.buildAndPersistNeighborEdges(); err != nil {
		t.Fatalf("buildAndPersistNeighborEdges: %v", err)
	}

	var got int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM neighbor_edges WHERE node_a = ? AND node_b = ?`, "aaaaaaaaaa", "bbbbbbbbbb").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("REQ should not produce an originator\u2194path[0] edge; got %d rows", got)
	}
}
