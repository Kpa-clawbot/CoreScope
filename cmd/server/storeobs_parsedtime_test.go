package main

import (
	"sync"
	"testing"
	"time"
)

// TestStoreObsParsedTimeCaches asserts ParsedTime parses once and
// returns the same value on subsequent calls — required so #1481 P0-2
// (handleObserverAnalytics RLock-held parse loop) doesn't reparse 60k
// times per request.
func TestStoreObsParsedTimeCaches(t *testing.T) {
	cases := []struct {
		name string
		ts   string
		want time.Time
		ok   bool
	}{
		{"rfc3339nano", "2026-05-29T07:00:00.123456789Z",
			time.Date(2026, 5, 29, 7, 0, 0, 123456789, time.UTC), true},
		{"rfc3339", "2026-05-29T07:00:00Z",
			time.Date(2026, 5, 29, 7, 0, 0, 0, time.UTC), true},
		{"sql-shape", "2026-05-29 07:00:00",
			time.Date(2026, 5, 29, 7, 0, 0, 0, time.UTC), true},
		{"empty", "", time.Time{}, false},
		{"garbage", "not-a-time", time.Time{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o := &StoreObs{Timestamp: c.ts}
			got, ok := o.ParsedTime()
			if ok != c.ok {
				t.Fatalf("ok=%v want %v", ok, c.ok)
			}
			if ok && !got.Equal(c.want) {
				t.Fatalf("got %v want %v", got, c.want)
			}
			// Second call must return identical result (cached).
			got2, ok2 := o.ParsedTime()
			if got2 != got || ok2 != ok {
				t.Fatalf("cache inconsistent: %v/%v vs %v/%v", got, ok, got2, ok2)
			}
		})
	}
}

// TestStoreObsParsedTimeConcurrent asserts no race under concurrent
// readers — sync.Once guards the parse.
func TestStoreObsParsedTimeConcurrent(t *testing.T) {
	o := &StoreObs{Timestamp: "2026-05-29T07:00:00Z"}
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok := o.ParsedTime(); !ok {
				t.Error("expected ok")
			}
		}()
	}
	wg.Wait()
}
