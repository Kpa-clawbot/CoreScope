package main

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"
)

func TestCountDistinctNonAdvert_2108(t *testing.T) {
	pt, advert := 2, payloadTypeAdvert
	tx := func(id int) *StoreTx { return &StoreTx{ID: id, PayloadType: &pt} }
	t1, t2, t3 := tx(1), tx(2), tx(3)
	ad := &StoreTx{ID: 4, PayloadType: &advert}
	untyped := &StoreTx{ID: 5}
	cases := []struct {
		name string
		list []*StoreTx
		want int
	}{
		{"empty", nil, 0},
		{"ascending", []*StoreTx{t1, t2, t3}, 3},
		{"descending (chunk load order)", []*StoreTx{t3, t2, t1}, 3},
		{"adjacent duplicate", []*StoreTx{t1, t1}, 1},
		{"ascending then duplicate", []*StoreTx{t1, t2, t3, t3}, 3},
		{"interleaved duplicates", []*StoreTx{t1, t2, t1, t3, t2}, 3},
		{"adverts and nils skipped", []*StoreTx{nil, ad, t2, ad, nil, t2}, 1},
		{"untyped counted", []*StoreTx{untyped, t1, untyped}, 2},
	}
	var ids []int
	for _, c := range cases {
		var got int
		got, ids = countDistinctNonAdvert(c.list, ids)
		if got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

// --- The per-transmission record behind the dedupe (pathHopResolved) ---
//
// It holds the resolved relay keys a live transmission is indexed under.
// These tests pin its lifecycle: bounded by relays (not observations),
// removed on eviction, kept for live transmissions only across an index
// rebuild, and emptied when a rebuild starts from an empty index.

var (
	phdRecordKey1 = strings.Repeat("a", 64)
	phdRecordKey2 = strings.Repeat("b", 64)
)

// Raw hops of every transmission built by phdRecordStore (makeTestStore's
// PathJSON), plus the two resolved keys above.
const phdRecordRefsPerTx = 3 + 2

// phdRecordStore builds count non-advert transmissions, the older half past
// the 1 h retention, each indexed under its raw hops and two resolved keys
// through the real byPathHop helpers.
func phdRecordStore(count int) (store *PacketStore, old, young []*StoreTx) {
	now := time.Now().UTC()
	store = makeTestStore(count, now.Add(-30*time.Minute), 0)
	store.byPayloadType[5] = store.byPayloadType[4]
	delete(store.byPayloadType, 4)
	store.byPathHop = make(map[string][]*StoreTx)
	store.useResolvedPathIndex = true
	store.initResolvedPathIndex()
	store.retentionHours = 1
	hopsSeen := make(map[string]bool)
	for i, tx := range store.packets {
		*tx.PayloadType = 5
		if i < count/2 {
			tx.FirstSeen = now.Add(-2 * time.Hour).Format(time.RFC3339)
			old = append(old, tx)
		} else {
			young = append(young, tx)
		}
		addTxToPathHopIndex(store.byPathHop, tx)
		store.indexResolvedPathHops(tx, phdFreshKeys(), hopsSeen)
	}
	return store, old, young
}

// phdFreshKeys returns the two resolved keys as newly allocated strings, as
// every ingest path produces them (json.Unmarshal / strings.ToLower).
func phdFreshKeys() []string {
	return []string{strings.Clone(phdRecordKey1), strings.Clone(phdRecordKey2)}
}

func phdEntriesOf(store *PacketStore, tx *StoreTx) int {
	n := 0
	for _, list := range store.byPathHop {
		for _, t := range list {
			if t == tx {
				n++
			}
		}
	}
	return n
}

// The record does not grow with observations, and eviction removes it
// together with the transmission's byPathHop entries.
func TestPathHopResolvedRecordBoundedAndEvicted_2108(t *testing.T) {
	store, old, young := phdRecordStore(40)
	hopsSeen := map[string]bool{}
	for _, tx := range store.packets {
		for k := 0; k < 10; k++ {
			store.indexResolvedPathHops(tx, phdFreshKeys(), hopsSeen)
		}
	}
	if got := len(store.pathHopResolved); got != len(old)+len(young) {
		t.Fatalf("record holds %d transmissions, want %d", got, len(old)+len(young))
	}
	for _, tx := range young {
		if got := len(store.pathHopResolved[tx]); got != 2 {
			t.Fatalf("tx %d record holds %d keys after 11 observations, want 2", tx.ID, got)
		}
		if n := phdEntriesOf(store, tx); n != phdRecordRefsPerTx {
			t.Fatalf("tx %d has %d byPathHop entries after 11 observations, want %d", tx.ID, n, phdRecordRefsPerTx)
		}
	}

	store.mu.Lock()
	evicted := store.EvictStale()
	store.mu.Unlock()
	if evicted != len(old) {
		t.Fatalf("evicted %d, want %d", evicted, len(old))
	}
	for _, tx := range old {
		if _, ok := store.pathHopResolved[tx]; ok {
			t.Fatalf("evicted tx %d is still in the record", tx.ID)
		}
		if n := phdEntriesOf(store, tx); n != 0 {
			t.Fatalf("evicted tx %d is still in %d byPathHop buckets", tx.ID, n)
		}
	}
	if got := len(store.pathHopResolved); got != len(young) {
		t.Fatalf("record holds %d transmissions after eviction, want %d", got, len(young))
	}
	// A survivor's next observation still adds nothing.
	for _, tx := range young {
		store.indexResolvedPathHops(tx, phdFreshKeys(), hopsSeen)
		if n := phdEntriesOf(store, tx); n != phdRecordRefsPerTx {
			t.Fatalf("tx %d has %d entries after eviction and another observation, want %d", tx.ID, n, phdRecordRefsPerTx)
		}
	}

	// Evicting the rest leaves no record and no interned key behind.
	for _, tx := range young {
		tx.FirstSeen = time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339)
	}
	store.mu.Lock()
	store.EvictStale()
	store.mu.Unlock()
	if len(store.pathHopResolved) != 0 || len(store.pathHopKeys) != 0 || len(store.byPathHop) != 0 {
		t.Fatalf("after evicting every transmission: record %d, interned keys %d, byPathHop keys %d; want 0/0/0",
			len(store.pathHopResolved), len(store.pathHopKeys), len(store.byPathHop))
	}
}

// A rebuild keeps the record of live transmissions (their resolved entries
// are carried over), so observations after it still add nothing; it drops
// the record of transmissions that are no longer in the store.
func TestPathHopResolvedRecordAcrossRebuild_2108(t *testing.T) {
	store, _, young := phdRecordStore(40)
	gone := store.packets[0]
	goneOnly := strings.Repeat("e", 64) // a relay only the removed transmission went through
	store.indexResolvedPathHops(gone, []string{goneOnly}, map[string]bool{})
	store.packets = store.packets[1:]
	delete(store.byTxID, gone.ID)
	delete(store.byHash, gone.Hash)

	store.mu.Lock()
	store.buildPathHopIndex()
	store.mu.Unlock()

	if _, ok := store.pathHopResolved[gone]; ok {
		t.Fatal("rebuild kept the record of a transmission no longer in the store")
	}
	if _, ok := store.byPathHop[goneOnly]; ok {
		t.Fatal("fixture: rebuild carried over a bucket of a removed transmission")
	}
	if _, ok := store.pathHopKeys[goneOnly]; ok || len(store.pathHopKeys) != 2 {
		t.Fatalf("rebuild kept interned keys without a bucket: %d interned, want 2", len(store.pathHopKeys))
	}
	hopsSeen := map[string]bool{}
	for _, tx := range young {
		if _, ok := store.pathHopResolved[tx]; !ok {
			t.Fatalf("rebuild dropped the record of live tx %d", tx.ID)
		}
		store.indexResolvedPathHops(tx, phdFreshKeys(), hopsSeen)
		if n := phdEntriesOf(store, tx); n != phdRecordRefsPerTx {
			t.Fatalf("tx %d has %d entries after a rebuild and another observation, want %d", tx.ID, n, phdRecordRefsPerTx)
		}
	}
}

// When the index being rebuilt holds no resolved entries at all, nothing is
// indexed under any resolved key any more, so the record must not claim
// otherwise: the next observation has to put the transmission back.
func TestPathHopResolvedRecordClearedWithEmptyIndex_2108(t *testing.T) {
	store, _, young := phdRecordStore(40)
	store.byPathHop = make(map[string][]*StoreTx) // nothing carried over

	store.mu.Lock()
	store.buildPathHopIndex()
	store.mu.Unlock()

	if got := len(store.pathHopResolved); got != 0 {
		t.Fatalf("record still holds %d transmissions after a rebuild from an empty index", got)
	}
	if got := len(store.pathHopKeys); got != 0 {
		t.Fatalf("%d interned keys survive a rebuild from an empty index", got)
	}
	hopsSeen := map[string]bool{}
	for _, tx := range young {
		store.indexResolvedPathHops(tx, phdFreshKeys(), hopsSeen)
		if n := phdEntriesOf(store, tx); n != phdRecordRefsPerTx {
			t.Fatalf("tx %d has %d entries after the rebuild and another observation, want %d", tx.ID, n, phdRecordRefsPerTx)
		}
	}
}

// The record compares keys exactly, and keeps one shared copy of each key
// rather than the per-observation string it was handed: every record entry
// and the byPathHop key point at the same bytes.
func TestPathHopResolvedRecordInternsKeys_2108(t *testing.T) {
	store, _, young := phdRecordStore(10)
	canon, ok := store.pathHopKeys[phdRecordKey1]
	if !ok || len(store.pathHopKeys) != 2 {
		t.Fatalf("interned keys = %d (key1 present %v), want 2", len(store.pathHopKeys), ok)
	}
	for _, tx := range young {
		rec := store.pathHopResolved[tx]
		if len(rec) != 2 || unsafe.StringData(rec[0]) != unsafe.StringData(canon) {
			t.Fatalf("tx %d record %v does not share the interned key", tx.ID, rec)
		}
	}
	for k := range store.byPathHop {
		if k == phdRecordKey1 && unsafe.StringData(k) != unsafe.StringData(canon) {
			t.Fatal("byPathHop key is not the interned copy")
		}
	}
}

// BenchmarkPathHopRecordMemory_2108 reports what the record costs: heap
// retained by pathHopResolved and pathHopKeys (including any key string
// only they keep alive) per transmission, for n transmissions with k
// distinct resolved relay keys each from 4000 relays, every transmission
// heard twice with freshly allocated keys as the ingest paths produce them.
// The fixture's union of resolved keys per transmission over all of its
// observations has a mean of 5 and a maximum of 17.
func BenchmarkPathHopRecordMemory_2108(b *testing.B) {
	heap := func() uint64 {
		var m runtime.MemStats
		runtime.GC()
		runtime.GC()
		runtime.ReadMemStats(&m)
		return m.HeapAlloc
	}
	const relays = 4000
	for _, n := range []int{100000} {
		for _, k := range []int{2, 5, 17} {
			b.Run(fmt.Sprintf("txs=%d/keys=%d", n, k), func(b *testing.B) {
				for it := 0; it < b.N; it++ {
					b.StopTimer()
					store := makeTestStore(0, time.Now().UTC(), 0)
					store.byPathHop = make(map[string][]*StoreTx)
					hopsSeen := map[string]bool{}
					pks := make([]string, k)
					for i := 0; i < n; i++ {
						tx := &StoreTx{ID: i + 1, parsedPath: []string{"zz"}, pathParsed: true}
						store.packets = append(store.packets, tx)
						for obs := 0; obs < 2; obs++ {
							for j := range pks {
								r := (i*31 + j*997) % relays
								pks[j] = fmt.Sprintf("%02x%062x", r%256, r)
							}
							store.indexResolvedPathHops(tx, pks, hopsSeen)
						}
					}
					with := heap()
					store.pathHopResolved, store.pathHopKeys = nil, nil
					without := heap()
					runtime.KeepAlive(store)
					b.ReportMetric((float64(with)-float64(without))/float64(n), "record-B/tx")
					b.StartTimer()
				}
			})
		}
	}
}
