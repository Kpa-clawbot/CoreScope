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

// TestNeighborEdgesBuilderInteriorHopEdges is the #1547 follow-up fix:
// consecutive hops WITHIN a path (not just the two endpoints) must also
// produce neighbor_edges rows. resolvePathWithContext's anchor-on-
// previous-hop lookup (path_resolver.go) needs adjacency data for
// interior hops to resolve anything past hop 0 in practice -- before
// this fix, neighbor_edges only ever recorded originator↔hop0 and
// observer↔lastHop, so a multi-hop path could never resolve its middle.
func TestNeighborEdgesBuilderInteriorHopEdges(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "build.db")

	store, err := OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	defer store.Close()

	// Three repeaters along the path, each with a unique 2-hex prefix.
	if _, err := store.db.Exec(
		`INSERT INTO nodes (public_key, name) VALUES (?, ?), (?, ?), (?, ?)`,
		"bbbbbbbbbb", "hop-b",
		"cccccccccc", "hop-c",
		"dddddddddd", "hop-d",
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

	// A non-ADVERT (CHAN) transmission, no from_pubkey -- interior edges
	// must not depend on isAdvert or a resolvable origin, unlike the two
	// endpoint edges.
	res, err := store.db.Exec(
		`INSERT INTO transmissions (raw_hex, hash, first_seen, route_type, payload_type, payload_version, decoded_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"", "h2", "2026-01-01T00:00:00Z", 0, 5, 0, "{}",
	)
	if err != nil {
		t.Fatal(err)
	}
	txID, _ := res.LastInsertId()

	// path = b -> c -> d (three hops). Expect interior edges b<->c and c<->d.
	if _, err := store.db.Exec(
		`INSERT INTO observations (transmission_id, observer_idx, path_json, timestamp) VALUES (?, ?, ?, ?)`,
		txID, obsRowid, `["bb","cc","dd"]`, int64(1735689600),
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

	for _, pair := range [][2]string{{"bbbbbbbbbb", "cccccccccc"}, {"cccccccccc", "dddddddddd"}} {
		var got int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM neighbor_edges WHERE node_a = ? AND node_b = ?`, pair[0], pair[1]).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != 1 {
			t.Errorf("expected interior edge %s<->%s to be persisted; got %d rows", pair[0], pair[1], got)
		}
	}
}

// (test ends here)

