package main

import (
	"sync"
	"testing"
	"time"
	"unsafe"
)

// StoreObs is the store's most numerous struct (400k on a week of a busy
// network). Its lazy timestamp cache used to be a time.Time, a sync.Once and
// a bool, 40 bytes that put each observation in the 192-byte size class. It
// is 8 bytes now, and each observation fits in 160.
func TestStoreObsFitsIn160Bytes(t *testing.T) {
	if n := unsafe.Sizeof(StoreObs{}); n > 160 {
		t.Fatalf("StoreObs is %d bytes, want at most 160 (one size class down from 192)", n)
	}
}

func parseObsTimestampReference(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// ParsedTime gives the same time, location included, as parsing the string.
func TestStoreObsParsedTimeMatchesParse(t *testing.T) {
	for _, ts := range []string{
		"2026-10-10T12:34:56.789Z",
		"2026-10-10T12:34:56Z",
		"2026-10-10T13:34:56+01:00",
		"2026-10-10 12:34:56",
		"",
		"not a time",
		"1970-01-01T00:00:00Z",
	} {
		o := &StoreObs{Timestamp: ts}
		want, wantOK := parseObsTimestampReference(ts)
		for i := 0; i < 2; i++ { // first call parses, second reads the cache
			got, ok := o.ParsedTime()
			if ok != wantOK || !got.Equal(want) || got.Location().String() != want.Location().String() {
				t.Fatalf("%q call %d: got %v (%v) ok=%v, want %v (%v) ok=%v", ts, i, got, got.Location(), ok, want, want.Location(), wantOK)
			}
			if wantOK && got.Hour() != want.Hour() {
				t.Fatalf("%q: hour %d, want %d", ts, got.Hour(), want.Hour())
			}
		}
	}
}

// Concurrent readers under the store's read lock may race to fill the cache.
func TestStoreObsParsedTimeConcurrentFill(t *testing.T) {
	o := &StoreObs{Timestamp: "2026-10-10T12:34:56.789Z"}
	want, _ := parseObsTimestampReference(o.Timestamp)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if got, ok := o.ParsedTime(); !ok || !got.Equal(want) {
					t.Errorf("got %v ok=%v", got, ok)
					return
				}
			}
		}()
	}
	wg.Wait()
}
