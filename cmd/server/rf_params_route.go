package main

import (
	"net/http"
	"strings"
	"time"
)

// handleRfParams — GET /api/rf-params (issue #1268).
//
// Returns the radio settings the MeshCore map holds for the nodes this
// instance knows, limited to adverts within maxAgeDays and not dated in the
// future (rfAdvertFresh), plus the named presets and when the map was last
// fetched. Served straight from the atomic snapshot; never waits on the
// upstream fetch.
func (s *Server) handleRfParams(w http.ResponseWriter, r *http.Request) {
	resp := RfParamsResponse{Presets: []RfPreset{}, Nodes: map[string]RfParams{}}
	if s.rfParams == nil {
		writeJSON(w, resp)
		return
	}
	resp.Enabled = true
	snap := s.rfParams.load()
	if snap == nil {
		w.Header().Set("Cache-Control", "public, max-age=30")
		writeJSON(w, resp)
		return
	}
	fetched := snap.FetchedAt
	resp.FetchedAt = &fetched
	resp.Source = snap.Source
	if snap.Presets != nil {
		resp.Presets = snap.Presets
	}
	now := time.Now().UTC()
	maxAge := s.rfParams.cfg.maxAge()
	nodes, _ := s.store.getCachedNodesAndPM()
	for _, n := range nodes {
		pk := strings.ToLower(n.PublicKey)
		p, ok := snap.Nodes[pk]
		if !ok {
			continue
		}
		if at, err := time.Parse(time.RFC3339, p.LastAdvert); err != nil || !rfAdvertFresh(at, now, maxAge) {
			continue
		}
		resp.Nodes[pk] = p
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, resp)
}
