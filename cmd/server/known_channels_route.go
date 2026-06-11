package main

import (
	"net/http"
	"time"
)

// handleKnownChannels — GET /api/known-channels?region=XX
// Returns the cached community catalogue, optionally filtered to one region
// (ISO 3166-1 alpha-2, case-insensitive). Empty/missing snapshot returns
// 200 with an empty Entries list — fail-soft for the UI. Issue #1323.
//
// STUB — full implementation lands with the green commit.
func (s *Server) handleKnownChannels(w http.ResponseWriter, r *http.Request) {
	// Stub: always return an empty snapshot (no region filtering, no cache
	// read). Tests asserting filtered output / non-empty payload fail here.
	writeJSON(w, &KnownChannelsSnapshot{
		FetchedAt: time.Time{},
		Source:    "",
		Entries:   []KnownChannelEntry{},
	})
}
