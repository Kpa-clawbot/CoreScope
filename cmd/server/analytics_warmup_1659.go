// Package main: issue #1659 stubs for the analytics warmup gate.
//
// The RED-commit stub: methods exist so the test file compiles and the
// tests fail on ASSERTIONS (not on missing-symbol build errors).
// The GREEN commit replaces this file's behavior with the real gate.
package main

import "time"

// FirstPassDoneAt_1659 reports when the recomputer first completed a
// full-range pass. Zero time means the recomputer has not yet finished
// its first pass — handlers must return 503 + Retry-After: 5 in that
// case for the default-shape request.
//
// RED stub: always returns zero time.
func (r *analyticsRecomputer) FirstPassDoneAt_1659() time.Time {
	return time.Time{}
}

// installWarmupBlocker_1659 wires the analytics recomputers with a
// compute func that blocks on the given channel until the channel
// closes. Used by tests to hold the recomputer in the pre-first-pass
// state while exercising the warmup gate.
//
// RED stub: no-op. Tests using this will see the live (unblocked)
// recomputer path, which yields 200 — failing the 503 assertion.
func (s *PacketStore) installWarmupBlocker_1659(block <-chan struct{}) {
	_ = block
}
