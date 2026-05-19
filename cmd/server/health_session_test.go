package main

import (
	"database/sql"
	"strings"
	"testing"
	"time"
	_ "modernc.org/sqlite"
)

func newTestHealthDB(t *testing.T) *HealthDB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	hdb := &HealthDB{db: db}
	if err := hdb.migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return hdb
}

func TestHealthDBMigrate(t *testing.T) {
	hdb := newTestHealthDB(t)

	// verify both tables exist
	for _, table := range []string{"health_sessions", "health_receipts"} {
		var name string
		err := hdb.db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", table, err)
		}
	}
}

func TestGenerateCode(t *testing.T) {
	codes := make(map[string]bool)
	for i := 0; i < 100; i++ {
		code, err := generateHealthCode()
		if err != nil {
			t.Fatalf("generateHealthCode: %v", err)
		}
		if !strings.HasPrefix(code, "MHC-") {
			t.Errorf("code %q missing MHC- prefix", code)
		}
		if len(code) != 10 { // "MHC-" + 6 chars
			t.Errorf("code %q wrong length", code)
		}
		codes[code] = true
	}
	if len(codes) < 90 {
		t.Errorf("too many collisions in 100 codes: only %d unique", len(codes))
	}
}

func TestCreateAndGetSession(t *testing.T) {
	hdb := newTestHealthDB(t)
	cfg := &HealthCheckConfig{
		SessionTTLSeconds:      600,
		MaxUsesPerSession:      3,
		ResultRetentionSeconds: 7200,
	}

	sess, err := hdb.CreateSession(cfg, false, nil)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.Status != SessionStatusWaiting {
		t.Errorf("expected status %q, got %q", SessionStatusWaiting, sess.Status)
	}
	if !strings.HasPrefix(sess.Code, "MHC-") {
		t.Errorf("code %q missing prefix", sess.Code)
	}

	got, err := hdb.GetSession(sess.ID)
	if err != nil {
		t.Fatalf("GetSession: %v", err)
	}
	if got.ID != sess.ID {
		t.Errorf("id mismatch: want %q got %q", sess.ID, got.ID)
	}
}

func TestReceiptUpsertAndLifecycle(t *testing.T) {
	hdb := newTestHealthDB(t)
	cfg := &HealthCheckConfig{
		SessionTTLSeconds:      600,
		MaxUsesPerSession:      2,
		ResultRetentionSeconds: 7200,
	}

	sess, _ := hdb.CreateSession(cfg, false, nil)

	r := HealthReceipt{
		SessionID:   sess.ID,
		ObserverKey: "obs-aaa",
		FirstSeenAt: time.Now().Unix(),
		LastSeenAt:  time.Now().Unix(),
		Count:       1,
	}
	if err := hdb.UpsertReceipt(sess.ID, r, "hash1"); err != nil {
		t.Fatalf("UpsertReceipt: %v", err)
	}

	// After first receipt, status should be active and use_count = 1
	got, _ := hdb.GetSession(sess.ID)
	if got.Status != SessionStatusActive {
		t.Errorf("expected active after first receipt, got %q", got.Status)
	}
	if got.UseCount != 1 {
		t.Errorf("expected use_count=1, got %d", got.UseCount)
	}

	// Second unique observer → use_count=2 → exhausted
	r2 := HealthReceipt{
		SessionID:   sess.ID,
		ObserverKey: "obs-bbb",
		FirstSeenAt: time.Now().Unix(),
		LastSeenAt:  time.Now().Unix(),
		Count:       1,
	}
	hdb.UpsertReceipt(sess.ID, r2, "hash2")
	got, _ = hdb.GetSession(sess.ID)
	if got.Status != SessionStatusExhausted {
		t.Errorf("expected exhausted after max_uses, got %q", got.Status)
	}
}

func TestLoadActiveSessions(t *testing.T) {
	hdb := newTestHealthDB(t)
	cfg := &HealthCheckConfig{SessionTTLSeconds: 600, MaxUsesPerSession: 5, ResultRetentionSeconds: 7200}

	hdb.CreateSession(cfg, false, nil)
	hdb.CreateSession(cfg, false, nil)

	active, err := hdb.LoadActiveSessions()
	if err != nil {
		t.Fatalf("LoadActiveSessions: %v", err)
	}
	if len(active) != 2 {
		t.Errorf("expected 2 active sessions, got %d", len(active))
	}
}
