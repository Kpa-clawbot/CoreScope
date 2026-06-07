package main

import (
	"log"
	"sync"
	"sync/atomic"
)

// IngestBuffer decouples MQTT message receipt from DB writes (#1608).
//
// On boot the ingestor must subscribe to MQTT immediately, but the single
// SQLite writer (#1283) can be held for minutes by a startup migration
// (e.g. a large CREATE INDEX) or prune. Without buffering, every QoS-0 packet
// received in that window is lost. IngestBuffer holds received work in a
// bounded FIFO and a single consumer goroutine drains it once Ready() is
// called — i.e. once the write path is free.
//
// A single consumer preserves the single-writer invariant: jobs run one at a
// time, exactly as paho's in-order handler did before. Submit never blocks the
// MQTT delivery goroutine; if the buffer is full it drops and counts (bounded
// memory). Buffering replays the original messages, so it introduces NO
// duplicates (contrast: a QoS-1 broker-queue would).
type IngestBuffer struct {
	jobs      chan func()
	ready     chan struct{}
	stop      chan struct{}
	done      chan struct{}
	dropped   atomic.Int64
	startOnce sync.Once
	readyOnce sync.Once
	stopOnce  sync.Once
}

// NewIngestBuffer returns a buffer holding up to capacity pending jobs.
// Non-positive capacity is clamped to 1 and a WARN is logged so the
// misconfiguration is visible (PR #1609 m2 — silent clamp hid bad
// ingestBufferSize values).
func NewIngestBuffer(capacity int) *IngestBuffer {
	if capacity < 1 {
		log.Printf("[ingest-buffer] WARN: requested capacity %d < 1, clamping to 1 — check ingestBufferSize config; default is 50000", capacity)
		capacity = 1
	}
	return &IngestBuffer{
		jobs:  make(chan func(), capacity),
		ready: make(chan struct{}),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
}

// Submit enqueues a job without blocking. If the buffer is full the job is
// dropped and the dropped counter is incremented. Safe for concurrent callers.
//
// Ordering invariant: callers MUST call Start() before the first Submit().
// Submit only enqueues — without a running consumer, jobs sit in the channel
// and (once cap is reached) are silently dropped until Start()+Ready() run.
func (b *IngestBuffer) Submit(job func()) {
	select {
	case b.jobs <- job:
	default:
		n := b.dropped.Add(1)
		if n == 1 || n%1000 == 0 {
			log.Printf("[ingest-buffer] WARNING: buffer full, dropped %d message(s) — raise ingestBufferSize", n)
		}
	}
}

// Start launches the consumer goroutine. It blocks until Ready() is called
// (or Stop() fires, whichever comes first), then drains buffered jobs and
// runs newly-submitted ones serially, in FIFO order. Idempotent. The
// consumer exits and closes Done() when either (a) the jobs channel is
// closed by Stop() while drained, or (b) Stop() is called before Ready().
func (b *IngestBuffer) Start() {
	b.startOnce.Do(func() {
		go func() {
			defer close(b.done)
			select {
			case <-b.ready:
			case <-b.stop:
				// Stopped before Ready — exit immediately. Pending jobs
				// are discarded; the buffer was never authorized to drain.
				return
			}
			for {
				select {
				case job, ok := <-b.jobs:
					if !ok {
						return
					}
					job()
				case <-b.stop:
					// Stop after Ready — drain whatever is queued so
					// shutdown is graceful, then exit.
					for {
						select {
						case job, ok := <-b.jobs:
							if !ok {
								return
							}
							job()
						default:
							return
						}
					}
				}
			}
		}()
	})
}

// Ready signals that the write path is available; the consumer begins
// draining. Idempotent.
//
// Ordering invariant: Start() MUST have been called before Ready() takes
// effect. Calling Ready() without a prior Start() simply closes the ready
// channel — nothing drains until a later Start() runs its consumer goroutine.
func (b *IngestBuffer) Ready() {
	b.readyOnce.Do(func() { close(b.ready) })
}

// Dropped returns the number of jobs dropped due to a full buffer.
func (b *IngestBuffer) Dropped() int64 { return b.dropped.Load() }

// Pending returns the current queue depth (best-effort; for observability).
func (b *IngestBuffer) Pending() int { return len(b.jobs) }

// Stop signals the consumer goroutine to exit. Test-hygiene helper so unit
// tests don't leak the goroutine that Start() spawns. Idempotent / safe to
// call without a prior Start(). After Stop() the consumer exits and Done()
// is closed.
func (b *IngestBuffer) Stop() {
	b.stopOnce.Do(func() { close(b.stop) })
}

// Done returns a channel that is closed after the consumer goroutine has
// exited. If Start() was never called, Done() never closes.
func (b *IngestBuffer) Done() <-chan struct{} {
	return b.done
}
