package main

import (
	"fmt"
	"sync"
	"testing"
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
				}
			}
		}()
	}
	ready.Wait()
	defer func() {
		close(stop)
		readers.Wait()
	}()
	for revision := 0; revision < 24; revision++ {
		decoded := fmt.Sprintf(`{"type":"CHAN","channel":"#revision-%d","flags":{"revision":"%d"}}`, revision, revision)
		backfillRewrite(t, writer, decoded, "#updated")
		if err := s.pollTxRewrites(500); err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("#revision-%d", revision)
		if got := tx.ParsedDecoded()["channel"]; got != want {
			t.Fatalf("rewrite cache stale: got %v, want %s", got, want)
		}
		if retained["channel"] != "#initial" || retained["flags"].(map[string]interface{})["revision"] != "initial" {
			t.Fatal("rewrite mutated a map retained by an earlier reader")
		}
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
