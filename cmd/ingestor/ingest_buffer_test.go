package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestIngestBuffer_BuffersUntilReady(t *testing.T) {
	b := NewIngestBuffer(10)
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
	b := NewIngestBuffer(2) // never Ready()'d -> nothing drains
	for i := 0; i < 5; i++ {
		b.Submit(func() {})
	}
	if got := b.Dropped(); got != 3 {
		t.Fatalf("want 3 dropped (cap 2, 5 submitted), got %d", got)
	}
}

func TestIngestBuffer_ProcessesAfterReady(t *testing.T) {
	b := NewIngestBuffer(10)
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
