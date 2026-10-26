package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type healthzCountsResponse struct {
	Ready     bool `json:"ready"`
	LoadedTx  int  `json:"loadedTx"`
	LoadedObs int  `json:"loadedObs"`
	Backfill  *struct {
		Total     int64 `json:"total"`
		Processed int64 `json:"processed"`
		Done      bool  `json:"done"`
	} `json:"from_pubkey_backfill"`
	Liveness map[string]SourceLivenessSnapshot `json:"ingest_liveness"`
}

func readHealthzCounts(t testing.TB, store *PacketStore) healthzCountsResponse {
	t.Helper()
	w := httptest.NewRecorder()
	(&Server{store: store}).handleHealthz(w, httptest.NewRequest("GET", "/api/healthz", nil))
	var got healthzCountsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode healthz: %v", err)
	}
	if w.Code != http.StatusOK || !got.Ready || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("healthz: code=%d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
	}
	if got.Backfill == nil || got.Backfill.Total != 0 || got.Backfill.Processed != 0 || !got.Backfill.Done {
		t.Fatalf("backfill changed: %+v", got.Backfill)
	}
	return got
}

func assertHealthzCounts(t testing.TB, store *PacketStore, wantTx, wantObs int) {
	t.Helper()
	if store != nil {
		store.mu.RLock()
		txCount, obsCount, counter := len(store.packets), 0, store.totalObs
		for _, tx := range store.packets {
			obsCount += len(tx.Observations)
		}
		store.mu.RUnlock()
		if txCount != wantTx || obsCount != wantObs || counter != obsCount {
			t.Fatalf("retained=%d/%d counter=%d, want %d/%d", txCount, obsCount, counter, wantTx, wantObs)
		}
	}
	got := readHealthzCounts(t, store)
	if got.LoadedTx != wantTx || got.LoadedObs != wantObs {
		t.Fatalf("healthz=%d/%d, want %d/%d", got.LoadedTx, got.LoadedObs, wantTx, wantObs)
	}
}

func TestHealthzCountsLifecycle(t *testing.T) {
	readiness.Store(1)
	t.Cleanup(func() { readiness.Store(0) })
	t.Setenv("CORESCOPE_INGESTOR_STATS", filepath.Join(t.TempDir(), "absent.json"))
	assertHealthzCounts(t, nil, 0, 0)
	if got := readHealthzCounts(t, nil); got.Liveness != nil {
		t.Fatalf("missing stats must omit ingest_liveness: %v", got.Liveness)
	}

	for _, loader := range []string{"Load", "LoadChunked", "loadChunk", "RunStartupLoad"} {
		t.Run(loader, func(t *testing.T) {
			db := setupTestDB(t)
			t.Cleanup(func() { db.Close() })
			seedTestData(t, db) // three transmissions, four distinct observations
			now := time.Now().UTC()
			// Include both a transmission with no observations and a duplicate
			// observer/path row: readiness counts retained observations, not DB rows.
			mustExec(t, db, `INSERT INTO transmissions(raw_hex,hash,first_seen) VALUES ('AA','empty',?)`, now.Add(-24*time.Hour).Format(time.RFC3339))
			mustExec(t, db, `INSERT INTO observations(transmission_id,observer_idx,path_json,timestamp) VALUES (1,1,'["aa","bb"]',?)`, now.Unix())
			store := NewPacketStore(db, &PacketStoreConfig{RetentionHours: 72})
			assertHealthzCounts(t, store, 0, 0)
			var err error
			switch loader {
			case "Load":
				err = store.Load()
				if !store.WaitIndexesReady(5 * time.Second) {
					t.Fatal("background indexes did not finish")
				}
			case "LoadChunked":
				err = store.LoadChunked(1)
			case "loadChunk":
				err = store.loadChunk(now.Add(-72*time.Hour), now.Add(time.Hour))
			case "RunStartupLoad":
				store.hotStartupHours = 2
				store.OnChunkLoaded(func(_, _ int) {
					if isClosedForTest(store.StartupLoadDone()) {
						t.Error("startup signal closed during hot load")
					}
					assertHealthzCounts(t, store, 1, 2)
				})
				store.bgLoaderEntryHook = func() {
					if !store.LoadComplete() || isClosedForTest(store.StartupLoadDone()) {
						t.Error("hot load must finish before background fill, startup signal must remain open")
					}
					assertHealthzCounts(t, store, 1, 2)
				}
				err = store.RunStartupLoad(1)
				if !isClosedForTest(store.StartupLoadDone()) {
					t.Error("startup signal did not close after background fill")
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			assertHealthzCounts(t, store, 4, 4)

			mustExec(t, db, `INSERT INTO transmissions(raw_hex,hash,first_seen) VALUES ('BB','live',?)`, now.Format(time.RFC3339))
			mustExec(t, db, `INSERT INTO observations(transmission_id,observer_idx,path_json,timestamp) VALUES (5,1,'[]',?),(5,1,'[]',?)`, now.Unix(), now.Unix())
			store.IngestNewFromDB(4, 10)
			store.IngestNewFromDB(4, 10) // replay must not add observations
			assertHealthzCounts(t, store, 5, 5)
			mustExec(t, db, `INSERT INTO observations(transmission_id,observer_idx,path_json,timestamp) VALUES (5,2,'[]',?),(5,2,'[]',?)`, now.Unix(), now.Unix())
			store.IngestNewObservations(0, 100)
			store.IngestNewObservations(0, 100)
			assertHealthzCounts(t, store, 5, 6)

			store.mu.Lock()
			store.retentionHours = 2
			evicted := store.EvictStale()
			store.mu.Unlock()
			if evicted != 3 {
				t.Fatalf("evicted %d, want 3 old transmissions", evicted)
			}
			assertHealthzCounts(t, store, 2, 4)
		})
	}
}

func TestHealthzCountsFailedStartup(t *testing.T) {
	readiness.Store(1)
	t.Cleanup(func() { readiness.Store(0) })
	t.Setenv("CORESCOPE_INGESTOR_STATS", filepath.Join(t.TempDir(), "absent.json"))
	for _, failAt := range []string{"hot chunk", "background fill"} {
		t.Run(failAt, func(t *testing.T) {
			db := setupTestDB(t)
			t.Cleanup(func() { db.Close() })
			seedTestData(t, db)
			store := NewPacketStore(db, &PacketStoreConfig{RetentionHours: 72, HotStartupHours: 2})
			closeDB := func() {
				assertHealthzCounts(t, store, 1, 2)
				if isClosedForTest(store.StartupLoadDone()) {
					t.Error("startup signal closed before failure")
				}
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			}
			if failAt == "hot chunk" {
				store.OnChunkLoaded(func(_, _ int) { closeDB() })
			} else {
				store.bgLoaderEntryHook = closeDB
			}
			err := store.RunStartupLoad(1)
			if failAt == "hot chunk" && err == nil {
				t.Error("expected hot-chunk query failure")
			}
			if !store.backgroundLoadFailed.Load() || !isClosedForTest(store.StartupLoadDone()) {
				t.Fatal("failed startup must publish its terminal signal")
			}
			assertHealthzCounts(t, store, 1, 2)
		})
	}
}

func TestHealthzCountsConcurrentIngestAndEviction(t *testing.T) {
	readiness.Store(1)
	t.Cleanup(func() { readiness.Store(0) })
	t.Setenv("CORESCOPE_INGESTOR_STATS", filepath.Join(t.TempDir(), "absent.json"))
	db := setupTestDB(t)
	t.Cleanup(func() { db.Close() })
	mustExec(t, db, `INSERT INTO observers(id) VALUES ('obs1'),('obs2')`)
	store := NewPacketStore(db, &PacketStoreConfig{RetentionHours: 24})
	if err := store.RunStartupLoad(10); err != nil {
		t.Fatal(err)
	}
	var readers sync.WaitGroup
	var samples atomic.Int64
	started, stop := make(chan struct{}, 4), make(chan struct{})
	for i := 0; i < cap(started); i++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			started <- struct{}{}
			for {
				select {
				case <-stop:
					return
				default:
				}
				got := readHealthzCounts(t, store)
				// Each real ingest publishes one transmission with two
				// observations. Eviction removes whole transmissions.
				if got.LoadedObs != 2*got.LoadedTx {
					t.Errorf("torn snapshot: %d transmissions, %d observations", got.LoadedTx, got.LoadedObs)
					return
				}
				samples.Add(1)
			}
		}()
	}
	t.Cleanup(func() { close(stop); readers.Wait() })
	for i := 0; i < cap(started); i++ {
		<-started
	}
	old := time.Now().UTC().Add(-48 * time.Hour)
	for id := 1; id <= 60; id++ {
		mustExec(t, db, `INSERT INTO transmissions(id,raw_hex,hash,first_seen) VALUES (?,'AA',?,?)`, id, fmt.Sprintf("live-%d", id), old.Format(time.RFC3339))
		mustExec(t, db, `INSERT INTO observations(transmission_id,observer_idx,path_json,timestamp) VALUES (?,1,'[]',?),(?,2,'[]',?)`, id, old.Unix(), id, old.Unix())
		store.IngestNewFromDB(id-1, 10)
		if id%3 == 0 {
			store.mu.Lock()
			store.EvictStale()
			store.mu.Unlock()
		}
	}
	assertHealthzCounts(t, store, 0, 0)
	if samples.Load() <= int64(cap(started)) {
		t.Fatal("readers did not sample during ingestion and eviction")
	}
}

func TestHealthzCountsMemoryEviction(t *testing.T) {
	readiness.Store(1)
	t.Cleanup(func() { readiness.Store(0) })
	t.Setenv("CORESCOPE_INGESTOR_STATS", filepath.Join(t.TempDir(), "absent.json"))
	path := filepath.Join(t.TempDir(), "healthz.db")
	createTestDBWithObs(t, path, 2000)
	db, err := OpenDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	store := NewPacketStore(db, nil)
	if err := store.RunStartupLoad(500); err != nil {
		t.Fatal(err)
	}
	assertHealthzCounts(t, store, 2000, 6000)
	store.mu.Lock()
	store.maxMemoryMB = 1 // use the real tracked size, not a fake memory counter
	evicted := store.EvictStale()
	store.mu.Unlock()
	if evicted == 0 {
		t.Fatal("fixture did not exceed the memory budget")
	}
	wantObs := 6000
	for id := 1; id <= evicted; id++ {
		wantObs -= id%5 + 1
	}
	assertHealthzCounts(t, store, 2000-evicted, wantObs)
}

func TestHealthzCountsKeepsLiveness(t *testing.T) {
	readiness.Store(1)
	t.Cleanup(func() { readiness.Store(0) })
	path := filepath.Join(t.TempDir(), "stats.json")
	if err := os.WriteFile(path, []byte(`{"source_liveness":{"mqtt-test":{"lastReceiptUnix":100,"lastMessageUnix":90}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CORESCOPE_INGESTOR_STATS", path)
	got := readHealthzCounts(t, nil)
	if len(got.Liveness) != 1 || got.Liveness["mqtt-test"] != (SourceLivenessSnapshot{LastReceiptUnix: 100, LastMessageUnix: 90}) {
		t.Fatalf("liveness changed: %v", got.Liveness)
	}
}

// Benchmark the actual ready handler, including JSON and optional-liveness
// lookup, with the same SQLite-loaded 1-5 observation fixture at each scale.
// Compare identical commands on the base and candidate; no timing thresholds.
func BenchmarkHealthzCounts(b *testing.B) {
	readiness.Store(1)
	b.Cleanup(func() { readiness.Store(0) })
	b.Setenv("CORESCOPE_INGESTOR_STATS", filepath.Join(b.TempDir(), "absent.json"))
	for _, n := range []int{1000, 30000, 100000} {
		b.Run(fmt.Sprintf("tx=%d", n), func(b *testing.B) {
			path := filepath.Join(b.TempDir(), "healthz.db")
			createTestDBWithObs(b, path, n)
			db, err := OpenDB(path)
			if err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { db.Close() })
			store := NewPacketStore(db, nil)
			if err := store.RunStartupLoad(10000); err != nil {
				b.Fatal(err)
			}
			assertHealthzCounts(b, store, n, 3*n)
			srv := &Server{store: store}
			req := httptest.NewRequest("GET", "/api/healthz", nil)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				w := httptest.NewRecorder()
				srv.handleHealthz(w, req)
				if w.Code != http.StatusOK {
					b.Fatal(w.Code)
				}
			}
		})
	}
}
