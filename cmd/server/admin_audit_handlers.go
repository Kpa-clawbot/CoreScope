package main

import (
	"errors"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/meshcore-analyzer/users"
)

// auditUserRef names an actor or target. Deleted accounts keep only the id.
type auditUserRef struct {
	ID          int64  `json:"id"`
	DisplayName string `json:"displayName,omitempty"`
	Email       string `json:"email,omitempty"`
	Deleted     bool   `json:"deleted,omitempty"`
}

type auditListEntryJSON struct {
	ID     int64             `json:"id"`
	At     string            `json:"at"`
	Action string            `json:"action"`
	Actor  *auditUserRef     `json:"actor"`
	Target *auditUserRef     `json:"target"`
	Detail map[string]string `json:"detail"`
}

type auditListJSON struct {
	Entries []auditListEntryJSON `json:"entries"`
	Next    *int64               `json:"next"`
}

// One action ("user.login.failed") or a group ending in ".*" ("user.login.*").
var auditActionRE = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*(\.\*)?$`)

func parseAuditTime(q url.Values, name string) (*time.Time, error) {
	v := q.Get(name)
	if v == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return nil, errors.New("invalid " + name + ": use RFC 3339")
	}
	return &t, nil
}

func parsePositiveID(v, name string) (int64, error) {
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid " + name)
	}
	return id, nil
}

// parseAuditFilter reads the query of GET /api/admin/audit. Any invalid
// parameter is an error (answered 400); limit above the cap is capped.
func parseAuditFilter(q url.Values) (users.AuditFilter, error) {
	f := users.AuditFilter{Limit: 100}
	if a := q.Get("action"); a != "" {
		if len(a) > 64 || !auditActionRE.MatchString(a) {
			return f, errors.New("invalid action filter")
		}
		f.Actions = []string{a}
	}
	if v := q.Get("user"); v != "" {
		id, err := parsePositiveID(v, "user filter")
		if err != nil {
			return f, err
		}
		f.UserID = &id
	}
	var err error
	if f.From, err = parseAuditTime(q, "from"); err != nil {
		return f, err
	}
	if f.To, err = parseAuditTime(q, "to"); err != nil {
		return f, err
	}
	if f.From != nil && f.To != nil && f.From.After(*f.To) {
		return f, errors.New("from is after to")
	}
	if v := q.Get("before"); v != "" {
		if f.BeforeID, err = parsePositiveID(v, "before"); err != nil {
			return f, err
		}
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			return f, errors.New("invalid limit")
		}
		if n > users.AuditListMax {
			n = users.AuditListMax
		}
		f.Limit = n
	}
	return f, nil
}

// auditUserIDs lists the distinct actor and target ids of a page.
func auditUserIDs(list []users.AuditEntry) []int64 {
	seen := map[int64]bool{}
	var ids []int64
	for _, e := range list {
		for _, p := range []*int64{e.ActorUserID, e.TargetUserID} {
			if p != nil && !seen[*p] {
				seen[*p] = true
				ids = append(ids, *p)
			}
		}
	}
	return ids
}

func auditRef(id *int64, known map[int64]users.User) *auditUserRef {
	if id == nil {
		return nil
	}
	u, ok := known[*id]
	if !ok {
		return &auditUserRef{ID: *id, Deleted: true}
	}
	return &auditUserRef{ID: u.ID, DisplayName: u.DisplayName, Email: u.Email}
}

func (s *Server) handleAdminAudit(w http.ResponseWriter, r *http.Request, _ *users.User, _ *users.Session) {
	f, err := parseAuditFilter(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	list, err := s.auth.st.AuditList(f)
	if err != nil {
		log.Printf("[users] admin audit list: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	known, err := s.auth.st.UsersByID(auditUserIDs(list))
	if err != nil {
		log.Printf("[users] admin audit users: %v", err)
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	out := auditListJSON{Entries: make([]auditListEntryJSON, 0, len(list))}
	for _, e := range list {
		out.Entries = append(out.Entries, auditListEntryJSON{ID: e.ID, At: rfc3339(e.At), Action: e.Action,
			Actor: auditRef(e.ActorUserID, known), Target: auditRef(e.TargetUserID, known), Detail: e.Detail})
	}
	if len(list) == f.Limit {
		next := list[len(list)-1].ID
		out.Next = &next
	}
	writeJSON(w, out)
}
