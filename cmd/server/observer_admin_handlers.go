package main

import (
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/users"
)

// adminObserverLoginJSON never carries credential material.
type adminObserverLoginJSON struct {
	UserID      int64    `json:"userId"`
	Email       string   `json:"email"`
	DisplayName string   `json:"displayName"`
	Username    string   `json:"username"`
	CreatedAt   string   `json:"createdAt"`
	Revoked     bool     `json:"revoked"`
	LastAuthAt  *string  `json:"lastAuthAt"`
	Observers   []string `json:"observers"`
}

type adminObserverBindingJSON struct {
	Pubkey  string `json:"pubkey"`
	UserID  *int64 `json:"userId"`
	BoundAt string `json:"boundAt"`
	BoundBy string `json:"boundBy"`
	// ConfirmedAt is null until the owner's login has published under the
	// pubkey, and for an orphan (the index never treats one as confirmed).
	ConfirmedAt *string `json:"confirmedAt"`
}

type adminObserverLoginsResponse struct {
	Logins   []adminObserverLoginJSON   `json:"logins"`
	Bindings []adminObserverBindingJSON `json:"bindings"`
}

type observerBindingRequest struct {
	UserID *int64 `json:"userId"`
}

func adminObserverFail(w http.ResponseWriter, op string, err error) {
	log.Printf("[observer-logins] admin %s: %v", op, err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func (s *Server) handleAdminObserverLogins(w http.ResponseWriter, _ *http.Request, _ *users.User, _ *users.Session) {
	a := s.auth
	logins, err := a.st.AllMQTTLogins()
	if err != nil {
		adminObserverFail(w, "list logins", err)
		return
	}
	bindings, err := a.st.ObserverBindings()
	if err != nil {
		adminObserverFail(w, "list bindings", err)
		return
	}
	ids := make([]int64, 0, len(logins))
	for _, l := range logins {
		ids = append(ids, l.UserID)
	}
	accounts, err := a.st.UsersByID(ids)
	if err != nil {
		adminObserverFail(w, "load users", err)
		return
	}
	owned := make(map[int64][]string, len(logins))
	out := adminObserverLoginsResponse{
		Logins:   make([]adminObserverLoginJSON, 0, len(logins)),
		Bindings: make([]adminObserverBindingJSON, 0, len(bindings)),
	}
	for _, b := range bindings {
		if b.UserID != nil {
			owned[*b.UserID] = append(owned[*b.UserID], b.Pubkey)
		}
		j := adminObserverBindingJSON{Pubkey: b.Pubkey, UserID: b.UserID, BoundAt: rfc3339(b.BoundAt), BoundBy: b.BoundBy}
		if b.UserID != nil && b.ConfirmedAt != nil {
			ts := rfc3339(*b.ConfirmedAt)
			j.ConfirmedAt = &ts
		}
		out.Bindings = append(out.Bindings, j)
	}
	for _, l := range logins {
		u := accounts[l.UserID]
		j := adminObserverLoginJSON{UserID: l.UserID, Email: u.Email, DisplayName: u.DisplayName, Username: l.Username,
			CreatedAt: rfc3339(l.CreatedAt), Revoked: l.RevokedAt != nil, Observers: owned[l.UserID]}
		if j.Observers == nil {
			j.Observers = []string{}
		}
		if l.LastAuthAt != nil {
			ts := rfc3339(*l.LastAuthAt)
			j.LastAuthAt = &ts
		}
		out.Logins = append(out.Logins, j)
	}
	writeJSON(w, out)
}

func (s *Server) handleAdminObserverLoginRevoke(w http.ResponseWriter, r *http.Request, actor *users.User, _ *users.Session) {
	a := s.auth
	if !a.observerFeature(w) {
		return
	}
	uid, ok := adminUserIDParam(w, r)
	if !ok {
		return
	}
	if err := a.st.RevokeMQTTLogin(uid); err != nil {
		if errors.Is(err, users.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user has no observer login")
			return
		}
		adminObserverFail(w, "revoke", err)
		return
	}
	a.obsIdx.revoke(uid)
	a.audit(idPtr(actor.ID), "observer.login.revoke", idPtr(uid), nil)
	writeJSON(w, okResponse{OK: true})
}

func (s *Server) handleAdminObserverLoginReissue(w http.ResponseWriter, r *http.Request, actor *users.User, _ *users.Session) {
	a := s.auth
	if !a.observerFeature(w) {
		return
	}
	t := s.adminTarget(w, r)
	if t == nil {
		return
	}
	if _, err := a.st.MQTTLoginForUser(t.ID); err != nil {
		if errors.Is(err, users.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user has no observer login")
			return
		}
		adminObserverFail(w, "get login", err)
		return
	}
	l, password, ok := a.newMQTTLogin(w, t.ID)
	if !ok {
		return
	}
	if err := a.st.ReissueMQTTLogin(l); err != nil {
		adminObserverFail(w, "reissue", err)
		return
	}
	a.obsIdx.setLogin(l)
	a.audit(idPtr(actor.ID), "observer.login.reissue", idPtr(t.ID), map[string]string{"username": l.Username})
	if !a.sendObserverCredentials(r.Context(), w, t, l.Username, password,
		"the login was reissued and the old one no longer works, but the mail could not be sent; the user can ask for it again from their account page") {
		return
	}
	writeJSON(w, sentResponse{Sent: true})
}

func (s *Server) handleAdminObserverBinding(w http.ResponseWriter, r *http.Request, actor *users.User, _ *users.Session) {
	a := s.auth
	if !a.observerFeature(w) {
		return
	}
	pk, err := users.NormalizeObserverKey(mux.Vars(r)["pubkey"])
	if err != nil {
		writeError(w, http.StatusBadRequest, "pubkey must be 64 hex characters")
		return
	}
	var req observerBindingRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	detail := map[string]string{"pubkey": pk, "userId": "none"}
	if req.UserID != nil {
		if _, err := a.st.GetByID(*req.UserID); err != nil {
			adminStoreFail(w, "load", *req.UserID, err)
			return
		}
		detail["userId"] = strconv.FormatInt(*req.UserID, 10)
	}
	detail["previousUserId"] = "none"
	if prev := a.obsIdx.ownerOf(pk); prev != nil {
		detail["previousUserId"] = strconv.FormatInt(*prev, 10)
	}
	if err := a.st.SetObserverOwner(pk, req.UserID); err != nil {
		adminObserverFail(w, "set binding", err)
		return
	}
	if req.UserID == nil {
		a.obsIdx.unbind(pk)
	} else {
		a.obsIdx.setOwner(pk, req.UserID)
	}
	a.audit(idPtr(actor.ID), "observer.binding.set", req.UserID, detail)
	writeJSON(w, okResponse{OK: true})
}

func adminUserIDParam(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(mux.Vars(r)["id"], 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return 0, false
	}
	return id, true
}
