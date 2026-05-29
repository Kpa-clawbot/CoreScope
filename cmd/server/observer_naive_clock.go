package main

import "time"

// observerNaiveClockWindow is the rolling window after which a recorded
// naive-clock skew event "decays" and the observer is no longer flagged in
// the UI. Read-time decay (no background sweep) keeps it cheap.
const observerNaiveClockWindow = 24 * time.Hour

// applyObserverNaiveClock populates the four clock_* fields on ObserverResp
// from the underlying Observer row, applying read-time decay: any event
// older than observerNaiveClockWindow is treated as absent so the chip and
// banner clear automatically without a background sweep.
//
// Issue #1478.
func applyObserverNaiveClock(resp *ObserverResp, o *Observer, now time.Time) {
	if o.ClockLastNaiveAt == nil || *o.ClockLastNaiveAt == "" {
		return
	}
	last, err := time.Parse(time.RFC3339, *o.ClockLastNaiveAt)
	if err != nil {
		return
	}
	if now.Sub(last) > observerNaiveClockWindow {
		// Decayed — leave clock_naive=false and counters at zero. We
		// intentionally do NOT clear the underlying row here (server is
		// read-only; the next ingestor write or a future #1478 followup
		// vacuum can rewrite). The response just shows zero/null.
		return
	}
	resp.ClockNaive = true
	if o.ClockSkewSeconds != nil {
		resp.ClockSkewSeconds = *o.ClockSkewSeconds
	}
	resp.ClockSkewCount24h = o.ClockSkewCount24h
	resp.ClockLastNaiveAt = *o.ClockLastNaiveAt
}
