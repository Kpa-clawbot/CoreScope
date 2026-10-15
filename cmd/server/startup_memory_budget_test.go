package main

import (
	"fmt"
	"runtime"
	"testing"
	"time"
)

// Each transmission has many distinct observer/path pairs. A transmission-count
// estimate cannot bound this fixture: every row is retained by the real deduper.
func startupMemoryFixture(t testing.TB, transmissions, observations int, recent bool) *DB {
	t.Helper()
	dsn := postgresTestDSN(t)
	createTestDBAt(t, dsn, 0)
	w, err := openFixtureSQL(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if _, err := w.Exec("ALTER TABLE transmissions ADD COLUMN last_seen BIGINT"); err != nil {
		t.Fatal(err)
	}
	// Use the production v3 epoch timestamp/observer-index representation, so
	// SQL fallback and PostgreSQL parameter types are exercised realistically.
	if _, err := w.Exec("DROP TABLE observations"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Exec(`CREATE TABLE observations (
		id BIGINT PRIMARY KEY, transmission_id BIGINT, observer_idx BIGINT,
		direction TEXT, snr DOUBLE PRECISION, rssi DOUBLE PRECISION, score INTEGER,
		path_json TEXT, timestamp BIGINT, raw_hex TEXT
	)`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	stamp := func(n int) time.Time {
		if recent || n == 0 {
			return now.Add(-30 * time.Minute)
		}
		return now.Add(-2 * time.Hour).Add(-time.Duration(n/3) * time.Second)
	}
	copyTestRows(t, w, "transmissions", []string{"id", "raw_hex", "hash", "first_seen", "route_type", "payload_type", "decoded_json", "last_seen"}, transmissions, func(n int) []any {
		return []any{n, "aabb", fmt.Sprintf("budget-%04d", n), stamp(n).Format(time.RFC3339), 0, 4, "{}", stamp(n).Unix()}
	})
	copyTestRows(t, w, "observers", []string{"rowid", "id", "name"}, observations, func(n int) []any {
		return []any{n, fmt.Sprintf("observer-%04d", n), "Synthetic observer"}
	})
	copyTestRows(t, w, "observations", []string{"id", "transmission_id", "observer_idx", "direction", "snr", "rssi", "score", "path_json", "timestamp"}, transmissions*observations, func(n int) []any {
		return []any{n + 1, n / observations, n % observations, "rx", 5.0, -80.0, 5, `["aa","bb","cc","dd","ee","ff","ab","bc","cd","de","ef","fa"]`, stamp(n / observations).Unix()}
	})
	for _, query := range []string{
		"CREATE INDEX budget_observations ON observations(transmission_id, timestamp)",
	} {
		if _, err := w.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	r, err := openFixtureReader(t, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.conn.Close() })
	return r
}

func assertStartupBudget(t *testing.T, s *PacketStore, observations int) {
	t.Helper()
	if s.trackedBytes > int64(s.maxMemoryMB)*1048576 {
		t.Fatalf("cache exceeds %d MiB budget: %.2f MiB, %d transmissions/%d observations", s.maxMemoryMB, s.trackedMemoryMB(), len(s.packets), s.totalObs)
	}
	for i, tx := range s.packets {
		if len(tx.Observations) != observations {
			t.Fatalf("partial transmission %d cached: %d/%d observations", tx.ID, len(tx.Observations), observations)
		}
		if s.byHash[tx.Hash] != tx || s.byTxID[tx.ID] != tx {
			t.Fatalf("transmission %d missing from indexes", tx.ID)
		}
		if i > 0 && s.packets[i-1].FirstSeen > tx.FirstSeen {
			t.Fatal("packet slice is not sorted oldest-first")
		}
	}
	if len(s.byHash) != len(s.packets) || len(s.byTxID) != len(s.packets) || len(s.byObsID) != s.totalObs {
		t.Fatal("partial transmission leaked into an index")
	}
}

func TestStartupMemoryBudget_HighFanout(t *testing.T) {
	for _, hotHours := range []float64{0, 1} {
		t.Run(fmt.Sprintf("hot-%g", hotHours), func(t *testing.T) {
			db := startupMemoryFixture(t, 120, 300, hotHours == 0)
			s := NewPacketStore(db, &PacketStoreConfig{MaxMemoryMB: 1, RetentionHours: 24, HotStartupHours: hotHours})
			if err := s.RunStartupLoad(10000); err != nil {
				t.Fatal(err)
			}
			assertStartupBudget(t, s, 300)
			if len(s.packets) == 0 || len(s.packets) >= 120 {
				t.Fatalf("expected a useful bounded cache, got %d/120", len(s.packets))
			}
			if !s.LoadComplete() || !s.backgroundLoadDone.Load() || s.backgroundLoadFailed.Load() {
				t.Fatalf("configured memory limit must finish successfully: %q", s.BackgroundLoadError())
			}
			if s.LoadCoverageRatio() >= 1 {
				t.Fatal("bounded cache must not claim complete database coverage")
			}
			if s.maxTxID != 119 || s.maxObsID != 120*300 {
				t.Fatalf("poll cursors replay excluded history: tx=%d, obs=%d", s.maxTxID, s.maxObsID)
			}
			if replayed, _ := s.IngestNewFromDB(s.MaxTransmissionID(), 500); len(replayed) != 0 {
				t.Fatalf("poll replayed %d omitted historical transmissions", len(replayed))
			}
			if replayed := s.IngestNewObservations(s.MaxObservationID(), 500); len(replayed) != 0 {
				t.Fatalf("poll replayed %d omitted historical observations", len(replayed))
			}
			if hotHours > 0 && s.byTxID[0] == nil {
				t.Fatal("old transmission ID with newest traffic was lost")
			}
			result := s.QueryPackets(PacketQuery{Since: time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339), Limit: 200})
			if result.Total != 120 {
				t.Fatalf("SQL history fallback omitted transmissions: %d/120", result.Total)
			}
		})
	}
}

func TestStartupMemoryBudget_OversizedTransmission(t *testing.T) {
	db := startupMemoryFixture(t, 1, 5000, true)
	s := NewPacketStore(db, &PacketStoreConfig{MaxMemoryMB: 1, RetentionHours: 24, HotStartupHours: 1})
	if err := s.RunStartupLoad(10000); err != nil {
		t.Fatal(err)
	}
	assertStartupBudget(t, s, 5000)
	if len(s.packets) != 0 || s.totalObs != 0 {
		t.Fatal("oversized transmission must stay in SQL, not partially in cache")
	}
	if s.QueryPackets(PacketQuery{Hash: "budget-0000", Limit: 1}).Total != 1 {
		t.Fatal("omitted transmission is unavailable without a date filter")
	}
	if s.QueryGroupedPackets(PacketQuery{Hash: "budget-0000", Limit: 1}).Total != 1 {
		t.Fatal("omitted transmission is unavailable in grouped packet queries")
	}
	if !s.backgroundLoadDone.Load() || s.backgroundLoadFailed.Load() {
		t.Fatalf("oversized row must not prevent readiness: %q", s.BackgroundLoadError())
	}
}

func TestStartupMemoryBudget_TimeAndIDCursor(t *testing.T) {
	db := startupMemoryFixture(t, 100, 30, true)
	s := NewPacketStore(db, &PacketStoreConfig{MaxMemoryMB: 1, RetentionHours: 24})
	if err := s.RunStartupLoad(2); err != nil {
		t.Fatal(err)
	}
	assertStartupBudget(t, s, 30)
	if s.byTxID[99] == nil || s.byTxID[0] != nil {
		t.Fatal("equal-time rows must load highest IDs first")
	}
	for id := 100 - len(s.packets); id < 100; id++ {
		if s.byTxID[id] == nil {
			t.Fatalf("time/ID cursor skipped transmission %d", id)
		}
	}
	// All timestamps tie; the floor must be conservative across the omitted
	// part of that second, rather than claiming that every tied row was loaded.
	if s.oldestLoaded <= s.packets[0].FirstSeen {
		t.Fatalf("cache coverage floor includes omitted tied rows: %s", s.oldestLoaded)
	}
}

func TestStartupMemoryBudget_MergesLiveObservations(t *testing.T) {
	db, writer := openPostgresValueFixture(t)
	now := time.Now().UTC().Truncate(time.Second)
	for i := 1; i <= 4; i++ {
		if _, err := writer.Exec(`INSERT INTO observers(rowid,id,name) VALUES($1,$2,$2)`, i, fmt.Sprintf("merge-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := writer.Exec(`INSERT INTO transmissions(id,raw_hex,hash,first_seen,last_seen,payload_type) VALUES(1,'AA','merge-history',$1,$2,4)`, now.Add(-3*time.Hour).Format(time.RFC3339), now.Unix()); err != nil {
		t.Fatal(err)
	}
	addObservation := func(id int) {
		t.Helper()
		if _, err := writer.Exec(`INSERT INTO observations(id,transmission_id,observer_idx,path_json,timestamp) VALUES($1,1,$1,'[]',$2)`, id, now.Add(time.Duration(id)*time.Minute).Unix()); err != nil {
			t.Fatal(err)
		}
	}
	addObservation(3)
	s := NewPacketStore(db, &PacketStoreConfig{MaxMemoryMB: 1, RetentionHours: 24})
	s.IngestNewFromDB(0, 10)
	original := s.byTxID[1]
	if original == nil || len(original.Observations) != 1 {
		t.Fatal("live observation setup failed")
	}
	addObservation(1)
	addObservation(2)
	// The loader has finished its SQL scan but has not published it yet.
	// A live poll is queued behind the advert-evidence gate with an observation
	// not present in the loader's snapshot. It must extend the same pointer.
	hookCalls := 0
	pollDone := make(chan struct{})
	db.advertEvidenceReadHook = func() {
		hookCalls++
		if hookCalls == 1 {
			addObservation(4)
			go func() {
				s.IngestNewObservations(3, 10)
				close(pollDone)
			}()
		}
	}
	if err := s.loadChunk(now.Add(-24*time.Hour), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if hookCalls == 0 {
		t.Fatal("interleaving hook did not run")
	}
	select {
	case <-pollDone:
	case <-time.After(5 * time.Second):
		t.Fatal("live poll remained blocked after loading")
	}
	assertStartupBudget(t, s, 4)
	if s.byTxID[1] != original || len(s.packets) != 1 {
		t.Fatal("load replaced the live pointer or duplicated its packet")
	}
	latest, err := time.Parse(time.RFC3339Nano, original.LatestSeen)
	if err != nil || !latest.Equal(now.Add(4*time.Minute)) {
		t.Fatalf("newer live observation lost: %q", original.LatestSeen)
	}
	bytes := s.trackedBytes
	if err := s.loadChunk(now.Add(-24*time.Hour), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	assertStartupBudget(t, s, 4)
	if s.trackedBytes != bytes || len(s.byPayloadType[4]) != 1 {
		t.Fatal("overlapping load duplicated accounting or payload index")
	}
}

func BenchmarkStartupMemoryBudget(b *testing.B) {
	benchmarkStartupMemoryBudget(b, 6000, 65)
}

func BenchmarkStartupMemoryBudgetTypicalFanout(b *testing.B) {
	benchmarkStartupMemoryBudget(b, 30000, 6)
}

func benchmarkStartupMemoryBudget(b *testing.B, transmissions, observations int) {
	b.StopTimer()
	db := startupMemoryFixture(b, transmissions, observations, false)
	var retained uint64
	for i := 0; i < b.N; i++ {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		s := NewPacketStore(db, &PacketStoreConfig{MaxMemoryMB: 16, RetentionHours: 24, HotStartupHours: 1})
		b.StartTimer()
		if err := s.RunStartupLoad(10000); err != nil {
			b.Fatal(err)
		}
		b.StopTimer()
		runtime.GC()
		runtime.ReadMemStats(&after)
		if after.HeapAlloc > before.HeapAlloc {
			retained += after.HeapAlloc - before.HeapAlloc
		}
		b.ReportMetric(s.trackedMemoryMB(), "cache-MiB")
		runtime.KeepAlive(s)
	}
	b.ReportMetric(float64(retained)/float64(b.N)/1048576, "heap-MiB")
}
