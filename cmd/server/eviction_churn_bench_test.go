package main

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Synthetic live-ingest bookkeeping uses the real membership, observation and byte
// accounting helpers. No fixture keeps packet pointers outside the store.
func appendEvictionChurnBatch(s *PacketStore, firstID, count, fanout int, now time.Time) {
	const hops = 8
	relays := make([]string, 512)
	for i := range relays {
		relays[i] = fmt.Sprintf("%02x%062x", i%256, i)
	}
	origin := strings.Repeat("f", 64) // distinct from every resolved relay
	stamp := now.UTC().Format(time.RFC3339)
	hopsSeen := make(map[string]bool)
	for id := firstID; id < firstID+count; id++ {
		pt := 5
		tx := &StoreTx{
			ID: id, Hash: fmt.Sprintf("%064x", id), RawHex: strings.Repeat("ab", 64),
			FirstSeen: stamp, LatestSeen: stamp, PayloadType: &pt,
			DecodedJSON: fmt.Sprintf(`{"pubKey":%q,"text":"synthetic churn packet %d"}`, origin, id),
		}
		s.packets = append(s.packets, tx)
		s.byHash[tx.Hash], s.byTxID[id] = tx, tx
		s.byPayloadType[pt] = append(s.byPayloadType[pt], tx)
		s.indexByNode(tx)
		for j := 0; j < fanout; j++ {
			pks, rawHops := make([]string, hops), make([]string, hops)
			for k := range pks {
				pks[k] = relays[(id*17+j*19+k*13)%len(relays)]
				rawHops[k] = fmt.Sprintf("%q", pks[k][:2])
			}
			path := "[" + strings.Join(rawHops, ",") + "]"
			observer := fmt.Sprintf("observer-%03d", (id+j)%225)
			snr := float64(j%12) - 4
			obs := &StoreObs{ID: id*fanout + j, TransmissionID: id,
				ObserverID: observer, ObserverName: "Synthetic " + observer,
				PathJSON: path, Timestamp: stamp, Direction: "rx", SNR: &snr}
			tx.addObservation(obs)
			s.byObsID[obs.ID] = obs
			s.byObserver[observer] = append(s.byObserver[observer], obs)
			s.totalObs++
			s.trackedBytes += estimateStoreObsBytes(obs)
			s.indexResolvedPathHops(tx, pks, hopsSeen, nil)
		}
		pickBestObservation(tx)
		s.trackedBytes += rechargeTx(tx)
		addTxToPathHopIndex(s.byPathHop, tx)
	}
}

func newEvictionChurnStore(now time.Time) *PacketStore {
	s := makeTestStore(0, now, 0)
	s.retentionHours = 1
	s.byPathHop = make(map[string][]*StoreTx)
	s.useResolvedPathIndex = true
	s.initResolvedPathIndex()
	return s
}

func evictedNodePacketCount(s *PacketStore) int {
	seen := make(map[int]bool)
	for _, list := range s.byNode {
		for _, tx := range list {
			if s.byTxID[tx.ID] != tx {
				seen[tx.ID] = true
			}
		}
	}
	return len(seen)
}

func TestEvictionChurnOwnsOnlyLivePackets(t *testing.T) {
	now := time.Now().UTC()
	s := newEvictionChurnStore(now)
	const batch = 8
	for cycle := 0; cycle < 5; cycle++ {
		for _, tx := range s.packets {
			tx.FirstSeen = now.Add(-2 * time.Hour).Format(time.RFC3339)
		}
		appendEvictionChurnBatch(s, cycle*batch+1, batch, 4, now)
		s.RunEviction()
		if len(s.packets) != batch || s.totalObs != batch*4 {
			t.Fatalf("cycle %d retained %d packets/%d observations", cycle, len(s.packets), s.totalObs)
		}
		if got := evictedNodePacketCount(s); got != 0 {
			t.Fatalf("cycle %d retains %d evicted packets through the node index", cycle, got)
		}
		var charged int64
		for _, tx := range s.packets {
			charged += tx.accountedBytes
			for _, obs := range tx.Observations {
				charged += estimateStoreObsBytes(obs)
			}
		}
		if s.trackedBytes != charged {
			t.Fatalf("cycle %d charged %d bytes, want live-packet sum %d", cycle, s.trackedBytes, charged)
		}
	}
}

// One operation is eight complete 1,000-transmission generations, each with
// sixteen observations and distinct resolved eight-hop paths. It leaves the
// final generation live. GC samples run outside the timer, and only the store
// retains packet pointers. This measures in-memory churn, not SQL or HTTP.
func BenchmarkEvictionOwnershipChurn(b *testing.B) {
	const batch, fanout, cycles = 1000, 16, 8
	b.ReportAllocs()
	var retained, tracked, orphans float64
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		now := time.Now().UTC()
		s := newEvictionChurnStore(now)
		b.StartTimer()
		for cycle := 0; cycle < cycles; cycle++ {
			for _, tx := range s.packets {
				tx.FirstSeen = now.Add(-2 * time.Hour).Format(time.RFC3339)
			}
			appendEvictionChurnBatch(s, cycle*batch+1, batch, fanout, now)
			s.RunEviction()
		}
		b.StopTimer()
		runtime.GC()
		runtime.ReadMemStats(&after)
		retained += float64(int64(after.HeapAlloc) - int64(before.HeapAlloc))
		tracked += float64(s.trackedBytes)
		orphans += float64(evictedNodePacketCount(s))
		runtime.KeepAlive(s)
		b.StartTimer()
	}
	b.ReportMetric(retained/float64(b.N), "retained-B/op")
	b.ReportMetric(tracked/float64(b.N), "tracked-B/op")
	b.ReportMetric(orphans/float64(b.N), "orphan-tx/op")
	b.ReportMetric(batch, "live-tx/op")
}
