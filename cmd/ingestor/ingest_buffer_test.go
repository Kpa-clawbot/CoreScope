package main

import (
	"bytes"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestIngestBuffer_BuffersUntilReady(t *testing.T) {
	b := NewIngestBuffer(10)
	t.Cleanup(b.Stop)
	var ran atomic.Int64
	b.Start()
	for i := 0; i < 3; i++ {
		b.Submit(func() { ran.Add(1) })
	}
	time.Sleep(30 * time.Millisecond)
	if ran.Load() != 0 {
		t.Fatalf("jobs ran before Ready(): %d", ran.Load())
	}
	b.Ready()
	deadline := time.Now().Add(time.Second)
	for ran.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if ran.Load() != 3 {
		t.Fatalf("want 3 ran after Ready, got %d", ran.Load())
	}
}

func TestIngestBuffer_FIFOOrder(t *testing.T) {
	b := NewIngestBuffer(10)
	t.Cleanup(b.Stop)
	out := make(chan int, 5)
	b.Start()
	for i := 0; i < 5; i++ {
		i := i
		b.Submit(func() { out <- i })
	}
	b.Ready()
	for want := 0; want < 5; want++ {
		select {
		case got := <-out:
			if got != want {
				t.Fatalf("order: want %d got %d", want, got)
			}
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for job %d", want)
		}
	}
}

func TestIngestBuffer_DropsWhenFull(t *testing.T) {
	b := NewIngestBuffer(2)
	t.Cleanup(b.Stop) // never Ready()'d -> nothing drains
	for i := 0; i < 5; i++ {
		b.Submit(func() {})
	}
	if got := b.Dropped(); got != 3 {
		t.Fatalf("want 3 dropped (cap 2, 5 submitted), got %d", got)
	}
}

func TestIngestBuffer_ProcessesAfterReady(t *testing.T) {
	b := NewIngestBuffer(10)
	t.Cleanup(b.Stop)
	b.Start()
	b.Ready()
	done := make(chan struct{})
	b.Submit(func() { close(done) })
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("job submitted after Ready was not processed")
	}
}

func TestIngestBuffer_SerialExecution(t *testing.T) {
	b := NewIngestBuffer(50)
	t.Cleanup(b.Stop)
	var inFlight atomic.Int32
	var overlap atomic.Bool
	var wg sync.WaitGroup
	b.Start()
	const n = 20
	wg.Add(n)
	for i := 0; i < n; i++ {
		b.Submit(func() {
			if inFlight.Add(1) > 1 {
				overlap.Store(true)
			}
			time.Sleep(time.Millisecond)
			inFlight.Add(-1)
			wg.Done()
		})
	}
	b.Ready()
	wg.Wait()
	if overlap.Load() {
		t.Fatal("jobs overlapped — consumer is not serial (violates single-writer)")
	}
}

func TestIngestBuffer_ConcurrentSubmitSafe(t *testing.T) {
	b := NewIngestBuffer(20000)
	t.Cleanup(b.Stop)
	b.Start()
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				b.Submit(func() {})
			}
		}()
	}
	wg.Wait()
	b.Ready()
	// Assertion is the absence of a race/panic; run under -race in CI.
}

// TestIngestBuffer_StopUnblocksConsumer guards the consumer-goroutine leak
// described in PR #1609 review m1: Start() blocks on <-b.ready forever if
// Ready() is never called, leaking the goroutine in test runs. Stop() must
// signal the consumer to exit cleanly without requiring Ready().
func TestIngestBuffer_StopUnblocksConsumer(t *testing.T) {
	b := NewIngestBuffer(10)
	t.Cleanup(b.Stop)
	b.Start()
	// Do NOT call Ready(). The consumer must exit purely because of Stop().
	b.Stop()
	select {
	case <-b.Done():
		// good — consumer goroutine returned
	case <-time.After(time.Second):
		t.Fatal("Stop() did not unblock the consumer goroutine within 1s (Done() never closed)")
	}
}

// TestNewIngestBuffer_WarnsOnSubOneClamp asserts that constructing the
// buffer with a non-positive capacity emits a WARN log line. Silent
// clamping (PR #1609 review m2) hid misconfigurations like
// ingestBufferSize=-1 or 0-from-default-not-applied paths.
func TestNewIngestBuffer_WarnsOnSubOneClamp(t *testing.T) {
	var buf bytes.Buffer
	oldOut := log.Writer()
	oldFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(oldOut)
		log.SetFlags(oldFlags)
	})

	b := NewIngestBuffer(0)
	t.Cleanup(b.Stop)

	got := buf.String()
	if !strings.Contains(got, "WARN") || !strings.Contains(got, "ingest-buffer") {
		t.Fatalf("expected WARN log on sub-one clamp, got %q", got)
	}
}

// TestIngestBuffer_DropLogThrottle asserts the time-based throttle (PR
// #1623 round-1 fix to #1609 M1): the FIRST drop of a stall logs
// immediately (loud), then subsequent drops within the same stall are
// rate-limited to at most one summary line per second, and a recovery
// line is emitted when Submit succeeds again. This prevents log-flood
// under sustained stalls (potentially hundreds of MB/min) while
// preserving "loud the instant the stall starts".
func TestIngestBuffer_DropLogThrottle(t *testing.T) {
	var buf bytes.Buffer
	oldOut := log.Writer()
	oldFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(oldOut)
		log.SetFlags(oldFlags)
	})

	b := NewIngestBuffer(2)
	t.Cleanup(b.Stop)
	// Fill to capacity (no Ready() — nothing drains).
	for i := 0; i < 2; i++ {
		b.Submit(func() {})
	}
	// 100 drops in tight loop (well under 1s).
	for i := 0; i < 100; i++ {
		b.Submit(func() {})
	}

	got := buf.String()
	lines := strings.Count(got, "buffer full")
	if lines < 1 {
		t.Fatalf("expected the FIRST drop to log immediately; got 0 'buffer full' lines:\n%s", got)
	}
	if lines > 2 {
		t.Fatalf("expected at most 2 'buffer full' lines for 100 drops in <1s (first + at-most-one summary), got %d:\n%s", lines, got)
	}
	// Every line must include the capacity for operator triage.
	if !strings.Contains(got, "cap 2") {
		t.Fatalf("expected every drop log line to include 'cap 2', got:\n%s", got)
	}
}

// TestIngestBuffer_DropLogFirstAlwaysImmediate guards the "loud the
// instant the stall starts" half of the throttle contract from PR
// #1623: even a single drop must log immediately, not be silently
// absorbed by the per-second summary window.
func TestIngestBuffer_DropLogFirstAlwaysImmediate(t *testing.T) {
	var buf bytes.Buffer
	oldOut := log.Writer()
	oldFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(oldOut)
		log.SetFlags(oldFlags)
	})

	b := NewIngestBuffer(1)
	t.Cleanup(b.Stop)
	b.Submit(func() {}) // fills cap=1
	b.Submit(func() {}) // first drop
	got := buf.String()
	if !strings.Contains(got, "buffer full") {
		t.Fatalf("expected FIRST drop to log immediately; got:\n%s", got)
	}
}

// TestIngestBuffer_DropLogRecoveryAfterDrain guards the recovery-line
// half of the throttle contract: once Submit succeeds again after one
// or more drops, a "recovered" / "drained" line must be emitted so
// operators can quantify the burst (PR #1623).
func TestIngestBuffer_DropLogRecoveryAfterDrain(t *testing.T) {
	var buf bytes.Buffer
	oldOut := log.Writer()
	oldFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(oldOut)
		log.SetFlags(oldFlags)
	})

	b := NewIngestBuffer(1)
	t.Cleanup(b.Stop)
	b.Submit(func() {}) // fills cap=1
	for i := 0; i < 3; i++ {
		b.Submit(func() {}) // drops
	}
	// Drain: start consumer and Ready(), wait for queue to empty.
	b.Start()
	b.Ready()
	deadline := time.Now().Add(time.Second)
	for b.Pending() > 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	// Now a successful Submit should trigger the recovery line.
	b.Submit(func() {})
	// Give the goroutine + log a moment.
	time.Sleep(20 * time.Millisecond)

	got := buf.String()
	if !strings.Contains(got, "drained") && !strings.Contains(got, "recovered") {
		t.Fatalf("expected a 'drained'/'recovered' log line after stall ended; got:\n%s", got)
	}
}
