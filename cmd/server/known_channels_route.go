package main

import (
	"net/http"
	"time"
)

// handleKnownChannels — GET /api/known-channels?region=XX
//
// Returns the cached community catalogue of hashtag channels (issue #1323),
// optionally filtered to one region (ISO 3166-1 alpha-2, case-insensitive).
// Empty/missing cache returns 200 with an empty Entries list so the UI
// degrades gracefully (fail-soft). Never blocks on the upstream fetch:
// the response is served straight off an atomic snapshot pointer.
func (s *Server) handleKnownChannels(w http.ResponseWriter, r *http.Request) {
	region := r.URL.Query().Get("region")
	var snap *KnownChannelsSnapshot
	if s.knownChannels != nil {
		snap = s.knownChannels.load()
	}
	if snap == nil {
		// Empty cache — return a well-formed empty snapshot. Short
		// max-age so a slow first fetch (or disabled feature) doesn't
		// freeze the UI for the whole page lifetime.
		w.Header().Set("Cache-Control", "public, max-age=30")
		writeJSON(w, &KnownChannelsSnapshot{
			FetchedAt: time.Time{},
			Source:    "",
			Entries:   []KnownChannelEntry{},
		})
		return
	}
	// Catalogue refreshes every 24h upstream; 5 min browser cache is
	// well under that and avoids hammering the endpoint when the UI
	// re-renders the sidebar.
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, filterSnapshotByRegion(snap, region))
}
