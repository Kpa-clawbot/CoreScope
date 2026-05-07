package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestHealthzNotReady(t *testing.T) {
	// Ensure readiness is 0 (not ready)
	readiness.Store(0)
	defer readiness.Store(0)

	srv := &Server{store: &PacketStore{}}
	req := httptest.NewRequest("GET", "/api/healthz", nil)
	w := httptest.NewRecorder()

	srv.handleHealthz(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp["ready"] != false {
		t.Fatalf("expected ready=false, got %v", resp["ready"])
	}
	if resp["reason"] != "loading" {
		t.Fatalf("expected reason=loading, got %v", resp["reason"])
	}
}

func TestHealthzReady(t *testing.T) {
	readiness.Store(1)
	defer readiness.Store(0)

	srv := &Server{store: &PacketStore{}}
	req := httptest.NewRequest("GET", "/api/healthz", nil)
	w := httptest.NewRecorder()

	srv.handleHealthz(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp["ready"] != true {
		t.Fatalf("expected ready=true, got %v", resp["ready"])
	}
	if _, ok := resp["loadedTx"]; !ok {
		t.Fatal("missing loadedTx field")
	}
	if _, ok := resp["loadedObs"]; !ok {
		t.Fatal("missing loadedObs field")
	}
}

func TestHealthzAntiTautology(t *testing.T) {
	// When readiness is 0, must NOT return 200
	readiness.Store(0)
	defer readiness.Store(0)

	srv := &Server{store: &PacketStore{}}
	req := httptest.NewRequest("GET", "/api/healthz", nil)
	w := httptest.NewRecorder()

	srv.handleHealthz(w, req)

	if w.Code == http.StatusOK {
		t.Fatal("anti-tautology: handler returned 200 when readiness=0; gating is broken")
	}
}

// TestHealthzExposesFromPubkeyBackfill verifies the from_pubkey backfill
// progress (#1143, M2) is observable via /api/healthz. The atomics are
// updated by backfillFromPubkeyAsync; without exposure here they were dead
// code. Asserts the response includes a from_pubkey_backfill object with
// total/processed/done fields.
func TestHealthzExposesFromPubkeyBackfill(t *testing.T) {
	readiness.Store(1)
	defer readiness.Store(0)

	// Set known values so we can assert wiring (not just presence).
	fromPubkeyBackfillReset()
	fromPubkeyBackfillSetTotal(7)
	fromPubkeyBackfillSetProcessed(3)
	defer fromPubkeyBackfillReset()

	srv := &Server{store: &PacketStore{}}
	req := httptest.NewRequest("GET", "/api/healthz", nil)
	w := httptest.NewRecorder()
	srv.handleHealthz(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	bf, ok := resp["from_pubkey_backfill"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing from_pubkey_backfill object in healthz response: %v", resp)
	}
	if got, want := bf["total"], float64(7); got != want {
		t.Errorf("from_pubkey_backfill.total = %v, want %v", got, want)
	}
	if got, want := bf["processed"], float64(3); got != want {
		t.Errorf("from_pubkey_backfill.processed = %v, want %v", got, want)
	}
	if got, want := bf["done"], false; got != want {
		t.Errorf("from_pubkey_backfill.done = %v, want %v", got, want)
	}
}

// TestHealthzFromPubkeyBackfillConsistentSnapshot exercises cycle-3 m2c:
// the handler used to read three independent atomics (Total/Processed/Done)
// in sequence, so a backfill update interleaved between reads could yield
// an inconsistent snapshot (e.g. done=true with processed<total, or
// processed>total when total is updated last). This test races concurrent
// progress updates against many healthz reads and asserts every snapshot
// satisfies the invariants:
//
//	processed <= total
//	if done: processed == total (or both 0 — nothing to do)
//
// With the pre-fix code (separate atomic.Load calls), this fires within
// a few hundred iterations on a multi-core box. With the RWMutex-guarded
// snapshot, it never fires.
func TestHealthzFromPubkeyBackfillConsistentSnapshot(t *testing.T) {
	readiness.Store(1)
	defer readiness.Store(0)
	defer fromPubkeyBackfillReset()

	srv := &Server{store: &PacketStore{}}

	stop := make(chan struct{})
	var writerWg sync.WaitGroup
	var readerWg sync.WaitGroup

	// Writer: simulates the backfill loop — sets total, then increments
	// processed in lock-step, occasionally finishing (done=true with
	// processed==total). Each "tick" mutates all three values.
	writerWg.Add(1)
	go func() {
		defer writerWg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			fromPubkeyBackfillSetTotal(100)
			for p := int64(0); p <= 100; p++ {
				select {
				case <-stop:
					return
				default:
				}
				fromPubkeyBackfillSetProcessed(p)
			}
			fromPubkeyBackfillMarkDone()
			fromPubkeyBackfillReset()
		}
	}()

	// Readers: hammer healthz, assert invariants on each response.
	const readers = 8
	const reads = 200
	errs := make(chan string, readers*reads)
	for i := 0; i < readers; i++ {
		readerWg.Add(1)
		go func() {
			defer readerWg.Done()
			for j := 0; j < reads; j++ {
				req := httptest.NewRequest("GET", "/api/healthz", nil)
				w := httptest.NewRecorder()
				srv.handleHealthz(w, req)
				var resp map[string]interface{}
				if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
					errs <- "invalid JSON: " + err.Error()
					return
				}
				bf, _ := resp["from_pubkey_backfill"].(map[string]interface{})
				total, _ := bf["total"].(float64)
				processed, _ := bf["processed"].(float64)
				done, _ := bf["done"].(bool)
				if processed > total {
					errs <- "processed>total snapshot: processed=" + ftoa(processed) + " total=" + ftoa(total)
					return
				}
				if done && processed != total {
					errs <- "done=true but processed!=total: processed=" + ftoa(processed) + " total=" + ftoa(total)
					return
				}
			}
		}()
	}

	// Wait for readers to complete (bounded by 'reads' iterations), then
	// stop the writer and drain.
	readerDone := make(chan struct{})
	go func() { readerWg.Wait(); close(readerDone) }()
	select {
	case <-readerDone:
	case <-time.After(5 * time.Second):
		close(stop)
		writerWg.Wait()
		t.Fatal("timed out waiting for reader goroutines")
	}
	close(stop)
	writerWg.Wait()

	close(errs)
	for e := range errs {
		t.Errorf("inconsistent snapshot: %s", e)
	}
}

func ftoa(f float64) string { return fmt.Sprintf("%g", f) }
