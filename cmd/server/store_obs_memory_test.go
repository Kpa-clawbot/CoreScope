package main

import (
	"runtime"
	"testing"
	"unsafe"
)

// Observations outnumber transmissions at high-fanout receivers. Keep the
// timestamp cache packed so each retained observation fits the 192-byte
// allocation class on the supported 64-bit deployment targets.
func TestStoreObsMemoryFootprint(t *testing.T) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		t.Skip("allocation budget applies to 64-bit targets")
	}
	if size := unsafe.Sizeof(StoreObs{}); size > 192 {
		t.Fatalf("StoreObs is %d bytes; want <=192 to avoid the 208-byte allocation class", size)
	}
}

// Measure live objects after GC; a heap profile taken after this benchmark
// returns would otherwise lose the observation set being compared.
func BenchmarkStoreObsRetainedMemory(b *testing.B) {
	const count = 100000
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		observations := make([]*StoreObs, count)
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		b.StartTimer()
		for j := range observations {
			observations[j] = &StoreObs{ID: j + 1, TransmissionID: j/16 + 1, Timestamp: "2026-01-01T00:00:00Z"}
			observations[j].ParsedTime()
		}
		b.StopTimer()
		runtime.GC()
		runtime.ReadMemStats(&after)
		b.ReportMetric(float64(int64(after.HeapAlloc)-int64(before.HeapAlloc))/count, "retained-B/obs")
		runtime.KeepAlive(observations)
		b.StartTimer()
	}
}
