package main

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
)

// ResolvePrefixResp is the tiny reply for /api/nodes/resolve — lets a client
// resolve a heard 2-3 byte path prefix (or full pubkey) to a node name without
// fetching the whole node list. Read-only.
type ResolvePrefixResp struct {
	Prefix    string `json:"prefix"`
	Pubkey    string `json:"pubkey,omitempty"`
	Name      string `json:"name,omitempty"`
	Ambiguous bool   `json:"ambiguous"`
}

var hexPrefixRe = regexp.MustCompile(`^[0-9a-f]{2,64}$`)

// minResolvePrefixHex is the shortest accepted prefix. 1-byte (2 hex) keys are
// never stored — the ingestor rejects heard keys shorter than 2 bytes — so the
// floor matches the data model and, by ruling out the 256 two-char prefixes,
// blunts trivial enumeration of every node name through this endpoint (#15).
const minResolvePrefixHex = 4

func (s *Server) handleResolvePrefix(w http.ResponseWriter, r *http.Request) {
	pfx := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("prefix")))
	if !hexPrefixRe.MatchString(pfx) {
		http.Error(w, "prefix must be hex", http.StatusBadRequest)
		return
	}
	if len(pfx) < minResolvePrefixHex {
		http.Error(w, "prefix must be at least 4 hex chars", http.StatusBadRequest)
		return
	}
	if s.db == nil || s.db.conn == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	// lookupPrefixes counts candidates in nodes and in client_node_names (a
	// repeater's own reported name), so a node known only from that table
	// resolves too, and one that collides across the two stays ambiguous.
	h, ok := s.lookupPrefixes([]string{pfx})[pfx]
	if !ok {
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	resp := ResolvePrefixResp{Prefix: pfx}
	switch {
	case h.matches == 1:
		// Parity with /api/nodes/search and /api/resolve-hops: never reveal the
		// identity of a blacklisted or hidden-prefix node (#1181). Report it as
		// not-found rather than leaking the name the rest of the API hides.
		if s.visible(h) {
			resp.Pubkey = h.pubkey
			resp.Name = h.name
		}
	default:
		resp.Ambiguous = h.matches > 1 // 0 → not found (name empty), >1 → ambiguous
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
