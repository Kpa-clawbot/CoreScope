package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestRolesAnalyticsRecomputerRegistered asserts that the
// /api/analytics/roles endpoint is backed by the steady-state
// analytics recomputer (issue #1256). On master, roles was
// NOT wired into StartAnalyticsRecomputers — every request
// holds s.mu.RLock for the whole compute and triggers a fleet
// clock-skew recompute over 78k transmissions, hanging >60s.
//
// Post-fix: after StartAnalyticsRecomputers, the store exposes
// a recomputer for roles whose Load() returns a populated
// RoleAnalyticsResponse (initial sync compute), and the
// PacketStore.GetAnalyticsRoles() accessor returns from the
// snapshot in sub-millisecond time.
func TestRolesAnalyticsRecomputerRegistered(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)

	stop := store.StartAnalyticsRecomputers(50 * time.Millisecond)
	defer stop()

	// Give the initial synchronous compute a beat to populate.
	time.Sleep(100 * time.Millisecond)

	store.analyticsRecomputerMu.RLock()
	rc := store.recompRoles
	store.analyticsRecomputerMu.RUnlock()
	if rc == nil {
		t.Fatalf("recompRoles not registered after StartAnalyticsRecomputers (issue #1256 not fixed)")
	}
	v := rc.Load()
	if v == nil {
		t.Fatalf("recompRoles snapshot is nil after initial compute")
	}
	if _, ok := v.(RoleAnalyticsResponse); !ok {
		t.Fatalf("recompRoles snapshot type = %T, want RoleAnalyticsResponse", v)
	}

	// Accessor must hit the snapshot path.
	t0 := time.Now()
	resp := store.GetAnalyticsRoles()
	dt := time.Since(t0)
	if dt > 5*time.Millisecond {
		t.Errorf("GetAnalyticsRoles latency = %v, want <5ms (snapshot path)", dt)
	}
	// Just confirm we got the response shape (empty store → empty roles).
	_ = resp
}

// TestRolesHandlerUsesRecomputer is a HTTP-level guard that the
// /api/analytics/roles handler returns from the recomputer snapshot
// quickly even when no clock skew engine state has been primed (the
// hang on staging was: every call drove a full clockSkew.Recompute
// on 78k adverts). With recomputer wired, the handler is an atomic
// pointer load + JSON encode.
func TestRolesHandlerSnapshotLatency(t *testing.T) {
	db := setupTestDB(t)
	defer db.Close()
	store := NewPacketStore(db, nil)
	stop := store.StartAnalyticsRecomputers(50 * time.Millisecond)
	defer stop()
	time.Sleep(100 * time.Millisecond)

	s := &Server{store: store}

	// p99 over 50 reads must be well under 2 s (issue acceptance).
	worst := time.Duration(0)
	for i := 0; i < 50; i++ {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/analytics/roles", nil)
		t0 := time.Now()
		s.handleAnalyticsRoles(rr, req)
		dt := time.Since(t0)
		if dt > worst {
			worst = dt
		}
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rr.Code)
		}
		var out RoleAnalyticsResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("invalid json: %v", err)
		}
	}
	if worst > 100*time.Millisecond {
		t.Fatalf("worst-of-50 handler latency = %v, want <100ms (recomputer snapshot)", worst)
	}
}
