package main

import (
	"fmt"
	"testing"
	"time"
	"unsafe"
)

// The bounded loader must use the same compact observation representation as
// live ingest and the unlimited loader, including both sides of the lazy-map
// threshold. A second window load merges no duplicate observations.
func TestPacketRangeUsesSharedObservationRepresentation(t *testing.T) {
	for _, count := range []int{obsIndexThreshold - 1, obsIndexThreshold + 1} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			db := startupMemoryFixture(t, 2, count, true)
			s := NewPacketStore(db, &PacketStoreConfig{MaxMemoryMB: 4})
			for pass := 0; pass < 2; pass++ {
				if _, err := s.loadPacketRange(time.Time{}, time.Time{}, 1, nil); err != nil {
					t.Fatal(err)
				}
				if len(s.packets) != 2 || s.totalObs != 2*count {
					t.Fatalf("pass %d loaded %d packets and %d observations", pass, len(s.packets), s.totalObs)
				}
				for _, tx := range s.packets {
					if tx.ObservationCount != count || tx.UniqueObserverCount != count {
						t.Fatalf("counts: %d/%d, want %d", tx.ObservationCount, tx.UniqueObserverCount, count)
					}
					if (tx.obsKeys != nil) != (count >= obsIndexThreshold) {
						t.Fatal("bounded loader bypassed lazy dedup maps")
					}
					for _, obs := range tx.Observations {
						if !tx.hasObservation(obs.ObserverID, obs.PathJSON) {
							t.Fatal("loaded observation missing from dedup lookup")
						}
					}
				}
			}
			a, b := s.packets[0].Observations[0], s.packets[1].Observations[0]
			if a.ObserverName != b.ObserverName || unsafe.StringData(a.ObserverName) != unsafe.StringData(b.ObserverName) {
				t.Fatal("bounded loader did not share repeated observer names")
			}
		})
	}
}
