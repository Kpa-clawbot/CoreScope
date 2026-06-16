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
	// LIMIT 2: we only need to know unique vs ambiguous. nodes.public_key is the
	// PK and stored lowercase; pfx is validated hex so the LIKE pattern is safe.
	rows, err := s.db.conn.Query(`SELECT public_key, COALESCE(name,'') FROM nodes WHERE public_key LIKE ? LIMIT 2`, pfx+"%")
	if err != nil {
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var pks, names []string
	for rows.Next() {
		var pk, nm string
		if err := rows.Scan(&pk, &nm); err != nil {
			http.Error(w, "scan failed", http.StatusInternalServerError)
			return
		}
		pks = append(pks, pk)
		names = append(names, nm)
	}

	resp := ResolvePrefixResp{Prefix: pfx}
	switch len(pks) {
	case 1:
		// Parity with /api/nodes/search and /api/resolve-hops: never reveal the
		// identity of a blacklisted or hidden-prefix node (#1181). Report it as
		// not-found rather than leaking the name the rest of the API hides.
		if !s.cfg.IsBlacklisted(pks[0]) && !s.cfg.IsNameHidden(names[0]) {
			resp.Pubkey = pks[0]
			resp.Name = names[0]
		}
	default:
		resp.Ambiguous = len(pks) > 1 // 0 → not found (name empty), >1 → ambiguous
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
