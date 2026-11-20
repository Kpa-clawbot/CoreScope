package main

import (
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/meshcore-analyzer/users"
)

// myRepeaterJSON is one repeater the caller claimed, for My account, My
// repeaters: enough to spot one that went quiet without opening each node.
type myRepeaterJSON struct {
	Pubkey string `json:"pubkey"`
	Name   string `json:"name"`  // "" when unnamed or in neither nodes nor inactive_nodes
	Known  bool   `json:"known"` // false when the node is in neither nodes nor inactive_nodes
	// Inactive: retention moved the node to inactive_nodes (silent longer
	// than retention.nodeDays); name, role and lastHeard come from there.
	Inactive bool   `json:"inactive"`
	Role     string `json:"role"` // as stored, lowercase; "" when not known
	// ClaimedAt is the caller's own claim.
	ClaimedAt string `json:"claimedAt"`
	// LastHeard is the newer of the node's last advert and the last
	// transmission certainly from it (the node page's Last Heard); null when
	// neither is known.
	LastHeard *string `json:"lastHeard"`
	// LastRelayed is the last time its hop prefix was seen relaying. The
	// prefix can be shared with other repeaters, so it only marks a silent
	// repeater as uncertain, never as heard.
	LastRelayed *string `json:"lastRelayed"`
	// Status is the saved repeater status (active, maintenance,
	// decommissioned); null when none is saved.
	Status *string `json:"status"`
	// Comaintainers are the other active claimers, by display name.
	Comaintainers []string `json:"comaintainers"`
}

type myRepeatersJSON struct {
	Repeaters []myRepeaterJSON `json:"repeaters"`
}

// handleMyRepeaters lists the repeaters the caller claimed, by name (then
// pubkey). A node retention moved to inactive_nodes is read from there and
// marked inactive; a claim of a node in neither table is kept as known: false.
func (s *Server) handleMyRepeaters(w http.ResponseWriter, _ *http.Request, u *users.User, _ *users.Session) {
	claims, err := s.auth.st.RepeaterClaimsByUser(u.ID)
	if err != nil {
		log.Printf("[users] my repeaters %d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := myRepeatersJSON{Repeaters: make([]myRepeaterJSON, 0, len(claims))}
	if len(claims) == 0 {
		writeJSON(w, out)
		return
	}
	pks := make([]string, len(claims))
	for i, c := range claims {
		pks[i] = c.Pubkey
	}
	nodes := map[string]notifyNode{}
	inactive := map[string]notifyNode{}
	if s.db != nil {
		if nodes, err = s.db.NotifyNodes(pks, false); err != nil {
			log.Printf("[users] my repeaters %d: nodes: %v", u.ID, err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		var missing []string
		for _, pk := range pks {
			if _, ok := nodes[pk]; !ok {
				missing = append(missing, pk)
			}
		}
		if len(missing) > 0 {
			if inactive, err = s.db.InactiveNodeBasics(missing); err != nil {
				log.Printf("[users] my repeaters %d: inactive nodes: %v", u.ID, err)
				writeError(w, http.StatusInternalServerError, "internal error")
				return
			}
		}
	}
	infos, err := s.auth.st.RepeaterInfoOfUserRepeaters(u.ID)
	if err != nil {
		log.Printf("[users] my repeaters %d: info: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	claimers, err := s.auth.st.RepeaterClaimsOfUserRepeaters(u.ID)
	if err != nil {
		log.Printf("[users] my repeaters %d: claims: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	heard := map[string]time.Time{}
	var relay map[string]RepeaterRelayInfo
	if s.store != nil {
		heard, _ = s.store.LastHeardMap(pks)
		relay = s.store.GetRepeaterRelayInfoMap(s.cfg.GetHealthThresholds().RelayActiveHours)
	}
	for _, c := range claims {
		nd, known := nodes[c.Pubkey]
		gone := false
		if !known {
			nd, gone = inactive[c.Pubkey]
			known = gone
		}
		r := myRepeaterJSON{Pubkey: c.Pubkey, Name: nd.Name, Known: known, Inactive: gone, Role: strings.ToLower(nd.Role),
			ClaimedAt: rfc3339(c.ClaimedAt), Comaintainers: []string{}}
		last, _ := parseRelayTS(nd.LastSeen)
		if t, ok := heard[c.Pubkey]; ok && t.After(last) {
			last = t
		}
		if !last.IsZero() {
			ts := last.UTC().Format(time.RFC3339)
			r.LastHeard = &ts
		}
		if info, ok := lookupRelayInfo(relay, c.Pubkey); ok {
			r.LastRelayed = stringPtrOrNil(info.LastRelayed)
		}
		if info, ok := infos[c.Pubkey]; ok {
			r.Status = stringPtrOrNil(info.Status)
		}
		for _, o := range claimers[c.Pubkey] {
			if o.UserID != u.ID {
				r.Comaintainers = append(r.Comaintainers, o.DisplayName)
			}
		}
		out.Repeaters = append(out.Repeaters, r)
	}
	sort.SliceStable(out.Repeaters, func(i, j int) bool {
		a, b := out.Repeaters[i], out.Repeaters[j]
		if an, bn := strings.ToLower(a.Name), strings.ToLower(b.Name); an != bn {
			// Named repeaters first, then the unnamed by pubkey.
			if an == "" || bn == "" {
				return bn == ""
			}
			return an < bn
		}
		return a.Pubkey < b.Pubkey
	})
	writeJSON(w, out)
}
