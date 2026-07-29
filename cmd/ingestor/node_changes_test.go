package main

import (
	"testing"
)

type nodeChangeRow struct {
	publicKey, changeType, oldValue, newValue string
}

func fetchNodeChanges(t *testing.T, s *Store, pubKey string) []nodeChangeRow {
	t.Helper()
	rows, err := s.db.Query(`SELECT public_key, change_type, old_value, new_value FROM node_changes WHERE public_key = ? ORDER BY id`, pubKey)
	if err != nil {
		t.Fatalf("query node_changes: %v", err)
	}
	defer rows.Close()
	var out []nodeChangeRow
	for rows.Next() {
		var r nodeChangeRow
		if err := rows.Scan(&r.publicKey, &r.changeType, &r.oldValue, &r.newValue); err != nil {
			t.Fatalf("scan node_changes row: %v", err)
		}
		out = append(out, r)
	}
	return out
}

// TestUpsertNode_LogsRoleChange confirms a role change between two ADVERTs
// for the same pubkey is recorded in node_changes.
func TestUpsertNode_LogsRoleChange(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.UpsertNode("rolechange0001", "RoleChanger", "companion", nil, nil, "2026-07-29T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertNode("rolechange0001", "RoleChanger", "repeater", nil, nil, "2026-07-29T11:00:00Z"); err != nil {
		t.Fatal(err)
	}

	changes := fetchNodeChanges(t, s, "rolechange0001")
	if len(changes) != 1 {
		t.Fatalf("len(changes) = %d, want 1, got: %+v", len(changes), changes)
	}
	if changes[0].changeType != "role" || changes[0].oldValue != "companion" || changes[0].newValue != "repeater" {
		t.Errorf("change = %+v, want role companion->repeater", changes[0])
	}
}

// TestUpsertNode_LogsNameChange confirms a name change is recorded.
func TestUpsertNode_LogsNameChange(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.UpsertNode("namechange0001", "OldName", "repeater", nil, nil, "2026-07-29T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertNode("namechange0001", "NewName", "repeater", nil, nil, "2026-07-29T11:00:00Z"); err != nil {
		t.Fatal(err)
	}

	changes := fetchNodeChanges(t, s, "namechange0001")
	if len(changes) != 1 {
		t.Fatalf("len(changes) = %d, want 1, got: %+v", len(changes), changes)
	}
	if changes[0].changeType != "name" || changes[0].oldValue != "OldName" || changes[0].newValue != "NewName" {
		t.Errorf("change = %+v, want name OldName->NewName", changes[0])
	}
}

// TestUpsertNode_LogsPositionChangeAboveThreshold confirms a move of >=1km
// is recorded, matching nodeChangePositionThresholdKm.
func TestUpsertNode_LogsPositionChangeAboveThreshold(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	lat1, lon1 := 56.0, 10.0
	lat2, lon2 := 56.1, 10.0 // ~11km north
	if err := s.UpsertNode("poschange0001", "Mover", "repeater", &lat1, &lon1, "2026-07-29T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertNode("poschange0001", "Mover", "repeater", &lat2, &lon2, "2026-07-29T11:00:00Z"); err != nil {
		t.Fatal(err)
	}

	changes := fetchNodeChanges(t, s, "poschange0001")
	if len(changes) != 1 {
		t.Fatalf("len(changes) = %d, want 1, got: %+v", len(changes), changes)
	}
	if changes[0].changeType != "position" {
		t.Errorf("changeType = %q, want position", changes[0].changeType)
	}
}

// TestUpsertNode_NoPositionChangeBelowThreshold confirms GPS jitter under
// 1km does NOT get logged as a position change.
func TestUpsertNode_NoPositionChangeBelowThreshold(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	lat1, lon1 := 56.0, 10.0
	lat2, lon2 := 56.0005, 10.0 // ~55m -- jitter, not a real move
	if err := s.UpsertNode("jitter0000001", "Steady", "repeater", &lat1, &lon1, "2026-07-29T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertNode("jitter0000001", "Steady", "repeater", &lat2, &lon2, "2026-07-29T11:00:00Z"); err != nil {
		t.Fatal(err)
	}

	changes := fetchNodeChanges(t, s, "jitter0000001")
	if len(changes) != 0 {
		t.Errorf("len(changes) = %d, want 0 (jitter should not be logged), got: %+v", len(changes), changes)
	}
}

// TestUpsertNode_NoChangeLoggedForBrandNewNode confirms the very first
// ADVERT for a never-before-seen pubkey (not in nodes, not in
// inactive_nodes) doesn't spuriously log a change -- that's what the New
// Nodes feed is for, not this audit log.
func TestUpsertNode_NoChangeLoggedForBrandNewNode(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.UpsertNode("brandnew0000001", "Fresh", "repeater", nil, nil, "2026-07-29T10:00:00Z"); err != nil {
		t.Fatal(err)
	}

	changes := fetchNodeChanges(t, s, "brandnew0000001")
	if len(changes) != 0 {
		t.Errorf("len(changes) = %d, want 0 for a genuinely new node, got: %+v", len(changes), changes)
	}
}

// TestUpsertNode_NoChangeLoggedWhenAdvertOmitsFields confirms a bare
// re-advert with no name/location (hasName/hasLocation both false) never
// gets compared against the existing name/position -- an omitted field is
// not "changed to empty".
func TestUpsertNode_NoChangeLoggedWhenAdvertOmitsFields(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	lat, lon := 56.0, 10.0
	if err := s.UpsertNode("bareadvert00001", "HasName", "repeater", &lat, &lon, "2026-07-29T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	// Bare re-advert: empty name, nil lat/lon (as if hasName/hasLocation
	// were both false this time), same role.
	if err := s.UpsertNode("bareadvert00001", "", "repeater", nil, nil, "2026-07-29T11:00:00Z"); err != nil {
		t.Fatal(err)
	}

	changes := fetchNodeChanges(t, s, "bareadvert00001")
	if len(changes) != 0 {
		t.Errorf("len(changes) = %d, want 0 (omitted fields must not be compared), got: %+v", len(changes), changes)
	}
}

// TestUpsertNode_NoChangeLoggedWhenValuesIdentical confirms a routine
// re-advert with unchanged name/role/position logs nothing.
func TestUpsertNode_NoChangeLoggedWhenValuesIdentical(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	lat, lon := 56.0, 10.0
	if err := s.UpsertNode("identical000001", "SameName", "repeater", &lat, &lon, "2026-07-29T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertNode("identical000001", "SameName", "repeater", &lat, &lon, "2026-07-29T11:00:00Z"); err != nil {
		t.Fatal(err)
	}

	changes := fetchNodeChanges(t, s, "identical000001")
	if len(changes) != 0 {
		t.Errorf("len(changes) = %d, want 0 for an unchanged re-advert, got: %+v", len(changes), changes)
	}
}

// TestUpsertNode_LogsResurrection confirms a pubkey that exists in
// inactive_nodes (previously pruned for going quiet) but NOT in the live
// nodes table gets a "resurrected" entry when it advertises again, rather
// than being silently treated as brand new.
func TestUpsertNode_LogsResurrection(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.db.Exec(`INSERT INTO inactive_nodes (public_key, name, role, lat, lon, last_seen, first_seen) VALUES (?,?,?,?,?,?,?)`,
		"resurrected0001", "OldTimer", "repeater", 56.0, 10.0, "2026-06-01T00:00:00Z", "2025-01-01T00:00:00Z"); err != nil {
		t.Fatalf("seed inactive_nodes: %v", err)
	}

	if err := s.UpsertNode("resurrected0001", "OldTimer", "repeater", nil, nil, "2026-07-29T10:00:00Z"); err != nil {
		t.Fatal(err)
	}

	changes := fetchNodeChanges(t, s, "resurrected0001")
	if len(changes) != 1 {
		t.Fatalf("len(changes) = %d, want 1, got: %+v", len(changes), changes)
	}
	if changes[0].changeType != "resurrected" || changes[0].oldValue != "2026-06-01T00:00:00Z" {
		t.Errorf("change = %+v, want resurrected with oldValue = inactive_nodes.last_seen", changes[0])
	}
}
