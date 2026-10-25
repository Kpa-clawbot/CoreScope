package main

import (
	"encoding/json"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestTxRewriteFeedConcurrentDecodedReaders(t *testing.T) {
	db, writer := txRewriteFixture(t, true)
	s := NewPacketStore(db, &PacketStoreConfig{})
	if err := s.Load(); err != nil {
		t.Fatal(err)
	}
	tx := s.byTxID[1]
	original := `{"type":"CHAN","channel":"#initial","flags":{"revision":"initial"}}`
	backfillRewrite(t, writer, original, "#initial")
	if err := s.pollTxRewrites(500); err != nil {
		t.Fatal(err)
	}
	retained := tx.ParsedDecoded()
	stop := make(chan struct{})
	var ready, readers sync.WaitGroup
	ready.Add(4)
	readers.Add(4)
	for range 4 {
		go func() {
			defer readers.Done()
			tx.ParsedDecoded()
			ready.Done()
			for {
				select {
				case <-stop:
					return
				default:
					d := tx.ParsedDecoded()
					if d == nil || d["type"] != "CHAN" {
						t.Error("rewrite exposed an empty or partially parsed cache")
						return
					}
					if flags, ok := d["flags"].(map[string]interface{}); !ok || flags["revision"] == nil {
						t.Error("rewrite exposed a partially parsed nested map")
						return
					}
					runtime.Gosched()
				}
			}
		}()
	}
	ready.Wait()
	defer func() {
		close(stop)
		done := make(chan struct{})
		go func() { readers.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("decoded readers blocked after rewrite")
		}
	}()
	rewritten := make(chan struct{})
	go func() {
		defer close(rewritten)
		for revision := 0; revision < 24; revision++ {
			decoded := fmt.Sprintf(`{"type":"CHAN","channel":"#revision-%d","flags":{"revision":"%d"}}`, revision, revision)
			backfillRewrite(t, writer, decoded, "#updated")
			if err := s.pollTxRewrites(500); err != nil {
				t.Error(err)
				return
			}
			want := fmt.Sprintf("#revision-%d", revision)
			if got := tx.ParsedDecoded()["channel"]; got != want {
				t.Errorf("rewrite cache stale: got %v, want %s", got, want)
				return
			}
			if retained["channel"] != "#initial" || retained["flags"].(map[string]interface{})["revision"] != "initial" {
				t.Error("rewrite mutated a map retained by an earlier reader")
				return
			}
		}
	}()
	select {
	case <-rewritten:
	case <-time.After(10 * time.Second):
		t.Fatal("rewrite/cache reader did not finish")
	}
}

// Model a reader already parsing an old generation. A writer holding store.mu
// must not wait for it, and its eventual completion must not replace new data.
func TestDecodedRewriteDoesNotWaitForOldParse(t *testing.T) {
	const oldJSON = `{"type":"old"}`
	tx := &StoreTx{DecodedJSON: oldJSON}
	old := &txDecodedCache{source: oldJSON}
	tx.decodedCache.Store(old)
	entered, release, parsed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(parsed)
		old.once.Do(func() {
			close(entered)
			<-release
			json.Unmarshal([]byte(old.source), &old.parsed)
		})
	}()
	<-entered
	var releaseOnce sync.Once
	finishParse := func() { releaseOnce.Do(func() { close(release) }); <-parsed }
	defer finishParse()
	rewritten := make(chan struct{})
	go func() {
		tx.setDecodedJSON(`{"type":"new"}`)
		close(rewritten)
	}()
	select {
	case <-rewritten:
	case <-time.After(time.Second):
		t.Fatal("rewrite waited for an old generation's JSON parse")
	}
	current := tx.ParsedDecoded()
	if current["type"] != "new" {
		t.Fatal("new generation did not parse the rewritten source")
	}
	cache := tx.decodedCache.Load()
	if tx.setDecodedJSON(`{"type":"new"}`) || tx.decodedCache.Load() != cache {
		t.Fatal("unchanged JSON discarded its warm cache")
	}
	finishParse()
	if old.parsed["type"] != "old" || tx.ParsedDecoded()["type"] != "new" {
		t.Fatal("old parser completion replaced or mutated the new generation")
	}
}

func TestDecodedGenerationIncludedInMemoryBudget(t *testing.T) {
	tx := &StoreTx{}
	before := estimateStoreTxBytes(tx)
	if before < int64(storeTxBaseBytes+numIndexesPerTx*indexEntryBytes+decodedCacheGenerationBytes) {
		t.Fatal("cache generation is not charged before parsing")
	}
	allocs := testing.AllocsPerRun(100, func() {
		if tx.ParsedDecoded() != nil {
			t.Fatal("empty source must stay empty")
		}
	})
	if allocs != 0 {
		t.Fatalf("warm decoded cache allocates: %v", allocs)
	}
}

func BenchmarkParsedDecodedWarm(b *testing.B) {
	tx := &StoreTx{DecodedJSON: decodedCacheFixture}
	tx.ParsedDecoded()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if tx.ParsedDecoded()["type"] != "ADVERT" {
			b.Fatal("cached decoded payload changed")
		}
	}
}

func BenchmarkParsedDecodedCold(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		tx := &StoreTx{DecodedJSON: decodedCacheFixture}
		if tx.ParsedDecoded()["type"] != "ADVERT" {
			b.Fatal("decoded payload changed")
		}
	}
}
