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
	// LivenessDisconnected (PR #1216 r2 item 1): paho reports !IsConnected.
	// Distinct from LivenessOK so processLivenessTransition does NOT
	// interpret a TCP drop as recovery and fire a spurious "messages
	// flowing again" INFO when the source actually went from silently
	// broken to overtly broken. paho's own reconnect logging already
	// covers the disconnect — this kind exists solely to keep the
	// transition engine from mis-classifying it.
	LivenessDisconnected
)

// SourceLivenessState tracks per-source last-message timestamp and connection
// state for the stall watchdog (#1212). LastMessageUnix is updated by the
// message handler via atomic store; the watchdog reads it via atomic load.
//
// PR #1216 r1 added:
//   - StartedAt: re-stamped on reconnect to suppress transient-stall WARNs
//     during paho's reconnect window.
//   - LastAlertUnix: edge-trigger cooldown; prevents 60-per-hour re-emits
//     of the same WARN.
//
// PR #1216 r2 added:
//   - FirstConnectedAt: stamped ONCE at registration, never reset. The
//     cold-start "NEVER received" alarm uses this so a broker that flaps
//     in CONNECT → SUBSCRIBE-deny cannot indefinitely re-arm the grace
//     window. r1's StartedAt-as-grace-clock conflated transient-stall
//     suppression with cold-start grace; r2 separates them.
type SourceLivenessState struct {
	Tag    string
	Broker string
	LastMessageUnix int64 // atomic; unix seconds of last successfully received MQTT message
	// FirstConnectedAt (PR #1216 r2 item 2) is stamped ONCE at
	// registerLivenessState time and never reset. Cold-start grace
	// checks against this so a flapping broker (CONNECT ok, SUBSCRIBE
	// ACL-denied — the #1212 shape) can no longer suppress the
	// "NEVER received" alarm by re-stamping StartedAt on every reconnect.
	FirstConnectedAt int64 // atomic; unix seconds of first registration
	StartedAt        int64 // atomic; unix seconds when the source was registered / last reconnected (transient-stall tracking)
	LastAlertUnix    int64 // atomic; unix seconds of last emit (WARN or heartbeat); 0 means quiet
	IsConnectedFn    func() bool
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
// StartedAt (transient-stall window restarts), and clears LastAlertUnix
// (edge-trigger re-arms).
//
// PR #1216 r2 item 2: FirstConnectedAt is INTENTIONALLY not touched here.
// Under broker flap (CONNECT ok, SUBSCRIBE ACL-denied — exact #1212
// class) r1 reset StartedAt on every reconnect, indefinitely re-arming
// the cold-start grace and silencing the headline "NEVER received"
// alarm. Cold-start grace now reads FirstConnectedAt instead, so the
// alarm fires after the FIRST grace window regardless of reconnect
// churn.
func (s *SourceLivenessState) MarkReconnected(now time.Time) {
	atomic.StoreInt64(&s.LastMessageUnix, 0)
	atomic.StoreInt64(&s.StartedAt, now.Unix())
	atomic.StoreInt64(&s.LastAlertUnix, 0)
}

// checkSourceLiveness returns (message, kind) describing the source's
// liveness state. kind==LivenessOK means quiet/healthy; kind==
// LivenessDisconnected means paho is not connected (silent state — no
// emit, no recovery). Any other kind indicates the caller may want to
// emit (subject to edge-trigger).
//
// Cold-start (PR #1216 r1 item 1, r2 item 2): when LastMessageUnix==0,
// the source has never published a single message. If FirstConnectedAt
// was stamped at registration and more than `threshold` has elapsed,
// this is the #1212 failure class — wrong channel hash, ACL drops
// SUBSCRIBE, half-open TCP after CONNECT, or a broker that loops
// CONNECT-then-disconnect. We emit a DISTINCT "NEVER received" alarm
// so operators can grep for it independently of generic stalls. Using
// FirstConnectedAt (not the reconnect-reset StartedAt) ensures broker
// flap cannot silence this alarm.
func checkSourceLiveness(s *SourceLivenessState, threshold time.Duration, now time.Time) (string, LivenessKind) {
	if s == nil || s.IsConnectedFn == nil {
		return "", LivenessOK
	}
	if !s.IsConnectedFn() {
		// paho's reconnect handler covers the disconnected case. Return
		// a DISTINCT kind so the transition engine does not mis-classify
		// disconnect as recovery (PR #1216 r2 item 1).
		return "", LivenessDisconnected
	}
	last := atomic.LoadInt64(&s.LastMessageUnix)
	if last == 0 {
		firstConnected := atomic.LoadInt64(&s.FirstConnectedAt)
		if firstConnected == 0 {
			// Registration didn't stamp FirstConnectedAt — conservative: stay quiet.
			return "", LivenessOK
		}
		sinceFirst := now.Sub(time.Unix(firstConnected, 0))
		if sinceFirst < threshold {
			return "", LivenessOK
		}
		msg := fmt.Sprintf("MQTT [%s] WATCHDOG: client reports connected to %s but has NEVER received a message in %s (threshold %s) — check channel hash / subscribe ACL / half-open TCP",
			s.Tag, s.Broker, sinceFirst.Round(time.Second), threshold)
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
// Also stamps StartedAt (transient-stall window) and FirstConnectedAt
// (cold-start grace anchor — never reset; see r2 item 2 in
// MarkReconnected) so the cold-start watchdog has its clocks.
func registerLivenessState(s *SourceLivenessState) error {
	livenessRegistryMu.Lock()
	defer livenessRegistryMu.Unlock()
	if existing, ok := livenessRegistry[s.Tag]; ok {
		return fmt.Errorf("liveness registry: duplicate tag %q (existing broker=%s, new broker=%s) — fix config so each MQTT source has a unique Name", s.Tag, existing.Broker, s.Broker)
	}
	nowUnix := time.Now().Unix()
	if atomic.LoadInt64(&s.StartedAt) == 0 {
		atomic.StoreInt64(&s.StartedAt, nowUnix)
	}
	if atomic.LoadInt64(&s.FirstConnectedAt) == 0 {
		atomic.StoreInt64(&s.FirstConnectedAt, nowUnix)
	}
	livenessRegistry[s.Tag] = s
	return nil
}

// registerLivenessOrSkip (PR #1216 r2 item 3) is the main-callsite wrapper
// that replaces the previous log.Fatalf on tag collision. Fatal at
// startup over a config typo would kill the entire ingestor and recreate
// the #1212 total-ingest-stop class this PR exists to prevent. On
// collision we log ERROR + skip — the MQTT source still attempts to
// connect, it just won't be tracked by the liveness watchdog. Returns
// true iff the source was registered.
func registerLivenessOrSkip(s *SourceLivenessState) bool {
	if err := registerLivenessState(s); err != nil {
		log.Printf("[ingestor] ERROR: source tag collision %q — skipping duplicate liveness registration, this source will connect but will not be tracked by the watchdog (%v)", s.Tag, err)
		return false
	}
	return true
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
	case LivenessDisconnected:
		// PR #1216 r2 item 1: disconnect is NOT recovery. Stay completely
		// silent — paho's reconnect handler already logs the drop — and
		// preserve LastAlertUnix so the WARN edge can re-fire if/when
		// the source comes back stalled. Clearing the cooldown here
		// would mean a flapping source spams the WARN every cycle.
	}
}

