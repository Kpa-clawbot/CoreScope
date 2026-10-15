package main

import (
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"
)

// Every observation carries its observer's ID, name, IATA code and the
// direction. A store holds hundreds of thousands of observations from a few
// hundred observers, so each loader shares one copy of each of those strings
// instead of keeping the copy every row scan makes.

func assertObserverStringsShared(t *testing.T, store *PacketStore, wantObs int) {
	t.Helper()
	store.mu.RLock()
	defer store.mu.RUnlock()
	var all []*StoreObs
	for _, tx := range store.packets {
		all = append(all, tx.Observations...)
	}
	if len(all) < wantObs {
		t.Fatalf("loaded %d observations, want at least %d", len(all), wantObs)
	}
	first := all[0]
	for _, f := range []struct {
		name string
		get  func(*StoreObs) string
	}{
		{"ObserverID", func(o *StoreObs) string { return o.ObserverID }},
		{"ObserverName", func(o *StoreObs) string { return o.ObserverName }},
		{"ObserverIATA", func(o *StoreObs) string { return o.ObserverIATA }},
		{"Direction", func(o *StoreObs) string { return o.Direction }},
	} {
		want := f.get(first)
		if want == "" {
			t.Fatalf("fixture: %s is empty", f.name)
		}
		copies := map[*byte]bool{}
		for _, o := range all {
			if f.get(o) == want {
				copies[unsafe.StringData(f.get(o))] = true
			}
		}
		if len(copies) != 1 {
			t.Errorf("%s %q is held in %d copies across %d observations, want 1", f.name, want, len(copies), len(all))
		}
	}
}

func openInternTestStore(t *testing.T, numTx int) *PacketStore {
	t.Helper()
	dbPath := postgresTestDSN(t)
	createTestDBAt(t, dbPath, numTx)
	// The shared fixture leaves observers empty; give each an IATA code so
	// the loaders read one for every observation.
	conn, err := openFixtureSQL(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(`INSERT INTO observers (id, name, iata) SELECT DISTINCT observer_id, observer_name, 'EDI' FROM observations`); err != nil {
		t.Fatalf("fixture observers: %v", err)
	}
	conn.Close()
	db, err := openFixtureReader(t, dbPath)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewPacketStore(db, &PacketStoreConfig{})
}

func TestObserverStringsShared_Load(t *testing.T) {
	store := openInternTestStore(t, 50)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	assertObserverStringsShared(t, store, 50)
}

func TestObserverStringsShared_LoadChunked(t *testing.T) {
	store := openInternTestStore(t, 50)
	if err := store.LoadChunked(10); err != nil {
		t.Fatal(err)
	}
	assertObserverStringsShared(t, store, 50)
}

func TestObserverStringsShared_LoadChunk(t *testing.T) {
	store := openInternTestStore(t, 50)
	from := time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)
	if err := store.loadChunk(from, from.Add(72*time.Hour)); err != nil {
		t.Fatal(err)
	}
	assertObserverStringsShared(t, store, 50)
}

// Many goroutines interning the same values all get the one shared copy.
func TestInternStrConcurrent(t *testing.T) {
	keys := make([]string, 50)
	for i := range keys {
		keys[i] = fmt.Sprintf("intern-concurrent-%062d", i)
	}
	var wg sync.WaitGroup
	got := make([][]string, 16)
	for g := range got {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for _, k := range keys {
				got[g] = append(got[g], internStr(strings.Clone(k)))
			}
		}(g)
	}
	wg.Wait()
	for i := range keys {
		want := unsafe.StringData(got[0][i])
		for g := range got {
			if got[g][i] != keys[i] || unsafe.StringData(got[g][i]) != want {
				t.Fatalf("goroutine %d got a different copy of %q", g, keys[i])
			}
		}
	}
}

// BenchmarkInternStr measures a hit (the steady state: a few hundred
// observers) alone and from every core. Loaders and ingest intern in
// parallel, so hits must not contend.
//
//	go test -run '^$' -bench BenchmarkInternStr -cpu 1,12 ./cmd/server
func BenchmarkInternStr(b *testing.B) {
	keys := make([]string, 300)
	for i := range keys {
		keys[i] = internStr(fmt.Sprintf("bench-observer-%049d", i))
	}
	b.Run("serial", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = internStr(keys[i%len(keys)])
		}
	})
	b.Run("parallel", func(b *testing.B) {
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				_ = internStr(keys[i%len(keys)])
				i++
			}
		})
	})
}

// A hit, the steady state, allocates nothing.
func TestInternStrHitDoesNotAllocate(t *testing.T) {
	k := internStr("hit-probe-0123456789abcdef0123456789abcdef0123456789abcdef01234567")
	if n := testing.AllocsPerRun(1000, func() { _ = internStr(k) }); n != 0 {
		t.Fatalf("internStr allocates %.0f times on a hit", n)
	}
}

// A canonical copy outlives a garbage collection: interning the same value
// after a GC returns the copy observations already hold, not a new one, so a
// long load or a long-running server keeps one copy per value in total.
func TestInternStrSurvivesGC(t *testing.T) {
	a := internStr(strings.Repeat("x", 3) + "gc-obs")
	runtime.GC()
	runtime.GC()
	b := internStr(strings.Repeat("x", 3) + "gc-obs")
	if unsafe.StringData(a) != unsafe.StringData(b) {
		t.Fatal("interning after a GC made a second copy of a value still in use")
	}
	runtime.KeepAlive(a)
}
