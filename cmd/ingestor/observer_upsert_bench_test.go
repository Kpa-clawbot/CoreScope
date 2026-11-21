package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"testing"
	"time"
)

// BenchmarkUpsertObserverAtExisting measures the per-packet observer upsert
// for observers that already have a row, on a store opened by OpenStore (WAL,
// synchronous=FULL, the real observers table), spread over 500 observers.
//
// "default" keeps the store's pragmas, so ns/op is the wall time per call.
// "no-autocheckpoint" turns the automatic checkpoint off so the WAL keeps
// every frame, and reports wal-frames/op: what each call wrote.
//
//	go test -run '^$' -bench BenchmarkUpsertObserverAtExisting -benchtime 20000x .
func BenchmarkUpsertObserverAtExisting(b *testing.B) {
	// OpenStore logs, and the lines would split the benchmark's result line.
	log.SetOutput(io.Discard)
	defer log.SetOutput(os.Stderr)
	for _, autoCheckpoint := range []bool{true, false} {
		name := "default"
		if !autoCheckpoint {
			name = "no-autocheckpoint"
		}
		b.Run(name, func(b *testing.B) {
			s, err := OpenStore(b.TempDir() + "/bench.db")
			if err != nil {
				b.Fatal(err)
			}
			s.WaitForAsyncMigrations()
			defer s.Close()

			const observers = 500
			ids := make([]string, observers)
			for i := range ids {
				ids[i] = fmt.Sprintf("obs-%03d", i)
				if err := s.UpsertObserverAt(ids[i], ids[i], "sjc", nil, "2026-01-01T00:00:00Z"); err != nil {
					b.Fatal(err)
				}
			}
			if !autoCheckpoint {
				if _, err := s.db.Exec(`PRAGMA wal_autocheckpoint = 0`); err != nil {
					b.Fatal(err)
				}
			}
			if _, err := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
				b.Fatal(err)
			}
			meta := &ObserverMeta{BatteryMv: new(int)}
			base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				*meta.BatteryMv = 3700 + i%100
				ts := base.Add(time.Duration(i) * time.Second).Format(time.RFC3339)
				if err := s.UpsertObserverAt(ids[i%observers], ids[i%observers], "sjc", meta, ts); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()

			if !autoCheckpoint {
				var busy, frames, done int
				if err := s.db.QueryRow(`PRAGMA wal_checkpoint(PASSIVE)`).Scan(&busy, &frames, &done); err != nil {
					b.Fatal(err)
				}
				b.ReportMetric(float64(frames)/float64(b.N), "wal-frames/op")
			}
		})
	}
}
