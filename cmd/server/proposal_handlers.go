package main

import (
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/channel"
	"github.com/meshcore-analyzer/users"
)

// Proposals and approved hashtag channels
// (docs/specs/2026-10-07-channel-proposals-design.md). The routes exist only
// when userManagement.channelProposals.enabled (see registerAuthRoutes).

const (
	maxProposalNote   = 500
	proposalRetention = 90 * 24 * time.Hour // rejected and revoked rows, from the decision
)

type proposalRequest struct {
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
}

type decideRequest struct {
	Note string `json:"note"`
}

type proposalJSON struct {
	ID        int64                `json:"id"`
	Kind      string               `json:"kind"`
	Subject   string               `json:"subject"`
	Status    users.ProposalStatus `json:"status"`
	Note      string               `json:"note"`
	CreatedAt string               `json:"createdAt"`
	DecidedAt *string              `json:"decidedAt"`
}

// adminProposalJSON adds who proposed and who decided; only admins see it.
type adminProposalJSON struct {
	proposalJSON
	Proposer *auditUserRef `json:"proposer"`
	Reviewer *auditUserRef `json:"reviewer"`
}

func (s *Server) registerProposalRoutes(r *mux.Router) {
	r.HandleFunc("/api/proposals", s.withUser(s.proposalCreateHandler(s.cfg.configuredChannelNames()))).Methods("POST")
	r.HandleFunc("/api/account/proposals", s.withUser(s.handleAccountProposals)).Methods("GET")
	r.HandleFunc("/api/admin/proposals", s.withAdmin(s.handleAdminProposals)).Methods("GET")
	r.HandleFunc("/api/admin/proposals/{id}/approve", s.withAdmin(s.decideHandler(users.ProposalApprove))).Methods("POST")
	r.HandleFunc("/api/admin/proposals/{id}/reject", s.withAdmin(s.decideHandler(users.ProposalReject))).Methods("POST")
	r.HandleFunc("/api/admin/proposals/{id}/revoke", s.withAdmin(s.decideHandler(users.ProposalRevoke))).Methods("POST")
}

func proposalToJSON(p users.Proposal) proposalJSON {
	return proposalJSON{ID: p.ID, Kind: p.Kind, Subject: p.Subject, Status: p.Status, Note: p.Note,
		CreatedAt: rfc3339(p.CreatedAt), DecidedAt: rfc3339Ptr(p.DecidedAt)}
}

// proposalUserIDs lists the distinct proposer and reviewer ids.
func proposalUserIDs(list []users.Proposal) []int64 {
	seen := map[int64]bool{}
	var ids []int64
	for _, p := range list {
		for _, id := range []*int64{p.ProposerID, p.ReviewerID} {
			if id != nil && !seen[*id] {
				seen[*id] = true
				ids = append(ids, *id)
			}
		}
	}
	return ids
}

func (s *Server) adminProposalRows(list []users.Proposal) ([]adminProposalJSON, error) {
	known, err := s.auth.st.UsersByID(proposalUserIDs(list))
	if err != nil {
		return nil, err
	}
	out := make([]adminProposalJSON, 0, len(list))
	for _, p := range list {
		out = append(out, adminProposalJSON{proposalJSON: proposalToJSON(p),
			Proposer: auditRef(p.ProposerID, known), Reviewer: auditRef(p.ReviewerID, known)})
	}
	return out, nil
}

// validateProposalNote trims a reviewer's note and enforces at most 500
// characters without control or invisible characters (newline and ZWJ allowed).
func validateProposalNote(raw string) (string, error) {
	n := strings.TrimSpace(raw)
	if utf8.RuneCountInString(n) > maxProposalNote {
		return "", &users.ValidationError{Msg: "the note is at most 500 characters"}
	}
	for _, r := range n {
		if r == '\n' || r == '\u200d' {
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return "", &users.ValidationError{Msg: "the note contains invisible or control characters"}
		}
	}
	return n, nil
}

// writeProposalError answers a failed store call; unknown errors are logged
// and answered 500.
func writeProposalError(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, users.ErrNotFound):
		writeError(w, http.StatusNotFound, "proposal not found")
	case errors.Is(err, users.ErrProposalPending):
		writeError(w, http.StatusConflict, "this channel is already proposed and waits for review")
	case errors.Is(err, users.ErrProposalApproved):
		writeError(w, http.StatusConflict, "this channel is already approved")
	case errors.Is(err, users.ErrProposalRejected):
		writeError(w, http.StatusConflict, "this channel was rejected by an admin")
	case errors.Is(err, users.ErrProposalTransition):
		writeError(w, http.StatusConflict, "not possible in the proposal's current state")
	case errors.Is(err, users.ErrProposalApprovedLimit):
		writeError(w, http.StatusConflict, "the limit of approved channels is reached; revoke one first")
	case errors.Is(err, users.ErrProposalUserLimit):
		writeError(w, http.StatusTooManyRequests, "you reached the daily proposal limit; try again tomorrow")
	case errors.Is(err, users.ErrProposalPendingLimit):
		writeError(w, http.StatusTooManyRequests, "too many proposals wait for review; try again later")
	default:
		log.Printf("[users] proposal %s: %v", op, err)
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

// proposalCreateHandler serves POST /api/proposals. configured holds the
// channel names config.json already decrypts (Config.configuredChannelNames),
// built once at route registration; proposing one of them is refused.
func (s *Server) proposalCreateHandler(configured map[string]bool) authedHandler {
	return func(w http.ResponseWriter, r *http.Request, u *users.User, _ *users.Session) {
		var req proposalRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		if req.Kind != users.KindHashtagChannel {
			writeError(w, http.StatusBadRequest, "unsupported proposal kind")
			return
		}
		name, err := channel.ValidateHashtagName(req.Subject)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if configured[name] {
			writeError(w, http.StatusConflict, "this channel is already decrypted on this instance")
			return
		}
		ps := s.auth.set.proposals
		p, err := s.auth.st.Propose(req.Kind, name, u.ID, users.ProposalLimits{MaxPending: ps.maxPending, PerUserPerDay: ps.perUserPerDay})
		if err != nil {
			writeProposalError(w, "create", err)
			return
		}
		s.auth.audit(idPtr(u.ID), "proposal.create", nil, map[string]string{"kind": p.Kind, "subject": p.Subject})
		writeJSONStatus(w, http.StatusCreated, proposalToJSON(*p))
	}
}

func (s *Server) handleAccountProposals(w http.ResponseWriter, _ *http.Request, u *users.User, _ *users.Session) {
	list, err := s.auth.st.ProposalsByUser(u.ID)
	if err != nil {
		log.Printf("[users] list proposals for user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := make([]proposalJSON, 0, len(list))
	for _, p := range list {
		out = append(out, proposalToJSON(p))
	}
	writeJSON(w, out)
}

func (s *Server) handleAdminProposals(w http.ResponseWriter, r *http.Request, _ *users.User, _ *users.Session) {
	q := r.URL.Query()
	f := users.ProposalFilter{Status: users.ProposalStatus(q.Get("status")), Kind: q.Get("kind")}
	if f.Status != "" && !f.Status.Valid() {
		writeError(w, http.StatusBadRequest, "invalid status filter")
		return
	}
	if f.Kind != "" && f.Kind != users.KindHashtagChannel {
		writeError(w, http.StatusBadRequest, "invalid kind filter")
		return
	}
	list, err := s.auth.st.ListProposals(f)
	if err != nil {
		writeProposalError(w, "list", err)
		return
	}
	out, err := s.adminProposalRows(list)
	if err != nil {
		writeProposalError(w, "list users", err)
		return
	}
	writeJSON(w, out)
}

// decideHandler serves POST /api/admin/proposals/{id}/<action>, body {note}.
func (s *Server) decideHandler(action users.ProposalAction) authedHandler {
	return func(w http.ResponseWriter, r *http.Request, admin *users.User, _ *users.Session) {
		id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusBadRequest, "invalid proposal id")
			return
		}
		var req decideRequest
		if !decodeJSON(w, r, &req) {
			return
		}
		note, err := validateProposalNote(req.Note)
		if writeValidation(w, err) {
			return
		}
		p, err := s.auth.st.Decide(id, action, admin.ID, note, s.auth.set.proposals.maxApproved)
		if err != nil {
			writeProposalError(w, string(action), err)
			return
		}
		s.auth.audit(idPtr(admin.ID), "proposal."+string(action), p.ProposerID, map[string]string{"kind": p.Kind, "subject": p.Subject})
		s.auth.refreshApproved()
		rows, err := s.adminProposalRows([]users.Proposal{*p})
		if err != nil {
			writeProposalError(w, "reload", err)
			return
		}
		writeJSON(w, rows[0])
	}
}
