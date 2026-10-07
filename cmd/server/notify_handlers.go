package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/meshcore-analyzer/users"
)

// Node notification routes (docs/specs/2026-10-07-node-notifications-design.md).
// Registered only when userManagement.notifications.enabled (see
// registerAuthRoutes).

type notifyPrefsRequest struct {
	Enabled bool     `json:"enabled"`
	Events  []string `json:"events"`
}

type notifyWatchJSON struct {
	Pubkey    string `json:"pubkey"`
	Name      string `json:"name"`  // "" when unnamed or no longer in the analyzer database
	Known     bool   `json:"known"` // false once the node left the analyzer database
	CreatedAt string `json:"createdAt"`
}

type notifyLimitsJSON struct {
	MaxWatches    int `json:"maxWatches"`
	PerUserPerDay int `json:"perUserPerDay"`
	MailsLast24h  int `json:"mailsLast24h"`
}

// notifyAccountJSON is the caller's notification state: GET answers it, and
// so does every change, so the client can replace its copy.
type notifyAccountJSON struct {
	Enabled         bool              `json:"enabled"`
	Events          []string          `json:"events"`
	AvailableEvents []string          `json:"availableEvents"`
	Watches         []notifyWatchJSON `json:"watches"`
	Limits          notifyLimitsJSON  `json:"limits"`
}

type watchMyNodesJSON struct {
	Added   int               `json:"added"`
	Already int               `json:"already"`
	Skipped int               `json:"skipped"` // malformed, not in the analyzer database, or over the limit
	Account notifyAccountJSON `json:"account"`
}

func (s *Server) registerNotifyRoutes(r *mux.Router) {
	r.HandleFunc("/api/account/notifications", s.withUser(s.handleNotifyGet)).Methods("GET")
	r.HandleFunc("/api/account/notifications", s.withUser(s.handleNotifyPut)).Methods("PUT")
	r.HandleFunc("/api/account/notifications/watches/{pubkey}", s.withUser(s.handleWatchPut)).Methods("PUT")
	r.HandleFunc("/api/account/notifications/watches/{pubkey}", s.withUser(s.handleWatchDelete)).Methods("DELETE")
	r.HandleFunc("/api/account/notifications/watch-my-nodes", s.withUser(s.handleWatchMyNodes)).Methods("POST")
	// No session: the link comes from a mail, and the provider's one-click
	// POST (List-Unsubscribe-Post) carries neither a cookie nor our Origin.
	r.HandleFunc("/api/notifications/unsubscribe", s.handleUnsubscribeGet).Methods("GET")
	r.HandleFunc("/api/notifications/unsubscribe", s.handleUnsubscribePost).Methods("POST")
}

var notifyPubkeyRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// notifyPubkey lowercases raw and reports whether it is a 64-hex-character key.
func notifyPubkey(raw string) (string, bool) {
	pk := strings.ToLower(strings.TrimSpace(raw))
	return pk, notifyPubkeyRE.MatchString(pk)
}

func availableNotifyEvents(u *users.User) []string {
	out := append([]string{}, users.NodeNotifyEvents...)
	if u.Role == users.RoleAdmin {
		out = append(out, users.AdminNotifyEvents...)
	}
	return out
}

func notifyInternal(w http.ResponseWriter, op string, uid int64, err error) {
	log.Printf("[notify] %s for user #%d: %v", op, uid, err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

// notifyAccount builds the caller's state; it creates the default
// preferences row on first use. A demoted admin's stored admin events are
// not shown (and are not evaluated).
func (s *Server) notifyAccount(u *users.User) (notifyAccountJSON, error) {
	n, st, ns := s.auth.notify, s.auth.st, s.auth.set.notify
	p, err := st.NotifyPrefsFor(u.ID)
	if err != nil {
		return notifyAccountJSON{}, err
	}
	ws, err := st.WatchesFor(u.ID)
	if err != nil {
		return notifyAccountJSON{}, err
	}
	pks := make([]string, len(ws))
	for i, w := range ws {
		pks[i] = w.Pubkey
	}
	nodes, err := n.src.nodes(pks, false)
	if err != nil {
		return notifyAccountJSON{}, err
	}
	_, perUser, err := st.NotifyMailCounts(n.now().Add(-24 * time.Hour))
	if err != nil {
		return notifyAccountJSON{}, err
	}
	out := notifyAccountJSON{Enabled: p.Enabled, Events: []string{}, AvailableEvents: availableNotifyEvents(u),
		Watches: make([]notifyWatchJSON, 0, len(ws)),
		Limits:  notifyLimitsJSON{MaxWatches: ns.maxWatchesPerUser, PerUserPerDay: ns.perUserPerDay, MailsLast24h: perUser[u.ID]}}
	for _, e := range p.Events {
		if u.Role == users.RoleAdmin || !users.IsAdminNotifyEvent(e) {
			out.Events = append(out.Events, e)
		}
	}
	for _, w := range ws {
		nd, known := nodes[w.Pubkey]
		out.Watches = append(out.Watches, notifyWatchJSON{Pubkey: w.Pubkey, Name: nd.Name, Known: known, CreatedAt: rfc3339(w.CreatedAt)})
	}
	return out, nil
}

func (s *Server) writeNotifyAccount(w http.ResponseWriter, u *users.User) {
	out, err := s.notifyAccount(u)
	if err != nil {
		notifyInternal(w, "account state", u.ID, err)
		return
	}
	writeJSON(w, out)
}

func (s *Server) handleNotifyGet(w http.ResponseWriter, _ *http.Request, u *users.User, _ *users.Session) {
	s.writeNotifyAccount(w, u)
}

func (s *Server) handleNotifyPut(w http.ResponseWriter, r *http.Request, u *users.User, _ *users.Session) {
	var req notifyPrefsRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	for _, e := range req.Events {
		if !users.ValidNotifyEvent(e) {
			writeError(w, http.StatusBadRequest, "unknown event type")
			return
		}
		if users.IsAdminNotifyEvent(e) && u.Role != users.RoleAdmin {
			writeError(w, http.StatusForbidden, "only admins can choose this event")
			return
		}
	}
	p, err := s.auth.st.SetNotifyPrefs(u.ID, req.Enabled, req.Events)
	if err != nil {
		notifyInternal(w, "save preferences", u.ID, err)
		return
	}
	s.auth.audit(idPtr(u.ID), "notify.prefs", idPtr(u.ID),
		map[string]string{"enabled": strconv.FormatBool(p.Enabled), "events": strings.Join(p.Events, ",")})
	s.writeNotifyAccount(w, u)
}

func (s *Server) handleWatchPut(w http.ResponseWriter, r *http.Request, u *users.User, _ *users.Session) {
	pk, ok := notifyPubkey(mux.Vars(r)["pubkey"])
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid node key: expected 64 hex characters")
		return
	}
	nodes, err := s.auth.notify.src.nodes([]string{pk}, false)
	if err != nil {
		notifyInternal(w, "look up node", u.ID, err)
		return
	}
	if _, found := nodes[pk]; !found {
		writeError(w, http.StatusNotFound, "node not found")
		return
	}
	if _, err := s.auth.st.NotifyPrefsFor(u.ID); err != nil {
		notifyInternal(w, "preferences", u.ID, err)
		return
	}
	limit := s.auth.set.notify.maxWatchesPerUser
	if err := s.auth.st.AddWatch(u.ID, pk, limit); err != nil {
		if errors.Is(err, users.ErrWatchLimit) {
			writeError(w, http.StatusConflict, fmt.Sprintf("you watch the maximum of %d nodes; remove one first", limit))
			return
		}
		notifyInternal(w, "add watch", u.ID, err)
		return
	}
	s.writeNotifyAccount(w, u)
}

func (s *Server) handleWatchDelete(w http.ResponseWriter, r *http.Request, u *users.User, _ *users.Session) {
	pk, ok := notifyPubkey(mux.Vars(r)["pubkey"])
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid node key: expected 64 hex characters")
		return
	}
	if err := s.auth.st.RemoveWatch(u.ID, pk); err != nil {
		notifyInternal(w, "remove watch", u.ID, err)
		return
	}
	s.writeNotifyAccount(w, u)
}

// myNodesPubkeys returns the pubkeys of the synced "meshcore-my-nodes" list
// (settings sync; [{pubkey, name, addedAt}]) in list order; nil when the
// document or the key is absent or does not decode.
func myNodesPubkeys(doc string) []string {
	if doc == "" {
		return nil
	}
	var d settingsDoc
	if json.Unmarshal([]byte(doc), &d) != nil {
		return nil
	}
	raw, ok := d.Keys["meshcore-my-nodes"]
	if !ok {
		return nil
	}
	var items []struct {
		Pubkey string `json:"pubkey"`
	}
	if json.Unmarshal([]byte(raw), &items) != nil {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Pubkey)
	}
	return out
}

func (s *Server) handleWatchMyNodes(w http.ResponseWriter, _ *http.Request, u *users.User, _ *users.Session) {
	st := s.auth.st
	doc, _, err := st.GetSettings(u.ID)
	if err != nil {
		notifyInternal(w, "read synced settings", u.ID, err)
		return
	}
	var res watchMyNodesJSON
	seen := map[string]bool{}
	var valid []string
	for _, raw := range myNodesPubkeys(doc) {
		pk, ok := notifyPubkey(raw)
		if !ok {
			res.Skipped++
			continue
		}
		if !seen[pk] {
			seen[pk] = true
			valid = append(valid, pk)
		}
	}
	nodes, err := s.auth.notify.src.nodes(valid, false)
	if err != nil {
		notifyInternal(w, "look up nodes", u.ID, err)
		return
	}
	known := make([]string, 0, len(valid))
	for _, pk := range valid {
		if _, ok := nodes[pk]; ok {
			known = append(known, pk)
		} else {
			res.Skipped++
		}
	}
	if _, err := st.NotifyPrefsFor(u.ID); err != nil {
		notifyInternal(w, "preferences", u.ID, err)
		return
	}
	added, already, over, err := st.AddWatches(u.ID, known, s.auth.set.notify.maxWatchesPerUser)
	if err != nil {
		notifyInternal(w, "add watches", u.ID, err)
		return
	}
	res.Added, res.Already, res.Skipped = added, already, res.Skipped+over
	if res.Account, err = s.notifyAccount(u); err != nil {
		notifyInternal(w, "account state", u.ID, err)
		return
	}
	writeJSON(w, res)
}

// handleUnsubscribeGet sends a mail client or a scanner that follows the
// List-Unsubscribe URL to the confirm view. A GET never changes anything.
func (s *Server) handleUnsubscribeGet(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, s.auth.link("unsubscribe", r.URL.Query().Get("token")), http.StatusSeeOther)
}

// handleUnsubscribePost turns notifications off for the token's owner: the
// confirm button and the provider's one-click POST. The token only does this.
func (s *Server) handleUnsubscribePost(w http.ResponseWriter, r *http.Request) {
	uid, was, err := s.auth.st.DisableNotifyByToken(r.URL.Query().Get("token"))
	if err != nil {
		if !errors.Is(err, users.ErrTokenInvalid) {
			log.Printf("[notify] unsubscribe: %v", err)
		}
		writeTokenError(w, err)
		return
	}
	if was {
		s.auth.audit(idPtr(uid), "notify.unsubscribe", idPtr(uid), map[string]string{"via": "link"})
	}
	writeJSON(w, okResponse{OK: true, Message: "Notifications are off. Turn them back on from your account page."})
}
