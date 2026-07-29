package main

import "testing"

// ensureNodeChangesTable creates a minimal node_changes table for tests --
// the shared setupTestDB schema predates this feature, same reasoning as
// new_nodes_test.go's ensureInactiveNodesTable.
func ensureNodeChangesTable(t *testing.T, srv *Server) {
	t.Helper()
	if _, err := srv.db.conn.Exec(`CREATE TABLE IF NOT EXISTS node_changes (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		public_key TEXT NOT NULL,
		change_type TEXT NOT NULL,
		old_value TEXT,
		new_value TEXT,
		detected_at TEXT NOT NULL
	)`); err != nil {
		t.Fatalf("create node_changes: %v", err)
	}
}

func insertNodeChangeRow(t *testing.T, srv *Server, pubkey, changeType, oldValue, newValue, detectedAt string) {
	t.Helper()
	if _, err := srv.db.conn.Exec(`INSERT INTO node_changes (public_key, change_type, old_value, new_value, detected_at) VALUES (?,?,?,?,?)`,
		pubkey, changeType, oldValue, newValue, detectedAt); err != nil {
		t.Fatalf("insert node_changes row: %v", err)
	}
}

// TestComputeNodeChanges_OrderedNewestFirst confirms rows come back
// newest-first (by insertion order / id, matching the ingestor's
// append-only write pattern).
func TestComputeNodeChanges_OrderedNewestFirst(t *testing.T) {
	srv, _ := setupTestServer(t)
	ensureNodeChangesTable(t, srv)

	insertNodeChangeRow(t, srv, "changetest0001", "role", "companion", "repeater", "2026-07-29T10:00:00Z")
	insertNodeChangeRow(t, srv, "changetest0002", "name", "Old", "New", "2026-07-29T11:00:00Z")

	entries, err := srv.computeNodeChanges(50)
	if err != nil {
		t.Fatalf("computeNodeChanges: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len(entries) = %d, want 2", len(entries))
	}
	if entries[0].PublicKey != "changetest0002" || entries[1].PublicKey != "changetest0001" {
		t.Errorf("order = [%s, %s], want [changetest0002, changetest0001] (newest first)", entries[0].PublicKey, entries[1].PublicKey)
	}
}

// TestComputeNodeChanges_ResolvesCurrentName confirms the entry's Name is
// resolved fresh from the nodes table, not read from old_value/new_value.
func TestComputeNodeChanges_ResolvesCurrentName(t *testing.T) {
	srv, _ := setupTestServer(t)
	ensureNodeChangesTable(t, srv)

	// aabbccdd11223344 is seeded by seedTestData as "TestRepeater".
	insertNodeChangeRow(t, srv, "aabbccdd11223344", "role", "companion", "repeater", "2026-07-29T10:00:00Z")

	entries, err := srv.computeNodeChanges(50)
	if err != nil {
		t.Fatalf("computeNodeChanges: %v", err)
	}
	if len(entries) != 1 || entries[0].Name != "TestRepeater" {
		t.Errorf("entries = %+v, want Name=TestRepeater resolved from the nodes table", entries)
	}
}

// TestComputeNodeChanges_ResolvesCurrentRole confirms Role is resolved
// live from the nodes table (for the Tools > Node Changes role filter),
// same convention as Name.
func TestComputeNodeChanges_ResolvesCurrentRole(t *testing.T) {
	srv, _ := setupTestServer(t)
	ensureNodeChangesTable(t, srv)

	// aabbccdd11223344 is seeded by seedTestData with role "repeater".
	insertNodeChangeRow(t, srv, "aabbccdd11223344", "name", "Old", "New", "2026-07-29T10:00:00Z")

	entries, err := srv.computeNodeChanges(50)
	if err != nil {
		t.Fatalf("computeNodeChanges: %v", err)
	}
	if len(entries) != 1 || entries[0].Role != "repeater" {
		t.Errorf("entries = %+v, want Role=repeater resolved from the nodes table", entries)
	}
}

// TestComputeNodeChanges_ResolvesCurrentForeign confirms Foreign mirrors
// nodes.foreign_advert for the node's CURRENT state, for the Tools >
// Node Changes All/Domestic/Foreign filter.
func TestComputeNodeChanges_ResolvesCurrentForeign(t *testing.T) {
	srv, _ := setupTestServer(t)
	ensureNodeChangesTable(t, srv)
	ensureInactiveNodesTable(t, srv)

	insertNewNodeRow(t, srv, "foreignchgtest001", "ForeignChg", "repeater", f64(52.5), f64(13.4), "2026-07-29T09:00:00Z")
	if _, err := srv.db.conn.Exec(`UPDATE nodes SET foreign_advert = 1 WHERE public_key = ?`, "foreignchgtest001"); err != nil {
		t.Fatalf("set foreign_advert: %v", err)
	}
	insertNodeChangeRow(t, srv, "foreignchgtest001", "role", "companion", "repeater", "2026-07-29T10:00:00Z")
	// aabbccdd11223344 has no foreign_advert set -- should resolve false.
	insertNodeChangeRow(t, srv, "aabbccdd11223344", "role", "companion", "repeater", "2026-07-29T10:00:00Z")

	entries, err := srv.computeNodeChanges(50)
	if err != nil {
		t.Fatalf("computeNodeChanges: %v", err)
	}
	var foreign, domestic *NodeChangeEntry
	for i := range entries {
		if entries[i].PublicKey == "foreignchgtest001" {
			foreign = &entries[i]
		}
		if entries[i].PublicKey == "aabbccdd11223344" {
			domestic = &entries[i]
		}
	}
	if foreign == nil || !foreign.Foreign {
		t.Errorf("foreignchgtest001 Foreign = %+v, want true", foreign)
	}
	if domestic == nil || domestic.Foreign {
		t.Errorf("aabbccdd11223344 Foreign = %+v, want false", domestic)
	}
}

// TestComputeNodeChanges_ExcludesBlacklisted confirms nodeBlacklist is honored.
func TestComputeNodeChanges_ExcludesBlacklisted(t *testing.T) {
	srv, _ := setupTestServer(t)
	ensureNodeChangesTable(t, srv)

	insertNodeChangeRow(t, srv, "blacklisted0000001", "role", "companion", "repeater", "2026-07-29T10:00:00Z")
	srv.cfg.SetNodeBlacklist([]string{"blacklisted0000001"})

	entries, err := srv.computeNodeChanges(50)
	if err != nil {
		t.Fatalf("computeNodeChanges: %v", err)
	}
	for _, e := range entries {
		if e.PublicKey == "blacklisted0000001" {
			t.Errorf("blacklisted node should be excluded, got: %+v", e)
		}
	}
}

// TestComputeNodeChanges_PositionDistanceParsed confirms a "position"
// change's old_value/new_value ("lat,lon" strings) get parsed into
// DistanceKm.
func TestComputeNodeChanges_PositionDistanceParsed(t *testing.T) {
	srv, _ := setupTestServer(t)
	ensureNodeChangesTable(t, srv)

	// ~11km apart (0.1 degree latitude).
	insertNodeChangeRow(t, srv, "postest00000001", "position", "56.000000,10.000000", "56.100000,10.000000", "2026-07-29T10:00:00Z")

	entries, err := srv.computeNodeChanges(50)
	if err != nil {
		t.Fatalf("computeNodeChanges: %v", err)
	}
	if len(entries) != 1 || entries[0].DistanceKm == nil {
		t.Fatalf("entries = %+v, want a non-nil DistanceKm", entries)
	}
	if *entries[0].DistanceKm < 10 || *entries[0].DistanceKm > 12 {
		t.Errorf("DistanceKm = %v, want ~11", *entries[0].DistanceKm)
	}
}

// TestComputeNodeChanges_NonPositionChangeHasNoDistance confirms
// DistanceKm stays nil for role/name/resurrected changes.
func TestComputeNodeChanges_NonPositionChangeHasNoDistance(t *testing.T) {
	srv, _ := setupTestServer(t)
	ensureNodeChangesTable(t, srv)

	insertNodeChangeRow(t, srv, "roletest0000001", "role", "companion", "repeater", "2026-07-29T10:00:00Z")

	entries, err := srv.computeNodeChanges(50)
	if err != nil {
		t.Fatalf("computeNodeChanges: %v", err)
	}
	if len(entries) != 1 || entries[0].DistanceKm != nil {
		t.Errorf("entries = %+v, want DistanceKm nil for a role change", entries)
	}
}

// TestComputeNodeChanges_LimitRespected confirms the limit param truncates
// after blacklist filtering (not before, which could silently undercount).
func TestComputeNodeChanges_LimitRespected(t *testing.T) {
	srv, _ := setupTestServer(t)
	ensureNodeChangesTable(t, srv)

	for i := 0; i < 5; i++ {
		insertNodeChangeRow(t, srv, "limittest0000000"+string(rune('1'+i)), "role", "companion", "repeater", "2026-07-29T10:00:00Z")
	}
	entries, err := srv.computeNodeChanges(2)
	if err != nil {
		t.Fatalf("computeNodeChanges: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("len(entries) = %d, want 2 (limit)", len(entries))
	}
}
