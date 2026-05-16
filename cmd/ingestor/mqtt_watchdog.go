package main

import (
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// SourceLivenessState tracks per-source last-message timestamp and connection
// state for the stall watchdog (#1212). LastMessageUnix is updated by the
// message handler via atomic store; the watchdog reads it via atomic load.
type SourceLivenessState struct {
	Tag             string
	Broker          string
	LastMessageUnix int64 // atomic; unix seconds of last successfully received MQTT message
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

// checkSourceLiveness returns (message, stalled). stalled=true means the
// client reports connected but no MQTT message has arrived for at least
// `threshold`. When stalled=true, callers should log a warning so operators
// see "silently dead" sockets (half-open TCP, broker accepted CONNECT but
// stopped publishing). When the client is disconnected, this returns
// stalled=false — paho's reconnect logging covers that case.
func checkSourceLiveness(s *SourceLivenessState, threshold time.Duration, now time.Time) (string, bool) {
	if s == nil || s.IsConnectedFn == nil {
		return "", false
	}
	if !s.IsConnectedFn() {
		// paho's reconnect handler covers the disconnected case.
		return "", false
	}
	last := atomic.LoadInt64(&s.LastMessageUnix)
	if last == 0 {
		// Never received a message; treat as stalled only after the threshold
		// has elapsed since the watchdog process started (we can't
		// distinguish startup from silence here without an extra timestamp,
		// so be conservative and skip).
		return "", false
	}
	silentFor := now.Sub(time.Unix(last, 0))
	if silentFor < threshold {
		return "", false
	}
	msg := fmt.Sprintf("MQTT [%s] WATCHDOG: client reports connected to %s but no messages received for %s (threshold %s) — possible half-open socket or upstream stall",
		s.Tag, s.Broker, silentFor.Round(time.Second), threshold)
	return msg, true
}

// livenessRegistry is a package-level lookup so handleMessage (called with
// only `tag string`) can mark liveness without threading the state through
// every call site. Reads dominate (per message); writes happen once per
// source at startup.
var (
	livenessRegistry   = map[string]*SourceLivenessState{}
	livenessRegistryMu sync.RWMutex
)

// registerLivenessState publishes a state to the registry by tag. Called
// once per source in main() after the client is constructed.
func registerLivenessState(s *SourceLivenessState) {
	livenessRegistryMu.Lock()
	livenessRegistry[s.Tag] = s
	livenessRegistryMu.Unlock()
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
// connected for more than `threshold`. Returns the *time.Ticker so callers
// can stop it on shutdown. interval should be a fraction of threshold (e.g.
// threshold/5) so the latency of detection is bounded.
func runLivenessWatchdog(interval, threshold time.Duration) *time.Ticker {
	t := time.NewTicker(interval)
	go func() {
		for now := range t.C {
			livenessRegistryMu.RLock()
			states := make([]*SourceLivenessState, 0, len(livenessRegistry))
			for _, s := range livenessRegistry {
				states = append(states, s)
			}
			livenessRegistryMu.RUnlock()
			for _, s := range states {
				if msg, stalled := checkSourceLiveness(s, threshold, now); stalled {
					log.Print(msg)
				}
			}
		}
	}()
	return t
}
