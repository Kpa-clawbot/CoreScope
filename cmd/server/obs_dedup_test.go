package main

import (
	"fmt"
	"testing"
)

func addTestObs(tx *StoreTx, observerID, pathJSON string) bool {
	if tx.hasObservation(observerID, pathJSON) {
		return false
	}
	tx.addObservation(&StoreObs{ObserverID: observerID, PathJSON: pathJSON})
	return true
}

// TestObsDedupCorrectness: the same observer with the same path is a
// duplicate; a new path or a new observer is not.
func TestObsDedupCorrectness(t *testing.T) {
	tx := &StoreTx{ID: 1, Hash: "abc123"}
	for i := 0; i < 5; i++ {
		if !addTestObs(tx, fmt.Sprintf("obs-%d", i), fmt.Sprintf(`["path-%d"]`, i)) {
			t.Fatalf("observation %d should not be a duplicate", i)
		}
	}
	if tx.ObservationCount != 5 || len(tx.Observations) != 5 {
		t.Fatalf("expected 5 observations, got count=%d len=%d", tx.ObservationCount, len(tx.Observations))
	}
	for i := 0; i < 5; i++ {
		if !tx.hasObservation(fmt.Sprintf("obs-%d", i), fmt.Sprintf(`["path-%d"]`, i)) {
			t.Fatalf("observation %d should be detected as duplicate", i)
		}
	}
	if tx.hasObservation("obs-0", `["different-path"]`) {
		t.Fatal("different path should not be a duplicate")
	}
	if tx.hasObservation("obs-new", `["path-0"]`) {
		t.Fatal("different observer should not be a duplicate")
	}
}

// TestObsDedupSmallTxCarriesNoMaps: most transmissions have a handful of
// observations, and two maps each cost them a few hundred bytes. Below the
// threshold the checks scan tx.Observations instead.
func TestObsDedupSmallTxCarriesNoMaps(t *testing.T) {
	tx := &StoreTx{ID: 1}
	for i := 0; i < obsIndexThreshold-1; i++ {
		addTestObs(tx, fmt.Sprintf("obs-%d", i%4), fmt.Sprintf(`["p%d"]`, i))
	}
	if len(tx.Observations) != obsIndexThreshold-1 {
		t.Fatalf("len = %d, want %d", len(tx.Observations), obsIndexThreshold-1)
	}
	if tx.obsKeys != nil || tx.observerSet != nil {
		t.Fatal("a transmission under the threshold should not carry dedup maps")
	}
	if tx.UniqueObserverCount != 4 {
		t.Fatalf("UniqueObserverCount = %d, want 4", tx.UniqueObserverCount)
	}
}

// TestObsDedupAcrossTheThreshold: past the threshold the maps take over and
// give the same answers as the scan did.
func TestObsDedupAcrossTheThreshold(t *testing.T) {
	tx := &StoreTx{ID: 1}
	n := obsIndexThreshold * 3
	for i := 0; i < n; i++ {
		if !addTestObs(tx, fmt.Sprintf("obs-%d", i%10), fmt.Sprintf(`["p%d"]`, i)) {
			t.Fatalf("observation %d rejected", i)
		}
	}
	if tx.obsKeys == nil || tx.observerSet == nil {
		t.Fatal("a busy transmission should index its observations")
	}
	if tx.UniqueObserverCount != 10 || tx.ObservationCount != n {
		t.Fatalf("unique=%d count=%d, want 10 and %d", tx.UniqueObserverCount, tx.ObservationCount, n)
	}
	for i := 0; i < n; i++ {
		if addTestObs(tx, fmt.Sprintf("obs-%d", i%10), fmt.Sprintf(`["p%d"]`, i)) {
			t.Fatalf("duplicate %d accepted after the threshold", i)
		}
	}
	if !addTestObs(tx, "obs-new", `["p0"]`) || tx.UniqueObserverCount != 11 {
		t.Fatalf("new observer: unique=%d, want 11", tx.UniqueObserverCount)
	}
}

// TestObsDedupEmptyObserverNotCounted: an observation without an observer
// ID is kept but does not count as a distinct observer.
func TestObsDedupEmptyObserverNotCounted(t *testing.T) {
	tx := &StoreTx{ID: 1}
	addTestObs(tx, "", `["a"]`)
	addTestObs(tx, "", `["b"]`)
	if tx.UniqueObserverCount != 0 || tx.ObservationCount != 2 {
		t.Fatalf("unique=%d count=%d, want 0 and 2", tx.UniqueObserverCount, tx.ObservationCount)
	}
}

func TestObsDedupUnionsRelayEvidenceAcrossThreshold(t *testing.T) {
	for _, count := range []int{4, obsIndexThreshold, obsIndexThreshold * 2} {
		t.Run(fmt.Sprintf("observations=%d", count), func(t *testing.T) {
			tx := &StoreTx{}
			for i := 0; i < count; i++ {
				addTestObs(tx, fmt.Sprintf("obs-%d", i), `["4F2A"]`)
			}
			id := fmt.Sprintf("obs-%d", count-1)
			for _, n := range []uint8{4, 0, 4} {
				obs := tx.mergeObservationEvidence(id, `["4F2A"]`, n)
				if obs != tx.Observations[count-1] || obs.relayHopHexLen != 4 {
					t.Fatal("dedup must upgrade the existing observation and never erase evidence")
				}
			}
			if tx.mergeObservationEvidence("absent", `["4F2A"]`, 4) != nil || len(tx.Observations) != count {
				t.Fatal("evidence union must not create observations")
			}
		})
	}
}

// BenchmarkObsDedup measures a miss (the common case on ingest) at sizes
// either side of the threshold.
func BenchmarkObsDedup(b *testing.B) {
	for _, obsCount := range []int{4, 16, 40, 63, 64, 500} {
		b.Run(fmt.Sprintf("obs=%d", obsCount), func(b *testing.B) {
			tx := &StoreTx{ID: 1}
			for i := 0; i < obsCount; i++ {
				addTestObs(tx, fmt.Sprintf("%064d", i), fmt.Sprintf(`["hop-%d"]`, i))
			}
			newID := fmt.Sprintf("%064d", obsCount+1)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = tx.hasObservation(newID, `["new-path"]`)
			}
		})
	}
}
