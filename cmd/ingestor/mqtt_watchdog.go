package main

import (
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
	// STUB: returns no-stall so RED tests fail on assertion (not compile).
	_ = atomic.LoadInt64(&s.LastMessageUnix)
	return "", false
}
