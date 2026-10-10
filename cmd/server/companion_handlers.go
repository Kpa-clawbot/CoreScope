package main

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/mailer"
	"github.com/meshcore-analyzer/sigvalidate"
	"github.com/meshcore-analyzer/users"
)

// linkMessage is what a companion signs to prove it holds its key. The
// instance's public host is part of it, so a signature cannot be replayed
// against another CoreScope.
func (a *authService) linkMessage(challenge string) []byte {
	return []byte("corescope-link:" + a.set.baseURL.Host + ":" + challenge)
}

func (a *authService) allowCompanion(w http.ResponseWriter, r *http.Request, u *users.User) bool {
	return a.allow(w, r, a.companion, "user:"+strconv.FormatInt(u.ID, 10))
}

func (s *Server) handleCompanionChallenge(w http.ResponseWriter, r *http.Request, u *users.User, _ *users.Session) {
	a := s.auth
	if !a.allowCompanion(w, r, u) {
		return
	}
	var req companionChallengeRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ch, exp, err := a.st.CreateLinkChallenge(u.ID, req.Pubkey)
	if errors.Is(err, users.ErrBadPubkey) {
		writeError(w, http.StatusBadRequest, "pubkey must be 64 hex characters")
		return
	}
	if err != nil {
		log.Printf("[users] link challenge for user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, companionChallengeResponse{Challenge: ch, ExpiresAt: rfc3339(exp), Host: a.set.baseURL.Host})
}

func (s *Server) handleCompanionLink(w http.ResponseWriter, r *http.Request, u *users.User, _ *users.Session) {
	a := s.auth
	if !a.allowCompanion(w, r, u) {
		return
	}
	var req companionLinkRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// The challenge is consumed before anything else can fail, so a refused
	// attempt never leaves a usable challenge behind.
	pk, pkErr := users.NormalizePubkey(req.Pubkey)
	chErr := a.st.ConsumeLinkChallenge(u.ID, req.Pubkey, req.Challenge)
	if pkErr != nil {
		writeError(w, http.StatusBadRequest, "pubkey must be 64 hex characters")
		return
	}
	switch {
	case chErr == nil:
	case errors.Is(chErr, users.ErrChallengeMissing), errors.Is(chErr, users.ErrChallengeExpired), errors.Is(chErr, users.ErrChallengeMismatch):
		writeError(w, http.StatusGone, "challenge expired or already used, request a new one")
		return
	default:
		log.Printf("[users] consume link challenge for user #%d: %v", u.ID, chErr)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	pub, _ := hex.DecodeString(pk)
	sig, err := hex.DecodeString(strings.TrimSpace(req.Signature))
	if err != nil || len(sig) != 64 {
		writeError(w, http.StatusBadRequest, "signature must be 64 bytes as hex")
		return
	}
	if ok, err := sigvalidate.VerifyMessage(pub, sig, a.linkMessage(req.Challenge)); err != nil || !ok {
		writeError(w, http.StatusBadRequest, "signature does not verify for this pubkey")
		return
	}
	// The previous owner's own name for the companion, for the transfer
	// mail: the new owner's label is theirs and is not passed on.
	var prevName string
	if old, err := a.st.GetCompanionLink(pk); err == nil && old.UserID != u.ID {
		prevName = old.Name
	}
	link, prev, err := a.st.UpsertCompanionLink(u.ID, pk, req.Name)
	if err != nil {
		log.Printf("[users] link companion for user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	a.companionLinked(u, link, prev, prevName)
	writeJSON(w, companionLinkResponse{Pubkey: link.Pubkey, Name: link.Name, LinkedAt: rfc3339(link.LinkedAt)})
}

// companionTransferMailPurpose labels the transfer mail in mail_log.
const companionTransferMailPurpose = "companion.transfer"

// companionLinked writes the audit rows of a link. For a transfer (prev is
// the previous owner, prevName their name for the companion) both users
// get a companion.transfer row and the previous owner a mail. Everything
// runs in the background, tracked by auditWG so waitAudits (tests,
// shutdown) covers the mail too.
func (a *authService) companionLinked(u *users.User, link *users.CompanionLink, prev int64, prevName string) {
	if prev == 0 {
		a.auditAsync(idPtr(u.ID), "companion.link", idPtr(u.ID), map[string]string{"pubkey": link.Pubkey})
		return
	}
	detail := map[string]string{"pubkey": link.Pubkey, "from": strconv.FormatInt(prev, 10), "to": strconv.FormatInt(u.ID, 10)}
	a.auditAsync(idPtr(u.ID), "companion.transfer", idPtr(u.ID), detail)
	a.auditAsync(idPtr(u.ID), "companion.transfer", idPtr(prev), detail)
	a.auditWG.Add(1)
	go func() {
		defer a.auditWG.Done()
		a.mailCompanionTransfer(prev, link.Pubkey, prevName)
	}()
}

// mailCompanionTransfer tells the previous owner that their companion now
// belongs to another account. It is a security notice, so node
// notification settings do not apply; only an inactive account or a
// bouncing address gets no mail.
func (a *authService) mailCompanionTransfer(prevID int64, pubkey, name string) {
	prev, err := a.st.GetByID(prevID)
	if err != nil || prev.Status != users.StatusActive || prev.EmailBouncing {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), notifySendTimeout)
	defer cancel()
	_ = a.sendMail(ctx, prev, companionTransferMailPurpose, a.companionTransferMail(prev, pubkey, name)) // failure logged by sendMail
}

func (a *authService) companionTransferMail(u *users.User, pubkey, name string) mailer.Message {
	what := pubkey[:12]
	if name := mailSafeText(name); name != "" {
		what = name + " (" + what + ")"
	}
	return a.render(u.Email, u.DisplayName, "companion", mailContent{
		subject:  "Your companion was linked to another account",
		greeting: "Hello " + u.DisplayName + ",",
		paragraphs: []string{
			"Your companion " + what + " was just linked to another account on " + a.set.baseURL.Host +
				". That account proved it holds the companion's private key, so the companion and its coverage now count for that account, not yours.",
			"If you passed the companion on, nothing needs to be done. If not, its key is in someone else's hands.",
		},
		actionLabel: "Your companions", actionURL: a.set.baseURL.String() + "/#/account?section=companions",
	})
}

func (s *Server) handleCompanionList(w http.ResponseWriter, _ *http.Request, u *users.User, _ *users.Session) {
	links, err := s.auth.st.ListCompanionLinks(u.ID)
	if err != nil {
		log.Printf("[users] list companions for user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, s.companionsJSON(links))
}

func (s *Server) handleCompanionDelete(w http.ResponseWriter, r *http.Request, u *users.User, _ *users.Session) {
	raw := mux.Vars(r)["pubkey"]
	switch err := s.auth.st.DeleteCompanionLink(u.ID, raw); {
	case err == nil:
	case errors.Is(err, users.ErrBadPubkey):
		writeError(w, http.StatusBadRequest, "pubkey must be 64 hex characters")
		return
	case errors.Is(err, users.ErrNotFound):
		writeError(w, http.StatusNotFound, "companion not linked to this account")
		return
	default:
		log.Printf("[users] unlink companion for user #%d: %v", u.ID, err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	pk, _ := users.NormalizePubkey(raw)
	s.auth.auditAsync(idPtr(u.ID), "companion.unlink", idPtr(u.ID), map[string]string{"pubkey": pk})
	w.WriteHeader(http.StatusNoContent)
}

// companionsJSON renders links with their last reception; always a
// non-nil slice.
func (s *Server) companionsJSON(links []users.CompanionLink) []companionJSON {
	pks := make([]string, 0, len(links))
	for _, l := range links {
		pks = append(pks, l.Pubkey)
	}
	seen := s.companionLastSeen(pks)
	out := make([]companionJSON, 0, len(links))
	for _, l := range links {
		c := companionJSON{Pubkey: l.Pubkey, Name: l.Name, LinkedAt: rfc3339(l.LinkedAt)}
		if at, ok := seen[l.Pubkey]; ok {
			c.LastSeenAt = &at
		}
		out = append(out, c)
	}
	return out
}

// companionLastSeen returns the newest client_receptions.rx_at per pubkey
// from the analyzer DB. It is informational: no DB, no table (client RX
// coverage never enabled) or a failed query give an empty map.
func (s *Server) companionLastSeen(pks []string) map[string]string {
	out := map[string]string{}
	if len(pks) == 0 || s.db == nil || s.db.conn == nil {
		return out
	}
	args := make([]interface{}, len(pks))
	for i, pk := range pks {
		args[i] = pk
	}
	rows, err := s.db.conn.Query(`SELECT rx_pubkey, MAX(rx_at) FROM client_receptions WHERE rx_pubkey IN (`+
		sqlPlaceholders(len(pks))+`) GROUP BY rx_pubkey`, args...)
	if err != nil {
		if !strings.Contains(err.Error(), "no such table") {
			log.Printf("[users] companion last seen: %v", err)
		}
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var pk string
		var at sql.NullString
		if rows.Scan(&pk, &at) == nil && at.Valid {
			out[strings.ToLower(pk)] = at.String
		}
	}
	return out
}
