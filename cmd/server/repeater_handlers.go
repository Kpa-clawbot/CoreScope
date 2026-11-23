package main

import (
	"errors"
	"log"
	"net/http"
	"sort"
	"strconv"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/users"
)

type repeaterClaimerJSON struct {
	DisplayName string `json:"displayName"`
	ClaimedAt   string `json:"claimedAt"`
}

// repeaterClaimRefJSON is one claim as seen from the claiming user (admin
// detail, account export).
type repeaterClaimRefJSON struct {
	Pubkey    string `json:"pubkey"`
	ClaimedAt string `json:"claimedAt"`
}

func claimRefsJSON(claims []users.RepeaterClaim) []repeaterClaimRefJSON {
	out := make([]repeaterClaimRefJSON, 0, len(claims))
	for _, c := range claims {
		out = append(out, repeaterClaimRefJSON{Pubkey: c.Pubkey, ClaimedAt: rfc3339(c.ClaimedAt)})
	}
	return out
}

type repeaterInfoJSON struct {
	Pubkey      string                `json:"pubkey"`
	Revision    int64                 `json:"revision"`
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
	r.HandleFunc("/api/repeaters/{pubkey}/info", s.withUser(s.handleRepeaterInfoPut)).Methods("PUT")
	r.HandleFunc("/api/repeaters/{pubkey}/claim", s.withUser(s.handleRepeaterClaim)).Methods("POST")
	r.HandleFunc("/api/repeaters/{pubkey}/claim", s.withUser(s.handleRepeaterUnclaim)).Methods("DELETE")
	r.HandleFunc("/api/account/repeaters", s.withUser(s.handleMyRepeaters)).Methods("GET")
	r.HandleFunc("/api/admin/repeaters/{pubkey}/claims/{userId}", s.withAdmin(s.handleAdminRepeaterUnclaim)).Methods("DELETE")
}

func stringPtrOrNil(s string) *string {
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
	out.Revision = info.Revision
	out.HeightM = info.HeightM
	out.GainDBi = info.GainDBi
	out.Status = stringPtrOrNil(info.Status)
	out.Power = stringPtrOrNil(info.Power)
	out.Notes = info.Notes
	t := rfc3339(info.UpdatedAt)
	out.UpdatedAt = &t
	if info.UpdatedBy != nil {
		out.UpdatedBy = stringPtrOrNil(info.UpdatedByName)
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
	u, _ := s.optionalUser(w, r)
	out, err := s.repeaterInfoFor(pk, u)
	if err != nil {
		log.Printf("[users] repeater info %s: %v", pk, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, out)
}

func (s *Server) handleRepeaterInfoList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.auth.st.ListRepeaterInfo()
	if err != nil {
		log.Printf("[users] list repeater info: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	claims, err := s.auth.st.AllRepeaterClaims()
	if err != nil {
		log.Printf("[users] list repeater claims: %v", err)
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

type repeaterEditRequest struct {
	HeightM *float64 `json:"heightM"`
	GainDBi *float64 `json:"gainDbi"` // nil = 0
	Status  string   `json:"status"`
	Power   string   `json:"power"`
	Notes   string   `json:"notes"`
	// BaseRevision makes the save conditional: it must be the stored
	// revision (0: no details saved yet). nil saves unconditionally, as the
	// browser editor does.
	BaseRevision *int64 `json:"baseRevision"`
}

func fmtFloat(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func fmtHeight(h *float64) string {
	if h == nil {
		return ""
	}
	return fmtFloat(*h)
}

// repeaterEditDiff lists the changed fields as <field>.old / <field>.new
// audit details. prev nil means no row before: the zero values.
func repeaterEditDiff(prev *users.RepeaterInfo, next users.RepeaterEdit) map[string]string {
	var old users.RepeaterInfo
	if prev != nil {
		old = *prev
	}
	out := map[string]string{}
	add := func(field, o, n string) {
		if o != n {
			out[field+".old"], out[field+".new"] = o, n
		}
	}
	add("height_m", fmtHeight(old.HeightM), fmtHeight(next.HeightM))
	add("gain_dbi", fmtFloat(old.GainDBi), fmtFloat(next.GainDBi))
	add("status", old.Status, next.Status)
	add("power", old.Power, next.Power)
	add("notes", old.Notes, next.Notes)
	return out
}

// withVia marks an audit detail written through a device token, so admins can
// tell app writes from browser ones.
func withVia(detail map[string]string, sess *users.Session) map[string]string {
	if sess != nil && sess.Kind == users.SessionKindDevice {
		detail["via"] = "device"
	}
	return detail
}

// repeaterTarget applies the per-user rate limit and normalises the pubkey;
// with needKnown it also requires a known repeater in the analyzer DB. ok
// false means the error answer is already written.
func (s *Server) repeaterTarget(w http.ResponseWriter, r *http.Request, u *users.User, needKnown bool) (string, bool) {
	if !s.auth.allow(w, r, s.auth.repeater, "user:"+strconv.FormatInt(u.ID, 10)) {
		return "", false
	}
	pk, err := users.NormalizePubkey(mux.Vars(r)["pubkey"])
	if err != nil {
		writeError(w, http.StatusBadRequest, "pubkey must be 64 hex characters")
		return "", false
	}
	if !needKnown {
		return pk, true
	}
	found, isRep, err := s.db.RepeaterKnown(pk)
	switch {
	case err != nil:
		log.Printf("[users] repeater lookup %s: %v", pk, err)
		writeError(w, http.StatusInternalServerError, "internal error")
	case !found:
		writeError(w, http.StatusNotFound, "unknown node")
	case !isRep:
		writeError(w, http.StatusBadRequest, "not a repeater")
	default:
		return pk, true
	}
	return "", false
}

func (s *Server) writeRepeaterInfo(w http.ResponseWriter, pk string, u *users.User) {
	out, err := s.repeaterInfoFor(pk, u)
	if err != nil {
		log.Printf("[users] repeater info %s: %v", pk, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, out)
}

func (s *Server) handleRepeaterInfoPut(w http.ResponseWriter, r *http.Request, u *users.User, sess *users.Session) {
	pk, ok := s.repeaterTarget(w, r, u, true)
	if !ok {
		return
	}
	var req repeaterEditRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.BaseRevision != nil && *req.BaseRevision < 0 {
		writeError(w, http.StatusBadRequest, "baseRevision must not be negative")
		return
	}
	can, err := s.auth.st.CanEditRepeater(u, pk)
	if err != nil {
		log.Printf("[users] repeater permission %s: %v", pk, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !can {
		writeError(w, http.StatusForbidden, "only this repeater's maintainers can edit it")
		return
	}
	edit := users.RepeaterEdit{HeightM: req.HeightM, Status: req.Status, Power: req.Power, Notes: req.Notes}
	if req.GainDBi != nil {
		edit.GainDBi = *req.GainDBi
	}
	edit, err = users.ValidateRepeaterEdit(edit)
	if writeValidation(w, err) {
		return
	}
	if err != nil {
		log.Printf("[users] validate repeater edit %s: %v", pk, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	cur, err := s.auth.st.GetRepeaterInfo(pk)
	if errors.Is(err, users.ErrNotFound) {
		cur, err = nil, nil
	}
	if err != nil {
		log.Printf("[users] read repeater info %s: %v", pk, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	var curRev int64
	if cur != nil {
		curRev = cur.Revision
	}
	// A conditional save from a stale base is refused even when it would
	// change nothing: the client's copy is out of date either way.
	if req.BaseRevision != nil && *req.BaseRevision != curRev {
		s.writeRepeaterConflict(w, pk, u)
		return
	}
	// An unchanged body writes nothing: updated_at, updated_by and the
	// revision stay. The early read only decides that; the audit diff comes
	// from the row the save replaced.
	if len(repeaterEditDiff(cur, edit)) > 0 {
		var prev *users.RepeaterInfo
		if req.BaseRevision != nil {
			prev, err = s.auth.st.SaveRepeaterInfoAt(u.ID, pk, edit, *req.BaseRevision)
		} else {
			prev, err = s.auth.st.SaveRepeaterInfo(u.ID, pk, edit)
		}
		if errors.Is(err, users.ErrRepeaterConflict) {
			// Another save landed between the read above and this one.
			s.writeRepeaterConflict(w, pk, u)
			return
		}
		if err != nil {
			if writeValidation(w, err) {
				return
			}
			log.Printf("[users] save repeater info %s: %v", pk, err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		diff := repeaterEditDiff(prev, edit)
		diff["pubkey"] = pk
		s.auth.auditAsync(idPtr(u.ID), "repeater.edit", nil, withVia(diff, sess))
	}
	s.writeRepeaterInfo(w, pk, u)
}

// writeRepeaterConflict answers 409 with the stored details, so the client
// can merge and retry from their revision without another read.
func (s *Server) writeRepeaterConflict(w http.ResponseWriter, pk string, u *users.User) {
	out, err := s.repeaterInfoFor(pk, u)
	if err != nil {
		log.Printf("[users] repeater info %s: %v", pk, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSONStatus(w, http.StatusConflict, out)
}

func (s *Server) handleRepeaterClaim(w http.ResponseWriter, r *http.Request, u *users.User, sess *users.Session) {
	pk, ok := s.repeaterTarget(w, r, u, true)
	if !ok {
		return
	}
	added, err := s.auth.st.ClaimRepeater(u.ID, pk)
	if err != nil {
		log.Printf("[users] claim repeater %s: %v", pk, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if added {
		s.auth.auditAsync(idPtr(u.ID), "repeater.claim", idPtr(u.ID), withVia(map[string]string{"pubkey": pk}, sess))
	}
	s.writeRepeaterInfo(w, pk, u)
}

func (s *Server) handleRepeaterUnclaim(w http.ResponseWriter, r *http.Request, u *users.User, sess *users.Session) {
	pk, ok := s.repeaterTarget(w, r, u, false)
	if !ok {
		return
	}
	err := s.auth.st.UnclaimRepeater(u.ID, pk)
	if errors.Is(err, users.ErrNotFound) {
		writeError(w, http.StatusNotFound, "you have not claimed this repeater")
		return
	}
	if err != nil {
		log.Printf("[users] unclaim repeater %s: %v", pk, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.auth.auditAsync(idPtr(u.ID), "repeater.unclaim", idPtr(u.ID), withVia(map[string]string{"pubkey": pk}, sess))
	s.writeRepeaterInfo(w, pk, u)
}

func (s *Server) handleAdminRepeaterUnclaim(w http.ResponseWriter, r *http.Request, admin *users.User, _ *users.Session) {
	pk, err := users.NormalizePubkey(mux.Vars(r)["pubkey"])
	if err != nil {
		writeError(w, http.StatusBadRequest, "pubkey must be 64 hex characters")
		return
	}
	uid, err := strconv.ParseInt(mux.Vars(r)["userId"], 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}
	err = s.auth.st.UnclaimRepeater(uid, pk)
	if errors.Is(err, users.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no such claim")
		return
	}
	if err != nil {
		log.Printf("[users] admin unclaim repeater %s for user #%d: %v", pk, uid, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	s.auth.auditAsync(idPtr(admin.ID), "repeater.unclaim", idPtr(uid), map[string]string{"pubkey": pk, "by": "admin"})
	w.WriteHeader(http.StatusNoContent)
}
