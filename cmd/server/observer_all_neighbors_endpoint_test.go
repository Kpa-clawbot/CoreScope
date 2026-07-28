package main

// Tools > Observer Neighbors: GET /api/observers/neighbors flattens every
// observer's reported direct-neighbor set into one network-wide list,
// requested by dborup as a single place to see this instead of clicking
// into each observer individually.

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestHandleAllObserverNeighbors_JoinsAcrossMultipleObservers(t *testing.T) {
	srv, router := setupTestServer(t)

	// obs1 ("Observer One", "SJC") and obs2 ("Observer Two", "SFO") are
	// already seeded by setupTestServer's shared fixture (seedTestData).
	if _, err := srv.db.conn.Exec(`INSERT INTO nodes (public_key, name, role) VALUES (?, 'Neighbor A', 'repeater')`,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatalf("seed node: %v", err)
	}
	if _, err := srv.db.conn.Exec(`INSERT INTO observer_neighbors (observer_id, neighbor_pubkey, scopes, status, reported_at) VALUES
		('obs1', ?, '#dk', 'responded', '2026-07-28T10:00:00Z'),
		('obs2', ?, '', 'timeout', '2026-07-28T11:00:00Z')`,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"); err != nil {
		t.Fatalf("seed observer_neighbors: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/observers/neighbors", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var body struct {
		Neighbors []struct {
			ObserverID     string  `json:"observerId"`
			ObserverName   *string `json:"observerName"`
			ObserverIATA   *string `json:"observerIata"`
			NeighborPubkey string  `json:"neighborPubkey"`
			NeighborName   *string `json:"neighborName"`
			NeighborRole   *string `json:"neighborRole"`
			Scopes         *string `json:"scopes"`
			Status         string  `json:"status"`
			ReportedAt     string  `json:"reportedAt"`
		} `json:"neighbors"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if len(body.Neighbors) != 2 {
		t.Fatalf("expected 2 rows across both observers, got %d: %+v", len(body.Neighbors), body.Neighbors)
	}

	byObserver := map[string]int{}
	for _, n := range body.Neighbors {
		byObserver[n.ObserverID]++
		if n.ObserverID == "obs1" {
			if n.ObserverName == nil || *n.ObserverName != "Observer One" {
				t.Errorf("obs1 row ObserverName = %v, want 'Observer One'", n.ObserverName)
			}
			if n.ObserverIATA == nil || *n.ObserverIATA != "SJC" {
				t.Errorf("obs1 row ObserverIATA = %v, want 'SJC'", n.ObserverIATA)
			}
			if n.NeighborName == nil || *n.NeighborName != "Neighbor A" {
				t.Errorf("obs1 row NeighborName = %v, want 'Neighbor A' (join against nodes failed)", n.NeighborName)
			}
			if n.NeighborRole == nil || *n.NeighborRole != "repeater" {
				t.Errorf("obs1 row NeighborRole = %v, want 'repeater'", n.NeighborRole)
			}
			if n.Scopes == nil || *n.Scopes != "#dk" {
				t.Errorf("obs1 row Scopes = %v, want '#dk'", n.Scopes)
			}
			if n.ReportedAt != "2026-07-28T10:00:00Z" {
				t.Errorf("obs1 row ReportedAt = %q, want '2026-07-28T10:00:00Z'", n.ReportedAt)
			}
		}
		if n.ObserverID == "obs2" {
			if n.NeighborName != nil {
				t.Errorf("obs2 row NeighborName = %v, want nil (pubkey doesn't resolve to a known node)", n.NeighborName)
			}
			if n.Scopes != nil {
				t.Errorf("obs2 row Scopes = %v, want nil (timeout entry)", n.Scopes)
			}
			if n.Status != "timeout" {
				t.Errorf("obs2 row Status = %q, want 'timeout'", n.Status)
			}
		}
	}
	if byObserver["obs1"] != 1 || byObserver["obs2"] != 1 {
		t.Errorf("byObserver = %+v, want exactly 1 row each for obs1 and obs2", byObserver)
	}
}

func TestHandleAllObserverNeighbors_EmptyReturnsEmptyArrayNotError(t *testing.T) {
	_, router := setupTestServer(t)

	req := httptest.NewRequest("GET", "/api/observers/neighbors", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200 (absence is not a fault), got %d body=%s", w.Code, w.Body.String())
	}

	var body struct {
		Neighbors []interface{} `json:"neighbors"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if body.Neighbors == nil {
		t.Error("neighbors must be an empty array, not null, when no observer has ever reported")
	}
	if len(body.Neighbors) != 0 {
		t.Errorf("expected 0 rows, got %d", len(body.Neighbors))
	}
}

// SeenViaPackets is memoized per distinct observer_id -- verify it's
// computed correctly for TWO different observers in the same response,
// one with packet-path evidence and one without, guarding against a bug
// where the cache accidentally shares state across observers.
func TestHandleAllObserverNeighbors_SeenViaPacketsPerObserver(t *testing.T) {
	srv, router := setupTestServer(t)

	observerWithEvidence := "1111111111111111111111111111111111111111111111111111111111111111"
	observerWithoutEvidence := "2222222222222222222222222222222222222222222222222222222222222222"
	neighborA := "3333333333333333333333333333333333333333333333333333333333333333"
	neighborB := "4444444444444444444444444444444444444444444444444444444444444444"

	if _, err := srv.db.conn.Exec(`INSERT INTO observer_neighbors (observer_id, neighbor_pubkey, scopes, status, reported_at) VALUES
		(?, ?, '', 'responded', '2026-07-28T12:00:00Z'),
		(?, ?, '', 'responded', '2026-07-28T12:00:00Z')`,
		observerWithEvidence, neighborA, observerWithoutEvidence, neighborB); err != nil {
		t.Fatalf("seed observer_neighbors: %v", err)
	}
	if _, err := srv.db.conn.Exec(`INSERT INTO neighbor_edges (node_a, node_b, count, last_seen) VALUES (?, ?, 5, '2026-07-28T11:00:00Z')`,
		observerWithEvidence, neighborA); err != nil {
		t.Fatalf("seed neighbor_edges: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/observers/neighbors", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var body struct {
		Neighbors []struct {
			ObserverID     string `json:"observerId"`
			SeenViaPackets bool   `json:"seenViaPackets"`
		} `json:"neighbors"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	byObserver := map[string]bool{}
	for _, n := range body.Neighbors {
		byObserver[n.ObserverID] = n.SeenViaPackets
	}
	if !byObserver[observerWithEvidence] {
		t.Errorf("seenViaPackets for %s = false, want true", observerWithEvidence)
	}
	if byObserver[observerWithoutEvidence] {
		t.Errorf("seenViaPackets for %s = true, want false", observerWithoutEvidence)
	}
}

func TestHandleAllObserverNeighbors_ExcludesBlacklistedObserver(t *testing.T) {
	srv, router := setupTestServer(t)
	srv.cfg.ObserverBlacklist = []string{"blacklisted-obs"}

	if _, err := srv.db.conn.Exec(`INSERT INTO observer_neighbors (observer_id, neighbor_pubkey, scopes, status, reported_at) VALUES
		('blacklisted-obs', ?, '', 'responded', '2026-07-28T13:00:00Z'),
		('normal-obs', ?, '', 'responded', '2026-07-28T13:00:00Z')`,
		"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"); err != nil {
		t.Fatalf("seed observer_neighbors: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/observers/neighbors", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var body struct {
		Neighbors []struct {
			ObserverID string `json:"observerId"`
		} `json:"neighbors"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if len(body.Neighbors) != 1 || body.Neighbors[0].ObserverID != "normal-obs" {
		t.Fatalf("expected only normal-obs's row, got %+v", body.Neighbors)
	}
}

// dborup: "kan vi have en panel med scopes vi ikke kender på corescope
// som observer neighbors har fundet" -- unknownScopes surfaces
// region-scope names seen in reported neighbor scope lists that aren't
// part of the deployment's configured hashRegions.
func TestHandleAllObserverNeighbors_UnknownScopes(t *testing.T) {
	srv, router := setupTestServer(t)
	srv.cfg.HashRegions = []string{"dk"}

	if _, err := srv.db.conn.Exec(`INSERT INTO observer_neighbors (observer_id, neighbor_pubkey, scopes, status, reported_at) VALUES
		('obs1', ?, '*,#dk,#dk-storkbh', 'responded', '2026-07-28T14:00:00Z')`,
		"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"); err != nil {
		t.Fatalf("seed observer_neighbors: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/observers/neighbors", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("expected 200, got %d body=%s", w.Code, w.Body.String())
	}

	var body struct {
		UnknownScopes []struct {
			Scope string `json:"scope"`
			Count int    `json:"count"`
		} `json:"unknownScopes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if len(body.UnknownScopes) != 1 {
		t.Fatalf("expected 1 unknown scope (#dk-storkbh; #dk is configured, * is the wildcard), got %+v", body.UnknownScopes)
	}
	if body.UnknownScopes[0].Scope != "#dk-storkbh" || body.UnknownScopes[0].Count != 1 {
		t.Errorf("UnknownScopes[0] = %+v, want {#dk-storkbh 1}", body.UnknownScopes[0])
	}
}
