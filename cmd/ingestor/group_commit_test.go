package main

import (
	"fmt"
	"testing"
)

// makePacket returns a minimal valid PacketData with a unique hash so
// each call is treated as a distinct transmission by InsertTransmission.
func makePacket(i int) *PacketData {
	snr := 1.0
	rssi := -90.0
	return &PacketData{
		RawHex:         fmt.Sprintf("AABB%04X", i),
		Timestamp:      "2026-05-01T00:00:00Z",
		ObserverID:     "obsGC",
		Hash:           fmt.Sprintf("gchash%010d", i),
		RouteType:      2,
		PayloadType:    2,
		PayloadVersion: 0,
		PathJSON:       "[]",
		DecodedJSON:    `{"type":"TXT_MSG"}`,
		SNR:            &snr,
		RSSI:           &rssi,
	}
}

// TestGroupCommit_BatchesInsertsIntoOneTx verifies M1 behavior: with
// groupCommitMs > 0, 50 InsertTransmission calls should produce ZERO
// commits until FlushGroupTx is called, then exactly 1 commit.
func TestGroupCommit_BatchesInsertsIntoOneTx(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.UpsertObserver("obsGC", "GC Observer", "SJC", nil); err != nil {
		t.Fatal(err)
	}

	// Enable group commit with a wide window so the test ticker doesn't fire.
	s.SetGroupCommit(60_000, 1000)

	startFlushes := s.Stats.GroupCommitFlushes.Load()

	for i := 0; i < 50; i++ {
		if _, err := s.InsertTransmission(makePacket(i)); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	// Before flush, no commits should have occurred. (max=1000, count=50.)
	if got := s.Stats.GroupCommitFlushes.Load() - startFlushes; got != 0 {
		t.Fatalf("flushes before manual flush: got %d, want 0", got)
	}

	// Manual flush — exactly one commit for all 50 inserts.
	if err := s.FlushGroupTx(); err != nil {
		t.Fatalf("FlushGroupTx: %v", err)
	}
	if got := s.Stats.GroupCommitFlushes.Load() - startFlushes; got != 1 {
		t.Fatalf("flushes after manual flush: got %d, want 1", got)
	}

	// All 50 rows must be visible after commit.
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM transmissions WHERE hash LIKE 'gchash%'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 50 {
		t.Fatalf("transmissions after flush: got %d, want 50", n)
	}
	if err := s.db.QueryRow("SELECT COUNT(*) FROM observations").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 50 {
		t.Fatalf("observations after flush: got %d, want 50", n)
	}
}

// TestGroupCommit_Disabled verifies that with groupCommitMs == 0, every
// InsertTransmission commits immediately (current behavior preserved) and
// the GroupCommitFlushes counter never advances.
func TestGroupCommit_Disabled(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.UpsertObserver("obsGC", "GC Observer", "SJC", nil); err != nil {
		t.Fatal(err)
	}

	// Explicitly disable.
	s.SetGroupCommit(0, 1000)

	startFlushes := s.Stats.GroupCommitFlushes.Load()

	for i := 0; i < 5; i++ {
		if _, err := s.InsertTransmission(makePacket(i)); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
		// Each insert is immediately visible — no flush required.
		var n int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM transmissions WHERE hash LIKE 'gchash%'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != i+1 {
			t.Fatalf("after insert %d: got %d transmissions, want %d", i, n, i+1)
		}
	}

	if got := s.Stats.GroupCommitFlushes.Load() - startFlushes; got != 0 {
		t.Fatalf("flushes with group commit disabled: got %d, want 0", got)
	}
}

// TestGroupCommit_MaxRowsForcesEarlyFlush verifies that exceeding the
// row cap triggers an immediate flush even before the ticker fires.
func TestGroupCommit_MaxRowsForcesEarlyFlush(t *testing.T) {
	s, err := OpenStore(tempDBPath(t))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.UpsertObserver("obsGC", "GC Observer", "SJC", nil); err != nil {
		t.Fatal(err)
	}

	// Window large; cap small (3) so the 4th insert should flush.
	s.SetGroupCommit(60_000, 3)

	startFlushes := s.Stats.GroupCommitFlushes.Load()

	for i := 0; i < 7; i++ {
		if _, err := s.InsertTransmission(makePacket(i)); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	// 7 inserts with cap 3 → 2 auto-flushes (after 3 and after 6); 1 still pending.
	if got := s.Stats.GroupCommitFlushes.Load() - startFlushes; got != 2 {
		t.Fatalf("auto-flushes: got %d, want 2", got)
	}

	if err := s.FlushGroupTx(); err != nil {
		t.Fatal(err)
	}
	if got := s.Stats.GroupCommitFlushes.Load() - startFlushes; got != 3 {
		t.Fatalf("flushes after final manual flush: got %d, want 3", got)
	}
}
