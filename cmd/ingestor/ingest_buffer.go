package main

import (
	"log"
	"sync"
	"sync/atomic"
	"time"
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

	// dropLogMu guards the time-based drop-log throttle (PR #1623
	// round-1 fix to #1609 M1). Per-drop logging under sustained
	// stalls could flood the log at MQTT inbound rate; instead we
	// always log the FIRST drop of a stall and then summarize at
	// most once per second until the stall ends.
	dropLogMu      sync.Mutex
	stallActive    bool      // true between first drop and first successful Submit
	stallStart     time.Time // when the current stall began
	stallStartDrop int64     // dropped() value when stall began
	lastSummaryAt  time.Time // last time we wrote a summary line
}

// dropLogSummaryInterval is the minimum interval between summary lines
// during a sustained stall. Exposed as a var so tests can shrink it.
var dropLogSummaryInterval = time.Second

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
//
// Drop logging (PR #1623 round-1 fix to #1609 M1) uses a time-based
// throttle to stay loud-on-stall-start without flooding under sustained
// stalls:
//   - the FIRST drop of a stall logs immediately
//   - subsequent drops are summarized at most once per second
//   - when the next Submit succeeds, a "drained" recovery line is
//     emitted so operators can quantify the burst
//
// All log lines include the buffer capacity for operator triage.
func (b *IngestBuffer) Submit(job func()) {
	select {
	case b.jobs <- job:
		b.maybeLogRecovery()
	default:
		n := b.dropped.Add(1)
		b.logDrop(n)
	}
}

// logDrop emits a drop log line under the time-based throttle. The first
// drop of a stall always logs; subsequent drops summarize at most once
// per dropLogSummaryInterval.
func (b *IngestBuffer) logDrop(n int64) {
	b.dropLogMu.Lock()
	defer b.dropLogMu.Unlock()
	now := time.Now()
	if !b.stallActive {
		b.stallActive = true
		b.stallStart = now
		b.stallStartDrop = n - 1 // last successful Submit -> this is the 1st drop of the stall
		b.lastSummaryAt = now
		log.Printf("[ingest-buffer] WARNING: buffer full (cap %d), dropped %d message(s) total — write path stalled, raise ingestBufferSize or investigate slow writer", cap(b.jobs), n)
		return
	}
	if now.Sub(b.lastSummaryAt) >= dropLogSummaryInterval {
		b.lastSummaryAt = now
		stallDrops := n - b.stallStartDrop
		log.Printf("[ingest-buffer] WARNING: buffer full (cap %d), %d drop(s) in current stall, %d total — write path still stalled", cap(b.jobs), stallDrops, n)
	}
}

// maybeLogRecovery is called from the success branch of Submit. If a
// stall was active, it logs a recovery line summarizing the burst and
// clears the stall state.
func (b *IngestBuffer) maybeLogRecovery() {
	b.dropLogMu.Lock()
	defer b.dropLogMu.Unlock()
	if !b.stallActive {
		return
	}
	stallDrops := b.dropped.Load() - b.stallStartDrop
	dur := time.Since(b.stallStart)
	log.Printf("[ingest-buffer] INFO: buffer drained, %d drop(s) over %s (cap %d) — write path recovered", stallDrops, dur.Round(time.Millisecond), cap(b.jobs))
	b.stallActive = false
}

// Start launches the consumer goroutine. It blocks until Ready() is called
// (or Stop() fires, whichever comes first), then drains buffered jobs and
// runs newly-submitted ones serially, in FIFO order. Idempotent.
//
// Lifecycle: Stop() closes b.stop, which causes the consumer to exit via
// the stop-select arm (after draining any queued jobs if Ready() had
// already fired). The b.jobs channel is never closed — closing it would
// race with concurrent Submit() callers and panic; instead jobs is
// garbage-collected with the buffer once all references drop. Done() is
// closed when the consumer goroutine returns.
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
				case job := <-b.jobs:
					job()
				case <-b.stop:
					// Stop after Ready — drain whatever is queued so
					// shutdown is graceful, then exit. b.jobs is never
					// closed (see Start godoc), so a default-case
					// non-blocking receive is the correct drain idiom.
					for {
						select {
						case job := <-b.jobs:
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
