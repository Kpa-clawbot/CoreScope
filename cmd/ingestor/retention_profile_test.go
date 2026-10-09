package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/mattn/go-sqlite3"
)

func seedRetentionFanout(tb testing.TB, s *Store, transmissions, observations int) {
	tb.Helper()
	s.WaitForAsyncMigrations()
	tx, err := s.db.Begin()
	if err != nil {
		tb.Fatal(err)
	}
	defer tx.Rollback()
	parents, err := tx.Prepare(`INSERT INTO transmissions(id,raw_hex,hash,first_seen,last_seen) VALUES(?,'00',?,'2020-01-01T00:00:00Z',1)`)
	if err != nil {
		tb.Fatal(err)
	}
	defer parents.Close()
	children, err := tx.Prepare(`INSERT INTO observations(transmission_id,observer_idx,path_json,timestamp) VALUES(?,?,'[]',1)`)
	if err != nil {
		tb.Fatal(err)
	}
	defer children.Close()
	for i := 1; i <= transmissions; i++ {
		if _, err := parents.Exec(i, fmt.Sprint("retention-", i)); err != nil {
			tb.Fatal(err)
		}
	}
	for i := 0; i < observations; i++ {
		if _, err := children.Exec(i%transmissions+1, i/transmissions); err != nil {
			tb.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		tb.Fatal(err)
	}
}

// A small open-loop schedule exposes queueing behind the real serial ingest
// caller. It is diagnostic, not a machine-speed pass/fail threshold.
func BenchmarkRetentionConcurrentIngest(b *testing.B) {
	for _, fixture := range []struct {
		name                        string
		transmissions, observations int
	}{{"typical16", 16000, 256000}, {"dense256", 500, 128000}, {"singleHot", 1, 64000}} {
		b.Run(fixture.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				s, err := OpenStore(filepath.Join(b.TempDir(), "retention-live.db"))
				if err != nil {
					b.Fatal(err)
				}
				seedRetentionFanout(b, s, fixture.transmissions, fixture.observations)
				ResetWriterStatsForTest()
				before := s.db.Stats()
				const arrivals = 250
				waits := make([]float64, 0, arrivals)
				completions := make([]float64, 0, arrivals)
				done := make(chan error, 1)
				var pruneElapsed time.Duration
				b.StartTimer()
				start := time.Now()
				for n := 0; n < arrivals; n++ {
					scheduled := start.Add(time.Duration(n) * 20 * time.Millisecond)
					if delay := time.Until(scheduled); delay > 0 {
						timer := time.NewTimer(delay)
						<-timer.C
					}
					waits = append(waits, float64(time.Since(scheduled))/float64(time.Millisecond))
					if _, err := s.InsertTransmission(&PacketData{Hash: fmt.Sprintf("live-%d", n), RawHex: "00", Timestamp: time.Now().UTC().Format(time.RFC3339), PathJSON: "[]", DecodedJSON: "{}"}); err != nil {
						b.Fatal(err)
					}
					completions = append(completions, float64(time.Since(scheduled))/float64(time.Millisecond))
					if n == 9 {
						go func() {
							began := time.Now()
							deleted, err := s.PruneOldPackets(5)
							pruneElapsed = time.Since(began)
							if err == nil && deleted != int64(fixture.transmissions) {
								err = fmt.Errorf("removed %d, want %d", deleted, fixture.transmissions)
							}
							done <- err
						}()
					}
				}
				if err := <-done; err != nil {
					b.Fatal(err)
				}
				b.StopTimer()
				after := s.db.Stats()
				var parents, observations int
				if err := s.db.QueryRow(`SELECT count(*) FROM transmissions`).Scan(&parents); err != nil {
					b.Fatal(err)
				}
				if err := s.db.QueryRow(`SELECT count(*) FROM observations`).Scan(&observations); err != nil {
					b.Fatal(err)
				}
				if parents != arrivals || observations != arrivals || s.Stats.WriteErrors.Load() != 0 {
					b.Fatalf("live row loss/errors: tx=%d obs=%d errors=%d", parents, observations, s.Stats.WriteErrors.Load())
				}
				sort.Float64s(waits)
				sort.Float64s(completions)
				result := struct {
					RetentionMS, QueueP50MS, QueueP95MS, QueueMaxMS, CompletionP95MS float64
					PoolWaitCount                                                    int64
					PoolWaitMS                                                       float64
					Writers                                                          map[string]WriterStatsSnapshot
				}{float64(pruneElapsed) / float64(time.Millisecond), waits[len(waits)/2], waits[(len(waits)*95-1)/100], waits[len(waits)-1], completions[(len(completions)*95-1)/100], after.WaitCount - before.WaitCount, float64(after.WaitDuration-before.WaitDuration) / float64(time.Millisecond), s.WriterStatsSnapshot()}
				data, _ := json.Marshal(result)
				b.Log(string(data))
				s.Close()
			}
		})
	}
}

func retentionHooks(tb testing.TB, db *sql.DB, update func(int, string, string, int64), commit func() int, rollback func()) {
	tb.Helper()
	conn, err := db.Conn(context.Background())
	if err != nil {
		tb.Fatal(err)
	}
	defer conn.Close()
	if err := conn.Raw(func(driver any) error {
		sqlite := driver.(*sqlite3.SQLiteConn)
		sqlite.RegisterUpdateHook(update)
		sqlite.RegisterCommitHook(commit)
		sqlite.RegisterRollbackHook(rollback)
		return nil
	}); err != nil {
		tb.Fatal(err)
	}
}

// Run explicitly with -run '^$' -bench BenchmarkRetentionFanout -benchtime=1x.
// Fixture creation stays outside the timer; all SQLite durability defaults stay
// at the production WriterDSN settings. No PostgreSQL server is involved.
func BenchmarkRetentionFanout(b *testing.B) {
	for _, fixture := range []struct {
		name                        string
		transmissions, observations int
	}{
		{"typical16", 16000, 256000},
		{"dense256", 500, 128000},
		{"singleHot", 1, 64000},
	} {
		b.Run(fixture.name, func(b *testing.B) {
			for n := 0; n < b.N; n++ {
				b.StopTimer()
				s, err := OpenStore(filepath.Join(b.TempDir(), "retention.db"))
				if err != nil {
					b.Fatal(err)
				}
				seedRetentionFanout(b, s, fixture.transmissions, fixture.observations)
				var inBatch, maxBatch, deleted, batches int
				retentionHooks(b, s.db, func(op int, _, table string, _ int64) {
					if op == sqlite3.SQLITE_DELETE && table == "observations" {
						inBatch++
						deleted++
					}
				}, func() int {
					if inBatch > maxBatch {
						maxBatch = inBatch
					}
					inBatch = 0
					batches++
					return 0
				}, func() { inBatch = 0 })
				ResetWriterStatsForTest()
				poolBefore := s.db.Stats()
				b.StartTimer()
				started := time.Now()
				removed, err := s.PruneOldPackets(5)
				elapsed := time.Since(started)
				b.StopTimer()
				poolAfter := s.db.Stats()
				if err != nil || removed != int64(fixture.transmissions) || deleted != fixture.observations {
					b.Fatalf("removed=%d deleted=%d err=%v", removed, deleted, err)
				}
				result := struct {
					Fixture                        string  `json:"fixture"`
					ElapsedMS                      float64 `json:"elapsed_ms"`
					MaxObservationDeletes, Commits int
					PoolWaitCount                  int64
					PoolWaitMS                     float64
					Writer                         WriterStatsSnapshot
				}{fixture.name, float64(elapsed) / float64(time.Millisecond), maxBatch, batches, poolAfter.WaitCount - poolBefore.WaitCount, float64(poolAfter.WaitDuration-poolBefore.WaitDuration) / float64(time.Millisecond), s.WriterStatsSnapshot()["prune_packets"]}
				encoded, _ := json.Marshal(result)
				b.Log(string(encoded))
				retentionHooks(b, s.db, nil, nil, nil)
				s.Close()
			}
		})
	}
}
