package main

import (
	"testing"
	"time"
)

// setupWatchdogTestLoop spawns runLivenessWatchdogLoop with a panic
// safety net so an unrecovered panic in the loop (the historical bug
// shape #1749 / #1810 round-1 gates against) does NOT crash the test
// binary. Returns:
//   - tick: send fabricated timestamps to drive the loop
//   - done: caller closes to ask the loop to exit cleanly
//   - exited: closed by the helper after the loop returns (whether by
//     done-signal, normal channel close, or panic propagation)
//
// Extracted as part of #1810 round-1 (adv #4) — four tests in this
// package previously open-coded the same scaffolding with subtle
// variations.
func setupWatchdogTestLoop(t *testing.T, threshold time.Duration, emit func(...any)) (tick chan time.Time, done chan struct{}, exited chan struct{}) {
	t.Helper()
	tick = make(chan time.Time)
	done = make(chan struct{})
	exited = make(chan struct{})
	go func() {
		defer func() {
			_ = recover() // safety net; the production loop is what we are asserting on
			close(exited)
		}()
		runLivenessWatchdogLoop(tick, done, threshold, emit)
	}()
	return tick, done, exited
}

// sendTickOrFail pushes one fabricated timestamp into the loop's tick
// channel and fails the test if the loop does not consume it within
// timeout. Use this everywhere a test wants to step the watchdog by
// exactly one tick — open-coded select-default-Fatal patterns are
// repetitive and easy to get wrong (off-by-one timeouts).
func sendTickOrFail(t *testing.T, tick chan<- time.Time, stamp time.Time, timeout time.Duration, label string) {
	t.Helper()
	select {
	case tick <- stamp:
	case <-time.After(timeout):
		t.Fatalf("%s: tick blocked after %s — loop dead?", label, timeout)
	}
}
