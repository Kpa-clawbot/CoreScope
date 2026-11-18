package main

import (
	"errors"
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
	Name   string `json:"name"`  // "" when unnamed or no longer in the analyzer database
	Known  bool   `json:"known"` // false once the node left the analyzer database
	Role   string `json:"role"`  // as stored, lowercase; "" when not known
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
// pubkey). Claims of nodes that left the analyzer database are kept and
// marked known: false.
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
	if s.db != nil {
		if nodes, err = s.db.NotifyNodes(pks, false); err != nil {
			log.Printf("[users] my repeaters %d: nodes: %v", u.ID, err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
	}
	heard := map[string]time.Time{}
	var relay map[string]RepeaterRelayInfo
	if s.store != nil {
		heard, _ = s.store.LastHeardMap(pks)
		relay = s.store.GetRepeaterRelayInfoMap(s.cfg.GetHealthThresholds().RelayActiveHours)
	}
	for _, c := range claims {
		nd, known := nodes[c.Pubkey]
		r := myRepeaterJSON{Pubkey: c.Pubkey, Name: nd.Name, Known: known, Role: strings.ToLower(nd.Role),
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
		info, err := s.auth.st.GetRepeaterInfo(c.Pubkey)
		if err == nil {
			r.Status = stringPtrOrNil(info.Status)
		} else if !errors.Is(err, users.ErrNotFound) {
			log.Printf("[users] my repeaters %d: info %s: %v", u.ID, c.Pubkey, err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		others, err := s.auth.st.RepeaterClaims(c.Pubkey)
		if err != nil {
			log.Printf("[users] my repeaters %d: claims %s: %v", u.ID, c.Pubkey, err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		for _, o := range others {
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
