package main

import (
	"errors"
	"net/http"
	"sort"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/users"
)

type repeaterClaimerJSON struct {
	DisplayName string `json:"displayName"`
	ClaimedAt   string `json:"claimedAt"`
}

type repeaterInfoJSON struct {
	Pubkey      string                `json:"pubkey"`
	HeightM     *float64              `json:"heightM"`
	GainDBi     float64               `json:"gainDbi"`
	Status      *string               `json:"status"`
	Power       *string               `json:"power"`
	Notes       string                `json:"notes"`
	Claimers    []repeaterClaimerJSON `json:"claimers"`
	UpdatedAt   *string               `json:"updatedAt"`
	UpdatedBy   *string               `json:"updatedBy"`
	CanEdit     *bool                 `json:"canEdit,omitempty"`
	ClaimedByMe *bool                 `json:"claimedByMe,omitempty"`
}

type repeaterInfoListJSON struct {
	Repeaters []repeaterInfoJSON `json:"repeaters"`
}

func (s *Server) registerRepeaterRoutes(r *mux.Router) {
	r.HandleFunc("/api/repeaters/info", s.handleRepeaterInfoList).Methods("GET")
	r.HandleFunc("/api/repeaters/{pubkey}/info", s.handleRepeaterInfoGet).Methods("GET")
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func claimersJSON(claims []users.RepeaterClaim) []repeaterClaimerJSON {
	out := make([]repeaterClaimerJSON, 0, len(claims))
	for _, c := range claims {
		out = append(out, repeaterClaimerJSON{DisplayName: c.DisplayName, ClaimedAt: rfc3339(c.ClaimedAt)})
	}
	return out
}

// repeaterInfoBase fills the stored fields; info may be nil (no row).
func repeaterInfoBase(pk string, info *users.RepeaterInfo, claims []users.RepeaterClaim) repeaterInfoJSON {
	out := repeaterInfoJSON{Pubkey: pk, Claimers: claimersJSON(claims)}
	if info == nil {
		return out
	}
	out.HeightM = info.HeightM
	out.GainDBi = info.GainDBi
	out.Status = strPtr(info.Status)
	out.Power = strPtr(info.Power)
	out.Notes = info.Notes
	t := rfc3339(info.UpdatedAt)
	out.UpdatedAt = &t
	if info.UpdatedBy != nil {
		out.UpdatedBy = strPtr(info.UpdatedByName)
	}
	return out
}

// repeaterInfoFor builds one entry. CanEdit and ClaimedByMe are set only when
// u is non-nil.
func (s *Server) repeaterInfoFor(pk string, u *users.User) (repeaterInfoJSON, error) {
	info, err := s.auth.st.GetRepeaterInfo(pk)
	if errors.Is(err, users.ErrNotFound) {
		info = nil
	} else if err != nil {
		return repeaterInfoJSON{}, err
	}
	claims, err := s.auth.st.RepeaterClaims(pk)
	if err != nil {
		return repeaterInfoJSON{}, err
	}
	out := repeaterInfoBase(pk, info, claims)
	if u != nil {
		can, err := s.auth.st.CanEditRepeater(u, pk)
		if err != nil {
			return repeaterInfoJSON{}, err
		}
		mine := false
		for _, c := range claims {
			if c.UserID == u.ID {
				mine = true
				break
			}
		}
		out.CanEdit, out.ClaimedByMe = &can, &mine
	}
	return out, nil
}

func (s *Server) handleRepeaterInfoGet(w http.ResponseWriter, r *http.Request) {
	pk, err := users.NormalizePubkey(mux.Vars(r)["pubkey"])
	if err != nil {
		writeError(w, http.StatusBadRequest, "pubkey must be 64 hex characters")
		return
	}
	u, _ := s.auth.currentUser(w, r)
	out, err := s.repeaterInfoFor(pk, u)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, out)
}

func (s *Server) handleRepeaterInfoList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.auth.st.ListRepeaterInfo()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	claims, err := s.auth.st.AllRepeaterClaims()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]repeaterInfoJSON, 0, len(rows)+len(claims))
	seen := make(map[string]bool, len(rows))
	for i := range rows {
		seen[rows[i].Pubkey] = true
		out = append(out, repeaterInfoBase(rows[i].Pubkey, &rows[i], claims[rows[i].Pubkey]))
	}
	for pk, c := range claims {
		if !seen[pk] {
			out = append(out, repeaterInfoBase(pk, nil, c))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pubkey < out[j].Pubkey })
	writeJSON(w, repeaterInfoListJSON{Repeaters: out})
}
