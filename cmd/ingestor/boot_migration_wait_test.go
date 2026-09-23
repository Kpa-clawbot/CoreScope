package main

import (
	"bytes"
	"log"
	"strings"
	"testing"
)

// Issue #2065: the race detector failed on master with
//
//	Write  by RunAsyncMigration.func1  (async_migration.go, log.Printf)
//	Read   by TestHandleMessageDecodeErrorLog_PII_Issue1211  (buf.String())
//
// OpenStore schedules two async migrations whose goroutines log while they
// run, and that test points the standard logger at a bytes.Buffer and reads
// it. Both touch one buffer with no synchronisation.
//
// Close() already waits on backfillWg, so the goroutines do not outlive their
// test: the race is inside a single test, not across tests. newTestStore
// therefore waits for the boot migrations before handing the store over, so
// no test body can run while one is in flight.
//
// These two tests pin that, deterministically — the race detector only catches
// the fault when the scheduler cooperates, which is why it sat latent from
// 2026-09-03 until it surfaced three weeks later.

// TestNewTestStoreWaitsForBootMigrations fails if the wait is removed from the
// helper. tx_last_seen_backfill_v1 is scheduled unconditionally by OpenStore
// (db.go), so on a fresh temp database it is always pending at that moment and
// can only read "done" here if something waited.
func TestNewTestStoreWaitsForBootMigrations(t *testing.T) {
	s := newTestStore(t)

	status, err := s.AsyncMigrationStatus("tx_last_seen_backfill_v1")
	if err != nil {
		t.Fatalf("AsyncMigrationStatus: %v", err)
	}
	if status != "done" {
		t.Errorf("tx_last_seen_backfill_v1 = %q immediately after newTestStore, want %q — "+
			"the helper must wait for the boot migrations, or a test capturing the standard "+
			"logger races their goroutines (#2065)", status, "done")
	}
}

// TestCapturedLogIsFreeOfMigrationOutput is the assertion from the failing
// test's own point of view: once the store is handed over, nothing else is
// writing to the logger, so a captured buffer holds only what the test put
// there. Without the wait this is the buffer two goroutines fight over.
func TestCapturedLogIsFreeOfMigrationOutput(t *testing.T) {
	newTestStore(t)

	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)

	log.Printf("sentinel line")

	out := buf.String()
	if !strings.Contains(out, "sentinel line") {
		t.Fatalf("capture did not work at all; got %q", out)
	}
	// "[migration/async]" is what the two boot migrations print. Their
	// appearance here means a goroutine was still running when the capture
	// started, which is the race, not merely untidy output.
	if strings.Contains(out, "[migration/async]") || strings.Contains(out, "[async-migration]") {
		t.Errorf("migration output landed in a captured buffer: %q", out)
	}
}
