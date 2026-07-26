package main

// #1865 follow-up "Direct Neighbors" panel: GET /api/observers/{id}/neighbors
// serves the observer's own firmware-reported zero-hop neighbor set.

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestHandleObserverNeighbors_ReturnsJoinedNames(t *testing.T) {
	srv, router := setupTestServer(t)

	if _, err := srv.db.conn.Exec(`INSERT INTO observer_neighbors (observer_id, neighbor_pubkey, scopes, status, reported_at) VALUES (?, ?, ?, ?, ?)`,
		"obs1", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "#dk", "responded", "2026-07-26T10:00:00Z"); err != nil {
		t.Fatalf("seed observer_neighbors: %v", err)
	}
	if _, err := srv.db.conn.Exec(`INSERT INTO nodes (public_key, name, role) VALUES (?, ?, ?)`,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "Neighbor Node", "repeater"); err != nil {
		t.Fatalf("seed node: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/observers/obs1/neighbors", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var body struct {
		Neighbors []struct {
			Pubkey string  `json:"pubkey"`
			Name   *string `json:"name"`
			Role   *string `json:"role"`
			Scopes *string `json:"scopes"`
			Status string  `json:"status"`
		} `json:"neighbors"`
		ReportedAt string `json:"reportedAt"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if len(body.Neighbors) != 1 {
		t.Fatalf("expected 1 neighbor, got %d", len(body.Neighbors))
	}
	n := body.Neighbors[0]
	if n.Name == nil || *n.Name != "Neighbor Node" {
		t.Errorf("Name = %v, want 'Neighbor Node' (join against nodes failed)", n.Name)
	}
	if n.Role == nil || *n.Role != "repeater" {
		t.Errorf("Role = %v, want 'repeater'", n.Role)
	}
	if n.Scopes == nil || *n.Scopes != "#dk" {
		t.Errorf("Scopes = %v, want '#dk'", n.Scopes)
	}
	if n.Status != "responded" {
		t.Errorf("Status = %q, want 'responded'", n.Status)
	}
	if body.ReportedAt != "2026-07-26T10:00:00Z" {
		t.Errorf("ReportedAt = %q, want '2026-07-26T10:00:00Z'", body.ReportedAt)
	}
}

func TestHandleObserverNeighbors_NeverReportedReturnsEmptyNotError(t *testing.T) {
	srv, router := setupTestServer(t)
	_ = srv

	req := httptest.NewRequest("GET", "/api/observers/obs1/neighbors", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200 (absence is not a fault), got %d body=%s", w.Code, w.Body.String())
	}

	var body struct {
		Neighbors  []interface{} `json:"neighbors"`
		ReportedAt string        `json:"reportedAt"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if body.Neighbors == nil {
		t.Error("neighbors must be an empty array, not null, when the observer never reported")
	}
	if len(body.Neighbors) != 0 {
		t.Errorf("expected 0 neighbors, got %d", len(body.Neighbors))
	}
}

func TestHandleObserverNeighbors_UnresolvedPubkeyHasNilNameAndRole(t *testing.T) {
	srv, router := setupTestServer(t)

	if _, err := srv.db.conn.Exec(`INSERT INTO observer_neighbors (observer_id, neighbor_pubkey, scopes, status, reported_at) VALUES (?, ?, ?, ?, ?)`,
		"obs1", "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", "", "timeout", "2026-07-26T11:00:00Z"); err != nil {
		t.Fatalf("seed observer_neighbors: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/observers/obs1/neighbors", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var body struct {
		Neighbors []struct {
			Pubkey string  `json:"pubkey"`
			Name   *string `json:"name"`
			Scopes *string `json:"scopes"`
			Status string  `json:"status"`
		} `json:"neighbors"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if len(body.Neighbors) != 1 {
		t.Fatalf("expected 1 neighbor, got %d", len(body.Neighbors))
	}
	n := body.Neighbors[0]
	if n.Name != nil {
		t.Errorf("Name = %v, want nil for a pubkey with no matching node", n.Name)
	}
	if n.Scopes != nil {
		t.Errorf("Scopes = %v, want nil for a timeout entry", n.Scopes)
	}
	if n.Status != "timeout" {
		t.Errorf("Status = %q, want 'timeout'", n.Status)
	}
}

// More ambitious use, requested by dborup after the panel shipped:
// cross-reference the firmware-confirmed direct neighbor against the
// packet-derived neighbor_edges graph and flag mismatches.
func TestHandleObserverNeighbors_SeenViaPacketsCrossReference(t *testing.T) {
	srv, router := setupTestServer(t)

	observerPubkey := "1111111111111111111111111111111111111111111111111111111111111111"
	confirmedAndSeen := "2222222222222222222222222222222222222222222222222222222222222222"
	confirmedButNeverSeen := "3333333333333333333333333333333333333333333333333333333333333333"

	if _, err := srv.db.conn.Exec(`INSERT INTO observer_neighbors (observer_id, neighbor_pubkey, scopes, status, reported_at) VALUES
		(?, ?, '', 'responded', '2026-07-26T12:00:00Z'),
		(?, ?, '', 'responded', '2026-07-26T12:00:00Z')`,
		observerPubkey, confirmedAndSeen, observerPubkey, confirmedButNeverSeen); err != nil {
		t.Fatalf("seed observer_neighbors: %v", err)
	}
	// Packet-path evidence exists for observerPubkey<->confirmedAndSeen but
	// NOT for observerPubkey<->confirmedButNeverSeen.
	if _, err := srv.db.conn.Exec(`INSERT INTO neighbor_edges (node_a, node_b, count, last_seen) VALUES (?, ?, 5, '2026-07-26T11:00:00Z')`,
		observerPubkey, confirmedAndSeen); err != nil {
		t.Fatalf("seed neighbor_edges: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/observers/"+observerPubkey+"/neighbors", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var body struct {
		Neighbors []struct {
			Pubkey         string `json:"pubkey"`
			SeenViaPackets bool   `json:"seenViaPackets"`
		} `json:"neighbors"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	got := map[string]bool{}
	for _, n := range body.Neighbors {
		got[n.Pubkey] = n.SeenViaPackets
	}
	if !got[confirmedAndSeen] {
		t.Errorf("seenViaPackets for %s = false, want true (edge exists in neighbor_edges)", confirmedAndSeen)
	}
	if got[confirmedButNeverSeen] {
		t.Errorf("seenViaPackets for %s = true, want false (no edge in neighbor_edges)", confirmedButNeverSeen)
	}
}

// The edge might be stored with the observer as node_b rather than node_a
// (canonEdge orders node_a<=node_b) -- must still be detected.
func TestHandleObserverNeighbors_SeenViaPacketsChecksBothEdgeColumns(t *testing.T) {
	srv, router := setupTestServer(t)

	observerPubkey := "9999999999999999999999999999999999999999999999999999999999999999"
	neighborPubkey := "1000000000000000000000000000000000000000000000000000000000000000"

	if _, err := srv.db.conn.Exec(`INSERT INTO observer_neighbors (observer_id, neighbor_pubkey, scopes, status, reported_at) VALUES (?, ?, '', 'responded', '2026-07-26T12:00:00Z')`,
		observerPubkey, neighborPubkey); err != nil {
		t.Fatalf("seed observer_neighbors: %v", err)
	}
	// neighborPubkey < observerPubkey lexicographically, so canonEdge would
	// store it as node_a=neighborPubkey, node_b=observerPubkey.
	if _, err := srv.db.conn.Exec(`INSERT INTO neighbor_edges (node_a, node_b, count, last_seen) VALUES (?, ?, 1, '2026-07-26T11:00:00Z')`,
		neighborPubkey, observerPubkey); err != nil {
		t.Fatalf("seed neighbor_edges: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/observers/"+observerPubkey+"/neighbors", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var body struct {
		Neighbors []struct {
			Pubkey         string `json:"pubkey"`
			SeenViaPackets bool   `json:"seenViaPackets"`
		} `json:"neighbors"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if len(body.Neighbors) != 1 || !body.Neighbors[0].SeenViaPackets {
		t.Fatalf("expected 1 neighbor with seenViaPackets=true (edge stored as node_a), got %+v", body.Neighbors)
	}
}
