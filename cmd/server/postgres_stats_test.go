package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPostgresInitialDiagnosticFailureIsExplicit(t *testing.T) {
	db := setupTestDB(t)
	db.conn.Close()
	bytes, err := json.Marshal(db.GetDBSizeStatsTyped())
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(bytes, &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] == nil || body["error"] == "" || body["rows"] != nil || body["dbSizeMB"] != nil {
		t.Fatalf("unknown database size/counts must not become zeros: %s", bytes)
	}
	w := httptest.NewRecorder()
	(&Server{db: db}).handlePerfPostgres(w, httptest.NewRequest("GET", "/api/perf/postgres", nil))
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["error"] == nil || body["databaseBytes"] != nil || body["cacheHitRate"] != nil {
		t.Fatalf("unknown database measurements must be null: %s", w.Body.String())
	}
}

func TestPostgresBlockCacheRatioNullAndZero(t *testing.T) {
	db := setupTestDB(t)
	nineTenths, zero, one := 0.9, 0.0, 1.0
	for _, tc := range []struct {
		name        string
		hits, reads any
		want        *float64
	}{
		{"hits", 9, 1, &nineTenths}, {"misses", 0, 9, &zero},
		{"all cached", 9, 0, &one}, {"no reads", 0, 0, nil},
		{"unavailable", nil, 9, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got *float64
			err := db.conn.QueryRow(`SELECT `+postgresBlockCacheRateSQL+` FROM (VALUES ($1::bigint,$2::bigint)) AS counters(blks_hit,blks_read)`, tc.hits, tc.reads).Scan(&got)
			if err != nil {
				t.Fatal(err)
			}
			if (got == nil) != (tc.want == nil) {
				t.Fatalf("ratio = %v, want %v", got, tc.want)
			}
			if got != nil && math.Abs(*got-*tc.want) > 1e-12 {
				t.Fatalf("ratio = %g, want %g", *got, *tc.want)
			}
		})
	}
}

func TestPostgresDiagnosticRequestsCoalesce(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	const requests = 12
	misses := make(chan struct{}, requests)
	release := make(chan struct{})
	var queries atomic.Int64
	db.pgStatsMissHook = func() { misses <- struct{}{} }
	db.pgStatsQueryHook = func() { queries.Add(1); <-release }
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				if got := db.GetDBSizeStatsTyped(); got.Rows.Transmissions != 3 {
					t.Errorf("sample rows = %d", got.Rows.Transmissions)
				}
			} else {
				w := httptest.NewRecorder()
				(&Server{db: db}).handlePerfPostgres(w, httptest.NewRequest("GET", "/api/perf/postgres", nil))
				if w.Code != 200 {
					t.Errorf("status = %d", w.Code)
				}
			}
		}(i)
	}
	// Every request must have observed the empty cache before the first query
	// completes. This proves coalescing without sleeps or timing assumptions.
	for i := 0; i < requests; i++ {
		<-misses
	}
	close(release)
	wg.Wait()
	if got := queries.Load(); got != 1 {
		t.Fatalf("expensive sampling queries = %d, want 1", got)
	}
}

func TestPostgresDiagnosticTTLDoesNotFollowIngestInvalidation(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	store := NewPacketStore(db, nil)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	queries := 0
	db.pgStatsQueryHook = func() {
		queries++
		if !db.pgStatsMu.TryLock() {
			t.Error("expensive sampling ran under the cache lock")
		} else {
			db.pgStatsMu.Unlock()
		}
	}
	first := db.GetDBSizeStatsTyped()
	if first.Rows.Transmissions != 3 || first.SampledAt == "" || first.SampleIntervalSeconds != 30 {
		t.Fatalf("initial sample = %+v", first)
	}
	if _, err := db.conn.Exec(`INSERT INTO transmissions(raw_hex,hash,first_seen,payload_type,decoded_json) VALUES('AA','new-sample-row',$1,5,'{}')`, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	store.IngestNewFromDB(3, 100)
	if got := db.GetDBSizeStatsTyped(); got.Rows.Transmissions != 3 || queries != 1 {
		t.Fatalf("ingest invalidated diagnostic sample: rows=%d queries=%d", got.Rows.Transmissions, queries)
	}
	db.pgStatsMu.Lock()
	db.pgStatsExpires = time.Time{}
	db.pgStatsMu.Unlock()
	if got := db.GetDBSizeStatsTyped(); got.Rows.Transmissions != 4 || queries != 2 {
		t.Fatalf("expiry did not refresh: rows=%d queries=%d", got.Rows.Transmissions, queries)
	}
}

func TestPostgresDiagnosticFailureMarksOldSampleStale(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	first, err := db.postgresStats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	db.pgStatsMu.Lock()
	db.pgStatsExpires = time.Time{}
	db.pgStatsMu.Unlock()
	db.conn.Close()
	got := db.GetDBSizeStatsTyped()
	if !got.Stale || got.SampledAt != first.SampledAt.UTC().Format(time.RFC3339Nano) || got.Rows.Transmissions != 3 {
		t.Fatalf("failed refresh did not disclose stale sample: %+v", got)
	}
}

func TestPostgresDiagnosticCanceledCallerKeepsSharedSample(t *testing.T) {
	db := setupTestDB(t)
	seedTestData(t, db)
	misses, entered, release := make(chan struct{}, 2), make(chan struct{}), make(chan struct{})
	var once sync.Once
	var queries atomic.Int64
	db.pgStatsMissHook = func() { misses <- struct{}{} }
	db.pgStatsQueryHook = func() { queries.Add(1); once.Do(func() { close(entered) }); <-release }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	go func() { _, err := db.postgresStats(ctx); first <- err }()
	<-misses
	<-entered
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation: %v", err)
	}
	second := make(chan error, 1)
	go func() { _, err := db.postgresStats(context.Background()); second <- err }()
	<-misses
	close(release)
	if err := <-second; err != nil {
		t.Fatalf("shared sample was canceled: %v", err)
	}
	if queries.Load() != 1 {
		t.Fatalf("queries = %d, want 1", queries.Load())
	}
}
