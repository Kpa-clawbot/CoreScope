package main

import "time"

// Limits on the unauthenticated /neighbors report (#1865). Any MQTT publisher
// can send one, so the values it carries are bounded before they reach the
// nodes table.
const (
	// maxConfiguredScopeLen caps the normalised comma-separated scope list.
	// Real repeaters carry a handful of short region names.
	maxConfiguredScopeLen = 256
	// maxReportFuture is how far ahead of our clock a report may be stamped
	// and still be accepted. Allows ordinary clock skew, rejects a timestamp
	// chosen to win last-write-wins forever.
	maxReportFuture = 5 * time.Minute
)

// reportNow is swapped in tests.
var reportNow = time.Now

// reportTooFarInFuture reports whether a canonical RFC3339 timestamp (the
// output of normalizeReportTS) is more than maxReportFuture ahead of now.
// Empty or unparseable input is not "in the future".
func reportTooFarInFuture(canonical string) bool {
	if canonical == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, canonical)
	if err != nil {
		return false
	}
	return t.After(reportNow().Add(maxReportFuture))
}
