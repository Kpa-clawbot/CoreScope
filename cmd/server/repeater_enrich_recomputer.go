package main

import "time"

// StartRepeaterEnrichmentRecomputer is the steady-state background
// recompute loop for the repeater enrichment bulk caches consumed by
// handleNodes (GetRepeaterRelayInfoMap + GetRepeaterUsefulnessScoreMap).
//
// Why this exists (issue #1262): PR #1260 added a 15s-TTL bulk cache,
// but the rebuild itself runs on the request-serving goroutine on the
// first request after startup or after the TTL expires. On staging
// (75k tx, 600 nodes) that cold rebuild took 15.7s and was triggered
// by every cold SPA load via live.js's /api/nodes?limit=2000 call.
//
// Calling Start does an initial synchronous compute (so the next
// request hits cache) and then ticks every `interval` to keep the
// snapshot fresh — same pattern as analytics_recomputer.go (#1240).
//
// Returns a stop closure that signals the goroutine and waits for it.
//
// NOTE: this is a NO-OP STUB. The accompanying perf test
// (TestHandleNodesLimit2000ColdMiss) deliberately exercises the
// cold-rebuild path on /api/nodes?limit=2000 and fails on master so
// the failure is assertion-shaped, not a missing-symbol compile error.
// The GREEN commit replaces this stub with a real prewarm + ticker.
func (s *PacketStore) StartRepeaterEnrichmentRecomputer(windowHours float64, interval time.Duration) func() {
	_ = windowHours
	_ = interval
	return func() {}
}
