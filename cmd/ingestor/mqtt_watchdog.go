package main

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// heartbeatInterval is how often the watchdog re-emits a still-stalled
// reminder once the initial WARN edge has fired. 1h matches the pager
// budget — frequent enough that an unattended stall is noticed within a
// shift, infrequent enough not to spam ops chat.
const livenessHeartbeatInterval = time.Hour

// LivenessKind enumerates the watchdog verdicts for a source. Edge-triggered
// transitions use this to decide whether to emit (and what severity).
type LivenessKind int

const (
	LivenessOK LivenessKind = iota
	LivenessStalled
	LivenessNeverReceived
	LivenessRecovered
	LivenessHeartbeat
)

// SourceLivenessState tracks per-source last-message timestamp and connection
// state for the stall watchdog (#1212). LastMessageUnix is updated by the
// message handler via atomic store; the watchdog reads it via atomic load.
//
// PR #1216 r1 added:
//   - StartedAt: cold-start grace clock; lets the watchdog distinguish
//     "never received since registration" (alarming) from "just registered,
//     paho hasn't subscribed yet" (normal).
//   - LastAlertUnix: edge-trigger cooldown; prevents 60-per-hour re-emits
//     of the same WARN.
type SourceLivenessState struct {
	Tag             string
	Broker          string
	LastMessageUnix int64 // atomic; unix seconds of last successfully received MQTT message
	StartedAt       int64 // atomic; unix seconds when the source was registered / last reconnected
	LastAlertUnix   int64 // atomic; unix seconds of last emit (WARN or heartbeat); 0 means quiet
	IsConnectedFn   func() bool
	// AttemptCount is incremented on every TCP/TLS connection attempt. Used
	// by ConnectionAttemptHandler to log attempt # independent of paho's
	// internal reconnect-loop state. atomic.
	AttemptCount int64
}

// MarkMessage records the time of a received MQTT message. Cheap; safe to
// call from the message-handling hot path.
func (s *SourceLivenessState) MarkMessage(now time.Time) {
	atomic.StoreInt64(&s.LastMessageUnix, now.Unix())
}

// MarkReconnected clears stale liveness state so the watchdog does not
// false-alarm on a pre-outage timestamp after paho re-establishes the
// connection (PR #1216 r1 item 2). Resets LastMessageUnix, re-stamps
// StartedAt (cold-start grace window restarts), and clears LastAlertUnix
// (edge-trigger re-arms).
func (s *SourceLivenessState) MarkReconnected(now time.Time) {
	atomic.StoreInt64(&s.LastMessageUnix, 0)
	atomic.StoreInt64(&s.StartedAt, now.Unix())
	atomic.StoreInt64(&s.LastAlertUnix, 0)
}

// checkSourceLiveness returns (message, kind) describing the source's
// liveness state. kind==LivenessOK means quiet/healthy; any other kind
// indicates the caller may want to emit (subject to edge-trigger).
//
// Cold-start (PR #1216 r1 item 1): when LastMessageUnix==0, the source
// has never published a single message. If StartedAt was stamped at
// registration and more than `threshold` has elapsed, this is the
// #1212 failure class — wrong channel hash, ACL drops SUBSCRIBE,
// half-open TCP after CONNECT. We emit a DISTINCT "NEVER received"
// alarm so operators can grep for it independently of generic stalls.
func checkSourceLiveness(s *SourceLivenessState, threshold time.Duration, now time.Time) (string, LivenessKind) {
	if s == nil || s.IsConnectedFn == nil {
		return "", LivenessOK
	}
	if !s.IsConnectedFn() {
		// paho's reconnect handler covers the disconnected case.
		return "", LivenessOK
	}
	last := atomic.LoadInt64(&s.LastMessageUnix)
	if last == 0 {
		started := atomic.LoadInt64(&s.StartedAt)
		if started == 0 {
			// Registration didn't stamp StartedAt — conservative: stay quiet.
			return "", LivenessOK
		}
		sinceStart := now.Sub(time.Unix(started, 0))
		if sinceStart < threshold {
			return "", LivenessOK
		}
		msg := fmt.Sprintf("MQTT [%s] WATCHDOG: client reports connected to %s but has NEVER received a message in %s (threshold %s) — check channel hash / subscribe ACL / half-open TCP",
			s.Tag, s.Broker, sinceStart.Round(time.Second), threshold)
		return msg, LivenessNeverReceived
	}
	silentFor := now.Sub(time.Unix(last, 0))
	if silentFor < threshold {
		return "", LivenessOK
	}
	msg := fmt.Sprintf("MQTT [%s] WATCHDOG: client reports connected to %s but no messages received for %s (threshold %s) — possible half-open socket or upstream stall",
		s.Tag, s.Broker, silentFor.Round(time.Second), threshold)
	return msg, LivenessStalled
}

// livenessRegistry is a package-level lookup so handleMessage (called with
// only `tag string`) can mark liveness without threading the state through
// every call site. Reads dominate (per message); writes happen once per
// source at startup.
var (
	livenessRegistry   = map[string]*SourceLivenessState{}
	livenessRegistryMu sync.RWMutex
)

// registerLivenessState publishes a state to the registry by tag. Returns
// an error on tag collision (PR #1216 r1 item 4) so operators see a
// startup misconfiguration instead of silently losing AttemptCount and
// LastMessageUnix for the clobbered source. The collision case is real:
// two MQTT sources with empty Name fall back to Broker; two sources with
// duplicate Name; copy-paste in config.json. Caller (main) decides whether
// to fatal or just log and skip. The first registration remains
// authoritative — we do NOT overwrite.
//
// Also stamps StartedAt so the cold-start watchdog knows when the
// grace clock starts.
func registerLivenessState(s *SourceLivenessState) error {
	livenessRegistryMu.Lock()
	defer livenessRegistryMu.Unlock()
	if existing, ok := livenessRegistry[s.Tag]; ok {
		return fmt.Errorf("liveness registry: duplicate tag %q (existing broker=%s, new broker=%s) — fix config so each MQTT source has a unique Name", s.Tag, existing.Broker, s.Broker)
	}
	if atomic.LoadInt64(&s.StartedAt) == 0 {
		atomic.StoreInt64(&s.StartedAt, time.Now().Unix())
	}
	livenessRegistry[s.Tag] = s
	return nil
}

// markLivenessForTag is the hot-path entry point: O(1) map lookup +
// atomic store. Safe to call for unknown tags (no-op).
func markLivenessForTag(tag string, now time.Time) {
	livenessRegistryMu.RLock()
	s := livenessRegistry[tag]
	livenessRegistryMu.RUnlock()
	if s != nil {
		s.MarkMessage(now)
	}
}

// runLivenessWatchdog starts a goroutine that scans the registry every
// `interval` and logs a warning for any source that has been silent while
// connected for more than `threshold`. Returns a stop function that halts
// the ticker AND signals the goroutine to exit (time.Ticker.Stop does NOT
// close the channel, so a naive `for range t.C` would leak). interval
// should be a fraction of threshold (e.g. threshold/5) so detection
// latency is bounded.
func runLivenessWatchdog(interval, threshold time.Duration) (stop func()) {
	t := time.NewTicker(interval)
	done := make(chan struct{})
	go runLivenessWatchdogLoop(t.C, done, threshold, log.Print)
	return func() {
		t.Stop()
		close(done)
	}
}

// runLivenessWatchdogLoop is the goroutine body, extracted so tests can
// drive it with a synthetic tick channel and capture log output without
// racing on the real ticker.
//
// Edge-triggered (PR #1216 r1 item 3):
//   - quiet → stalled / never-received: emit WARN once, record LastAlertUnix
//   - still stalled, < heartbeat interval since last alert: suppress
//   - still stalled, ≥ heartbeat interval since last alert: emit reminder,
//     refresh LastAlertUnix
//   - stalled → flowing: emit recovery INFO once, clear LastAlertUnix
//
// Without this, the original loop re-emitted the same WARN on every 60s
// tick (60 alerts/hr/source) — the kind of log flood that trains ops to
// mute alerts and miss the next real outage.
func runLivenessWatchdogLoop(tick <-chan time.Time, done <-chan struct{}, threshold time.Duration, emit func(...any)) {
	for {
		select {
		case <-done:
			return
		case now, ok := <-tick:
			if !ok {
				return
			}
			livenessRegistryMu.RLock()
			states := make([]*SourceLivenessState, 0, len(livenessRegistry))
			for _, s := range livenessRegistry {
				states = append(states, s)
			}
			livenessRegistryMu.RUnlock()
			for _, s := range states {
				msg, kind := checkSourceLiveness(s, threshold, now)
				processLivenessTransition(s, kind, msg, now, emit)
			}
		}
	}
}

// processLivenessTransition applies the edge-trigger rules and updates
// LastAlertUnix accordingly. Separated for testability and to keep the
// loop body small.
func processLivenessTransition(s *SourceLivenessState, kind LivenessKind, msg string, now time.Time, emit func(...any)) {
	lastAlert := atomic.LoadInt64(&s.LastAlertUnix)
	switch kind {
	case LivenessStalled, LivenessNeverReceived:
		if lastAlert == 0 {
			// First detection — fire WARN edge.
			emit(msg)
			atomic.StoreInt64(&s.LastAlertUnix, now.Unix())
			return
		}
		// Already alerted; only re-emit on heartbeat interval to avoid log flood.
		if now.Sub(time.Unix(lastAlert, 0)) >= livenessHeartbeatInterval {
			emit(fmt.Sprintf("MQTT [%s] WATCHDOG heartbeat: still stalled — %s", s.Tag, msg))
			atomic.StoreInt64(&s.LastAlertUnix, now.Unix())
		}
	case LivenessOK:
		if lastAlert != 0 {
			// Recovered: emit INFO once, clear the cooldown.
			emit(fmt.Sprintf("MQTT [%s] WATCHDOG INFO: messages flowing again (recovered)", s.Tag))
			atomic.StoreInt64(&s.LastAlertUnix, 0)
		}
	}
}
